// Package middleware provides HTTP middleware for JWT auth and role enforcement.
package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/whisper-darkly/sticky-dvr/backend/auth"
)

type contextKey int

const (
	ctxUserID    contextKey = iota
	ctxUserRole  contextKey = iota
	ctxSessionID contextKey = iota
)

// RequireAuth validates the Bearer JWT and injects userID + role into context.
// Returns 401 on missing/invalid token, 403 on expired.
func RequireAuth(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if raw == "" {
				writeError(w, http.StatusUnauthorized, "missing authorization header")
				return
			}
			claims, err := auth.ParseAccessToken(secret, raw)
			if err != nil {
				writeError(w, http.StatusUnauthorized, err.Error())
				return
			}
			userID, err := strconv.ParseInt(claims.Subject, 10, 64)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid token subject")
				return
			}
			ctx := context.WithValue(r.Context(), ctxUserID, userID)
			ctx = context.WithValue(ctx, ctxUserRole, claims.Role)
			ctx = context.WithValue(ctx, ctxSessionID, claims.SessionID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin returns 403 if the request context role is not "admin".
func RequireAdmin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ContextUserRole(r) != "admin" {
				writeError(w, http.StatusForbidden, "admin role required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ContextUserID extracts the userID injected by RequireAuth.
func ContextUserID(r *http.Request) int64 {
	v, _ := r.Context().Value(ctxUserID).(int64)
	return v
}

// ContextUserRole extracts the role injected by RequireAuth.
func ContextUserRole(r *http.Request) string {
	v, _ := r.Context().Value(ctxUserRole).(string)
	return v
}

// ContextSessionID extracts the session UUID injected by RequireAuth.
func ContextSessionID(r *http.Request) uuid.UUID {
	v, _ := r.Context().Value(ctxSessionID).(uuid.UUID)
	return v
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
