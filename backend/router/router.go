// Package router registers all HTTP endpoints using vanilla net/http (Go 1.22+ mux).
package router

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/whisper-darkly/sticky-dvr/backend/auth"
	"github.com/whisper-darkly/sticky-dvr/backend/config"
	"github.com/whisper-darkly/sticky-dvr/backend/converter"
	"github.com/whisper-darkly/sticky-dvr/backend/manager"
	"github.com/whisper-darkly/sticky-dvr/backend/middleware"
	"github.com/whisper-darkly/sticky-dvr/backend/store"
	"github.com/whisper-darkly/sticky-dvr/backend/thumbnailer"
)

const refreshCookie = "refresh_token"
const accessCookie  = "access_token"
const sessionTTL    = 24 * time.Hour

// Deps holds all dependencies for the router.
type Deps struct {
	Store             store.Store
	Manager           *manager.Manager
	Config            *config.Global
	JWTSecret         []byte
	ConverterClient   *converter.Client   // nil → files endpoint returns empty list
	ThumbnailerClient *thumbnailer.Client // nil → thumbnailer diagnostics unavailable
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
	mux.Handle("POST /api/me/change-password", requireAuth(http.HandlerFunc(changePassword(d))))

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
	mux.Handle("GET /api/sources/{driver}/{username}/filestat",
		requireAuth(http.HandlerFunc(getSourceFileStat(d))))

	// ---- admin: subscription management (by sub_id) ----
	mux.Handle("POST /api/admin/subscriptions/{sub_id}/pause",
		requireAuth(requireAdmin(http.HandlerFunc(adminPauseSubscription(d)))))
	mux.Handle("POST /api/admin/subscriptions/{sub_id}/resume",
		requireAuth(requireAdmin(http.HandlerFunc(adminResumeSubscription(d)))))
	mux.Handle("POST /api/admin/subscriptions/{sub_id}/archive",
		requireAuth(requireAdmin(http.HandlerFunc(adminArchiveSubscription(d)))))
	mux.Handle("DELETE /api/admin/subscriptions/{sub_id}",
		requireAuth(requireAdmin(http.HandlerFunc(adminDeleteSubscription(d)))))
	mux.Handle("POST /api/admin/subscriptions/{sub_id}/reset-error",
		requireAuth(requireAdmin(http.HandlerFunc(adminResetError(d)))))

	// ---- admin: bulk source operations ----
	mux.Handle("POST /api/admin/sources/restart-all",
		requireAuth(requireAdmin(http.HandlerFunc(adminRestartAllSources(d)))))

	// ---- admin: source subscribers + user subscriptions ----
	mux.Handle("GET /api/admin/sources/{driver}/{username}/subscribers",
		requireAuth(requireAdmin(http.HandlerFunc(adminGetSourceSubscribers(d)))))
	mux.Handle("GET /api/admin/users/{id}/subscriptions",
		requireAuth(requireAdmin(http.HandlerFunc(adminGetUserSubscriptions(d)))))

	// ---- admin: config ----
	mux.Handle("GET /api/config", requireAuth(requireAdmin(http.HandlerFunc(getConfig(d)))))
	mux.Handle("PUT /api/config", requireAuth(requireAdmin(http.HandlerFunc(putConfig(d)))))

	// ---- admin: users ----
	mux.Handle("GET /api/users", requireAuth(requireAdmin(http.HandlerFunc(listUsers(d)))))
	mux.Handle("POST /api/users", requireAuth(requireAdmin(http.HandlerFunc(createUser(d)))))
	mux.Handle("GET /api/users/{id}", requireAuth(requireAdmin(http.HandlerFunc(getUser(d)))))
	mux.Handle("PUT /api/users/{id}", requireAuth(requireAdmin(http.HandlerFunc(updateUser(d)))))
	mux.Handle("DELETE /api/users/{id}", requireAuth(requireAdmin(http.HandlerFunc(deleteUser(d)))))

	// ---- admin: diagnostics ----
	mux.Handle("GET /api/admin/diagnostics",
		requireAuth(requireAdmin(http.HandlerFunc(getDiagnostics(d)))))

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
		setAccessCookie(w, token)
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": token,
			"user":         u,
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
		setAccessCookie(w, token)
		writeJSON(w, http.StatusOK, map[string]any{"access_token": token})
	}
}

func logout(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessID := middleware.ContextSessionID(r)
		if sessID != (uuid.UUID{}) {
			_ = d.Store.DeleteSession(r.Context(), sessID)
		}
		clearRefreshCookie(w)
		clearAccessCookie(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

// cookieSecure controls whether cookies are sent with Secure=true.
// Set COOKIE_SECURE=false in dev/HTTP environments to allow cookies over HTTP.
var cookieSecure = os.Getenv("COOKIE_SECURE") != "false"

func setAccessCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     accessCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   cookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func clearAccessCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     accessCookie,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
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

func changePassword(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.CurrentPassword == "" || body.NewPassword == "" {
			writeError(w, http.StatusBadRequest, "current_password and new_password are required")
			return
		}

		userID := middleware.ContextUserID(r)
		u, err := d.Store.GetUser(r.Context(), userID)
		if err != nil || u == nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !auth.CheckPassword(u.PasswordHash, body.CurrentPassword) {
			writeError(w, http.StatusUnauthorized, "current password is incorrect")
			return
		}
		hash, err := auth.HashPassword(body.NewPassword)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if _, err := d.Store.UpdateUser(r.Context(), userID, store.UserUpdate{PasswordHash: &hash}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
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
		events, err := d.Manager.GetWorkerEvents(r.Context(), userID, isAdmin, driver, username, 25)
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
		driver, username := r.PathValue("driver"), r.PathValue("username")
		if d.ConverterClient == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"driver": driver, "username": username, "files": []any{},
			})
			return
		}
		files, err := d.ConverterClient.GetFiles(r.Context(), driver, username)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "converter error: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"driver": driver, "username": username, "files": files,
		})
	}
}

// mediaRoot returns the MEDIA_ROOT env var value (or empty string if not set).
var mediaRoot = os.Getenv("MEDIA_ROOT")

type fileStatChild struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	TotalBytes       int64  `json:"total_bytes"`
	FileCount        int    `json:"file_count"`
	TsCount          int    `json:"ts_count"`
	EstimatedMinutes int    `json:"estimated_minutes"`
}

type fileStatResponse struct {
	Path       string          `json:"path"`
	Children   []fileStatChild `json:"children"`
	TotalBytes int64           `json:"total_bytes"`
	FileCount  int             `json:"file_count"`
}

func getSourceFileStat(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mediaRoot == "" {
			writeError(w, http.StatusServiceUnavailable, "MEDIA_ROOT not configured")
			return
		}
		driver, username := r.PathValue("driver"), r.PathValue("username")
		userID := middleware.ContextUserID(r)
		isAdmin := middleware.ContextUserRole(r) == "admin"

		// Ownership check.
		src, err := d.Store.GetSourceByKey(r.Context(), driver, username)
		if err != nil || src == nil {
			writeError(w, http.StatusNotFound, "source not found")
			return
		}
		if !isAdmin {
			sub, err := d.Store.GetSubscription(r.Context(), userID, src.ID)
			if err != nil || sub == nil {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
		}

		reqPath := r.URL.Query().Get("path")
		// Sanitize: reject paths with ".." components.
		if strings.Contains(reqPath, "..") {
			writeError(w, http.StatusBadRequest, "invalid path")
			return
		}

		basePath := filepath.Join(mediaRoot, driver, username, filepath.FromSlash(reqPath))
		// Clean and ensure it's within mediaRoot.
		basePath = filepath.Clean(basePath)
		if !strings.HasPrefix(basePath, filepath.Clean(mediaRoot)) {
			writeError(w, http.StatusBadRequest, "invalid path")
			return
		}

		// Get segment length from config for duration estimation.
		segLen := parseDurationSeconds(d.Manager.GetConfig().SegmentLength, 300) // default 5m

		entries, err := os.ReadDir(basePath)
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "path not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		var children []fileStatChild
		var totalBytes int64
		var totalFiles int

		for _, entry := range entries {
			child := fileStatChild{
				Name: entry.Name(),
				Type: "file",
			}
			if entry.IsDir() {
				child.Type = "directory"
				// Walk directory recursively to sum sizes and count .ts files.
				childPath := filepath.Join(basePath, entry.Name())
				filepath.WalkDir(childPath, func(p string, d fs.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return nil
					}
					info, err := d.Info()
					if err != nil {
						return nil
					}
					child.TotalBytes += info.Size()
					child.FileCount++
					if strings.HasSuffix(p, ".ts") {
						child.TsCount++
					}
					return nil
				})
				if segLen > 0 {
					child.EstimatedMinutes = child.TsCount * segLen / 60
				}
				totalBytes += child.TotalBytes
				totalFiles += child.FileCount
			} else {
				info, err := entry.Info()
				if err == nil {
					child.TotalBytes = info.Size()
					child.FileCount = 1
					totalBytes += child.TotalBytes
					totalFiles++
				}
			}
			children = append(children, child)
		}
		if children == nil {
			children = []fileStatChild{}
		}

		writeJSON(w, http.StatusOK, fileStatResponse{
			Path:       reqPath,
			Children:   children,
			TotalBytes: totalBytes,
			FileCount:  totalFiles,
		})
	}
}

// parseDurationSeconds parses a duration string and returns it in whole seconds.
func parseDurationSeconds(s string, def int) int {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return int(d.Seconds())
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

// ---- admin: subscription management handlers ----

func parseSubID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("sub_id"), 10, 64)
}

func adminPauseSubscription(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subID, err := parseSubID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid sub_id")
			return
		}
		status, err := d.Manager.AdminPause(r.Context(), subID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func adminResumeSubscription(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subID, err := parseSubID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid sub_id")
			return
		}
		status, err := d.Manager.AdminResume(r.Context(), subID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func adminArchiveSubscription(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subID, err := parseSubID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid sub_id")
			return
		}
		status, err := d.Manager.AdminArchive(r.Context(), subID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func adminDeleteSubscription(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subID, err := parseSubID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid sub_id")
			return
		}
		if err := d.Manager.AdminUnsubscribe(r.Context(), subID); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func adminResetError(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subID, err := parseSubID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid sub_id")
			return
		}
		status, err := d.Manager.AdminResetError(r.Context(), subID)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func adminRestartAllSources(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IncludeErrored bool `json:"include_errored"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && r.ContentLength > 0 {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		restarted, skipped := d.Manager.RestartAll(r.Context(), body.IncludeErrored)
		writeJSON(w, http.StatusOK, map[string]int{
			"restarted": restarted,
			"skipped":   skipped,
		})
	}
}

func adminGetSourceSubscribers(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, username := r.PathValue("driver"), r.PathValue("username")
		subscribers, err := d.Manager.GetSourceSubscribers(r.Context(), driver, username)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if subscribers == nil {
			subscribers = []*store.SubscriberInfo{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"driver":      driver,
			"username":    username,
			"subscribers": subscribers,
		})
	}
}

func adminGetUserSubscriptions(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid user id")
			return
		}
		subs, err := d.Manager.ListSubscriptions(r.Context(), id, false)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, subs)
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

// svcInfo is the per-service diagnostics payload.
type svcInfo struct {
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
	Pool      any    `json:"pool,omitempty"`
	Metrics   any    `json:"metrics,omitempty"`
}

func getDiagnostics(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var (
			recorderInfo   svcInfo
			converterInfo  svcInfo
			thumbnailerInfo svcInfo
			wg             sync.WaitGroup
		)

		// ---- Recorder (persistent client) ----
		wg.Add(1)
		go func() {
			defer wg.Done()
			oc := d.Manager.GetOverseerClient()
			if oc == nil {
				recorderInfo = svcInfo{Error: "overseer client not initialised"}
				return
			}
			if !oc.IsConnected() {
				recorderInfo = svcInfo{Error: "overseer disconnected"}
				return
			}
			recorderInfo.Connected = true
			var innerWg sync.WaitGroup
			innerWg.Add(2)
			go func() {
				defer innerWg.Done()
				if pi, err := oc.PoolInfo(ctx); err == nil {
					recorderInfo.Pool = pi
				}
			}()
			go func() {
				defer innerWg.Done()
				if gm, err := oc.Metrics(ctx); err == nil {
					recorderInfo.Metrics = gm
				}
			}()
			innerWg.Wait()
		}()

		// ---- Converter (per-request dial) ----
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d.ConverterClient == nil {
				converterInfo = svcInfo{Error: "CONVERTER_URL not configured"}
				return
			}
			// Use pool info reachability as the connected signal (metrics may be zero).
			pi, _ := d.ConverterClient.GetPoolInfo(ctx)
			if pi == nil {
				converterInfo = svcInfo{Error: "converter unreachable"}
				return
			}
			converterInfo.Connected = true
			converterInfo.Pool = pi
			if gm, _ := d.ConverterClient.GetMetrics(ctx); gm != nil {
				converterInfo.Metrics = gm
			}
		}()

		// ---- Thumbnailer (per-request dial) ----
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d.ThumbnailerClient == nil {
				thumbnailerInfo = svcInfo{Error: "THUMBNAILER_URL not configured"}
				return
			}
			pi, _ := d.ThumbnailerClient.GetPoolInfo(ctx)
			if pi == nil {
				thumbnailerInfo = svcInfo{Error: "thumbnailer unreachable"}
				return
			}
			thumbnailerInfo.Connected = true
			thumbnailerInfo.Pool = pi
			if gm, _ := d.ThumbnailerClient.GetMetrics(ctx); gm != nil {
				thumbnailerInfo.Metrics = gm
			}
		}()

		wg.Wait()

		writeJSON(w, http.StatusOK, map[string]any{
			"recorder":   recorderInfo,
			"converter":  converterInfo,
			"thumbnailer": thumbnailerInfo,
		})
	}
}
