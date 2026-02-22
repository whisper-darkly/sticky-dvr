---
title: Smoke test full stack — build, run, verify health and login
created: 2026-02-22
origin: POC audit verification step — confirms stack actually works end to end
priority: high
complexity: medium
notes:
  - 503 from health is acceptable (overseer not running)
  - Must tear down after test
  - Self-signed cert requires -k flag for curl
  - This is a verification job, not a code change job
---

Follow exactly and without stopping:

## Task: Smoke test the full sticky-dvr stack

## Background & Context

After Phase 2 fixes are committed and Makefile improvements are in place, verify that the
complete stack builds and runs correctly end-to-end.

## Steps

### 1. Build all images
```
make image-all
```
Expect: All 3 images build successfully (backend, frontend, proxy).
If any image fails to build, capture the error and report it.

### 2. Generate dev certs (if not already present)
```
make dev-certs
```
Expect: `certs/tls.crt` and `certs/tls.key` exist after this step.

### 3. Start the stack
```
make run-all
```
Then wait for all containers to be healthy (up to 60 seconds):
```
docker compose ps
```

### 4. Verify health endpoint
```
curl -sk https://localhost/api/health
```
Acceptable responses:
- HTTP 200 with `{"status":"ok",...}` — fully healthy
- HTTP 503 with `{"status":"degraded","overseer_connected":false,...}` — overseer offline (expected)

Any other response (connection refused, 500, 401, etc.) is a failure.

### 5. Verify login works
```
curl -sk -X POST https://localhost/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin"}' \
  -c /tmp/sticky-test-cookies.txt
```
Expect: HTTP 200 with a JSON body containing a `token` field.

### 6. Verify authenticated endpoint
Use the token from step 5:
```
curl -sk https://localhost/api/me \
  -H "Authorization: Bearer <token>"
```
Expect: HTTP 200 with user info showing `"role":"admin"`.

### 7. Tear down
```
make down
```
or
```
docker compose down
```

## Reporting

Report:
- Which steps passed/failed
- Any unexpected output or errors
- Docker container status at time of verification
- Whether `make bootstrap` (if available) worked as an alternative to steps 1-3

## Acceptance Criteria

- [ ] All 3 images build without error
- [ ] All containers start and reach healthy/running state
- [ ] `/api/health` returns 200 or 503 (either is acceptable)
- [ ] Login with admin/admin returns HTTP 200 with a token
- [ ] `/api/me` with valid token returns HTTP 200
- [ ] Stack tears down cleanly
