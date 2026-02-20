---
title: Periodic expired session cleanup
created: 2026-02-20
origin: sticky-dvr backend Phase 1 implementation - sessions table grows unboundedly with no cleanup mechanism
priority: medium
complexity: low
notes:
  - The DeleteExpiredSessions method already exists in store/postgres/postgres.go line 194
  - Expired sessions are checked on use (secure) but waste storage if accumulate indefinitely
  - One-hour cleanup interval is sufficient for typical deployments
  - Should log any errors but not fatally exit on cleanup failure
---

Follow exactly and without stopping:

## Task: Implement periodic cleanup of expired sessions

## Background & Context

The sticky-dvr backend Phase 1 (sticky-dvr repo, backend/ directory) implements a session management system with PostgreSQL. The `sessions` table stores user sessions with an `expires_at TIMESTAMPTZ` field. Sessions are checked on each API request to ensure validity - if a session has expired, it is rejected (security is maintained).

The store interface already provides a `DeleteExpiredSessions(ctx context.Context) error` method implemented in `backend/store/postgres/postgres.go` at line 194. This method queries and deletes all sessions where `expires_at < now()`. However, nothing in the system currently calls this method, so expired sessions accumulate indefinitely in the database.

While this is not a security issue (expired sessions are rejected on use), it causes the sessions table to grow unboundedly, wasting storage and causing slow queries over time.

## Problem Description

Specific issue: The sessions table will grow without bound as users log in over time. After weeks of use, the table could contain thousands of expired session rows that serve no purpose.

Current state: `backend/main.go` starts the HTTP server and manager but has no maintenance background tasks. The `DeleteExpiredSessions()` method exists but is never called.

Workaround (manual): Database administrator could manually run `DELETE FROM sessions WHERE expires_at < now()` on a schedule, but this is not automated.

Discovery: Identified during sticky-dvr Phase 1 implementation review as an obvious maintenance gap.

## Implementation Plan

1. In `backend/main.go`, after the manager starts successfully (after line 67-69 where `mgr.Start(ctx)` is called):
   - Add a new goroutine that runs indefinitely
   - Create a ticker with 1-hour interval: `ticker := time.NewTicker(1 * time.Hour)`
   - In the goroutine, loop: `for range ticker.C`
   - Call `db.DeleteExpiredSessions(ctx)` inside the loop
   - Log any errors returned (use log.Printf, not fatal - don't crash server on cleanup failure)
   - Log success is optional but helpful: `log.Printf("cleaned up expired sessions")` or similar

2. Example pattern to follow (after mgr.Start(ctx)):
   ```go
   go func() {
       ticker := time.NewTicker(1 * time.Hour)
       defer ticker.Stop()
       for range ticker.C {
           if err := db.DeleteExpiredSessions(ctx); err != nil {
               log.Printf("delete expired sessions: %v", err)
           }
       }
   }()
   ```

3. Ensure `time` package is imported (it already is in main.go)

## Technical Requirements

- File path:
  - `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/main.go`

- Dependencies:
  - `time` package (already imported in main.go)
  - `log` package (already imported in main.go)
  - `context` (already available as ctx variable)

- Existing method being called:
  - `db.DeleteExpiredSessions(ctx context.Context) error` from store/postgres/postgres.go line 194
  - This method returns error if SQL execution fails, nil on success

- Reference implementation:
  - Similar goroutine pattern used for overseer.Run(ctx) at line 65 in main.go
  - Current main.go location: `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/main.go` lines 22-103

## Success Criteria

- New goroutine starts successfully on server startup
- Ticker fires every 1 hour after server starts
- DeleteExpiredSessions is called each hour and returns without blocking
- Errors from DeleteExpiredSessions are logged but do not crash the server
- Server shutdown (sigterm/sigint) properly cleans up the ticker via defer
- Actual expired sessions are deleted from the database when cleanup runs
- Build compiles without errors
- No existing functionality is broken

## Expected Outcome

After implementation:
- sticky-backend starts normally
- Cleanup goroutine begins running in background
- Every 1 hour, expired sessions are silently cleaned from the database
- If cleanup has an error (network issue, disk full, etc.), log message appears but server continues running
- Sessions table stays bounded in size even with heavy user activity
- Storage waste from expired sessions is eliminated

Example log output (optional enhancement):
```
sticky-backend v0.2.0
listening on :8080
[1 hour later]
cleaned up expired sessions
[1 hour later]
delete expired sessions: context canceled (on shutdown)
```

## Reference Information

- Main.go startup flow: `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/main.go` lines 22-103
- DeleteExpiredSessions implementation: `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/store/postgres/postgres.go` line 194-200
- Similar background pattern (overseer): main.go line 65 - `go oc.Run(ctx)`
- Session table schema: Has `expires_at TIMESTAMPTZ NOT NULL`
- Time package docs: https://golang.org/pkg/time/

## Notes & Warnings

- The 1-hour interval is arbitrary but reasonable - adjust if different frequency is desired
- Ticker.Stop() MUST be called in defer to prevent goroutine leak on shutdown
- The context ctx is already in scope and will be canceled on shutdown (sigterm/sigint), so the goroutine will naturally exit
- DeleteExpiredSessions can be slow if sessions table is very large, but 1-hour interval provides adequate spacing
- Error logging using log.Printf is appropriate - use same style as existing error logs in main.go
- Do NOT use log.Fatalf for cleanup errors - the server must stay running even if cleanup fails once
- The cleanup will happen one hour after startup, then every hour thereafter - initial cleanup interval is normal and acceptable
