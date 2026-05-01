# Test-Time Discipline

> Status: ratified 2026-05-01 with G6 / TEST-TIME-LITERAL-01 (refactor/501).
> Companion gate: PROD-DURATION-CONST-01 (PR#336) for production code.

## Why

Tests written with hard-coded `time.Sleep(50 * time.Millisecond)` /
`require.Eventually(..., 3*time.Second, 50*time.Millisecond)` /
`context.WithTimeout(ctx, 5*time.Second)` create three structural problems:

1. **CI flakes scale with hardware variance.** A 50 ms sleep that worked on a
   16-core dev box will flake under contention on a shared 4-core CI worker.
2. **The same intent is re-encoded across hundreds of files.** "Wait for the
   HTTP server to become ready" appears as `3*time.Second` in some tests and
   `5*time.Second` in others; tuning one is invisible to the rest.
3. **There is no static guard.** New tests keep introducing new literals.

The fix is the same architectural pattern PROD-DURATION-CONST-01 established
for production code: ban `time.Duration` literals at call sites, force every
duration into a named constant. A central package — `pkg/testutil/testtime` —
exposes the canonical timeouts and poll intervals so a single edit retunes
the entire suite.

## Invariant (TEST-TIME-LITERAL-01)

> In every Go file whose role is *test code*, any expression whose static
> type is `time.Duration` and whose subtree contains a numeric literal must
> appear directly in the initializer of a **package-level** `const`
> declaration. The literal `0` is exempt (zero-value idiom).

"Test code" means any of:

- `*_test.go` (the canonical Go test convention)
- `**/conformance.go` (driver-conformance suites for adapters)
- any file under a test-helper package: `**/locktest/`, `**/outboxtest/`,
  `**/storetest/`, `**/healthtest/`, `**/contracttest/`, `**/commandtest/`

The static guard lives at `tools/archtest/test_time_literal_test.go`. The
`hack/verify-test-time-literal.sh` script wires it into `make verify`.

## How to write a new test

### 1. Reach for a `pkg/testutil/testtime` constant first

```go
import (
    "github.com/stretchr/testify/require"
    "github.com/ghbvf/gocell/pkg/testutil/testtime"
)

require.Eventually(t, func() bool {
    return server.Ready()
}, testtime.EventuallyDefault, testtime.MediumPoll, "server should become ready")
```

The package exports two naming styles:

| Style | When to use | Examples |
|---|---|---|
| **Semantic** | Call-site intent fits one of the names | `EventuallyDefault`, `EventuallyLong`, `FastPoll`, `MediumPoll`, `SelectShutdown`, `SelectAsyncSettle`, `CtxDefault`, `ShortSleep` |
| **Mechanical** | The literal is just a duration value, no clear intent | `D5ms`, `D50ms`, `D3s`, `D5s`, `D24h`, `DNeg1s` |

Prefer semantic names — they document intent. Use mechanical names when no
semantic alias fits or during sweep refactors.

### 2. Declare a file-local const for unique site values

If your test needs a deadline that no `testtime.*` constant captures, put it
at the top of the file:

```go
package mypackage_test

import (
    "testing"
    "time"
)

const redisConformanceTTLBuffer = 5 * time.Millisecond // wait past TTL=1ms to verify expiry

func TestRedisTTLExpiry(t *testing.T) {
    // ...
    time.Sleep(redisConformanceTTLBuffer)
    // ...
}
```

The const must be **package-level** (top of the file). Function-local
`const` declarations are also flagged by the gate.

### 3. Don't try to bypass the gate

There is no `//archtest:allow` mechanism — and there shouldn't be. Any
literal duration in test code, including test data, has a name worth
spelling out. If you find yourself wanting to bypass: declare a file-local
const. The gate accepts any named-constant identifier.

## Intentional "real-clock" sleeps

Three test sites hold genuine real-clock waits because the system under test
exposes no synchronization point:

| File | Const | Reason |
|---|---|---|
| `adapters/redis/integration_test.go` | `redisConformanceTTLBuffer = 5 * time.Millisecond` | Redis Cluster TTL=1ms conformance: the test sets a 1 ms TTL and waits past it to verify physical expiry. Replacing with a fake clock would defeat the assertion. |
| `runtime/shutdown/shutdown_wait_signal_unix_test.go` | `signalHandlerSetupGrace = 50 * time.Millisecond` | Signal-handler installation race: `Wait()` registers `signal.Notify` from a goroutine; the test must let the goroutine reach the registration before sending the signal. |
| `cells/accesscore/initialadmin/cleaner_test.go` | (named local consts) | Mostly migrated to fake clock; remaining literals are bridge-period synchronizers. |

These keep their own file-local consts rather than importing
`testtime.*` — the value is meaningful at the site, not a generic timeout.

## `runtime.Gosched()` is not in scope

`runtime.Gosched()` takes no duration argument and is the canonical
poll-with-deadline yield primitive used in FakeClock-driven tests:

```go
// runtime/distlock/manager_test.go
deadline := time.Now().Add(testtime.EventuallyExtraLong)
for fc.PendingTimers() < 1 {
    if time.Now().After(deadline) {
        t.Fatalf("timed out (got %d)", fc.PendingTimers())
    }
    runtime.Gosched()
}
```

Replacing `Gosched` with `sync.WaitGroup` would deadlock these tests because
the FakeClock event loop has no signal source — only the test's own
`fc.Advance(...)` calls drive it. The gate explicitly does not flag bare
`runtime.Gosched()` calls.

## Future scope

- **D6 PROD-CLOCK-INJECTION-01**. After G6 lands, run a soak window in CI
  to identify TTL/lease/expiry tests that remain flaky despite the literal
  cleanup. If real-clock dependence is the residual cause, promote the
  three local `Clock` interfaces (`runtime/distlock/clock.go`,
  `runtime/auth/refresh/types.go`, `cells/accesscore/initialadmin/clock.go`)
  into a shared `kernel/clock/` and force production code to inject it.
  Tracked as Track D #D6 in `docs/plans/202605011500-029-master-roadmap.md`.

- **forbidigo `time.Sleep` in `*_test.go`**. When G2 enables `forbidigo`
  cluster-wide, add `time.Sleep` to its forbid list as a belt-and-suspenders
  guard. The current archtest already enforces named constants, so this
  duplication is purely defensive.

## References

- Roadmap entry: `docs/plans/202605011500-029-master-roadmap.md` G6
- Sibling gate: `tools/archtest/prod_duration_const_test.go` (PR#336 PROD-DURATION-CONST-01)
- Constants: `pkg/testutil/testtime/testtime.go`
- Gate implementation: `tools/archtest/test_time_literal_test.go`
- Verify script: `hack/verify-test-time-literal.sh`
