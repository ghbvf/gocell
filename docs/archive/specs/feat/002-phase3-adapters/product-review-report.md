# Product Review Report -- Phase 3: Adapters

> Branch: `feat/002-phase3-adapters`
> Role: Product Manager
> Date: 2026-04-06
> Tip commit: `cbab9f3` (chore: S7 gate PASS)
> Input materials:
>   - product-context.md (4 persona, 12 success criteria)
>   - product-acceptance-criteria.md (54 P1 + 14 P2 + 11 P3 = 79 AC)
>   - qa-report.md (60/60 go test PASS, validate 0 error)
>   - review-findings.md (3 P0 + 5 P1 + 7 P2 = 15 findings)
>   - tech-debt.md (9 TECH + 3 PRODUCT = 12 deferred)
>   - user-signoff.md (CONDITIONAL, B=4/5, C=3/5, D=3/5)
>   - gate-audit.log (S0-S7 PASS)
>   - evidence/go-test, evidence/validate, evidence/journey

---

## 7 Dimensions Evaluation

### A. Acceptance Criteria Coverage -- YELLOW

**Rule**: P1 = 100% PASS, P2 = no FAIL (SKIP with reason), P3 = SKIP allowed.

**P1 (54 items)**:

- FR-1 to FR-6 (adapter functionality, 25 AC): QA report judges all PASS based on unit tests with mocks. However, the acceptance criteria document specifies `[integration test]` as the verification method for the majority of these items (AC-1.1 through AC-5.5). The QA report acknowledges that all integration_test.go files are `t.Skip` stubs. The unit tests verify API correctness through mocks, but they do not satisfy the declared verification method. **Judgment**: Accepting QA's determination that these are PASS based on available evidence (unit test + compile-time assertions), with the caveat that the declared verification method (integration test) was not executed.
- FR-8 (integration tests, 5 AC: AC-8.1 through AC-8.5): **SKIP** -- all integration tests are `t.Skip` stubs (F-03). testcontainers-go not in go.mod (F-14). This is 5 P1 items that cannot be verified.
- FR-9 (security hardening, 8 AC): All PASS per QA report. F-01 (access-core still uses HS256) was raised as P0 in review-findings, but user-signoff records that F-01 was addressed via `WithSigningMethod` Option injection pattern (RS256 available, HS256 default). AC-9.2 technically passes ("Given RS256 token; When verify; Then PASS" -- the runtime/auth layer does verify RS256), though the default signing behavior remains HS256 until explicitly configured. tech-debt #9 records the remaining gap.
- FR-13.3 (go.mod dependency control): **PARTIAL** -- 4 of 5 declared dependencies present. testcontainers-go absent (F-14).
- FR-13.4 (SQL migrations): PASS.
- FR-14.1 (coverage >= 80%): **FAIL** -- postgres adapter at 46.6% (F-04). rabbitmq at 78.4% (below 80%).
- FR-14.2 (integration test tags): PASS (build tags present in stub files).
- FR-14.3 (Journey end-to-end): **SKIP** -- all journey tests output SKIP/FAIL per evidence/journey/result.txt.
- FR-14.4 (regression): PASS (60/60 packages, kernel >= 90%).
- FR-15 (bootstrap refactor, 2 P1): PASS.
- NFR-1.1 (layering), NFR-1.2 (interface compliance): PASS.
- NFR-2.1 (error spec): **PARTIAL** -- 9 fmt.Errorf violations found (F-07).
- NFR-4.1 (go vet): PASS.
- AC-REPO-1 to AC-REPO-3 (Cell PG repos): PASS per go test + compile.
- AC-ASSY-1 (core-bundle wiring): PASS per go test + compile.

**P1 Summary**: 54 items. 42 PASS, 5 SKIP (FR-8), 3 PARTIAL (FR-13.3, FR-14.1, NFR-2.1), 1 FAIL (FR-14.1 postgres 46.6%). P1 100% PASS requirement **not met**.

**P2 (14 items)**: QA report records all P2 as PASS. AC-10.1 through AC-10.8, AC-11.1 through AC-11.3 -- all verified by unit tests and code review. No FAIL or unjustified SKIP. **P2 requirement met.**

**P3 (11 items)**: All PASS or appropriately scoped. Docker Compose, Makefile, doc.go all present. AC-12.3 (integration test guide) has no standalone guide document but Makefile target exists. **P3 requirement met.**

**Dimension A: YELLOW** -- P1 has 5 SKIP (integration tests) + 1 FAIL (coverage) + 3 PARTIAL, violating the 100% PASS rule. P2 and P3 are clean.

---

### B. UI Compliance -- N/A

GoCell is a pure backend Go framework. No UI components exist. Frontend developer role declared OFF (SCOPE_IRRELEVANT) in role-roster.md. Phase 3 deliverables are entirely Go packages, Docker Compose configurations, and test suites.

**Dimension B: N/A**

---

### C. Error Path Coverage -- YELLOW

**Spec edge cases identified (from spec.md, kernel-constraints.md, product-acceptance-criteria.md)**:

| Edge case | Test coverage | Source |
|-----------|---------------|--------|
| Pool health check when PG unreachable | Unit test with mock (PASS) | AC-1.1 |
| TxManager RunInTx panic recovery | Unit test (PASS) | AC-1.2 |
| TxManager nested savepoint | Unit test (PASS) | AC-1.2 |
| OutboxWriter.Write outside transaction | Unit test fail-fast (PASS) | AC-1.4 |
| Relay FOR UPDATE SKIP LOCKED concurrent safety | **No test** -- F-06 identifies the query runs outside transaction | AC-1.5 |
| Redis lock contention (double acquire) | Unit test with mock (PASS) | AC-2.2 |
| Idempotency TTL expiry | Unit test with mock (PASS) | AC-2.3 |
| OIDC JWKS kid rotation | Unit test with httptest (PASS) | AC-3.3 |
| OIDC expired token rejection | Unit test (PASS) | AC-3.3 |
| ConsumerBase DLQ after 3 retries | Unit test with mock (PASS) | AC-5.4 |
| ConsumerBase unmarshal failure -> DLQ | Unit test (PASS) | AC-5.4 |
| ConsumerBase idempotency skip | Unit test with mock (PASS) | AC-5.4 |
| RealIP XFF spoofing from untrusted proxy | Unit test (PASS) | AC-9.3 |
| ServiceToken 5min window boundary | Unit test (PASS) | AC-9.4 |
| Refresh token reuse detection | Unit test (PASS) | AC-9.7 |
| Auth middleware public vs protected endpoints | Unit test (PASS) | AC-9.8 |
| DLQ route failure (DLQ exchange down) | **slog.Error only, message silently dropped** | user-signoff B.2 |
| Network disconnect + reconnect (RabbitMQ) | **No test** -- requires real infrastructure | AC-5.1 |
| Outbox write + business write atomicity failure | **No test** -- F-02 identifies non-atomic pattern | AC-8.2 |
| Configuration env prefix mismatch | **Known bug** -- S3 adapter reads `S3_*` while .env.example uses `GOCELL_S3_*` | user-signoff B.1 |

**Coverage ratio**: 16 edge cases covered by unit tests out of 20 identified = 80%. The 4 uncovered cases are all infrastructure-dependent scenarios that require real databases/message brokers.

**Dimension C: YELLOW** -- Unit test coverage of error paths is strong (80%), but the 4 uncovered paths include critical scenarios: outbox atomicity failure (F-02) and relay concurrent safety (F-06) directly impact the L2 consistency promise. The DLQ route failure path silently drops messages without alerting the developer.

---

### D. Documentation Link Completeness -- GREEN

| Documentation area | Status | Evidence |
|-------------------|--------|----------|
| adapter godoc (6 packages) | Complete | 6 doc.go files, exported types annotated |
| kernel doc.go (10 packages) | Complete | assembly, cell, governance, idempotency, journey, metadata, outbox, registry, scaffold, slice |
| runtime doc.go (9 packages) | Complete | auth, eventbus, health, router, logging, metrics, tracing, shutdown, worker |
| pkg doc.go (4 packages) | Complete | ctxkeys, errcode, httputil, uid |
| Total doc.go coverage | 29 packages | `go doc ./...` output comprehensive |
| kernel interface godoc enhancement | Complete | outbox.Writer.Write documents context-embedded tx convention; outbox.Entry.ID annotated as canonical idempotency identifier |
| adapter error codes godoc | Complete | 34 error codes across 6 adapters, each with godoc comment |
| .env.example | Present | All adapter connection parameters with defaults |
| Docker Compose | Present | docker-compose.yml with healthchecks |
| Makefile | Present | test-integration target |
| Reference annotations | Present | doc.go files note ref sources (Watermill, coreos/go-oidc) with adopt/deviate rationale |

**Gaps**:
- No standalone integration test guide document (FR-12.3 AC-12.3). Makefile serves as functional equivalent but lacks step-by-step explanation for new developers. [P3 item, acceptable.]
- No Example_* test functions in any adapter package. [DX improvement, not a doc completeness issue.]

**Dimension D: GREEN** -- 29 doc.go files cover all public packages. Error codes have godoc. Kernel interfaces have enhanced documentation. .env.example and Docker Compose present. The gaps are P3 and DX items, not structural documentation failures.

---

### E. Feature Completeness -- YELLOW

**FR-1 through FR-15 implementation status**:

| FR | Description | Implementation | Verification |
|----|-------------|----------------|--------------|
| FR-1 | PostgreSQL adapter | Code complete (Pool, TxManager, Migrator, OutboxWriter, OutboxRelay, RowScanner) | Unit tests PASS; integration test SKIP |
| FR-2 | Redis adapter | Code complete (Client, DistLock, IdempotencyChecker, Cache) | Unit tests PASS; integration test SKIP |
| FR-3 | OIDC adapter | Code complete (Provider, TokenExchange, JWKS Verifier, UserInfo) | Unit tests PASS |
| FR-4 | S3 adapter | Code complete (Client, Upload/Download/Delete, PresignedURL) | Unit tests PASS; integration test SKIP |
| FR-5 | RabbitMQ adapter | Code complete (Connection, Publisher, Subscriber, ConsumerBase, DLQ) | Unit tests PASS; integration test SKIP |
| FR-6 | WebSocket adapter | Code complete (Hub, UpgradeHandler, Heartbeat) | Unit tests PASS |
| FR-7 | Docker Compose | Complete (docker-compose.yml + .env.example + Makefile) | Config validation PASS |
| FR-8 | Testcontainers integration | **Not implemented** -- all files are t.Skip stubs | SKIP |
| FR-9 | Security hardening | 7/8 complete. SEC-04 RS256 available as Option but default remains HS256 | Unit tests PASS |
| FR-10 | Tech-debt payoff | ~65/74 RESOLVED | Unit tests + code review PASS |
| FR-11 | Product fixes | Complete (time format validation, PATCH semantics, Retry-After) | Unit tests PASS |
| FR-12 | Documentation | 29 doc.go + error code annotations complete | Code review PASS |
| FR-13 | DevOps | Docker Compose + Makefile + migrations complete. go.mod missing testcontainers-go | Partial |
| FR-14 | Testing | Unit tests PASS. Integration tests SKIP. postgres coverage 46.6% | Partial |
| FR-15 | Bootstrap refactor | Complete (WithPublisher/WithSubscriber + WithEventBus backward compat) | Unit tests PASS |

**Completeness**: 12 of 15 FRs fully implemented. FR-8 not implemented. FR-13 and FR-14 partial. 90/90 tasks marked complete per user-signoff, but "complete" for FR-8 tasks means stubs are in place, not functional tests.

**Dimension E: YELLOW** -- Code structure for all 15 FRs is in place (90/90 tasks). However, FR-8 (testcontainers integration tests) is a P1 feature that exists only as stubs. The gap between "stub exists" and "test runs" is the core issue.

---

### F. Success Criteria Achievement -- YELLOW

| # | Criterion | Status | Evidence |
|---|-----------|--------|----------|
| S1 | 6 adapter integration tests all PASS | **NOT_VERIFIED** | All integration_test.go are t.Skip. Unit tests PASS but do not satisfy S1's requirement of `go test ./adapters/... -tags=integration` PASS |
| S2 | Outbox full-chain end-to-end | **NOT_VERIFIED** | TestIntegration_OutboxFullChain = t.Skip. F-02 identified that business write + outbox write are not atomic (fixed in b8d7662 per user-signoff). Code-level binding present but no runtime verification |
| S3 | Phase 2 Journey real verification | **NOT_VERIFIED** | evidence/journey/result.txt shows all SKIP/FAIL. Journey test stubs exist in tests/integration/journey_test.go but not executable |
| S4 | adapters/ coverage >= 80% | **PARTIAL** | postgres 46.6% FAIL; redis 80.8% PASS; rabbitmq 78.4% marginal; oidc/s3/websocket data not provided in evidence |
| S5 | Zero layering violations | **PASS** | go build + go vet + grep import all compliant. Verified in review-findings compliance matrix |
| S6 | Security tech-debt cleared | **PARTIAL** | 7/8 items resolved. SEC-04 RS256 migration available as Option but default HS256. tech-debt #9 records deferral |
| S7 | tech-debt >= 60/74 RESOLVED | **PASS** | tech-debt.md confirms ~65 RESOLVED, exceeding 60 threshold. 12 new deferred items logged |
| S8 | Docker Compose 30s healthy | **LIKELY_PASS** | docker-compose.yml + healthcheck present. F-11 notes missing start_period risk for RabbitMQ cold start. Not blocking in practice |
| S9 | External dependencies controlled | **PARTIAL** | 4/5 declared deps in go.mod. testcontainers-go absent (F-14) |
| S10 | kernel/ zero regression | **PASS** | kernel coverage 93-100%. 60/60 go test packages PASS. go vet 0 warnings |
| S11 | RabbitMQ DLQ observable | **PASS** | consumer_base.go implements DLQ with slog.Error containing event_id, topic, error, retry_count |
| S12 | adapter godoc complete | **PASS** | 6 adapter doc.go + exported type annotations confirmed |

**Summary**: 5 PASS, 3 NOT_VERIFIED, 3 PARTIAL, 1 LIKELY_PASS. The 3 NOT_VERIFIED criteria (S1/S2/S3) are the core value propositions of Phase 3 -- proving that adapter integration works with real infrastructure.

**Dimension F: YELLOW** -- S5/S7/S10/S11/S12 clearly PASS. S8 likely PASS. S1/S2/S3 cannot be verified due to integration test stubs. S4/S6/S9 partial. No success criteria is definitively FAIL (code is in place), but 3 core criteria lack verification evidence.

---

### G. Product Tech Debt -- YELLOW

**[PRODUCT] tagged items in tech-debt.md**:

| # | Tag | Issue | Inherited | Impact |
|---|-----|-------|-----------|--------|
| 10 | [PRODUCT] | Phase 2 #54 TOCTOU race condition in session refresh | Yes (Phase 2 DEFERRED) | Users could exploit concurrent refresh to obtain duplicate tokens. Requires Redis distributed lock + persistent session stability |
| 11 | [PRODUCT] | Phase 2 #56-59 domain model refactoring not executed | Yes (Phase 2 DEFERRED) | Service interfaces return concrete types instead of domain interfaces; limits testability and adapter swappability |
| 12 | [PRODUCT] | Phase 2 #62 configpublish.Rollback version validation missing | Yes (Phase 2 DEFERRED) | Rollback could target invalid version without persistent version management |

All 3 [PRODUCT] items are inherited from Phase 2 DEFERRED, with clear deferral reasons and planned Phase 4 resolution. No new product debt was introduced in Phase 3.

**[TECH] items with product impact**:

| # | Issue | Product impact |
|---|-------|----------------|
| 1 | Integration tests all t.Skip | Framework evaluators (P4 persona) cannot verify core promises |
| 6 | outboxWriter nil guard silent fallback | L2 consistency silently degrades in misconfigured production deployments |
| 9 | RS256 default still HS256 | Developers who don't explicitly configure RS256 ship with weaker signing |

**Dimension G: YELLOW** -- 3 [PRODUCT] items, all inherited Phase 2 deferrals with documented rationale. No new product debt introduced. However, tech-debt #1 (integration test stubs) and #6 (silent fallback) have significant product-facing impact.

---

## Dimension Summary

| Dimension | Rating | Rationale |
|-----------|--------|-----------|
| A. Acceptance Criteria Coverage | YELLOW | P1: 42/54 PASS, 5 SKIP (FR-8), 1 FAIL (coverage), 3 PARTIAL. P2/P3 clean |
| B. UI Compliance | N/A | Pure backend framework, no UI |
| C. Error Path Coverage | YELLOW | 80% edge cases covered by unit tests. Critical gaps in outbox atomicity and relay concurrency |
| D. Documentation Completeness | GREEN | 29 doc.go, error code godoc, .env.example, Docker Compose all present |
| E. Feature Completeness | YELLOW | 12/15 FR fully implemented. FR-8 stub-only. FR-13/FR-14 partial |
| F. Success Criteria Achievement | YELLOW | 5 PASS, 3 NOT_VERIFIED (S1/S2/S3), 3 PARTIAL, 1 LIKELY_PASS |
| G. Product Tech Debt | YELLOW | 3 [PRODUCT] inherited from Phase 2, all with deferral rationale. No new product debt |

**Red dimensions: 0. Yellow dimensions: 5. Green dimensions: 1. N/A: 1.**

---

## Product Acceptance Determination

**Checklist**:

- [x] Product context defined (4 persona + 12 success criteria)
- [x] Acceptance criteria graded (P1/P2/P3)
- [ ] P1 acceptance criteria = 100% PASS -- **NOT MET** (5 SKIP + 1 FAIL + 3 PARTIAL)
- [x] P2 no FAIL (SKIP with reason) -- MET
- [ ] Product review report no RED dimensions -- **MET** (0 RED)
- [ ] User signoff not REJECT -- **MET** (CONDITIONAL)

**Determination: PRODUCT FAIL**

P1 100% PASS requirement not met. 5 P1 items (FR-8 integration tests) are SKIP, 1 P1 item (FR-14.1 postgres coverage) is FAIL, 3 P1 items are PARTIAL.

---

## Must-Fix Items (3 items maximum)

### MF-1: Implement at least postgres + rabbitmq + redis integration tests with testcontainers [P0]

**Affected AC**: AC-8.1, AC-8.2, AC-8.3, AC-8.4, AC-8.5 (5 P1 items)
**Affected Success Criteria**: S1, S2, S3
**Category**: `[Acceptance Criteria Missing]` -- verification evidence absent for core Phase 3 value proposition

**Current state**: All 8 integration test files contain only `t.Skip` stubs. testcontainers-go not in go.mod. The 3 core success criteria (S1 adapter integration, S2 outbox full chain, S3 journey verification) have zero runtime verification evidence.

**Required action**:
1. Add `testcontainers-go` to go.mod
2. Implement non-skip tests in at least `adapters/postgres/integration_test.go`, `adapters/rabbitmq/integration_test.go`, `adapters/redis/integration_test.go`
3. Implement `TestIntegration_OutboxFullChain` covering write + relay + publish + consume + idempotency

**Impact**: Moves AC-8.1 through AC-8.5 from SKIP to PASS. Moves S1/S2/S3 from NOT_VERIFIED to PASS. Resolves tech-debt #1 and #7.

### MF-2: Raise postgres adapter coverage to >= 80% [P1]

**Affected AC**: AC-14.1 (P1)
**Affected Success Criteria**: S4
**Category**: `[Acceptance Criteria Missing]` -- quantitative threshold not met

**Current state**: postgres adapter coverage is 46.6%, 33.4 percentage points below the 80% requirement. Pool core lifecycle (NewPool real path, Health, Close, Stats), TxManager top-level transaction Commit/Rollback paths, and Migrator Up/Down/Status are uncovered. rabbitmq at 78.4% is marginal.

**Required action**: Add mock-based unit tests for Pool lifecycle methods, TxManager commit/rollback/panic paths, and Migrator state transitions without requiring real PostgreSQL. These can use interface abstractions already present in the codebase.

**Impact**: Moves AC-14.1 from FAIL to PASS for postgres. Moves S4 from PARTIAL to closer-to-PASS.

### MF-3: Fix S3 adapter ConfigFromEnv environment variable prefix to GOCELL_S3_* [P1]

**Affected AC**: AC-7.3 (P3), developer experience
**Affected Success Criteria**: S8 (indirect)
**Category**: `[Developer Experience]` -- configuration trap causes first-time integration failure

**Current state**: .env.example uses `GOCELL_S3_*` prefix. S3 adapter's `ConfigFromEnv()` reads `S3_ENDPOINT`, `S3_REGION` etc. without the `GOCELL_` prefix. This was identified in user-signoff B.1 and review-findings F-05 (which fixed postgres but missed S3). A developer following .env.example will have S3 adapter silently receive empty configuration.

**Required action**: Align S3 adapter `ConfigFromEnv()` to read `GOCELL_S3_ENDPOINT`, `GOCELL_S3_REGION`, `GOCELL_S3_BUCKET`, `GOCELL_S3_ACCESS_KEY`, `GOCELL_S3_SECRET_KEY`. Update corresponding tests.

**Impact**: Eliminates the configuration mismatch that causes silent failure on first developer integration. Consistent with postgres adapter's `GOCELL_PG_*` pattern established in F-05 fix.

---

## Additional Recommendations (not blocking)

| Priority | Item | Expected impact |
|----------|------|----------------|
| P2 | Add `slog.Warn` when outboxWriter is nil and fallback to direct publish activates (tech-debt #6) | Makes L2 consistency degradation observable in logs |
| P2 | Add `// Deprecated` annotation to `WithEventBus` (tech-debt #8, F-15) | Guides developers toward WithPublisher/WithSubscriber |
| P2 | Add Example_* test functions for postgres/redis/rabbitmq | Improves `go doc` output for framework evaluators (P4 persona) |
| P3 | Add `start_period: 15s` to rabbitmq and minio healthchecks in docker-compose.yml (F-11) | Reduces false-negative health check failures on slow CI |

---

## Conditional Approval Path

Completing MF-1 + MF-2 + MF-3 would:
- Move 5 P1 SKIP items to PASS (FR-8)
- Move 1 P1 FAIL item to PASS (FR-14.1)
- Move S1/S2/S3 from NOT_VERIFIED to PASS
- Resolve the configuration trap for S3 adapter
- Expected dimension changes: A YELLOW->GREEN, E YELLOW->GREEN, F YELLOW->GREEN

This would yield 0 RED, 2 YELLOW (C error path gaps remain infrastructure-dependent; G inherited product debt remains), allowing re-evaluation for PRODUCT PASS.

---

*Report generated by Product Manager Agent based on cross-referencing product-context.md, product-acceptance-criteria.md, qa-report.md, review-findings.md, tech-debt.md, user-signoff.md, gate-audit.log, and evidence artifacts.*
