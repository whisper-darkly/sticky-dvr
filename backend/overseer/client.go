// Package overseer provides a persistent WebSocket client for sticky-overseer.
// Protocol reference: sticky-overseer messages.go (task_id-keyed, v0.3+).
package overseer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// RetryPolicy mirrors the overseer's RetryPolicy (duration strings).
type RetryPolicy struct {
	RestartDelay   string `json:"restart_delay,omitempty"`
	ErrorWindow    string `json:"error_window,omitempty"`
	ErrorThreshold int    `json:"error_threshold,omitempty"`
}

// TaskInfo mirrors the overseer's TaskInfo response.
type TaskInfo struct {
	TaskID        string     `json:"task_id"`
	Command       string     `json:"command"`
	Args          []string   `json:"args"`
	State         string     `json:"state"`        // active | stopped | errored
	WorkerState   string     `json:"worker_state"` // running | exited
	CurrentPID    int        `json:"current_pid,omitempty"`
	RestartCount  int        `json:"restart_count"`
	CreatedAt     time.Time  `json:"created_at"`
	LastStartedAt *time.Time `json:"last_started_at,omitempty"`
	LastExitedAt  *time.Time `json:"last_exited_at,omitempty"`
	LastExitCode  *int       `json:"last_exit_code,omitempty"`
	ErrorMessage  string     `json:"error_message,omitempty"`
}

// Handler receives broadcast events from the overseer.
type Handler struct {
	OnStarted    func(taskID string, pid int, restartOf int, ts time.Time)
	OnOutput     func(taskID string, pid int, stream, data string, ts time.Time)
	OnExited     func(taskID string, pid int, exitCode int, intentional bool, ts time.Time)
	OnRestarting func(taskID string, pid int, attempt int, ts time.Time)
	OnErrored    func(taskID string, pid int, exitCount int, ts time.Time)
}

// inbound is the superset of all messages sent by the overseer.
type inbound struct {
	Type         string     `json:"type"`
	ID           string     `json:"id,omitempty"`
	TaskID       string     `json:"task_id,omitempty"`
	PID          int        `json:"pid,omitempty"`
	RestartOf    int        `json:"restart_of,omitempty"`
	Stream       string     `json:"stream,omitempty"`
	Data         string     `json:"data,omitempty"`
	ExitCode     int        `json:"exit_code,omitempty"`
	Intentional  bool       `json:"intentional,omitempty"`
	ExitCount    int        `json:"exit_count,omitempty"`
	Attempt      int        `json:"attempt,omitempty"`
	Message      string     `json:"message,omitempty"`
	Tasks        []TaskInfo `json:"tasks,omitempty"`
	TS           time.Time  `json:"ts"`
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

	startPending sync.Map // request id → chan startResult
	listPending  sync.Map // request id → chan []TaskInfo

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

	case "error":
		if msg.ID != "" {
			if ch, ok := c.startPending.LoadAndDelete(msg.ID); ok {
				ch.(chan startResult) <- startResult{err: fmt.Errorf("overseer: %s", msg.Message)}
				return
			}
			if ch, ok := c.listPending.LoadAndDelete(msg.ID); ok {
				ch.(chan []TaskInfo) <- nil
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

// Start asks the overseer to spawn a task with the given task_id, args, and retry policy.
// If taskID is empty, the overseer will auto-generate one (returned in result).
func (c *Client) Start(ctx context.Context, taskID string, args []string, rp *RetryPolicy) (string, int, error) {
	id := c.nextID()
	ch := make(chan startResult, 1)
	c.startPending.Store(id, ch)

	msg := map[string]any{
		"type":    "start",
		"id":      id,
		"task_id": taskID,
		"args":    args,
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

// Stop sends a stop command for the given task_id.
func (c *Client) Stop(taskID string) error {
	return c.send(map[string]any{
		"type":    "stop",
		"task_id": taskID,
	})
}

// Reset clears the errored state for a task and restarts it.
func (c *Client) Reset(taskID string) error {
	return c.send(map[string]any{
		"type":    "reset",
		"task_id": taskID,
	})
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
