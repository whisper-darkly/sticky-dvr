// Package converter provides a WebSocket client for the sticky-converter (sticky-refinery) service.
// The converter uses sticky-overseer v2 protocol at /ws.
// Note: the converter only exposes queued/active/errored tasks; successfully completed
// conversions are tracked in the converter's internal DB and not exposed via this API.
package converter

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// FileInfo describes a single conversion task returned to API consumers.
type FileInfo struct {
	Filename   string `json:"filename"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Pipeline   string `json:"pipeline"`
	ErrorCount int    `json:"error_count,omitempty"`
}

// taskInfo mirrors the overseer v2 TaskInfo for converter tasks.
type taskInfo struct {
	TaskID       string            `json:"task_id"`
	Action       string            `json:"action"`
	Params       map[string]string `json:"params,omitempty"`
	State        string            `json:"state"`
	RestartCount int               `json:"restart_count"`
	ErrorMessage string            `json:"error_message,omitempty"`
}

// Client is a WebSocket client for the sticky-converter service.
type Client struct {
	wsURL string
	idSeq atomic.Int64
}

// NewClient returns a Client targeting the given WebSocket URL (e.g. "ws://converter:8080/ws").
func NewClient(wsURL string) *Client {
	return &Client{wsURL: strings.TrimRight(wsURL, "/")}
}

func (c *Client) nextID() string {
	return fmt.Sprintf("r%d", c.idSeq.Add(1))
}

// GetFiles dials the converter, lists all tasks, and returns those belonging to the given
// driver/username source (matched on params["file"] containing "/{driver}/{username}/").
// Returns an empty list if the converter is unreachable (graceful degradation).
func (c *Client) GetFiles(ctx context.Context, driver, username string) ([]FileInfo, error) {
	subpath := fmt.Sprintf("/%s/%s/", driver, username)

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		// Converter unreachable — degrade gracefully.
		return []FileInfo{}, nil
	}
	defer conn.Close()

	reqID := c.nextID()
	req, _ := json.Marshal(map[string]any{"type": "list", "id": reqID})
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		return []FileInfo{}, nil
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return []FileInfo{}, nil
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
			return filterTasks(msg.Tasks, subpath), nil
		}
	}
}

func filterTasks(tasks []taskInfo, subpath string) []FileInfo {
	var files []FileInfo
	for _, t := range tasks {
		filePath := t.Params["file"]
		if !strings.Contains(strings.ToLower(filePath), strings.ToLower(subpath)) {
			continue
		}
		files = append(files, FileInfo{
			Filename:   filepath.Base(filePath),
			Path:       filePath,
			Status:     t.State,
			Pipeline:   t.Action,
			ErrorCount: t.RestartCount,
		})
	}
	if files == nil {
		files = []FileInfo{}
	}
	return files
}

// QueueFile sends a start request to the converter to queue the given file for conversion.
func (c *Client) QueueFile(ctx context.Context, filePath string) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return fmt.Errorf("connect to converter: %w", err)
	}
	defer conn.Close()

	reqID := c.nextID()
	req, _ := json.Marshal(map[string]any{
		"type":    "start",
		"id":      reqID,
		"action":  "convert",
		"params":  map[string]string{"file": filePath},
	})
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		return fmt.Errorf("send queue request: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			return fmt.Errorf("read response: %w", readErr)
		}
		var resp struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}
		if resp.ID != reqID {
			continue
		}
		if resp.Type == "started" {
			return nil
		}
		if resp.Type == "error" {
			return fmt.Errorf("converter: %s", resp.Message)
		}
	}
}
