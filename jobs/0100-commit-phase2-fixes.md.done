---
title: Commit Phase 2 working-tree fixes as a single logical commit
created: 2026-02-22
origin: POC audit revealed 9 uncommitted files with critical bug fixes that prevent the stack from working
priority: critical
complexity: low
notes:
  - Proxy auth was broken (upstream_http_x_user_id → js_var $jwt_user_id)
  - NJS module loading was broken in Dockerfile.proxy
  - Overseer protocol mismatch (old CLI-args → v2 action+map protocol)
  - Reset() API changed (now context-aware)
  - All 9 files are complete and correct — not in-progress work
---

Follow exactly and without stopping:

## Task: Commit Phase 2 working-tree fixes

## Background & Context

The sticky-dvr working tree contains 9 modified files representing complete, correct bug fixes
from Phase 2 development. These were left uncommitted after Phase 2 job execution. The fixes
include:

1. **proxy/proxy.conf** — Changed `$upstream_http_x_user_id` → `$jwt_user_id` (NJS variable).
   The old variable reads upstream *response* headers, not the NJS-set request variables.
   This silently passed empty user identity to the backend.

2. **Dockerfile.proxy** — Fixed NJS module loading: uses correct package name and absolute
   module path.

3. **backend/manager/manager.go** — Updated overseer Start() call from old CLI-args protocol
   `Start(ctx, taskID, []string{args...}, rp)` to v2 protocol
   `Start(ctx, taskID, action, map[string]string{"source": username}, rp)`.
   Also updated Reset() to be context-aware.

4. **backend/overseer/client.go** — Overseer client updated for v2 protocol.

5. **backend/converter/client.go** — Converter client implementation.

6. **compose.yaml** — Compose configuration updates.

7. **proxy/jwt-validate.js** — NJS JWT validation script fixes.

8. **Makefile** — Makefile improvements.

9. **.gitignore** — Gitignore updates.

10. **scripts/test-api.sh** — Integration test script improvements.

## Steps

1. Stage all 9 modified files:
   ```
   git add .gitignore Dockerfile.proxy Makefile backend/converter/client.go \
     backend/manager/manager.go backend/overseer/client.go compose.yaml \
     proxy/jwt-validate.js proxy/proxy.conf scripts/test-api.sh
   ```

2. Commit with a clear message describing the Phase 2 fixes:
   ```
   git commit -m "fix: Phase 2 bug fixes — proxy auth, NJS loading, overseer v2 protocol"
   ```

## Acceptance Criteria

- [ ] `git status` shows clean working tree (no modified files)
- [ ] `git log --oneline -3` shows the new commit at HEAD
- [ ] The commit message clearly describes the Phase 2 fixes
