# ADR: Architectural Panic Whitelist (ERROR-FIRST-API-01)

> Date: 2026-04-27
> Status: Accepted (revised 2026-05-11)
> Context PR: PR-MODE-6 ERROR-FIRST-API (refactor/555-pr-mode-6-error-first-api)
> Scope update: PR-MODE-6.1 Nil / Error-First full cleanup
> Tag: ERROR-FIRST-API-01

## Context

GoCell's six-role retrospective (`bak/20260426-layered-six-role-review/`)
identified **panic on misuse** as one of eight recurring patterns: 28+
constructor- and registration-time panics scattered across `kernel/wrapper/`,
`kernel/cell/auth_plan.go`, `kernel/outbox/`, `kernel/idempotency/`,
`runtime/auth/route.go`, `runtime/eventrouter/`, and `adapters/postgres/`.
Cells that consult runtime configuration (planned future state) cannot
recover from misconfiguration if construction panics; tests cannot exercise
the validation surface without `recover()`-based scaffolding; operators see
crash dumps for what should be structured 4xx/5xx responses.

PR-MODE-6 introduces the `tools/archtest/error_first_test.go::TestErrorFirstAPI01`
rule: **error-less function declarations in the enrolled file scope MUST NOT
contain `panic(...)` calls**. Auto-exemptions: `Must*`-prefixed names and
`func init()`. PR-MODE-6.1 expands the rule to 23 enrolled files, including
the remaining nil/error-first roadmap files and `runtime/http/router/router.go`.

## Decision

### 1. Convert all 28+ panics in scope to error-first APIs

The bulk of the work refactors registration/construction sites to return
error and provides `Must*` wrappers (composition-root convenience that
panic on error):

- `kernel/cell/auth_plan.go::NewAuthJWT` / `NewAuthJWTFromAssembly` /
  `NewAuthServiceToken` → `(plan, error)` + `MustNewAuth*` wrappers
- `kernel/wrapper/handler.go::HTTPHandler` → `(handler, error)` + `MustHTTPHandler`
- `kernel/wrapper/consumer.go::WrapConsumer` → `(consumer, error)` + `MustWrapConsumer`
- `kernel/wrapper/subscriber.go::WrapSubscriber` → `(handler, error)` (sole API after N8 — the `MustWrapSubscriber` panic helper was removed; manual callers handle the error explicitly via `runtime/eventrouter.NewContractTracingSubscriber.Subscribe`)
- `runtime/eventrouter/router.go::AddContractHandler` → `error`
  (`kernel/cell.EventRouter` interface signature updated; cells'
  `RegisterSubscriptions` propagate the error)
- `runtime/auth/route.go::Mount` → `error` + `MustMount` wrapper
  (`cell.RouteGroup.Register` signature changed to `func(RouteMux) error`;
  bootstrap phase5 propagates)
- `adapters/postgres/refresh_store.go::NewRefreshStore` → `(*PGRefreshStore, error)`
  (B2-A-11: `MustNewRefreshStore` removed; error-first only)
- `kernel/outbox/entry_id.go::NewEntryID` → `(string, error)` + `MustNewEntryID`
- `kernel/idempotency/inmem.go::newToken` → `(string, error)` (private;
  propagated to `Claim` which already returns error)

### 2. Rename `MarshalDirectEnvelope` → `MustMarshalDirectEnvelope`

Internal envelope construction with caller-controlled invariants. Marshalling
failure indicates a writer-side programmer error; the rename makes the
panic semantics explicit (auto-exempted by the `Must*` rule).

### 3. Map worker nil-exit to `ErrWorkerExitedEarly` sentinel

`runtime/worker.WorkerGroup.Start` previously produced silent
`firstErr=nil` when a member worker terminated early without context
cancellation. Modelled as `kworker.ErrWorkerExitedEarly` so callers can
`errors.Is`-detect the abnormal signal — not strictly part of the panic
refactor, but bundled for the same "fail loudly, propagate errors" theme.

### 4. Approved panic reason catalog

All production panic call sites use `panic(panicregister.Approved(<reason>, <value>))`.
The `<reason>` literal documents the architectural category; it is not cross-checked
against this catalog by archtest (`PANIC-REGISTERED-01` only enforces the call shape).
This section serves as human-readable governance reference.

#### 4.1 C-class framework re-throw (preserve panic semantics after cleanup)

These re-panic the original recovered value after mandatory cleanup. Refactoring to
return error would break the panic-propagation contract observed by outer recover.

| Reason | Call site | Justification |
|---|---|---|
| `lifecycle-recover-rethrow-to-recovery-middleware` | `kernel/wrapper/lifecycle.go::recoverAndFinish` | Middle of defer recover() chain; outer `runtime/http/middleware.Recovery` must serialize the panic. |
| `circuit-breaker-rethrow-after-failure-report` | `runtime/http/middleware/circuit_breaker.go::repanicAfterBreakerFailure` | Reports handler panic as circuit-breaker failure then re-panics so Recovery remains single panic-to-HTTP boundary. |
| `pg-tx-top-level-rollback-rethrow` | `adapters/postgres/tx_manager.go::repanicAfterTopLevelTxRollback` | Rolls back open pgx transaction before preserving caller panic; returning error would change RunInTx contract. |
| `pg-tx-savepoint-rollback-rethrow` | `adapters/postgres/tx_manager.go::repanicAfterSavepointRollback` | Rolls back to savepoint before preserving caller panic; ensures nested/top-level semantics align. |

#### 4.2 Programmer-error sites (state-machine / param validation)

These wrap `errcode.Assertion(...)` for structured 500 conversion by Recovery middleware.
reason-literal is `<module>-<invariant>` kebab-case. Catalog is illustrative; new sites
add their own reason at the call site; review verifies the reason is descriptive but
there is no automatic cross-check.

Examples by module:
- `cells/accesscore/slices/session{login,logout,refresh}/service.go` — `sessionlogin-invariant` / `sessionlogout-invariant` / `sessionrefresh-invariant`
- `kernel/cell/auth_plan.go` — `auth-plan-jwt-init`, `auth-plan-jwt-assembly-init`, `auth-plan-service-token-init`
- `kernel/cell/base.go` — `cell-base-init`
- `kernel/cell/registry.go` — `registry-health-name`, `registry-lifecycle-hook-name`, `registry-reload-prefix-empty`, `registry-reload-fn-nil`, `registry-post-snapshot-mutate`
- `kernel/clock/guard.go` — `clock-nil`, `clock-typed-nil`, `clock-non-positive-interval`
- `kernel/outbox/entry_id.go` — `outbox-entry-id-init`
- `kernel/wrapper/{consumer,handler}.go` — `wrapper-consumer-init`, `wrapper-handler-init`
- `pkg/errcode/errcode.go` — `errcode-redact-attr-self`, `errcode-redact-message-self`
- `runtime/audit/ledger/mem_store.go` — `audit-mem-tamper-hash-out-of-range`, `audit-mem-tamper-prev-hash-out-of-range`
- `runtime/auth/{keys,principal,provider,route}.go` — `auth-test-rsa-keypair`, `auth-test-keyset`, `auth-principal-context-missing`, `auth-test-hmac-keyring`, `auth-route-mount`
- `runtime/distlock/locker.go` — `distlock-init`
- `runtime/http/{health,middleware/cookie_session,middleware/circuit_breaker,router}` — `health-checker-register`, `cookie-session-init`, `circuit-breaker-init`, `router-init`
- `cells/auditcore/internal/appender/spec.go` — `appender-actor-mode-zero`, `appender-actor-mode-unknown`
- `runtime/websocket/hub.go` — `websocket-hub-shutdown-timeout-negative`, `websocket-hub-close-limit-negative`
- `kernel/observability/metrics/metrics.go` — `metrics-validate-labels-mismatch`
- `adapters/websocket/handler.go` — `websocket-upgrade-handler-init`
- `runtime/audit/ledger/protocol.go` — `audit-ledger-protocol-init`
- `runtime/auth/session/protocol.go` — `auth-session-protocol-init`
- `kernel/metadata/identifier.go` — `metadata-go-identifier-codegen-invalid`
- `generated/contracts/http/**/*_gen.go` — codegen emits `<contract-id>-policy-nil`, `<contract-id>-schema-compile-failed`

### 5. No prefix-based exemption

There is no `Must*`-prefix exemption. Every production panic — including those in
`Must*` constructors — must go through `panicregister.Approved`. The Must convention
remains a useful naming pattern for composition-root convenience constructors, but it
is not recognized by archtest. `Must*` wrappers that previously received an implicit
free pass now carry `panic(panicregister.Approved("<reason>", err))` at their panic
site, making the architectural intent explicit and machine-verifiable.

### 6. PR-MODE-6.1 scope expansion

PR-MODE-6.1 closes the remaining nil/error-first candidates without adding a
router whitelist entry. It adds one narrow circuit-breaker re-panic whitelist
after review found that swallowing handler panics before `Recovery` regressed
tracing semantics.

- `.golangci.yml` enables `nilerr`, `nilnesserr`, and `nilnil` with
  `nilnil.only-two: false`, so multi-return success sentinels like
  `nil, ..., nil` are rejected.
- `tools/archtest/error_first_test.go` adds `ERROR-FIRST-TYPED-NIL-01`:
  enrolled error-first constructors with interface dependencies must validate
  those dependencies through `validation.IsNilInterface(param)`, not only
  `param == nil`.
- `runtime/distlock/locker.go::New`,
  `runtime/auth/refresh/memstore/store.go::New`, and the accesscore
  session login/refresh/logout `NewService` constructors now return
  `(..., error)` with `Must*` wrappers for static wiring and test setup.
- `runtime/http/middleware/circuit_breaker.go::CircuitBreaker`,
  `runtime/http/health/health.go::RegisterChecker`, and
  `runtime/http/router/router.go::New` / `NewForListener` return errors
  for nil, typed-nil, duplicate, and invalid configuration paths.
- `runtime/http/router/router.go` is now in the archtest scope. Auth metadata
  declaration returns errors, `auth.Mount` propagates them, and a router with
  declared auth metadata but missing `FinalizeAuth` fails closed with HTTP 500
  instead of panicking. No router whitelist entry was added.
- `command.ActiveScanner.GetCommand` not-found handling,
  `adminprovision.Ensure`, initial-admin cleanup (including exact custom
  credential-path sweep), governance helpers,
  archtest AST helpers, and `tools/nogo/unconditionalskip` were adjusted so
  nil no longer encodes success or silently hides parse/walk failures.

## Rationale

### Why error-first over Must-renaming for 28+ sites?

For cell composition roots, fail-fast at startup IS the right
semantic — cells statically declare their routes and auth plans, and a
spec literal that fails validation is a build-time bug. So why introduce
an error-returning surface at all?

Three reasons:

1. **Future runtime-config sites**: Cells that resolve their auth plan
   or contract specs from runtime config (Vault secrets, dynamic feature
   flags, multi-tenant route registries) need to refuse misconfiguration
   gracefully — return 503 to a controlplane endpoint, surface to
   `/readyz`, etc. Panic forces a process restart that operators cannot
   intercept.

2. **Test ergonomics**: Negative-path tests no longer need `recover()`
   scaffolding. `assert.Error(t, err)` reads naturally and groups with
   the rest of the test suite's error assertions.

3. **Layered visibility**: bootstrap phase5 already returns error to
   `Bootstrap.Run`; surfacing route registration / consumer subscription
   failures through that channel keeps the operator's view of "what
   went wrong at startup" coherent — log + structured error, not a
   stack trace.

The `Must*` wrappers preserve the static-composition ergonomics: cells
that construct a single static `cell.NewAuthJWTFromAssembly(asm)` continue
to write `cell.MustNewAuthJWTFromAssembly(asm)` with no error-handling
boilerplate. Under the Wave 2 single-funnel design, each `Must*` wrapper's
panic site now calls `panic(panicregister.Approved("<reason>", err))` rather
than bare `panic(err)`.

### Why minimum (4) whitelist?

Each whitelist entry is a future regression risk: a new contributor sees
"there are some panics here, panicking must be OK" and adds another. The
review loop catches new architectural panic claims via the ADR, not via
the archtest list. By keeping the whitelist to **only** re-panic propagation
helpers that preserve an already in-flight panic after mandatory cleanup or
recording, we maximize the "panic outside Approved = bug" signal.

The previous design (5 entries) was rejected: outbox crypto/rand failures
and envelope marshal invariants were absorbable as either error
propagation (entry_id, idempotency) or `Must*` rename (envelope), so they
moved out of the whitelist. PR-MODE-6.1 also rejected adding the HTTP router
as a whitelist entry: router construction, auth metadata declaration,
and unfinalized auth state now surface through errors or fail-closed HTTP
responses, while `MustNew` remains the explicit panic wrapper for static
wiring.

### Mechanics

`pkg/panicregister/panicregister.go` exports a single function:

```go
func Approved(reason string, value any) any { return value }
```

Archtest `PANIC-REGISTERED-01` (`tools/archtest/panic_invariants_test.go`) enforces:

- Every production `panic(arg)` call has `arg = *ast.CallExpr` whose Fun resolves (via `*types.Info`) to `pkg/panicregister.Approved`
- The first argument is `*ast.BasicLit` of kind STRING (no fmt.Sprintf / concat / variable)

Both checks are pure AST + types.Info — no comment scanning, no name convention.
The previous `architecturalPanicWhitelist` Go map and `AllowMust` prefix exemption
have been removed; this ADR is no longer cross-referenced by archtest at runtime.

### Trade-offs

- **API surface growth**: ~12 new `Must*` wrappers added; doubled the
  registration-API count. Mitigated by uniform naming convention and
  one-line documentation referencing the canonical error-first variant.
- **Caller refactor cost**: ~80 production + test call sites updated.
  Bulk-handled via `perl -i -pe` substitution; reviewer should focus on
  the few sites that needed manual closure rewrites
  (`cell.RouteGroup.Register` signature change, `cells/{accesscore,auditcore,configcore}`
  `RegisterSubscriptions` error propagation).
- **Test split**: panic-asserting tests split into
  `Test*_ReturnsError*` (on the canonical error path) and
  `TestMust*_Panics*` (on the wrapper). Adds test count but improves
  intent clarity.

## Roadmap

Completed in PR-MODE-6.1:

- `kernel/persistence/tx.go::RunInTx` — nil callback now returns an explicit
  error.
- `runtime/distlock/locker.go::New` — config validation now returns error
  with `MustNew` for static wiring.
- `runtime/auth/refresh/memstore/store.go::New` — policy/clock/random source
  validation now returns error with `MustNew`.
- `runtime/http/middleware/circuit_breaker.go` — nil and typed-nil Allower
  validation now returns error with `MustCircuitBreaker`.
- `runtime/http/health/health.go::RegisterChecker` — nil checker and
  duplicate-name validation now return error with `MustRegisterChecker`.
- `runtime/http/router/router.go` — added to archtest scope and refactored
  without adding a router whitelist entry.
- `cells/accesscore/slices/sessionlogin/service.go::NewService`,
  `sessionrefresh/service.go::NewService`, and
  `sessionlogout/service.go::NewService` — nil and typed-nil dependency
  checks now return errors with `MustNewService` wrappers, enforced by
  `ERROR-FIRST-TYPED-NIL-01`.

Remaining watchlist:

- `kernel/observability/metrics/metrics.go` — previously auto-exempt via
  `MustRegister` prefix; under the Wave 2 design the panic site now uses
  `panicregister.Approved("metrics-validate-labels-mismatch", ...)`.

## Revision history

2026-05-11: rewritten for Wave 2 single-funnel design (PR #467; commit message
references this ADR). §4 replaced the 4-row Function-name whitelist table with a
categorized reason-literal catalog. §5 (Auto-exempt categories / Must* prefix) deleted
and replaced with "No prefix-based exemption". §"Whitelist mechanics" deleted and
replaced with the Mechanics section describing `pkg/panicregister.Approved` and the
updated `PANIC-REGISTERED-01` enforcement. The previous `architecturalPanicWhitelist`
Go map and `AllowMust` flag are removed from archtest; this ADR is no longer
cross-referenced at archtest runtime.

## References

- Parent plan: `docs/plans/202604270020-1-2-ci-3-claude-ship-reactive-bachman.md`
  §阶段 1 PR-MODE-6 ERROR-FIRST-API.
- Sub-plan / commit log: `~/.claude/plans/docs-plans-202604270020-1-2-ci-3-claude-scalable-stroustrup.md`.
- Six-role review report: `bak/20260426-layered-six-role-review/`.
- Go std lib precedent: `crypto/uuid` panics on `crypto/rand.Read` failure
  ([documented behaviour](https://pkg.go.dev/crypto/rand#Read)) — informs
  the `MustNewEntryID` decision.
- `Must*` convention: `regexp.MustCompile`, `template.Must`,
  `kubernetes/client-go MustGetSelfLink`.
- Go `nilness` analyzer; `golangci-lint` `nilnil` settings.
- Kratos middleware constructors and Kubernetes client-go resourcelock
  constructor error patterns.
- Charter §4 Wave 2 panic 单源 typed marker: `docs/plans/202605101300-ai-first-governance-charter.md`.
- Single-funnel implementation: `pkg/panicregister/panicregister.go`.
