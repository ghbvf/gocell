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

## References

- `docs/plans/202605011500-029-master-roadmap.md` Track D #D6
- `docs/plans/202605011500-029-master-roadmap.md` Track G #G6
  (TEST-SLEEP-DISCIPLINE-01, the inventory predecessor)
- `kernel/clock/clock.go`, `kernel/clock/clockmock/fake.go`
- `tools/archtest/prod_clock_injection_test.go`
- `hack/verify-prod-clock-injection.sh`
- jonboulle/clockwork — Clock interface shape parity check
- k8s.io/utils/clock — single-layer Clock vs PassiveClock decision
- runtime/distlock/locktest/fake_clock.go (deleted) — FakeClock
  algorithm migration source
