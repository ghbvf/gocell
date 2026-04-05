# S6 Integration Review Findings -- Phase 4: Examples + Documentation

## Review Metadata

- **Branch**: feat/003-phase4-examples-docs
- **Baseline commit**: 28ac80f (Phase 3 complete, capability inventory)
- **Review date**: 2026-04-06
- **Review scope**: All changes from develop to HEAD on feat/003-phase4-examples-docs
- **Reviewer**: 6-Seat Reviewer (all seats)

---

## Summary

| Severity | Count |
|----------|-------|
| P0 (blocks merge) | 3 |
| P1 (should fix) | 9 |
| P2 (suggestions) | 7 |

---

## P0 Findings (Block Merge)

### P0-1: access-core Init uses string literal instead of typed errcode.Code

- **Seat**: S1 Architecture / S5 DX
- **Affected file**: `src/cells/access-core/cell.go:169, 172`
- **Evidence**:
  ```go
  return errcode.New("ERR_AUTH_MISSING_KEY", "JWT issuer/verifier or signing key is required")
  return errcode.New("ERR_AUTH_MISSING_KEY", "JWT signing key must be at least 32 bytes")
  ```
  `errcode.New` accepts `errcode.Code` as first argument. The string literal `"ERR_AUTH_MISSING_KEY"` compiles because `Code` is `type Code string`, but it violates the project convention of using declared sentinel constants in `pkg/errcode/errcode.go`. Meanwhile `ErrAuthKeyInvalid` exists as a declared sentinel. There is also no `ERR_AUTH_MISSING_KEY` defined in the errcode package. Similarly, `src/cells/access-core/slices/sessionlogout/service.go:77` uses bare string `"ERR_SESSION_NOT_FOUND"`.
- **Risk**: Inconsistent error code management; impossible to programmatically enumerate all error codes; may cause runtime mismatches in error handling middleware.
- **Fix**: Replace string literals with declared constants. Either use existing `ErrAuthKeyInvalid` or add a new sentinel `ErrAuthMissingKey` to `pkg/errcode/errcode.go`. Add `ErrSessionNotFound` as well.

### P0-2: access-core RS256 migration has HS256 fallback path that generates ephemeral RSA keys

- **Seat**: S2 Security
- **Affected file**: `src/cells/access-core/cell.go:159-184`
- **Evidence**:
  ```go
  // Fallback: generate an ephemeral RSA key pair from the HMAC key seed.
  if c.jwtIssuer == nil || c.jwtVerifier == nil {
      priv, pub := auth.MustGenerateTestKeyPair()
      ...
  }
  ```
  When `WithSigningKey([]byte)` is used (deprecated path), the code silently generates ephemeral RSA keys via `MustGenerateTestKeyPair()`. This means: (a) tokens signed with these keys cannot be verified after process restart; (b) the deprecated HS256 path does not actually use HS256 -- it generates random RSA keys, making the `signingKey` argument meaningless beyond the 32-byte length check; (c) this is a silent security degradation, not a fail-fast as spec FR-7.1/KG-01 requires.
- **Risk**: Tokens issued via the deprecated path are ephemeral and unreproducible. A restart invalidates all outstanding tokens without warning. The signingKey bytes are accepted but never used for actual signing.
- **Fix**: The deprecated `WithSigningKey` path should either: (a) deterministically derive RSA keys from the signing key bytes (using a KDF), or (b) refuse to start and return `ERR_AUTH_MISSING_KEY` with a message directing users to provide RSA keys via `WithJWTIssuer`/`WithJWTVerifier`. Option (b) is safer and aligns with the spec's fail-fast intent.

### P0-3: WithEventBus missing Deprecated annotation (spec FR-8.3 / P3-TD-08)

- **Seat**: S1 Architecture / S5 DX
- **Affected file**: `src/runtime/bootstrap/bootstrap.go:86`
- **Evidence**:
  ```go
  // WithEventBus is a convenience method that sets both Publisher and Subscriber
  // from an InMemoryEventBus. It is equivalent to calling WithPublisher(eb) and
  // WithSubscriber(eb). Retained for backward compatibility.
  func WithEventBus(eb *eventbus.InMemoryEventBus) Option {
  ```
  Spec FR-8.3 explicitly requires: `// Deprecated: Use WithPublisher and WithSubscriber instead.` This is also P3-TD-08 (Phase 3 tech-debt). The annotation is absent.
- **Risk**: Consumers will not receive deprecation warnings from linters (`staticcheck SA1019`). This was a committed Phase 3 debt item.
- **Fix**: Add `// Deprecated: Use WithPublisher and WithSubscriber instead.` as the first line of the doc comment, before the existing description.

---

## P1 Findings (Should Fix)

### P1-1: order-cell and device-cell have zero unit tests

- **Seat**: S3 Test/Regression
- **Affected files**: `src/cells/order-cell/**`, `src/cells/device-cell/**`
- **Evidence**: `Glob("src/cells/order-cell/**/*_test.go")` and `Glob("src/cells/device-cell/**/*_test.go")` return no results.
  New cells contain 7 service files and 6 handler files with no test coverage. The spec NFR and project convention require new code >= 80% coverage.
- **Fix**: Add table-driven tests for at minimum: `ordercreate.Service.Create`, `orderquery.Service.GetByID/List`, `deviceregister.Service.Register`, `devicecommand.Service.Enqueue/ListPending/Ack`, `devicestatus.Service.GetStatus`, and handler tests with httptest.

### P1-2: testcontainers-go and postgres module marked as `// indirect` in go.mod

- **Seat**: S4 Ops/Deploy
- **Affected file**: `src/go.mod:68-69`
- **Evidence**:
  ```
  github.com/testcontainers/testcontainers-go v0.41.0 // indirect
  github.com/testcontainers/testcontainers-go/modules/postgres v0.41.0 // indirect
  ```
  These are directly imported by `src/adapters/postgres/integration_test.go`, `src/adapters/redis/integration_test.go`, and `src/adapters/rabbitmq/integration_test.go`. They should be direct dependencies. The `// indirect` marker suggests `go mod tidy` was not run correctly, possibly because integration tests have a build tag and `go mod tidy` does not process build-tagged files by default.
- **Fix**: Run `go mod tidy -tags=integration` to correctly classify these dependencies, or manually remove the `// indirect` comment from the testcontainers entries.

### P1-3: List endpoints missing `page` field in response format

- **Seat**: S6 Product/UX
- **Affected files**: `src/cells/order-cell/slices/order-query/handler.go:42-45`, `src/cells/device-cell/slices/device-command/handler.go:64-67`
- **Evidence**:
  ```go
  httputil.WriteJSON(w, http.StatusOK, map[string]any{
      "data":  orders,
      "total": len(orders),
  })
  ```
  The API versioning rule (`.claude/rules/gocell/api-versioning.md`) and `templates/contract-review.md:30` require the unified format `{"data": ..., "total": ..., "page": ...}`. The `page` field is missing from all list responses in the new cells.
- **Fix**: Add `"page": 1` (or implement real pagination with query params `?page=1&pageSize=20`) to list responses.

### P1-4: No pagination enforcement on list endpoints

- **Seat**: S6 Product/UX
- **Affected files**: `src/cells/order-cell/slices/order-query/service.go:30`, `src/cells/device-cell/slices/device-command/service.go:66`
- **Evidence**: `orderquery.Service.List(ctx)` returns all orders with no limit. `devicecommand.Service.ListPending(ctx, deviceID)` returns all pending commands. The `order-query/handler.go` and `device-command/handler.go` do not accept pagination query parameters.
  Per S6 review focus: "list pagination enforcement (<=500)".
- **Fix**: Add pagination parameters (page/pageSize) to list handlers, with a maximum page size of 500.

### P1-5: Example docker-compose files missing `start_period` on rabbitmq healthcheck

- **Seat**: S4 Ops/Deploy
- **Affected files**: `src/examples/todo-order/docker-compose.yml`, `src/examples/sso-bff/docker-compose.yml`, `src/examples/iot-device/docker-compose.yml`
- **Evidence**: The root `docker-compose.yml` correctly has `start_period: 15s` on rabbitmq and minio (added per P3-TD-05). But all three example docker-compose files lack `start_period` on their rabbitmq healthcheck.
  ```yaml
  # todo-order docker-compose.yml line 38-42
  healthcheck:
    test: ["CMD", "rabbitmq-diagnostics", "-q", "ping"]
    interval: 10s
    timeout: 5s
    retries: 5
    # missing: start_period: 15s
  ```
- **Fix**: Add `start_period: 15s` to rabbitmq healthcheck in all three example docker-compose files.

### P1-6: sso-bff README missing refresh token and config hot-update curl examples

- **Seat**: S6 Product/UX
- **Affected file**: `src/examples/sso-bff/README.md`
- **Evidence**: Spec FR-1.4 requires `POST /api/v1/auth/refresh` demo. FR-1.6 requires config update demo with event. FR-1.7 requires complete curl sequence (login -> me -> refresh -> config update -> logout -> audit). The README omits: (a) token refresh curl command, (b) `GET /api/v1/access/users/{id}` (me endpoint), (c) demonstration of event publishing/consumption in logs.
- **Fix**: Add refresh curl command, add note about checking logs for event consumption confirmation, add "me" endpoint call between login and logout.

### P1-7: .env.example missing GOCELL_S3_REGION

- **Seat**: S4 Ops/Deploy
- **Affected file**: `.env.example:16-20`
- **Evidence**: `ConfigFromEnv()` in `src/adapters/s3/client.go:49` reads `GOCELL_S3_REGION`, and `Config.Validate()` requires region to be non-empty. But `.env.example` does not include `GOCELL_S3_REGION`. Users copying `.env.example` will get an S3 validation error.
- **Fix**: Add `GOCELL_S3_REGION=us-east-1` to `.env.example`.

### P1-8: sessionvalidate.IssueTestToken still supports HS256 signing

- **Seat**: S2 Security
- **Affected file**: `src/cells/access-core/slices/sessionvalidate/service.go:64-91`
- **Evidence**:
  ```go
  case []byte:
      token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
      return token.SignedString(k)
  default:
      token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
      return token.SignedString(signingKey)
  ```
  `IssueTestToken` accepts `[]byte` and defaults to HS256. While this is a test helper, `JWTVerifier.Verify()` explicitly rejects non-RS256 tokens. Tokens issued with `IssueTestToken([]byte(...), ...)` will always fail verification. This creates a trap for test writers who use the seemingly-supported `[]byte` path.
- **Fix**: Either (a) remove the `[]byte` and `default` cases from `IssueTestToken`, making it RS256-only (accept `*rsa.PrivateKey` only), or (b) add a clear comment warning that HS256 tokens will fail `JWTVerifier.Verify()` and are only for testing the rejection path.

### P1-9: CI workflow does not run `gocell validate` on examples under src/examples

- **Seat**: S4 Ops/Deploy
- **Affected file**: `.github/workflows/ci.yml:36-43`
- **Evidence**:
  ```yaml
  - name: Validate examples metadata
    run: |
      for dir in examples/*/; do
        ...
        go run ./cmd/gocell validate --root "$dir" || true
      done
  ```
  The `|| true` at the end means validation failures are silently ignored -- the CI step will never fail regardless of metadata errors. This defeats the purpose of the validation gate (KG-08, spec FR-8.1).
- **Fix**: Remove `|| true` so that metadata validation failures actually fail the CI pipeline.

---

## P2 Findings (Suggestions)

### P2-1: order-cell declared L2 but uses publisher.Publish directly (no outbox writer)

- **Seat**: S1 Architecture
- **Affected file**: `src/cells/order-cell/cell.go:87-89`, `src/cells/order-cell/slices/order-create/service.go:54-71`
- **Evidence**: order-cell declares `ConsistencyLevel: cell.L2` in metadata and code, but Init does not inject or validate outboxWriter. The `order-create` service uses `publisher.Publish()` directly (best-effort), not `outboxWriter.Write()` within a transaction. This contradicts the L2 OutboxFact pattern which requires transactional outbox writes.
  The three built-in L2 cells (access-core, audit-core, config-core) all enforce outboxWriter != nil in Init with fail-fast. order-cell does not.
- **Suggestion**: Either (a) add outboxWriter fail-fast to order-cell Init matching the pattern in access-core/config-core, or (b) document clearly that order-cell demonstrates L2 aspirational metadata with in-memory simplified event publishing for demo purposes.

### P2-2: order-cell uses deprecated `bootstrap.WithEventBus` in example main.go

- **Seat**: S5 DX
- **Affected files**: `src/examples/todo-order/main.go:52`, `src/examples/iot-device/main.go:48`, `src/examples/sso-bff/main.go:99`
- **Evidence**: All three examples use `bootstrap.WithEventBus(eb)`. This function should be marked Deprecated (P0-3). Examples should use `WithPublisher(eb)` + `WithSubscriber(eb)` to demonstrate the recommended API.
- **Suggestion**: Replace `WithEventBus(eb)` with `WithPublisher(eb), WithSubscriber(eb)` in all example main.go files.

### P2-3: order-query handler imports chi directly for URL params

- **Seat**: S1 Architecture
- **Affected files**: `src/cells/order-cell/slices/order-query/handler.go:6`, `src/cells/device-cell/slices/device-command/handler.go:7`, `src/cells/device-cell/slices/device-status/handler.go:6`
- **Evidence**: These handlers import `github.com/go-chi/chi/v5` directly to use `chi.URLParam(r, "id")`. This couples cell implementation to chi, while the architecture uses `kernel/cell.RouteMux` to abstract the router. Other cells (access-core, audit-core, config-core) also use chi, so this is a pre-existing pattern, but it means cells are not truly router-agnostic.
- **Suggestion**: Consider adding a `URLParam(r *http.Request, key string) string` helper to `pkg/httputil` that wraps chi.URLParam, reducing direct chi imports across cells.

### P2-4: README tutorial Step 3 missing required import for `http` and `context`

- **Seat**: S5 DX
- **Affected file**: `README.md:123-158`
- **Evidence**: The Step 3 code block uses `http.HandlerFunc` but the import block only imports `"context"`, `"log/slog"`, and `"github.com/ghbvf/gocell/kernel/cell"`. Missing import: `"net/http"`. Also, `cell.HTTPRegistrar` interface is implemented via `RegisterRoutes` but the compile-time check `var _ cell.HTTPRegistrar = (*MyCell)(nil)` is not shown.
- **Suggestion**: Add `"net/http"` to the import block in the tutorial code example. Add the compile-time interface assertion as a best practice.

### P2-5: sso-bff noopWriter defined locally instead of using a shared test utility

- **Seat**: S5 DX
- **Affected file**: `src/examples/sso-bff/main.go:30-34`
- **Evidence**: `type noopWriter struct{}` is defined inline. KG-02 suggested providing a shared noop writer (e.g., in `kernel/outbox` or `pkg/testutil`) for exactly this purpose. The rabbitmq integration test also defines its own `noopChecker` struct.
- **Suggestion**: Create a shared `outbox.NoopWriter` in the `kernel/outbox` package (or `pkg/testutil`), and use it in examples and tests to avoid duplication.

### P2-6: Example docker-compose files use deprecated `version: "3.9"` key

- **Seat**: S4 Ops/Deploy
- **Affected file**: `src/examples/todo-order/docker-compose.yml:1`
- **Evidence**: `version: "3.9"` is deprecated in Docker Compose V2 and generates a warning. The root `docker-compose.yml` correctly omits the `version` key.
- **Suggestion**: Remove the `version: "3.9"` line from example docker-compose files to match the root convention and suppress the deprecation warning.

### P2-7: templates/ directory exists but README claims it is at top-level while files are under src/

- **Seat**: S5 DX
- **Affected files**: `README.md:220`, `src/templates/`
- **Evidence**: README says:
  ```
  ├── templates/    — Project templates (ADR / cell-design / ...)
  ```
  suggesting templates are at the top-level `templates/` directory. But `Glob("templates/**/*")` at root returns nothing; the actual files are under `src/templates/`. The directory tree in README shows `src/` as the root of the codebase structure which is correct, but the bullet "Project Templates" section at line 262 lists paths without the `src/` prefix:
  ```
  - `templates/adr.md`
  ```
  This is fine since the working context is within `src/`, but could confuse users who are navigating from the repo root.
- **Suggestion**: Clarify in README that template paths are relative to `src/` or use full paths from repo root.

---

## Cross-PR Integration Issues

### INT-1: Outbox full-chain integration test not present

- **Seat**: S3 Test/Regression
- **Evidence**: Spec FR-6.5 requires `TestIntegration_OutboxFullChain` testing write -> relay -> publish -> consume -> idempotency check. The postgres integration test covers outbox writing; the rabbitmq integration test covers publish/consume. But there is no single test that chains: business write + outbox write (same tx) -> outbox relay poll -> RabbitMQ publish -> consumer consume -> idempotency check. This was a key spec deliverable.
- **Fix**: Create `TestIntegration_OutboxFullChain` that orchestrates all three adapters in a single test.

### INT-2: Contracts registered for order-cell and device-cell but not validated

- **Seat**: S1 Architecture
- **Evidence**: Contract files exist under `src/contracts/http/order/v1/contract.yaml`, `src/contracts/event/order-created/v1/contract.yaml`, `src/contracts/http/device/v1/contract.yaml`, etc. The slice YAML files reference these contracts in `contractUsages`. However, CI validation step uses `|| true` (P1-9), so these contract references are never actually validated in CI.

---

## Layering Constraint Checks (All Seats)

| Constraint | Result |
|------------|--------|
| kernel/ imports runtime/adapters/cells/ | PASS -- no violations |
| cells/ imports adapters/ | PASS -- no direct adapter imports |
| Cross-Cell internal/ imports | PASS -- no examples importing cells/*/internal/ |
| New CUD ops annotated with consistency level | PASS -- order-cell L2, device-cell L4 annotated |
| kernel/ code zero modification | PASS -- kernel files unchanged from Phase 3 |

---

*Generated by 6-Seat Reviewer on 2026-04-06*
