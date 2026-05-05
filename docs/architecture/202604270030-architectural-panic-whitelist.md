# ADR: Architectural Panic Whitelist (ERROR-FIRST-API-01)

> Date: 2026-04-27
> Status: Accepted
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

### 4. Hardcoded ADR-pinned whitelist (4 functions)

The archtest holds **four** function-level whitelist entries. Every other
production panic is either refactored into an error-returning API or made
explicit through a `Must*` wrapper.

| # | Function | Justification |
|---|---|---|
| 1 | `kernel/wrapper/lifecycle.go::recoverAndFinish` | Middle of a `defer recover()` chain that re-panics so the outer `runtime/http/middleware.Recovery` middleware can record + serialize the panic. Refactoring it to return error would dismantle the entire recover propagation idiom and force every wrapped consumer to pre-route panics through a synthetic error path before Go's runtime gets the chance. |
| 2 | `runtime/http/middleware/circuit_breaker.go::repanicAfterBreakerFailure` | Middle of a `defer recover()` chain that first reports the handler panic as a circuit-breaker failure, then re-panics so the outer `Recovery` middleware remains the single panic-to-HTTP and panic-to-tracing boundary. Swallowing the panic here would bypass `Recovery` and lose panic span recording in the normal router chain. |
| 3 | `adapters/postgres/tx_manager.go::repanicAfterTopLevelTxRollback` | Top-level transaction panic path must rollback the open pgx transaction before preserving the caller's original panic semantics. Returning an error would swallow programmer/runtime panics from the callback and change `RunInTx`'s transaction-safety contract. |
| 4 | `adapters/postgres/tx_manager.go::repanicAfterSavepointRollback` | Nested transaction panic path must rollback to the savepoint before preserving the caller's original panic semantics. Returning an error would swallow programmer/runtime panics from the callback and make nested and top-level transactions diverge. |

### 5. Auto-exempt categories

- **`Must*` prefix**: Go community convention for the panic-on-misuse
  twin of an error-returning constructor. Examples in this PR:
  `MustNewAuthJWT`, `MustHTTPHandler`, `MustWrapConsumer`,
  `MustMount`, `MustNewEntryID`,
  `MustMarshalDirectEnvelope` (renamed from non-Must form), plus
  pre-existing `MustValidateLabels`, `MustRegister`, `MustNewTestKeyProvider`,
  `MustCookieSession`, etc.
  (Note: `MustNewRefreshStore` was removed in B2-A-11 / PR-V1-PG-REFRESH-HARDEN-AND-IDLE-GRACE.)

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
boilerplate.

### Why minimum (4) whitelist?

Each whitelist entry is a future regression risk: a new contributor sees
"there are some panics here, panicking must be OK" and adds another. The
review loop catches new architectural panic claims via the ADR, not via
the archtest list. By keeping the whitelist to **only** re-panic propagation
helpers that preserve an already in-flight panic after mandatory cleanup or
recording, we maximize the "panic outside Must* = bug" signal.

The previous design (5 entries) was rejected: outbox crypto/rand failures
and envelope marshal invariants were absorbable as either error
propagation (entry_id, idempotency) or `Must*` rename (envelope), so they
moved out of the whitelist. PR-MODE-6.1 also rejected adding the HTTP router
as a whitelist entry: router construction, auth metadata declaration,
and unfinalized auth state now surface through errors or fail-closed HTTP
responses, while `MustNew` remains the explicit panic wrapper for static
wiring.

### Whitelist mechanics

Hardcoded in `tools/archtest/panic_registered_test.go`:

```go
var architecturalPanicWhitelist = map[string]string{
    "kernel/wrapper/lifecycle.go::recoverAndFinish": "re-panics from defer recover so outer Recovery middleware can serialize the panic",
    "runtime/http/middleware/circuit_breaker.go::repanicAfterBreakerFailure": "re-panics from defer recover after reporting circuit-breaker failure",
    "adapters/postgres/tx_manager.go::repanicAfterTopLevelTxRollback": "re-panics after top-level transaction rollback so caller panic semantics are preserved",
    "adapters/postgres/tx_manager.go::repanicAfterSavepointRollback": "re-panics after savepoint rollback so nested transaction panic semantics are preserved",
}
```

To add an entry: open a PR, update this ADR's §4 table with a new row,
update the map. `PANIC-REGISTERED-01` parses the ADR table and fails unless
the table and map are exactly equal, every map entry is used by a real panic,
and the whitelist contains exactly the four approved permanent entries.
Reviewer must reject any whitelist addition that's absorbable as `Must*`
rename or error propagation.

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

- `kernel/observability/metrics/metrics.go` — already auto-exempt via
  `MustRegister` prefix, but worth confirming the pattern holds when new
  metrics are added.

Order suggestion: each PR adds 1-3 files to `errorFirstEnforcedFiles`
in the archtest, refactors the corresponding panics, and updates this
ADR's §Roadmap to mark the file done.

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
