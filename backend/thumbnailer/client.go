// Package thumbnailer provides a per-request WebSocket client for the sticky-thumbnailer service.
// The thumbnailer uses sticky-overseer v2 protocol at /ws.
package thumbnailer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// GlobalMetrics holds aggregate counters from the thumbnailer's in-memory state.
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

// PoolInfo holds a point-in-time snapshot of the thumbnailer pool state.
type PoolInfo struct {
	Limit      int `json:"limit"`
	Running    int `json:"running"`
	QueueDepth int `json:"queue_depth"`
}

// TaskInfo describes a single thumbnailer task.
type TaskInfo struct {
	TaskID       string `json:"task_id"`
	File         string `json:"file"`
	State        string `json:"state"`
	RestartCount int    `json:"restart_count"`
}

// taskInfo mirrors the overseer v2 TaskInfo for thumbnailer tasks.
type taskInfo struct {
	TaskID       string            `json:"task_id"`
	Action       string            `json:"action"`
	Params       map[string]string `json:"params,omitempty"`
	State        string            `json:"state"`
	RestartCount int               `json:"restart_count"`
}

// Client is a per-request WebSocket client for the sticky-thumbnailer service.
type Client struct {
	wsURL string
	idSeq atomic.Int64
}

// NewClient returns a Client targeting the given WebSocket URL (e.g. "ws://thumbnailer:8080/ws").
func NewClient(wsURL string) *Client {
	return &Client{wsURL: strings.TrimRight(wsURL, "/")}
}

func (c *Client) nextID() string {
	return fmt.Sprintf("r%d", c.idSeq.Add(1))
}

// GetMetrics dials the thumbnailer and returns global aggregate counters.
// Returns nil, nil if the thumbnailer is unreachable (graceful degradation).
func (c *Client) GetMetrics(ctx context.Context) (*GlobalMetrics, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return nil, nil
	}
	defer conn.Close()

	reqID := c.nextID()
	req, _ := json.Marshal(map[string]any{"type": "metrics", "id": reqID})
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		return nil, nil
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return nil, nil
		}
		var msg struct {
			Type   string           `json:"type"`
			ID     string           `json:"id"`
			Global *json.RawMessage `json:"global,omitempty"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Type == "metrics" && msg.ID == reqID {
			if msg.Global == nil {
				return nil, nil
			}
			var gm GlobalMetrics
			if err := json.Unmarshal(*msg.Global, &gm); err != nil {
				return nil, nil
			}
			return &gm, nil
		}
	}
}

// GetPoolInfo dials the thumbnailer and returns a snapshot of global pool state.
// Returns nil, nil if the thumbnailer is unreachable (graceful degradation).
func (c *Client) GetPoolInfo(ctx context.Context) (*PoolInfo, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return nil, nil
	}
	defer conn.Close()

	reqID := c.nextID()
	req, _ := json.Marshal(map[string]any{"type": "pool_info", "id": reqID})
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		return nil, nil
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return nil, nil
		}
		var msg struct {
			Type string           `json:"type"`
			ID   string           `json:"id"`
			Pool *json.RawMessage `json:"pool,omitempty"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Type == "pool_info" && msg.ID == reqID {
			if msg.Pool == nil {
				return nil, nil
			}
			var pi PoolInfo
			if err := json.Unmarshal(*msg.Pool, &pi); err != nil {
				return nil, nil
			}
			return &pi, nil
		}
	}
}

// GetTasks dials the thumbnailer and returns all active/queued/errored tasks.
// Returns an empty slice if the thumbnailer is unreachable (graceful degradation).
func (c *Client) GetTasks(ctx context.Context) ([]TaskInfo, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return []TaskInfo{}, nil
	}
	defer conn.Close()

	reqID := c.nextID()
	req, _ := json.Marshal(map[string]any{"type": "list", "id": reqID})
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		return []TaskInfo{}, nil
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return []TaskInfo{}, nil
		}
		var msg struct {
			Type  string     `json:"type"`
			ID    string     `json:"id"`
			Tasks []taskInfo `json:"tasks"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Type == "tasks" && msg.ID == reqID {
			tasks := make([]TaskInfo, 0, len(msg.Tasks))
			for _, t := range msg.Tasks {
				tasks = append(tasks, TaskInfo{
					TaskID:       t.TaskID,
					File:         t.Params["file"],
					State:        t.State,
					RestartCount: t.RestartCount,
				})
			}
			return tasks, nil
		}
	}
}
