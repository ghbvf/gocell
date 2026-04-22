# Architecture Review -- Phase 4: Examples + Documentation

> Reviewer: Architect Agent
> Date: 2026-04-06
> Branch: `feat/003-phase4-examples-docs`
> Input: spec.md, product-context.md, phase-charter.md
> Codebase baseline: Phase 3 complete (28ac80f), 191 files, 16K lines

---

## Review Summary

Phase 4 scope is well-bounded: examples, documentation, templates, and tech-debt closure. The primary architectural risks are: (1) examples introducing dependency patterns that contradict established layer boundaries, (2) the RS256 migration creating a breaking change in access-core's public API, (3) the outboxWriter fail-fast guard conflicting with the existing Cell lifecycle model, and (4) the IoT-device example introducing an L4 Cell type without sufficient kernel-level support for the DeviceLatent consistency pattern. Below are 10 findings with concrete code references.

---

## Findings

### A-01 | P0 | [Layer Architecture] access-core still couples JWT signing to `[]byte` signingKey -- RS256 migration will be a breaking change to Cell public API

**Current state**: `AccessCore` struct in `/Users/shengming/Documents/code/gocell/cells/access-core/cell.go:90` stores `signingKey []byte`. The `WithSigningKey(key []byte)` Option at line 62 and all three slice constructors (`sessionlogin.NewService`, `sessionrefresh.NewService`, `sessionvalidate.NewService`) accept `[]byte signingKey` as a positional parameter (e.g. `sessionlogin/service.go:88`).

FR-7.1 requires `NewIssuer`/`NewVerifier` to default to RS256 and fail-fast without RSA key pair. FR-7.2 requires access-core to replace HS256 references with injected `auth.JWTIssuer` + `auth.JWTVerifier`.

**Issue**: The spec does not address the breaking change to `AccessCore`'s public constructor and Option signatures. `WithSigningKey(key []byte)` must become something like `WithRSAKeyPair(priv *rsa.PrivateKey, pub *rsa.PublicKey)`. The three slice `NewService` functions that accept `signingKey []byte` as a positional parameter must change their signatures. Consumers of the `sessionvalidate.IssueTestToken` helper (used in test utilities) will break.

**Impact analysis**: High. Any downstream code that creates `AccessCore` or calls slice constructors directly (including tests in `cell_test.go:26` using `WithSigningKey(testKey)`) will fail to compile. The `runtime/auth/jwt.go` already has `JWTIssuer`/`JWTVerifier` using RS256 -- the issue is the Cell-level API surface that still expects raw bytes.

**Suggested fix**: 
1. Spec should explicitly list the breaking API changes and deprecation path:
   - `WithSigningKey([]byte)` -> deprecated, replaced by `WithJWTIssuer(auth.JWTIssuer)` + `WithJWTVerifier(auth.JWTVerifier)`
   - Slice constructors should accept `auth.TokenIssuer` interface rather than raw key material
2. Add a migration section documenting the signature changes for downstream consumers
3. Provide `auth.GenerateTestKeyPair()` helper in `runtime/auth/keys.go` (the file already exists at `/Users/shengming/Documents/code/gocell/runtime/auth/keys.go`) to simplify test migration

---

### A-02 | P0 | [Cell Aggregate Boundary] IoT-device example introduces L4 Cell type `edge` without kernel validation support

**Current state**: `kernel/cell/types.go` defines `CellTypeEdge CellType = "edge"` (line 17) and `L4 Level = 4` (line 28). However, there is no kernel-level enforcement or lifecycle distinction for L4 cells. `BaseCell.Init/Start/Stop` in `kernel/cell/base.go` treats all levels identically. The outbox pattern (L2) has concrete support (outbox.Writer, OutboxRelay, TxManager), but L4's "DeviceLatent" pattern (command queuing, high-latency ack, timeout management) has zero kernel-level support.

FR-3.1 specifies: `device-cell` with `type: edge, consistencyLevel: L4` + 3 slices (device-register, device-command, device-status). FR-3.3 specifies command queuing with device polling for L4 high-latency mode.

**Issue**: The spec asks examples to demonstrate L4, but the framework has no L4-specific primitives. The example will need to implement command queuing, ack timeout, and device polling entirely within the example's application code, which defeats the purpose of demonstrating the framework's L4 capability. Evaluators (persona P4) will see L4 as a "label" with no framework support.

**Impact analysis**: High. This is a credibility gap for persona P2 (architect evaluating L4 viability). The example will look like a custom application rather than a framework-assisted pattern.

**Suggested fix**:
1. Option A (recommended): Add minimal L4 kernel primitives before building the example:
   - `kernel/command` package: `CommandQueue` interface (Enqueue/Dequeue/Ack/Timeout)
   - `kernel/cell/base.go`: Optional `CommandQueueRegistrar` interface (analogous to `HTTPRegistrar`)
   - Implementation can live in `adapters/postgres` (command_queue table) or in-memory
2. Option B (scope-constrained): Downgrade iot-device example to demonstrating L3 (WorkflowEventual) instead of L4, and document L4 as "planned" in the architecture overview. This is honest about current framework capabilities.
3. Option C (minimum viable): Keep L4 example but add a prominent disclaimer in the example README that L4 command primitives are application-level and the framework plans to provide first-class support in v1.1. Add a kernel interface definition (no implementation) as a design anchor.

---

### A-03 | P1 | [Interface Stability] outboxWriter fail-fast guard (FR-7.3) should validate at Assembly.Start, not Cell.Init

**Current state**: FR-7.3 specifies "Cell.Init 阶段校验 outboxWriter != nil". Looking at `assembly.go:99-108`, the Init phase already runs before Start. However, the outboxWriter is injected via Cell Options (e.g. `accesscore.WithOutboxWriter(w)`) before the Cell is registered. The current `sessionlogin/service.go:175-186` uses a conditional `if s.outboxWriter != nil` pattern.

**Issue**: Fail-fast at `Cell.Init` time mixes infrastructure validation (is the outbox wired?) with business initialization (setting up slices, services). This creates a dependency: the Cell must know its own consistency level and check it against its injected dependencies. Currently `BaseCell` has no visibility into what Options were applied to its embedding struct.

A cleaner approach: Assembly.Start should validate that all L2+ cells have outboxWriter injected, *after* registration but *before* Init. This keeps validation centralized and doesn't require each Cell to implement its own guard.

**Impact analysis**: Medium. The spec's approach works but couples validation logic into each Cell implementation. Every custom Cell (like todo-order's order-cell in FR-2) will need to replicate this guard, creating boilerplate.

**Suggested fix**:
1. Add a `DependencyValidator` interface in `kernel/cell`:
   ```go
   type DependencyValidator interface {
       ValidateDependencies() error
   }
   ```
2. Have `CoreAssembly.Start` call `ValidateDependencies()` for each cell between registration and Init
3. Alternatively, keep it in Cell.Init but document the pattern in the todo-order example so custom Cell authors know to replicate it

---

### A-04 | P1 | [Dependency Direction] examples/ using root `go.mod` risks coupling example code into the main module's dependency graph

**Current state**: Assumption 6 in spec.md states "示例项目使用根目录 go.mod（module 路径 github.com/ghbvf/gocell），不创建独立 module". The root `go.mod` at `/Users/shengming/Documents/code/gocell/go.mod` currently has no testcontainers dependency or example-specific deps.

**Issue**: When FR-6.1 adds `testcontainers-go` to `go.mod`, this dependency becomes part of the main module. Any consumer doing `go get github.com/ghbvf/gocell` will transitively pull testcontainers and its Docker dependencies. Similarly, example-specific imports (if any) pollute the core module.

Additionally, the `examples/` directory is under 项目根目录 (based on the `.gitkeep` at `examples/.gitkeep`), meaning example Go files will participate in `go build ./...` from the 项目根目录 directory. This is by design (FR-10.1 requires `go build ./examples/...` to pass), but it means example compilation errors block framework CI.

**Impact analysis**: Medium. testcontainers is a test-only dependency (build-tagged), so normal consumers won't link it, but `go.sum` will include its hashes, and `go mod tidy` will pull its transitive deps. This is a common Go module design trade-off.

**Suggested fix**:
1. Use `//go:build integration` tags on all testcontainers test files (already specified in FR-10.2) -- this mitigates the compilation impact
2. Consider adding a top-level `//go:build ignore` or separate `go.mod` for examples (`examples/go.mod` with `replace` directive pointing to parent). This keeps example deps isolated. However, this complicates the developer experience (multiple modules).
3. If staying with single module: Document explicitly that `go get` consumers should use `go install` or vendoring to avoid pulling test deps. Verify that `testcontainers-go` appears only in `go.mod` `require` with `// indirect` or under test-only imports.

---

### A-05 | P1 | [Consistency Level] todo-order's order-cell declares L2 but spec does not specify TxManager injection path

**Current state**: FR-2.1 declares `order-cell` with `consistencyLevel: L2`. FR-2.4 specifies `order.Create` uses `TxManager.RunInTx` for atomic outbox writes. But the spec does not describe how the todo-order example obtains and injects `TxManager`.

The existing pattern in access-core (`cell.go:54`) uses `WithOutboxWriter(w)` and separately the slice accepts `WithTxManager(tx)` (sessionlogin/service.go:54-55). There is no Cell-level `WithTxManager` Option on AccessCore -- the TxManager is wired at the slice level. This is inconsistent: the Cell-level outboxWriter Option exists, but TxManager is slice-level.

**Issue**: For the golden path example that teaches "how to build L2 cells", this inconsistency will confuse developers. The todo-order Cell needs both `TxManager` and `OutboxWriter`, but the spec does not specify whether they are Cell-level Options or slice-level injections.

**Impact analysis**: Medium. This is a developer experience issue. The golden path example sets the standard for how all future custom Cells are built. If the wiring pattern is awkward, every team will replicate that awkwardness.

**Suggested fix**:
1. Spec should prescribe a consistent injection pattern for todo-order:
   - Cell-level: `WithTxManager(tm)`, `WithOutboxWriter(w)`, `WithPublisher(pub)`
   - Cell.Init forwards these to slices
2. This becomes the documented "L2 Cell pattern" that the 30-minute tutorial teaches
3. Consider adding `WithTxRunner(runner TxRunner)` as a Cell-level Option to `BaseCell` itself (or a helper mixin) so L2+ cells don't each reinvent it

---

### A-06 | P1 | [Cross-Cell Communication] sso-bff assembles 3 built-in Cells but spec does not define contract-based communication

**Current state**: FR-1.5 specifies "login/logout 事件被 audit-core 消费". This implies cross-Cell event flow: access-core publishes `event.session.created` / `event.session.revoked`, audit-core subscribes and writes audit logs.

Looking at the current code: `access-core/cell.go:222-224` shows `RegisterSubscriptions` is a no-op. `audit-core/cell.go` (not read, but based on the slices: auditappend, auditquery, auditverify, auditarchive) presumably has a similar structure.

**Issue**: The spec does not specify:
1. Whether the sso-bff example defines formal contract YAML files for the cross-Cell events
2. How audit-core's `RegisterSubscriptions` discovers and subscribes to access-core's event topics
3. Whether the event flow uses the outbox relay (PostgreSQL -> RabbitMQ -> audit consumer) or the InMemory eventbus

For persona P2 (architect), this is the most critical demonstration: how Cells communicate via contracts. The CLAUDE.md rule states "Cell 之间只通过 contract 通信，禁止直接 import 另一个 Cell 的 internal/". The example must show this pattern working end-to-end.

**Suggested fix**:
1. Add to FR-1 a sub-item requiring contract YAML definitions:
   - `contracts/event/session/v1/contract.yaml` (owner: access-core, subscribers: audit-core)
   - `contracts/event/config/v1/contract.yaml` (owner: config-core)
2. Require sso-bff example to demonstrate: access-core publishes via outbox -> relay -> RabbitMQ -> audit-core consumes via `RegisterSubscriptions`
3. This proves the L2 end-to-end path with real infrastructure, connecting FR-1 with FR-6.5 (outbox full chain test)

---

### A-07 | P1 | [Performance / Scalability] OutboxRelay publishes inside FOR UPDATE transaction -- failed publishes block other relay instances

**Current state**: `adapters/postgres/outbox_relay.go:133-221` uses `FOR UPDATE SKIP LOCKED` to fetch entries in a transaction, publishes each entry, marks it published, then commits. If `pub.Publish` (line 196) hangs or is slow for one entry, the entire batch's transaction remains open.

FR-6.5 specifies `TestIntegration_OutboxFullChain` that validates write -> relay -> publish -> consume. The full-chain test should catch timeout scenarios.

**Issue**: For the sso-bff and todo-order examples running against real RabbitMQ, a slow broker will cause the relay to hold PostgreSQL row locks for extended periods. In production scenarios with multiple relay instances, this degrades to single-instance throughput because `SKIP LOCKED` skips the locked rows.

**Impact analysis**: Medium. This is not a Phase 4 blocker but should be documented as known behavior and tested in the full-chain integration test. The evaluator (P2 architect) will notice this pattern.

**Suggested fix**:
1. Add a timeout per-publish in `pollOnce`: wrap `r.pub.Publish` with `context.WithTimeout`
2. In the full-chain integration test (FR-6.5), add a sub-test for slow publisher behavior
3. Document this pattern in the todo-order example's README as "Production consideration: OutboxRelay publish timeout"
4. If out of Phase 4 scope, add to tech-debt registry as P3-TD-13

---

### A-08 | P2 | [Layer Architecture] FR-2.2 places `internal/domain` and `internal/ports` inside the example Cell -- this should be the canonical directory structure documentation

**Current state**: FR-2.2 specifies `cells/order-cell/internal/domain/`, `cells/order-cell/internal/ports/`. The existing built-in Cells follow this: `cells/access-core/internal/domain/`, `cells/access-core/internal/ports/`, `cells/access-core/internal/mem/`.

**Issue**: The spec treats the directory structure as a given but does not call out two important architectural decisions for the example:
1. Where does the PostgreSQL repository implementation live? In access-core, it lives at `cells/audit-core/internal/adapters/postgres/` (cross-referencing `cells/audit-core/internal/adapters/postgres/audit_repo.go`). This means cells can have internal adapter implementations. But the todo-order example's FR-2.3 says "repository 实现 PostgreSQL adapter" -- does this mean the repo lives in `cells/order-cell/internal/adapters/postgres/` or in the top-level `adapters/postgres/`?
2. The convention of `internal/adapters/postgres/` inside a Cell creates a hidden coupling to pgx. The Cell does not directly import `adapters/postgres` (top-level), but it imports `pgx/v5` transitively.

**Impact analysis**: Low-Medium. The golden path example should be unambiguous about where adapter code lives relative to the Cell boundary.

**Suggested fix**:
1. Spec should explicitly state: "order-cell's PostgreSQL repository implementation lives at `cells/order-cell/internal/adapters/postgres/order_repo.go`, following the same pattern as `audit-core/internal/adapters/postgres/audit_repo.go`"
2. The repository interface lives in `internal/ports/order_repo.go`, the implementation in `internal/adapters/postgres/order_repo.go`, and the in-memory fallback in `internal/mem/order_repo.go`
3. Document this three-location pattern in FR-4.7 (directory structure explanation) as the canonical adapter wiring pattern

---

### A-09 | P2 | [Interface Stability] `WithEventBus` deprecation (FR-8.3) should include compile-time deprecation signal

**Current state**: `runtime/bootstrap/bootstrap.go:86-91` defines `WithEventBus(eb *eventbus.InMemoryEventBus)`. FR-8.3 specifies adding a `// Deprecated` comment.

**Issue**: Go's `// Deprecated:` comment is informational only -- it produces a `staticcheck` warning but does not prevent compilation. The current `WithEventBus` accepts `*eventbus.InMemoryEventBus` as a concrete type, while `WithPublisher`/`WithSubscriber` accept `outbox.Publisher`/`outbox.Subscriber` interfaces. The deprecation comment alone may not be sufficient for evaluators to understand the migration path.

**Impact analysis**: Low. This is already correctly scoped as a comment change, but for completeness:

**Suggested fix**:
1. FR-8.3 is fine as-is for Phase 4
2. Add a follow-up item: In v1.1, remove `WithEventBus` entirely and provide a migration guide
3. The `// Deprecated:` comment should include the alternative: `// Deprecated: Use WithPublisher and WithSubscriber instead. Will be removed in v1.1.`

---

### A-10 | P2 | [Consistency Level] S3 adapter `ConfigFromEnv` fix (FR-7.4) should validate required fields

**Current state**: `adapters/s3/client.go:41-50` reads raw env vars (`S3_ENDPOINT`, `S3_REGION`, etc.) without any validation. FR-7.4 changes the prefix to `GOCELL_S3_*`.

**Issue**: `ConfigFromEnv` returns a `Config` struct even if all fields are empty. The caller (`s3.NewClient(s3.ConfigFromEnv())`) will get a client that fails at first use rather than at construction time. This contradicts the fail-fast principle applied in FR-7.3 (outboxWriter nil guard).

**Impact analysis**: Low. S3 is used only by `audit-core/internal/adapters/s3archive/` currently. But examples may demonstrate S3 usage, and a silent empty config is a debugging nightmare for evaluators.

**Suggested fix**:
1. When fixing the env prefix, also add validation: `ConfigFromEnv` should return `(Config, error)`, failing if `Endpoint` or `Bucket` is empty
2. Alternatively, keep the current signature but have `NewClient` validate and return error for missing required fields
3. If this is too much scope, document the expected env vars in the example docker-compose and `.env.example`

---

## Summary Table

| ID | Priority | Dimension | Summary |
|----|----------|-----------|---------|
| A-01 | P0 | Interface Stability | RS256 migration breaks `AccessCore` public API -- spec must document breaking changes and migration path |
| A-02 | P0 | Cell Aggregate Boundary | L4 Cell has no kernel primitives -- example will demonstrate custom app code, not framework capability |
| A-03 | P1 | Consistency Level | outboxWriter fail-fast should be Assembly-level, not per-Cell boilerplate |
| A-04 | P1 | Dependency Direction | Single go.mod means testcontainers pollutes consumer dependency graph |
| A-05 | P1 | Consistency Level | todo-order L2 example needs canonical TxManager injection pattern |
| A-06 | P1 | Cross-Cell Communication | sso-bff cross-Cell event flow needs contract YAML and end-to-end wiring |
| A-07 | P1 | Performance | OutboxRelay holds row locks during publish -- needs timeout and documentation |
| A-08 | P2 | Layer Architecture | Example should canonicalize Cell-internal adapter directory convention |
| A-09 | P2 | Interface Stability | WithEventBus deprecation comment should include migration target |
| A-10 | P2 | Consistency Level | S3 ConfigFromEnv should validate required fields on prefix change |

---

## Verdict

**2 P0, 5 P1, 3 P2 findings.**

P0 items (A-01, A-02) must be addressed in the spec before implementation begins:
- A-01 requires documenting the RS256 migration's API breaking changes and providing a test key pair helper.
- A-02 requires an architectural decision: add minimal L4 primitives, downgrade to L3 demonstration, or add a disclaimer. Recommend Option C (minimum viable) given Phase 4 time constraints.

P1 items should be addressed during implementation. A-06 (contract YAML for sso-bff cross-Cell events) is particularly important because it validates the core Cell communication model.

P2 items are documentation and polish -- address if time permits.
