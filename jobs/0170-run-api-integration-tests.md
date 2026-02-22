---
title: Run API integration tests against local backend and fix any failures
created: 2026-02-22
origin: POC audit verification step — confirms API contracts are correct
priority: high
complexity: medium
notes:
  - Run scripts/test-api.sh --direct against locally running backend+postgres
  - Fix any test failures found
  - Report pass/fail counts
  - Do not run --proxy mode in this job (that requires full stack from job 0160)
---

Follow exactly and without stopping:

## Task: Run API integration tests and fix failures

## Background & Context

The `scripts/test-api.sh` script performs integration tests against the sticky-dvr API.
It supports two modes:
- `--direct`: hits the backend directly (no proxy, no JWT)
- `--proxy`: hits through the NGINX proxy (requires full stack)

This job focuses on `--direct` mode, which requires:
1. A running PostgreSQL instance (from docker compose)
2. A running sticky-backend instance (direct, not proxied)

## Steps

### 1. Start dependencies
Start just the database:
```
docker compose up -d postgres
```
Wait for postgres to be healthy (check with `docker compose ps`).

### 2. Start backend directly
Read the Makefile for the `run-backend` target or similar. If there's a direct run target,
use it. Otherwise start the backend binary directly with required env vars:
```
DB_DSN="postgres://sticky:sticky@localhost:5432/sticky?sslmode=disable" \
JWT_SECRET="devsecret" \
./dist/sticky-backend
```

Or if using `make run-backend` (check the Makefile first).

The backend listens on port 8080 by default.

### 3. Run integration tests
```
BACKEND_URL=http://localhost:8080 bash scripts/test-api.sh --direct
```

Or check `scripts/test-api.sh` for the correct invocation syntax/env vars.

### 4. Fix failures
If any tests fail:
- Investigate the root cause (read the failing test code in test-api.sh)
- Fix the backend code or the test as appropriate
- Re-run until all tests pass

### 5. Report results
Report:
- Total tests run
- Pass count
- Fail count
- Any tests that were fixed and what the fix was

### 6. Tear down
```
docker compose down
```

## Notes

- The test script was updated as part of Phase 2 (one of the 9 committed files)
- Read the script carefully before running to understand what env vars it needs
- If the backend binary doesn't exist, run `make build-backend` first
- The admin seed user (username: admin, password: admin) should be created automatically
  by the backend on first startup if the DB is empty

## Acceptance Criteria

- [ ] All integration tests pass (or failures are documented with root cause)
- [ ] Pass count reported
- [ ] Any test failures fixed in backend code or test script
- [ ] Stack torn down after test run
