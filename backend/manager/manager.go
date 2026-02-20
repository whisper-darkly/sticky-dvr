// Package manager orchestrates source workers via sticky-overseer.
//
// Each source with ≥1 active subscription maps to one overseer Task.
// The overseer handles restart delays and error thresholds via RetryPolicy.
// The manager:
//   - On startup: loads active sources from store, reconciles with overseer.
//   - Receives overseer events (started/exited/errored) and records them in the
//     worker_events table for display in the UI.
//   - On posture changes: starts/stops the overseer task when the active
//     subscriber count for a source crosses 0.
package manager

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/whisper-darkly/sticky-dvr/backend/config"
	"github.com/whisper-darkly/sticky-dvr/backend/overseer"
	"github.com/whisper-darkly/sticky-dvr/backend/store"
)

const maxLogs = 200

// sourceState holds in-memory runtime state for one source.
type sourceState struct {
	source *store.Source
	mu     sync.Mutex
	pid    int
	// workerState mirrors what the overseer reports: idle | starting | running | errored
	workerState  string
	errorMessage string
	logs         []string
}

func (s *sourceState) addLog(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.logs) >= maxLogs {
		s.logs = s.logs[1:]
	}
	s.logs = append(s.logs, line)
}

func (s *sourceState) getLogs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.logs))
	copy(out, s.logs)
	return out
}

// SubscriptionStatus is the API-facing combined view of a source + subscription
// + runtime state, scoped to one user's subscription.
type SubscriptionStatus struct {
	// Source identity
	Driver   string `json:"driver"`
	Username string `json:"username"`
	SourceID int64  `json:"source_id"`

	// Subscription
	SubID     int64         `json:"sub_id"`
	UserID    int64         `json:"user_id"`
	Posture   store.Posture `json:"posture"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`

	// Runtime (from overseer)
	WorkerState  string   `json:"worker_state"` // idle | starting | running | errored
	PID          int      `json:"pid,omitempty"`
	ErrorMessage string   `json:"error_message,omitempty"`
	Logs         []string `json:"logs"`
}

// Manager orchestrates source workers.
type Manager struct {
	mu      sync.RWMutex
	states  map[int64]*sourceState // sourceID → runtime state
	taskIdx map[string]int64       // overseer task_id → sourceID

	cfg *config.Global
	st  store.Store
	oc  *overseer.Client
	ctx context.Context
}

// New creates a Manager. Call SetOverseerClient then Start before use.
func New(cfg *config.Global, st store.Store) *Manager {
	return &Manager{
		states:  make(map[int64]*sourceState),
		taskIdx: make(map[string]int64),
		cfg:     cfg,
		st:      st,
	}
}

// SetOverseerClient wires in the overseer client. Must be called before Start.
func (m *Manager) SetOverseerClient(oc *overseer.Client) {
	m.oc = oc
}

// GetOverseerClient returns the overseer client (may be nil).
func (m *Manager) GetOverseerClient() *overseer.Client { return m.oc }

// Start loads active sources, reconciles with the overseer, and launches
// the periodic reconciler.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx = ctx

	// Load all active subscriptions and find unique sources.
	subs, err := m.st.ListActiveSubscriptions(ctx)
	if err != nil {
		return fmt.Errorf("list active subscriptions: %w", err)
	}

	seen := make(map[int64]bool)
	for _, sub := range subs {
		if seen[sub.SourceID] {
			continue
		}
		seen[sub.SourceID] = true

		// We need the source object to know driver/username/task_id.
		// We'll build states lazily below; for now just pre-populate sourceIDs.
	}

	// Fetch sources for all unique source IDs.
	sources, err := m.st.ListSources(ctx)
	if err != nil {
		return fmt.Errorf("list sources: %w", err)
	}

	m.mu.Lock()
	for _, src := range sources {
		if !seen[src.ID] {
			continue
		}
		state := &sourceState{source: src, workerState: "idle"}
		m.states[src.ID] = state
		if src.OverseerTaskID != "" {
			m.taskIdx[src.OverseerTaskID] = src.ID
		}
	}
	m.mu.Unlock()

	// Reconcile with running overseer tasks.
	m.reconcileStartup(ctx)

	go m.reconcileLoop(ctx)
	return nil
}

// reconcileStartup claims already-running overseer tasks and starts workers
// for active sources that have no task running.
func (m *Manager) reconcileStartup(ctx context.Context) {
	tasks, err := m.oc.List(ctx)
	if err != nil {
		log.Printf("manager: startup overseer list failed (will start fresh): %v", err)
	}

	// Build task_id → TaskInfo map for quick lookup.
	byTaskID := make(map[string]overseer.TaskInfo, len(tasks))
	for _, t := range tasks {
		byTaskID[t.TaskID] = t
	}

	m.mu.RLock()
	ids := make([]int64, 0, len(m.states))
	for id := range m.states {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, id := range ids {
		state := m.stateByID(id)
		if state == nil {
			continue
		}
		state.mu.Lock()
		taskID := state.source.OverseerTaskID
		driver := state.source.Driver
		username := state.source.Username
		state.mu.Unlock()

		if taskID != "" {
			if t, ok := byTaskID[taskID]; ok && t.WorkerState == "running" {
				state.mu.Lock()
				state.pid = t.CurrentPID
				state.workerState = "running"
				state.mu.Unlock()
				log.Printf("manager: claimed existing task=%s pid=%d for %s/%s", taskID, t.CurrentPID, driver, username)
				state.addLog(fmt.Sprintf("[system] claimed existing worker task=%s pid=%d", taskID, t.CurrentPID))
				continue
			}
		}
		// No running task — start one.
		go m.startWorker(id)
	}
}

// ---- overseer event callbacks ----

// OnStarted records a started event and updates in-memory state.
func (m *Manager) OnStarted(taskID string, pid int, restartOf int, ts time.Time) {
	sourceID := m.sourceIDByTask(taskID)
	if sourceID == 0 {
		return
	}
	state := m.stateByID(sourceID)
	if state == nil {
		return
	}

	state.mu.Lock()
	state.pid = pid
	state.workerState = "running"
	state.mu.Unlock()

	if restartOf > 0 {
		state.addLog(fmt.Sprintf("[system] restarted (pid=%d, was %d)", pid, restartOf))
	} else {
		state.addLog(fmt.Sprintf("[system] started (pid=%d)", pid))
	}

	if err := m.st.RecordWorkerEvent(context.Background(), sourceID, pid, store.EventStarted, nil); err != nil {
		log.Printf("manager: record started event source=%d: %v", sourceID, err)
	}
}

// OnOutput routes a stdout/stderr line to the source's log buffer.
func (m *Manager) OnOutput(taskID string, pid int, stream, data string, ts time.Time) {
	sourceID := m.sourceIDByTask(taskID)
	if sourceID == 0 {
		return
	}
	state := m.stateByID(sourceID)
	if state == nil {
		return
	}
	state.addLog(fmt.Sprintf("[%s] %s", stream, data))
}

// OnExited handles process exit.
func (m *Manager) OnExited(taskID string, pid int, exitCode int, intentional bool, ts time.Time) {
	sourceID := m.sourceIDByTask(taskID)
	if sourceID == 0 {
		return
	}
	state := m.stateByID(sourceID)
	if state == nil {
		return
	}

	state.mu.Lock()
	if state.pid == pid {
		state.pid = 0
	}
	if state.workerState == "running" {
		state.workerState = "idle"
	}
	driver := state.source.Driver
	username := state.source.Username
	state.mu.Unlock()

	state.addLog(fmt.Sprintf("[system] process pid=%d exited (code=%d intentional=%v)", pid, exitCode, intentional))
	log.Printf("manager: worker pid=%d exited for %s/%s (code=%d intentional=%v)", pid, driver, username, exitCode, intentional)

	et := store.EventExited
	if err := m.st.RecordWorkerEvent(context.Background(), sourceID, pid, et, &exitCode); err != nil {
		log.Printf("manager: record exited event source=%d: %v", sourceID, err)
	}
}

// OnRestarting is called when the overseer is scheduling a restart.
func (m *Manager) OnRestarting(taskID string, pid int, attempt int, ts time.Time) {
	sourceID := m.sourceIDByTask(taskID)
	if sourceID == 0 {
		return
	}
	state := m.stateByID(sourceID)
	if state == nil {
		return
	}
	state.addLog(fmt.Sprintf("[system] restarting (attempt %d)", attempt))
}

// OnErrored is called when the overseer gives up retrying.
func (m *Manager) OnErrored(taskID string, pid int, exitCount int, ts time.Time) {
	sourceID := m.sourceIDByTask(taskID)
	if sourceID == 0 {
		return
	}
	state := m.stateByID(sourceID)
	if state == nil {
		return
	}

	msg := fmt.Sprintf("error threshold reached after %d exits", exitCount)
	state.mu.Lock()
	state.workerState = "errored"
	state.errorMessage = msg
	state.mu.Unlock()

	state.addLog("[system] error threshold reached — use reset-error to retry")
	log.Printf("manager: source=%d errored: %s", sourceID, msg)
}

// ---- worker lifecycle ----

func (m *Manager) startWorker(sourceID int64) {
	state := m.stateByID(sourceID)
	if state == nil {
		return
	}

	state.mu.Lock()
	if state.workerState == "running" || state.workerState == "starting" {
		state.mu.Unlock()
		return
	}
	src := state.source
	taskID := src.OverseerTaskID
	state.workerState = "starting"
	state.mu.Unlock()

	defer func() {
		state.mu.Lock()
		if state.workerState == "starting" {
			state.workerState = "idle"
		}
		state.mu.Unlock()
	}()

	g := m.cfg.Get()
	args := buildArgs(src.Driver, src.Username, g)
	rp := &overseer.RetryPolicy{
		RestartDelay:   g.RestartDelay,
		ErrorWindow:    g.ErrorWindow,
		ErrorThreshold: g.ErrorThreshold,
	}

	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
	defer cancel()

	gotTaskID, pid, err := m.oc.Start(ctx, taskID, args, rp)
	if err != nil {
		log.Printf("manager: start worker for %s/%s: %v", src.Driver, src.Username, err)
		state.addLog(fmt.Sprintf("[system] start failed: %v", err))
		return
	}

	// Persist the task_id if it's new.
	if gotTaskID != taskID {
		if err := m.st.SetSourceTaskID(context.Background(), sourceID, gotTaskID); err != nil {
			log.Printf("manager: SetSourceTaskID source=%d: %v", sourceID, err)
		}
		state.mu.Lock()
		state.source.OverseerTaskID = gotTaskID
		state.mu.Unlock()

		m.mu.Lock()
		if taskID != "" {
			delete(m.taskIdx, taskID)
		}
		m.taskIdx[gotTaskID] = sourceID
		m.mu.Unlock()
	}

	state.mu.Lock()
	state.pid = pid
	state.workerState = "running"
	state.mu.Unlock()

	log.Printf("manager: started worker task=%s pid=%d for %s/%s", gotTaskID, pid, src.Driver, src.Username)
}

// stopWorker sends a stop command to the overseer for a source's task.
func (m *Manager) stopWorker(state *sourceState) {
	state.mu.Lock()
	taskID := state.source.OverseerTaskID
	state.mu.Unlock()

	if taskID == "" {
		return
	}
	if err := m.oc.Stop(taskID); err != nil {
		log.Printf("manager: stop task=%s: %v", taskID, err)
	}
}

// ---- public API ----

// Subscribe creates or reactivates a subscription for userID → driver/username.
// Starts a worker if this is the first active subscriber for the source.
func (m *Manager) Subscribe(ctx context.Context, userID int64, driver, username string) (*SubscriptionStatus, error) {
	src, err := m.st.GetOrCreateSource(ctx, driver, username)
	if err != nil {
		return nil, err
	}

	sub, err := m.st.CreateSubscription(ctx, userID, src.ID)
	if err != nil {
		return nil, err
	}

	// Ensure the source has an in-memory state entry.
	m.mu.Lock()
	if _, exists := m.states[src.ID]; !exists {
		m.states[src.ID] = &sourceState{source: src, workerState: "idle"}
		if src.OverseerTaskID != "" {
			m.taskIdx[src.OverseerTaskID] = src.ID
		}
	}
	m.mu.Unlock()

	// Start worker only if this is the first active subscriber.
	count, err := m.st.GetSourceActiveSubscriberCount(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	if count == 1 {
		go m.startWorker(src.ID)
	}

	return m.statusFor(src, sub), nil
}

// Unsubscribe archives the subscription and stops the worker if no active subs remain.
func (m *Manager) Unsubscribe(ctx context.Context, userID int64, driver, username string) error {
	src, sub, err := m.lookupSub(ctx, userID, driver, username)
	if err != nil {
		return err
	}

	if err := m.st.SetPosture(ctx, sub.ID, store.PostureArchived); err != nil {
		return err
	}

	count, err := m.st.GetSourceActiveSubscriberCount(ctx, src.ID)
	if err != nil {
		return err
	}
	if count == 0 {
		state := m.stateByID(src.ID)
		if state != nil {
			m.stopWorker(state)
		}
	}
	return nil
}

// Pause pauses the subscription; stops worker if no active subs remain.
func (m *Manager) Pause(ctx context.Context, userID int64, driver, username string) (*SubscriptionStatus, error) {
	src, sub, err := m.lookupSub(ctx, userID, driver, username)
	if err != nil {
		return nil, err
	}
	if sub.Posture == store.PosturePaused {
		return m.statusFor(src, sub), nil
	}

	if err := m.st.SetPosture(ctx, sub.ID, store.PosturePaused); err != nil {
		return nil, err
	}
	sub.Posture = store.PosturePaused

	count, err := m.st.GetSourceActiveSubscriberCount(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		state := m.stateByID(src.ID)
		if state != nil {
			m.stopWorker(state)
		}
	}
	return m.statusFor(src, sub), nil
}

// Resume sets the subscription active; starts worker if it's the first active sub.
func (m *Manager) Resume(ctx context.Context, userID int64, driver, username string) (*SubscriptionStatus, error) {
	src, sub, err := m.lookupSub(ctx, userID, driver, username)
	if err != nil {
		return nil, err
	}
	if sub.Posture == store.PostureActive {
		return m.statusFor(src, sub), nil
	}

	if err := m.st.SetPosture(ctx, sub.ID, store.PostureActive); err != nil {
		return nil, err
	}
	sub.Posture = store.PostureActive

	count, err := m.st.GetSourceActiveSubscriberCount(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	if count == 1 {
		go m.startWorker(src.ID)
	}
	return m.statusFor(src, sub), nil
}

// Archive sets the subscription archived (permanent pause); stops worker if needed.
func (m *Manager) Archive(ctx context.Context, userID int64, driver, username string) (*SubscriptionStatus, error) {
	src, sub, err := m.lookupSub(ctx, userID, driver, username)
	if err != nil {
		return nil, err
	}

	if err := m.st.SetPosture(ctx, sub.ID, store.PostureArchived); err != nil {
		return nil, err
	}
	sub.Posture = store.PostureArchived

	count, err := m.st.GetSourceActiveSubscriberCount(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		state := m.stateByID(src.ID)
		if state != nil {
			m.stopWorker(state)
		}
	}
	return m.statusFor(src, sub), nil
}

// ResetError clears overseer errored state and restarts the worker.
func (m *Manager) ResetError(ctx context.Context, userID int64, driver, username string) (*SubscriptionStatus, error) {
	src, sub, err := m.lookupSub(ctx, userID, driver, username)
	if err != nil {
		return nil, err
	}

	state := m.stateByID(src.ID)
	if state == nil {
		return nil, fmt.Errorf("source %s/%s not tracked", driver, username)
	}

	state.mu.Lock()
	taskID := state.source.OverseerTaskID
	wsState := state.workerState
	state.mu.Unlock()

	if wsState != "errored" {
		return nil, fmt.Errorf("source %s/%s is not in errored state", driver, username)
	}

	if taskID != "" {
		if err := m.oc.Reset(taskID); err != nil {
			return nil, fmt.Errorf("overseer reset: %w", err)
		}
	} else {
		go m.startWorker(src.ID)
	}

	state.mu.Lock()
	state.workerState = "starting"
	state.errorMessage = ""
	state.mu.Unlock()
	state.addLog("[system] reset — restarting worker")

	return m.statusFor(src, sub), nil
}

// GetStatus returns status for a specific user's subscription.
func (m *Manager) GetStatus(ctx context.Context, userID int64, driver, username string) (*SubscriptionStatus, error) {
	src, sub, err := m.lookupSub(ctx, userID, driver, username)
	if err != nil {
		return nil, err
	}
	return m.statusFor(src, sub), nil
}

// ListSubscriptions returns visible subscriptions for a user (or all for admin).
func (m *Manager) ListSubscriptions(ctx context.Context, userID int64, isAdmin bool) ([]*SubscriptionStatus, error) {
	var subs []*store.Subscription
	var err error
	if isAdmin {
		subs, err = m.st.ListAllSubscriptions(ctx)
	} else {
		subs, err = m.st.ListSubscriptionsByUser(ctx, userID)
	}
	if err != nil {
		return nil, err
	}

	// Pre-load sources to avoid N+1 queries.
	sources, err := m.st.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	srcMap := make(map[int64]*store.Source, len(sources))
	for _, s := range sources {
		srcMap[s.ID] = s
	}

	out := make([]*SubscriptionStatus, 0, len(subs))
	for _, sub := range subs {
		src := srcMap[sub.SourceID]
		if src == nil {
			continue
		}
		out = append(out, m.statusFor(src, sub))
	}
	return out, nil
}

// GetLogs returns the in-memory log buffer for a source.
func (m *Manager) GetLogs(ctx context.Context, userID int64, isAdmin bool, driver, username string) ([]string, error) {
	src, err := m.st.GetSourceByKey(ctx, driver, username)
	if err != nil || src == nil {
		return nil, fmt.Errorf("source %s/%s not found", driver, username)
	}
	if !isAdmin {
		// Verify user has a subscription to this source.
		sub, err := m.st.GetSubscription(ctx, userID, src.ID)
		if err != nil || sub == nil {
			return nil, fmt.Errorf("source %s/%s not found", driver, username)
		}
	}
	state := m.stateByID(src.ID)
	if state == nil {
		return []string{}, nil
	}
	return state.getLogs(), nil
}

// GetWorkerEvents returns persisted worker lifecycle events for a source.
func (m *Manager) GetWorkerEvents(ctx context.Context, userID int64, isAdmin bool, driver, username string, limit int) ([]store.WorkerEvent, error) {
	src, err := m.st.GetSourceByKey(ctx, driver, username)
	if err != nil || src == nil {
		return nil, fmt.Errorf("source %s/%s not found", driver, username)
	}
	if !isAdmin {
		sub, err := m.st.GetSubscription(ctx, userID, src.ID)
		if err != nil || sub == nil {
			return nil, fmt.Errorf("source %s/%s not found", driver, username)
		}
	}
	return m.st.RecentWorkerEvents(ctx, src.ID, limit)
}

func (m *Manager) GetConfig() config.Data        { return m.cfg.Get() }
func (m *Manager) SetConfig(ctx context.Context, d config.Data) error {
	return m.cfg.Set(ctx, d)
}

// ---- periodic reconciliation ----

func (m *Manager) reconcileLoop(ctx context.Context) {
	g := m.cfg.Get()
	ticker := time.NewTicker(parseDuration(g.ReconcileInterval, 60*time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) {
	tasks, err := m.oc.List(ctx)
	if err != nil {
		log.Printf("manager: reconcile: overseer list: %v", err)
		return
	}

	runningTasks := make(map[string]struct{})
	for _, t := range tasks {
		if t.WorkerState == "running" {
			runningTasks[t.TaskID] = struct{}{}
		}
	}

	m.mu.RLock()
	ids := make([]int64, 0, len(m.states))
	for id := range m.states {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, id := range ids {
		state := m.stateByID(id)
		if state == nil {
			continue
		}

		state.mu.Lock()
		wsState := state.workerState
		taskID := state.source.OverseerTaskID
		driver := state.source.Driver
		username := state.source.Username
		state.mu.Unlock()

		if wsState != "running" {
			continue
		}
		if taskID == "" {
			continue
		}
		if _, alive := runningTasks[taskID]; alive {
			continue
		}

		log.Printf("manager: reconcile: task=%s for %s/%s gone, restarting", taskID, driver, username)
		state.mu.Lock()
		state.pid = 0
		state.workerState = "idle"
		state.mu.Unlock()
		state.addLog("[system] worker gone (detected by reconciler), restarting")
		go m.startWorker(id)
	}
}

// ---- internal helpers ----

func (m *Manager) stateByID(id int64) *sourceState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.states[id]
}

func (m *Manager) sourceIDByTask(taskID string) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.taskIdx[taskID]
}

func (m *Manager) lookupSub(ctx context.Context, userID int64, driver, username string) (*store.Source, *store.Subscription, error) {
	src, err := m.st.GetSourceByKey(ctx, driver, username)
	if err != nil {
		return nil, nil, err
	}
	if src == nil {
		return nil, nil, fmt.Errorf("source %s/%s not found", driver, username)
	}

	sub, err := m.st.GetSubscription(ctx, userID, src.ID)
	if err != nil {
		return nil, nil, err
	}
	if sub == nil {
		return nil, nil, fmt.Errorf("subscription %s/%s not found", driver, username)
	}
	return src, sub, nil
}

func (m *Manager) statusFor(src *store.Source, sub *store.Subscription) *SubscriptionStatus {
	state := m.stateByID(src.ID)

	s := &SubscriptionStatus{
		Driver:    src.Driver,
		Username:  src.Username,
		SourceID:  src.ID,
		SubID:     sub.ID,
		UserID:    sub.UserID,
		Posture:   sub.Posture,
		CreatedAt: sub.CreatedAt,
		UpdatedAt: sub.UpdatedAt,
		Logs:      []string{},
	}

	if state == nil {
		s.WorkerState = "idle"
		return s
	}

	state.mu.Lock()
	s.WorkerState = state.workerState
	s.PID = state.pid
	s.ErrorMessage = state.errorMessage
	s.Logs = make([]string, len(state.logs))
	copy(s.Logs, state.logs)
	state.mu.Unlock()

	return s
}

// ---- args builder ----

func buildArgs(driver, username string, g config.Data) []string {
	args := []string{
		"--source", username,
		"--driver", driver,
		"--resolution", fmt.Sprintf("%d", g.Resolution),
		"--framerate", fmt.Sprintf("%d", g.Framerate),
		"--out", g.OutPattern,
		"--log-level", g.LogLevel,
		"--output-format", "json",
	}
	if g.LogPattern != "" {
		args = append(args, "--log", g.LogPattern)
	}
	if g.CheckInterval != "" {
		args = append(args, "--check-interval", g.CheckInterval)
	}
	if g.RetryDelay != "" {
		args = append(args, "--retry-delay", g.RetryDelay)
	}
	if g.SegmentLength != "" {
		args = append(args, "--segment-length", g.SegmentLength)
	}
	if g.SegmentTimeout != "" {
		args = append(args, "--segment-timeout", g.SegmentTimeout)
	}
	if g.RecordingTimeout != "" {
		args = append(args, "--recording-timeout", g.RecordingTimeout)
	}
	if g.Cookies != "" {
		args = append(args, "--cookies", g.Cookies)
	}
	if g.UserAgent != "" {
		args = append(args, "--user-agent", g.UserAgent)
	}
	return args
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}
