// Package converter provides an HTTP client for the sticky-converter (sticky-refinery) service.
// The converter tracks video file conversion tasks, identified by filesystem path.
// Files belonging to a source are identified by path containing "/{driver}/{username}/".
package converter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// FileInfo describes a single converted (or in-progress) file returned to API consumers.
type FileInfo struct {
	Filename    string     `json:"filename"`
	Path        string     `json:"path"`
	Status      string     `json:"status"`
	Pipeline    string     `json:"pipeline"`
	QueuedAt    *time.Time `json:"queued_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ErrorCount  int        `json:"error_count,omitempty"`
}

// converterTask mirrors the JSON shape returned by GET /tasks on sticky-refinery.
type converterTask struct {
	Path            string     `json:"Path"`
	PipelineName    string     `json:"PipelineName"`
	Status          string     `json:"Status"`
	ErrorCount      int        `json:"ErrorCount"`
	ErrorMessage    string     `json:"ErrorMessage"`
	QueuedAt        *time.Time `json:"QueuedAt"`
	StartedAt       *time.Time `json:"StartedAt"`
	CompletedAt     *time.Time `json:"CompletedAt"`
	LastAttemptedAt *time.Time `json:"LastAttemptedAt"`
}

// Client is an HTTP client for the sticky-refinery converter service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient returns a Client targeting the given base URL (e.g. "http://converter:8080").
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GetFiles fetches tasks from sticky-refinery that belong to the given driver/username
// source. Matching is done by checking whether the task's path contains the
// subpath "/{driver}/{username}/".
// If the converter service is unreachable, an empty list is returned (graceful degradation).
func (c *Client) GetFiles(ctx context.Context, driver, username string) ([]FileInfo, error) {
	// The path segment we expect inside converter task paths.
	// Example: tasks for "chaturbate/alice" will have paths like
	//   /recordings/chaturbate/alice/2024-01-01_stream.mp4
	subpath := fmt.Sprintf("/%s/%s/", driver, username)

	url := fmt.Sprintf("%s/tasks?limit=200", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Converter unreachable — degrade gracefully.
		return []FileInfo{}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Non-200 from converter — return empty list rather than propagating.
		return []FileInfo{}, nil
	}

	var tasks []converterTask
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil, fmt.Errorf("decode converter response: %w", err)
	}

	var files []FileInfo
	for _, t := range tasks {
		// Match by path segment — case-insensitive to handle filesystem variations.
		if !strings.Contains(strings.ToLower(t.Path), strings.ToLower(subpath)) {
			continue
		}
		files = append(files, FileInfo{
			Filename:    filepath.Base(t.Path),
			Path:        t.Path,
			Status:      t.Status,
			Pipeline:    t.PipelineName,
			QueuedAt:    t.QueuedAt,
			StartedAt:   t.StartedAt,
			CompletedAt: t.CompletedAt,
			ErrorCount:  t.ErrorCount,
		})
	}
	if files == nil {
		files = []FileInfo{}
	}
	return files, nil
}
