# ADR: kernel/clock injection (D6 PROD-CLOCK-INJECTION-01)

> Status: Accepted
> Date: 2026-05-02
> ref: `docs/plans/202605011500-029-master-roadmap.md` Track D #D6

## Context

Before this change, GoCell production code accessed wall-clock time
directly via `time.Now()`, `time.Since(...)`, `time.Until(...)`, and
`time.NewTimer(...)`. The result was a flaky test surface: every
TTL / lease / expiry / latency measurement either pinned tests to real
sleeps or required ad-hoc mocking.

Three local `Clock` interfaces had grown up independently to cover
narrow slices of this problem:

| Owner                                 | Surface                                | Test fake                                  |
|---------------------------------------|----------------------------------------|--------------------------------------------|
| `runtime/distlock/clock.go`           | `Now`, `Since`, `NewTimerAt` + `Timer` | `runtime/distlock/locktest.FakeClock`      |
| `runtime/auth/refresh/types.go`       | `Now` only                             | (none — `time.Now` baked into tests)       |
| `cells/accesscore/initialadmin/clock.go` | `Now` only                          | (none — `time.Now` baked into tests)       |

These three were structurally compatible (any `Now()-only` value
satisfies the wider `distlock.Clock`) but lived in three packages with
three distinct fakes and three subtly different injection styles
(functional Option / required struct field / optional fallback).

G6 PR#346 inventoried the residual wall-clock dependencies in test
code by requiring `//archtest:allow:test-sleep <reason>` on every
`time.Sleep(...)` call. The 88 markers it produced split roughly into:

- TTL / expiry / lease (~30): can be eliminated by an injectable Clock.
- OS-sync (fsnotify delivery, goroutine entering `select`) (~30-40): no
  synchronization point exists; sleep is the right tool.
- Sleep-IS-the-test (debounce window, negative tests) (~20): sleep is
  the system under test, not infrastructure.

The first bucket is what this change targets. Eliminating those ~30
sleeps requires that production code stop reading the wall clock
directly so that tests can drive expiry semantics deterministically.

## Decision

Introduce a single platform-level Clock package at `kernel/clock` and
forbid every production package from calling `time.Now / time.Since /
time.Until / time.NewTimer` directly.

### Public surface

```go
package clock

type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
    Until(t time.Time) time.Duration
    NewTimerAt(deadline time.Time) Timer
}

type Timer interface {
    C() <-chan time.Time
    Stop() bool
    Reset(d time.Duration) bool
}

func Real() Clock // returns the singleton wall-clock implementation
```

`Now / Since / Until` are the obvious shorthands. `NewTimerAt` takes an
absolute deadline rather than a duration: a duration-based shape would
require a non-atomic read-then-act between fetching `Now()` and arming
the timer, and the FakeClock cannot provide a race-free duration timer
under concurrent `Advance`. Callers wanting "fire after d" write
`clk.NewTimerAt(clk.Now().Add(d))` — the absolute deadline is captured
at one point under the FakeClock's mutex.

### Test fake

`kernel/clock/clockmock` ships the deterministic `FakeClock`:

```go
package clockmock

func New(initial time.Time) *FakeClock

func (fc *FakeClock) Now() time.Time
func (fc *FakeClock) Since(t time.Time) time.Duration
func (fc *FakeClock) Until(t time.Time) time.Duration
func (fc *FakeClock) NewTimerAt(deadline time.Time) clock.Timer
func (fc *FakeClock) Advance(d time.Duration)
func (fc *FakeClock) Set(t time.Time)
func (fc *FakeClock) PendingTimers() int
```

`Advance` and `Set` move the synthetic clock and atomically fire all
timers whose deadline has passed. `PendingTimers` lets tests block
until a goroutine has registered a timer before driving the clock.

### Injection convention

Every type that depends on the clock takes it through the constructor.
The canonical pattern is a `Clock clock.Clock` field on the type's
`Config` (or a positional `clk` parameter when the constructor has no
config):

```go
// composition root passes clock.Real() once
asm := assembly.New(assembly.Config{
    ID:    "primary",
    Clock: clock.Real(),
    ...
})
```

`if cfg.Clock == nil { cfg.Clock = clock.Real() }` is the project's
canonical "construct-with-fallback-to-non-nil" pattern (CLAUDE.md
"构造函数出口保证所有字段非 nil"). The fallback is to `clock.Real()`,
which is itself in the archtest whitelist, so this satisfies both the
PROD-CLOCK-INJECTION-01 gate and the project rule that constructors
should not propagate nil.

### Static enforcement

The new archtest `tools/archtest/prod_clock_injection_test.go` enforces
PROD-CLOCK-INJECTION-01: every Go file classified by
`tools/internal/fileroles.IsProductionCode` must not call
`time.Now / time.Since / time.Until / time.NewTimer` directly. The
single whitelist is:

- `kernel/clock/` (and its `clockmock/` subpackage) — owns the Real
  implementation that legitimately delegates to the standard library.
- `pkg/securecookie/` — `pkg/` packages are constrained by depguard to
  stdlib-only imports, so they cannot reach `kernel/clock`. Each
  affected `pkg/` package therefore declares a tiny local `Clock`
  interface (a single `Now()` method) and a private `realClock` value
  the `New()` constructor falls back to. Higher layers pass their
  injected `kernel/clock.Clock` into `WithClock(...)` — the
  structural-typing check makes them interchangeable.

The gate runs in the `tools` build-test shard (already wired) and as a
dedicated `hack/verify-prod-clock-injection.sh` entry point under the
`make verify` umbrella.

## Consequences

### Positive

- A single canonical `Clock` interface; no more interface drift across
  three packages. The three local `Clock` types are deleted; their
  callers import `kernel/clock` directly.
- Time-dependent business logic (refresh-token rotation, lockout
  windows, hashchain timestamps, command lease expiry, hook-runtime
  measurement, latency middleware, etc.) is now testable on a
  deterministic clock without ad-hoc mocking per package.
- The archtest gate prevents new wall-clock dependencies from sneaking
  into production code via plain `time.Now()` calls.
- D6 unlocks G9 (PR-V11-SLOW-TEST-BUDGET): once TTL-driven sleeps in
  test code are replaced with `clockmock.Advance(d)`, G9 can apply a
  much tighter per-test wall-time budget (~2s) without false positives.

### Negative

- Constructor signatures across cells/runtime/adapters/examples grow
  by one field. Each composition root has to thread `clock.Real()` to
  every consumer; tests that don't exercise time-dependent paths
  acquire a `Clock: clock.Real()` line of boilerplate.
- The `pkg/securecookie` exemption introduces a small structural
  duplication (a local `Clock` interface that mirrors
  `kernel/clock.Clock`'s `Now()` method). This is the one place the
  layer rule forces a copy. Future `pkg/` packages with the same need
  follow the same pattern.

### Test-side residue

The 88 `//archtest:allow:test-sleep` markers from G6 are not removed by
this change — they catalog the OS-sync and sleep-is-SUT cases that
even an injected clock cannot eliminate. After D6 lands, the next
sweep should walk the remaining markers and replace the TTL/expiry
ones with `clockmock.Advance(d)` to drop the test-sleep count further.
G9 (PR-V11-SLOW-TEST-BUDGET) will then enable a tighter wall-time
budget gate that catches future drift.

## Alternatives considered

- **External library (`jonboulle/clockwork`, `benbjohnson/clock`)** —
  rejected per the dependency-selection rule (Clock is GoCell domain
  lifecycle infrastructure, not an external standard). The locally
  authored implementation reuses the proven FakeClock algorithm from
  `runtime/distlock/locktest`, which already passes the manager's
  race-detector workload.

- **`PassiveClock` / `Clock` split** (Kubernetes `k8s.io/utils/clock`
  shape) — rejected. No GoCell consumer needs a read-only view that
  excludes timers; the single combined interface keeps the surface
  small.

- **Required Clock with no fallback** — rejected after empirical
  evaluation. The "required field, panic on nil" shape would force
  every test fixture in the repo to add `Clock: clock.Real()` to its
  config literal, which compounds across ~50 test files. The
  fallback-to-`clock.Real()` pattern follows the existing
  `NopHookObserver`/`NopProvider` convention in the same Config types
  and gates production wiring through composition-root review rather
  than runtime panics.

- **Functional `WithClock` Option on every constructor** — rejected.
  Three of the affected types already use struct-Config injection;
  forcing Options would have created mixed conventions. The struct
  field is cheaper to read and to verify in tests.

## Closure status (2026-05-02 follow-up)

The original D6 PR shipped Now/Since/Until/NewTimer injection with a
narrow gate. The follow-up review (`witty-dazzling-balloon` plan) found
three structural gaps and closed them in a single PR continuation:

1. **Gate scope expanded to 9 forbidden symbols + type-aware resolution.**
   `forbiddenTimeFns` now covers `Now / Since / Until / NewTimer /
   NewTicker / After / AfterFunc / Tick / Sleep`. Resolution moved from
   `ident.Name == "time"` string matching to `info.ObjectOf(sel).(*types.Func)`
   + `obj.Pkg().Path() == "time"`, immune to import aliases (`import t
   "time"; t.Now`) and dot-imports (`import . "time"; Now()`). The walk
   covers both `*ast.SelectorExpr` (call positions and value references
   like `now := time.Now`) and bare `*ast.Ident` (dot-import call sites).
   Method calls `t1.After(u)` / `t1.Before(u)` are excluded by checking
   `sig.Recv() != nil`. ref: `dominikh/go-tools` analysis/code/code.go
   `CallName` / `IsCallToAny`.

2. **Single-root Clock now propagates through `cell.Dependencies`.**
   `kernel/cell.Dependencies` gains a required `Clock clock.Clock` field;
   `assembly.startInternal` threads `cfg.Clock` into every cell's `Init`.
   `assembly.New` rejects nil and typed-nil at construction (reflect-based
   `IsNil` on Ptr/Map/Chan/Func/Slice/Interface kinds) so misconfiguration
   surfaces at composition rather than at first method call. The kernel
   panic is delegated to the public `clock.MustHaveClock(c, ctx)` helper,
   keeping it auto-exempt under PANIC-REGISTERED-01 §5 (Must* prefix).

3. **All `nil → clock.Real()` fallbacks and `WithXxxClock` Options that
   carried the old `func() time.Time` pattern were deleted.** Every
   migrated constructor now takes `clk clock.Clock` as a required
   parameter and validates it via `clock.MustHaveClock`. Migrated
   modules: bootstrap (+phases_assembly + lifecycle), runtime/http/health,
   runtime/eventrouter, runtime/outbox/relay, runtime/distlock,
   runtime/config/watcher, runtime/auth/refresh/{gc_worker,memstore},
   runtime/worker/periodic, runtime/websocket/hub, runtime/eventbus
   (pubsub channel-time), runtime/bootstrap/phases_shutdown,
   runtime/http/middleware/metrics, kernel/outbox/consumer_base,
   kernel/assembly/hook_dispatcher, kernel/command/sweeper, kernel/idempotency/inmem,
   kernel/governance/validate, runtime/auth/{authenticator,jwt,keys,nonce,
   servicetoken,config/registry}, cells/accesscore/internal/sessionmint,
   adapters/postgres/refresh_store, adapters/rabbitmq/{connection,publisher},
   adapters/ratelimit/token_bucket, adapters/vault/transit_provider,
   cells/configcore/internal/adapters/postgres/plaintext_migration,
   cells/accesscore/initialadmin/{scheduler,sweep,cleaner,bootstrap,lifecycle},
   tests/e2e/internal/clients.

   `runtime/auth/keys.go` carries a coordination note for
   `worktrees/201-wm2-key-rotation`; the `WithKeySetClock(clk clock.Clock)`
   signature replaces the previous `WithKeySetClock(fn func() time.Time)`.

### Compatibility statement

There is no backwards compatibility carve-out: `WithClock` Options that
took `func() time.Time` arguments were removed entirely (no deprecation
window). Production composition roots (`gocell.go`, `cmd/corebundle/bundle.go`)
remain the only legitimate sources of `clock.Real()`; every other `New(...)`
caller threads the same instance.

The Clock interface gained three methods (`NewTicker(d) Ticker`,
`AfterFunc(deadline, fn) Timer`, `Sleep(ctx, until) error`) plus the
`Ticker` type; this is also a breaking change for any out-of-tree
implementor of `clock.Clock`. The repository contains exactly two
in-tree implementors (`realClock`, `clockmock.FakeClock`); both were
updated atomically in the same commit.

### Gate verification

`tools/archtest/testdata/prod_clock_injection_fixtures/` holds 10
fixture sub-packages exercising every bypass shape: `after_violates`,
`newticker_violates`, `afterfunc_violates`, `tick_violates`,
`sleep_violates`, `alias_violates` (`import t "time"; t.Now`),
`dot_import_violates` (`import . "time"; Now()`),
`func_value_ref_violates` (`now := time.Now`),
`struct_field_assign_violates` (`{now: time.Now}`), and
`injected_clock_passes` (positive shape). `TestProdClockInjectionFixtures`
asserts the exact violation lines, so any regression that shrinks the
type-aware predicate fails the fixture suite.

`hack/verify-prod-clock-injection.sh` is the operator-facing wrapper that
runs all D6 archtests as one shot:
`go test ./tools/archtest/ -run 'TestProdClockInjection|TestKernelClockLeafFallback|TestProdClockInjectionFixtures'`
(extended in PR #348 Round 2 to include the leaf-fallback gate and the
fixture regression suite). Round 2 also added two additional D6 gates
(`KERNEL-CLOCK-RESET-RELATIVE-PROD-01`, `CLOCK-INJECTION-TEST-CALLSITE-01`)
which are part of the broader `tools/archtest` suite invoked by `make verify`.

### Industry alignment

The closure aligns with three OSS reference points consulted during
review (see `witty-dazzling-balloon.md`):

- `dominikh/go-tools` analysis/code/code.go — type-aware `CallName` /
  `IsCallToAny` is the gate's resolution model.
- `jonboulle/clockwork` / `benbjohnson/clock` — caller-required, no-default
  Clock; matches the deleted-fallback decision.
- `k8s.io/client-go SharedIndexInformer` — single root threads down via
  explicit assignment; matches the `cell.Dependencies.Clock` propagation.

## Closure follow-up (2026-05-02, PR #348 review)

The first closure pass left two structural drifts that the PR #348 review
surfaced and this follow-up resolves end-to-end:

1. **Leaf-level `clock.Real()` fallbacks survived in production code.**
   Constructors such as `NewOutboxStore(db, clk ...clock.Clock) { c :=
   clock.Real(); if len(clk) > 0 ... }`, `LoadKeySetFromEnv`, middleware
   factories (`access_log`, `metrics`), and assorted adapters
   (`rabbitmq/{connection,publisher,subscriber}`, `vault/transit_provider`,
   `eventbus`, `websocket/hub`) were "construct succeeds even when the
   composition root forgot to thread Clock"; first `Now()` call would then
   panic at runtime instead of at startup.

2. **Bootstrap had a dual-clock channel.** `bootstrap.WithAssembly(asm)`
   took the assembly's clock implicitly while `bootstrap.WithClock(clk)`
   set a parallel one; phase3 did warn-and-ignore on mismatch, leaving a
   silent split between assembly hooks (using `asm.Clock()`) and bootstrap
   timers (using `b.clock`). The same dual-channel pattern existed for
   hook observers and hook timeouts (`WithHookTimeout` / `WithHookObserver`
   shadowing `assembly.Config` fields).

### Resolution

- **All 18 production-code leaf fallbacks were eliminated** (caller-side
  cascade where needed). Each migrated constructor takes `clk clock.Clock`
  as a required parameter and validates via `clock.MustHaveClock`. Every
  caller now threads the single root instance.

- **New archtest gate `KERNEL-CLOCK-LEAF-FALLBACK-01`** at
  `tools/archtest/clock_leaf_fallback_test.go` blocks any future
  reintroduction. It performs type-aware AST scanning (`info.ObjectOf` →
  `kernel/clock.Real`), so import aliases and dot-imports are uniformly
  caught. Whitelist is exactly the production composition roots
  (`kernel/clock/clock.go` Real() factory itself, `cmd/corebundle/`,
  `cmd/gocell/` CLI, `gocell.go`, three `examples/*/main.go`,
  `tests/e2e/internal/clients/clients.go`); `_test.go` is out of scope and
  tracked separately as `G12-TEST-CLOCK-REAL-CLEANUP`.

- **Bootstrap collapses to a single channel.** `bootstrap.WithClock` is
  now mandatory whenever `WithAssembly` is used; `phases_assembly.go`
  exposes `CoreAssembly.Clock()` and runs `validateAssemblyClockAlignment`
  in phase0 — `b.clock != asm.Clock()` fail-fasts with a clear error
  rather than the prior warn-and-ignore. The phase3 warning is gone.

- **`WithHookTimeout` and `WithHookObserver` are deleted** (no
  deprecation alias). They were the same dual-channel anti-pattern at the
  hook-policy layer; consumers configure these via `assembly.Config`
  directly. `validateAssemblyHookOptions` blocks any caller that still
  tries to set them when an assembly is present.

### Compatibility statement (follow-up)

There is no backwards-compatibility carve-out. The deleted Options
(`WithHookTimeout`, `WithHookObserver`, plus the leaf-level Options
`WithIssuerClock`, `WithVerifierClock`, `WithKeySetClock` whose only role
was nil-fallback into Real()) had no remaining out-of-tree consumers; all
in-tree callers were rewritten in this PR.

The README quickstart (`README.md` Step 4) was updated to thread `clk :=
clock.Real()` through `assembly.Config.Clock` and `bootstrap.WithClock`,
making the contract visible at the very first sample a new user copies.

### Verification

The single-root contract is enforced statically (the new gate plus the
existing `PROD-CLOCK-INJECTION-01`) and behaviorally:

- `runtime/bootstrap/hook_options_test.go` exercises
  `validateAssemblyClockAlignment` for the matched / mismatched / nil
  permutations.
- `cells/accesscore/auth_integration_test.go` runs the cell wiring with
  a `storetest.FakeClock`-backed refresh store, validating that
  refresh-window arithmetic threads through the injected clock.
- `kernel/outbox/emitter_test.go` covers the `DirectEmitter` boundary
  contract (`TestNewDirectEmitter_NilClock` plus the existing
  zero-`CreatedAt` backfill cases).

## References

- `docs/plans/202605011500-029-master-roadmap.md` Track D #D6
- `docs/plans/202605011500-029-master-roadmap.md` Track G #G6
  (TEST-SLEEP-DISCIPLINE-01, the inventory predecessor)
- `/Users/shengming/.claude-ming/plans/witty-dazzling-balloon.md` —
  follow-up plan that drove the closure
- `kernel/clock/clock.go`, `kernel/clock/clockmock/fake.go`,
  `kernel/clock/guard.go`
- `tools/archtest/prod_clock_injection_test.go`,
  `tools/archtest/prod_clock_injection_fixtures_test.go`,
  `tools/archtest/testdata/prod_clock_injection_fixtures/`
- `hack/verify-prod-clock-injection.sh`
- jonboulle/clockwork — Clock interface shape parity check
- k8s.io/utils/clock — single-layer Clock vs PassiveClock decision
- dominikh/go-tools — CallName / IsCallToAny type-aware predicate
- runtime/distlock/locktest/fake_clock.go (deleted) — FakeClock
  algorithm migration source
