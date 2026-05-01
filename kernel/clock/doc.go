// Package clock is GoCell's platform-level Clock abstraction.
//
// All production code that depends on the current time, elapsed durations, or
// timer firing must accept a [Clock] through its constructor and call into the
// injected instance. Direct calls to time.Now / time.Since / time.Until / time.NewTimer
// in non-test code are forbidden by the PROD-CLOCK-INJECTION-01 archtest gate
// (see tools/archtest); the only whitelist is this package itself, which holds
// the canonical wall-clock implementation [Real].
//
// Test code should use [github.com/ghbvf/gocell/kernel/clock/clockmock] which
// provides a deterministic [Clock] implementation whose progress is controlled
// explicitly via Advance and Set.
//
// Composition root convention: a single [Real] instance is constructed at
// process start and threaded through to every consumer. Constructors must
// declare clock as a required parameter — no default fallback, no Option-style
// optional injection — so missing wiring fails fast at construction time
// rather than masquerading as wall-clock-driven flakiness in tests.
package clock
