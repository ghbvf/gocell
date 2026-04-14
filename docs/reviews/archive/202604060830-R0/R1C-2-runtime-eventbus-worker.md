# R1C-2: runtime/eventbus + runtime/worker Review Report

- **Reviewer agent**: R1C-2 (runtime/eventbus, runtime/worker)
- **Review baseline commit**: `ce03ba1` (HEAD of develop at review time)
- **Review date**: 2026-04-06
- **Seats exercised**: S1 (Architecture), S3 (Test/Regression), S5 (DX/Maintainability)

---

## Review Scope

| Package | Source files | LOC (source) | Test files | LOC (test) |
|---------|-------------|-------------|------------|------------|
| `runtime/eventbus` | `eventbus.go`, `doc.go` | 237 | `eventbus_test.go` | 226 |
| `runtime/worker` | `worker.go`, `periodic.go`, `doc.go` | 96 | `worker_test.go` | 204 |
| **Total** | **5 source** | **~333** | **2 test** | **~430** |

### Downstream consumers examined

- `adapters/postgres/outbox_relay.go` -- implements both `outbox.Relay` and `worker.Worker`
- `runtime/bootstrap/bootstrap.go` -- orchestrates `worker.WorkerGroup` and `eventbus.InMemoryEventBus`
- All `cells/*/cell_test.go` and `cells/*/slices/*/service_test.go` -- use `eventbus.New()` as test double

---

## Dependency Direction Check

| Check | Result | Evidence |
|-------|--------|----------|
| `runtime/eventbus` imports kernel/ or pkg/ only | PASS | Imports: `kernel/outbox`, `pkg/errcode`, `pkg/uid`, stdlib (`context`, `log/slog`, `sync`, `time`) |
| `runtime/eventbus` does NOT import cells/ or adapters/ | PASS | No such imports |
| `runtime/worker` imports only stdlib | PASS | Imports: `context`, `log/slog`, `sync`, `time` -- no kernel/ or pkg/ deps at all |
| `runtime/worker` does NOT import cells/ or adapters/ | PASS | No such imports |
| Cells depend on `outbox.Publisher` interface, not concrete `InMemoryEventBus` | PASS | All cell production code uses `outbox.Publisher`; `eventbus.New()` appears only in `_test.go` files |
| `adapters/postgres/outbox_relay.go` depends on `runtime/worker.Worker` (interface) | PASS | Compile-time check at line 20: `_ worker.Worker = (*OutboxRelay)(nil)` |

---

## Findings

### R1C2-F01 | P1 | eventbus: Close() + Subscribe() race on channel read after close

- **Seat**: S1 (Architecture), S3 (Test)
- **File**: `runtime/eventbus/eventbus.go:138-148`, `runtime/eventbus/eventbus.go:152-168`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: In `Close()` (line 152-168), the bus cancels each subscription's context and then closes its channel in the same loop iteration. Meanwhile, in `Subscribe()` (line 138-148), the select statement reads from `sub.ch`. There is a timing window where:

1. `Close()` calls `sub.cancel()` -- this triggers `subCtx.Done()` to become ready.
2. `Close()` calls `close(sub.ch)` -- this makes `sub.ch` readable (yields zero value).
3. In `Subscribe`, the `select` at line 139-148 non-deterministically picks between `<-subCtx.Done()` and `<-sub.ch`. If `<-sub.ch` wins after close, `ok` is `false` and the goroutine returns cleanly (line 143-145). This path is fine.

However, the real risk is subtler: if a **new message** is sent to `sub.ch` by `Publish()` concurrently with `Close()`, the RWMutex protects `Publish` from writing to the map after `closed=true`, but `Publish()` may already be inside the `RLock` section iterating subscribers at line 99-108 when `Close()` acquires the write lock. Because `Close()` holds `mu.Lock()` while closing channels, and `Publish()` holds `mu.RLock()` while sending to channels, the `RLock` in `Publish` blocks `Close()` from acquiring `Lock` -- so `close(sub.ch)` cannot execute while `Publish` is in-flight. This means the mutex correctly prevents sending to a closed channel.

**After deeper analysis**: The RWMutex discipline is actually sound. `Publish` holds `RLock` during the send, and `Close` acquires `Lock` before closing channels. This prevents the "send on closed channel" panic. The concern is mitigated by the existing locking. **Downgrading this to P2** -- the code is correct but the lack of an explicit comment explaining this invariant makes it a maintenance hazard.

**Revised severity**: P2

**Suggested fix**: Add a comment above the `Close()` method explaining why the RWMutex prevents send-on-closed-channel panics:

```go
// Close terminates all subscriber goroutines and prevents new publishes.
// Safety: Close holds mu.Lock(), which excludes all Publish() calls (which
// hold mu.RLock()), so no goroutine can send to sub.ch while Close is
// executing close(sub.ch).
```

---

### R1C2-F02 | P1 | eventbus: Subscribe leaks subscription from subs map on exit

- **Seat**: S1 (Architecture)
- **File**: `runtime/eventbus/eventbus.go:118-149`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: When `Subscribe()` returns (either by context cancellation at line 140-141 or channel close at line 143-145), the `subscription` struct remains in `b.subs[topic]`. This means:

1. If a caller subscribes and then cancels, the stale `*subscription` stays in the slice.
2. Subsequent `Publish()` calls iterate over the stale subscription and attempt to send to its channel. The channel is not closed by `Subscribe()` on exit (only `Close()` closes channels). Sending to the channel will succeed (the message is buffered or dropped) but no goroutine is reading from it, so messages are silently lost.
3. Over time, repeated subscribe/cancel cycles accumulate stale subscriptions, leaking memory (the channel buffer) and masking message drops.

This is particularly relevant for long-running services that dynamically subscribe/unsubscribe to topics (e.g., during config reload or cell restart).

**Suggested fix**: On `Subscribe()` return, remove the subscription from `b.subs[topic]` and close the channel:

```go
defer func() {
    b.mu.Lock()
    subs := b.subs[topic]
    for i, s := range subs {
        if s == sub {
            b.subs[topic] = append(subs[:i], subs[i+1:]...)
            break
        }
    }
    b.mu.Unlock()
    close(sub.done)
}()
```

---

### R1C2-F03 | P1 | worker: WorkerGroup.Start does not cancel remaining workers on first failure

- **Seat**: S1 (Architecture)
- **File**: `runtime/worker/worker.go:47-72`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: `WorkerGroup.Start()` launches all workers concurrently and waits for all of them to finish (`wg.Wait()`). If one worker returns an error, the error is recorded (line 65) but the other workers continue running until they independently exit. The caller's context (`ctx`) is not cancelled.

In `bootstrap.go` (line 310-323), the pattern is:

```go
workerCtx, workerCancel := context.WithCancel(ctx)
go func() { workerErrCh <- wg.Start(workerCtx) }()
```

If one worker fails, `workerErrCh` only receives the error **after all workers have exited** (`wg.Wait()` at line 71). This means the bootstrap shutdown is delayed until all workers happen to exit on their own. If a worker blocks indefinitely (which is the `Worker.Start` contract: "block until ctx is cancelled"), then a single worker failure does not trigger shutdown of sibling workers.

The go-zero `ServiceGroup` reference implementation cancels all services on any single failure.

**Suggested fix**: Accept a cancellable context and cancel sibling workers on first error:

```go
func (g *WorkerGroup) Start(ctx context.Context) error {
    ctx, cancel := context.WithCancel(ctx)
    defer cancel()
    // ... launch workers with this derived ctx ...
    // On first error, call cancel() to signal siblings.
}
```

Alternatively, add a `CancelOnError bool` field to `WorkerGroup` to make this opt-in.

---

### R1C2-F04 | P1 | worker: PeriodicWorker missing compile-time interface check

- **Seat**: S3 (Test), S5 (DX)
- **File**: `runtime/worker/periodic.go`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: `PeriodicWorker` implements the `Worker` interface (`Start(ctx) error`, `Stop(ctx) error`), but there is no compile-time assertion `var _ Worker = (*PeriodicWorker)(nil)` in the package. The `adapters/postgres/outbox_relay.go` correctly has `var _ worker.Worker = (*OutboxRelay)(nil)` (line 20), and `eventbus_test.go` has `var _ outbox.Publisher = (*InMemoryEventBus)(nil)` (line 223). `PeriodicWorker` should follow the same pattern.

If the `Worker` interface evolves (e.g., adding a `Name() string` method), the breakage in `PeriodicWorker` will only be caught when a test constructs and uses it, not at compile time.

**Suggested fix**: Add to `periodic.go` or `worker.go`:

```go
// Compile-time interface check.
var _ Worker = (*PeriodicWorker)(nil)
```

---

### R1C2-F05 | P1 | worker: PeriodicWorker.Stop is not safe against double-Start

- **Seat**: S1 (Architecture)
- **File**: `runtime/worker/periodic.go:18-52`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: `PeriodicWorker` creates the `done` channel once in the constructor (line 20: `done: make(chan struct{})`). If `Start()` is called twice (either concurrently or sequentially after a stop), both goroutines share the same `done` channel. After the first `Stop()` closes `done`, the second `Start()` will immediately exit because `<-p.done` is ready.

There is no guard against double-start or restart. While the `WorkerGroup` only calls `Start` once, a standalone usage of `PeriodicWorker` could hit this.

**Suggested fix**: Either (a) document that `PeriodicWorker` is single-use, or (b) recreate the `done` channel in `Start()`:

```go
func (p *PeriodicWorker) Start(ctx context.Context) error {
    p.done = make(chan struct{}) // fresh channel per Start
    // ... rest of logic
}
```

Option (b) introduces its own race if `Stop()` is called concurrently with `Start()` assigning a new channel. A `sync.Once` or `atomic` state flag is safer.

---

### R1C2-F06 | P1 | eventbus: handleWithRetry uses time.After without jitter

- **Seat**: S5 (DX/Maintainability)
- **File**: `runtime/eventbus/eventbus.go:197-231`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: The retry delay at line 202 uses pure exponential backoff (`baseRetryDelay * (1 << attempt)`) without jitter. When multiple subscribers fail simultaneously on the same topic, they all retry at the exact same intervals (100ms, 200ms, 400ms), causing a "thundering herd" effect. The Watermill reference implementation uses randomized backoff.

For an in-memory dev/test bus this is unlikely to cause real issues, but since this code is referenced as the pattern for production consumer implementations (per the `eventbus.md` rule), it sets a bad precedent.

**Suggested fix**: Add random jitter (e.g., +/- 25%):

```go
jitter := time.Duration(rand.Int63n(int64(delay) / 2))
delay = delay + jitter - delay/4
```

Or document clearly that production implementations must add jitter.

---

### R1C2-F07 | P2 | worker: WorkerGroup does not prevent Add after Start

- **Seat**: S5 (DX/Maintainability)
- **File**: `runtime/worker/worker.go:38-42`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: `Add()` can be called after `Start()` has already launched workers. The newly added worker will not be started (since `Start` already copied the slice and is running). This is a silent misconfiguration. The go-zero `ServiceGroup` prevents adds after start.

**Suggested fix**: Add a `started bool` field; `Add()` should return an error (or panic) if called after `Start()`.

---

### R1C2-F08 | P2 | eventbus: doc.go and eventbus.go have different package doc comments

- **Seat**: S5 (DX)
- **File**: `runtime/eventbus/doc.go:1-4`, `runtime/eventbus/eventbus.go:1-8`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: `doc.go` says "delivers messages synchronously and is not suitable for production use", while `eventbus.go` says "in-memory implementation of kernel/outbox.Publisher and kernel/outbox.Subscriber for development and testing". Both are package-level doc comments. Go tooling picks one (typically alphabetically first file, which is `doc.go`). The `eventbus.go` comment is more detailed and includes the Watermill reference. Having two competing comments is confusing.

**Suggested fix**: Remove the package comment from `eventbus.go` (keep only the `doc.go` one, but merge the Watermill reference and design notes into it).

---

### R1C2-F09 | P2 | worker: doc.go and worker.go have different package doc comments

- **Seat**: S5 (DX)
- **File**: `runtime/worker/doc.go:1-3`, `runtime/worker/worker.go:1-8`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: Same issue as F08. `doc.go` says "Worker interface and WorkerGroup for managing concurrent background workers with graceful lifecycle control." while `worker.go` has a longer comment including the go-zero reference. Only one survives in `go doc`.

**Suggested fix**: Consolidate into `doc.go`, remove the package comment from `worker.go`.

---

### R1C2-F10 | P2 | eventbus: DeadLetterLen uses Mutex instead of RWMutex for read-only operation

- **Seat**: S5 (DX/Maintainability)
- **File**: `runtime/eventbus/eventbus.go:182-186`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: `DeadLetterLen()` and `DrainDeadLetters()` both use `deadLettersMu.Lock()` (exclusive lock). `DeadLetterLen()` is a read-only operation and could use `RLock()` if `deadLettersMu` were a `sync.RWMutex`. This is a minor performance concern (read contention on the dead letter count) but more importantly a code clarity issue -- readers seeing `Lock()` for a read-only function may wonder if there's a hidden mutation.

**Suggested fix**: Change `deadLettersMu` to `sync.RWMutex` and use `RLock()` in `DeadLetterLen()`.

---

### R1C2-F11 | P2 | worker: WorkerGroup error handling loses context from multiple failures

- **Seat**: S5 (DX)
- **File**: `runtime/worker/worker.go:53-72`
- **Commit**: `ce03ba1`
- **Status**: OPEN

**Description**: `Start()` captures only the first error via `errOnce.Do` (line 65). If multiple workers fail, only the first error is returned; subsequent errors are logged but discarded from the return value. For debugging, it would be useful to aggregate all errors (e.g., via `errors.Join` from Go 1.20+).

Similarly, `Stop()` (line 76-93) also returns only the first error.

**Suggested fix**: Use `errors.Join(firstErr, err)` to aggregate all worker errors into the return value.

---

## Test Coverage Assessment

### eventbus test coverage

| Test case | What it covers |
|-----------|---------------|
| `TestPublishSubscribe` | Happy path: 2 messages delivered to 1 subscriber |
| `TestPublish_NoSubscribers` | Publish to topic with no subscribers (no-op, no error) |
| `TestSubscribe_RetryAndDeadLetter` | All 3 retries fail -> dead letter queue |
| `TestClose_PreventsFurtherPublish` | Publish after Close returns error |
| `TestClose_Idempotent` | Double-close is safe |
| `TestSubscribe_ClosedBus` | Subscribe to closed bus returns error |
| `TestMultipleSubscribers` | Fan-out: 1 message delivered to 2 subscribers |
| `TestSubscribe_SuccessAfterRetry` | Handler succeeds on 3rd attempt -> no dead letter |
| `TestHealth` | Health reports "healthy" / "closed" |
| `TestTopicConfigChangedConstant` | Constant value assertion |
| Compile-time checks | `var _ outbox.Publisher = (*InMemoryEventBus)(nil)` and `Subscriber` |

**Missing test scenarios**:
- Buffer-full message drop (subscriber buffer is full, publish should log warning and not block) -- the `WithBufferSize` option is tested implicitly but the drop path is not exercised.
- Concurrent publish/subscribe stress test (race detector).
- Subscribe context cancellation during retry backoff (the `time.After` select in `handleWithRetry` line 207-210 covers this but is not tested explicitly).
- Unsubscribe / re-subscribe lifecycle (related to F02).

### worker test coverage

| Test case | What it covers |
|-----------|---------------|
| `TestWorkerGroup_StartStop` | Start 2 workers, cancel context, verify Canceled error |
| `TestWorkerGroup_StartError` | Worker returns immediate error |
| `TestWorkerGroup_Stop` | Stop directly (without Start) |
| `TestWorkerGroup_StopSerialReverseOrder` | Reverse-order stop verification |
| `TestPeriodicWorker_ExecutesFunction` | Function called >= 3 times |
| `TestPeriodicWorker_PanicIsolation` | Panic on 1st call, continues on subsequent |
| `TestPeriodicWorker_Stop` | Stop via `Stop()` method (not context) |

**Missing test scenarios**:
- `PeriodicWorker` compile-time interface check (F04).
- `PeriodicWorker` double-start behavior (F05).
- `WorkerGroup.Add` after `Start` (F07).
- `WorkerGroup` with zero workers (edge case -- `Start` should return immediately).
- Stop with context timeout (verify behavior when `Stop(ctx)` context expires).

---

## Architecture Notes

### Should Worker interface move to kernel/?

The `Worker` interface is currently in `runtime/worker`. It is consumed by `adapters/postgres/outbox_relay.go` and `runtime/bootstrap/bootstrap.go`. The kernel `outbox.Relay` interface already defines `Start(ctx) error` and `Stop(ctx) error` with the same signature as `Worker`.

**Assessment**: `Worker` is a generic runtime concern (background task lifecycle), not a domain/governance concept. Keeping it in `runtime/` is correct per the GoCell layering rules. The `outbox.Relay` interface in `kernel/` is domain-specific (outbox relay semantics). The fact that `OutboxRelay` implements both is appropriate -- it IS both a worker and a relay. No change needed.

### RunConfig

No `RunConfig` type exists in the codebase. The worker package uses a simple `Worker` interface + `WorkerGroup` pattern. The `PeriodicWorker` accepts its configuration (interval + function) via constructor parameters. This is clean and sufficient for the current scope.

### eventbus as test double vs. production component

The `InMemoryEventBus` is documented as "for development and testing" (doc.go). However, all three example applications (`sso-bff`, `todo-order`, `iot-device`) and the `cmd/core-bundle` main use it as their **only** event bus. There is no production event bus adapter (e.g., RabbitMQ, Kafka). This is noted but not a finding -- it is an expected gap for the current project stage.

---

## Findings Summary

| ID | Severity | Category | File | Description |
|----|----------|----------|------|-------------|
| R1C2-F01 | ~~P1~~ P2 | Thread safety | `eventbus.go:152-168` | Close/Publish RWMutex invariant undocumented (code is correct) |
| R1C2-F02 | **P1** | Resource leak | `eventbus.go:118-149` | Subscribe leaks subscription from subs map on exit |
| R1C2-F03 | **P1** | Lifecycle | `worker.go:47-72` | WorkerGroup.Start does not cancel siblings on first failure |
| R1C2-F04 | **P1** | Interface | `periodic.go` | PeriodicWorker missing compile-time Worker interface check |
| R1C2-F05 | **P1** | Lifecycle | `periodic.go:18-52` | PeriodicWorker not safe against double-Start |
| R1C2-F06 | **P1** | Retry | `eventbus.go:197-231` | handleWithRetry uses pure exponential backoff without jitter |
| R1C2-F07 | P2 | DX | `worker.go:38-42` | WorkerGroup.Add after Start silently ignored |
| R1C2-F08 | P2 | DX | `eventbus doc.go + eventbus.go` | Duplicate package doc comments |
| R1C2-F09 | P2 | DX | `worker doc.go + worker.go` | Duplicate package doc comments |
| R1C2-F10 | P2 | DX | `eventbus.go:182-186` | DeadLetterLen uses Mutex for read-only op |
| R1C2-F11 | P2 | DX | `worker.go:53-72` | Multiple worker errors lost (only first returned) |

**Totals**: 0 P0, 5 P1, 6 P2

---

## Highlights

1. **Clean dependency direction**: Both packages strictly follow the GoCell layering rules. `eventbus` depends only on `kernel/outbox` + `pkg/`. `worker` depends only on stdlib. No reverse dependencies.
2. **Good interface discipline**: Cells consume `outbox.Publisher` (kernel interface), not the concrete `InMemoryEventBus`. The `adapters/postgres/outbox_relay.go` correctly verifies both `outbox.Relay` and `worker.Worker` interface compliance at compile time.
3. **Solid test coverage**: Both packages have comprehensive test suites covering happy paths, error paths, concurrency, panic isolation, and idempotent close. The eventbus tests use proper synchronization (`sync.Mutex`, `atomic.Int32`, `Eventually`).
4. **Reference traceability**: Both packages cite their reference frameworks (Watermill for eventbus, go-zero for worker) in package-level comments with adopted/deviated notes, per project convention.
5. **Error handling**: `eventbus` uses `pkg/errcode` for all returned errors (`ErrBusClosed`). `worker` does not return domain errors (only propagates worker errors or `ctx.Err()`), which is appropriate.
