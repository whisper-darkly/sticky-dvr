// Package handler implements the "thumbnailer" ActionHandler for sticky-overseer.
// It watches configured glob paths for video files, extracts thumbnails via ffmpeg,
// and propagates them up the directory hierarchy.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	overseer "github.com/whisper-darkly/sticky-overseer/v2"
)

func init() {
	overseer.RegisterFactory(&thumbnailerFactory{})
}

// thumbnailerConfig is the parsed "config" block from the overseer action YAML.
type thumbnailerConfig struct {
	ScanInterval string   `json:"scan_interval"`
	Paths        []string `json:"paths"`
	DBPath       string   `json:"db_path"`
	ThumbLevels  int      `json:"thumb_levels"`
}

type thumbnailerHandler struct {
	actionName string
	cfg        thumbnailerConfig
	store      *Store
}

// ---- Factory ----

type thumbnailerFactory struct{}

func (f *thumbnailerFactory) Type() string { return "thumbnailer" }

func (f *thumbnailerFactory) Create(
	rawCfg map[string]any,
	actionName string,
	_ overseer.RetryPolicy,
	_ overseer.PoolConfig,
	_ []string,
) (overseer.ActionHandler, error) {
	b, err := json.Marshal(rawCfg)
	if err != nil {
		return nil, fmt.Errorf("thumbnailer: marshal config: %w", err)
	}
	var cfg thumbnailerConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("thumbnailer: unmarshal config: %w", err)
	}
	if cfg.ThumbLevels <= 0 {
		cfg.ThumbLevels = 2
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "/data/thumbnailer.db"
	}
	if cfg.ScanInterval == "" {
		cfg.ScanInterval = "5s"
	}

	st, err := OpenStore(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("thumbnailer: open store: %w", err)
	}

	return &thumbnailerHandler{
		actionName: actionName,
		cfg:        cfg,
		store:      st,
	}, nil
}

// ---- ActionHandler ----

func (h *thumbnailerHandler) Describe() overseer.ActionInfo {
	return overseer.ActionInfo{
		Name: h.actionName,
		Type: "thumbnailer",
		Params: map[string]*overseer.ParamSpec{
			"file": nil, // required
		},
	}
}

func (h *thumbnailerHandler) Validate(params map[string]string) error {
	if params["file"] == "" {
		return fmt.Errorf("thumbnailer: required parameter \"file\" is missing")
	}
	return nil
}

// Start launches ffmpeg to extract the last frame as a thumbnail.
// On success it generates the thumbnail hierarchy via the OnExited callback.
func (h *thumbnailerHandler) Start(taskID string, params map[string]string, cb overseer.WorkerCallbacks) (*overseer.Worker, error) {
	inputPath := params["file"]

	// Derive thumbnail output path: /path/to/file.ts → /path/to/file.jpg
	ext := filepath.Ext(inputPath)
	thumbPath := strings.TrimSuffix(inputPath, ext) + ".jpg"

	args := []string{
		"-sseof", "-1",
		"-i", inputPath,
		"-vframes", "1",
		"-q:v", "2",
		"-y", thumbPath,
	}

	h.store.MarkInFlight(inputPath)

	wrappedCB := overseer.WorkerCallbacks{
		OnOutput: cb.OnOutput,
		LogEvent: cb.LogEvent,
		OnExited: func(w *overseer.Worker, exitCode int, intentional bool, t time.Time) {
			if exitCode == 0 {
				// Verify output was written
				fi, err := os.Stat(thumbPath)
				if err != nil || fi.Size() == 0 {
					// Retry once: remove partial output and try again
					os.Remove(thumbPath)
					if retryErr := runFFmpegThumb(inputPath, thumbPath); retryErr != nil {
						log.Printf("thumbnailer: retry failed for %s: %v", inputPath, retryErr)
						h.store.MarkErrored(inputPath)
						cb.OnExited(w, 1, intentional, t)
						return
					}
				}
				h.propagateThumbnail(thumbPath, h.cfg.ThumbLevels)
				h.store.MarkCompleted(inputPath)
			} else {
				h.store.MarkErrored(inputPath)
			}
			cb.OnExited(w, exitCode, intentional, t)
		},
	}

	workerCfg := overseer.WorkerConfig{
		TaskID:        taskID,
		Command:       "ffmpeg",
		Args:          args,
		IncludeStdout: false,
		IncludeStderr: false,
	}
	return overseer.StartWorker(workerCfg, wrappedCB)
}

// runFFmpegThumb runs ffmpeg synchronously to extract the last frame.
func runFFmpegThumb(inputPath, thumbPath string) error {
	cmd := exec.Command("ffmpeg",
		"-sseof", "-1",
		"-i", inputPath,
		"-vframes", "1",
		"-q:v", "2",
		"-y", thumbPath,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// propagateThumbnail copies thumbPath up the directory hierarchy for `levels` levels.
// Level 0: already written (thumbPath itself, e.g. /recordings/cb/alice/2026/seg.jpg)
// Level 1: filepath.Dir(input) + ".jpg" = /recordings/cb/alice/2026.jpg
// Level 2: filepath.Dir(filepath.Dir(input)) + ".jpg" = /recordings/cb/alice.jpg
func (h *thumbnailerHandler) propagateThumbnail(thumbPath string, levels int) {
	// thumbPath is derived from inputPath by stripping ext; we walk up from there.
	dir := filepath.Dir(thumbPath)
	for i := 1; i <= levels; i++ {
		destPath := dir + ".jpg"
		if err := copyFile(thumbPath, destPath); err != nil {
			log.Printf("thumbnailer: propagate level %d → %s: %v", i, destPath, err)
		}
		dir = filepath.Dir(dir)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// ---- ServiceHandler (directory scanner) ----

// RunService implements overseer.ServiceHandler. It scans configured paths
// on startup and periodically, submitting video files as thumbnail tasks.
func (h *thumbnailerHandler) RunService(ctx context.Context, submit overseer.TaskSubmitter) {
	interval := parseDuration(h.cfg.ScanInterval, 5*time.Second)

	// Startup: full scan of all paths (skips only in_flight)
	h.scan(submit, false)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Periodic: recent files only (mtime within last interval)
			h.scanRecent(submit, interval)
		}
	}
}

// scan walks all configured glob paths and submits video files.
// If inFlightOnly is true, only skip in_flight entries (completed/errored are re-submitted).
func (h *thumbnailerHandler) scan(submit overseer.TaskSubmitter, _ bool) {
	for _, pattern := range h.cfg.Paths {
		if err := walkGlob(pattern, func(path string) {
			if h.store.IsInFlight(path) {
				return
			}
			submit.Submit(h.actionName, "", map[string]string{"file": path}) //nolint:errcheck
		}); err != nil {
			log.Printf("thumbnailer: scan glob %s: %v", pattern, err)
		}
	}
}

// scanRecent walks all configured paths and submits files with mtime within the last interval.
func (h *thumbnailerHandler) scanRecent(submit overseer.TaskSubmitter, since time.Duration) {
	cutoff := time.Now().Add(-since)
	for _, pattern := range h.cfg.Paths {
		if err := walkGlob(pattern, func(path string) {
			fi, err := os.Stat(path)
			if err != nil {
				return
			}
			if fi.ModTime().Before(cutoff) {
				return
			}
			if h.store.IsInFlight(path) {
				return
			}
			submit.Submit(h.actionName, "", map[string]string{"file": path}) //nolint:errcheck
		}); err != nil {
			log.Printf("thumbnailer: scan recent glob %s: %v", pattern, err)
		}
	}
}

// walkGlob expands a glob pattern (supports **) and calls fn for each matching file.
// Pattern example: /recordings/**/*.ts
func walkGlob(pattern string, fn func(string)) error {
	// Split on ** to get the base dir and file pattern
	parts := strings.SplitN(pattern, "**", 2)
	baseDir := strings.TrimRight(parts[0], "/")
	if baseDir == "" {
		baseDir = "/"
	}

	var filePattern string
	if len(parts) == 2 {
		filePattern = strings.TrimLeft(parts[1], "/")
	}

	return filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if filePattern != "" {
			matched, _ := filepath.Match(filePattern, filepath.Base(path))
			if !matched {
				return nil
			}
		}
		fn(path)
		return nil
	})
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
