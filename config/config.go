// Package config manages the global, persisted backend configuration.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const DefaultOutPattern = "/data/recordings/{{.Driver}}/{{.Source}}/{{.Session.Year}}-{{.Session.Month}}-{{.Session.Day}}_{{.Session.Hour}}-{{.Session.Minute}}-{{.Session.Second}}/{{.Recording.Year}}-{{.Recording.Month}}-{{.Recording.Day}}_{{.Recording.Hour}}-{{.Recording.Minute}}-{{.Recording.Second}}"

// Data holds the serialisable global configuration.
// Driver is NOT included here — it is a per-subscription field.
type Data struct {
	// sticky-recorder flags applied to every spawned process
	Resolution       int    `json:"resolution"`
	Framerate        int    `json:"framerate"`
	OutPattern       string `json:"out_pattern"`
	LogPattern       string `json:"log_pattern"`
	SegmentLength    string `json:"segment_length"`
	CheckInterval    string `json:"check_interval"`
	RetryDelay       string `json:"retry_delay"`
	SegmentTimeout   string `json:"segment_timeout"`
	RecordingTimeout string `json:"recording_timeout"`
	LogLevel         string `json:"log_level"`
	OutputFormat     string `json:"output_format"`
	UserAgent        string `json:"user_agent"`
	Cookies          string `json:"cookies"`

	// Backend behaviour
	RestartDelay      string `json:"restart_delay"`      // wait before auto-restart after a non-explicit exit
	ReconcileInterval string `json:"reconcile_interval"` // how often the manager checks workers against overseer
	ErrorThreshold    int    `json:"error_threshold"`    // error exits within ErrorWindow before entering error state
	ErrorWindow       string `json:"error_window"`       // rolling window for error counting (e.g. "5m")
}

// Global is a thread-safe, disk-backed wrapper around Data.
type Global struct {
	mu      sync.RWMutex
	data    Data
	confDir string
}

// Load reads the config from confDir/config.json, filling in defaults for any
// missing fields.  Creates the directory if it does not exist.
func Load(confDir string) (*Global, error) {
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return nil, err
	}

	g := &Global{confDir: confDir, data: defaults()}

	raw, err := os.ReadFile(filepath.Join(confDir, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return g, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(raw, &g.data); err != nil {
		return nil, err
	}
	return g, nil
}

func defaults() Data {
	return Data{
		Resolution:        720,
		Framerate:         30,
		OutPattern:        DefaultOutPattern,
		CheckInterval:     "1m",
		RetryDelay:        "5s",
		SegmentTimeout:    "5m",
		RecordingTimeout:  "30m",
		LogLevel:          "error",
		OutputFormat:      "json",
		RestartDelay:      "30s",
		ReconcileInterval: "60s",
		ErrorThreshold:    5,
		ErrorWindow:       "5m",
	}
}

// Get returns a thread-safe copy of the current configuration.
func (g *Global) Get() Data {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.data
}

// Set replaces the current configuration and persists it to disk.
func (g *Global) Set(d Data) error {
	g.mu.Lock()
	g.data = d
	g.mu.Unlock()
	return g.save()
}

func (g *Global) save() error {
	g.mu.RLock()
	raw, err := json.MarshalIndent(g.data, "", "  ")
	g.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(g.confDir, "config.json"), raw, 0o644)
}
