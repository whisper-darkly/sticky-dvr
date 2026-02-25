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
	"encoding/json"
	"fmt"
	"log"
	"strings"
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
	// Recording-level state derived from recorder JSON output events.
	recordingState  string    // recording | sleeping | idle
	sessionDuration string    // last known session duration from HEARTBEAT
	lastHeartbeat   time.Time // time of last HEARTBEAT event
	lastRecordingAt time.Time // time of the most recent RECORDING START event
	// sessionActive is true from the first RECORDING START until SESSION END or process exit.
	// Unlike recordingState, it stays true through segment boundaries (RECORDING END → RECORDING START)
	// and SLEEP events, so the UI can show the source as "in session" without debounce logic.
	sessionActive    bool
	sessionStartedAt time.Time // wall-clock time of the first RECORDING START in this session
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

	// Recording-level state derived from recorder JSON output events.
	RecordingState  string     `json:"recording_state,omitempty"`  // recording | sleeping | idle
	SessionDuration string     `json:"session_duration,omitempty"`
	LastHeartbeat   time.Time  `json:"last_heartbeat,omitempty"`
	LastRecordingAt *time.Time `json:"last_recording_at,omitempty"`
	// SessionActive is true from RECORDING START until SESSION END or process exit.
	// Stays true through segment boundaries and SLEEP — use this for "in session" UI logic.
	SessionActive    bool       `json:"session_active"`
	SessionStartedAt *time.Time `json:"session_started_at,omitempty"`

	// Derived fields
	CanonicalURL string `json:"canonical_url,omitempty"`
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

	byTaskID, byKey := taskMaps(tasks)

	m.mu.RLock()
	ids := make([]int64, 0, len(m.states))
	for id := range m.states {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	var toStart []int64
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
				log.Printf("manager: startup: claimed task=%s pid=%d for %s/%s", taskID, t.CurrentPID, driver, username)
				m.claimTask(ctx, id, state, taskID, t)
				continue
			}
		}

		// Check by action/source key in case the task ID changed.
		key := driver + "/" + username
		if t, ok := byKey[key]; ok && t.WorkerState == "running" {
			log.Printf("manager: startup: claimed untracked task=%s pid=%d for %s/%s", t.TaskID, t.CurrentPID, driver, username)
			m.claimTask(ctx, id, state, taskID, t)
			continue
		}

		toStart = append(toStart, id)
	}

	if len(toStart) > 0 {
		log.Printf("manager: startup: starting %d worker(s)", len(toStart))
		m.bulkStart(toStart)
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

// OnOutput routes a stdout/stderr line to the source's log buffer,
// and parses JSON recorder events to update recording-level state.
func (m *Manager) OnOutput(taskID string, pid int, stream, data string, ts time.Time) {
	sourceID := m.sourceIDByTask(taskID)
	if sourceID == 0 {
		return
	}
	stateObj := m.stateByID(sourceID)
	if stateObj == nil {
		return
	}
	stateObj.addLog(fmt.Sprintf("[%s] %s", stream, data))

	// Parse JSON recorder events to derive recording state.
	var ev map[string]string
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		// Non-JSON output — log it so it's visible in docker logs.
		log.Printf("recorder [%s/%s] pid=%d %s: %s",
			stateObj.source.Driver, stateObj.source.Username, pid, stream, data)
		return
	}
	event := ev["event"]

	// Log all recorder events except HEARTBEAT (too frequent) to stdout.
	if event != "" && event != "HEARTBEAT" {
		log.Printf("recorder [%s/%s] pid=%d event=%q %v",
			stateObj.source.Driver, stateObj.source.Username, pid, event, ev)
	}

	stateObj.mu.Lock()
	defer stateObj.mu.Unlock()

	switch event {
	case "RECORDING START":
		stateObj.recordingState = "recording"
		stateObj.lastRecordingAt = ts
		if !stateObj.sessionActive {
			// First segment of a new session — anchor the session clock.
			stateObj.sessionStartedAt = ts
		}
		stateObj.sessionActive = true
	case "RECORDING END":
		stateObj.recordingState = "idle"
		// sessionActive stays true — segment boundary, not session end
	case "SLEEP":
		stateObj.recordingState = "sleeping"
		// sessionActive stays true — source went offline, session may resume
	case "SESSION END":
		stateObj.recordingState = "idle"
		stateObj.sessionActive = false
		stateObj.sessionStartedAt = time.Time{}
	case "HEARTBEAT":
		stateObj.recordingState = "recording"
		stateObj.lastHeartbeat = ts
		stateObj.sessionActive = true
		if d, ok := ev["session_duration"]; ok {
			stateObj.sessionDuration = d
		}
	}
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
	state.recordingState = ""
	state.sessionActive = false
	state.sessionStartedAt = time.Time{}
	driver := state.source.Driver
	username := state.source.Username
	state.mu.Unlock()

	state.addLog(fmt.Sprintf("[system] process pid=%d exited (code=%d intentional=%v)", pid, exitCode, intentional))
	log.Printf("manager: worker pid=%d exited for %s/%s (code=%d intentional=%v)", pid, driver, username, exitCode, intentional)

	// On unexpected non-zero exit, dump the last few log lines for context.
	if exitCode != 0 && !intentional {
		logs := state.getLogs()
		start := len(logs) - 8
		if start < 0 {
			start = 0
		}
		log.Printf("manager: last %d log lines for %s/%s pid=%d:", len(logs)-start, driver, username, pid)
		for _, l := range logs[start:] {
			log.Printf("manager:   %s", l)
		}
	}

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
	rp := &overseer.RetryPolicy{
		RestartDelay:   g.RestartDelay,
		ErrorWindow:    g.ErrorWindow,
		ErrorThreshold: g.ErrorThreshold,
	}

	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
	defer cancel()

	segLen := g.SegmentLength
	if segLen == "" {
		segLen = "0"
	}
	params := map[string]string{
		"source":             src.Username,
		"out":                g.OutPattern,
		"segment_length":     segLen,
		"check_interval":     g.CheckInterval,
		"resolution":         fmt.Sprintf("%d", g.Resolution),
		"framerate":          fmt.Sprintf("%d", g.Framerate),
		"cookies":            g.Cookies,
		"user_agent":         g.UserAgent,
		"heartbeat_interval": "30s",
	}
	gotTaskID, pid, err := m.oc.Start(ctx, taskID, src.Driver, params, rp)
	if err != nil && taskID != "" && strings.Contains(err.Error(), "already running") {
		// Overseer task is still in active state (race between stop and start).
		// Force-stop it and start a fresh task with a new ID.
		log.Printf("manager: task=%s still active at overseer, clearing and restarting %s/%s", taskID, src.Driver, src.Username)
		_ = m.oc.Stop(taskID)
		if dbErr := m.st.SetSourceTaskID(context.Background(), sourceID, ""); dbErr != nil {
			log.Printf("manager: clear task_id source=%d: %v", sourceID, dbErr)
		}
		m.mu.Lock()
		delete(m.taskIdx, taskID)
		m.mu.Unlock()
		state.mu.Lock()
		state.source.OverseerTaskID = ""
		state.mu.Unlock()
		taskID = ""

		retryCtx, retryCancel := context.WithTimeout(m.ctx, 20*time.Second)
		defer retryCancel()
		gotTaskID, pid, err = m.oc.Start(retryCtx, "", src.Driver, params, rp)
	}
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
// The old task is stopped and its ID cleared so startWorker creates a new task
// with the current configuration (cookies, user_agent, etc.) rather than the
// params that were originally stored in the overseer at task creation time.
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

	// Stop the current task and clear its ID so startWorker creates a brand-new
	// task that picks up the latest configuration values.
	if taskID != "" {
		_ = m.oc.Stop(taskID)
		if dbErr := m.st.SetSourceTaskID(context.Background(), src.ID, ""); dbErr != nil {
			log.Printf("manager: clear task_id source=%d: %v", src.ID, dbErr)
		}
		m.mu.Lock()
		delete(m.taskIdx, taskID)
		m.mu.Unlock()
		state.mu.Lock()
		state.source.OverseerTaskID = ""
		state.mu.Unlock()
	}

	state.mu.Lock()
	state.workerState = "idle"
	state.errorMessage = ""
	state.mu.Unlock()
	state.addLog("[system] reset — restarting worker with current configuration")

	go m.startWorker(src.ID)

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

// ---- admin helpers ----

// lookupSubByID fetches a subscription by its ID along with the parent source.
func (m *Manager) lookupSubByID(ctx context.Context, subID int64) (*store.Source, *store.Subscription, error) {
	sub, err := m.st.GetSubscriptionByID(ctx, subID)
	if err != nil {
		return nil, nil, err
	}
	if sub == nil {
		return nil, nil, fmt.Errorf("subscription %d not found", subID)
	}
	src, err := m.st.GetSourceByID(ctx, sub.SourceID)
	if err != nil {
		return nil, nil, err
	}
	if src == nil {
		return nil, nil, fmt.Errorf("source for subscription %d not found", subID)
	}
	return src, sub, nil
}

// AdminPause pauses any subscription by its ID (admin only).
func (m *Manager) AdminPause(ctx context.Context, subID int64) (*SubscriptionStatus, error) {
	src, sub, err := m.lookupSubByID(ctx, subID)
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
		if state := m.stateByID(src.ID); state != nil {
			m.stopWorker(state)
		}
	}
	return m.statusFor(src, sub), nil
}

// AdminResume resumes any subscription by its ID (admin only).
func (m *Manager) AdminResume(ctx context.Context, subID int64) (*SubscriptionStatus, error) {
	src, sub, err := m.lookupSubByID(ctx, subID)
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

	// Ensure source has in-memory state.
	m.mu.Lock()
	if _, exists := m.states[src.ID]; !exists {
		m.states[src.ID] = &sourceState{source: src, workerState: "idle"}
		if src.OverseerTaskID != "" {
			m.taskIdx[src.OverseerTaskID] = src.ID
		}
	}
	m.mu.Unlock()

	count, err := m.st.GetSourceActiveSubscriberCount(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	if count == 1 {
		go m.startWorker(src.ID)
	}
	return m.statusFor(src, sub), nil
}

// AdminArchive archives any subscription by its ID (admin only).
func (m *Manager) AdminArchive(ctx context.Context, subID int64) (*SubscriptionStatus, error) {
	src, sub, err := m.lookupSubByID(ctx, subID)
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
		if state := m.stateByID(src.ID); state != nil {
			m.stopWorker(state)
		}
	}
	return m.statusFor(src, sub), nil
}

// AdminUnsubscribe deletes any subscription by its ID (admin only).
func (m *Manager) AdminUnsubscribe(ctx context.Context, subID int64) error {
	src, sub, err := m.lookupSubByID(ctx, subID)
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
		if state := m.stateByID(src.ID); state != nil {
			m.stopWorker(state)
		}
	}
	return nil
}

// AdminResetError clears errored state for any subscription by its ID (admin only).
// Same stop+clear+restart approach as ResetError to ensure fresh config params are used.
func (m *Manager) AdminResetError(ctx context.Context, subID int64) (*SubscriptionStatus, error) {
	src, sub, err := m.lookupSubByID(ctx, subID)
	if err != nil {
		return nil, err
	}
	state := m.stateByID(src.ID)
	if state == nil {
		return nil, fmt.Errorf("source %s/%s not tracked", src.Driver, src.Username)
	}
	state.mu.Lock()
	taskID := state.source.OverseerTaskID
	wsState := state.workerState
	state.mu.Unlock()
	if wsState != "errored" {
		return nil, fmt.Errorf("source %s/%s is not in errored state", src.Driver, src.Username)
	}
	// Stop old task, clear ID, let startWorker create a fresh task with current config.
	if taskID != "" {
		_ = m.oc.Stop(taskID)
		if dbErr := m.st.SetSourceTaskID(context.Background(), src.ID, ""); dbErr != nil {
			log.Printf("manager: clear task_id source=%d: %v", src.ID, dbErr)
		}
		m.mu.Lock()
		delete(m.taskIdx, taskID)
		m.mu.Unlock()
		state.mu.Lock()
		state.source.OverseerTaskID = ""
		state.mu.Unlock()
	}
	state.mu.Lock()
	state.workerState = "idle"
	state.errorMessage = ""
	state.mu.Unlock()
	state.addLog("[system] reset — restarting worker with current configuration")
	go m.startWorker(src.ID)
	return m.statusFor(src, sub), nil
}

// AdminRestartSource stops and restarts a single subscription's worker by sub_id (admin only).
// Applies the same stop+clear+startWorker pattern as RestartAll so the latest configuration
// (cookies, user_agent, etc.) is always picked up. Works on any worker state including errored.
func (m *Manager) AdminRestartSource(ctx context.Context, subID int64) (*SubscriptionStatus, error) {
	src, sub, err := m.lookupSubByID(ctx, subID)
	if err != nil {
		return nil, err
	}

	count, err := m.st.GetSourceActiveSubscriberCount(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, fmt.Errorf("source %s/%s has no active subscribers", src.Driver, src.Username)
	}

	// Ensure in-memory state exists (may not be tracked if subscription was just resumed).
	m.mu.Lock()
	if _, exists := m.states[src.ID]; !exists {
		m.states[src.ID] = &sourceState{source: src, workerState: "idle"}
	}
	m.mu.Unlock()

	state := m.stateByID(src.ID)

	state.mu.Lock()
	taskID := state.source.OverseerTaskID
	state.mu.Unlock()

	if taskID != "" {
		_ = m.oc.Stop(taskID)
		if dbErr := m.st.SetSourceTaskID(ctx, src.ID, ""); dbErr != nil {
			log.Printf("manager: restart: clear task_id source=%d: %v", src.ID, dbErr)
		}
		m.mu.Lock()
		delete(m.taskIdx, taskID)
		m.mu.Unlock()
		state.mu.Lock()
		state.source.OverseerTaskID = ""
		state.mu.Unlock()
	}

	state.mu.Lock()
	state.workerState = "idle"
	state.errorMessage = ""
	state.sessionActive = false
	state.sessionStartedAt = time.Time{}
	state.recordingState = ""
	state.mu.Unlock()
	state.addLog("[system] restarting (manual restart — applying current configuration)")

	go m.startWorker(src.ID)
	return m.statusFor(src, sub), nil
}

// GetSourceSubscribers returns subscriber info for all users subscribed to a source.
func (m *Manager) GetSourceSubscribers(ctx context.Context, driver, username string) ([]*store.SubscriberInfo, error) {
	src, err := m.st.GetSourceByKey(ctx, driver, username)
	if err != nil || src == nil {
		return nil, fmt.Errorf("source %s/%s not found", driver, username)
	}
	return m.st.GetSourceSubscribers(ctx, src.ID)
}

// RestartAll stops every tracked source that has at least one active subscriber and
// restarts it with fresh configuration. Sources in "errored" state are skipped unless
// includeErrored is true. Returns counts of restarted and skipped sources.
func (m *Manager) RestartAll(ctx context.Context, includeErrored bool) (restarted, skipped int) {
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

		// Only restart sources that have at least one active subscriber.
		count, err := m.st.GetSourceActiveSubscriberCount(ctx, id)
		if err != nil || count == 0 {
			skipped++
			continue
		}

		state.mu.Lock()
		wsState := state.workerState
		taskID := state.source.OverseerTaskID
		state.mu.Unlock()

		// Skip errored sources unless the caller asked to include them.
		if wsState == "errored" && !includeErrored {
			skipped++
			continue
		}

		// Stop the current task, clear its ID, then start fresh so the worker
		// picks up the latest configuration (cookies, user_agent, etc.).
		if taskID != "" {
			_ = m.oc.Stop(taskID)
			if dbErr := m.st.SetSourceTaskID(ctx, id, ""); dbErr != nil {
				log.Printf("manager: restart-all: clear task_id source=%d: %v", id, dbErr)
			}
			m.mu.Lock()
			delete(m.taskIdx, taskID)
			m.mu.Unlock()
			state.mu.Lock()
			state.source.OverseerTaskID = ""
			state.mu.Unlock()
		}

		state.mu.Lock()
		state.workerState = "idle"
		state.errorMessage = ""
		state.sessionActive = false
		state.sessionStartedAt = time.Time{}
		state.recordingState = ""
		state.mu.Unlock()
		state.addLog("[system] restarting (restart-all — applying current configuration)")

		go m.startWorker(id)
		restarted++
	}
	return
}

// OnConnected is called each time the overseer WebSocket connection is established.
// It reconciles in-memory state against the live overseer: claims tasks that are
// still running (same overseer instance reconnect) and restarts workers for any
// source with active subscriptions whose task was lost (overseer restart).
func (m *Manager) OnConnected() {
	log.Printf("manager: overseer connected, reconciling")
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	m.reconcileOnConnect(ctx)
}

// reconcileOnConnect is the full reconnect reconciliation. For each source it
// either claims the still-running overseer task, or (re)starts the worker if
// the source has active subscriptions but no running task. Errored sources are
// left alone — they require an explicit reset.
func (m *Manager) reconcileOnConnect(ctx context.Context) {
	tasks, err := m.oc.List(ctx)
	if err != nil {
		log.Printf("manager: reconnect reconcile: overseer list: %v", err)
		return
	}

	byTaskID, byKey := taskMaps(tasks)

	m.mu.RLock()
	ids := make([]int64, 0, len(m.states))
	for id := range m.states {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	var toStart []int64
	claimed := 0
	for _, id := range ids {
		state := m.stateByID(id)
		if state == nil {
			continue
		}

		state.mu.Lock()
		taskID := state.source.OverseerTaskID
		driver := state.source.Driver
		username := state.source.Username
		wsState := state.workerState
		state.mu.Unlock()

		// Don't touch errored sources — require explicit reset-error.
		if wsState == "errored" {
			continue
		}

		if taskID != "" {
			if t, ok := byTaskID[taskID]; ok && t.WorkerState == "running" {
				log.Printf("manager: reconnect: claimed task=%s pid=%d for %s/%s", taskID, t.CurrentPID, driver, username)
				m.claimTask(ctx, id, state, taskID, t)
				claimed++
				continue
			}
		}

		// Task is gone (or was never assigned). Only restart if there are
		// active subscriptions; otherwise the source is intentionally idle.
		count, err := m.st.GetSourceActiveSubscriberCount(ctx, id)
		if err != nil {
			log.Printf("manager: reconnect: active sub count source=%d: %v", id, err)
			continue
		}
		if count == 0 {
			// Paused/archived — ensure state is consistent.
			state.mu.Lock()
			if wsState == "running" {
				state.workerState = "idle"
				state.pid = 0
			}
			state.mu.Unlock()
			continue
		}

		// Before starting fresh, check whether the overseer already has a
		// running task for this source under an unknown task ID (e.g., a
		// previous start confirmation arrived late and was never persisted).
		key := driver + "/" + username
		if t, ok := byKey[key]; ok && t.WorkerState == "running" {
			log.Printf("manager: reconnect: found untracked task=%s pid=%d for %s/%s, claiming", t.TaskID, t.CurrentPID, driver, username)
			state.mu.Lock()
			if wsState == "running" || wsState == "starting" {
				state.workerState = "idle"
				state.pid = 0
			}
			state.mu.Unlock()
			m.claimTask(ctx, id, state, taskID, t)
			claimed++
			continue
		}

		// Has active subscriptions but no running task — queue for (re)start.
		log.Printf("manager: reconnect: source=%d %s/%s missing task, will restart", id, driver, username)
		state.mu.Lock()
		if wsState == "running" || wsState == "starting" {
			state.workerState = "idle"
			state.pid = 0
		}
		state.mu.Unlock()
		toStart = append(toStart, id)
	}

	started := len(toStart)
	if started > 0 {
		m.bulkStart(toStart)
	}
	log.Printf("manager: reconnect reconcile: claimed=%d restarted=%d", claimed, started)
}

// claimTask updates in-memory state and subscribes to an overseer task that is
// already running but not yet tracked (or tracked under a stale ID). The store
// and taskIdx are updated if the task ID has changed.
func (m *Manager) claimTask(ctx context.Context, sourceID int64, state *sourceState, oldTaskID string, t overseer.TaskInfo) {
	if t.TaskID != oldTaskID {
		if err := m.st.SetSourceTaskID(ctx, sourceID, t.TaskID); err != nil {
			log.Printf("manager: claimTask: set task_id source=%d: %v", sourceID, err)
		}
		m.mu.Lock()
		if oldTaskID != "" {
			delete(m.taskIdx, oldTaskID)
		}
		m.taskIdx[t.TaskID] = sourceID
		m.mu.Unlock()
		state.mu.Lock()
		state.source.OverseerTaskID = t.TaskID
		state.mu.Unlock()
	}
	state.mu.Lock()
	state.pid = t.CurrentPID
	state.workerState = "running"
	state.mu.Unlock()
	state.addLog(fmt.Sprintf("[system] claimed running task=%s pid=%d", t.TaskID, t.CurrentPID))
	if err := m.oc.Subscribe(t.TaskID); err != nil {
		log.Printf("manager: claimTask: subscribe task=%s: %v", t.TaskID, err)
	}
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

	byTaskID, byKey := taskMaps(tasks)

	m.mu.RLock()
	ids := make([]int64, 0, len(m.states))
	for id := range m.states {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	var toStart []int64
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

		// Never interfere with errored or in-flight starts.
		if wsState == "errored" || wsState == "starting" {
			continue
		}

		key := driver + "/" + username

		if wsState == "running" {
			if taskID != "" {
				if _, alive := byTaskID[taskID]; alive {
					continue // running as expected
				}
			}
			log.Printf("manager: reconcile: task=%s for %s/%s gone, restarting", taskID, driver, username)
			state.mu.Lock()
			state.pid = 0
			state.workerState = "idle"
			state.mu.Unlock()
			state.addLog("[system] worker gone (detected by reconciler), restarting")
			go m.startWorker(id)
			continue
		}

		// wsState == "idle": check whether this source should be running.
		count, err := m.st.GetSourceActiveSubscriberCount(ctx, id)
		if err != nil {
			log.Printf("manager: reconcile: active sub count source=%d: %v", id, err)
			continue
		}
		if count == 0 {
			continue
		}

		// Active subscriptions exist but we think it's idle. Check whether
		// the overseer has a running task we lost track of — most commonly
		// from a start confirmation that arrived after our 20 s timeout.
		if t, ok := byKey[key]; ok && t.WorkerState == "running" {
			log.Printf("manager: reconcile: found untracked task=%s pid=%d for %s/%s, claiming", t.TaskID, t.CurrentPID, driver, username)
			m.claimTask(ctx, id, state, taskID, t)
			continue
		}

		// Nothing running — queue for start.
		log.Printf("manager: reconcile: source=%d %s/%s is idle with active subs, will start", id, driver, username)
		state.addLog("[system] detected idle with active subscriptions (reconciler), starting")
		toStart = append(toStart, id)
	}

	if len(toStart) > 0 {
		m.bulkStart(toStart)
	}
}

// taskMaps builds two lookup tables from an overseer task list:
//   - byTaskID: task_id → TaskInfo (all tasks)
//   - byKey: "action/source" → TaskInfo (running tasks only, for unknown-ID matching)
func taskMaps(tasks []overseer.TaskInfo) (byTaskID map[string]overseer.TaskInfo, byKey map[string]overseer.TaskInfo) {
	byTaskID = make(map[string]overseer.TaskInfo, len(tasks))
	byKey = make(map[string]overseer.TaskInfo, len(tasks))
	for _, t := range tasks {
		byTaskID[t.TaskID] = t
		if t.WorkerState == "running" {
			if src, ok := t.Params["source"]; ok && src != "" {
				byKey[t.Action+"/"+src] = t
			}
		}
	}
	return
}

// bulkStart launches startWorker for each id with bounded concurrency so that
// a large number of simultaneous starts doesn't flood the overseer's confirmation
// queue and trigger timeouts. Returns immediately; dispatch runs in the background.
func (m *Manager) bulkStart(ids []int64) {
	concurrency := m.cfg.Get().StartConcurrency
	if concurrency <= 0 {
		concurrency = 5
	}
	go func() {
		sem := make(chan struct{}, concurrency)
		for _, id := range ids {
			id := id
			sem <- struct{}{} // block until a slot is free
			go func() {
				defer func() { <-sem }()
				m.startWorker(id)
			}()
		}
	}()
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
	s.RecordingState = state.recordingState
	s.SessionDuration = state.sessionDuration
	s.LastHeartbeat = state.lastHeartbeat
	s.SessionActive = state.sessionActive
	if !state.lastRecordingAt.IsZero() {
		t := state.lastRecordingAt
		s.LastRecordingAt = &t
	}
	if !state.sessionStartedAt.IsZero() {
		t := state.sessionStartedAt
		s.SessionStartedAt = &t
	}
	state.mu.Unlock()

	// Populate canonical URL from config driver_urls.
	if tmpl := m.cfg.Get().DriverURLs[src.Driver]; tmpl != "" {
		s.CanonicalURL = strings.ReplaceAll(tmpl, "{{.Username}}", src.Username)
	}

	return s
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
