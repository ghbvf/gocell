// Package testtime exports the canonical timeout / poll-interval constants
// used across GoCell test code. Centralizing these values lets a single edit
// retune the entire suite when CI hardware speeds change, and lets the
// archtest TEST-TIME-LITERAL-01 gate enforce that test code never hard-codes
// time literals at call sites.
//
// Two naming styles are exported:
//
//  1. Semantic aliases (preferred for new code): EventuallyDefault,
//     SelectShutdown, ShortSleep, etc. Use when the call site has a clear
//     intent that one of these names captures.
//  2. Mechanical aliases (fallback for sweeps and unique sites): D5ms,
//     D3s, D24h, etc. Use when no semantic alias fits — the constant just
//     names a duration value directly. Identifier convention: `D` + value
//     + `ms`/`s`/`min`/`h`/`Neg<value><unit>` for negatives.
//
// When a test site has a genuinely unique deadline that no exported constant
// captures (e.g. a Redis TTL=1ms conformance buffer), declare a file-local
// `const xxx = N * time.Millisecond` next to the test instead of inflating
// this package. The archtest accepts any named-constant identifier.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6 TEST-TIME-LITERAL-01
package testtime

import "time"

// --- Semantic aliases (preferred) ------------------------------------------

// EventuallyShort is the upper bound for in-memory unit-test convergence
// (no I/O, no goroutine scheduling latency beyond Go runtime).
const EventuallyShort = time.Second

// EventuallyDefault is the default Eventually timeout for tests that wait on
// HTTP / outbox / authentication readiness.
const EventuallyDefault = 3 * time.Second

// EventuallyLong is the upper bound for tests that wait on Postgres,
// RabbitMQ, or Vault readiness — adapters that round-trip a network.
const EventuallyLong = 5 * time.Second

// EventuallyExtraLong is the upper bound for full-stack e2e suites that
// boot multiple containers and assemble a full assembly.
const EventuallyExtraLong = 10 * time.Second

// FastPoll is the polling cadence for HTTP readiness / heartbeat checks
// where the underlying state flips on the order of single-digit ms.
const FastPoll = 5 * time.Millisecond

// MediumPoll is the default polling cadence for outbox / idempotency /
// session translations where state flips on the order of tens of ms.
const MediumPoll = 50 * time.Millisecond

// SlowPoll is the polling cadence for tests that drive RabbitMQ / Postgres
// migrators where state flips can take 100+ ms.
const SlowPoll = 100 * time.Millisecond

// ShortSleep is the canonical "wait for the goroutine to make progress"
// duration. Prefer fake clocks / sync primitives; use ShortSleep only when
// the runtime under test exposes no synchronization point.
const ShortSleep = 50 * time.Millisecond

// SelectShutdown is the deadline for `case <-time.After(...)` clauses that
// guard a graceful shutdown assertion.
const SelectShutdown = 5 * time.Second

// SelectAsyncSettle is the deadline for `case <-time.After(...)` clauses
// that guard an asynchronous event-driven settle (outbox publish, consumer
// dispatch, audit append).
const SelectAsyncSettle = 10 * time.Second

// CtxShort is the default `context.WithTimeout` deadline for tests that
// exercise a single in-process operation.
const CtxShort = time.Second

// CtxDefault is the default `context.WithTimeout` deadline for tests that
// exercise an HTTP round-trip or single DB query.
const CtxDefault = 5 * time.Second

// CtxLong is the upper bound for `context.WithTimeout` deadlines in tests
// that drive integration adapters.
const CtxLong = 30 * time.Second

// --- Mechanical aliases (fallback for unique-context literals) -------------
//
// These constants name duration values directly so any literal seen during
// the G6 sweep can be replaced without inventing a semantic name.
// Naming: `D` + magnitude + unit (`ms` / `s` / `min` / `h`); negatives use
// `Neg` (e.g. `DNeg1s`).

// Sub-second.
const (
	D1ms   = 1 * time.Millisecond
	D2ms   = 2 * time.Millisecond
	D5ms   = 5 * time.Millisecond
	D10ms  = 10 * time.Millisecond
	D20ms  = 20 * time.Millisecond
	D25ms  = 25 * time.Millisecond
	D50ms  = 50 * time.Millisecond
	D80ms  = 80 * time.Millisecond
	D100ms = 100 * time.Millisecond
	D150ms = 150 * time.Millisecond
	D200ms = 200 * time.Millisecond
	D250ms = 250 * time.Millisecond
	D300ms = 300 * time.Millisecond
	D500ms = 500 * time.Millisecond
	D750ms = 750 * time.Millisecond
)

// Seconds.
const (
	D1s  = 1 * time.Second
	D2s  = 2 * time.Second
	D3s  = 3 * time.Second
	D5s  = 5 * time.Second
	D7s  = 7 * time.Second
	D10s = 10 * time.Second
	D15s = 15 * time.Second
	D20s = 20 * time.Second
	D30s = 30 * time.Second
	D60s = 60 * time.Second
)

// Minutes.
const (
	D1min  = 1 * time.Minute
	D2min  = 2 * time.Minute
	D5min  = 5 * time.Minute
	D10min = 10 * time.Minute
	D15min = 15 * time.Minute
	D30min = 30 * time.Minute
)

// Hours and longer.
const (
	D1h  = 1 * time.Hour
	D2h  = 2 * time.Hour
	D24h = 24 * time.Hour
)

// Negatives (used when a test asserts an expired deadline).
const (
	DNeg1s = -1 * time.Second
	DNeg1h = -1 * time.Hour
)
