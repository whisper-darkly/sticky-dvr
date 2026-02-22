// Package overseer provides a persistent WebSocket client for sticky-overseer.
// Protocol reference: sticky-overseer messages.go (task_id-keyed, v0.3+).
package overseer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// debugLog is set to true when LOG_DEBUG=1; enables verbose overseer message logging.
var debugLog = os.Getenv("LOG_DEBUG") == "1"

// RetryPolicy mirrors the overseer's RetryPolicy (duration strings).
type RetryPolicy struct {
	RestartDelay   string `json:"restart_delay,omitempty"`
	ErrorWindow    string `json:"error_window,omitempty"`
	ErrorThreshold int    `json:"error_threshold,omitempty"`
}

// TaskInfo mirrors the overseer's TaskInfo response.
type TaskInfo struct {
	TaskID        string            `json:"task_id"`
	Action        string            `json:"action"`
	Params        map[string]string `json:"params,omitempty"`
	State         string            `json:"state"`        // active | stopped | errored
	WorkerState   string            `json:"worker_state"` // running | exited
	CurrentPID    int               `json:"current_pid,omitempty"`
	RestartCount  int               `json:"restart_count"`
	CreatedAt     time.Time         `json:"created_at"`
	LastStartedAt *time.Time        `json:"last_started_at,omitempty"`
	LastExitedAt  *time.Time        `json:"last_exited_at,omitempty"`
	LastExitCode  *int              `json:"last_exit_code,omitempty"`
	ErrorMessage  string            `json:"error_message,omitempty"`
}

// Handler receives broadcast events from the overseer.
type Handler struct {
	OnStarted    func(taskID string, pid int, restartOf int, ts time.Time)
	OnOutput     func(taskID string, pid int, stream, data string, ts time.Time)
	OnExited     func(taskID string, pid int, exitCode int, intentional bool, ts time.Time)
	OnRestarting func(taskID string, pid int, attempt int, ts time.Time)
	OnErrored    func(taskID string, pid int, exitCount int, ts time.Time)
	// OnConnected is called each time a WebSocket connection to the overseer is established.
	// Use it to re-subscribe to existing task IDs after reconnect.
	OnConnected func()
}

// GlobalMetrics holds aggregate counters from the overseer's in-memory state.
type GlobalMetrics struct {
	TasksStarted     int64 `json:"tasks_started"`
	TasksCompleted   int64 `json:"tasks_completed"`
	TasksErrored     int64 `json:"tasks_errored"`
	TasksRestarted   int64 `json:"tasks_restarted"`
	TotalOutputLines int64 `json:"total_output_lines"`
	Enqueued         int64 `json:"enqueued"`
	Dequeued         int64 `json:"dequeued"`
	Displaced        int64 `json:"displaced"`
	Expired          int64 `json:"expired"`
}

// PoolInfo holds a point-in-time snapshot of the overseer pool state.
type PoolInfo struct {
	Limit      int `json:"limit"`
	Running    int `json:"running"`
	QueueDepth int `json:"queue_depth"`
}

// inbound is the superset of all messages sent by the overseer.
type inbound struct {
	Type         string           `json:"type"`
	ID           string           `json:"id,omitempty"`
	TaskID       string           `json:"task_id,omitempty"`
	PID          int              `json:"pid,omitempty"`
	RestartOf    int              `json:"restart_of,omitempty"`
	Stream       string           `json:"stream,omitempty"`
	Data         string           `json:"data,omitempty"`
	ExitCode     int              `json:"exit_code,omitempty"`
	Intentional  bool             `json:"intentional,omitempty"`
	ExitCount    int              `json:"exit_count,omitempty"`
	Attempt      int              `json:"attempt,omitempty"`
	Message      string           `json:"message,omitempty"`
	Tasks        []TaskInfo       `json:"tasks,omitempty"`
	Global       *json.RawMessage `json:"global,omitempty"`
	Pool         *json.RawMessage `json:"pool,omitempty"`
	TS           time.Time        `json:"ts"`
}

type startResult struct {
	taskID string
	pid    int
	err    error
}

// Client maintains a persistent WebSocket connection to a sticky-overseer instance.
type Client struct {
	url     string
	handler Handler

	connMu  sync.Mutex
	conn    *websocket.Conn
	writeMu sync.Mutex

	startPending   sync.Map // request id → chan startResult
	listPending    sync.Map // request id → chan []TaskInfo
	metricsPending sync.Map // request id → chan *GlobalMetrics
	poolPending    sync.Map // request id → chan *PoolInfo

	idSeq          atomic.Int64
	reconnectDelay time.Duration
}

// NewClient creates a Client targeting the given WebSocket URL.
func NewClient(url string, h Handler) *Client {
	return &Client{
		url:            url,
		handler:        h,
		reconnectDelay: 5 * time.Second,
	}
}

// Run connects and reconnects until ctx is cancelled. Call in a dedicated goroutine.
func (c *Client) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := c.connect(ctx); err != nil && ctx.Err() == nil {
			log.Printf("overseer: %v — retrying in %s", err, c.reconnectDelay)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.reconnectDelay):
		}
	}
}

// IsConnected reports whether a connection is currently active.
func (c *Client) IsConnected() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.conn != nil
}

func (c *Client) connect(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.url, err)
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	log.Printf("overseer: connected to %s", c.url)

	// Notify the handler so it can re-subscribe to any claimed tasks.
	if c.handler.OnConnected != nil {
		go c.handler.OnConnected()
	}

	defer func() {
		conn.Close()
		c.connMu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.connMu.Unlock()

		c.startPending.Range(func(k, v any) bool {
			v.(chan startResult) <- startResult{err: fmt.Errorf("overseer: connection lost")}
			c.startPending.Delete(k)
			return true
		})
		c.listPending.Range(func(k, v any) bool {
			v.(chan []TaskInfo) <- nil
			c.listPending.Delete(k)
			return true
		})
		c.metricsPending.Range(func(k, v any) bool {
			v.(chan *GlobalMetrics) <- nil
			c.metricsPending.Delete(k)
			return true
		})
		c.poolPending.Range(func(k, v any) bool {
			v.(chan *PoolInfo) <- nil
			c.poolPending.Delete(k)
			return true
		})

		log.Printf("overseer: disconnected from %s", c.url)
	}()

	for {
		if ctx.Err() != nil {
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return nil
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		c.dispatch(raw)
	}
}

func (c *Client) dispatch(raw []byte) {
	var msg inbound
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("overseer: bad message: %v", err)
		return
	}

	if debugLog && msg.Type != "output" {
		// Log all non-output events when debug mode is enabled (output is too frequent).
		log.Printf("overseer: recv type=%q task_id=%q pid=%d exit_code=%d intentional=%v",
			msg.Type, msg.TaskID, msg.PID, msg.ExitCode, msg.Intentional)
	}

	switch msg.Type {
	case "started":
		if msg.ID != "" {
			if ch, ok := c.startPending.LoadAndDelete(msg.ID); ok {
				ch.(chan startResult) <- startResult{taskID: msg.TaskID, pid: msg.PID}
				return
			}
		}
		if c.handler.OnStarted != nil {
			c.handler.OnStarted(msg.TaskID, msg.PID, msg.RestartOf, msg.TS)
		}

	case "tasks":
		if ch, ok := c.listPending.LoadAndDelete(msg.ID); ok {
			ch.(chan []TaskInfo) <- msg.Tasks
		}

	case "metrics":
		if ch, ok := c.metricsPending.LoadAndDelete(msg.ID); ok {
			if msg.Global != nil {
				var gm GlobalMetrics
				if err := json.Unmarshal(*msg.Global, &gm); err == nil {
					ch.(chan *GlobalMetrics) <- &gm
					return
				}
			}
			ch.(chan *GlobalMetrics) <- nil
		}

	case "pool_info":
		if ch, ok := c.poolPending.LoadAndDelete(msg.ID); ok {
			if msg.Pool != nil {
				var pi PoolInfo
				if err := json.Unmarshal(*msg.Pool, &pi); err == nil {
					ch.(chan *PoolInfo) <- &pi
					return
				}
			}
			ch.(chan *PoolInfo) <- nil
		}

	case "error":
		if msg.ID != "" {
			if ch, ok := c.startPending.LoadAndDelete(msg.ID); ok {
				ch.(chan startResult) <- startResult{err: fmt.Errorf("overseer: %s", msg.Message)}
				return
			}
			if ch, ok := c.listPending.LoadAndDelete(msg.ID); ok {
				ch.(chan []TaskInfo) <- nil
			}
			if ch, ok := c.metricsPending.LoadAndDelete(msg.ID); ok {
				ch.(chan *GlobalMetrics) <- nil
			}
			if ch, ok := c.poolPending.LoadAndDelete(msg.ID); ok {
				ch.(chan *PoolInfo) <- nil
			}
		}

	case "output":
		if c.handler.OnOutput != nil {
			c.handler.OnOutput(msg.TaskID, msg.PID, msg.Stream, msg.Data, msg.TS)
		}

	case "exited":
		if c.handler.OnExited != nil {
			c.handler.OnExited(msg.TaskID, msg.PID, msg.ExitCode, msg.Intentional, msg.TS)
		}

	case "restarting":
		if c.handler.OnRestarting != nil {
			c.handler.OnRestarting(msg.TaskID, msg.PID, msg.Attempt, msg.TS)
		}

	case "errored":
		if c.handler.OnErrored != nil {
			c.handler.OnErrored(msg.TaskID, msg.PID, msg.ExitCount, msg.TS)
		}

	case "subscribed", "unsubscribed":
		// Acknowledgement only — no action needed.
	}
}

func (c *Client) send(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected to overseer")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, raw)
}

func (c *Client) nextID() string {
	return fmt.Sprintf("r%d", c.idSeq.Add(1))
}

// Start asks the overseer to spawn a task with the given task_id, action, params, and retry policy.
// If taskID is empty, the overseer will auto-generate one (returned in result).
func (c *Client) Start(ctx context.Context, taskID string, action string, params map[string]string, rp *RetryPolicy) (string, int, error) {
	id := c.nextID()
	ch := make(chan startResult, 1)
	c.startPending.Store(id, ch)

	msg := map[string]any{
		"type":    "start",
		"id":      id,
		"task_id": taskID,
		"action":  action,
		"params":  params,
	}
	if rp != nil {
		msg["retry_policy"] = rp
	}

	if err := c.send(msg); err != nil {
		c.startPending.Delete(id)
		return "", 0, err
	}

	select {
	case res := <-ch:
		return res.taskID, res.pid, res.err
	case <-ctx.Done():
		c.startPending.Delete(id)
		return "", 0, ctx.Err()
	case <-time.After(20 * time.Second):
		c.startPending.Delete(id)
		return "", 0, fmt.Errorf("timeout waiting for start confirmation")
	}
}

// Subscribe registers this client as a subscriber for task-specific events (output,
// started, exited, restarting, errored) for the given taskID.
// The overseer only broadcasts task events to subscribed clients, so this must
// be called after claiming an existing task via List on reconnect.
func (c *Client) Subscribe(taskID string) error {
	return c.send(map[string]any{
		"type":    "subscribe",
		"id":      c.nextID(),
		"task_id": taskID,
	})
}

// Stop sends a stop command for the given task_id.
// The overseer does not send a success acknowledgement for stop, so this is
// fire-and-forget; a correlation ID is included so error responses can be traced.
func (c *Client) Stop(taskID string) error {
	return c.send(map[string]any{
		"type":    "stop",
		"id":      c.nextID(),
		"task_id": taskID,
	})
}

// Reset clears the errored state for a task and restarts it.
// The overseer responds with a "started" message on success or "error" on failure,
// both carrying the same correlation ID, so this call blocks until one arrives.
func (c *Client) Reset(ctx context.Context, taskID string) (int, error) {
	id := c.nextID()
	ch := make(chan startResult, 1)
	c.startPending.Store(id, ch)

	if err := c.send(map[string]any{
		"type":    "reset",
		"id":      id,
		"task_id": taskID,
	}); err != nil {
		c.startPending.Delete(id)
		return 0, err
	}

	select {
	case res := <-ch:
		return res.pid, res.err
	case <-ctx.Done():
		c.startPending.Delete(id)
		return 0, ctx.Err()
	case <-time.After(10 * time.Second):
		c.startPending.Delete(id)
		return 0, fmt.Errorf("timeout waiting for reset confirmation")
	}
}

// Metrics returns global aggregate counters from the overseer.
func (c *Client) Metrics(ctx context.Context) (*GlobalMetrics, error) {
	id := c.nextID()
	ch := make(chan *GlobalMetrics, 1)
	c.metricsPending.Store(id, ch)

	if err := c.send(map[string]any{"type": "metrics", "id": id}); err != nil {
		c.metricsPending.Delete(id)
		return nil, err
	}

	select {
	case gm := <-ch:
		if gm == nil {
			return nil, fmt.Errorf("metrics request failed or connection lost")
		}
		return gm, nil
	case <-ctx.Done():
		c.metricsPending.Delete(id)
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		c.metricsPending.Delete(id)
		return nil, fmt.Errorf("timeout waiting for metrics")
	}
}

// PoolInfo returns a snapshot of the global pool state (limit, running, queue depth).
func (c *Client) PoolInfo(ctx context.Context) (*PoolInfo, error) {
	id := c.nextID()
	ch := make(chan *PoolInfo, 1)
	c.poolPending.Store(id, ch)

	if err := c.send(map[string]any{"type": "pool_info", "id": id}); err != nil {
		c.poolPending.Delete(id)
		return nil, err
	}

	select {
	case pi := <-ch:
		if pi == nil {
			return nil, fmt.Errorf("pool_info request failed or connection lost")
		}
		return pi, nil
	case <-ctx.Done():
		c.poolPending.Delete(id)
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		c.poolPending.Delete(id)
		return nil, fmt.Errorf("timeout waiting for pool info")
	}
}

// List returns all tasks tracked by the overseer.
func (c *Client) List(ctx context.Context) ([]TaskInfo, error) {
	id := c.nextID()
	ch := make(chan []TaskInfo, 1)
	c.listPending.Store(id, ch)

	if err := c.send(map[string]any{"type": "list", "id": id}); err != nil {
		c.listPending.Delete(id)
		return nil, err
	}

	select {
	case tasks := <-ch:
		if tasks == nil {
			return nil, fmt.Errorf("list request failed or connection lost")
		}
		return tasks, nil
	case <-ctx.Done():
		c.listPending.Delete(id)
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		c.listPending.Delete(id)
		return nil, fmt.Errorf("timeout waiting for task list")
	}
}
