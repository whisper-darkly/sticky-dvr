// Package router registers all HTTP endpoints using vanilla net/http (Go 1.22+ mux).
package router

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/whisper-darkly/sticky-backend/config"
	"github.com/whisper-darkly/sticky-backend/manager"
	"github.com/whisper-darkly/sticky-backend/store"
)

// New builds and returns the application HTTP handler.
//
// Subscription endpoints are keyed by {driver}/{source} — e.g.
//
//	POST /api/subscriptions          {"driver":"chaturbate","source":"alice"}
//	GET  /api/subscriptions/chaturbate/alice
//	DELETE /api/subscriptions/chaturbate/alice
func New(mgr *manager.Manager, _ *config.Global) http.Handler {
	mux := http.NewServeMux()

	// Collection
	mux.HandleFunc("GET /api/subscriptions", listSubscriptions(mgr))
	mux.HandleFunc("POST /api/subscriptions", createSubscription(mgr))

	// Single subscription — {driver}/{source}
	mux.HandleFunc("GET /api/subscriptions/{driver}/{source}", getSubscription(mgr))
	mux.HandleFunc("DELETE /api/subscriptions/{driver}/{source}", deleteSubscription(mgr))

	// Actions
	mux.HandleFunc("POST /api/subscriptions/{driver}/{source}/pause", pauseSubscription(mgr))
	mux.HandleFunc("POST /api/subscriptions/{driver}/{source}/resume", resumeSubscription(mgr))
	mux.HandleFunc("POST /api/subscriptions/{driver}/{source}/restart", restartSubscription(mgr))
	mux.HandleFunc("POST /api/subscriptions/{driver}/{source}/reset-error", resetError(mgr))

	// Logs convenience endpoint (also present in the full GET response)
	mux.HandleFunc("GET /api/subscriptions/{driver}/{source}/logs", getSubscriptionLogs(mgr))

	// Worker lifecycle events
	mux.HandleFunc("GET /api/subscriptions/{driver}/{source}/events", getSubscriptionEvents(mgr))

	// Global config
	mux.HandleFunc("GET /api/config", getConfig(mgr))
	mux.HandleFunc("PUT /api/config", putConfig(mgr))

	// System / diagnostics
	mux.HandleFunc("GET /api/health", health(mgr))
	mux.HandleFunc("GET /api/workers", listWorkers(mgr))

	return mux
}

// ---- response helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ---- handlers ----

func listSubscriptions(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subs, err := mgr.ListVisible(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, subs)
	}
}

func createSubscription(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Driver string `json:"driver"`
			Source string `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if body.Driver == "" {
			writeError(w, http.StatusBadRequest, "driver is required (chaturbate, stripchat, …)")
			return
		}
		if body.Source == "" {
			writeError(w, http.StatusBadRequest, "source is required")
			return
		}
		status, err := mgr.Subscribe(r.Context(), body.Driver, body.Source)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, status)
	}
}

func getSubscription(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, source := r.PathValue("driver"), r.PathValue("source")
		status, err := mgr.GetStatus(r.Context(), driver, source)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func deleteSubscription(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, source := r.PathValue("driver"), r.PathValue("source")
		if err := mgr.Unsubscribe(r.Context(), driver, source); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func pauseSubscription(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, source := r.PathValue("driver"), r.PathValue("source")
		status, err := mgr.Pause(r.Context(), driver, source)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func resumeSubscription(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, source := r.PathValue("driver"), r.PathValue("source")
		status, err := mgr.Resume(r.Context(), driver, source)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func restartSubscription(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, source := r.PathValue("driver"), r.PathValue("source")
		status, err := mgr.Restart(r.Context(), driver, source)
		if err != nil {
			code := http.StatusNotFound
			if err.Error() == fmt.Sprintf("subscription %s/%s has no running worker", driver, source) {
				code = http.StatusConflict
			}
			writeError(w, code, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func resetError(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, source := r.PathValue("driver"), r.PathValue("source")
		current, err := mgr.GetStatus(r.Context(), driver, source)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if current.State != store.StateError {
			writeError(w, http.StatusConflict, "subscription is not in error state")
			return
		}
		status, err := mgr.ResetError(r.Context(), driver, source)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func getSubscriptionLogs(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, source := r.PathValue("driver"), r.PathValue("source")
		logs, err := mgr.GetLogs(r.Context(), driver, source)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"driver": driver,
			"source": source,
			"logs":   logs,
		})
	}
}

func getSubscriptionEvents(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, source := r.PathValue("driver"), r.PathValue("source")
		limit := 50
		events, err := mgr.GetWorkerEvents(r.Context(), driver, source, limit)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"driver": driver,
			"source": source,
			"events": events,
		})
	}
}

func getConfig(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, mgr.GetConfig())
	}
}

func putConfig(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var d config.Data
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := mgr.SetConfig(d); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, mgr.GetConfig())
	}
}

func health(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		oc := mgr.GetOverseerClient()
		connected := oc != nil && oc.IsConnected()

		subs, _ := mgr.ListVisible(r.Context())
		recording, errored, paused := 0, 0, 0
		for _, s := range subs {
			switch s.State {
			case store.StateError:
				errored++
			case store.StatePaused:
				paused++
			}
			if s.WorkerState == "recording" {
				recording++
			}
		}

		code := http.StatusOK
		if !connected {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{
			"status":             statusStr(connected),
			"overseer_connected": connected,
			"subscriptions":      len(subs),
			"recording":          recording,
			"paused":             paused,
			"errored":            errored,
		})
	}
}

func listWorkers(mgr *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		oc := mgr.GetOverseerClient()
		if oc == nil || !oc.IsConnected() {
			writeError(w, http.StatusServiceUnavailable, "not connected to overseer")
			return
		}
		workers, err := oc.List(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "overseer error: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, workers)
	}
}

func statusStr(connected bool) string {
	if connected {
		return "ok"
	}
	return "overseer_disconnected"
}
