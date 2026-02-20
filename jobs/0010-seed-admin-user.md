---
title: Seed admin user on first startup
created: 2026-02-20
origin: sticky-dvr backend Phase 1 refactor - PostgreSQL migration discovered no mechanism to create initial admin user on fresh deployments
priority: high
complexity: medium
notes:
  - Must use auth.HashPassword() from backend/auth/auth.go
  - Only seeds if no users exist in database
  - Env vars: ADMIN_USERNAME (default "admin"), ADMIN_PASSWORD (required, must be set for seeding to occur)
  - Without this, fresh deployments cannot log in to the API at all
  - Need to update both Dockerfile.backend and compose.yaml with new env vars
---

Follow exactly and without stopping:

## Task: Implement SeedAdminUser mechanism for initial deployment

## Background & Context

During the sticky-dvr backend Phase 1 refactor (sticky-dvr repo, backend/ directory), the database was migrated from SQLite to PostgreSQL. The new schema includes a `users` table with `id`, `username`, `password_hash`, `role`, `created_at`, and `updated_at` fields. The `backend/store/postgres/postgres.go` `Open()` function successfully runs migrations but provides no mechanism to seed the initial admin user. 

When operators deploy sticky-dvr fresh (no existing database), they cannot log in to the system at all because `POST /api/auth/login` requires a user to exist, but there are zero users in the database. This blocks the entire API - the system is unusable on first boot.

The backend already has an `auth.HashPassword()` function in `backend/auth/auth.go` that uses bcrypt for secure password hashing. The store interface provides `CreateUser(ctx, username, passwordHash, role)` method in the postgres implementation.

## Problem Description

Specific issue: Fresh deployments with empty PostgreSQL database have no admin user. When an operator tries to log in with initial credentials, the login endpoint returns 401 "user not found" because no users exist.

Current state: `backend/main.go` calls `postgres.Open(ctx, dbDSN)` which runs migrations, then immediately calls `config.Load(ctx, db)`. There is no code path to create an initial admin user.

Workaround (manual): Operator must manually connect to PostgreSQL and INSERT a user row, or create a separate script and run it independently.

Discovery: Identified during sticky-dvr Phase 1 implementation review when setting up local test deployments.

## Implementation Plan

1. In `backend/store/postgres/postgres.go`, add new method `SeedAdminUser(ctx context.Context, username, password string) error` that:
   - Queries `SELECT COUNT(*) FROM users`
   - If count > 0, return nil (users already exist, skip seeding)
   - If count == 0 (fresh database):
     - Call `auth.HashPassword(password)` to hash the password
     - Call the existing `d.CreateUser(ctx, username, passwordHash, "admin")` to insert the admin user
     - Return any error from CreateUser
   - Handle import of auth package at top of postgres.go

2. In `backend/main.go`, after the line `db, err := postgres.Open(ctx, dbDSN)`:
   - Read env var `ADMIN_USERNAME` using the existing `env()` helper function with default "admin"
   - Read env var `ADMIN_PASSWORD` using `os.Getenv()` (no default; optional)
   - Add import for `github.com/whisper-darkly/sticky-dvr/backend/auth` if not present
   - If `ADMIN_PASSWORD` is non-empty string:
     - Call `db.SeedAdminUser(ctx, adminUsername, adminPassword)`
     - Log result: on success: "seeded admin user: {username}", on error: log fatally with "seed admin user: {error}"
   - If `ADMIN_PASSWORD` is empty:
     - Log warning: "ADMIN_PASSWORD not set; skipping admin user seeding"

3. Update `Dockerfile.backend` (currently at /home/mmulligan/Development/whisper-darkly-github/sticky-dvr/Dockerfile.backend):
   - Add two new ENV declarations after the existing ENV lines (after line 29):
     - `ENV ADMIN_USERNAME=admin`
     - `ENV ADMIN_PASSWORD=""`
   - Add comments explaining these are for initial deployment seeding only

4. Update `compose.yaml` (currently at /home/mmulligan/Development/whisper-darkly-github/sticky-dvr/compose.yaml):
   - In the `backend` service's `environment` section, add two new lines:
     - `ADMIN_USERNAME: admin`
     - `ADMIN_PASSWORD: change-me-on-first-run` (or empty string, depending on desired default)

## Technical Requirements

- File paths:
  - `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/store/postgres/postgres.go`
  - `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/main.go`
  - `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/Dockerfile.backend`
  - `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/compose.yaml`

- Dependencies:
  - `github.com/whisper-darkly/sticky-dvr/backend/auth` (provides HashPassword)
  - `context` (standard library, already imported)
  - `fmt` (standard library, already imported)

- Commands to use:
  - `auth.HashPassword(password string) (string, error)` from auth package
  - Existing method `d.CreateUser(ctx, username, passwordHash, role string) (*store.User, error)`
  - SQL: `SELECT COUNT(*) FROM users` to check if database is seeded

- Reference implementation:
  - auth.HashPassword() is in `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/auth/auth.go` lines 62-69
  - CreateUser() is in `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/store/postgres/postgres.go` lines 85-98
  - Current main.go flow: postgres.Open (line 42) → config.Load (line 49)

## Success Criteria

- Fresh deployment with empty PostgreSQL database creates admin user automatically when ADMIN_PASSWORD env var is set
- Subsequent restarts with existing database do not attempt to re-create the user
- Log message clearly indicates success: "seeded admin user: admin" (or chosen username)
- ADMIN_PASSWORD="" (empty) prevents seeding and logs appropriate warning
- POST /api/auth/login with admin credentials works immediately after fresh deployment
- All four modified files compile without errors
- No existing functionality is broken (all other startup steps unchanged)

## Expected Outcome

On fresh deployment, if ADMIN_PASSWORD is set:
- sticky-backend starts
- Migrations run
- Admin user is automatically created in PostgreSQL with the provided credentials
- Log line shows: "seeded admin user: admin" (or configured username)
- Operator can immediately POST /api/auth/login without manual database setup
- Deployment is fully functional upon boot

On subsequent restarts:
- SeedAdminUser detects existing users and skips (no error, no action)
- System starts normally

On deployment without ADMIN_PASSWORD set:
- Log shows warning: "ADMIN_PASSWORD not set; skipping admin user seeding"
- System continues normal startup (useful for environments with pre-populated databases)

## Reference Information

- Current main.go startup sequence: `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/main.go` lines 22-69
- Database schema users table: Defined in migrations (embedded in postgres.go via migrations/*.sql)
- auth.HashPassword source: `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/auth/auth.go` lines 62-69
- CreateUser existing implementation: `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/backend/store/postgres/postgres.go` lines 85-98
- Testing endpoint: POST /api/auth/login with body `{username, password}` returns 200 + JWT on success

## Notes & Warnings

- Do NOT call SeedAdminUser before postgres.Open() completes and migrations run - the tables must exist
- Do NOT default ADMIN_PASSWORD to a fixed value - leaving it empty is the safe default for security reasons
- The seeding only happens if database has zero users - this prevents accidentally recreating admin accounts on restarts
- auth.HashPassword() uses bcrypt.DefaultCost which is CPU-intensive; this is only run once per deployment so performance is not a concern
- Error handling: If SeedAdminUser fails (e.g., duplicate username due to race condition), log.Fatalf is appropriate since the DB is in unknown state
- The env() helper function exists in main.go (line 105-110) for reading env vars with defaults; use it for ADMIN_USERNAME
- For ADMIN_PASSWORD, use os.Getenv() directly to keep it optional (empty string means "don't seed")
