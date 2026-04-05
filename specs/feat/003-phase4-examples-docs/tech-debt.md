# Tech Debt Registry -- Phase 4: Examples + Documentation

## Review Metadata

- **Branch**: feat/003-phase4-examples-docs
- **Baseline commit**: 28ac80f
- **Date**: 2026-04-06
- **Source**: S6 Integration Review

---

## New Tech Debt Items (Phase 4)

### P4-TD-01: Shared noopWriter / NoopOutboxWriter not provided

- **Severity**: P2
- **Source**: S6 review P2-5
- **Affected files**: `src/examples/sso-bff/main.go:30-34`, `src/adapters/rabbitmq/integration_test.go:253-260`
- **Description**: Multiple locations define ad-hoc noop implementations of `outbox.Writer` or `idempotency.Checker`. KG-02 recommended a shared noop writer. Currently each consumer creates its own.
- **Recommended location**: `kernel/outbox/noop.go` or `pkg/testutil/noop.go`
- **Status**: OPEN
- **Priority**: Low -- functional but duplicated
- **Target**: v1.1

### P4-TD-02: chi.URLParam coupling in cell handlers

- **Severity**: P2
- **Source**: S6 review P2-3
- **Affected files**: All handler files that use `chi.URLParam` (6+ files across order-cell, device-cell, access-core, audit-core)
- **Description**: Cell handler code directly imports `github.com/go-chi/chi/v5` to extract URL parameters. This couples cell code to the chi router implementation despite the `kernel/cell.RouteMux` abstraction layer.
- **Recommended fix**: Add `URLParam(r *http.Request, key string) string` to `pkg/httputil`, delegating to chi internally, so cells import only `pkg/httputil`.
- **Status**: OPEN
- **Priority**: Low -- existing pattern since Phase 2
- **Target**: v1.1

### P4-TD-03: sessionvalidate.IssueTestToken HS256 dead code

- **Severity**: P1
- **Source**: S6 review P1-8
- **Affected file**: `src/cells/access-core/slices/sessionvalidate/service.go:64-91`
- **Description**: `IssueTestToken` accepts `[]byte` and `default` cases that produce HS256 tokens. Since `JWTVerifier` rejects all non-RS256 tokens, these code paths produce tokens that will always fail verification. This is misleading for test writers.
- **Recommended fix**: Remove HS256 paths from `IssueTestToken`, accept only `*rsa.PrivateKey`.
- **Status**: OPEN
- **Priority**: Medium -- test trap, not production risk
- **Target**: Phase 4 fix or v1.1

### P4-TD-04: order-cell L2 consistency declaration without outboxWriter enforcement

- **Severity**: P1
- **Source**: S6 review P2-1
- **Affected file**: `src/cells/order-cell/cell.go:56-103`
- **Description**: order-cell declares `consistencyLevel: L2` in both cell.yaml and code, but its Init does not validate outboxWriter presence (unlike access-core and config-core which fail-fast). The order-create service uses direct `publisher.Publish()` instead of transactional outbox writes. This creates a semantic gap: the metadata promises L2 but the code delivers best-effort publishing.
- **Recommended fix**: Either (a) enforce outboxWriter in Init like other L2 cells, or (b) downgrade to L1 and document why.
- **Status**: OPEN
- **Priority**: Medium -- architectural inconsistency
- **Target**: Phase 4 fix

### P4-TD-05: No outbox full-chain integration test (FR-6.5)

- **Severity**: P1
- **Source**: S6 review INT-1
- **Affected scope**: Cross-adapter integration
- **Description**: Spec FR-6.5 requires `TestIntegration_OutboxFullChain` spanning postgres outbox write -> relay -> rabbitmq publish -> subscribe -> redis idempotency check. Individual adapter integration tests exist but the full chain test that proves L2 end-to-end consistency is missing.
- **Status**: OPEN
- **Priority**: High -- core spec deliverable
- **Target**: Phase 4 fix (before gate)

### P4-TD-06: CI validation ignores example metadata errors

- **Severity**: P1
- **Source**: S6 review P1-9
- **Affected file**: `.github/workflows/ci.yml:42`
- **Description**: The `|| true` suffix on the example validation loop means all metadata validation errors are silently swallowed. This makes the CI step cosmetic rather than functional.
- **Status**: OPEN
- **Priority**: High -- CI quality gate integrity
- **Target**: Phase 4 fix

### P4-TD-07: Example docker-compose files missing start_period and using deprecated version key

- **Severity**: P1/P2
- **Source**: S6 review P1-5, P2-6
- **Affected files**: `src/examples/*/docker-compose.yml`
- **Description**: (a) `start_period: 15s` missing on rabbitmq healthcheck in all example compose files (root compose has it). (b) `version: "3.9"` key is deprecated in Docker Compose V2.
- **Status**: OPEN
- **Priority**: Medium
- **Target**: Phase 4 fix

---

## Inherited Tech Debt Status (from Phase 3)

| Phase 3 ID | Description | Phase 4 Status | Notes |
|-------------|-------------|----------------|-------|
| P3-TD-01 | testcontainers integration tests stubbed | RESOLVED | Real testcontainers tests implemented for postgres, redis, rabbitmq |
| P3-TD-02 | postgres adapter coverage 46.6% | PARTIALLY RESOLVED | testcontainers tests added, but actual coverage not measured in this review |
| P3-TD-03 | No CI workflow | RESOLVED | `.github/workflows/ci.yml` created with build, test, vet, validate, kernel coverage gate |
| P3-TD-05 | docker-compose missing start_period | RESOLVED (root) / OPEN (examples) | Root compose fixed, example compose files still missing |
| P3-TD-06 | outboxWriter nil guard silent fallback | RESOLVED | access-core, audit-core, config-core all have fail-fast with ERR_CELL_MISSING_OUTBOX |
| P3-TD-07 | testcontainers-go not in go.mod | RESOLVED | Added to go.mod (though incorrectly marked as indirect, see P1-2) |
| P3-TD-08 | WithEventBus not marked Deprecated | OPEN (P0-3) | Annotation still missing |
| P3-TD-09 | RS256 not default | RESOLVED with caveat | JWTIssuer/JWTVerifier are RS256-only. But access-core deprecated path generates ephemeral keys (P0-2) |
| MF-1 | testcontainers integration tests | RESOLVED | postgres, redis, rabbitmq integration tests implemented |
| MF-2 | postgres adapter coverage >= 80% | NEEDS VERIFICATION | Tests added, coverage not measured |
| MF-3 | S3 env prefix GOCELL_S3_* | RESOLVED | ConfigFromEnv uses GOCELL_S3_* with legacy fallback + slog.Warn |

---

## Deferred Items (Not Phase 4 Scope)

| ID | Description | Reason for Deferral | Target |
|----|-------------|---------------------|--------|
| P3-TD-10 | TOCTOU race in governance validate | High-risk refactor, not Phase 4 scope | v1.1 |
| P3-TD-11 | Domain model refactoring (access-core) | Requires significant Cell redesign | v1.1 |
| P3-TD-12 | Rollback version validation in config-core | Medium-risk, not Phase 4 scope | v1.1 |
| P4-TD-01 | Shared NoopWriter | Low priority | v1.1 |
| P4-TD-02 | chi.URLParam coupling | Existing pattern, low priority | v1.1 |

---

## Summary

Phase 4 successfully delivers:
- 3 example projects (sso-bff, todo-order, iot-device) with docker-compose and README
- testcontainers integration tests for postgres, redis, rabbitmq
- RS256 migration (JWTIssuer/JWTVerifier RS256-only)
- outboxWriter fail-fast on L2+ cells
- S3 env prefix migration with deprecation fallback
- CI workflow with build/test/vet/validate gates
- 6 project templates
- Framework README with tutorial

Key items requiring attention before gate:
1. **P0-2**: access-core ephemeral RSA key generation on deprecated path (security)
2. **P0-3**: WithEventBus missing Deprecated annotation (committed debt)
3. **P0-1**: String literal error codes in access-core
4. **P4-TD-05**: Missing outbox full-chain integration test (spec FR-6.5)
5. **P1-1**: Zero tests on new cells

---

*Generated by 6-Seat Reviewer on 2026-04-06*
