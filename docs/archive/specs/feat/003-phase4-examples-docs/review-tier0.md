# Review: Tier 0 Preflight Fixes

- **Branch**: `fix/004-tier0-preflight`
- **Base**: `feat/003-phase4-examples-docs`
- **Commits**: 2 (test(cells): add unit tests for order-cell + device-cell; fix: Tier 0 preflight)
- **Review Baseline**: `3b616233d8bc0799f205347cce3ac044c2d66683`
- **Reviewer**: All 6 seats (consolidated)
- **Date**: 2026-04-06

---

## Summary

The branch adds 68 new test functions across order-cell and device-cell (repository, service, handler, cell lifecycle, route integration), adds `ErrSessionNotFound` and other sentinel codes to `pkg/errcode`, provides sso-bff/todo-order/iot-device README files with curl commands, adds `start_period: 15s` to docker-compose healthchecks, removes `|| true` from CI example validation, and creates the full CI workflow.

Overall: solid test suite with good coverage of the happy path, error paths, and boundary conditions. Several issues need attention before merge.

---

## Findings

### F-01 [P0] Seat 5 (DX/Maintainability) + Seat 1 (Architecture): Duplicate `ErrSessionNotFound` with conflicting error code values

- **File**: `/Users/shengming/Documents/code/gocell/cells/access-core/internal/mem/session_repo.go` line 13; `/Users/shengming/Documents/code/gocell/pkg/errcode/errcode.go` line 32
- **Evidence**:
  - `pkg/errcode/errcode.go:32`: `ErrSessionNotFound Code = "ERR_SESSION_NOT_FOUND"`
  - `cells/access-core/internal/mem/session_repo.go:13`: `ErrSessionNotFound errcode.Code = "ERR_AUTH_SESSION_NOT_FOUND"`
  - `cells/access-core/slices/sessionlogout/service.go:77`: uses `errcode.ErrSessionNotFound` (canonical, `"ERR_SESSION_NOT_FOUND"`)
  - The session_repo uses its local `ErrSessionNotFound` (value `"ERR_AUTH_SESSION_NOT_FOUND"`) at lines 54, 66, 78, 90, 127
- **Problem**: Two different error code values exist for the same semantic concept within the same Cell. The repository layer returns `ERR_AUTH_SESSION_NOT_FOUND` while the service layer returns `ERR_SESSION_NOT_FOUND`. This creates inconsistent API responses for the consumer and breaks `errors.As`-based matching against a single sentinel. The local constant in `session_repo.go` shadows the canonical package-level constant, violating the errcode centralization rule.
- **Fix**: Delete the local constant in `session_repo.go:13` and replace all 5 usages (lines 54, 66, 78, 90, 127) with `errcode.ErrSessionNotFound`. Alternatively, if `ERR_AUTH_SESSION_NOT_FOUND` is the intended value, update `pkg/errcode/errcode.go` to match and remove the local redefinition.
- **Status**: OPEN

---

### F-02 [P1] Seat 5 (DX/Maintainability) + Seat 6 (Product/UX): order-cell and device-cell misuse kernel error code `ErrCellNotFound` for domain "not found" errors

- **Files**: `/Users/shengming/Documents/code/gocell/cells/order-cell/internal/mem/repository.go` line 49; `/Users/shengming/Documents/code/gocell/cells/device-cell/internal/mem/repository.go` lines 52, 106
- **Evidence**:
  - `order-cell/internal/mem/repository.go:49`: `errcode.New(errcode.ErrCellNotFound, fmt.Sprintf("order %q not found", id))`
  - `device-cell/internal/mem/repository.go:52`: `errcode.New(errcode.ErrCellNotFound, fmt.Sprintf("device %q not found", id))`
  - `device-cell/internal/mem/repository.go:106`: `errcode.New(errcode.ErrCellNotFound, fmt.Sprintf("command %q not found", cmdID))`
  - `pkg/errcode/errcode.go:15`: `ErrCellNotFound Code = "ERR_CELL_NOT_FOUND"` -- this is a kernel-level code for Cell registry lookup failures
- **Problem**: `ERR_CELL_NOT_FOUND` is a kernel concept (Cell not found in the assembly registry). Using it for "order not found" or "device not found" in domain repositories is semantically misleading. The API consumer sees `ERR_CELL_NOT_FOUND` and thinks it is a framework/infrastructure error, not a domain "resource not found" error. This is especially confusing because these example Cells are the "golden path" that developers will copy. While the HTTP status mapping (`mapCodeToStatus` uses `strings.Contains(c, "NOT_FOUND")`) happens to produce the correct 404, the error code value in the JSON body is wrong.
- **Fix**: Add domain-appropriate sentinel codes to `pkg/errcode`: `ErrOrderNotFound Code = "ERR_ORDER_NOT_FOUND"`, `ErrDeviceNotFound Code = "ERR_DEVICE_NOT_FOUND"`, `ErrCommandNotFound Code = "ERR_COMMAND_NOT_FOUND"`. Use these in the respective repositories.
- **Status**: OPEN

---

### F-03 [P1] Seat 6 (Product/UX): sso-bff README step 9 curl URL does not match actual route for feature flags

- **File**: `/Users/shengming/Documents/code/gocell/examples/sso-bff/README.md` line 92
- **Evidence**:
  - README step 9: `curl -s http://localhost:8081/api/v1/config/flags | jq`
  - Actual route in `config-core/cell.go:167`: `v1.Route("/flags", ...)` which resolves to `/api/v1/flags/`, NOT `/api/v1/config/flags`
  - The config CRUD routes are under `/api/v1/config/`, but feature flags are at `/api/v1/flags/` (a sibling, not a child)
- **Problem**: The curl command in the README will return a 404, breaking the documented walkthrough for the evaluator. Since `/api/v1/config/{key}` is a dynamic route, `curl /api/v1/config/flags` might actually match the config-read GET handler and return a config entry with key "flags" (which doesn't exist), producing a confusing error rather than the expected flags list.
- **Fix**: Change README line 92 to `curl -s http://localhost:8081/api/v1/flags | jq` (remove `/config` prefix).
- **Status**: OPEN

---

### F-04 [P2] Seat 6 (Product/UX): sso-bff README has duplicate step numbering

- **File**: `/Users/shengming/Documents/code/gocell/examples/sso-bff/README.md` lines 51, 57
- **Evidence**:
  - Line 51: `### 4. List users`
  - Line 57: `### 4. Logout (delete session)`
  - Steps should be numbered 4 and 5 respectively, with subsequent steps renumbered
- **Problem**: Duplicate step 4 confuses the walkthrough sequence.
- **Fix**: Renumber: "4. List users", "5. Logout", "6. Query audit entries", "7. Create config", "8. Update config", "9. Read config", "10. List feature flags", "11. Verify audit trail", "12. Health checks".
- **Status**: OPEN

---

### F-05 [P1] Seat 4 (Ops/Deploy): CI example validation step is a no-op -- example directories have no metadata files

- **File**: `/Users/shengming/Documents/code/gocell/.github/workflows/ci.yml` lines 36-42
- **Evidence**:
  - CI step: `for dir in examples/*/; do ... go run ./cmd/gocell validate --root "$dir" ...`
  - `examples/todo-order/` contains only: `main.go`, `README.md`, `docker-compose.yml`
  - `examples/iot-device/` contains only: `main.go`, `README.md`, `docker-compose.yml`
  - `examples/sso-bff/` contains only: `main.go`, `README.md`, `docker-compose.yml`
  - The metadata parser (`kernel/metadata/parser.go`) looks for `cells/*/cell.yaml`, `contracts/*/contract.yaml`, etc. under the `--root` directory. None of these subdirectories exist under `examples/*/`.
  - The actual cell.yaml files are at `cells/order-cell/cell.yaml`, `cells/device-cell/cell.yaml` (under the project root, not under the example directories).
- **Problem**: The `|| true` removal is safe (no false failures) but the validation itself is vacuous. The parser finds 0 metadata files, reports "0 error(s), 0 warning(s)", and passes. The example metadata is never validated in CI. This was the original reason for `|| true` -- the step was always going to pass trivially.
- **Fix**: Either (a) restructure so example cells have their metadata under the example directory, or (b) replace the per-example `--root` loop with a targeted check that the main project validation (which already runs at line 33) covers cells/order-cell and cells/device-cell metadata. The simplest fix: remove the per-example validation loop (it provides false confidence) and rely on the main `gocell validate` step (line 33) which already covers `cells/order-cell/cell.yaml` etc. since they are under the project root.
- **Status**: OPEN

---

### F-06 [P2] Seat 6 (Product/UX): List endpoints in order-cell and device-cell lack `page` field in response

- **Files**: `/Users/shengming/Documents/code/gocell/cells/order-cell/slices/order-query/handler.go` lines 42-45; `/Users/shengming/Documents/code/gocell/cells/device-cell/slices/device-command/handler.go` lines 64-67
- **Evidence**:
  - `order-query/handler.go:42-45`: `httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": orders, "total": len(orders)})` -- no `page` field
  - API versioning rule in `.claude/rules/gocell/api-versioning.md`: `{"data":...,"total":...,"page":...}`
  - This is consistent with ALL existing cells (config-core, audit-core, rbaccheck all omit `page` too), so this is a pre-existing project-wide pattern
- **Problem**: The documented API response format specifies a `page` field. None of the list endpoints include it. Since this is a demo example meant as a golden path, it should model the documented format.
- **Fix**: Add `"page": 1` to list responses, or update the API versioning rule to make `page` optional for unpaginated lists. This is a pre-existing issue and not a regression from this branch.
- **Status**: OPEN

---

### F-07 [P2] Seat 6 (Product/UX): order-create POST response format inconsistent with GET endpoints

- **File**: `/Users/shengming/Documents/code/gocell/cells/order-cell/slices/order-create/handler.go` lines 41-45
- **Evidence**:
  - POST handler returns: `{"id":"...","item":"...","status":"..."}`  (flat, no `data` wrapper)
  - GET handler returns: `{"data": {...}}` (wrapped)
  - LIST handler returns: `{"data": [...], "total": N}` (wrapped)
  - Same inconsistency exists for device-register: POST returns `{"id":"...","name":"...","status":"..."}` (flat)
- **Problem**: Inconsistent response envelope between POST (flat) and GET (wrapped in `data`) within the same Cell. As a golden path example, this teaches developers to use inconsistent patterns.
- **Fix**: Wrap POST 201 responses in `{"data": {...}}` to match the GET pattern, or document the convention that creation responses are flat. Update README sample responses accordingly.
- **Status**: OPEN

---

### F-08 [P2] Seat 3 (Test/Regression): `TestSentinelCodes` in errcode_test.go does not cover newly added sentinel codes

- **File**: `/Users/shengming/Documents/code/gocell/pkg/errcode/errcode_test.go` lines 190-209
- **Evidence**:
  - `TestSentinelCodes` checks 10 codes: `ErrMetadataInvalid` through `ErrReferenceBroken`
  - Missing from the test: `ErrInternal`, `ErrAuthUnauthorized`, `ErrAuthForbidden`, `ErrRateLimited`, `ErrBodyTooLarge`, `ErrJourneyNotFound`, `ErrTestExecution`, `ErrBusClosed`, `ErrCellMissingOutbox`, `ErrSessionNotFound`, `ErrAdapterNoTx`, `ErrAuthKeyInvalid`, `ErrAuthTokenInvalid`, `ErrAuthTokenExpired`
  - 14 sentinel codes are not covered by the uniqueness/non-empty test
- **Problem**: The test was written when only 10 codes existed and was never updated. If a new code accidentally duplicates an existing value, the test will not catch it. The duplicate `ErrSessionNotFound` issue (F-01) demonstrates why this matters.
- **Fix**: Add all sentinel codes to the `TestSentinelCodes` slice. Consider using reflection or a code generation approach to automatically capture all constants.
- **Status**: OPEN

---

### F-09 [P2] Seat 4 (Ops/Deploy): iot-device docker-compose lacks rabbitmq service

- **File**: `/Users/shengming/Documents/code/gocell/examples/iot-device/docker-compose.yml`
- **Evidence**: The file defines only `postgres` and `redis` services. No `rabbitmq` service is present. Contrast with sso-bff and todo-order which both include rabbitmq with `start_period: 15s`.
- **Problem**: When the iot-device example is later extended to use real adapters (Docker Mode), it will need rabbitmq for event publishing. The `device-register` slice publishes `event.device-registered.v1`. Without rabbitmq in docker-compose, the Docker Mode section of the README would be incomplete. For now, since the example runs in-memory mode, this is not a blocker.
- **Fix**: Add rabbitmq service to iot-device docker-compose.yml consistent with the other two examples, or explicitly document in the README that rabbitmq is not needed for L4 cells (if that is the design intent).
- **Status**: OPEN

---

### F-10 [P1] Seat 3 (Test/Regression): order-cell and device-cell tests do not verify errcode types on all error paths

- **File**: `/Users/shengming/Documents/code/gocell/cells/device-cell/internal/mem/repository_test.go`
- **Evidence**:
  - `TestDeviceRepository_Create` line 56: `assert.Error(t, err)` -- does not check `errcode.Code`
  - `TestDeviceRepository_GetByID` line 95: `assert.Error(t, err)` -- does not check `errcode.Code`
  - `TestCommandRepository_Create` line 160: `assert.Error(t, err)` -- does not check `errcode.Code`
  - `TestCommandRepository_Ack` lines 258, 262: `assert.Error(t, err)` -- does not check `errcode.Code`
  - Contrast with `order-cell/internal/mem/repository_test.go:47-49` which correctly does `require.ErrorAs(t, err, &ecErr); assert.Equal(t, tt.errCode, ecErr.Code)`
- **Problem**: The device-cell repository tests only check `assert.Error(t, err)` without verifying the error is an `*errcode.Error` with the expected Code. This means the tests would pass even if the repository returned `errors.New("...")` instead of `errcode.New(...)`, defeating the errcode compliance rule. The order-cell tests do this correctly; device-cell tests are inconsistent.
- **Fix**: In device-cell repository tests, use `require.ErrorAs(t, err, &ecErr)` and `assert.Equal(t, expectedCode, ecErr.Code)` on all error-path assertions, following the pattern established in order-cell tests.
- **Status**: OPEN

---

### F-11 [P2] Seat 1 (Architecture): order-cell declares L2 but Init does not enforce outboxWriter fail-fast

- **File**: `/Users/shengming/Documents/code/gocell/cells/order-cell/cell.go` lines 86-89
- **Evidence**:
  - `cell.yaml:3`: `consistencyLevel: L2`
  - `cell.go:57-61`: BaseCell metadata declares `ConsistencyLevel: cell.L2`
  - `cell.go:86-89`: `if c.publisher == nil { c.logger.Warn("order-cell: no publisher injected...") }` -- warning only, no fail-fast
  - No check for `outboxWriter` anywhere in order-cell Init
  - KG-02/FR-7.3 requires L2+ Cells to fail-fast if outboxWriter is nil
- **Problem**: The order-cell claims L2 consistency but silently degrades. The comment at line 87 says "in demo mode we skip that" which is an intentional design choice for the example, but it contradicts the L2 contract. The `TestOrderCell_InitDefaults` test at cell_test.go:77 explicitly tests `"no options uses in-memory defaults"` and expects success with nil outboxWriter.
- **Fix**: This is likely intentional for the demo. Document the deviation: add a comment/README note that the example demonstrates L2 structure but runs in best-effort mode for simplicity. Alternatively, inject `noopWriter` (like sso-bff does) and add the fail-fast check.
- **Status**: OPEN

---

### F-12 [P2] Seat 4 (Ops/Deploy): docker-compose uses deprecated `version` key

- **Files**: All three docker-compose files (`sso-bff/docker-compose.yml`, `todo-order/docker-compose.yml`, `iot-device/docker-compose.yml`)
- **Evidence**: All three files start with `version: "3.9"` (line 1). Docker Compose V2 (the current standard) ignores the `version` key and issues a deprecation warning.
- **Problem**: The `version` key is deprecated in Docker Compose V2. While it still works, it generates a warning that could confuse developers following the tutorial. As a golden path example, it should demonstrate current best practices.
- **Fix**: Remove `version: "3.9"` from all three docker-compose files, or add a comment noting it is included for backward compatibility.
- **Status**: OPEN

---

### F-13 [P2] Seat 2 (Security): sso-bff example uses hardcoded HMAC key

- **File**: `/Users/shengming/Documents/code/gocell/examples/sso-bff/main.go` line 64
- **Evidence**: `auditHMACKey := []byte("sso-bff-dev-hmac-key-32-bytes!!!")`
- **Problem**: While this is a development-only example, the hardcoded key with no environment variable override makes it easy for teams to copy this pattern into production code. The key is deterministic and visible in the source code.
- **Fix**: Add a comment `// DO NOT use hardcoded keys in production. Use environment variable: GOCELL_AUDIT_HMAC_KEY` or read from env with a fallback: `hmacKey := os.Getenv("GOCELL_AUDIT_HMAC_KEY"); if hmacKey == "" { hmacKey = "sso-bff-dev-hmac-key-32-bytes!!!" }`.
- **Status**: OPEN

---

### F-14 [P2] Seat 3 (Test/Regression): No concurrent access test for in-memory repositories

- **Files**: `/Users/shengming/Documents/code/gocell/cells/order-cell/internal/mem/repository.go`, `/Users/shengming/Documents/code/gocell/cells/device-cell/internal/mem/repository.go`
- **Evidence**:
  - Both repositories use `sync.RWMutex` for thread safety
  - No test exercises concurrent access (no `t.Parallel()` + goroutine stress tests)
  - The repositories correctly implement locking, but the correctness of the locking under concurrent access is not verified
- **Problem**: The mutex-based synchronization is present but unverified. A race condition (e.g., missing `defer r.mu.Unlock()`) would go undetected because all tests are sequential.
- **Fix**: Add a concurrent access test for each repository, e.g., 100 goroutines doing Create/GetByID simultaneously. Run with `-race` flag to detect data races. This is a testing gap, not a production bug.
- **Status**: OPEN

---

### F-15 [P1] Seat 4 (Ops/Deploy): CI integration-test job defines static `services` that conflict with testcontainers concept

- **File**: `/Users/shengming/Documents/code/gocell/.github/workflows/ci.yml` lines 68-108
- **Evidence**:
  - The `integration-test` job defines GitHub Actions `services:` for postgres, redis, and rabbitmq (lines 74-108)
  - This creates static service containers managed by GitHub Actions, with hardcoded credentials (`gocell` / `gocell_dev`)
  - FR-6 specifies testcontainers-based integration tests, where containers are managed by the test code itself
  - If both static services AND testcontainers are used simultaneously, there will be port conflicts (both trying to bind port 5432, 6379, 5672)
- **Problem**: The CI job is designed for testcontainers tests (`go test -tags=integration ./adapters/...`) but also starts static service containers. When testcontainers tests start their own PostgreSQL/Redis/RabbitMQ containers, the ports will collide with the GitHub Actions `services:`. This will cause integration tests to either fail (port already in use) or connect to the wrong service (the static one instead of the testcontainers one).
- **Fix**: Either (a) remove the `services:` block and let testcontainers manage everything (the standard approach), or (b) if the current adapter integration tests use static connections (not testcontainers), keep the services but update the step comment to clarify this. Given that FR-6 testcontainers are not yet implemented (they are `t.Skip` stubs), the current approach is fine for now but must be revisited when testcontainers are actually implemented.
- **Status**: OPEN

---

## Docker Compose `start_period` Verification

- **sso-bff/docker-compose.yml**: rabbitmq healthcheck has `start_period: 15s` -- format correct
- **todo-order/docker-compose.yml**: rabbitmq healthcheck has `start_period: 15s` -- format correct
- **iot-device/docker-compose.yml**: no rabbitmq service, hence no `start_period` -- see F-09

The `start_period` is properly formatted as a duration string, which is the correct Docker Compose syntax.

---

## CI `|| true` Removal Assessment

The `|| true` has been removed from the CI example validation loop. The removal is safe because:
1. The `gocell validate --root examples/*/` step finds 0 metadata files (see F-05) and returns 0 errors
2. Therefore it never fails, so removing `|| true` has no practical effect
3. However, this means the step provides false confidence -- it appears to validate example metadata but actually validates nothing

The real concern is not that `|| true` removal causes CI failures, but that the validation step is vacuous (F-05).

---

## Test Quality Assessment (68 new tests)

### Strengths
- Table-driven tests throughout (order-cell repository: 11 subtests, device-cell repository: 15 subtests)
- Both happy-path and error-path coverage
- Copy-safety tests (`TestOrderRepository_Create_StoresCopy`, `TestOrderRepository_GetByID_ReturnsCopy`, `TestDeviceRepository_GetByID_ReturnsCopy`)
- Integration-style tests with real chi router (`initCellWithRouter` + `ServeHTTP`)
- End-to-end flow test (`TestService_Enqueue_ThenListPending_ThenAck`)
- Handler tests verify response status codes AND content-type headers
- Idempotency test (`TestCommandRepository_Ack` "ack already acked is idempotent")
- Publisher failure isolation test (`TestService_Register_PublishFails_StillReturnsDevice`, `TestService_Create_PublishesEvent`)

### Gaps
- No concurrent access tests despite mutex usage (F-14)
- device-cell repository tests lack errcode type assertions (F-10)
- No tests for empty body / nil body on POST endpoints
- `TestSentinelCodes` not updated for new codes (F-08)
- No test verifying that order-cell lifecycle (Start/Stop) affects route handler behavior
- No test for order List returning a large number of items (pagination stress)

---

## Findings Summary

| # | Severity | Seat | File(s) | Issue |
|---|----------|------|---------|-------|
| F-01 | **P0** | S5+S1 | errcode.go, session_repo.go | Duplicate `ErrSessionNotFound` with conflicting values |
| F-02 | P1 | S5+S6 | order-cell/mem/repository.go, device-cell/mem/repository.go | Misuse of kernel `ErrCellNotFound` for domain errors |
| F-03 | P1 | S6 | sso-bff/README.md | Feature flags curl URL wrong (`/config/flags` vs `/flags`) |
| F-04 | P2 | S6 | sso-bff/README.md | Duplicate step numbering (two "step 4"s) |
| F-05 | P1 | S4 | ci.yml | CI example validation is a no-op (no metadata in example dirs) |
| F-06 | P2 | S6 | order-query/handler.go, device-command/handler.go | List responses missing `page` field (pre-existing) |
| F-07 | P2 | S6 | order-create/handler.go, device-register/handler.go | POST response not wrapped in `{"data":...}` |
| F-08 | P2 | S3 | errcode_test.go | 14 sentinel codes missing from uniqueness test |
| F-09 | P2 | S4 | iot-device/docker-compose.yml | Missing rabbitmq service |
| F-10 | P1 | S3 | device-cell/mem/repository_test.go | Error path tests lack errcode type assertions |
| F-11 | P2 | S1 | order-cell/cell.go | L2 declared but no outboxWriter fail-fast |
| F-12 | P2 | S4 | all docker-compose files | Deprecated `version` key |
| F-13 | P2 | S2 | sso-bff/main.go | Hardcoded HMAC key without production warning |
| F-14 | P2 | S3 | both mem/repository.go | No concurrent access tests for mutex-guarded code |
| F-15 | P1 | S4 | ci.yml | Static services will conflict with testcontainers |

**Totals**: 1 P0, 5 P1, 9 P2

---

## Merge Recommendation

**Block on P0 (F-01)**: The duplicate `ErrSessionNotFound` with conflicting code values is a correctness issue that must be resolved before merge. The fix is straightforward (delete the local constant, use the canonical one).

**Strongly recommended before merge**: F-02 (errcode misuse in examples -- these are the golden path), F-03 (broken curl URL in README), F-05 (vacuous CI step).

**Can merge with follow-up**: F-10, F-15, and all P2 items.

---

*Generated: 2026-04-06*
*Baseline commit: 3b616233d8bc0799f205347cce3ac044c2d66683*
