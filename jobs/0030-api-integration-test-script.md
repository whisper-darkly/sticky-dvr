---
title: Integration test script for Phase 1 API
created: 2026-02-20
origin: sticky-dvr backend Phase 1 implementation lacks automated regression testing
priority: medium
complexity: medium
notes:
  - Create shell script scripts/test-api.sh in the repository root
  - Tests must not require external data or database state beyond running sticky-backend
  - Should handle server not available gracefully
  - Color output makes test results easy to scan
  - Admin credentials are admin/admin by default (from CLAUDE.md seeding mechanism)
---

Follow exactly and without stopping:

## Task: Create automated API integration test script for Phase 1 endpoints

## Background & Context

The sticky-dvr backend Phase 1 (sticky-dvr repo, backend/ directory) was successfully implemented with a complete REST API including authentication, subscriptions, and admin endpoints. The implementation is documented in CLAUDE.md and includes endpoints like POST /api/auth/login, GET /api/me, GET /api/subscriptions, POST /api/subscriptions, pause/resume/archive operations, and admin config/users endpoints.

Currently, there is no automated way to verify the API works correctly after deployment or code changes. The original implementation plan included manual verification steps using curl commands, but these require human execution and are easy to forget or misrun.

The sticky-dvr system is containerized (docker-compose), so an integration test script that can be run after starting the containers would significantly improve confidence in deployments and catch regressions early.

## Problem Description

Specific issue: No automated regression testing for the Phase 1 API. Each deployment requires manual testing with curl commands to verify basic functionality works.

Current state: sticky-backend is implemented with all Phase 1 endpoints working, but verification requires manual steps documented somewhere (if documented at all). This is error-prone and time-consuming.

Example manual steps currently required:
- Start services: docker-compose up
- Login and capture token: curl POST /api/auth/login
- Test subscriptions: curl GET /api/subscriptions with token
- Test pause operation: curl POST /api/subscriptions/chaturbate/testuser/pause
- Compare responses to expected values
- This is all manual, no automation

Workaround (current): Operator manually runs curl commands and visually inspects responses.

Discovery: Identified during Phase 1 implementation as a gap in the testing story.

## Implementation Plan

1. Create new file `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/scripts/test-api.sh` with full test coverage

2. The script should:
   - Accept optional BASE_URL as first argument (default to http://localhost:8080)
   - Print header with API being tested
   - Define color codes: GREEN='\033[0;32m', RED='\033[0;31m', NC='\033[0m' (no color) for output
   - Test each endpoint in sequence with proper ordering

3. Implement 13 test cases (in order):
   
   Test 1: GET /api/health
   - Command: `curl -s -w "\n%{http_code}" -X GET $BASE_URL/api/health`
   - Expected: HTTP 503 (OK because overseer is not running in test environment, but endpoint exists)
   - Rationale: Verifies API is responding at all
   - Print: "[PASS] GET /api/health => 503" in green or "[FAIL] GET /api/health" in red
   
   Test 2: POST /api/auth/login with wrong password
   - Command: `curl -s -w "\n%{http_code}" -X POST $BASE_URL/api/auth/login -H "Content-Type: application/json" -d '{"username":"admin","password":"wrongpassword"}'`
   - Expected: HTTP 401 (Unauthorized)
   - Rationale: Ensures authentication rejects bad credentials
   - Print: "[PASS] POST /api/auth/login (bad password) => 401" or "[FAIL]"
   
   Test 3: POST /api/auth/login with correct admin credentials
   - Command: `curl -s -w "\n%{http_code}" -X POST $BASE_URL/api/auth/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}'`
   - Expected: HTTP 200, response body contains "access_token" field
   - Extract token: Parse response JSON, extract .access_token field, save to TOKEN variable
   - Rationale: Establishes authentication for remaining tests
   - Print: "[PASS] POST /api/auth/login => 200" or "[FAIL]"
   - Stop testing if this fails (all remaining tests need token)
   
   Test 4: GET /api/me with token
   - Command: `curl -s -w "\n%{http_code}" -X GET $BASE_URL/api/me -H "Authorization: Bearer $TOKEN"`
   - Expected: HTTP 200, response body contains "id", "username", "role" fields
   - Rationale: Verifies token is valid and user profile endpoint works
   - Print: "[PASS] GET /api/me => 200" or "[FAIL]"
   
   Test 5: GET /api/subscriptions with token
   - Command: `curl -s -w "\n%{http_code}" -X GET $BASE_URL/api/subscriptions -H "Authorization: Bearer $TOKEN"`
   - Expected: HTTP 200, response body is JSON array (initially empty or with existing subscriptions)
   - Rationale: Lists subscriptions for authenticated user
   - Print: "[PASS] GET /api/subscriptions => 200" or "[FAIL]"
   
   Test 6: POST /api/subscriptions create new subscription
   - Command: `curl -s -w "\n%{http_code}" -X POST $BASE_URL/api/subscriptions -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"driver":"chaturbate","username":"testuser"}'`
   - Expected: HTTP 201 (Created), response body contains subscription details
   - Rationale: Creates a test subscription for subsequent pause/resume tests
   - Print: "[PASS] POST /api/subscriptions => 201" or "[FAIL]"
   
   Test 7: GET /api/subscriptions/chaturbate/testuser retrieve specific subscription
   - Command: `curl -s -w "\n%{http_code}" -X GET $BASE_URL/api/subscriptions/chaturbate/testuser -H "Authorization: Bearer $TOKEN"`
   - Expected: HTTP 200, response contains subscription for chaturbate/testuser
   - Rationale: Retrieves specific subscription by driver and username
   - Print: "[PASS] GET /api/subscriptions/chaturbate/testuser => 200" or "[FAIL]"
   
   Test 8: POST /api/subscriptions/chaturbate/testuser/pause pause subscription
   - Command: `curl -s -w "\n%{http_code}" -X POST $BASE_URL/api/subscriptions/chaturbate/testuser/pause -H "Authorization: Bearer $TOKEN"`
   - Expected: HTTP 200, response shows subscription with posture "paused"
   - Rationale: Changes subscription status to paused
   - Print: "[PASS] POST /api/subscriptions/chaturbate/testuser/pause => 200" or "[FAIL]"
   
   Test 9: GET /api/config without token
   - Command: `curl -s -w "\n%{http_code}" -X GET $BASE_URL/api/config`
   - Expected: HTTP 401 (Unauthorized)
   - Rationale: Ensures config endpoint (admin only) rejects unauthenticated requests
   - Print: "[PASS] GET /api/config (no auth) => 401" or "[FAIL]"
   
   Test 10: GET /api/config with admin token
   - Command: `curl -s -w "\n%{http_code}" -X GET $BASE_URL/api/config -H "Authorization: Bearer $TOKEN"`
   - Expected: HTTP 200, response contains config object (depends on what's in config)
   - Rationale: Admin can retrieve global config
   - Print: "[PASS] GET /api/config (with token) => 200" or "[FAIL]"
   
   Test 11: DELETE /api/subscriptions/chaturbate/testuser delete subscription
   - Command: `curl -s -w "\n%{http_code}" -X DELETE $BASE_URL/api/subscriptions/chaturbate/testuser -H "Authorization: Bearer $TOKEN"`
   - Expected: HTTP 204 (No Content)
   - Rationale: Removes the test subscription
   - Print: "[PASS] DELETE /api/subscriptions/chaturbate/testuser => 204" or "[FAIL]"
   
   Test 12: POST /api/auth/logout logout with token
   - Command: `curl -s -w "\n%{http_code}" -X POST $BASE_URL/api/auth/logout -H "Authorization: Bearer $TOKEN"`
   - Expected: HTTP 204 (No Content)
   - Rationale: Invalidates the session/token
   - Print: "[PASS] POST /api/auth/logout => 204" or "[FAIL]"

4. Script structure:
   - Shebang: #!/bin/bash
   - Set -e (exit on error from curl or other commands)
   - Define BASE_URL="${1:-http://localhost:8080}" to accept argument or use default
   - Define color variables and helper function pass_test() and fail_test()
   - Each test should:
     a. Execute curl command capturing HTTP status code
     b. Check status code against expected value
     c. Optionally parse JSON response if needed (for token extraction)
     d. Call pass_test or fail_test with descriptive message
   - At end, print summary: "X passed, Y failed" with appropriate count
   - Exit with code 0 if all pass, 1 if any fail

5. Error handling:
   - If curl fails to connect, print "[ERROR] Cannot connect to $BASE_URL" and exit 1
   - If login fails, stop remaining tests (they all need token)
   - If JSON parsing fails when extracting token, exit with error
   - Gracefully handle curl timeouts (set --max-time 5 or similar)

## Technical Requirements

- File path to create:
  - `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/scripts/test-api.sh`

- Dependencies (standard UNIX tools):
  - curl (for HTTP requests)
  - jq (for JSON parsing to extract token)
  - bash (for script execution)
  - standard text tools: grep, cut, tail (for parsing curl output)

- Commands to use:
  - `curl -s -w "\n%{http_code}"` to capture HTTP status code
  - `echo $response | jq -r '.access_token'` to extract token from JSON
  - Standard bash string manipulation for colors and output

- Reference API endpoints (from CLAUDE.md):
  - POST /api/auth/login → returns JWT
  - GET /api/me → current user profile
  - GET /api/subscriptions → list subscriptions
  - POST /api/subscriptions → create subscription
  - GET /api/subscriptions/{driver}/{username} → get specific subscription
  - POST /api/subscriptions/{driver}/{username}/pause → pause subscription
  - GET /api/config → get global config (admin only)
  - DELETE /api/subscriptions/{driver}/{username} → delete subscription
  - POST /api/auth/logout → logout
  - GET /api/health → health check

## Success Criteria

- Script exists at `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/scripts/test-api.sh`
- Script is executable (chmod +x)
- Running `./scripts/test-api.sh` with no args tests http://localhost:8080
- Running `./scripts/test-api.sh http://custom:9000` tests custom URL
- All 12 test cases run in order
- Tests that depend on earlier results (like token) skip if prerequisite fails
- Color output clearly shows PASS in green and FAIL in red
- Test summary printed at end with count of passed/failed
- Script exits with code 0 if all tests pass, code 1 if any fail
- Script handles server not running gracefully (error message, exit 1)
- No external files or database setup required to run script

## Expected Outcome

Running `./scripts/test-api.sh` with sticky-backend running prints:

```
Testing sticky-dvr API at http://localhost:8080

[PASS] GET /api/health => 503
[PASS] POST /api/auth/login (bad password) => 401
[PASS] POST /api/auth/login => 200
[PASS] GET /api/me => 200
[PASS] GET /api/subscriptions => 200
[PASS] POST /api/subscriptions => 201
[PASS] GET /api/subscriptions/chaturbate/testuser => 200
[PASS] POST /api/subscriptions/chaturbate/testuser/pause => 200
[PASS] GET /api/config (no auth) => 401
[PASS] GET /api/config (with token) => 200
[PASS] DELETE /api/subscriptions/chaturbate/testuser => 204
[PASS] POST /api/auth/logout => 204

All 12 tests passed!
```

## Reference Information

- API contract: `/home/mmulligan/Development/whisper-darkly-github/sticky-dvr/CLAUDE.md` lines for "API Surface (target)"
- Example curl for login: `curl -X POST http://localhost:8080/api/auth/login -H "Content-Type: application/json" -d '{"username":"admin","password":"admin"}'`
- JWT token format: Bearer token returned in access_token field of login response
- Test server: sticky-backend running on port 8080 (default), should be started via docker-compose up
- Default admin credentials: username "admin" (from ADMIN_USERNAME env var), password "admin" (from ADMIN_PASSWORD env var in compose.yaml or Dockerfile)

## Notes & Warnings

- The script tests a fresh deployment scenario - admin user should exist (seeded by Job 1: seed-admin-user)
- HTTP 503 on /api/health is expected because sticky-overseer is not running in test environment (not a failure)
- Test assumes admin credentials are "admin"/"admin" - if changed via ADMIN_PASSWORD env var, script would need adjustment
- Script should be idempotent - running it multiple times should work (each run creates/deletes test subscription)
- Do NOT hardcode sensitive data like production passwords - keep admin/admin for testing only
- Curl output parsing is fragile with -w "\n%{http_code}" - ensure format is correct (newline before code)
- jq must be installed - add check at start of script: `command -v jq >/dev/null || { echo "jq not found"; exit 1; }`
- Same for curl: `command -v curl >/dev/null || { echo "curl not found"; exit 1; }`
- Test order matters: login must succeed before other authenticated tests run
- Each test should have a clear name that identifies what endpoint is being tested
