# PR #125 Review: fix/225-contract-correctness

**Review baseline commit**: `491227a7190bfe69d39e90e229953ce81054999e`
**Branch**: `fix/225-contract-correctness`
**Date**: 2026-04-15

---

## Summary

PR #125 addresses four contract correctness issues:
1. (#27d) Error propagation from `publishEvent`/`publishChange`/`publish` to preserve L2 atomicity
2. (#3) Added `endpoints.http` metadata to all 19 HTTP contracts, plus a new `http.auth.session.delete.v1` contract
3. (#4) Rewrote `contract_test.go` in 8 slices to invoke real handlers/services
4. (#22) Added explicit 204 No Content negative test

---

## Findings

### S1 Architecture Consistency

**S1-F1 [C2] Missing `http.auth.session.delete.v1` contract test in sessionlogout slice**

The new contract `http.auth.session.delete.v1` is declared in `cells/access-core/slices/session-logout/slice.yaml` (line 4) with `role: serve`, and `verify.contract` lists `contract.http.auth.session.delete.v1.serve` (line 12). However, `cells/access-core/slices/sessionlogout/contract_test.go` contains only `TestEventSessionRevokedV1Publish` -- there is no test function that loads `http.auth.session.delete.v1` and validates HTTP request/response via `ValidateHTTPResponseRecorder`.

The handler exists (`sessionlogout/handler.go` `HandleLogout`) and returns 204, and there is a handler_test.go that tests 204/404, but this is not a contract test -- it does not load the contract YAML or validate against the schema.

Evidence:
- `cells/access-core/slices/session-logout/slice.yaml:12`: `contract.http.auth.session.delete.v1.serve`
- `cells/access-core/slices/sessionlogout/contract_test.go`: no reference to `http.auth.session.delete`

Suggestion: Add a contract test similar to `TestHttpAuthUserDeleteV1Serve` in identitymanage that:
1. Seeds a session
2. Sends `DELETE /api/v1/access/sessions/{id}` via httptest
3. Calls `c.ValidateHTTPResponseRecorder(t, rec)` against the loaded contract
4. Asserts `c.ValidateRequest(t, []byte('{}'))` and `c.MustRejectRequest(t, []byte('{"unexpected":true}'))`

---

**S1-F2 [C3] `endpoints.http.clients` is empty on 10 of 19 HTTP contracts**

Several HTTP contracts (`http.auth.refresh.v1`, `http.config.flags.*.v1`, `http.order.*.v1`, `http.device.*.v1`) declare `clients: []`. While this is syntactically valid, empty client lists weaken contract governance -- a contract with no declared consumers cannot be validated for cross-cell coverage.

Evidence:
- `contracts/http/auth/refresh/v1/contract.yaml:9`: `clients: []`
- `contracts/http/config/flags/list/v1/contract.yaml:9`: `clients: []`
- (and 8 others)

Suggestion: Declare at least the intended consumer (e.g. `edge-bff`) or add a `lifecycle: draft` annotation to signal that client mappings are deferred.

---

**S1-F3 [C3] Consistency level on session delete contract may need review**

`contracts/http/auth/session/delete/v1/contract.yaml` declares `consistencyLevel: L2`, which is correct because session revocation writes to the outbox. However, the other session-related HTTP contracts (`http.auth.login.v1`, `http.auth.refresh.v1`) declare `L1`. This asymmetry is semantically correct (login does not use outbox), but worth documenting the rationale for future maintainers.

Evidence:
- `contracts/http/auth/session/delete/v1/contract.yaml:4`: `consistencyLevel: L2`
- `contracts/http/auth/login/v1/contract.yaml:4`: `consistencyLevel: L1`

---

### S2 Security / Permissions

**S2-F1 [C3] New DELETE endpoint is covered by auth middleware**

The integration test `TestAuthWiring_RealAssembly_ProtectedRoutes401` at `cmd/core-bundle/auth_integration_test.go:129` includes `{http.MethodDelete, "/api/v1/access/sessions/some-id"}` in the protected routes list. This confirms the new session-delete endpoint is behind JWT auth. No security issue found.

---

### S3 Testing / Regression

**S3-F1 [C2] Missing outbox write error propagation test for sessionlogout**

All other L2 services that got #27d fixes have explicit `OUTBOX-WRITE-ERR-01` tests:
- `identitymanage/outbox_test.go:TestService_Create_OutboxWriteError`
- `identitymanage/outbox_test.go:TestService_Lock_OutboxWriteError`
- `configwrite/service_test.go:TestService_Create_OutboxWriteError` (and Update, Delete)
- `configpublish/service_test.go:TestService_Publish_OutboxWriteError` (and Rollback)

But `sessionlogout` has no such test, even though its `Logout()` method (service.go lines 88-98) does propagate outbox write errors.

Evidence:
- `cells/access-core/slices/sessionlogout/outbox_test.go`: no `OutboxWriteError` test
- `cells/access-core/slices/sessionlogout/service.go:95-97`: `if writeErr := s.outboxWriter.Write(txCtx, entry); writeErr != nil { return fmt.Errorf(...) }`

Suggestion: Add:
```go
func TestService_Logout_OutboxWriteError(t *testing.T) {
    repo := mem.NewSessionRepository()
    seedSession(repo, "sess-1", "usr-1")
    failWriter := &stubOutboxWriter{err: errors.New("outbox unavailable")}
    // add an err field to stubOutboxWriter as in identitymanage
    svc := NewService(repo, eventbus.New(), slog.Default(),
        WithOutboxWriter(failWriter), WithTxManager(&stubTxRunner{}))
    err := svc.Logout(context.Background(), "sess-1")
    require.Error(t, err)
    assert.Contains(t, err.Error(), "outbox")
}
```

---

**S3-F2 [C3] 8 contract tests still use hardcoded JSON instead of real handlers**

The PR rewrote 8 slices, but 8 remain with hardcoded JSON validation:
- `sessionlogin/contract_test.go` -- hardcoded JSON for login response/session.created event
- `sessionrefresh/contract_test.go` -- hardcoded JSON for refresh response
- `configsubscribe/contract_test.go` -- hardcoded JSON for event payloads
- `configread/contract_test.go` -- hardcoded JSON (with TODO #16)
- `featureflag/contract_test.go` -- hardcoded JSON (with TODO #16)
- `auditappend/contract_test.go` -- hardcoded JSON for event payloads
- `auditverify/contract_test.go` -- hardcoded JSON for event payloads
- `device-command/contract_test.go` -- hardcoded JSON for command schemas

Some of these have valid reasons (configread/featureflag have TODO #16 for PascalCase issue). The event subscriber tests (auditappend, configsubscribe) legitimately validate schema acceptance only. But `sessionlogin` and `sessionrefresh` could invoke real handlers.

This is not blocking; just noting the remaining gap for tracking.

---

**S3-F3 [C3] `capturingTB` nil-panic risk**

In `cells/access-core/slices/identitymanage/contract_test.go` lines 273-279, `capturingTB` embeds `testing.TB` as a nil interface. It only implements `Helper()` and `Errorf()`. If `ValidateHTTPResponseRecorder` calls any other `testing.TB` method (e.g., `Logf`, `Name`, `Cleanup`), it will panic with a nil pointer dereference.

Currently this is safe because the code path only calls `Errorf`/`Helper`. But it is fragile if `contracttest.go` is later modified.

Evidence:
- `contract_test.go:273-279`: `type capturingTB struct { testing.TB; errored bool }`

Suggestion: Consider adding a `Fatalf` method (since `contracttest.go` uses `t.Fatalf` in `Load` paths, though those are not reached in this test). Or use a more robust testing double.

---

**S3-F4 [C3] Unused `httptest.NewRecorder()` call in unlock test**

In `cells/access-core/slices/identitymanage/contract_test.go` line 199:
```go
httptest.NewRecorder()  // return value discarded
lockReq := httptest.NewRequest(lockContract.HTTP.Method, lockPath, nil)
handler.ServeHTTP(httptest.NewRecorder(), lockReq)
```
Line 199 creates a recorder whose return value is discarded. This is a minor waste but does not affect correctness.

---

### S4 Operations / Deployment

**S4-F1 [C3] No migration or infrastructure changes**

This PR is purely code + metadata (contract YAML). No Dockerfile, docker-compose, CI, or migration changes. No findings.

---

### S5 DX / Maintainability

**S5-F1 [C2] Duplicate test double types across packages**

The following types are reimplemented in multiple packages:
- `recordingWriter` / `contractRecordingWriter` / `stubOutboxWriter`: defined in identitymanage, sessionlogout, configwrite, configpublish, order-create
- `noopTxRunner` / `stubTxRunner` / `contractTxRunner`: defined in identitymanage, sessionlogout, configwrite, configpublish

Each is ~10 lines. Having 5+ copies increases maintenance burden.

Evidence:
- `identitymanage/outbox_test.go:22-33` (stubOutboxWriter)
- `identitymanage/contract_test.go:27-42` (contractRecordingWriter, contractTxRunner)
- `sessionlogout/outbox_test.go:17-22` (stubOutboxWriter)
- `configwrite/service_test.go:19-32` (recordingWriter)
- `configpublish/service_test.go:21-34` (recordingWriter)

Suggestion: Extract to a shared test helper package (e.g., `kernel/outbox/outboxtest` or `pkg/testutil`).

---

**S5-F2 [C3] Topic constant `TopicConfigChanged` is declared twice**

`TopicConfigChanged = "event.config.changed.v1"` is declared in both:
- `cells/config-core/slices/configwrite/service.go:22`
- `cells/config-core/slices/configpublish/service.go:23`

This violates the "string used >= 3 times must be a constant" rule -- the constant exists, but is duplicated across packages.

Evidence:
- `configwrite/service.go:22`: `TopicConfigChanged = "event.config.changed.v1"`
- `configpublish/service.go:23`: `TopicConfigChanged = "event.config.changed.v1"`

Suggestion: Define once in a shared location (e.g., `cells/config-core/topics.go` or `cells/config-core/internal/domain/topics.go`).

---

**S5-F3 [C3] Demo-mode publisher error is logged but comment says "not propagated"**

In `identitymanage/service.go:235-239`, `configwrite/service.go:191-196`, and `configpublish/service.go:177-180`, when outboxWriter is nil (demo mode), publisher errors are logged but not returned. The code comment says "demo mode does not guarantee L2 atomicity" which is correct. However, the logging uses `slog.Error` level -- per observability rules, `Error` level is for correctness-affecting failures. In demo mode, this is a degraded operation, so `Warn` may be more appropriate.

Evidence:
- `identitymanage/service.go:236`: `s.logger.Error("identity-manage: failed to publish event",...)`
- `configwrite/service.go:191`: `s.logger.Error("config-write: failed to publish event",...)`
- `configpublish/service.go:178`: `s.logger.Error("config-publish: failed to publish event",...)`

Suggestion: Use `slog.Warn` for demo-mode publisher failures, reserving `slog.Error` for real L2 atomicity violations.

---

### S6 Product / User Experience

**S6-F1 [C3] Lock/Unlock API returns `{"data":{"status":"locked"}}` -- no user context**

`identitymanage/handler.go:167` returns `{"data": {"status": "locked"}}` for lock, and `{"data": {"status": "active"}}` for unlock. This provides minimal feedback -- the user ID, username, and timestamps are not included. While the PR does not change this behavior, the contract test now validates against it. Consider including the full `UserResponse` DTO in a future iteration to match the pattern used by create/get/update/patch.

Evidence:
- `identitymanage/handler.go:167`: `httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"status": "locked"}})`

---

## Findings Summary

| ID | Seat | Severity | Description |
|----|------|----------|-------------|
| S1-F1 | Architecture | C2 | Missing contract test for `http.auth.session.delete.v1` |
| S1-F2 | Architecture | C3 | Empty `clients: []` on 10 HTTP contracts |
| S1-F3 | Architecture | C3 | L2 vs L1 consistency level asymmetry worth documenting |
| S3-F1 | Testing | C2 | Missing outbox write error test for sessionlogout |
| S3-F2 | Testing | C3 | 8 contract tests still use hardcoded JSON |
| S3-F3 | Testing | C3 | `capturingTB` nil-panic risk if contracttest.go changes |
| S3-F4 | Testing | C3 | Unused `httptest.NewRecorder()` return value |
| S5-F1 | DX | C2 | Duplicate test double types across 5 packages |
| S5-F2 | DX | C3 | `TopicConfigChanged` constant declared twice |
| S5-F3 | DX | C3 | Demo-mode publisher errors logged at Error instead of Warn |
| S6-F1 | Product | C3 | Lock/Unlock returns minimal status, no user context |

**Totals**: 0 C1 (must fix), 3 C2 (should fix), 8 C3 (nice to have)

**Verdict**: No blocking issues. The C2 findings (S1-F1, S3-F1, S5-F1) should be addressed before or shortly after merge. The PR achieves its stated goals: L2 atomicity is preserved, HTTP contract metadata is complete, and 8 contract tests now invoke real handlers.
