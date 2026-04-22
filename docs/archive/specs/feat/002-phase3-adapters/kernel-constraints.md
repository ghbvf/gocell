# Kernel Constraints Report -- Phase 3: Adapters

> Role: Kernel Guardian
> Date: 2026-04-05
> Input: spec.md, product-context.md, phase-charter.md, kernel source code

**审查维度**: 集成风险评估、分层隔离验证、核心约束清单、元数据合规检查、契约完整性守护
> Branch: `feat/002-phase3-adapters`

---

## (a) Modification Suggestions (10 items)

### KS-01: outbox.Writer lacks transaction context -- kernel interface gap [HIGH]

**Current state**: `outbox.Writer.Write(ctx context.Context, entry Entry) error` accepts a plain `context.Context`. The spec (FR-1.4) requires "Write must execute within TxManager transaction scope", meaning the postgres adapter's TxManager must pass a transaction handle through to the Writer.

**Problem**: The kernel interface has no mechanism to convey a database transaction. The adapter will need to extract a `pgx.Tx` from the context (context-embedded transaction pattern) or take a `pgx.Tx` as a constructor parameter. The former is a common Go pattern (`TxFromContext(ctx)`) but the kernel interface doc does not mandate it. The latter ties the Writer to a specific transaction instance.

**Suggestion**: Add a doc comment to `outbox.Writer.Write` explicitly endorsing the context-embedded transaction pattern: "Implementations that require transactional guarantees SHOULD use a context-embedded transaction (e.g., `TxFromContext(ctx)`) to participate in the caller's transaction scope." This is a documentation-only change to `kernel/outbox/outbox.go` -- no signature change needed. The adapter will provide `TxManager.RunInTx(ctx, func(tx) { ... })` which stores the tx in context, and `OutboxWriter.Write` extracts it. This pattern must be documented in kernel to make the contract explicit.

### KS-02: outbox.Publisher.Publish lacks Entry metadata -- information loss [MEDIUM]

**Current state**: `Publisher.Publish(ctx context.Context, topic string, payload []byte) error` accepts raw bytes. The `Relay` adapter will read full `outbox.Entry` records (with `ID`, `AggregateID`, `AggregateType`, `EventType`, `Metadata`) from the database, but can only pass `topic` + `payload` to the Publisher.

**Problem**: The relay loses `Entry.Metadata`, `Entry.AggregateID`, `Entry.AggregateType`, and `Entry.ID` when calling `Publisher.Publish`. RabbitMQ message headers (for idempotency, tracing, routing) need these fields.

**Suggestion**: Consider evolving `Publisher` to `Publish(ctx, topic string, entry Entry) error` OR `Publish(ctx, topic string, payload []byte, metadata map[string]string) error`. This is a kernel interface change. If the change is rejected for stability, the adapter Relay must serialize all Entry fields into the payload bytes before calling Publish, and the Subscriber must deserialize them -- which makes the raw `[]byte` payload a de-facto `Entry` wire format. Either way, a design decision is needed before implementation.

### KS-03: outbox.Subscriber handler receives Entry but Cells only get payload [MEDIUM]

**Current state**: `Subscriber.Subscribe` handler signature is `func(context.Context, Entry) error`. The `InMemoryEventBus.Publish` constructs an `Entry` from `(topic, payload)` with a generated ID but empty `AggregateID`/`AggregateType`/`Metadata`. When switching to RabbitMQ, the subscriber will construct `Entry` from AMQP message headers + body, populating all fields.

**Problem**: Cell consumers (e.g., `auditappend.Service.HandleEvent`) currently receive `Entry` but only use `Entry.Payload` and `Entry.EventType`. The switch to RabbitMQ is transparent at the interface level, but consumers should use `Entry.ID` for idempotency keys. Currently, the `InMemoryEventBus` generates synthetic IDs that are never checked.

**Suggestion**: No kernel interface change needed, but document in `outbox.Entry` that `ID` is the canonical idempotency identifier. Phase 3 implementation should ensure the RabbitMQ subscriber populates `Entry.ID` from the AMQP message's `MessageId` header (or from `Entry.Metadata["event_id"]`). This alignment ensures consumers can switch from in-memory to RabbitMQ without code changes.

### KS-04: cell.Dependencies struct insufficient for adapter injection [HIGH]

**Current state**: `cell.Dependencies` has three fields:
```go
type Dependencies struct {
    Cells     map[string]Cell
    Contracts map[string]Contract
    Config    map[string]any
}
```

Cells currently receive adapters (Publisher, Repositories) via constructor Option functions (e.g., `WithPublisher(eb)`, `WithUserRepository(repo)`), NOT through `Dependencies`. The `Dependencies.Config` map is only used for scalar config values (`access.signing_key`, `audit.hmac_key`).

**Problem**: The spec (section 4.2) shows adapter instances created in `cmd/core-bundle/main.go` and injected via Options. This pattern works but bypasses `Dependencies` entirely. If Phase 3 introduces additional adapters that Cells need (e.g., `outbox.Writer` for L2 transactional writes), new Option functions must be added to each Cell.

**Suggestion**: This is acceptable for Phase 3 -- the Option pattern is idiomatic Go and maintains type safety. Do NOT add adapter instances to `Dependencies.Config` as `map[string]any` (loses type safety and breaks interface compliance checks). Instead, each Cell that needs a new adapter should add a typed `With*` Option. Example: `accesscore.WithOutboxWriter(w outbox.Writer)`. The `Dependencies` struct itself does not need modification in Phase 3. Record as a [TECH] item for Phase 4 evaluation: whether to introduce a typed `Dependencies.Adapters` field or maintain the Option-per-adapter pattern.

### KS-05: Cells use Publisher.Publish directly -- not through outbox.Writer [HIGH]

**Current state**: All three Cells (`access-core`, `audit-core`, `config-core`) call `s.publisher.Publish(ctx, topic, payload)` directly in their service methods. They do NOT use `outbox.Writer`. Example from `configwrite/service.go:130`:
```go
if err := s.publisher.Publish(ctx, TopicConfigChanged, payload); err != nil { ... }
```

**Problem**: This is fire-and-forget publishing, not transactional outbox. For L2 consistency, the correct flow is: (1) write business state to DB, (2) write outbox entry in same transaction, (3) relay publishes asynchronously. Phase 3 must refactor these call sites to use `outbox.Writer.Write()` within a TxManager transaction, and let the Relay handle actual publishing.

**Suggestion**: This is a Cell-level refactoring, not a kernel change. Each Cell service that publishes events (7 call sites across 3 Cells) must be modified to:
1. Accept an `outbox.Writer` (in addition to or replacing `outbox.Publisher`)
2. Call `writer.Write(ctx, entry)` inside the transaction instead of `publisher.Publish(ctx, topic, payload)`
3. The Relay reads unpublished entries and calls `publisher.Publish`

This is the ARCH-07 tech-debt item from Phase 2 ("L2 events should use outbox transaction"). It is correctly listed in phase-charter.md (P1-Should). The kernel interface supports this -- no kernel change needed, but Cell refactoring is required.

### KS-06: Bootstrap couples to concrete InMemoryEventBus type [MEDIUM]

**Current state**: `bootstrap.Bootstrap` has field `eventBus *eventbus.InMemoryEventBus` and `WithEventBus(eb *eventbus.InMemoryEventBus) Option`. The `RegisterSubscriptions` call in `bootstrap.Run` line 229 passes `eb` (concrete type) to `er.RegisterSubscriptions(eb)`.

**Problem**: When switching to RabbitMQ, the bootstrap must accept an `outbox.Subscriber` interface instead of the concrete `*InMemoryEventBus`. The current code will not compile if you pass a `rabbitmq.Subscriber` to `WithEventBus`.

**Suggestion**: Refactor `bootstrap.Bootstrap` to use `outbox.Publisher` and `outbox.Subscriber` interfaces instead of `*eventbus.InMemoryEventBus`. This is a runtime/ change (not kernel/), required before the RabbitMQ adapter can be wired. Alternatively, the assembly layer (`cmd/core-bundle/main.go`) can call `RegisterSubscriptions` directly (bypassing bootstrap), but that duplicates lifecycle orchestration logic.

### KS-07: outbox.Relay Start/Stop aligns with worker.Worker interface [LOW]

**Current state**: `outbox.Relay` interface: `Start(ctx) error` + `Stop(ctx) error`. `worker.Worker` interface: `Start(ctx) error` + `Stop(ctx) error`. They have identical signatures.

**Problem**: None. This is a positive observation.

**Suggestion**: The postgres `OutboxRelay` should implement both `outbox.Relay` and `worker.Worker` interfaces (they are structurally identical). This allows the relay to be registered with `bootstrap.WithWorkers(relay)` for automatic lifecycle management. Document this pattern in the adapter's godoc.

### KS-08: kernel/ code itself needs minimal modification [LOW]

**Current state**: After reviewing all kernel packages, no Go code changes are needed for Phase 3 adapter implementation. The interfaces (`outbox.Writer`, `outbox.Relay`, `outbox.Publisher`, `outbox.Subscriber`, `idempotency.Checker`) are sufficient.

**Suggestion**: The only kernel modifications recommended are:
1. Documentation enhancement to `outbox.Writer.Write` (KS-01, doc-only)
2. Documentation enhancement to `outbox.Entry.ID` (KS-03, doc-only)
3. Potential `Publisher.Publish` signature evolution (KS-02, design decision needed)

If KS-02 is accepted, this is the ONLY kernel signature change in Phase 3. All other work is in adapters/, cells/, runtime/, and cmd/.

### KS-09: Subscriber.Close() lacks context parameter -- asymmetric with Relay [LOW]

**Current state**: `outbox.Subscriber.Close() error` has no `context.Context` parameter. `outbox.Relay.Stop(ctx context.Context) error` does. `worker.Worker.Stop(ctx context.Context) error` does.

**Problem**: RabbitMQ subscriber shutdown may need a timeout (draining in-flight messages). Without a context, the implementation must use internal timeouts, which are not configurable by the caller.

**Suggestion**: Consider changing to `Close(ctx context.Context) error` for consistency with the rest of the lifecycle interfaces. This is a kernel interface change. If rejected, the RabbitMQ subscriber can accept a shutdown timeout in its Config struct. Decision needed before implementation.

### KS-10: No kernel interface for ArchiveStore -- defined in cells/ [LOW]

**Current state**: `ports.ArchiveStore` is defined in `cells/audit-core/internal/ports/`, not in kernel/. The spec (FR-4.4) has `adapters/s3/archive.go` implementing `cells/audit-core/internal/ports.ArchiveStore`.

**Problem**: This creates an import from adapters/ to cells/, violating the layering rule: "adapters/ does not import cells/".

**Suggestion**: Either (A) move `ArchiveStore` interface to `kernel/` (e.g., `kernel/archive/archive.go`), or (B) the S3 adapter provides a generic `ObjectStore` interface and the Cell maps it to `ArchiveStore` internally. Option (B) is preferred -- the S3 adapter's `Upload/Download/Delete` methods already cover the ArchiveStore semantics. The Cell's `internal/adapters/` layer can wrap the S3 client into an ArchiveStore implementation without the S3 adapter importing cells/. This is correctly noted in spec section 5.2 ("Phase 3 provides adapters/postgres/ infrastructure... specific Repository implementations by Cell internal/adapters/"), but FR-4.4 contradicts this by saying the S3 adapter directly implements `ports.ArchiveStore`.

---

## (b) Integration Risk Assessment

### RISK-01: Outbox Writer transaction binding [HIGH]

**Question**: Can `kernel/outbox.Writer.Write(ctx, entry)` participate in a PostgreSQL transaction managed by `TxManager.RunInTx`?

**Assessment**: YES, but requires a design convention. The `context.Context` parameter can carry a `pgx.Tx` via `context.WithValue`. The `postgres.OutboxWriter` extracts the tx from context. This is a well-established Go pattern (see `database/sql` standard library). The kernel interface supports it without modification.

**Risk**: If the developer forgets to wrap the `Write` call inside `RunInTx`, the outbox write occurs outside a transaction, violating L2 guarantees. There is no compile-time enforcement.

**Mitigation**: (1) Document the contract requirement in `outbox.Writer` godoc. (2) Add a runtime check in `postgres.OutboxWriter.Write`: if no tx is found in context, return `ERR_ADAPTER_NO_TX` error (fail-fast). (3) Integration test must verify the atomic commit/rollback behavior.

### RISK-02: Relay-Publisher cross-adapter collaboration [MEDIUM]

**Question**: Can `postgres.OutboxRelay` call `rabbitmq.Publisher.Publish`?

**Assessment**: YES. The relay accepts `outbox.Publisher` (kernel interface) in its constructor. The rabbitmq publisher implements `outbox.Publisher`. No import from `adapters/postgres` to `adapters/rabbitmq` is needed -- both depend only on `kernel/outbox`.

```
postgres.OutboxRelay --uses--> outbox.Publisher (interface)
rabbitmq.Publisher --implements--> outbox.Publisher (interface)
cmd/core-bundle/main.go: relay := postgres.NewOutboxRelay(pool, rabbitPublisher)
```

**Risk**: If `Publisher.Publish` fails (RabbitMQ down), the relay must NOT mark the outbox entry as published. The relay's error handling must be transactional: poll -> publish -> mark published, with mark-published only on publish success.

**Mitigation**: Standard outbox relay pattern. The relay polls entries with status `pending`, attempts publish, marks `published` only on success. On failure, the entry remains `pending` for the next poll cycle. The relay's poll interval and max-retry configuration handle transient failures.

### RISK-03: Cell.Init Dependencies -- adapter instance passing [LOW]

**Question**: Does `cell.Dependencies` need to be modified to pass adapter instances?

**Assessment**: NO. Adapter instances are injected via Cell constructor Options (`WithPublisher`, `WithUserRepository`, etc.), not through `Dependencies`. The `Dependencies` struct passes cross-Cell references and config scalars. This pattern is already established in Phase 2 and works correctly.

**Risk**: As the number of adapters grows, each Cell accumulates more `With*` Option functions. This is manageable for Phase 3 (adding `WithOutboxWriter` to 3 Cells) but may become verbose in later phases.

**Mitigation**: Acceptable for Phase 3. Consider a `cell.Infra` struct for Phase 4+ if adapter count exceeds 5-6 per Cell.

### RISK-04: EventRegistrar.RegisterSubscriptions switching to RabbitMQ [MEDIUM]

**Question**: Can the existing `RegisterSubscriptions(sub outbox.Subscriber)` work with a RabbitMQ subscriber?

**Assessment**: YES at the interface level. The `outbox.Subscriber` interface is implementation-agnostic. However, `bootstrap.Bootstrap.WithEventBus` currently takes `*eventbus.InMemoryEventBus` (concrete type), which prevents passing a `rabbitmq.Subscriber`.

**Risk**: `bootstrap.go` line 64 (`WithEventBus(eb *eventbus.InMemoryEventBus)`) must be refactored to accept `outbox.Subscriber` (and separately `outbox.Publisher`). This is a runtime/ change that touches the bootstrap startup sequence.

**Mitigation**: (1) Add `WithSubscriber(outbox.Subscriber)` and `WithPublisher(outbox.Publisher)` options to bootstrap. (2) Deprecate `WithEventBus` or make it a convenience that sets both. (3) The bootstrap teardown sequence must close the subscriber before the publisher (matching NFR-8 shutdown order). This change must happen early in Phase 3 (Wave 0 or Wave 1) as all adapter wiring depends on it.

---

## (c) Phase 3 Kernel Constraint Verification Checklist

### Layering Isolation

| ID | Constraint | Verification Method |
|----|-----------|-------------------|
| C-01 | adapters/ only imports kernel/ + runtime/ + pkg/ + external deps | `go build ./...` + grep imports in adapters/**/*.go for cells/ imports |
| C-02 | adapters/ does NOT import cells/ | grep `"github.com/ghbvf/gocell/cells` in adapters/**/*.go -- must return 0 matches |
| C-03 | kernel/ does NOT import adapters/ | grep `"github.com/ghbvf/gocell/adapters` in kernel/**/*.go -- must return 0 matches (currently verified: 0 matches) |
| C-04 | runtime/ does NOT import adapters/ | grep `"github.com/ghbvf/gocell/adapters` in runtime/**/*.go -- must return 0 matches (currently verified: 0 matches) |
| C-05 | kernel/ does NOT import runtime/ | grep `"github.com/ghbvf/gocell/runtime` in kernel/**/*.go -- must return 0 matches |

### Interface Compliance

| ID | Constraint | Verification Method |
|----|-----------|-------------------|
| C-06 | postgres.OutboxWriter implements outbox.Writer | `var _ outbox.Writer = (*OutboxWriter)(nil)` compile check |
| C-07 | postgres.OutboxRelay implements outbox.Relay | `var _ outbox.Relay = (*OutboxRelay)(nil)` compile check |
| C-08 | rabbitmq.Publisher implements outbox.Publisher | `var _ outbox.Publisher = (*Publisher)(nil)` compile check |
| C-09 | rabbitmq.Subscriber implements outbox.Subscriber | `var _ outbox.Subscriber = (*Subscriber)(nil)` compile check |
| C-10 | redis.IdempotencyChecker implements idempotency.Checker | `var _ idempotency.Checker = (*IdempotencyChecker)(nil)` compile check |
| C-11 | No adapter extends kernel interface signatures | Review: adapter methods superset of kernel interface; no kernel method modified |

### Lifecycle

| ID | Constraint | Verification Method |
|----|-----------|-------------------|
| C-12 | Every adapter provides Close(ctx) error | Code review: each adapter struct has Close method |
| C-13 | Shutdown order: Subscriber -> Publisher -> connection pool | Integration test: verify no publish-after-close or consume-after-close errors |
| C-14 | OutboxRelay implements worker.Worker for bootstrap integration | `var _ worker.Worker = (*OutboxRelay)(nil)` or equivalent lifecycle management |
| C-15 | Assembly.Start/Stop unchanged -- no regression | `go test ./kernel/assembly/...` passes with >= 90% coverage |

### Metadata Integrity

| ID | Constraint | Verification Method |
|----|-----------|-------------------|
| C-16 | No new cells/ added -- existing cell.yaml unchanged | diff check: cells/*/cell.yaml identical to Phase 2 |
| C-17 | No new contracts required for adapter layer | Adapters implement kernel interfaces, not contract-based communication |
| C-18 | go.mod new dependencies limited to 5 whitelist entries | `go.mod` diff: only pgx/v5, go-redis/v9, amqp091-go, nhooyr.io/websocket, testcontainers-go added as direct deps |

### Error Handling

| ID | Constraint | Verification Method |
|----|-----------|-------------------|
| C-19 | Adapter errors use pkg/errcode with ERR_ADAPTER_* prefix | grep `errcode.New\|errcode.Wrap` in adapters/**/*.go; no bare `errors.New` |
| C-20 | Driver-level errors are wrapped, not exposed | grep `pgx\.\|redis\.\|amqp\.` in error returns; must be wrapped |

### Consistency Level

| ID | Constraint | Verification Method |
|----|-----------|-------------------|
| C-21 | L2 Cell operations (session-login, session-logout, config-write, config-publish) use outbox.Writer in transaction | Code review: these service methods call writer.Write inside TxManager.RunInTx |
| C-22 | L3 operations (audit-append) use Relay-based async publishing | Code review: audit subscription consumes from Subscriber, writes use Writer |

### Kernel Code Stability

| ID | Constraint | Verification Method |
|----|-----------|-------------------|
| C-23 | kernel/ test coverage >= 90% after Phase 3 | `go test -cover ./kernel/...` |
| C-24 | kernel/ zero new go vet warnings | `go vet ./kernel/...` |
| C-25 | No deprecated field names introduced | grep for cellId/sliceId/contractId/assemblyId/ownedSlices/authoritativeData/producer/consumers in new code |

---

## (d) Workflow Executability Assessment

### Can this spec walk through S0-S8 (all 9 stages)?

**Overall verdict**: YES with caveats. The spec is executable across all 9 stages, but 3 stages have identified friction points.

### Stage-by-Stage Assessment

| Stage | Assessment | Notes |
|-------|-----------|-------|
| S0 Context | PASS | product-context.md and phase-charter.md are complete. 4 personas defined. 12 success criteria quantified. |
| S1 Spec | PASS | spec.md covers FR-1 through FR-14, NFR-1 through NFR-8. Interface list complete. Architecture constraints documented. |
| S2 Checklist | PASS | Derivable from FR/NFR items. ~60 tasks across 6 adapters + tech-debt + security + testing. |
| S3 Arch | PASS with note | Dependency direction is clear (section 4.1). Injection point is clear (section 4.2). Missing: sequence diagram for outbox full-chain (Writer -> Relay -> Publisher -> Subscriber -> Consumer). |
| S4 Design | PASS | Interfaces are defined in kernel/. Adapter struct designs follow Go conventions. |
| S5 Implement | **FRICTION** | `go build ./...` will fail until external dependencies are added to go.mod (`go get pgx/v5 go-redis/v9 amqp091-go nhooyr.io/websocket`). This requires network access during implementation. Testcontainers requires Docker daemon. Both are infrastructure prerequisites that must be resolved before S5 starts. |
| S6 Verify | **FRICTION** | Unit tests can run without infrastructure. Integration tests (`//go:build integration`) require Docker for testcontainers or `docker compose up`. CI environment must have Docker. If Docker is unavailable, only unit tests pass -- integration test coverage gap. The spec correctly mitigates this (FR-8 build tag, Risk section). |
| S7 Journey Test | **FRICTION** | J-audit-login-trail and J-config-hot-reload require the full stack (PostgreSQL + Redis + RabbitMQ) running. Docker Compose is the enabler. If Docker Compose services fail to start within 30s (FR-7.2), journey tests cannot execute. |
| S8 Gate | PASS | Gate command is defined: `docker compose up -d && go test ./adapters/... -tags=integration`. Pass criteria are the 12 success criteria from product-context.md. |

### Potential Blockers

**Blocker 1: Bootstrap refactoring must precede adapter wiring (KS-06/RISK-04)**

The current `bootstrap.WithEventBus(*eventbus.InMemoryEventBus)` accepts a concrete type. Before any RabbitMQ subscriber can be wired into the bootstrap lifecycle, this must be refactored to accept interfaces. This is a prerequisite for Wave 2+ tasks. If not addressed in Wave 0/1, all subscriber wiring will be blocked.

**Recommendation**: Create a "Wave 0: Kernel/Runtime preparation" phase that includes:
- Bootstrap refactoring (interface-based event bus injection)
- outbox.Writer doc enhancement (KS-01)
- KS-02 design decision (Publisher signature)
- KS-10 ArchiveStore layering fix

**Blocker 2: Cell service refactoring for outbox.Writer (KS-05)**

Seven call sites across 3 Cells currently call `publisher.Publish()` directly. They must be refactored to call `outbox.Writer.Write()` inside a transaction. This is a cross-cutting change that touches Cell business logic. It should be done AFTER the postgres TxManager + OutboxWriter are implemented (Wave 1) but BEFORE the full-chain integration test (Wave 3).

**Blocker 3: KS-10 adapters/s3 must NOT import cells/ (C-02)**

FR-4.4 says "implement `cells/audit-core/internal/ports.ArchiveStore`". This would violate C-02. The implementation must use the indirection pattern described in KS-10 (S3 adapter provides generic ObjectStore; Cell wraps it into ArchiveStore).

### Recommended Wave Structure

```
Wave 0: Preparation (no external deps)
  - Bootstrap interface refactoring (KS-06)
  - kernel/outbox doc enhancements (KS-01, KS-03)
  - KS-02 design decision
  - KS-10 ArchiveStore layering resolution

Wave 1: Infrastructure adapters (external deps required)
  - adapters/postgres: Pool, TxManager, Migrator
  - adapters/redis: Client, DistLock, IdempotencyChecker
  - adapters/rabbitmq: Connection, Publisher, Subscriber, ConsumerBase
  - Docker Compose setup

Wave 2: Application adapters
  - adapters/postgres: OutboxWriter, OutboxRelay
  - adapters/oidc: Provider, TokenExchange, JWKS Verifier
  - adapters/s3: Client, ObjectStore (generic)
  - adapters/websocket: Hub, UpgradeHandler

Wave 3: Cell integration + tech-debt
  - Cell service refactoring for outbox.Writer (KS-05, 7 call sites)
  - Security fixes (FR-9)
  - Tech-debt batch processing (FR-10)
  - cmd/core-bundle/main.go rewire to real adapters

Wave 4: Testing + documentation
  - Unit tests for all adapters
  - Integration tests (testcontainers)
  - Journey tests (J-audit-login-trail, J-config-hot-reload)
  - godoc, doc.go, integration test guide
```

---

## Summary of Required Actions Before Implementation

| Priority | Item | Type | Owner |
|----------|------|------|-------|
| P0 | KS-06: Refactor bootstrap to interface-based event bus | runtime/ change | Implementor |
| P0 | KS-10: Resolve ArchiveStore layering violation in FR-4.4 | spec clarification | Spec author |
| P0 | KS-05: Plan Cell refactoring for outbox.Writer (7 call sites) | cells/ change | Implementor |
| P1 | KS-01: Document context-embedded transaction pattern in outbox.Writer | kernel/ doc change | Implementor |
| P1 | KS-02: Design decision on Publisher.Publish signature | kernel/ decision | Architect |
| P1 | KS-09: Design decision on Subscriber.Close context parameter | kernel/ decision | Architect |
| P2 | KS-04: Evaluate Dependencies.Adapters for Phase 4+ | tech-debt record | Guardian |
| P2 | KS-07: Document Relay as worker.Worker pattern | adapter doc | Implementor |
