// Package router registers all HTTP endpoints using vanilla net/http (Go 1.22+ mux).
package router

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/whisper-darkly/sticky-dvr/backend/auth"
	"github.com/whisper-darkly/sticky-dvr/backend/config"
	"github.com/whisper-darkly/sticky-dvr/backend/manager"
	"github.com/whisper-darkly/sticky-dvr/backend/middleware"
	"github.com/whisper-darkly/sticky-dvr/backend/store"
)

const refreshCookie = "refresh_token"
const sessionTTL = 24 * time.Hour

// Deps holds all dependencies for the router.
type Deps struct {
	Store     store.Store
	Manager   *manager.Manager
	Config    *config.Global
	JWTSecret []byte
}

// New builds and returns the application HTTP handler.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()

	requireAuth := middleware.RequireAuth(d.JWTSecret)
	requireAdmin := middleware.RequireAdmin()

	// ---- auth (no auth required) ----
	mux.HandleFunc("POST /api/auth/login", login(d))
	mux.HandleFunc("POST /api/auth/refresh", refreshToken(d))

	// ---- auth (requires valid JWT) ----
	mux.Handle("POST /api/auth/logout", requireAuth(http.HandlerFunc(logout(d))))

	// ---- current user ----
	mux.Handle("GET /api/me", requireAuth(http.HandlerFunc(getMe(d))))

	// ---- subscriptions (user-scoped) ----
	mux.Handle("GET /api/subscriptions",
		requireAuth(http.HandlerFunc(listSubscriptions(d))))
	mux.Handle("POST /api/subscriptions",
		requireAuth(http.HandlerFunc(createSubscription(d))))
	mux.Handle("GET /api/subscriptions/{driver}/{username}",
		requireAuth(http.HandlerFunc(getSubscription(d))))
	mux.Handle("DELETE /api/subscriptions/{driver}/{username}",
		requireAuth(http.HandlerFunc(deleteSubscription(d))))
	mux.Handle("POST /api/subscriptions/{driver}/{username}/pause",
		requireAuth(http.HandlerFunc(pauseSubscription(d))))
	mux.Handle("POST /api/subscriptions/{driver}/{username}/resume",
		requireAuth(http.HandlerFunc(resumeSubscription(d))))
	mux.Handle("POST /api/subscriptions/{driver}/{username}/archive",
		requireAuth(http.HandlerFunc(archiveSubscription(d))))
	mux.Handle("POST /api/subscriptions/{driver}/{username}/reset-error",
		requireAuth(http.HandlerFunc(resetError(d))))

	// ---- per-source data (auth + ownership) ----
	mux.Handle("GET /api/sources/{driver}/{username}/events",
		requireAuth(http.HandlerFunc(getSourceEvents(d))))
	mux.Handle("GET /api/sources/{driver}/{username}/logs",
		requireAuth(http.HandlerFunc(getSourceLogs(d))))
	mux.Handle("GET /api/sources/{driver}/{username}/files",
		requireAuth(http.HandlerFunc(getSourceFiles(d))))

	// ---- admin: config ----
	mux.Handle("GET /api/config", requireAuth(requireAdmin(http.HandlerFunc(getConfig(d)))))
	mux.Handle("PUT /api/config", requireAuth(requireAdmin(http.HandlerFunc(putConfig(d)))))

	// ---- admin: users ----
	mux.Handle("GET /api/users", requireAuth(requireAdmin(http.HandlerFunc(listUsers(d)))))
	mux.Handle("POST /api/users", requireAuth(requireAdmin(http.HandlerFunc(createUser(d)))))
	mux.Handle("GET /api/users/{id}", requireAuth(requireAdmin(http.HandlerFunc(getUser(d)))))
	mux.Handle("PUT /api/users/{id}", requireAuth(requireAdmin(http.HandlerFunc(updateUser(d)))))
	mux.Handle("DELETE /api/users/{id}", requireAuth(requireAdmin(http.HandlerFunc(deleteUser(d)))))

	// ---- system ----
	mux.HandleFunc("GET /api/health", health(d))
	mux.Handle("GET /api/workers", requireAuth(requireAdmin(http.HandlerFunc(listWorkers(d)))))

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

// ---- auth handlers ----

func login(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Username == "" || body.Password == "" {
			writeError(w, http.StatusBadRequest, "username and password are required")
			return
		}

		u, err := d.Store.GetUserByUsername(r.Context(), body.Username)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if u == nil || !auth.CheckPassword(u.PasswordHash, body.Password) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		refreshTok, err := auth.GenerateRefreshToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		sess, err := d.Store.CreateSession(r.Context(), u.ID, refreshTok, time.Now().Add(sessionTTL))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		token, err := auth.IssueAccessToken(d.JWTSecret, u.ID, sess.ID, u.Role)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		setRefreshCookie(w, refreshTok)
		writeJSON(w, http.StatusOK, map[string]any{
			"token": token,
			"user":  u,
		})
	}
}

func refreshToken(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(refreshCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "missing refresh token")
			return
		}

		sess, err := d.Store.GetSessionByRefreshToken(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if sess == nil || sess.ExpiresAt.Before(time.Now()) {
			writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
			return
		}

		u, err := d.Store.GetUser(r.Context(), sess.UserID)
		if err != nil || u == nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Rotate: delete old session, create new one.
		_ = d.Store.DeleteSession(r.Context(), sess.ID)

		newRefreshTok, err := auth.GenerateRefreshToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		newSess, err := d.Store.CreateSession(r.Context(), u.ID, newRefreshTok, time.Now().Add(sessionTTL))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		token, err := auth.IssueAccessToken(d.JWTSecret, u.ID, newSess.ID, u.Role)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		setRefreshCookie(w, newRefreshTok)
		writeJSON(w, http.StatusOK, map[string]any{"token": token})
	}
}

func logout(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessID := middleware.ContextSessionID(r)
		if sessID != (uuid.UUID{}) {
			_ = d.Store.DeleteSession(r.Context(), sessID)
		}
		clearRefreshCookie(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

func setRefreshCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookie,
		Value:    value,
		Path:     "/api/auth/refresh",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookie,
		Path:     "/api/auth/refresh",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// ---- user handlers ----

func getMe(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := middleware.ContextUserID(r)
		u, err := d.Store.GetUser(r.Context(), userID)
		if err != nil || u == nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, u)
	}
}

// ---- subscription handlers ----

func listSubscriptions(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := middleware.ContextUserID(r)
		isAdmin := middleware.ContextUserRole(r) == "admin"
		subs, err := d.Manager.ListSubscriptions(r.Context(), userID, isAdmin)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, subs)
	}
}

func createSubscription(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Driver   string `json:"driver"`
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Driver == "" {
			writeError(w, http.StatusBadRequest, "driver is required")
			return
		}
		if body.Username == "" {
			writeError(w, http.StatusBadRequest, "username is required")
			return
		}
		userID := middleware.ContextUserID(r)
		status, err := d.Manager.Subscribe(r.Context(), userID, body.Driver, body.Username)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, status)
	}
}

func getSubscription(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, username := r.PathValue("driver"), r.PathValue("username")
		userID := middleware.ContextUserID(r)
		isAdmin := middleware.ContextUserRole(r) == "admin"
		// Admins can view any subscription; users only their own.
		var lookupID int64
		if isAdmin {
			lookupID = 0 // not used
		} else {
			lookupID = userID
		}
		_ = lookupID
		status, err := d.Manager.GetStatus(r.Context(), userID, driver, username)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func deleteSubscription(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, username := r.PathValue("driver"), r.PathValue("username")
		userID := middleware.ContextUserID(r)
		if err := d.Manager.Unsubscribe(r.Context(), userID, driver, username); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func pauseSubscription(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, username := r.PathValue("driver"), r.PathValue("username")
		userID := middleware.ContextUserID(r)
		status, err := d.Manager.Pause(r.Context(), userID, driver, username)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func resumeSubscription(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, username := r.PathValue("driver"), r.PathValue("username")
		userID := middleware.ContextUserID(r)
		status, err := d.Manager.Resume(r.Context(), userID, driver, username)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func archiveSubscription(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, username := r.PathValue("driver"), r.PathValue("username")
		userID := middleware.ContextUserID(r)
		status, err := d.Manager.Archive(r.Context(), userID, driver, username)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func resetError(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, username := r.PathValue("driver"), r.PathValue("username")
		userID := middleware.ContextUserID(r)
		status, err := d.Manager.ResetError(r.Context(), userID, driver, username)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

// ---- source data handlers ----

func getSourceEvents(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, username := r.PathValue("driver"), r.PathValue("username")
		userID := middleware.ContextUserID(r)
		isAdmin := middleware.ContextUserRole(r) == "admin"
		events, err := d.Manager.GetWorkerEvents(r.Context(), userID, isAdmin, driver, username, 50)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"driver":   driver,
			"username": username,
			"events":   events,
		})
	}
}

func getSourceLogs(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, username := r.PathValue("driver"), r.PathValue("username")
		userID := middleware.ContextUserID(r)
		isAdmin := middleware.ContextUserRole(r) == "admin"
		logs, err := d.Manager.GetLogs(r.Context(), userID, isAdmin, driver, username)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"driver":   driver,
			"username": username,
			"logs":     logs,
		})
	}
}

func getSourceFiles(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Stub: converter integration is Phase 2.
		driver, username := r.PathValue("driver"), r.PathValue("username")
		writeJSON(w, http.StatusOK, map[string]any{
			"driver":   driver,
			"username": username,
			"files":    []any{},
		})
	}
}

// ---- admin: config ----

func getConfig(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, d.Manager.GetConfig())
	}
}

func putConfig(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var cfg config.Data
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := d.Manager.SetConfig(r.Context(), cfg); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, d.Manager.GetConfig())
	}
}

// ---- admin: users ----

func listUsers(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := d.Store.ListUsers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, users)
	}
}

func createUser(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Username == "" || body.Password == "" {
			writeError(w, http.StatusBadRequest, "username and password are required")
			return
		}
		if body.Role == "" {
			body.Role = "user"
		}
		if body.Role != "admin" && body.Role != "user" {
			writeError(w, http.StatusBadRequest, "role must be 'admin' or 'user'")
			return
		}
		hash, err := auth.HashPassword(body.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		u, err := d.Store.CreateUser(r.Context(), body.Username, hash, body.Role)
		if err != nil {
			writeError(w, http.StatusConflict, "username already exists")
			return
		}
		writeJSON(w, http.StatusCreated, u)
	}
}

func getUser(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid user id")
			return
		}
		u, err := d.Store.GetUser(r.Context(), id)
		if err != nil || u == nil {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeJSON(w, http.StatusOK, u)
	}
}

func updateUser(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid user id")
			return
		}
		var body struct {
			Username *string `json:"username"`
			Password *string `json:"password"`
			Role     *string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}

		fields := store.UserUpdate{
			Username: body.Username,
			Role:     body.Role,
		}
		if body.Password != nil {
			hash, err := auth.HashPassword(*body.Password)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			fields.PasswordHash = &hash
		}

		u, err := d.Store.UpdateUser(r.Context(), id, fields)
		if err != nil || u == nil {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeJSON(w, http.StatusOK, u)
	}
}

func deleteUser(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid user id")
			return
		}
		if err := d.Store.DeleteUser(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- system ----

func health(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		oc := d.Manager.GetOverseerClient()
		connected := oc != nil && oc.IsConnected()

		code := http.StatusOK
		status := "ok"
		if !connected {
			code = http.StatusServiceUnavailable
			status = "overseer_disconnected"
		}
		writeJSON(w, code, map[string]any{
			"status":             status,
			"overseer_connected": connected,
		})
	}
}

func listWorkers(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		oc := d.Manager.GetOverseerClient()
		if oc == nil || !oc.IsConnected() {
			writeError(w, http.StatusServiceUnavailable, "not connected to overseer")
			return
		}
		tasks, err := oc.List(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "overseer error: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, tasks)
	}
}
