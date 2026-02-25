# sticky-dvr — CLAUDE.md

## Repository Purpose

This repo is the **control plane** of the Sticky DVR system. It houses three
tightly coupled components tracked together because they share API contracts
and HTML/JS assets:

1. **sticky-backend** — Go HTTP API server (this is the existing code)
2. **sticky-frontend** — Flat HTML/CSS/JS SPA (no SSR; served by NGINX)
3. **sticky-proxy** — NGINX reverse-proxy + JWT-validation layer (Dockerfile only)

These three ship as separate Docker images built from this single repo.

---

## System Architecture

The full Sticky DVR system consists of these Docker containers:

| Container | Repo | Role |
|---|---|---|
| `sticky-overseer` | `../sticky-overseer` | Recorder worker pool overseer (WebSocket) |
| `sticky-converter` | `../sticky-converter` | Conversion worker pool |
| `sticky-thumbnailer` | *(not yet impl.)* | Thumbnail server `/{driver}/{source}[/file.jpg]` |
| **sticky-backend** | **this repo** | REST API — users, subscriptions, config |
| **sticky-frontend** | **this repo** | Flat HTML/CSS/JS SPA |
| **sticky-proxy** | **this repo** | NGINX — terminates TLS, validates JWT, reverse-proxies |

The proxy is the only public-facing entry point. It:
- Validates JWT signature via a Lua/NJS plugin (shared secret with backend)
- Passes valid sessions to the backend or returns 403 → redirect `/login`
- Directly proxies `/thumbnails/` to sticky-thumbnailer (no auth for cached thumbs)
- Serves the frontend static files directly from its own image

---

## Data Model

### PostgreSQL (replaces SQLite)

```
users
  id            BIGSERIAL PK
  username      TEXT UNIQUE NOT NULL
  password_hash TEXT NOT NULL          -- bcrypt
  role          TEXT NOT NULL DEFAULT 'user'  -- 'admin' | 'user'
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()

sessions
  id            UUID PK DEFAULT gen_random_uuid()
  user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE
  expires_at    TIMESTAMPTZ NOT NULL
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()

sources
  id            BIGSERIAL PK
  driver        TEXT NOT NULL           -- 'chaturbate' | 'stripchat' | …
  username      TEXT NOT NULL
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
  UNIQUE (driver, username)

subscriptions
  id            BIGSERIAL PK
  user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE
  source_id     BIGINT NOT NULL REFERENCES sources(id)
  posture       TEXT NOT NULL DEFAULT 'active'
                  -- 'active' | 'paused' | 'archived'
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
  UNIQUE (user_id, source_id)

worker_events
  id              BIGSERIAL PK
  source_id       BIGINT NOT NULL REFERENCES sources(id)
  pid             INT NOT NULL
  event_type      TEXT NOT NULL   -- 'started' | 'exited' | 'stopped'
  exit_code       INT             -- NULL unless event_type = 'exited'
  ts              TIMESTAMPTZ NOT NULL DEFAULT now()
```

**Key rules:**
- A `source` is recorded when ≥1 `subscription` with posture `active` exists.
- Admins see all sources/subscriptions; users see only their own.
- Re-subscribing to an existing source reuses the existing `sources` row.

---

## Authentication & Authorization

- Login: `POST /api/auth/login` → returns JWT (short-lived, e.g. 15 min) +
  refresh token stored as HttpOnly cookie; session row inserted into `sessions`.
- JWT payload: `{ sub: userID, session_id: uuid, role: "admin"|"user", exp }`
- NGINX validates JWT signature (shared HS256 secret via env var).
  - Invalid/missing → 403 + `X-Redirect-To: /login` header (frontend handles redirect).
  - Valid → `X-User-Id` and `X-User-Role` headers forwarded to backend.
- Backend trusts these headers (only reachable through proxy).
- Refresh: `POST /api/auth/refresh` (cookie-based); issues new JWT + rotates session.
- Logout: `POST /api/auth/logout`; deletes session row.

---

## API Surface (target)

```
POST   /api/auth/login
POST   /api/auth/refresh
POST   /api/auth/logout

GET    /api/me                           -- current user profile

# Subscriptions (scoped to requesting user; admin sees all)
GET    /api/subscriptions
POST   /api/subscriptions                -- body: {driver, username}
GET    /api/subscriptions/{driver}/{username}
DELETE /api/subscriptions/{driver}/{username}
POST   /api/subscriptions/{driver}/{username}/pause
POST   /api/subscriptions/{driver}/{username}/resume
POST   /api/subscriptions/{driver}/{username}/archive
POST   /api/subscriptions/{driver}/{username}/reset-error

# Per-source data (admin only)
GET    /api/sources/{driver}/{username}/events
GET    /api/sources/{driver}/{username}/logs
GET    /api/sources/{driver}/{username}/files

# Admin: config
GET    /api/config
PUT    /api/config

# Admin: users
GET    /api/users
POST   /api/users
GET    /api/users/{id}
PUT    /api/users/{id}
DELETE /api/users/{id}

# System
GET    /api/health
GET    /api/workers                      -- proxy to overseer list (admin)
```

---

## Frontend Pages (flat HTML/JS)

The SPA polls the API (no WebSockets, no SSR).

- `/login` — login form
- `/` (dashboard) — active sources overview, recording status, quick actions
- `/subscriptions` — user's subscription list with posture controls
- `/source/{driver}/{username}` — per-source detail: status, logs, files, events
- `/admin/config` — global server config editor (admin only)
- `/admin/users` — user management (admin only)
- `/admin/sources` — all sources across all users (admin only)

---

## Build Targets

```makefile
build-backend   # go build → dist/sticky-backend
build-frontend  # rsync/copy frontend/ → dist/frontend/
image-backend   # docker build -f Dockerfile.backend
image-frontend  # docker build -f Dockerfile.frontend   (nginx + static files)
image-proxy     # docker build -f Dockerfile.proxy      (nginx + JWT plugin)
```

---

## Configuration

- Config stored in PostgreSQL, not flat files.
- YAML still acceptable for bootstrap/seed values loaded at startup if DB is empty.
- Env vars for secrets: `DB_DSN`, `JWT_SECRET`, `OVERSEER_URL`, `CONVERTER_URL`.

---

## Versioning

- All repos in this network use a `./VERSION` file (plain `MAJOR.MINOR.PATCH`) to
  track the current version. This is the source of truth — not go.mod, not a tag.
- When committing functional changes, bump `VERSION` in the same commit. At minimum,
  increment the patch. Use minor for new features, major for breaking changes.
- When asked to "bump" or "tag", update `VERSION` first, then tag to match.
- Git tags are `v`-prefixed: `v0.3.0` for VERSION `0.3.0`.

---

## Key Conventions

- Go 1.22+ stdlib `net/http` mux (no third-party router).
- PostgreSQL via `pgx/v5` (pure Go, no CGO).
- Migrations via embedded SQL files using `golang-migrate` or a simple embedded
  ordered-file runner — no ORM.
- Frontend: vanilla JS, no build step, no framework. Polling interval ~5 s.
- Posture vs State: "posture" is a user-level intent (`active`/`paused`/`archived`);
  "state" is the operational reality from the overseer (`recording`/`idle`/`error`).
- Admins manage global config and all users; regular users manage their own subs.
- The `source` entity is driver-scoped — `chaturbate/alice` and `stripchat/alice`
  are distinct sources.
- Error window / threshold logic lives in the backend, keyed by `source_id`.
