// Package overseer provides a persistent WebSocket client for sticky-overseer.
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

// WorkerInfo mirrors the WorkerInfo type returned by sticky-overseer.
type WorkerInfo struct {
	PID         int        `json:"pid"`
	Command     string     `json:"command"`
	Args        []string   `json:"args"`
	State       string     `json:"state"` // "running" | "exited"
	StartedAt   time.Time  `json:"started_at"`
	ExitedAt    *time.Time `json:"exited_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	LastEventAt *time.Time `json:"last_event_at,omitempty"`
}

// Handler receives broadcast events from the overseer.
type Handler struct {
	OnOutput func(pid int, stream, data string, ts time.Time)
	OnExited func(pid int, exitCode int, ts time.Time)
}

// startResult carries the outcome of a start request.
type startResult struct {
	pid int
	err error
}

// Client maintains a persistent WebSocket connection to a sticky-overseer instance.
// It automatically reconnects on failure and serialises all writes.
type Client struct {
	url     string
	handler Handler

	// conn is the active connection; nil when disconnected.
	connMu  sync.Mutex
	conn    *websocket.Conn
	writeMu sync.Mutex // serialises writes to conn

	// pending start requests: id → chan startResult
	startPending sync.Map
	// pending list requests: id → chan []WorkerInfo
	listPending sync.Map

	idSeq atomic.Int64

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

// Run connects and reconnects until ctx is cancelled.
// Call this in a dedicated goroutine.
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

		// Fail all in-flight requests.
		c.startPending.Range(func(k, v any) bool {
			v.(chan startResult) <- startResult{err: fmt.Errorf("overseer: connection lost")}
			c.startPending.Delete(k)
			return true
		})
		c.listPending.Range(func(k, v any) bool {
			v.(chan []WorkerInfo) <- nil
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

// inbound is the superset of all messages sent by the overseer.
type inbound struct {
	Type     string       `json:"type"`
	ID       string       `json:"id,omitempty"`
	PID      int          `json:"pid,omitempty"`
	Stream   string       `json:"stream,omitempty"`
	Data     string       `json:"data,omitempty"`
	ExitCode int          `json:"exit_code,omitempty"`
	Message  string       `json:"message,omitempty"`
	Workers  []WorkerInfo `json:"workers,omitempty"`
	TS       time.Time    `json:"ts"`
}

func (c *Client) dispatch(raw []byte) {
	var msg inbound
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("overseer: bad message: %v", err)
		return
	}

	switch msg.Type {
	case "started":
		if ch, ok := c.startPending.LoadAndDelete(msg.ID); ok {
			ch.(chan startResult) <- startResult{pid: msg.PID}
		}

	case "workers":
		if ch, ok := c.listPending.LoadAndDelete(msg.ID); ok {
			ch.(chan []WorkerInfo) <- msg.Workers
		}

	case "error":
		if msg.ID != "" {
			if ch, ok := c.startPending.LoadAndDelete(msg.ID); ok {
				ch.(chan startResult) <- startResult{err: fmt.Errorf("overseer: %s", msg.Message)}
				return
			}
			if ch, ok := c.listPending.LoadAndDelete(msg.ID); ok {
				ch.(chan []WorkerInfo) <- nil
			}
		}

	case "output":
		if c.handler.OnOutput != nil {
			c.handler.OnOutput(msg.PID, msg.Stream, msg.Data, msg.TS)
		}

	case "exited":
		if c.handler.OnExited != nil {
			c.handler.OnExited(msg.PID, msg.ExitCode, msg.TS)
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

// Start asks the overseer to spawn a new process with the given args.
// The command defaults to the overseer's pinned command (sticky-recorder).
func (c *Client) Start(ctx context.Context, args []string) (int, error) {
	id := c.nextID()
	ch := make(chan startResult, 1)
	c.startPending.Store(id, ch)

	err := c.send(map[string]any{
		"type": "start",
		"id":   id,
		"args": args,
	})
	if err != nil {
		c.startPending.Delete(id)
		return 0, err
	}

	select {
	case res := <-ch:
		return res.pid, res.err
	case <-ctx.Done():
		c.startPending.Delete(id)
		return 0, ctx.Err()
	case <-time.After(15 * time.Second):
		c.startPending.Delete(id)
		return 0, fmt.Errorf("timeout waiting for start confirmation")
	}
}

// Stop sends SIGTERM to the process with the given PID.
func (c *Client) Stop(pid int) error {
	return c.send(map[string]any{
		"type": "stop",
		"pid":  pid,
	})
}

// List returns all workers tracked by the overseer.
func (c *Client) List(ctx context.Context) ([]WorkerInfo, error) {
	id := c.nextID()
	ch := make(chan []WorkerInfo, 1)
	c.listPending.Store(id, ch)

	err := c.send(map[string]any{
		"type": "list",
		"id":   id,
	})
	if err != nil {
		c.listPending.Delete(id)
		return nil, err
	}

	select {
	case workers := <-ch:
		if workers == nil {
			return nil, fmt.Errorf("list request failed or connection lost")
		}
		return workers, nil
	case <-ctx.Done():
		c.listPending.Delete(id)
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		c.listPending.Delete(id)
		return nil, fmt.Errorf("timeout waiting for workers list")
	}
}
