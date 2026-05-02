// Package clock is GoCell's platform-level Clock abstraction.
//
// All production code that depends on the current time, elapsed durations,
// timer firing, ticker delivery, callback scheduling, or context-aware
// blocking sleep must accept a [Clock] through its constructor and call
// into the injected instance. Two complementary archtest gates enforce
// this contract (tools/archtest, both type-aware so import aliases,
// dot-imports, function-value references, and struct-field assignments
// are uniformly covered):
//
//   - PROD-CLOCK-INJECTION-01 forbids direct stdlib time entry points in
//     production code (time.Now / time.Since / time.Until / time.NewTimer /
//     time.NewTicker / time.After / time.AfterFunc / time.Tick / time.Sleep).
//     Whitelist: kernel/clock and kernel/clock/clockmock; pkg/securecookie
//     keeps a thin local Clock interface that the higher layer satisfies
//     structurally with a kernel/clock.Clock instance, since pkg/ is
//     constrained by LAYER-01 to stdlib-only imports and cannot reach
//     kernel/clock.
//
//   - KERNEL-CLOCK-LEAF-FALLBACK-01 forbids leaf-level construction via
//     kernel/clock.Real() outside the composition root. Whitelist: the
//     Real() factory definition itself (kernel/clock/clock.go), the main
//     and example composition roots (cmd/corebundle/, gocell.go,
//     examples/*/main.go, examples/ssobff/app.go), and the e2e suite's
//     own composition root (tests/e2e/internal/clients/clients.go).
//     _test.go files are out of scope — test-side cleanup is tracked
//     separately as G12-TEST-CLOCK-REAL-CLEANUP.
//
// Test code should use [github.com/ghbvf/gocell/kernel/clock/clockmock] which
// provides a deterministic [Clock] implementation whose progress is controlled
// explicitly via Advance and Set.
//
// Composition root convention: a single [Real] instance is constructed at
// process start (cmd/corebundle/bundle.go and gocell.go are the only
// legitimate callers) and threaded through to every consumer. Constructors
// must declare clock as a required parameter — no default fallback, no
// Option-style optional injection — and validate at the boundary via the
// public helper [MustHaveClock]. The kernel/cell.Dependencies struct carries
// the Clock field so that assembly auto-propagates the root clock to every
// cell's Init.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md (ADR
// + 2026-05-02 closure)
// ref: jonboulle/clockwork — caller-required, no-default Clock parity
// ref: k8s.io/client-go SharedIndexInformer — single root threaded down
package clock
