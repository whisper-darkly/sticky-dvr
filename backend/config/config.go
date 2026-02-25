// Package config manages the global backend configuration.
// Defaults are loaded from an embedded YAML file; the live config is stored
// in a single DB row and read/written via the ConfigStore interface.
package config

import (
	"context"
	_ "embed"
	"encoding/json"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed config.default.yaml
var defaultYAML []byte

const DefaultOutPattern = "/data/recordings/{{.Driver}}/{{.Source}}/{{.Session.Year}}-{{.Session.Month}}-{{.Session.Day}}_{{.Session.Hour}}-{{.Session.Minute}}-{{.Session.Second}}/{{.Recording.Year}}-{{.Recording.Month}}-{{.Recording.Day}}_{{.Recording.Hour}}-{{.Recording.Minute}}-{{.Recording.Second}}"

// Data holds the serialisable global configuration.
type Data struct {
	Resolution       int    `json:"resolution"        yaml:"resolution"`
	Framerate        int    `json:"framerate"         yaml:"framerate"`
	OutPattern       string `json:"out_pattern"       yaml:"out_pattern"`
	LogPattern       string `json:"log_pattern"       yaml:"log_pattern"`
	SegmentLength    string `json:"segment_length"    yaml:"segment_length"`
	CheckInterval    string `json:"check_interval"    yaml:"check_interval"`
	RetryDelay       string `json:"retry_delay"       yaml:"retry_delay"`
	SegmentTimeout   string `json:"segment_timeout"   yaml:"segment_timeout"`
	RecordingTimeout string `json:"recording_timeout" yaml:"recording_timeout"`
	LogLevel         string `json:"log_level"         yaml:"log_level"`
	OutputFormat     string `json:"output_format"     yaml:"output_format"`
	UserAgent        string `json:"user_agent"        yaml:"user_agent"`
	Cookies          string `json:"cookies"           yaml:"cookies"`

	RestartDelay      string `json:"restart_delay"      yaml:"restart_delay"`
	ReconcileInterval string `json:"reconcile_interval" yaml:"reconcile_interval"`
	StartConcurrency  int    `json:"start_concurrency"  yaml:"start_concurrency"`
	ErrorThreshold    int    `json:"error_threshold"    yaml:"error_threshold"`
	ErrorWindow       string `json:"error_window"       yaml:"error_window"`

	// DriverURLs maps driver names to URL templates.
	// Use {{.Username}} as the performer name placeholder.
	DriverURLs map[string]string `json:"driver_urls" yaml:"driver_urls"`
}

// ConfigStore is the persistence interface for the live config row.
// Implemented by store/postgres.DB; defined here to avoid circular imports.
type ConfigStore interface {
	GetConfig(ctx context.Context) (map[string]any, error)
	SetConfig(ctx context.Context, data map[string]any) error
}

// Global is a thread-safe, DB-backed wrapper around Data.
type Global struct {
	mu   sync.RWMutex
	data Data
	st   ConfigStore
}

// Load initialises Global from the DB.
// If the DB row is empty/missing, the embedded default YAML is seeded.
func Load(ctx context.Context, st ConfigStore) (*Global, error) {
	g := &Global{st: st, data: defaults()}

	raw, err := st.GetConfig(ctx)
	if err != nil {
		return nil, err
	}

	if len(raw) == 0 {
		// Seed defaults into the DB.
		if err := g.persistDefaults(ctx); err != nil {
			return nil, err
		}
		return g, nil
	}

	// Re-serialise the map → JSON → Data so we benefit from json tags.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &g.data); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *Global) persistDefaults(ctx context.Context) error {
	b, err := json.Marshal(g.data)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	return g.st.SetConfig(ctx, m)
}

// defaults returns the built-in configuration by parsing the embedded YAML.
func defaults() Data {
	var d Data
	_ = yaml.Unmarshal(defaultYAML, &d)
	return d
}

// Get returns a thread-safe copy of the current configuration.
func (g *Global) Get() Data {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.data
}

// Set replaces the configuration and persists it to the DB.
func (g *Global) Set(ctx context.Context, d Data) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if err := g.st.SetConfig(ctx, m); err != nil {
		return err
	}
	g.mu.Lock()
	g.data = d
	g.mu.Unlock()
	return nil
}
