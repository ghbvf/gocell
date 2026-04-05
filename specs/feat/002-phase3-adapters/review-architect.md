# Architect Review -- Phase 3: Adapters Spec

> Reviewer: Architect Agent
> Date: 2026-04-05
> Input: spec.md, product-context.md, phase-charter.md
> Cross-referenced code: kernel/outbox, kernel/idempotency, kernel/cell, kernel/assembly, cells/*/internal/ports, runtime/bootstrap, runtime/eventbus, cmd/core-bundle/main.go

---

## Summary

The spec is well-structured and covers the 6 adapter modules with clear interface mappings. However, I identified 8 architecture issues ranging from a critical dependency inversion violation to design gaps in the outbox transaction model. The most impactful finding (ARCH-01) is that the spec's outbox.Writer design cannot fulfill its L2 consistency promise without a kernel-level transaction abstraction that does not yet exist.

---

## Findings

### ARCH-01: outbox.Writer 缺少事务上下文传递机制 -- L2 一致性承诺不可兑现

**Priority: P0**

**Problem:**

Spec FR-1.4 states: "Write(ctx, Entry) error must execute within TxManager transaction scope." The kernel interface at `/Users/shengming/Documents/code/gocell/src/kernel/outbox/outbox.go:26-28` is:

```go
type Writer interface {
    Write(ctx context.Context, entry Entry) error
}
```

However, there is no mechanism for the Writer to participate in the same database transaction as the business state write. The current Cell service code (e.g. `sessionlogin/service.go:135`) calls `publisher.Publish()` *after* the business write completes -- these are two separate operations with no transactional coupling.

For L2 OutboxFact to work, the outbox write and business write must be in the same PostgreSQL transaction. This requires one of:
1. A kernel-level `TxContext` type that carries a `pgx.Tx` through `context.Context`, or
2. A `UnitOfWork` / `TxRunner` interface in kernel/ that encapsulates "do business write + outbox write atomically."

Without this, `adapters/postgres/outbox_writer.go` has no way to share the transaction with the Cell Repository's business writes, and the L2 promise is structurally impossible to fulfill.

**Suggestion:**

Add a kernel-level transaction abstraction. The cleanest approach for GoCell's architecture:

```go
// kernel/tx/tx.go
type TxContext interface {
    RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}
```

Where `fn` receives a `context.Context` enriched with the active transaction. Both `ports.UserRepository` (business write) and `outbox.Writer` (outbox write) would extract the tx from the context. The `adapters/postgres/` package provides the concrete implementation.

This is how Watermill's `watermill-sql` works: the outbox publisher uses the same `*sql.Tx` as the business code via context propagation.

**Impact:** High -- without this, the entire L2 Outbox pattern is structurally broken. This is the spec's core architectural promise.

---

### ARCH-02: adapters/s3 import cells/ -- 分层违反

**Priority: P0**

**Problem:**

Spec section 4.1 and FR-4.4 declare:

```
cells/*/ports.ArchiveStore <-- adapters/s3/archive.go
```

This means `adapters/s3/archive.go` must import `cells/audit-core/internal/ports` to implement the `ArchiveStore` interface. The interface is defined at `/Users/shengming/Documents/code/gocell/src/cells/audit-core/internal/ports/archive_store.go` and references `cells/audit-core/internal/domain.AuditEntry`.

This violates two rules:
1. **NFR-1** in the spec itself: "adapters/ does not import cells/"
2. **CLAUDE.md dependency rule**: "adapters/ implements kernel/ or runtime/ defined interfaces"

The spec contradicts itself -- FR-4.4 requires a dependency that NFR-1 forbids.

**Suggestion:**

Move the `ArchiveStore` interface to kernel/ (e.g., `kernel/archive/archive.go`) with a generic payload type, or define a thin adapter-compatible interface in kernel/ that the Cell can satisfy via a wrapper. For example:

```go
// kernel/archive/archive.go
type Store interface {
    Archive(ctx context.Context, entries []json.RawMessage) error
}
```

Then `cells/audit-core` wraps its `domain.AuditEntry` into `json.RawMessage` before calling the kernel interface. `adapters/s3/` implements `kernel/archive.Store` without importing cells/.

Alternatively, keep `ArchiveStore` in `cells/audit-core/internal/ports/` and have the concrete S3 implementation live in `cells/audit-core/internal/adapters/` (not in top-level `adapters/s3/`), consistent with how spec section 5.2 says "concrete Repository implementation by Cell internal `internal/adapters/` sub-package."

**Impact:** High -- compile failure or architectural violation. Must resolve before implementation.

---

### ARCH-03: Outbox Relay 与 RabbitMQ Publisher 的耦合路径未明确

**Priority: P1**

**Problem:**

Spec section 4.2 shows:

```go
outboxRelay := postgres.NewOutboxRelay(pool, rabbitPublisher)
```

This means `adapters/postgres/outbox_relay.go` takes a `rabbitPublisher` (concrete type from `adapters/rabbitmq/`) as a constructor argument. If the relay takes the concrete type, this creates a compile-time coupling between two adapter packages (`adapters/postgres` imports `adapters/rabbitmq`), which is an undesirable horizontal dependency within the same layer.

The kernel interface `outbox.Publisher` exists at `/Users/shengming/Documents/code/gocell/src/kernel/outbox/outbox.go:37-39` and should be the injection point, not the concrete type.

**Suggestion:**

Ensure the constructor signature is:

```go
func NewOutboxRelay(pool *Pool, publisher outbox.Publisher, opts ...RelayOption) *OutboxRelay
```

The relay depends on the kernel `outbox.Publisher` interface, not the concrete RabbitMQ implementation. This is likely the intent but the spec should state it explicitly to prevent implementors from taking a shortcut. Add a sentence to FR-1.5: "Relay accepts `outbox.Publisher` (kernel interface), not a concrete adapter type."

**Impact:** Medium -- easy to get wrong during implementation, causing adapter-to-adapter coupling.

---

### ARCH-04: bootstrap.go 硬绑定 InMemoryEventBus 具体类型 -- Phase 3 无法注入 RabbitMQ

**Priority: P1**

**Problem:**

At `/Users/shengming/Documents/code/gocell/src/runtime/bootstrap/bootstrap.go:64`, the `WithEventBus` option accepts only the concrete type:

```go
func WithEventBus(eb *eventbus.InMemoryEventBus) Option {
```

And at line 99, the struct field is also concrete:

```go
eventBus *eventbus.InMemoryEventBus
```

Phase 3 needs to inject a RabbitMQ-backed publisher/subscriber instead of the in-memory eventbus. The current bootstrap cannot accept `adapters/rabbitmq.Publisher` because it expects the concrete InMemoryEventBus type.

The spec does not address this migration path. When the RabbitMQ adapter is wired in, either:
- bootstrap must accept `outbox.Publisher` + `outbox.Subscriber` interfaces, or
- the Cell registration/subscription flow must be refactored.

**Suggestion:**

Add to the spec a requirement to refactor `runtime/bootstrap`:

1. Change `WithEventBus` to accept interface types: `WithPublisher(outbox.Publisher)` + `WithSubscriber(outbox.Subscriber)`.
2. Keep `WithEventBus(*eventbus.InMemoryEventBus)` as a convenience that sets both.
3. The `RegisterSubscriptions` call at bootstrap line 229 already passes the eventbus as `outbox.Subscriber`, so the interface boundary is correct at the consumption side -- only the injection side needs fixing.

This is a breaking change to `runtime/bootstrap`'s public API. The spec should acknowledge it under "Phase 2 code modification scope."

**Impact:** Medium -- blocks RabbitMQ integration with the bootstrap lifecycle. Without this fix, the real adapter can only be wired manually, bypassing bootstrap's shutdown ordering.

---

### ARCH-05: Cell Repository 实现归属不清晰 -- adapters/ vs cells/internal/adapters/

**Priority: P1**

**Problem:**

The spec states two contradictory positions:

1. FR-1.6: "Does not implement concrete Cell Repository (each Cell implements its own)"
2. Section 5.2: "Concrete Repository implementation by Cell internal `internal/adapters/` sub-package (or in Phase 4)"

This is architecturally correct but creates a Phase 3 gap: the 6 Cell repository interfaces (UserRepository, SessionRepository, RoleRepository, AuditRepository, ConfigRepository, FlagRepository) will have no PostgreSQL implementation after Phase 3 completes. Cells will still use in-memory implementations.

This means the "L2 consistency" outbox full-chain test (FR-8.2: "business write + outbox write in same transaction") cannot be verified, because the business write side has no PostgreSQL implementation to participate in the transaction.

**Suggestion:**

Either:
1. **Implement at least one Cell Repository** (e.g., `cells/config-core/internal/adapters/pg_config_repo.go`) in Phase 3 as a proof-of-concept to validate the transaction propagation design from ARCH-01. This would be the minimum viable demonstration of L2 consistency.
2. Or explicitly acknowledge in the spec that FR-8.2 (outbox full-chain test) will use a **test-only stub repository** that participates in the PostgreSQL transaction, and document this as a known limitation.

Without either option, the core value proposition of Phase 3 ("prove L2 consistency works end-to-end") cannot be delivered.

**Impact:** Medium -- undermines the primary success criterion S2 ("outbox full-chain end-to-end verification").

---

### ARCH-06: Outbox Relay 轮询效率与并发控制未规定

**Priority: P1**

**Problem:**

FR-1.5 says the Relay "polls unpublished entries, supports batch fetch, interval config, error retry" but does not specify:

1. **Polling strategy**: Simple `SELECT ... WHERE published = false ORDER BY created_at LIMIT N FOR UPDATE SKIP LOCKED`? Or advisory locks? The `FOR UPDATE SKIP LOCKED` pattern is critical for multi-instance relay deployments to avoid double-publishing.
2. **Batch size default**: No default specified. Too small = high DB overhead. Too large = high memory and long transaction hold time.
3. **Single-instance vs multi-instance**: The spec does not specify whether multiple relay instances can run concurrently. If not, the Redis `DistLock` (FR-2.2) should be mentioned as a coordination mechanism. If yes, `SKIP LOCKED` is mandatory.
4. **Published entry cleanup**: No mention of how published entries are cleaned up (TTL-based deletion? Separate cleanup worker?). Without cleanup, the outbox table grows unbounded.

The Watermill reference (`watermill-sql`) uses `SELECT ... FOR UPDATE SKIP LOCKED` and allows concurrent pollers. The spec should explicitly choose a strategy.

**Suggestion:**

Add to FR-1.5:
- Default polling interval: 1 second (configurable via `RelayConfig.PollInterval`).
- Default batch size: 100 (configurable via `RelayConfig.BatchSize`).
- Concurrency strategy: `SELECT ... FOR UPDATE SKIP LOCKED` to support multiple relay instances.
- Published entry retention: configurable TTL; default 72 hours; cleanup via a separate `PeriodicWorker`.
- The relay should integrate with `runtime/worker.Worker` interface so it can be registered with `bootstrap.WithWorkers()`.

**Impact:** Medium -- affects production reliability and the outbox table's long-term storage growth.

---

### ARCH-07: List/Query 接口缺少分页 -- PostgreSQL 实现后有全表扫描风险

**Priority: P1**

**Problem:**

The following Cell port interfaces return unbounded lists:

- `/Users/shengming/Documents/code/gocell/src/cells/config-core/internal/ports/config_repo.go:16`: `List(ctx context.Context) ([]*domain.ConfigEntry, error)`
- `/Users/shengming/Documents/code/gocell/src/cells/config-core/internal/ports/flag_repo.go:14`: `List(ctx context.Context) ([]*domain.FeatureFlag, error)`
- `/Users/shengming/Documents/code/gocell/src/cells/audit-core/internal/ports/audit_repo.go:22`: `GetRange(ctx context.Context, from, to int) ([]*domain.AuditEntry, error)` -- range-based but `to` can be arbitrarily large
- `/Users/shengming/Documents/code/gocell/src/cells/audit-core/internal/ports/audit_repo.go:23`: `Query(ctx context.Context, filters AuditFilters) ([]*domain.AuditEntry, error)` -- no limit/offset

With in-memory storage these are harmless, but once PostgreSQL is the backend, these become full table scans with unbounded result sets. The audit table especially can grow very large.

This is not strictly a Phase 3 adapter spec issue (the interfaces belong to Phase 2 cells), but Phase 3 should acknowledge it since it sets up the PostgreSQL infrastructure that these interfaces will run against.

**Suggestion:**

Add to FR-10.2 (Architecture Fixes) or create a new FR:
- Add `Pagination` struct to kernel/ (e.g., `kernel/query/pagination.go`): `type Pagination struct { Limit int; Offset int }`.
- Update Cell port interfaces to accept pagination: `List(ctx, Pagination) ([]*T, int, error)` where `int` is total count.
- Update `AuditFilters` to include `Limit` and `Offset` fields.
- Set a system-wide max page size (e.g., 100) enforced at the handler layer.

If this is too large for Phase 3, explicitly DEFER it and document a hardcoded `LIMIT 1000` safety net in the PostgreSQL Repository implementations as an interim measure.

**Impact:** Medium -- not a blocker for Phase 3 but becomes a production risk as soon as the PostgreSQL adapter is used with real data.

---

### ARCH-08: NFR-8 关闭顺序与 Outbox Relay 生命周期存在 race

**Priority: P2**

**Problem:**

NFR-8 specifies shutdown order: "Stop Subscriber first, then Publisher, then connection pool." But the Outbox Relay is a special case: it reads from PostgreSQL (connection pool) and writes to RabbitMQ (Publisher). If the Publisher is stopped before the Relay finishes its current batch, in-flight outbox entries will fail to publish and remain in the "unpublished" state (which is safe for at-least-once semantics, but will generate spurious error logs).

More critically, the Relay's `Stop()` should drain its current batch before the PostgreSQL pool closes. The spec does not establish where the Relay sits in the shutdown sequence.

**Suggestion:**

Clarify the full shutdown order in NFR-8:

1. Stop Subscribers (stop accepting new messages from RabbitMQ)
2. Stop Relay (drain current batch, stop polling)
3. Stop Publisher (flush pending confirms)
4. Close RabbitMQ connection
5. Close PostgreSQL pool
6. Close Redis client

The Relay should implement `worker.Worker` interface and be registered via `bootstrap.WithWorkers()`, which ensures it participates in the bootstrap shutdown sequence. Add this to FR-1.5.

**Impact:** Low -- at-least-once semantics mask the issue, but spurious error logs during shutdown degrade operational clarity.

---

## Summary Table

| ID | Title | Priority | Dimension |
|----|-------|----------|-----------|
| ARCH-01 | outbox.Writer 缺少事务上下文传递机制 | P0 | Consistency Level / Interface Stability |
| ARCH-02 | adapters/s3 import cells/ 分层违反 | P0 | Dependency Direction |
| ARCH-03 | Outbox Relay 与 RabbitMQ Publisher 耦合路径 | P1 | Module Coupling |
| ARCH-04 | bootstrap.go 硬绑定 InMemoryEventBus | P1 | Interface Stability / Compatibility |
| ARCH-05 | Cell Repository 实现归属不清晰 | P1 | Cell Aggregate Boundary |
| ARCH-06 | Outbox Relay 轮询效率与并发控制 | P1 | Performance / Scalability |
| ARCH-07 | List/Query 接口缺少分页 | P1 | Performance / Scalability |
| ARCH-08 | 关闭顺序与 Outbox Relay 生命周期 race | P2 | Lifecycle / Consistency |

**Verdict:** 2 P0 findings (ARCH-01, ARCH-02) must be resolved before implementation begins. ARCH-01 requires a kernel-level design decision (transaction propagation pattern) that affects the entire adapter layer. ARCH-02 requires either moving an interface to kernel/ or relocating the S3 archive implementation to within the Cell.
