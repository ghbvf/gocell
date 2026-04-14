# Event Consumption Architecture -- Root Cause Analysis

**Date**: 2026-04-11
**Reviewer**: Architect
**Scope**: Structural root cause analysis of 6 recurring findings across 4 independent PR#68 reviews
**Commit baseline**: d68ea14 (Solution B two-phase idempotency + DLX enforcement)

---

## Executive Summary

The 6 problems identified by independent reviewers are **symptoms of two root causes**:

1. **Missing Router abstraction** -- `outbox.Subscriber.Subscribe()` conflates topology setup (exchange/queue declaration) with long-running consumption into a single blocking call, forcing Cells to use heuristic timers and unmanaged goroutines.

2. **Misplaced error classification** -- `PermanentError` is defined in `adapters/rabbitmq/` instead of `kernel/outbox/`, creating a layering inversion where kernel-level `WrapLegacyHandler` cannot reference the concept it needs to implement correct Disposition mapping.

These two root causes produce a cascade of downstream problems: the 100ms race, divergent InMemoryEventBus behavior, goroutine leak on shutdown, and the dual-path complexity of Checker+Claimer coexistence.

---

## Root Cause 1: Subscriber.Subscribe() is Both Setup and Run

### Evidence

The `outbox.Subscriber` interface defines a single method:

```go
// kernel/outbox/outbox.go:189
Subscribe(ctx context.Context, topic string, handler EntryHandler) error
```

The RabbitMQ implementation (`adapters/rabbitmq/subscriber.go:147-212`) does the following inside this single call:

1. Acquire AMQP channel (line 225)
2. Set QoS (line 255)
3. Declare exchange (line 260)
4. Declare DLX exchange (line 266)
5. Declare queue with DLX args (line 279)
6. Bind queue to exchange (line 283)
7. Start consuming (line 290)
8. **Block in consumeLoop** (line 301)

Steps 1-7 are setup; step 8 is a blocking run loop. There is no way for the caller to distinguish "setup completed, now blocking" from "setup failed partway through" without either:

- **A return value** (impossible -- the call blocks)
- **A callback/channel** (not in the interface)
- **A timer heuristic** (what Cells actually do)

### Resulting Problems

| # | Problem | Direct link to root cause |
|---|---------|--------------------------|
| 1 | `time.After(100ms)` race | Cells use timer to guess if setup succeeded |
| 5 | InMemoryEventBus divergence | Each Subscriber implementation reinvents its own run loop semantics |
| 6 | Goroutine supervision | Cells must launch `go func()` because Subscribe blocks; no lifecycle tracking possible |

### Current Cell Code (duplicated pattern)

**audit-core** (`cells/audit-core/cell.go:174-196`):
```go
go func() {
    ctx := context.Background()       // BUG: ignores shutdown
    errCh <- sub.Subscribe(ctx, topic, handler)
}()
select {
case err := <-errCh: ...             // setup error
case <-time.After(100 * time.Millisecond): // heuristic
}
```

**config-core** (`cells/config-core/cell.go:181-195`): identical pattern.

Both cells:
- Use `context.Background()` despite `BaseCell.ShutdownCtx()` being available since the same PR
- Launch goroutines with no WaitGroup tracking
- Have no way to propagate runtime subscription errors after the 100ms window

### Watermill Router Pattern (Reference)

Watermill solves this exact problem by splitting the concern:

```
Router.AddHandler(name, topic, sub, pubTopic, pub, handlerFunc)  // declaration (non-blocking)
Router.Run(ctx)                                                    // blocking run
```

`AddHandler` only registers intent. `Run` does setup + consume for all handlers, and returns errors for setup failures synchronously before entering the blocking consume loop. The Router owns all goroutines and has a unified `Close()` that drains in-flight messages.

Key Watermill design decisions relevant to GoCell:
- `Router` tracks all subscription goroutines via `sync.WaitGroup`
- Setup errors are returned from `Run`, not hidden behind timers
- `Router.Running()` returns a `<-chan struct{}` that closes when all handlers have started
- Each handler gets its own middleware chain (like GoCell's `TopicHandlerMiddleware`)

---

## Root Cause 2: PermanentError Misplaced in Adapters Layer

### Evidence

`PermanentError` is defined in `adapters/rabbitmq/consumer_base.go:63-71`:

```go
package rabbitmq

type PermanentError struct {
    Err error
}
```

But the concept is referenced (by name, in comments) from `kernel/outbox/outbox.go:157-160`:

```go
// Note: PermanentError is mapped to DispositionRequeue, not DispositionReject.
// ConsumerBase.Wrap detects PermanentError via errors.As and upgrades to Reject.
// Without ConsumerBase wrapping, PermanentError will be retried like any other
// error. Direct Subscribe callers needing Reject should use EntryHandler directly.
```

This creates a design inversion: the kernel defines `WrapLegacyHandler` and `Disposition`, but the error classification concept that determines the correct Disposition lives in an adapter. The result:

### Resulting Problems

| # | Problem | Direct link to root cause |
|---|---------|--------------------------|
| 2 | PermanentError only detected by ConsumerBase | `WrapLegacyHandler` cannot reference `rabbitmq.PermanentError` (correct layering prevents import), so it maps ALL errors to Requeue |
| 5 | InMemoryEventBus no PermanentError detection | `runtime/eventbus` also cannot import `adapters/rabbitmq` to detect PermanentError |
| 3 | Dual Checker/Claimer paths | Partially related -- the Checker adapter has a known race, but its deprecation is blocked by the same "everything important lives in adapters" pattern |

### Correct Layering

`PermanentError` is a domain concept (an error that should never be retried). It belongs in `kernel/outbox` alongside `Disposition`, `HandleResult`, and `EntryHandler`. If it lived there:

1. `WrapLegacyHandler` could detect `PermanentError` and return `DispositionReject` directly
2. `InMemoryEventBus` could detect `PermanentError` and route to dead letters immediately
3. Handlers would not need `ConsumerBase` wrapping to get correct PermanentError routing

---

## Root Cause Map (Problem -> Cause)

| Problem | Root Cause 1 (Missing Router) | Root Cause 2 (Misplaced PermanentError) |
|---------|-------------------------------|----------------------------------------|
| P1: 100ms race | PRIMARY | -- |
| P2: PermanentError only in ConsumerBase | -- | PRIMARY |
| P3: Dual Checker/Claimer | secondary (lifecycle complexity) | -- |
| P4: Receipt lifecycle edge cases | secondary (no Router to centralize receipt settlement) | -- |
| P5: InMemoryEventBus divergence | PRIMARY (separate run loop) | PRIMARY (no PermanentError) |
| P6: Goroutine supervision | PRIMARY | -- |

---

## Architectural Direction: Event Router

### Phase 1: Move PermanentError to kernel/outbox (1 PR, non-breaking)

**Scope**: `kernel/outbox/outbox.go`, `adapters/rabbitmq/consumer_base.go`, `runtime/eventbus/eventbus.go`

1. Define `outbox.PermanentError` in `kernel/outbox/outbox.go`
2. Add `rabbitmq.PermanentError` as a type alias: `type PermanentError = outbox.PermanentError`
3. Update `WrapLegacyHandler` to detect `outbox.PermanentError` and return `DispositionReject`
4. Update `InMemoryEventBus.handleWithRetry` to detect `outbox.PermanentError`

**Breaking change risk**: None. The type alias preserves backward compatibility for existing `rabbitmq.PermanentError` users. `WrapLegacyHandler` behavior change (Reject instead of Requeue for PermanentError) is strictly a bug fix -- the current behavior is documented as wrong.

**Files changed**: 3 files, ~30 lines total.

### Phase 2: Introduce EventRouter in runtime/ (1 PR, additive)

**Scope**: New `runtime/eventrouter/router.go`, changes to `kernel/cell/registrar.go`

Define an `EventRouter` that separates declaration from execution:

```go
// runtime/eventrouter/router.go
package eventrouter

type HandlerConfig struct {
    Topic         string
    Handler       outbox.EntryHandler
    Middleware    []outbox.TopicHandlerMiddleware
    ConsumerGroup string
}

type Router struct {
    subscriber outbox.Subscriber
    handlers   []HandlerConfig
    wg         sync.WaitGroup
    running    chan struct{}     // closed when all handlers are consuming
    errCh      chan error        // setup errors
}

func (r *Router) AddHandler(cfg HandlerConfig)
func (r *Router) Run(ctx context.Context) error    // blocks; returns setup errors synchronously
func (r *Router) Running() <-chan struct{}           // closes when setup complete
func (r *Router) Close() error                      // drains in-flight, waits for WaitGroup
```

**Key design decisions** (ref: Watermill `message/router.go`):

1. `Run` performs all topology setup for every handler first. If any setup fails, it returns the error before entering the consume loop. This eliminates the 100ms heuristic.
2. `Running()` channel closes only after all handlers have successfully entered their consume loops. This gives bootstrap a reliable "all subscriptions ready" signal.
3. `Close()` cancels the internal context and waits on `sync.WaitGroup` for all goroutines. This solves goroutine supervision.
4. The Router owns the goroutine lifecycle, not the Cell. Cells only call `AddHandler` during `RegisterSubscriptions`.

**Interface change to EventRegistrar**:

```go
// kernel/cell/registrar.go
type EventRegistrar interface {
    // RegisterSubscriptions declares event subscriptions. Non-blocking.
    // The caller (bootstrap/Router) is responsible for starting consumption.
    RegisterSubscriptions(r EventRouter) error
}

type EventRouter interface {
    AddHandler(topic string, handler outbox.EntryHandler)
}
```

This is a **breaking change** to `cell.EventRegistrar`. Migration path:
- `EventRouter` is a new interface in `kernel/cell/` (minimal: just `AddHandler`)
- The concrete `Router` implementation lives in `runtime/eventrouter/` (correct layering)
- All 3 cells (audit-core, config-core, access-core) are updated atomically in the same PR

**Breaking change risk**: Moderate. All `EventRegistrar` implementations must be updated. Since there are exactly 3 in-tree implementations and 0 known out-of-tree implementations, this is manageable.

### Phase 3: Deprecate Checker, Consolidate Receipt (1 PR, cleanup)

**Scope**: `kernel/idempotency/idempotency.go`, `adapters/rabbitmq/consumer_base.go`, `adapters/redis/`

With the Router centralizing receipt settlement (Commit/Release after broker Ack/Nack), the dual Checker/Claimer paths in ConsumerBase can be reduced:

1. Remove `ConsumerBase.wrapWithChecker` (legacy path)
2. Remove `idempotency.Checker` interface (currently deprecated)
3. Add `sync.Once` guard to Receipt implementations to prevent double Commit/Release
4. Add LeaseTTL vs RetryLoop duration validation in ConsumerBase config

**Breaking change risk**: Low. `Checker` is already `Deprecated` with removal target "Phase 3". The only adapter implementation is `adapters/redis/IdempotencyChecker`.

---

## Layering Verification of Proposed Changes

| Component | Layer | Depends On | Correct? |
|-----------|-------|-----------|----------|
| `outbox.PermanentError` | kernel/outbox | stdlib | YES |
| `cell.EventRouter` (interface) | kernel/cell | kernel/outbox | YES |
| `eventrouter.Router` (impl) | runtime/ | kernel/outbox, kernel/cell | YES |
| `ConsumerBase` (adapter) | adapters/ | kernel/outbox, kernel/idempotency | YES |
| Cell implementations | cells/ | kernel/cell, kernel/outbox | YES |

No new layering violations introduced. The key improvement is that `runtime/eventrouter` sits between `kernel/` (interface) and `adapters/` (subscriber implementation), which is the correct composition layer.

---

## Findings Summary

```
1. [Dependency Direction] PermanentError (adapters/rabbitmq) is a kernel-level concept
   referenced by kernel/outbox comments and needed by runtime/eventbus
   -- Reason: creates a layering smell where adapter defines domain concept
   -- Impact: High (P2, P5 directly caused; P2 is data safety concern)

2. [Cell Boundary] outbox.Subscriber.Subscribe() mixes setup and run lifecycle phases
   -- Reason: prevents synchronous setup validation, forces timer heuristics
   -- Impact: High (P1, P5, P6 directly caused; P1 is reliability concern)

3. [Interface Stability] cell.EventRegistrar change from Subscriber to EventRouter
   is a breaking change requiring coordinated migration
   -- Reason: 3 in-tree implementations, 0 known out-of-tree
   -- Impact: Medium (contained to single PR, all consumers known)

4. [Consistency Level] Receipt lifecycle (LeaseTTL vs RetryLoop duration) has no
   validation -- retryLoop with 3 retries * exponential backoff can exceed
   default 5m LeaseTTL (1s + 2s + 4s = 7s << 5m, safe today, but configurable
   RetryCount with no upper bound)
   -- Reason: no config validation in ConsumerBaseConfig.setDefaults()
   -- Impact: Low (current defaults are safe; becomes a problem if RetryCount
      is increased or RetryBaseDelay is set high)

5. [Performance] audit-core creates 6 goroutines with 6 independent channels for
   100ms heuristic; Router pattern reduces this to a single Run call
   -- Reason: per-topic goroutine launch is forced by blocking Subscribe
   -- Impact: Low (6 goroutines is fine; the real cost is code complexity)

6. [Consistency Level] InMemoryEventBus declares "at-most-once" but provides retry
   (3 attempts) -- this is functionally "at-most-once per message, at-most-3-attempts
   per delivery" which does not match any standard delivery guarantee terminology
   -- Reason: behavioral divergence from production Subscriber
   -- Impact: Medium (tests may pass with InMemoryEventBus but fail in production)
```

---

## Verdict

The 6 recurring problems are not independent bugs -- they trace to two architectural gaps. The proposed 3-PR sequence (PermanentError move, Router introduction, Checker removal) addresses all root causes with minimal disruption:

- **PR 1** (Phase 1): Zero breaking changes, immediate safety improvement
- **PR 2** (Phase 2): One breaking interface change, eliminates 3 problems (P1, P5, P6)
- **PR 3** (Phase 3): Removes deprecated code, simplifies ConsumerBase by ~40%

Total estimated diff: ~300 lines added (Router), ~200 lines removed (Checker path + timer hacks), ~100 lines changed (Cell migrations).
