# Sticky DVR — Quick Start Guide

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/) (with Compose plugin v2.20+)
- `make`
- `openssl` (for self-signed TLS cert generation)

---

## Quick Start (single command)

```bash
make bootstrap
```

This command:
1. Generates a self-signed TLS certificate in `certs/` (skipped if already present)
2. Builds all three Docker images (`sticky-backend`, `sticky-frontend`, `sticky-proxy`)
3. Starts the full stack with `docker compose up -d`

Then open **https://localhost** in your browser, accept the self-signed certificate
warning, and log in with:

| Field    | Value   |
|----------|---------|
| Username | `admin` |
| Password | `admin` |

> **Change the admin password** in `compose.yaml` before exposing to a network.

---

## What to Expect

### Health status

The stack health endpoint (`/api/health`) may return **HTTP 503** when the `sticky-overseer`
container is not running. This is **normal and expected** — it means the overseer recording
worker pool is offline, not that the stack itself is broken.

The dashboard shows an amber **"Overseer offline"** badge in this state rather than an error.
The `overseer_connected: false` field in the health response body distinguishes this
from a real stack failure.

### First login

- The subscriptions list is empty on first login — add subscriptions to start recording.
- Admin users see additional nav items: **Config**, **Users**, **Sources**.

---

## Step-by-Step (manual)

If you prefer to run each step individually:

```bash
# 1. Generate self-signed TLS certs (localhost only)
make dev-certs

# 2. Build all three Docker images
make image-all

# 3. Start the stack
make run-all

# 4. Check container status
docker compose ps
```

---

## Useful Commands

| Command          | Description                                   |
|------------------|-----------------------------------------------|
| `make bootstrap` | One-command: certs + images + up              |
| `make run-all`   | Start all services (images must already exist) |
| `make down`      | Stop and remove all containers                |
| `make restart`   | Restart all services without rebuilding       |
| `make logs-all`  | Follow logs from all services                 |
| `make logs`      | Follow backend logs only                      |
| `make dev`       | HTTP-only dev mode (no TLS, port 8080)        |
| `make dev-down`  | Stop HTTP-only dev stack                      |

---

## HTTP-Only Dev Mode

For local development without TLS certificate warnings:

```bash
make dev
```

The stack starts on **http://localhost:8080**. No TLS, no JWT validation.
All requests are treated as the `admin` user (user ID 1).

> This mode is for **local development only** — it bypasses all authentication.

Stop with: `make dev-down`

---

## Environment Variables

These are set in `compose.yaml` and can be overridden at runtime:

| Variable          | Default                                    | Description                                    |
|-------------------|--------------------------------------------|------------------------------------------------|
| `DB_DSN`          | `postgres://sticky:changeme@postgres/sticky` | PostgreSQL connection string                  |
| `JWT_SECRET`      | `change-me-in-production`                  | HS256 shared secret (must match proxy + backend) |
| `OVERSEER_URL`    | `ws://overseer:8080/ws`                    | WebSocket URL for the overseer worker pool     |
| `CONVERTER_URL`   | `ws://converter:8080/ws`                   | WebSocket URL for the converter service        |
| `ADMIN_USERNAME`  | `admin`                                    | Initial admin account username                 |
| `ADMIN_PASSWORD`  | `admin`                                    | Initial admin account password                 |

> `JWT_SECRET` **must** be identical for `proxy` and `backend` services or login will fail.

---

## Troubleshooting

**Port 443 already in use**
```
Error: bind: address already in use
```
Another process (e.g., another NGINX) is using port 443. Stop it or use `make dev` (port 8080).

**Certificate warning in browser**
Expected for self-signed certs. Click "Advanced" → "Proceed to localhost (unsafe)" in Chrome,
or "Accept the Risk" in Firefox.

**Login returns 401 / "invalid token"**
The `JWT_SECRET` env var differs between the `proxy` and `backend` services in `compose.yaml`.
They must be identical strings.

**Backend won't start — database connection refused**
The `backend` service depends on `db-init` which depends on `postgres`. Check postgres health:
```bash
docker compose ps postgres
docker compose logs postgres
```

**`make image-all` fails — Go build error**
Ensure you have not modified Go source files with syntax errors. Run `make build-backend`
locally (requires Go 1.22+) to see the compiler error clearly.

**`make dev-certs` fails — openssl not found**
Install `openssl` via your package manager:
- Ubuntu/Debian: `sudo apt install openssl`
- macOS: `brew install openssl`
