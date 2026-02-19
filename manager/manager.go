// Package manager orchestrates subscription workers via sticky-overseer.
//
// Each active subscription maps 1-to-1 with a long-running sticky-recorder
// process started in --check-interval watch mode.  The manager:
//
//   - On startup: claims already-running overseer workers, starts the rest.
//   - On worker exit: records the event, queries the store for the windowed
//     error count, and either restarts or transitions the subscription to
//     StateError.
//   - On explicit user action (pause / restart): records EventStopped before
//     sending SIGTERM, which marks the exit as intentional so it is excluded
//     from the error count, and establishes the cycle-reset boundary.
//   - Periodically: reconciles in-memory state against the overseer worker
//     list to recover from missed events (e.g. after an overseer restart).
package manager

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/whisper-darkly/sticky-backend/config"
	"github.com/whisper-darkly/sticky-backend/overseer"
	"github.com/whisper-darkly/sticky-backend/store"
)

const maxLogs = 200

// subState holds the in-memory runtime state for a single subscription.
// Error counting is fully delegated to the store; no counter lives here.
type subState struct {
	sub  *store.Subscription
	mu   sync.Mutex
	pid  int
	starting bool
	logs []string
}

func (s *subState) addLog(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.logs) >= maxLogs {
		s.logs = s.logs[1:]
	}
	s.logs = append(s.logs, line)
}

func (s *subState) getLogs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.logs))
	copy(out, s.logs)
	return out
}

// SubscriptionStatus is the API-facing view of a subscription with its runtime state.
type SubscriptionStatus struct {
	*store.Subscription
	WorkerState string   `json:"worker_state"` // idle | starting | recording
	PID         int      `json:"pid,omitempty"`
	Logs        []string `json:"logs"`
}

// Manager orchestrates subscription workers.
type Manager struct {
	mu       sync.RWMutex
	states   map[int64]*subState // subscriptionID → runtime state
	pidIndex map[int]int64       // overseer PID → subscriptionID

	cfg *config.Global
	st  store.Store
	oc  *overseer.Client
	ctx context.Context
}

// New creates a Manager.  Call SetOverseerClient then Start before use.
func New(cfg *config.Global, st store.Store) *Manager {
	return &Manager{
		states:   make(map[int64]*subState),
		pidIndex: make(map[int]int64),
		cfg:      cfg,
		st:       st,
	}
}

// SetOverseerClient wires in the overseer client.  Must be called before Start.
func (m *Manager) SetOverseerClient(oc *overseer.Client) {
	m.oc = oc
}

// Start loads active subscriptions, reconciles with any already-running
// overseer workers, starts workers for anything not yet covered, and
// launches the periodic reconciler.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx = ctx

	subs, err := m.st.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("list active subscriptions: %w", err)
	}

	m.mu.Lock()
	for _, sub := range subs {
		m.states[sub.ID] = &subState{sub: sub}
	}
	m.mu.Unlock()

	// Claim already-running workers from the overseer (backend-restart scenario).
	existingByKey := m.fetchRunningByKey(ctx)

	for _, sub := range subs {
		key := subKey(sub.Driver, sub.Source)
		if pid, ok := existingByKey[key]; ok {
			state := m.stateByID(sub.ID)
			state.mu.Lock()
			state.pid = pid
			state.mu.Unlock()
			m.mu.Lock()
			m.pidIndex[pid] = sub.ID
			m.mu.Unlock()
			log.Printf("manager: claimed existing worker pid=%d for %s/%s", pid, sub.Driver, sub.Source)
			state.addLog(fmt.Sprintf("[system] claimed existing worker (pid=%d)", pid))
		} else {
			go m.startWorker(sub.ID)
		}
	}

	go m.reconcileLoop(ctx)
	return nil
}

// fetchRunningByKey queries the overseer and returns a map of "driver/source" → PID
// for all currently running workers.
func (m *Manager) fetchRunningByKey(ctx context.Context) map[string]int {
	out := make(map[string]int)
	workers, err := m.oc.List(ctx)
	if err != nil {
		log.Printf("manager: startup overseer list failed (will start fresh): %v", err)
		return out
	}
	for _, w := range workers {
		if w.State != "running" {
			continue
		}
		driver, source := extractDriverSource(w.Args)
		if driver != "" && source != "" {
			out[subKey(driver, source)] = w.PID
		}
	}
	return out
}

// ---- overseer event callbacks ----

// OnOutput routes a stdout/stderr line to the correct subscription's log buffer.
func (m *Manager) OnOutput(pid int, stream, data string, _ time.Time) {
	state := m.stateByPID(pid)
	if state == nil {
		return
	}
	state.addLog(fmt.Sprintf("[%s] %s", stream, data))
}

// OnExited handles process termination:
//  1. Records EventExited in the store.
//  2. Queries the windowed error count.
//  3. Either transitions to StateError or schedules a restart.
func (m *Manager) OnExited(pid int, exitCode int, _ time.Time) {
	m.mu.Lock()
	subID, ok := m.pidIndex[pid]
	if ok {
		delete(m.pidIndex, pid)
	}
	m.mu.Unlock()
	if !ok {
		return
	}

	state := m.stateByID(subID)
	if state == nil {
		return
	}

	state.mu.Lock()
	if state.pid == pid {
		state.pid = 0
	}
	sub := state.sub
	state.mu.Unlock()

	state.addLog(fmt.Sprintf("[system] process pid=%d exited (code %d)", pid, exitCode))
	log.Printf("manager: worker pid=%d for %s/%s exited (code %d)", pid, sub.Driver, sub.Source, exitCode)

	g := m.cfg.Get()
	target := store.Target{Sub: sub, Config: g}

	// Record the exit event; this must happen BEFORE the threshold check so
	// the current exit is included in ErrorExitsSince.
	if err := m.st.RecordWorkerEvent(context.Background(), target, pid, store.EventExited, &exitCode); err != nil {
		log.Printf("manager: record exited event for %s/%s: %v", sub.Driver, sub.Source, err)
	}

	// Check whether the subscription should transition to error state.
	if exitCode != 0 {
		if m.checkErrorThreshold(context.Background(), subID, sub, g) {
			return // threshold exceeded; subscription now in StateError
		}
	}

	// Only restart if the subscription is still active and tracked.
	m.mu.RLock()
	_, stillTracked := m.states[subID]
	m.mu.RUnlock()

	if stillTracked && sub.State == store.StateActive {
		d := parseDuration(g.RestartDelay, 30*time.Second)
		state.addLog(fmt.Sprintf("[system] restarting in %s", d))
		time.AfterFunc(d, func() { m.startWorker(subID) })
	}
}

// checkErrorThreshold queries the store for the windowed error count and
// transitions the subscription to StateError if the threshold is exceeded.
// Returns true if the threshold was exceeded (caller should stop retrying).
func (m *Manager) checkErrorThreshold(ctx context.Context, subID int64, sub *store.Subscription, g config.Data) bool {
	threshold := g.ErrorThreshold
	if threshold <= 0 {
		threshold = 5
	}
	errorWindow := parseDuration(g.ErrorWindow, 5*time.Minute)

	// The "since" boundary is the more recent of:
	//   • the start of the current intentional stop/start cycle, or
	//   • now − errorWindow.
	cycleStart, err := m.st.CycleResetAt(ctx, subID)
	if err != nil {
		log.Printf("manager: CycleResetAt for %s/%s: %v", sub.Driver, sub.Source, err)
	}
	windowStart := time.Now().Add(-errorWindow)
	since := windowStart
	if !cycleStart.IsZero() && cycleStart.After(since) {
		since = cycleStart
	}

	errCount, err := m.st.ErrorExitsSince(ctx, subID, since)
	if err != nil {
		log.Printf("manager: ErrorExitsSince for %s/%s: %v", sub.Driver, sub.Source, err)
		return false
	}

	if errCount < threshold {
		return false
	}

	reason := fmt.Sprintf(
		"%d non-intentional error exit(s) within %s (threshold: %d)",
		errCount, errorWindow, threshold)
	log.Printf("manager: %s/%s exceeded error threshold: %s", sub.Driver, sub.Source, reason)

	state := m.stateByID(subID)
	if state != nil {
		state.addLog("[system] error threshold reached — recording stopped. Use /reset-error to retry.")
		state.mu.Lock()
		state.sub.State = store.StateError
		state.sub.ErrorMessage = reason
		state.mu.Unlock()
	}

	if err := m.st.SetState(ctx, subID, store.StateError, reason); err != nil {
		log.Printf("manager: set error state for %s/%s: %v", sub.Driver, sub.Source, err)
	}
	return true
}

// ---- worker lifecycle ----

func (m *Manager) startWorker(subID int64) {
	state := m.stateByID(subID)
	if state == nil {
		return
	}

	state.mu.Lock()
	if state.pid > 0 || state.starting {
		state.mu.Unlock()
		return
	}
	if state.sub.State != store.StateActive {
		state.mu.Unlock()
		return
	}
	state.starting = true
	state.mu.Unlock()

	defer func() {
		state.mu.Lock()
		state.starting = false
		state.mu.Unlock()
	}()

	g := m.cfg.Get()
	args := buildArgs(state.sub.Driver, state.sub.Source, g)

	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
	defer cancel()

	pid, err := m.oc.Start(ctx, args)
	if err != nil {
		log.Printf("manager: start worker for %s/%s: %v", state.sub.Driver, state.sub.Source, err)
		state.addLog(fmt.Sprintf("[system] start failed: %v", err))
		// Don't count overseer-start failures as recorder errors; the
		// reconciler will retry.
		return
	}

	state.mu.Lock()
	state.pid = pid
	state.mu.Unlock()

	m.mu.Lock()
	m.pidIndex[pid] = subID
	m.mu.Unlock()

	target := store.Target{Sub: state.sub, Config: g}
	if err := m.st.RecordWorkerEvent(ctx, target, pid, store.EventStarted, nil); err != nil {
		log.Printf("manager: record started event for %s/%s: %v", state.sub.Driver, state.sub.Source, err)
	}

	state.addLog(fmt.Sprintf("[system] recorder started (pid=%d)", pid))
	log.Printf("manager: started worker pid=%d for %s/%s", pid, state.sub.Driver, state.sub.Source)
}

// stopWorkerIntentionally records EventStopped (establishing a cycle boundary
// and marking the subsequent exit as intentional) and sends SIGTERM.
func (m *Manager) stopWorkerIntentionally(state *subState, pid int) {
	g := m.cfg.Get()
	target := store.Target{Sub: state.sub, Config: g}

	// Record BEFORE sending the signal so the exit that follows can be
	// correlated by PID and excluded from error counting.
	if err := m.st.RecordWorkerEvent(context.Background(), target, pid, store.EventStopped, nil); err != nil {
		log.Printf("manager: record stopped event for %s/%s: %v",
			state.sub.Driver, state.sub.Source, err)
	}

	// Clear the PID and pidIndex now; OnExited will be a no-op for this PID.
	state.mu.Lock()
	if state.pid == pid {
		state.pid = 0
	}
	state.mu.Unlock()

	m.mu.Lock()
	delete(m.pidIndex, pid)
	m.mu.Unlock()

	if err := m.oc.Stop(pid); err != nil {
		log.Printf("manager: stop pid=%d: %v", pid, err)
	}
}

// ---- public API ----

// Subscribe creates (or reactivates) a subscription and starts a worker.
func (m *Manager) Subscribe(ctx context.Context, driver, source string) (*SubscriptionStatus, error) {
	sub, err := m.st.CreateSubscription(ctx, driver, source)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	state, exists := m.states[sub.ID]
	if !exists {
		state = &subState{sub: sub}
		m.states[sub.ID] = state
	} else {
		state.mu.Lock()
		state.sub = sub
		state.mu.Unlock()
	}
	m.mu.Unlock()

	go m.startWorker(sub.ID)
	return m.statusFor(sub.ID), nil
}

// Unsubscribe marks the subscription inactive and stops its worker.
func (m *Manager) Unsubscribe(ctx context.Context, driver, source string) error {
	sub, err := m.st.GetSubscriptionByKey(ctx, driver, source)
	if err != nil {
		return err
	}
	if sub == nil || sub.State == store.StateInactive {
		return fmt.Errorf("subscription %s/%s not found", driver, source)
	}

	if err := m.st.SetState(ctx, sub.ID, store.StateInactive, ""); err != nil {
		return err
	}

	m.mu.Lock()
	state, ok := m.states[sub.ID]
	if ok {
		delete(m.states, sub.ID)
	}
	m.mu.Unlock()

	if ok {
		state.mu.Lock()
		pid := state.pid
		state.mu.Unlock()
		if pid > 0 {
			m.stopWorkerIntentionally(state, pid)
		}
	}
	return nil
}

// Pause stops the worker and marks the subscription paused.
// Records EventStopped to establish the cycle boundary.
func (m *Manager) Pause(ctx context.Context, driver, source string) (*SubscriptionStatus, error) {
	sub, state, err := m.lookupByKey(ctx, driver, source)
	if err != nil {
		return nil, err
	}

	if err := m.st.SetState(ctx, sub.ID, store.StatePaused, ""); err != nil {
		return nil, err
	}

	state.mu.Lock()
	state.sub.State = store.StatePaused
	state.sub.ErrorMessage = ""
	pid := state.pid
	state.mu.Unlock()

	if pid > 0 {
		m.stopWorkerIntentionally(state, pid)
	}

	return m.statusFor(sub.ID), nil
}

// Resume clears paused/error state and starts a worker.
// Does NOT record EventStopped — the previous pause already established the
// cycle boundary; the upcoming EventStarted from startWorker completes it.
func (m *Manager) Resume(ctx context.Context, driver, source string) (*SubscriptionStatus, error) {
	sub, state, err := m.lookupByKey(ctx, driver, source)
	if err != nil {
		return nil, err
	}

	if err := m.st.SetState(ctx, sub.ID, store.StateActive, ""); err != nil {
		return nil, err
	}

	state.mu.Lock()
	state.sub.State = store.StateActive
	state.sub.ErrorMessage = ""
	state.mu.Unlock()

	go m.startWorker(sub.ID)
	return m.statusFor(sub.ID), nil
}

// ResetError records EventStopped (as a cycle-reset marker even though no
// process is running) then activates the subscription and starts a worker.
// The EventStopped ensures the subsequent EventStarted forms a clean cycle,
// so the error window starts fresh from that point.
func (m *Manager) ResetError(ctx context.Context, driver, source string) (*SubscriptionStatus, error) {
	sub, state, err := m.lookupByKey(ctx, driver, source)
	if err != nil {
		return nil, err
	}
	if sub.State != store.StateError {
		return nil, fmt.Errorf("subscription %s/%s is not in error state", driver, source)
	}

	// Synthetic stopped event — no live process, pid=0 is fine; we just need
	// the timestamp marker so CycleResetAt has a 'stopped' to pivot from.
	g := m.cfg.Get()
	target := store.Target{Sub: sub, Config: g}
	if err := m.st.RecordWorkerEvent(ctx, target, 0, store.EventStopped, nil); err != nil {
		log.Printf("manager: record reset stopped event for %s/%s: %v", driver, source, err)
	}

	if err := m.st.SetState(ctx, sub.ID, store.StateActive, ""); err != nil {
		return nil, err
	}

	state.mu.Lock()
	state.sub.State = store.StateActive
	state.sub.ErrorMessage = ""
	state.mu.Unlock()

	go m.startWorker(sub.ID)
	return m.statusFor(sub.ID), nil
}

// Restart kills the current worker.  Records EventStopped so the exit is
// marked intentional and the restart begins a new cycle.
func (m *Manager) Restart(ctx context.Context, driver, source string) (*SubscriptionStatus, error) {
	sub, state, err := m.lookupByKey(ctx, driver, source)
	if err != nil {
		return nil, err
	}

	state.mu.Lock()
	pid := state.pid
	state.mu.Unlock()

	if pid == 0 {
		return nil, fmt.Errorf("subscription %s/%s has no running worker", driver, source)
	}

	m.stopWorkerIntentionally(state, pid)

	// startWorker will be triggered by OnExited, but since we cleared pidIndex
	// before OnExited fires, OnExited won't know about this PID.  Schedule the
	// restart directly.
	g := m.cfg.Get()
	d := parseDuration(g.RestartDelay, 30*time.Second)
	state.addLog(fmt.Sprintf("[system] restarting in %s (user request)", d))
	time.AfterFunc(d, func() { m.startWorker(sub.ID) })

	return m.statusFor(sub.ID), nil
}

// GetStatus returns the current status of a subscription.
func (m *Manager) GetStatus(ctx context.Context, driver, source string) (*SubscriptionStatus, error) {
	sub, err := m.st.GetSubscriptionByKey(ctx, driver, source)
	if err != nil {
		return nil, err
	}
	if sub == nil || sub.State == store.StateInactive {
		return nil, fmt.Errorf("subscription %s/%s not found", driver, source)
	}

	m.mu.RLock()
	_, tracked := m.states[sub.ID]
	m.mu.RUnlock()

	if !tracked {
		return &SubscriptionStatus{Subscription: sub, WorkerState: "idle", Logs: []string{}}, nil
	}
	return m.statusFor(sub.ID), nil
}

// ListVisible returns status for all non-inactive subscriptions.
func (m *Manager) ListVisible(ctx context.Context) ([]*SubscriptionStatus, error) {
	subs, err := m.st.ListVisible(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*SubscriptionStatus, 0, len(subs))
	for _, sub := range subs {
		m.mu.RLock()
		_, tracked := m.states[sub.ID]
		m.mu.RUnlock()

		if tracked {
			out = append(out, m.statusFor(sub.ID))
		} else {
			out = append(out, &SubscriptionStatus{Subscription: sub, WorkerState: "idle", Logs: []string{}})
		}
	}
	return out, nil
}

// GetLogs returns the in-memory log buffer for a subscription.
func (m *Manager) GetLogs(ctx context.Context, driver, source string) ([]string, error) {
	sub, err := m.st.GetSubscriptionByKey(ctx, driver, source)
	if err != nil {
		return nil, err
	}
	if sub == nil || sub.State == store.StateInactive {
		return nil, fmt.Errorf("subscription %s/%s not found", driver, source)
	}
	state := m.stateByID(sub.ID)
	if state == nil {
		return []string{}, nil
	}
	return state.getLogs(), nil
}

// GetWorkerEvents returns persisted worker lifecycle events for a subscription.
func (m *Manager) GetWorkerEvents(ctx context.Context, driver, source string, limit int) ([]store.WorkerEvent, error) {
	sub, err := m.st.GetSubscriptionByKey(ctx, driver, source)
	if err != nil {
		return nil, err
	}
	if sub == nil || sub.State == store.StateInactive {
		return nil, fmt.Errorf("subscription %s/%s not found", driver, source)
	}
	return m.st.RecentWorkerEvents(ctx, sub.ID, limit)
}

func (m *Manager) GetConfig() config.Data             { return m.cfg.Get() }
func (m *Manager) SetConfig(d config.Data) error      { return m.cfg.Set(d) }
func (m *Manager) GetOverseerClient() *overseer.Client { return m.oc }

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
	workers, err := m.oc.List(ctx)
	if err != nil {
		log.Printf("manager: reconcile: overseer list: %v", err)
		return
	}

	runningPIDs := make(map[int]struct{})
	for _, w := range workers {
		if w.State == "running" {
			runningPIDs[w.PID] = struct{}{}
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
		subState := state.sub.State
		pid := state.pid
		starting := state.starting
		driver := state.sub.Driver
		source := state.sub.Source
		state.mu.Unlock()

		if subState != store.StateActive || starting {
			continue
		}
		if pid > 0 {
			if _, alive := runningPIDs[pid]; alive {
				continue
			}
			log.Printf("manager: reconcile: pid=%d for %s/%s gone, restarting", pid, driver, source)
			state.mu.Lock()
			if state.pid == pid {
				state.pid = 0
			}
			state.mu.Unlock()
			m.mu.Lock()
			if m.pidIndex[pid] == id {
				delete(m.pidIndex, pid)
			}
			m.mu.Unlock()
			state.addLog("[system] worker gone (detected by reconciler), restarting")
		}
		go m.startWorker(id)
	}
}

// ---- internal helpers ----

func (m *Manager) stateByPID(pid int) *subState {
	m.mu.RLock()
	id, ok := m.pidIndex[pid]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return m.stateByID(id)
}

func (m *Manager) stateByID(id int64) *subState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.states[id]
}

func (m *Manager) lookupByKey(ctx context.Context, driver, source string) (*store.Subscription, *subState, error) {
	sub, err := m.st.GetSubscriptionByKey(ctx, driver, source)
	if err != nil {
		return nil, nil, err
	}
	if sub == nil || sub.State == store.StateInactive {
		return nil, nil, fmt.Errorf("subscription %s/%s not found", driver, source)
	}

	m.mu.RLock()
	state, ok := m.states[sub.ID]
	m.mu.RUnlock()

	if !ok {
		state = &subState{sub: sub}
		m.mu.Lock()
		m.states[sub.ID] = state
		m.mu.Unlock()
	}
	return sub, state, nil
}

func (m *Manager) statusFor(subID int64) *SubscriptionStatus {
	state := m.stateByID(subID)
	if state == nil {
		return nil
	}

	state.mu.Lock()
	sub := state.sub
	pid := state.pid
	starting := state.starting
	logs := make([]string, len(state.logs))
	copy(logs, state.logs)
	state.mu.Unlock()

	workerState := "idle"
	switch {
	case starting:
		workerState = "starting"
	case pid > 0:
		workerState = "recording"
	}

	return &SubscriptionStatus{
		Subscription: sub,
		WorkerState:  workerState,
		PID:          pid,
		Logs:         logs,
	}
}

// ---- args builder ----

func buildArgs(driver, source string, g config.Data) []string {
	args := []string{
		"--source", source,
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

func extractDriverSource(args []string) (driver, source string) {
	for i, arg := range args {
		if i+1 >= len(args) {
			break
		}
		switch arg {
		case "--driver", "-d":
			driver = args[i+1]
		case "--source", "-s":
			source = args[i+1]
		}
	}
	return
}

func subKey(driver, source string) string { return driver + "/" + source }

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
