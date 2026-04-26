# ADR: Architectural Panic Whitelist (ERROR-FIRST-API-01)

> Date: 2026-04-27
> Status: Accepted
> Context PR: PR-MODE-6 ERROR-FIRST-API (refactor/555-pr-mode-6-error-first-api)
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
`func init()`. The rule scans 13 enrolled files (the parent plan's PR-MODE-6
scope). Future PRs may extend the scope; the path forward is in §Roadmap.

## Decision

### 1. Convert all 28+ panics in scope to error-first APIs

The bulk of the work refactors registration/construction sites to return
error and provides `Must*` wrappers (composition-root convenience that
panic on error):

- `kernel/cell/auth_plan.go::NewAuthJWT` / `NewAuthJWTFromAssembly` /
  `NewAuthServiceToken` → `(plan, error)` + `MustNewAuth*` wrappers
- `kernel/wrapper/handler.go::HTTPHandler` → `(handler, error)` + `MustHTTPHandler`
- `kernel/wrapper/consumer.go::WrapConsumer` → `(consumer, error)` + `MustWrapConsumer`
- `runtime/eventrouter/router.go::AddContractHandler` → `error`
  (`kernel/cell.EventRouter` interface signature updated; cells'
  `RegisterSubscriptions` propagate the error)
- `runtime/auth/route.go::Mount` → `error` + `MustMount` wrapper
  (`cell.RouteGroup.Register` signature changed to `func(RouteMux) error`;
  bootstrap phase5 propagates)
- `adapters/postgres/refresh_store.go::NewRefreshStore` → `(*PGRefreshStore, error)`
  + `MustNewRefreshStore`
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

### 4. Hardcoded ADR-pinned whitelist (1 file)

The archtest holds **one** file-level whitelist entry. Every other panic
in the scoped files is either refactored, renamed, or in an `init()`
function (auto-exempt).

| # | File | Justification |
|---|---|---|
| 1 | `kernel/wrapper/lifecycle.go` | `recoverAndFinishWithRedactor` is the middle of a `defer recover()` chain that re-panics so the outer `runtime/http/middleware.Recovery` middleware can record + serialize the panic. Refactoring it to return error would dismantle the entire recover propagation idiom and force every wrapped consumer to pre-route panics through a synthetic error path before Go's runtime gets the chance. |

### 5. Auto-exempt categories

- **`Must*` prefix**: Go community convention for the panic-on-misuse
  twin of an error-returning constructor. Examples in this PR:
  `MustNewAuthJWT`, `MustHTTPHandler`, `MustWrapConsumer`,
  `MustMount`, `MustNewRefreshStore`, `MustNewEntryID`,
  `MustMarshalDirectEnvelope` (renamed from non-Must form), plus
  pre-existing `MustValidateLabels`, `MustRegister`, `MustNewTestKeyProvider`,
  `MustCookieSession`, etc.
- **`func init()`**: Init cannot return error; package-level invariant
  violations are by definition fatal. Example: `adapters/postgres/embed.go`
  embedded migrations subdir loading.

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

### Why minimum (1) whitelist?

Each whitelist entry is a future regression risk: a new contributor sees
"there are some panics here, panicking must be OK" and adds another. The
review loop catches new architectural panic claims via the ADR, not via
the archtest list. By keeping the whitelist to **just** the
defer-recover-re-panic case (which is structurally not refactorable), we
maximize the "panic in error-less function = bug" signal.

The previous design (5 entries) was rejected: outbox crypto/rand failures
and envelope marshal invariants were absorbable as either error
propagation (entry_id, idempotency) or `Must*` rename (envelope), so they
moved out of the whitelist. The HTTP router state machine (`runtime/http/router/router.go`,
5 panics across `FinalizeAuth`/`Mount`/state transitions) is **out of
scope for the archtest**: that file is not in the enrolled list yet.
PR-MODE-6.1 (scope expansion) may add it later, alongside a router refactor
or its own whitelist entry.

### Whitelist mechanics

Hardcoded in `tools/archtest/error_first_test.go`:

```go
var errorFirstWhitelistedFiles = map[string]string{
    "kernel/wrapper/lifecycle.go": "recoverAndFinishWithRedactor re-panics from defer recover",
}
```

To add an entry: open a PR, update this ADR's §4 table with a new row,
update the map. Reviewer must reject any whitelist addition that's
absorbable as `Must*` rename or error propagation.

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

Future PR-MODE-6.1 candidates for archtest scope expansion (each costs
its own refactor + propagation pass):

- `kernel/persistence/tx.go::RunInTx` — single panic on nil fn, easy
  refactor to error.
- `kernel/observability/metrics/metrics.go` — already auto-exempt via
  `MustRegister` prefix, but worth confirming the pattern holds when new
  metrics are added.
- `runtime/distlock/locker.go::New` — 5 config-validation panics; refactor
  + propagation to `cmd/corebundle` distlock wiring.
- `runtime/auth/refresh/memstore/store.go::New` — 3 config-validation
  panics; analogue of `adapters/postgres/refresh_store.go`.
- `runtime/http/middleware/circuit_breaker.go` — Allower nil check + re-panic.
- `runtime/http/health/health.go::Register` — nil-checker / duplicate-name
  panics.
- `runtime/http/router/router.go` — state-machine post-conditions (5 sites);
  largest scope; either whitelist-as-architectural or major refactor of
  `FinalizeAuth`.
- `cells/accesscore/slices/sessionlogin/service.go::NewService` — 7 nil-check
  panics; the cells-layer first foothold for the rule.

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
