# R1B-2: kernel/outbox + kernel/idempotency Review

**Reviewer**: Kernel Guardian  
**Date**: 2026-04-06  
**Scope**: `kernel/outbox/` (3 files, ~65 LOC prod) + `kernel/idempotency/` (3 files, ~22 LOC prod)  
**Role**: Interface sufficiency for L2/L3 consistency semantics, dependency compliance, type safety

---

## 1. Executive Summary

Both packages are pure interface/type definitions with zero external dependencies -- the canonical kernel pattern. They compile cleanly and are consumed by 4+ adapter/cell packages. However, the review surfaces **2 P1 findings** (one confirmed prior TOCTOU, one new topic-routing ambiguity) and **3 P2 findings** (batch write gap, Entry validation gap, noop duplication).

| Severity | Count | Summary |
|----------|-------|---------|
| P0 | 0 | -- |
| P1 | 2 | Idempotency TOCTOU (confirmed), Entry topic/routing field gap |
| P2 | 3 | No batch Writer, Entry lacks validation contract, noop duplication |
| P3 | 2 | Minor naming, test coverage depth |

---

## 2. Dependency Compliance (GREEN)

### kernel/outbox

**Imports**: `context`, `time` -- standard library only.

Evidence: Lines 7-9 of `kernel/outbox/outbox.go`:
```go
import (
    "context"
    "time"
)
```

No imports of `runtime/`, `adapters/`, `cells/`, `pkg/`. Fully compliant with kernel isolation rule.

### kernel/idempotency

**Imports**: `context`, `time` -- standard library only.

Evidence: Lines 4-7 of `kernel/idempotency/idempotency.go`:
```go
import (
    "context"
    "time"
)
```

Fully compliant.

### kernel/cell -> kernel/outbox coupling

`kernel/cell/registrar.go` imports `kernel/outbox` for the `outbox.Subscriber` type in `EventRegistrar` interface. This is a kernel-to-kernel dependency (permitted). No upward dependencies detected.

**Verdict**: Dependency compliance is GREEN. Both packages satisfy the constraint "kernel/ only depends on standard library + pkg/".

---

## 3. Interface Sufficiency Analysis

### 3.1 outbox.Writer

**Current signature**:
```go
type Writer interface {
    Write(ctx context.Context, entry Entry) error
}
```

**Transaction context**: The doc comment states "extract tx from context via TxFromContext(ctx)". This is confirmed by `adapters/postgres/outbox_writer.go:34-36` which calls `TxFromContext(ctx)` and returns `ErrAdapterPGNoTx` if missing. The pattern works for the current single-DB setup.

**Assessment**: Adequate for current scope. The context-embedded tx pattern avoids coupling the kernel interface to a specific DB driver.

#### F-OB-01 [P2]: No batch write support

**Finding**: `Writer.Write` accepts a single `Entry`. All 7 call sites in cells (`sessionlogin`, `sessionlogout`, `identitymanage`, `auditappend`, `auditverify`, `configwrite`, `configpublish`) write exactly one entry per operation. However, as the framework matures, aggregate operations that emit multiple events (e.g., bulk config changes, batch identity provisioning) will need to write N outbox entries atomically within one transaction.

**Evidence**: All 7 call sites confirmed single-entry:
- `cells/access-core/slices/sessionlogin/service.go:167`
- `cells/access-core/slices/sessionlogout/service.go:102`
- `cells/access-core/slices/identitymanage/service.go:230`
- `cells/audit-core/slices/auditappend/service.go:123`
- `cells/audit-core/slices/auditverify/service.go:104`
- `cells/config-core/slices/configwrite/service.go:185`
- `cells/config-core/slices/configpublish/service.go:169`

**Impact**: Low today, medium when L3 workflow sagas or bulk operations are implemented.

**Recommendation**: Add `WriteBatch(ctx context.Context, entries []Entry) error` to the `Writer` interface when a concrete need arises (YAGNI for now -- track as tech debt). Default implementation can loop over `Write`.

---

### 3.2 outbox.Entry

**Current fields**:
```go
type Entry struct {
    ID            string
    AggregateID   string
    AggregateType string
    EventType     string
    Payload       []byte
    CreatedAt     time.Time
    Metadata      map[string]string
}
```

#### F-OB-02 [P1]: Entry lacks explicit Topic field -- EventType serves double duty

**Finding**: `Entry` has no `Topic` field. The relay (`adapters/postgres/outbox_relay.go:196`) uses `e.EventType` as the publish topic:
```go
r.pub.Publish(ctx, e.EventType, payload)
```

Meanwhile, all cell producers set `EventType` to topic constants (e.g., `TopicSessionCreated`, `TopicConfigChanged`). This conflates two distinct concepts:

1. **EventType**: The semantic type of the domain event (e.g., `session.created.v1`)
2. **Topic**: The message broker routing key (e.g., `gocell.access.session.created`)

Today these are 1:1, but they will diverge when:
- Multiple event types are multiplexed onto one topic (common in event-sourced aggregates)
- Topic naming follows broker conventions (e.g., `gocell.{cell}.{aggregate}.{event}`) while EventType follows domain conventions
- Event versioning introduces `session.created.v2` but the topic remains `session.created`

**Evidence**: The relay hardcodes `e.EventType` as topic at `outbox_relay.go:196`. The `Subscriber.Subscribe` takes an explicit `topic string` parameter, showing the framework already recognizes topic as a first-class concept on the consumer side.

**Impact**: Medium. When topic != EventType routing is needed, every relay consumer must be refactored.

**Recommendation**: Add an optional `Topic string` field to `Entry`. If empty, relay falls back to `EventType` for backward compatibility. This is a non-breaking, additive change.

---

#### F-OB-03 [P2]: Entry has no validation contract

**Finding**: All `Entry` fields are bare strings/bytes with no compile-time or runtime guarantees. `ID`, `AggregateID`, and `EventType` are semantically required but can be empty string. The `OutboxWriter` implementation inserts whatever it receives -- an Entry with empty ID would create a row with an empty primary key or violate a DB constraint at runtime.

**Evidence**: `adapters/postgres/outbox_writer.go:49-51` inserts `entry.ID` directly without nil/empty check. The DB schema likely has a NOT NULL constraint, but the interface contract is silent.

**Recommendation**: Add a `Validate() error` method or doc-comment contract specifying required fields. This is standard for kernel types that cross layer boundaries.

---

### 3.3 outbox.Relay

**Current signature**:
```go
type Relay interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}
```

**Assessment**: Adequate. The Relay is intentionally opaque -- polling interval, batch size, and retry/backoff are implementation concerns (configured via `RelayConfig` in `adapters/postgres`). The kernel interface correctly hides these details.

The postgres implementation (`adapters/postgres/outbox_relay.go`) provides:
- Configurable `PollInterval`, `BatchSize`, `RetentionPeriod`
- `FOR UPDATE SKIP LOCKED` for multi-instance safety
- Automatic cleanup of published entries
- Graceful shutdown via context cancellation + WaitGroup

No gaps identified in the Relay interface.

---

### 3.4 outbox.Publisher and outbox.Subscriber

**Publisher**: Simple `Publish(ctx, topic, payload) error` -- fire-and-forget semantics appropriate for the relay-to-broker handoff.

**Subscriber**: `Subscribe(ctx, topic, handler) error` + `Close() error`. The callback-based model aligns with GoCell's ConsumerBase pattern (documented deviation from Watermill's channel-based model).

**Assessment**: Both interfaces are adequate. The Subscriber correctly defines the handler signature as `func(context.Context, Entry) error` which enables the ConsumerBase to wrap it with idempotency/retry/DLQ logic.

---

### 3.5 idempotency.Checker

**Current signature**:
```go
type Checker interface {
    IsProcessed(ctx context.Context, key string) (bool, error)
    MarkProcessed(ctx context.Context, key string, ttl time.Duration) error
}
```

#### F-ID-01 [P1]: TOCTOU race between IsProcessed and MarkProcessed (CONFIRMED)

**Finding**: This is a confirmed prior finding (F-11P-01, P1-M3 in the findings ledger). The two-step pattern creates a race condition:

```
Consumer A: IsProcessed("key") -> false
Consumer B: IsProcessed("key") -> false   // race window
Consumer A: handler() -> success
Consumer A: MarkProcessed("key")
Consumer B: handler() -> success           // duplicate processing
Consumer B: MarkProcessed("key")           // no-op, but damage done
```

**Evidence from consumer_base.go:107-131**:
```go
processed, err := cb.checker.IsProcessed(ctx, idempotencyKey)  // line 107
// ... gap ...
if processed { return nil }
// ... handler executes ...
cb.checker.MarkProcessed(ctx, idempotencyKey, ...)              // line 129
```

The Redis implementation uses `SetNX` for `MarkProcessed` (which is atomic), but the check-then-act gap between `IsProcessed` (GET) and `MarkProcessed` (SETNX) is still vulnerable.

**Actual impact**: In the current ConsumerBase, the window is small because: (a) the same consumer group will not receive the same message concurrently under normal broker behavior, and (b) Redis GET + SETNX is fast. However, during rebalancing, crash recovery, or at-least-once redelivery, two consumers CAN process the same event ID concurrently.

**Recommended fix at interface level**: Add an atomic method to the `Checker` interface:

```go
// TryProcess atomically checks whether the key has been processed and,
// if not, marks it as processed with the given TTL. Returns true if the
// caller won the race (should process), false if already processed.
TryProcess(ctx context.Context, key string, ttl time.Duration) (bool, error)
```

The Redis adapter can implement this with a single `SET NX EX` command. The existing `IsProcessed` and `MarkProcessed` methods should be retained for backward compatibility and for use cases where the caller needs to query or explicitly mark after async processing.

**ConsumerBase change**: Replace the two-step pattern with:
```go
shouldProcess, err := cb.checker.TryProcess(ctx, idempotencyKey, cb.config.IdempotencyTTL)
if !shouldProcess { return nil }
// ... handler ...
// No MarkProcessed needed -- already marked atomically
```

**Status**: Previously identified as F-11P-01 / P1-M3, status UNKNOWN. This review confirms the finding and proposes the interface-level fix.

---

#### F-ID-02 [P3]: DefaultTTL constant is adequate but TTL=0 risk undocumented

**Finding**: `DefaultTTL = 24 * time.Hour` is defined and used as the fallback in `ConsumerBaseConfig.setDefaults()`. However, the interface allows `ttl=0` which in Redis means "no expiry" -- potentially causing memory leaks for high-volume consumers.

**Evidence**: `adapters/redis/idempotency.go:52-53` passes `ttl` directly to `SetNX` without floor check. This was also noted in P1-K10.

**Recommendation**: Add a doc-comment warning on `MarkProcessed` that `ttl <= 0` results in no expiry. Consider a minimum TTL floor in the Redis adapter (not the kernel interface).

---

## 4. Type Safety

### 4.1 Naming compliance

| Item | Check | Result |
|------|-------|--------|
| `Entry.ID` | Uses `ID` not `Id` | PASS |
| `AggregateID` | Correct Go acronym casing | PASS |
| `idempotency.Checker` | Clear, descriptive | PASS |
| `outbox.Writer` / `Relay` / `Publisher` / `Subscriber` | Distinct roles, no confusion | PASS |

### 4.2 Type confusion risk

`Entry.Payload` is `[]byte` -- callers are responsible for serialization. This is intentional (the kernel cannot assume JSON/protobuf/etc.) and consistent with Watermill's `Message.Payload []byte` pattern.

`Entry.Metadata` is `map[string]string` -- also consistent with Watermill. No type confusion risks identified.

---

## 5. Test Coverage Assessment

### kernel/outbox/outbox_test.go

The test file contains:
- Compile-time interface assertions for all 4 interfaces (Writer, Relay, Publisher, Subscriber) -- good
- `TestSubscriberInterface` -- exercises mock Subscribe/Close
- `TestEntryFields` -- verifies Entry struct can be constructed with all fields

**Gap (P1-L1 confirmed)**: These are pure compile-time checks and trivial field assertions. Since the package defines only interfaces (no behavior), there is no behavioral logic to cover. This is acceptable for a pure-interface package. The `coverage: [no statements]` output in CI is expected and correct.

### kernel/idempotency/idempotency_test.go

Same pattern: compile-time interface check + trivial mock test. Acceptable for the same reason.

---

## 6. Cross-Reference: Noop Duplication (P4-TD-01)

**Finding**: 5 separate `noopWriter` definitions exist across the codebase:

1. `examples/sso-bff/main.go:32`
2. `cells/access-core/cell_test.go:28`
3. `cells/config-core/cell_test.go:20`
4. `cells/audit-core/cell_test.go:21`
5. (Various test files with similar patterns)

This was previously tracked as P4-TD-01. The kernel package should provide `outbox.NoopWriter` and `idempotency.NoopChecker` to eliminate this duplication. These are zero-behavior types that belong in the kernel alongside the interface definitions.

---

## 7. Findings Summary

### P1 (Must Fix before v1.0)

| ID | Module | Finding | Recommendation |
|----|--------|---------|----------------|
| F-ID-01 | kernel/idempotency | TOCTOU between IsProcessed/MarkProcessed (confirmed F-11P-01) | Add `TryProcess(ctx, key, ttl) (bool, error)` atomic method to Checker interface |
| F-OB-02 | kernel/outbox | Entry lacks Topic field; EventType doubles as routing key | Add optional `Topic string` field to Entry; relay falls back to EventType if empty |

### P2 (Should Fix)

| ID | Module | Finding | Recommendation |
|----|--------|---------|----------------|
| F-OB-01 | kernel/outbox | No batch write support | Track as tech debt; add `WriteBatch` when concrete need arises |
| F-OB-03 | kernel/outbox | Entry has no validation contract for required fields | Add doc-comment contract or `Validate() error` method |
| P4-TD-01 | kernel/outbox + kernel/idempotency | 5+ noop implementations scattered across codebase | Provide `outbox.NoopWriter`, `idempotency.NoopChecker` in kernel |

### P3 (Nice to Have)

| ID | Module | Finding | Recommendation |
|----|--------|---------|----------------|
| F-ID-02 | kernel/idempotency | TTL=0 causes no-expiry keys (memory leak risk) | Doc-comment warning; adapter-level minimum TTL floor |
| P1-L1 | kernel/outbox | Pure interface package shows "no statements" coverage | Expected; no action needed |

---

## 8. Dependency Compliance Matrix

| Package | stdlib | pkg/ | kernel/ | runtime/ | adapters/ | cells/ | Verdict |
|---------|--------|------|---------|----------|-----------|--------|---------|
| kernel/outbox | context, time | -- | -- | -- | -- | -- | GREEN |
| kernel/idempotency | context, time | -- | -- | -- | -- | -- | GREEN |

---

## 9. Dimension Scores (for kernel/outbox + kernel/idempotency only)

| Dimension | Score | Evidence |
|-----------|-------|----------|
| Dependency compliance | GREEN | Zero non-stdlib imports confirmed |
| Interface sufficiency | YELLOW | TOCTOU gap in Checker; Topic field missing in Entry |
| Type safety | GREEN | Correct Go naming; clear type boundaries |
| Test coverage | GREEN | Pure-interface packages; compile-time checks adequate |
| Doc quality | GREEN | Watermill ref annotations; clear doc comments on all interfaces |

---

## 10. Relation to Prior Findings

| Prior Finding | Status in This Review |
|--------------|----------------------|
| F-11P-01 (TOCTOU) | CONFIRMED. Interface-level fix proposed (TryProcess) |
| P1-M3 (same) | CONFIRMED. Same as above |
| P1-L1 (no behavior coverage) | CONFIRMED ACCEPTABLE. Pure-interface package |
| P1-K10 (TTL=0 leak) | CONFIRMED. Doc-comment recommendation added |
| P4-TD-01 (noop duplication) | CONFIRMED. Recommend kernel-level noop types |
| P1-M2 (missing attempt_count/last_error) | OUT OF SCOPE (adapter schema, not kernel interface) |
