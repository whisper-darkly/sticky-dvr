#!/usr/bin/env bash
# test-api.sh — sticky-backend API integration tests
#
# Usage:
#   ./scripts/test-api.sh [--direct|--proxy] [BASE_URL]
#
# Modes:
#   --direct  Target the backend directly at http://localhost:8080 (no TLS, no JWT proxy).
#             Auth is Bearer token in Authorization header as returned by /api/auth/login.
#             Default when no mode flag is given.
#
#   --proxy   Target the full stack through sticky-proxy at https://localhost.
#             Uses -k (insecure) to accept self-signed dev certs.
#             Same API contract; proxy validates JWT and forwards X-User-Id/X-User-Role.
#
# Environment:
#   ADMIN_USER  (default: admin)
#   ADMIN_PASS  (default: admin)

set -euo pipefail

# ---- parse mode / URL ----
MODE="direct"
BASE_URL=""

for arg in "$@"; do
  case "$arg" in
    --direct) MODE="direct" ;;
    --proxy)  MODE="proxy"  ;;
    http*|ws*) BASE_URL="$arg" ;;
  esac
done

if [[ -z "$BASE_URL" ]]; then
  if [[ "$MODE" == "proxy" ]]; then
    BASE_URL="https://localhost"
  else
    BASE_URL="http://localhost:8080"
  fi
fi

ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASS="${ADMIN_PASS:-admin}"

# Extra curl flags: -k for self-signed certs in proxy mode.
CURL_EXTRA=""
if [[ "$MODE" == "proxy" ]]; then
  CURL_EXTRA="-k"
fi

# ---- colour helpers ----
RED='\033[0;31m'
GREEN='\033[0;32m'
RESET='\033[0m'

pass=0
fail=0

ok()   { echo -e "${GREEN}PASS${RESET} $1"; ((pass++)) || true; }
fail() { echo -e "${RED}FAIL${RESET} $1"; ((fail++)) || true; }

# ---- pre-flight checks ----
for cmd in curl jq; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: '$cmd' is required but not installed." >&2
    exit 2
  fi
done

# ---- helpers ----
# http <method> <path> [extra curl args...]
# Prints HTTP status code on stdout; response body goes to /tmp/sticky_resp
http() {
  local method="$1"; shift
  local path="$1";   shift
  curl -s -o /tmp/sticky_resp -w '%{http_code}' \
    $CURL_EXTRA \
    -X "$method" "${BASE_URL}${path}" "$@"
}

token=""

echo "=== sticky-backend API tests: $BASE_URL (mode: $MODE) ==="
echo

# 1. Health check (/api/health)
# Overseer is likely absent in CI, so we accept both 200 and 503.
status=$(http GET /api/health)
if [[ "$status" == "200" || "$status" == "503" ]]; then
  ok "1. GET /api/health → $status"
else
  fail "1. GET /api/health → $status (expected 200 or 503)"
fi

# 2. Login with bad password → 401
status=$(http POST /api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"wrong-password"}')
if [[ "$status" == "401" ]]; then
  ok "2. POST /api/auth/login bad password → 401"
else
  fail "2. POST /api/auth/login bad password → $status (expected 401)"
fi

# 3. Login with correct credentials → 200, extract token
status=$(http POST /api/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}")
if [[ "$status" == "200" ]]; then
  token=$(jq -r '.access_token // empty' /tmp/sticky_resp)
  if [[ -n "$token" ]]; then
    ok "3. POST /api/auth/login correct → 200, token obtained"
  else
    fail "3. POST /api/auth/login correct → 200, but no access_token in body"
  fi
else
  fail "3. POST /api/auth/login correct → $status (expected 200)"
fi

# 4. GET /api/me → 200
status=$(http GET /api/me -H "Authorization: Bearer $token")
if [[ "$status" == "200" ]]; then
  ok "4. GET /api/me → 200"
else
  fail "4. GET /api/me → $status (expected 200)"
fi

# 5. GET /api/subscriptions → 200
status=$(http GET /api/subscriptions -H "Authorization: Bearer $token")
if [[ "$status" == "200" ]]; then
  ok "5. GET /api/subscriptions → 200"
else
  fail "5. GET /api/subscriptions → $status (expected 200)"
fi

# 6. POST /api/subscriptions → 201
status=$(http POST /api/subscriptions \
  -H "Authorization: Bearer $token" \
  -H 'Content-Type: application/json' \
  -d '{"driver":"chaturbate","username":"testuser"}')
if [[ "$status" == "201" ]]; then
  ok "6. POST /api/subscriptions {chaturbate/testuser} → 201"
else
  fail "6. POST /api/subscriptions {chaturbate/testuser} → $status (expected 201)"
fi

# 7. GET /api/subscriptions/chaturbate/testuser → 200
status=$(http GET /api/subscriptions/chaturbate/testuser \
  -H "Authorization: Bearer $token")
if [[ "$status" == "200" ]]; then
  ok "7. GET /api/subscriptions/chaturbate/testuser → 200"
else
  fail "7. GET /api/subscriptions/chaturbate/testuser → $status (expected 200)"
fi

# 8. POST .../pause → 200
status=$(http POST /api/subscriptions/chaturbate/testuser/pause \
  -H "Authorization: Bearer $token")
if [[ "$status" == "200" ]]; then
  ok "8. POST /api/subscriptions/chaturbate/testuser/pause → 200"
else
  fail "8. POST /api/subscriptions/chaturbate/testuser/pause → $status (expected 200)"
fi

# 9. GET /api/config without auth → 401 (direct) or 403 (proxy — missing JWT)
status=$(http GET /api/config)
if [[ "$status" == "401" || "$status" == "403" ]]; then
  ok "9. GET /api/config (no auth) → $status"
else
  fail "9. GET /api/config (no auth) → $status (expected 401 or 403)"
fi

# 10. GET /api/config with admin token → 200
status=$(http GET /api/config -H "Authorization: Bearer $token")
if [[ "$status" == "200" ]]; then
  ok "10. GET /api/config (admin) → 200"
else
  fail "10. GET /api/config (admin) → $status (expected 200)"
fi

# 11. DELETE /api/subscriptions/chaturbate/testuser → 204
status=$(http DELETE /api/subscriptions/chaturbate/testuser \
  -H "Authorization: Bearer $token")
if [[ "$status" == "204" ]]; then
  ok "11. DELETE /api/subscriptions/chaturbate/testuser → 204"
else
  fail "11. DELETE /api/subscriptions/chaturbate/testuser → $status (expected 204)"
fi

# 12. POST /api/auth/logout → 204
status=$(http POST /api/auth/logout -H "Authorization: Bearer $token")
if [[ "$status" == "204" ]]; then
  ok "12. POST /api/auth/logout → 204"
else
  fail "12. POST /api/auth/logout → $status (expected 204)"
fi

# ---- summary ----
echo
total=$((pass + fail))
echo "=== Results: $pass/$total passed ==="
[[ $fail -eq 0 ]]
