# ADR: CQRS Projection lifecycle harness — design convergence (Q1–Q5) + kernel/projection skeleton

- Status: Accepted
- Date: 2026-05-26
- Epic: #1100 (CQRS Projection lifecycle harness)
- Implemented by (this PR — PR-00): #1172 [W10 PR-00] — ADR + `kernel/projection` skeleton (types/marker/phase, no implementation)
- Sub-issues converged by the epic: #1079 (Projection/Replay runtime), #834 (L3 example projection), #960 (PROJECTION-CONSISTENCY-01 → Hard), #961 (L3 projection observability)
- Spec / Plan / Tasks: `docs/plans/specs/202605261600-1100-cqrs-projection-harness-spec.md` (+ `-implementation-plan.md`, `-tasks.md`)
- Builds on ADRs: `202605051600-adr-pg-outbox-fencing.md` (fencing token), `202605101900-adr-cell-raw-infra-sealed-marker.md` (sealed marker), `202605161030-adr-cell-repo-readyz-probe.md` (readyz funnel), `202605241940-adr-l2-atomicity-subtypes.md` (L3 ⊥ L2)

## 1. Context and scope

GoCell's L3 projection scenario is hand-rolled today. The single instance —
`examples/todoorder/cells/ordercell/internal/orderprojection` — mixes the
framework-mechanical parts (sequence counter, in-memory append-only log, replay,
rebuild) with the business parts (the event→state apply logic) in one
hand-written `store`. Subscription wiring is already funneled through cellgen
(`reg.Subscribe`), but **checkpoint / replay / rebuild / metrics are entirely
business-implemented**: no persisted checkpoint (demo `nextSeq` is in-memory),
rebuild is a bare in-memory log replay with no event-store replay protocol, and
there is zero replay-lag / rebuild-duration / event-log-length observability.

Two roadmap-committed consumers (winmdm `unified_device_id` / unified view,
zerotrust `trustscore` / `eventcorrelation`) are about to ship streaming
aggregation read-models across ~19 cells. Letting AI hand-write the projection
loop (sequence / replay / checkpoint) in each is a textbook AI-robust Soft
proliferation — a literal convention that **will** drift. Harnessing it once is a
Hard funnel; hand-writing it N times is what the AI-robust charter forbids.

### In scope (this epic)

| Projection part | Owner |
|---|---|
| subscription wiring | harness (existing cellgen `reg.Subscribe`); this epic adds the `kind: projection` derivation path |
| idempotent consume | harness (existing `ConsumerBase`); unchanged |
| replay source | reuse the existing outbox journal / event store; this epic defines the cursor read contract |
| **checkpoint / offset** | **new** — framework-owned offset table, committed in the same `CellTx` as business apply (exactly-once; does not touch business schema) |
| **rebuild orchestration** | **new** — harness shell (Stop → Reset → Replay → Catch-up state machine) + business hook |
| **observability metrics** | **new** — `projection_event_replay_lag_seconds` / `projection_rebuild_duration_seconds` / `projection_pending_events` (covers #961) |
| **PROJECTION-CONSISTENCY-01 → Hard** | **new** — contractgen codegen funnel: `types_gen.go` compile-time guard (covers #960; the original parser-jsonschema framing was rejected — see §Amendment 2026-06-02 #960) |
| **examples L3 reference** | orderprojection rewritten onto the harness (covers #834) |

### Out of scope (GAP-8 sealed — see §4)

The event→state apply **function body** and the read-model **schema** stay
business-owned. The harness does not define a framework-level CQRS read-model
table. v1 also defers snapshot / partial replay (§Q4), multi-projection
concurrent rebuild, and cross-cell projection.

### This PR (PR-00) delivers only

The ADR (this document) plus the `kernel/projection` skeleton — interface
declarations with no consume logic:

- `doc.go` (package contract + ADR pointer), `phase.go` (the frozen 5-member
  `Phase` enum), `types.go` (`Apply`, `CheckpointStore`, `Option`),
  `cell_marker.go` (`CellCheckpointStore` sealed marker + `WrapCheckpointStoreForCell`).
- `contract_test.go` — an *implementability proof*: a fake `CheckpointStore`, a
  sample `Apply`, and assertions that the sealed marker + `Phase` work. This is
  how PR-00 verifies the ADR's decisions are expressible **now** rather than
  prose: `go build` proves the ambient-tx signatures typecheck against the real
  `outbox.Entry` / `persistence` types; the test proves the contracts are
  implementable and the marker actually seals. Behavioral correctness
  (exactly-once / crash recovery) is verified by PR-01..PR-06 (see §6 threat
  matrix → discharge column).
- `tools/archtest/projection_state_phase_frozen_test.go` — PROJECTION-STATE-PHASE-FROZEN-01,
  green from this PR (locks the `Phase` membership; §7).

The Coordinator, mem/PG checkpoint stores, rebuild state machine, metrics,
readyz, and cellgen derivation land in PR-01..PR-06 per the implementation plan.

## 2. Open-source benchmarks (对标)

Per CLAUDE.md every new module is benchmarked against open-source frameworks
first. This is the deeper version of the spec §0 table.

| Framework | apply hook shape | checkpoint store | rebuild orchestration | exactly-once | read during replay |
|---|---|---|---|---|---|
| **Axon** (Java — primary) | `@EventHandler void(MyEvent)` + business fetches repo | `token_entry` (processor_name, segment, token, **owner**) | 4-phase: Stop → Reset (`@ResetHandler`) → Replay → Catch-up | token committed in the **same tx** as the read-model | not blocked (stale) |
| **Marten** Async (.NET) | `Apply(IDocumentOperations ops, Event e)` | `mt_event_progression` | `daemon.RebuildProjectionAsync<T>` | separate tx, idempotent upsert | not blocked (stale) |
| **eventhorizon** (Go) | `Project(ctx, event, entity) (entity, error)` | **no built-in checkpoint** | no rebuild API | entity optimistic lock | business self-manages |
| **Commanded** (Elixir) | `project %Event{}, meta, do: Ecto.Multi.x(...)` | EventStore `subscriptions` | clear table + restart subscriber | separate tx | not blocked (stale) |
| **Watermill CQRS** (Go) | `Handle(ctx, event) error` | broker-side offset | no rebuild | broker-guaranteed | not blocked (stale) |

`ref:` (deep citations used in the decisions below):

- **Axon** — `org.axonframework.eventhandling.tokenstore.jdbc.JdbcTokenStore` (token-in-tx), `TrackingEventProcessor` lifecycle (Stop/Reset/Replay/Catch-up), `@ResetHandler`, `token_entry.owner` (distributed claim), `axon-micrometer` metrics (`eventProcessor.latency`).
- **Marten** — `Marten.Events.Daemon` async-daemon, `IProjection.ApplyAsync` / `IDocumentOperations`, `mt_event_progression`.
- **eventhorizon** — `github.com/looplab/eventhorizon/eventhandler/projector` (`Project(ctx, event, entity)`); **explicitly rejected** below.
- **Commanded** — `Commanded.Projections.Ecto` read-model-projections (`Ecto.Multi`).
- **Watermill** — `github.com/ThreeDotsLabs/watermill/components/cqrs` `EventProcessor` (GoCell's outbox + ConsumerBase already aligns with this; this epic is the projection extension on top).

Key takeaways driving the decisions:

1. eventhorizon's single-entity read-modify-write (its `Project` shape) **does
   not fit GoCell** — GoCell projections routinely write across tables; a single
   entity model locks the business. Explicitly rejected (Q2).
2. Axon's `token_entry` + same-tx commit is the most rigorous exactly-once model
   and maps exactly onto GoCell's `persistence.TxRunner.RunInTx` (Q1).
3. Axon rebuild is **4-phase** (Stop → Reset → Replay → Catch-up); the original
   spec's "3-phase" is superseded (§Q4-supplement / §3 Phase decision).
4. Industry consensus: **rebuild does not block reads** (Axon / Marten /
   Commanded / Watermill agree); stale read is a business concern. The spec §4
   Scenario C "read returns 503" line is superseded (§5).
5. Axon's `owner` column implements distributed claim; this introduces Q5
   (multi-pod concurrency for a single projection).
6. Snapshot is unnecessary for v1: Axon introduces snapshots because its event
   log is globally shared with replay volumes ≥ millions; GoCell's outbox is a
   per-cell local event stream, far smaller (Q4).

## 3. Decisions (Q1–Q5)

> Terminology: the Q-table option letters (A/B/C/D) are GoCell-local and do NOT
> correspond to the open-source A/B/C in §2's table. Each decision names its
> benchmark anchor explicitly.

### Q1 — checkpoint exactly-once protocol → **A** (caller-provided tx, harness-internal SaveOffset)

- **Options.** A: harness takes the caller's ambient tx and calls `SaveOffset`
  internally (ref: Axon JdbcTokenStore). B: harness exposes
  `RegisterCheckpoint(tx, offset)` for the business to commit. C: after-commit
  hook writes the offset asynchronously (not exactly-once; ref: Marten Async /
  Commanded).
- **Argument.** A matches the existing `outbox.Writer.Write(ctx, entry)` pattern
  exactly: the caller wraps in `RunInTx`, the implementation joins via
  `persistence.TxFromContext(ctx)`. The Coordinator runs `apply` + `SaveOffset`
  in one `RunInTx`, so the read-model mutation and the offset advance commit or
  roll back together — the strictest exactly-once model, and it is the same
  ambient-tx idiom every GoCell repository already uses (enforced by
  `PG-REPO-AMBIENT-TX-01`). B leaks the offset mechanism into business code; C is
  not exactly-once.
- **Decision.** A. The `CheckpointStore` interface (PR-00 `types.go`) is
  ambient-tx: `SaveOffset(ctx, cellID, projectionID, offset)` — no explicit tx
  parameter.
- **Correction of the plan sketch.** The implementation-plan and spec sketched
  `apply(ctx, event, txHandle)` / `SaveOffset(ctx, tx, ...)` with a
  `persistence.TxHandle` parameter. **No such type exists**, and an explicit
  tx-handle parameter contradicts `PG-REPO-AMBIENT-TX-01` (landed 2026-05-29,
  PR #1224), which seals repository tx access behind the ambient
  `persistence.TxFromContext` + `internal/pgexec` funnel. This ADR freezes the
  ambient-tx shape; the sketch is superseded.
- **Risk.** If a future cross-cell projection needs two independent
  transactions, ambient single-tx is insufficient. **Rollback condition:** ADR
  amend + PR-01 Coordinator rework. (Cross-cell projection is already out of
  scope for v1, §1.)
- **ref:** Axon JdbcTokenStore (same-tx commit); GoCell `kernel/outbox/outbox.go::Writer`.

### Q2 — business apply hook signature → **A** (`apply(ctx, event) error`, ambient tx)

- **Options.** A: `apply(ctx, event) error` with the tx ambient in ctx (ref:
  Marten Async `IDocumentOperations`). B: `apply(ctx, event, entity) (entity,
  error)` + harness read-modify-write (ref: eventhorizon — rejected). C:
  `apply(ctx, event) error` + business self-manages tx (ref: Watermill). D:
  annotation-driven (ref: Axon `@EventHandler`; not idiomatic in Go).
- **Argument.** A is the minimal dependency: the hook receives the event and
  obtains the tx ambiently — it does not bind the read-model schema or an entity
  type. This is the Marten `IDocumentOperations` shape adapted to GoCell's
  ambient-tx convention. B (eventhorizon) locks the business into single-entity
  read-modify-write — GoCell projections write across tables; rejected. C makes
  exactly-once the business's problem; rejected.
- **Decision.** A. PR-00 `types.go`:
  `type Apply func(ctx context.Context, event outbox.Entry) error`.
- **Risk.** Ambient tx is implicit — a business author might forget the apply
  runs in a tx and open their own connection. Mitigated by
  `PROJECTION-CHECKPOINT-TX-BOUND-01` (PR-02) + the doc.go contract + the
  cellgen-derived wiring being the only Subscribe callsite
  (`PROJECTION-APPLY-HOOK-FUNNEL-01`, PR-04). **Rollback condition:** if review
  finds the ambient model error-prone in practice, switch to an explicit
  ops-handle parameter — but this is an ADR-amend + epic-wide signature change,
  not a silent patch.
- **ref:** Marten async-daemon `IDocumentOperations`; eventhorizon `projector.Project` (rejected).

### Q3 — kind:projection codegen funnel shape → **A** (single subscribe-CU → single projection; a slice may declare multiple projections)

- **Options.** A: one `role: subscribe` contractUsage declares one projection
  (its event stream is that projection's input). B: multiple slices share one
  projection (needs a new metadata node).
- **Argument.** A covers both roadmap-committed scenarios (winmdm
  `unified_device_id`, zerotrust `trustscore`). B is a v1.1 extension point;
  building the multi-slice metadata node now is speculative generality.
- **Decision.** A. cellgen derives the projection wiring from each individual
  `contractUsages[role=subscribe]` entry that carries a `projection:` field
  (PR-04b). Each such CU produces one `reg.RegisterProjection` call (one
  projectionID + one checkpoint). A single slice may declare multiple projections
  by listing multiple `projection:`-bearing CUs; projectionID must be unique
  within a cell (`validateProjectionUniqueness` fail-closed).
- **Risk.** A logical read-model that needs to fan-in from multiple independent
  event streams corresponds to multiple projectionIDs (multiple checkpoints)
  writing to the same business read-model store — fan-in at the store level, not
  at the checkpoint level. **Rollback condition:** v1.1 adds a multi-slice
  projection metadata node; no v1 data migration needed (the checkpoint key is
  `(cell_id, projection_id)`, agnostic to slice count).
- **ref:** Axon processor-to-event-handler grouping.

#### Amendment 2026-06-02 — cardinality 更正为 per-subscribe-CU

**矛盾。** Q3 原文措辞「单 slice 单 projection，其 subscribe **集合**是输入流」暗示一个
projection 可 fan-in 多个事件流并共享一个 checkpoint。但 PR-04a 合并的实现是结构性单流：
`cell.ProjectionRequest` 携带**单个** `Spec`（`Spec.Kind=="event"`），
`projection.Coordinator.Subscribe` 是 once-only，`CheckpointStore.LoadOffset(cellID, projectionID)`
是**单 offset**。因此一个 projection ≡ 一个事件流 ≡ 一个 checkpoint。

**更正。** v1 派生单位是 **subscribe-CU**，不是 slice。每个带 `projection:` 的
`role: subscribe` contractUsage 派生出**一个** `reg.RegisterProjection`（一个 projectionID
+ 一个 checkpoint）。一个 slice **可以**声明多个 projection（每个带 `projection:` 的 CU
一个，projectionID 在 cell 内唯一，由 parser `validateProjectionUniqueness` fail-closed
守卫）。一个需要 fan-in 多个事件流的逻辑读模型，在 v1 表现为多个 projectionID（多
checkpoint）写同一 store 的**不相交子区域**，在 query 时合成。**不可**写共享可变态：
每个 projectionID 有独立的 onReset，naive 共享 store 会在某个 projection 的 rebuild
Reset 阶段把其它 projection 的贡献一并清空（reset 互清）。disjoint 子视图 + query 合成
是 v1 唯一 sound 的 fan-in 形态，连同 per-spec replay 过滤一起落地于 §Amendment
2026-06-04（#1482，examples/todoorder orderprojection 为 reference）。

**不变项。**「多 slice 共享一个 projection」仍是 Q3 的 v1.1 rollback condition（需要新的
multi-slice metadata node），本次 amendment 不改变它。checkpoint 键仍是
`(cell_id, projection_id)`，与本更正一致。

**威胁矩阵逐行重评。** 本更正的影响范围限于 cardinality 措辞（一个 projection 对应几个
事件流）；机制本身（checkpoint 提交、rebuild 状态机、exactly-once 路径）不受影响：

- **Row 1**（exactly-once）：per-CU 模型下每个 projectionID 仍是独立单流，apply +
  SaveOffset 在同一 CellTx 内提交的前提不变。✅ 不降级。
- **Row 2**（crash recovery）：checkpoint 持久化语义与 cardinality 无关。✅ 不降级。
- **Row 3**（rebuild-period read consistency）：`Phase()` 是 per-Coordinator 的，每个
  projectionID 有独立 Coordinator，语义不变。✅ 不降级。
- **Row 4**（out-of-order / serial-delivery）：**单流 checkpoint 前提在 per-CU 模型下不变**。
  每个 projectionID 仍是独立单流，serial-delivery 前提逐 projection 成立。
  per-CU 模型不引入新的并发向量（不同 projectionID 的 Coordinator 相互独立）。✅ 不降级。
  注：serial-delivery 的 enforcement 机制本身尚未落地——仍由 Amendment 2026-05-31 Row 4
  补偿（`cmd/*` 仅连线串行内存总线）追踪，完整 enforcement 在 PR-04d (#1369)。
- **Row 5**（fail-closed）：SaveOffset 失败回滚整个 CellTx，per-CU 无影响。✅ 不降级。
- **Row 6**（GAP-8 boundary）：per-CU 派生不增加框架对读模型 schema 的约束。✅ 不降级。
- **Row 7**（multi-pod concurrency）：owner 列 write-guard 作用于 projection_checkpoints 表，
  与 CU 数量无关。✅ 不降级。

所有行均无格子从 ✅ 降级为 ⚠️/❌。

### Q4 — snapshot / partial replay in v1 → **A** (not in v1; full rebuild)

- **Options.** A: v1 does not do snapshots (rebuild is full). B: v1 leaves a
  hook but no PG store. C: v1 full support.
- **Argument.** A snapshot PG-table abstraction would cross the GAP-8 seal
  boundary (it would impose a framework read-model-ish table). The
  full-replay-only choice is well-supported by the benchmark: Axon introduces
  snapshots because its event log is globally shared with replay volumes ≥
  millions; GoCell's outbox is a per-cell local stream, far smaller.
- **Decision.** A. No snapshot store in v1; deliberately blank.
- **Risk.** If winmdm Stage 1 measures full rebuild ≥ 30 min, snapshot becomes a
  v1.1 emergency. **This trigger condition is documented here, not filed as a
  silent backlog** (per the no-lazy-deferral rule): *winmdm Stage 1 full-rebuild
  wall-clock ≥ 30 min ⇒ open a v1.1 snapshot-store epic.* **Rollback
  condition:** same trigger.
- **ref:** Axon snapshot design motivation (shared-log replay volume) vs GoCell per-cell stream scale.

### Q5 — multi-pod concurrency for one projection → **A** (v1 single-pod; reserve `owner` column)

- **Options.** A: v1 single-pod (leader election handled by the upper layer
  `cmd/corebundle`); the schema reserves an `owner TEXT` column that v1 never
  **writes** (and currently does not read). B: v1 built-in pessimistic claim
  (owner column + advisory lock).
- **Argument.** A is the lowest-complexity safe v1. The PG checkpoint schema
  (PR-02) includes `owner TEXT NOT NULL DEFAULT ''` (ref: Axon
  `token_entry.owner`), but v1 INSERT/UPDATE **write** paths **must not touch
  it** — statically guarded by `PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01`
  (landed in PR-02 with the PG adapter: an AST scan rejecting `owner` in any
  INSERT/UPDATE on `projection_checkpoints`). The guard is **write-scoped** by
  design: writing a stale `owner` would seed dirty claim state, whereas *reading*
  `owner` returns the empty default and is harmless — so reads are deliberately
  not forbidden, leaving the v1.1 claim path free to read the column. Because the
  v1 upsert omits `owner`, the NOT NULL constraint is satisfied solely by the
  `DEFAULT ''`; that load-bearing default is asserted at startup by
  `schema_guard.verifyDefaults` (a dropped default would otherwise fail the first
  write). Multi-pod safety in v1 is the responsibility of upper-layer leader
  election. v1.1 implements pessimistic claim on the existing `owner` column —
  zero migration cost.
- **Decision.** A. **Explicit v1 boundary:** v1 runs single-pod; running two
  pods consuming the same projection without external leader election is
  **unsafe in v1** (both would advance the same checkpoint). This is a documented
  v1 limitation, surfaced in the threat matrix row 7.
- **Risk.** An operator deploys 2+ replicas of a projection cell without leader
  election → double-apply / checkpoint races. Mitigation: documented boundary +
  the `owner` column is pre-provisioned so v1.1 claim needs no schema change.
  **Rollback condition:** none needed — v1.1 is a forward extension, not a
  rollback.
- **ref:** Axon `token_entry.owner` distributed-claim column.

### Phase enum (settled alongside Q-decisions)

The `Phase` enum (PR-00 `phase.go`) is frozen to **five** members:
`PhaseLive`, `PhaseStopped`, `PhaseReset`, `PhaseReplay`, `PhaseCatchup`. The
four rebuild phases mirror Axon's processor lifecycle; `PhaseLive` is the
steady-state value outside a rebuild, needed because `Phase()` is also the value
a business read path inspects for its read-503 opt-in (§5).

**Supersession (per ai-robust.md "原文与 amendment 矛盾段落同 PR 内重写").** The
spec §5.1 "three-phase (reset/replay/catch-up)" and §4 Scenario C "business read
returns 503 (blocking)" predate the Axon benchmark and are **superseded by this
ADR**: rebuild is 4-phase (Stop added) and reads are not blocked (§5). The spec
text is the historical input; this ADR is the authority.

## 4. GAP-8 seal re-review record

GAP-8 (the six-seat sealed boundary: "the framework does not prescribe CQRS
read-model tables") was reviewed against this harness. **Finding:** the
CellTx-offset design keeps the harness clear of the seal. The harness owns only
its own `projection_checkpoints` offset table; it does **not** define, require,
or constrain any business read-model table schema, and the event→state apply
function body stays business-written. Therefore the harness is **not** within
GAP-8's sealing rationale ("do not bind the business's table-design freedom").

**Seal narrowed (recorded for the upcoming winmdm Stage 1 six-seat re-review):**
the GAP-8 seal is read here as covering exactly two things — *(a)* the apply
function body, and *(b)* the read-model table schema. Lifecycle mechanics
(checkpoint / replay / rebuild / metrics) are explicitly outside the seal. This
record is the input to feed the winmdm Stage 1 six-seat meeting; the seal itself
is unchanged, only its scope is documented.

## 5. Q4-supplement — reads are not blocked during rebuild; `Phase()` is the opt-in

Industry consensus (Axon / Marten / Commanded / Watermill, §2 row "read during
replay") is that **rebuild does not block business reads**; a stale read during
rebuild is the business's responsibility. The harness follows this:

- The harness **never** forces a 503 on business read paths.
- The Coordinator exposes `Phase() Phase`. `PhaseLive` ⇒ the read-model is fresh
  and serving; any other phase ⇒ a rebuild is in progress. A business read path
  **may** consult `Phase()` and return 503 of its own accord, but the harness
  does not impose it.
- `Phase()` is a **best-effort point-in-time snapshot, not a freshness lock**: a
  read path that observes `PhaseLive` and then queries the read-model has no
  atomic ordering against a rebuild that may begin between the two calls.
  Business code requiring strict freshness must maintain its own read quiescence;
  the harness deliberately does not provide a happens-before guarantee here.

This supersedes spec §4 Scenario C "business read returns 503" and §5.2
"rebuild 期间业务 read endpoint 必须 503": the contract is **non-blocking +
`Phase()` opt-in**, recorded here as the single source of truth.

### Readyz probe name (PR-03 forward contract)

The projection readiness probe name is frozen here as the operational contract
(it becomes a dashboard/alert dependency once PR-03 lands):
`<cell>_projection_<name>_ready` (supersedes the spec §3 `<cell>_projection_ready`
form, which omitted the projection name — a cell may host more than one
projection). It is constructed via `kernel/healthz.NewProbeName(...)` — there is
**no `ReadyProbeName` type** and **no bare `healthz.ProbeName(string)` cast**
(that would bypass `PROBENAME-SEALED-FUNNEL-01`). **Length budget:**
`NewProbeName` caps names at 64 chars; the fixed segments cost 18
(`_projection_` = 12, `_ready` = 6), so `len(cellID) + len(projectionID) ≤ 46`.
PR-03's constructor returns `(ProbeName, error)` and fails fast at construction
when the budget is exceeded, rather than silently dropping the probe.

### Rebuild control-plane endpoint (forward contract)

The rebuild trigger is a framework control-plane HTTP endpoint. PR-03 freezes its
forward contract (below) and ships only the kernel/runtime harness with no HTTP
surface. **The original framing of this endpoint as a per-cell `contract.yaml` +
cellgen handler (with a host cell to satisfy `DEAD-CONTRACT-01`) was superseded by
PR-04e (#1370)**: it is mounted by bootstrap itself as a framework-owned
RouteGroup — the same pattern as `/healthz`·`/readyz`·`/metrics` — so it has **no
`contract.yaml`, no host cell, and never reaches `DEAD-CONTRACT-01`**. See
§Amendment 2026-06-03.

The HTTP trigger contract. The **current** (post-#1505) form is the operator
admin plane; the PR-03/PR-04 forward contract (service-token + caller-cell
allowlist on `/internal/v1/*`) was superseded by **PR #1505** — it lands the
"deferred operator-credential admin surface" that §Amendment 2026-06-03 already
anticipated. Full rationale + the operator-credential threat matrix live in ADR
`202606041200-1505-adr-operator-control-plane-auth.md`; see §Amendment 2026-06-04
below.

- **Endpoint**: `POST /admin/v1/projection/<cellID>/<projectionID>/rebuild` on the
  `cell.AdminListener` (operator control-plane). *Superseded the PR-03/PR-04
  forward contract `POST /internal/v1/<cellID>/projection/<projectionID>/rebuild`.*
- **Caller / auth**: operator-credential gate (`auth.AuthOperator`: HTTP Basic
  Auth over env credentials + per-IP rate limit + constant-time compare). This is
  an **operator→system** action, NOT cell→cell — there is **no caller-cell
  allowlist** (the prior `/internal/v1/*` model). No public access.
- **Network boundary**: a network-isolated (loopback) admin listener; loopback
  isolation **plus** operator credentials are a defense-in-depth pair. Never
  mounted on the public or internal listener — admin-path ↔ AdminListener affinity
  is enforced bidirectionally at startup by the router.
- **HTTP semantics** (unchanged across the migration): `202 Accepted` (rebuild is
  async — `Coordinator.Rebuild` returns immediately; the state machine runs on a
  background goroutine) / `409 Conflict` (`ErrRebuildInProgress` — a rebuild is
  already running) / `404 Not Found` (unknown cell/projection).
- **Response envelope** (unchanged): unified `{"data": {...}}` shape; `data`
  carries the current `{phase, replayLagSeconds, pendingEvents}` snapshot. Errors
  use the shared error envelope.
- **PR-03 programmatic surface**: `Coordinator.Rebuild(ctx) error` (admission
  CAS `PhaseLive→PhaseStopped`) + `Coordinator.Close(ctx)` for graceful drain.
  The snapshot is computed by the readyz probe / metrics path delivered in PR-03.

**PR-04e (#1370) authored the HTTP endpoint** as a framework-owned RouteGroup
(bootstrap-mounted via `WithProjectionRebuildEndpoint`, **not** cellgen output)
calling `Coordinator.Rebuild`. **PR #1505 then migrated the listener / path / auth
model** to the operator admin plane above (the 202·409·404 status semantics + the
framework-owned-RouteGroup mechanism are unchanged). Mechanism detail
(framework-mount, the `Coordinator.Snapshot` accessor) in §Amendment 2026-06-03;
the operator-credential migration in §Amendment 2026-06-04.

**Threat-matrix re-evaluation.** No cell flips: row 3 (rebuild-period read
consistency) is discharged by `Phase()` + readyz, **both delivered in PR-03** as
planned — the HTTP *trigger* is a convenience surface, not a threat-discharge
mechanism (a rebuild is equally triggerable via `Coordinator.Rebuild`). The #1505
listener/path/auth migration changes *who may reach the trigger* (operator vs
cell), not *how rebuild-period reads stay consistent*, so the row-3 discharge is
untouched. The operator-credential surface's own threats are matrixed in the
#1505 ADR.

## 6. Threat matrix

Each row names the threat, the v1 mechanism, and **which PR discharges it**
(PR-00 freezes the contracts; behavior is proven downstream — answering "can an
ADR-only PR be verified": the type-level decisions are verified now, behavior is
verified by the listed PR).

| # | Threat | v1 mechanism | Discharged by |
|---|---|---|---|
| 1 | **exactly-once** (apply runs once per offset) | apply + `SaveOffset` in one `CellTx` (Q1); the harness compares each event's stream position (from the `Cursor` contract defined in PR-01 — not an `outbox.Entry` field) against the stored checkpoint and skips when ≤ checkpoint | PR-01 defines the `Cursor` contract and verifies the compare/skip logic with a test fake (cold-start / out-of-order / forward-gap unit tests). The production journal-backed `Cursor` + `ReplaySource` are **DELIVERED in PR-04c (#1368)** — outbox-journal-backed (`outbox_entries.seq`), enrolled in the shared Cursor/ReplaySource conformance suites; corebundle wires them in PG mode (mem cursor/replay fakes remain for the serial in-memory bus / demos). See §Amendment 2026-06-03. PR-06 real-PG e2e (cold-start/crash/rebuild) remains a follow-up |
| 2 | **crash recovery** (no replay window after restart) | checkpoint persisted in the apply tx; restart loads checkpoint, resumes at offset+1 | PR-01 crash-recovery unit test; PR-06 real-PG integration (kill → restart) |
| 3 | **rebuild-period read consistency** | non-blocking by design (§5); `Phase()` lets business opt into 503; stale read is a business concern | PR-03 `Phase()` + readyz; ADR §5 contract. The HTTP trigger moved to **PR-04e (#1370)** (a #1176 follow-up — §Amendment 2026-05-31), then **migrated to the operator admin plane in PR #1505** (`/admin/v1/*` + `AuthOperator`, §Amendment 2026-06-04); the Phase()/readyz discharge mechanism landed in PR-03 as planned and is unchanged by either move (the HTTP trigger is a convenience surface, not a threat-discharge mechanism). |
| 4 | **out-of-order / concurrent delivery** (broker redelivery-reorder OR intra-consumer-group concurrency, e.g. AMQP prefetch>1 dispatching a goroutine per delivery) | checkpoint is monotonic; a redelivered/late event whose replay-cursor position ≤ checkpoint does NOT invoke apply (exactly-once delivery to apply); ConsumerBase's Claimer idempotency layer (keyed per event-ID) sits above the Coordinator as defense-in-depth. **PRECONDITION (PR-01 amendment):** this skip is only sound under STRICTLY SERIAL, IN-ORDER delivery of the stream — a single consumer group does NOT provide it. Under concurrent delivery a higher position can commit the checkpoint before a lower position is applied, silently dropping the lower event's distinct apply (projection gap). This is distinct from row 7's multi-pod boundary (it bites within a single pod via prefetch>1). The per-event-ID Claimer does NOT serialize positions, so it gives no protection here. **Compensation:** v1 is safe because cmd/* wires only the serial in-memory bus (`runtime/eventbus`, single-goroutine consume); serial-delivery enforcement (prefetch=1 / single-goroutine dispatch for projection subscriptions) is a HARD prerequisite of the production wiring — no concurrent transport may carry a projection subscription until it lands. | PR-01 reorder-hazard characterization unit test (`TestCoordinator_ReorderDropsLowerPosition`) + `applyOne` `pos<1` guard + doc.go "Ordering precondition"; **serial-delivery enforcement DISCHARGED by PR-04d (#1369)** — the `outbox.SerialInOrderGuarantor` capability marker + the fail-closed-by-absence guard `checkSubscriberGuaranteesSerialDelivery` in `runtime/bootstrap/phases_projection.go` (rejects wiring a projection onto any subscriber that does not guarantee serial in-order delivery), locked by archtest `PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01`. A concurrent transport (AMQP prefetch>1 / MQTT worker pool) now fails fast at bootstrap instead of silently dropping positions. See §Amendment 2026-06-02 |
| 5 | **fail-closed** (checkpoint store failure) | `SaveOffset` failure rolls back the whole `CellTx` (apply not committed); Coordinator requeues; never advances offset past an un-applied event | PR-01 fail-closed unit test |
| 6 | **GAP-8 boundary** (harness must not prescribe read-model schema) | CellTx-offset design touches only the framework offset table; apply body + read-model schema stay business-owned (§4) | This PR (§4 record) + PR-02 schema review |
| 7 | **multi-pod concurrency (v1 boundary)** | v1 single-pod (Q5); `owner` column reserved, **write-guarded** (reads harmless/unused); multi-pod safety = upper-layer leader election; **2+ replicas without leader election is unsafe in v1** | PR-02 `owner`-reserved archtest (write-scoped, landed); `schema_guard.verifyDefaults` asserts the load-bearing `owner DEFAULT ''`; documented v1 limitation (Q5) |

## 7. AI-robust ratings

The epic introduces six enforcement mechanisms. Ratings follow
`.claude/rules/gocell/ai-robust.md`; symbol inventories live in each archtest's
package godoc (not duplicated here).

| ID | PR (stub → green) | Funnel direction / rating | Hard-template (§"Hard 范本目录") |
|---|---|---|---|
| **PROJECTION-STATE-PHASE-FROZEN-01** | PR-00 (green now) | n/a (membership freeze) — **Medium** (AST const-set + String-arm lock; Go enums are not reflectable as a set, so an AST/golden lock is the ceiling — same shape as `OUTBOX-STATE-TRANSITION-COMPLETENESS-01`). The orthogonal "zero value invalid" guarantee is Hard via `iota+1` + `Phase.Valid()` (type system). | enum-set golden lock |
| **PROJECTION-APPLY-HOOK-FUNNEL-01** | PR-01 active (genuinely-green) → **PR-04a bootstrap-drain callsite** | 下游 **Medium** / 上游 **Medium** — archtest caller-allowlist locks `Coordinator.Subscribe` callers to {kernel/projection self, `_test.go`, **`runtime/bootstrap/phases_projection.go`**}. **Relocation (PR-04a, Option A — §Amendment 2026-05-31):** the sanctioned callsite moved from cellgen `cell_gen.go` to the single bootstrap drain file, because `Coordinator.Subscribe` must be fed framework-owned raw infrastructure that cell code may never hold (sealed-marker architecture). Go cannot type-system-gate who calls a *public* method, so the caller-identity allowlist is the strongest achievable form (Medium, same shape as `HEALTHZ-WRITE-01` A2) — now over a single hand-written kernel/runtime file (tighter than "any cell_gen.go"). A true Hard would need a cellgen-only sealed-token parameter changing the PR-00-frozen `Subscribe` signature, but that is **not expressible in Go**: cellgen emits the caller into the cell's own package, indistinguishable at compile time from a same-package hand-written call — so this stays Medium permanently (won't-do, **gh #1372**; ceiling family #851 / #893 / #1282 — see §Amendment 2026-06-04). | single sanctioned holder / caller-identity allowlist |
| **PROJECTION-REGISTER-FUNNEL-01** | **PR-04b active (load-bearing)** — first prod callsite `examples/todoorder/cells/ordercell/cell_gen.go` (#834) | 下游 **Medium** / 上游 **Medium** — archtest caller-allowlist locks `cell.Registrar.RegisterProjection` callers to {`_test.go`, cellgen `cell_gen.go` + DO-NOT-EDIT banner}. The upstream complement of APPLY-HOOK-FUNNEL: cellgen's record-only single source is `reg.RegisterProjection` (guarded here); the `Coordinator.Subscribe` terminal that consumes it is in the bootstrap drain (guarded by APPLY-HOOK-FUNNEL). Same permanent Go-language ceiling (Medium); a sealed-token Hard-ization is **not expressible** (cellgen emits into the cell's own package, indistinguishable at compile time from a same-package hand-written call) — won't-do, **gh #1372** (ceiling family #851 / #893 / #1282 — see §Amendment 2026-06-04). The raw-infra-stays-in-bootstrap property is the **Hard** complement (type system): cells hold sealed markers, never the raw `CheckpointStore`/`TxRunner` the drain constructs. | single sanctioned holder / caller-identity allowlist |
| **PROJECTION-CHECKPOINT-TX-BOUND-01** | PR-01 stub → PR-02 green | **Medium** (`SaveOffset` impl must obtain tx via `persistence.TxFromContext`; raw `db.Exec` / `*sql.Tx` form fails). Rides the existing `PG-REPO-AMBIENT-TX-01` Hard funnel for the PG adapter. | typed-param / ambient-tx form |
| **PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01** | PR-02 | **Medium** (SQL-literal scan rejects `owner` in INSERT/UPDATE write paths; v1 scope — removed in the same PR that enables v1.1 claim). | input-struct field exclusion (SQL-write variant) |
| **PROJECTION-CONSISTENCY-01** (codegen funnel) | gh #960 (delivered) | 下游 **Hard** — contractgen emits `const _ = uint(cellvocab.<level> - cellvocab.L3)` into the projection `types_gen.go`; a level below L3 overflows uint at compile time, so an invalid `codegen=true` projection cannot exist in a buildable tree. 上游 **Hard** (structural, not caller-allowlist) — `generateOneContract` renders `types.tmpl` unconditionally for every `kind:projection` contract and the guard sits in an unconditional `{{if eq .Kind "projection"}}` block, so a generated projection `types_gen.go` cannot exist without the guard; the byte-lock is the committed `generated/.../types_gen.go` + `hack/verify-codegen-contract.sh` regenerate-and-diff CI. The governance rule `PROJECTION-CONSISTENCY-01` is the **Medium** backstop for the two vectors the codegen funnel cannot reach (`codegen=false` contracts + in-memory fixtures). The original "parser load-time `jsonschema.Validate`" framing was **rejected** (a parse-time validator is a Medium runtime guard and does not cover the in-memory vector — see §Amendment 2026-06-02 #960). | codegen funnel + compile-error downstream |
| **PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01** | PR-04d (#1369) green | **Medium** (single axis — runtime drain fail-fast + fail-closed-by-absence capability marker; injected-Subscriber dynamic property has no compile-time expression, same ceiling as #851 / #893). Sub-rules: marker freeze / exact implementer set `{InMemoryEventBus}` / single guard callsite. Hard path (sealed projection-transport token) = gh #1475. | runtime invariant guard + typed marker (fail-closed-by-absence) |

Sealed-marker note: `CellCheckpointStore` (PR-00 `cell_marker.go`) is the
sealed-marker Hard at the field/assignment layer (external code cannot express
`internalCellCheckpointStore`), mirroring `outbox.CellPublisher`. It auto-enrolls
in `SEALED-MARKER-NOOP-TRANSPARENCY-01` (every kernel `internalCell*` must
forward `Noop()`); this PR bumps that archtest's floor guard 4 → 5.

## 8. Consequences

**Positive.** One Hard funnel replaces N hand-written projection loops across
winmdm/zerotrust. Checkpoint exactly-once is the same ambient-tx idiom as
outbox.Writer (no new mental model). The skeleton makes the ADR's signature
decisions compile-verifiable from day one.

**Negative / accepted costs.** Ambient tx is implicit (mitigated by archtests +
doc contract, Q2 risk). v1 is single-pod (Q5 boundary). No snapshot in v1 (Q4
trigger documented). The `Phase` enum freeze means adding a phase is a
deliberate golden update (intended).

## 9. Alternatives rejected

- **Explicit `persistence.TxHandle` parameter** (the plan sketch): rejected —
  contradicts `PG-REPO-AMBIENT-TX-01` and `outbox.Writer`; would invent and seal
  a new type for no benefit over the established ambient idiom (§Q1 correction).
- **eventhorizon single-entity `Project(ctx, event, entity)`**: rejected —
  GoCell projections write across tables (§Q2).
- **After-commit async offset write** (Marten/Commanded separate-tx): rejected —
  not exactly-once (§Q1 option C).
- **Snapshot store in v1**: rejected — crosses GAP-8, unsupported by replay-scale
  benchmark; deferred with a documented trigger (§Q4).

## Amendment 2026-05-31 (PR-04a — Option A wiring seam + PR-04 split)

PR-04 (#1176) as originally scoped bundled six separable concerns; the
≤2000-line/PR budget and a file-level conflict with epic **#1085** (CellModule →
`runtime/composition`: its plan edits `tools/codegen/cellgen` templates + golden
and refactors `cmd/corebundle`) made a single PR infeasible. PR-04 is therefore
split into sequenced sub-PRs (filed under epic #1100):

- **PR-04a** (this amendment, #1176): the `reg.RegisterProjection` record-only seam
  + bootstrap projection drain + DI options + the two caller funnels.
  **Conflict-free** — touches only `kernel/cell` + `runtime/bootstrap` + `tools/archtest`.
- **PR-04b (#1367)**: cellgen `kind:projection` derivation + metadata (conflicts
  #1085 Batch 4 → after it lands). **PR-04c (#1368)**: production journal-backed
  `Cursor` + `ReplaySource` + corebundle wiring (conflicts #1085 Batch 2).
  **PR-04d (#1369)**: serial-delivery enforcement. **PR-04e (#1370)**: HTTP rebuild
  control-plane endpoint. **PR-04f (#1371)**: dev guide. **PR-04g (#1372)**: funnel
  Hard-ization (cellgen-only sealed token) — **closed won't-do** (the sealed token
  is not expressible in Go; the two funnels stay Medium permanently). See
  §Amendment 2026-06-04.

### Option A — wiring seam (supersedes the §7 "cell_gen.go Subscribe callsite")

The cellgen-generated `cell_gen.go`'s `Init(ctx, reg)` holds only a
`cell.Registrar`, yet `projection.Coordinator` needs framework-owned raw
infrastructure (`CheckpointStore` / `TxRunner` / `Cursor` / `ReplaySource`). Cells
must never reach raw infra (sealed-marker architecture, `CELL-RAW-INFRA-*`), so
emitting `projection.NewCoordinator` into generated cell code is rejected.
Instead (mirroring the subscribe / webhook record-in-Init → drain-in-bootstrap
split):

1. cellgen emits **record-only** `reg.RegisterProjection(cell.ProjectionRequest{…})`
   into `cell_gen.go` (PR-04b). `ProjectionRequest` is self-contained in
   `kernel/cell` (uses only `outbox.Entry` / `contractspec` + cell-local
   `ProjectionApply` / `ProjectionResetHook` func types) — it carries **no**
   `kernel/projection` symbol, because `kernel/projection` already imports
   `kernel/cell` (its Coordinator holds a `cell.Registrar`) and a reverse import
   would be a compile-time cycle. Guarded by **PROJECTION-REGISTER-FUNNEL-01**.
2. `runtime/bootstrap` (which legally imports both kernel layers) drains
   `RegistrySnapshot.Projections`, constructs one Coordinator per request from the
   raw deps it holds, and calls `Coordinator.Subscribe`. The named-type conversion
   `cell.ProjectionApply → projection.Apply` happens here. This is the single
   sanctioned `Coordinator.Subscribe` callsite, so **PROJECTION-APPLY-HOOK-FUNNEL-01**
   relocates its allowlist from `cell_gen.go` to `runtime/bootstrap/phases_projection.go`.

Because merged `Coordinator.Subscribe` calls `reg.Subscribe` internally (designed
for an Init-time call against the live `RegistryRecorder`), the drain passes the
Coordinator a **capture `Registrar`** whose `Subscribe` records the wrapped
`(spec, handler, consumerGroup, cellID, sliceID)` instead of appending to the
finalized recorder; the drain then feeds that `SubscriptionRequest` to
`evtRouter.AddContractHandler` — the same sink as `drainCellSubscriptions`. The
Coordinator is unchanged; only its injected Registrar differs.

### Threat-matrix re-evaluation (ai-robust ADR-amendment requirement)

No row flips to ⚠️/❌. The discharge **mechanisms** are unchanged; only the PR
that lands each moves from "PR-04" to a named sub-PR:

- **Row 1** (exactly-once / production Cursor) → PR-04c. PR-04a ships the seam on
  the mem cursor/replay fakes, which are sound for the serial in-memory bus (the
  only transport PR-04a's wiring path carries). No regression: cold-start /
  out-of-order / fail-closed are already discharged by PR-01 unit tests.
- **Row 3** (rebuild read consistency / HTTP trigger) → PR-04e. The actual
  discharge (`Phase()` + readyz) already landed in PR-03; the HTTP trigger is a
  convenience surface, not a threat-discharge mechanism — moving it is inert.
- **Row 4** (serial-delivery enforcement) → PR-04d, **now DISCHARGED** (see
  §Amendment 2026-06-02). Until #1369 landed, the compensation was load-bearing:
  PR-04a's `checkNoEventConsumersWhenSubscriberNil` + the drain require a
  Subscriber, and production wiring (corebundle) carries only the serial in-memory
  bus. #1369 replaces that "must not until it lands" convention with a hard
  bootstrap guard: a concurrent transport (AMQP prefetch>1 / MQTT) wired onto a
  projection now fails fast rather than relying on reviewers. PR-04c (#1368,
  corebundle wiring) is therefore protected by the guard, not by review alone.

§3 Q3 (single subscribe-CU → single projection; a slice may declare multiple
projections) is unchanged: PR-04b derives one `RegisterProjection` per
`projection:`-bearing CU; a slice that lists multiple such CUs produces multiple
projections, each with its own checkpoint.

## Amendment 2026-06-02 (PR-04d #1369 — serial-delivery enforcement landed)

Row 4's compensation ("no concurrent transport may carry a projection until the
enforcement lands") is replaced by a hard bootstrap guard.

**Mechanism.**

- `kernel/outbox.SerialInOrderGuarantor` — an optional Subscriber-implementer
  extension contract (`GuaranteesSerialInOrderDelivery() bool`), mirroring the
  existing `SubscriberIntakeStopper` pattern. A transport opts in to carrying a
  projection by implementing it and returning true.
- `runtime/eventbus.InMemoryEventBus` implements it (single consume goroutine per
  subscription → FIFO in-order). The guarantee is scoped to a single subscriber on
  a `(consumerGroup, topic)`; a projection's group is `"<cellID>-<projectionID>"`
  with exactly one subscription registered by the drain, so the precondition
  holds. AMQP/MQTT do **not** implement it.
- `runtime/bootstrap/phases_projection.go::checkSubscriberGuaranteesSerialDelivery`
  type-asserts the raw wired transport (`s.sub`, not the `contractTracingSubscriber`
  decorator, which wraps rather than embeds and does not forward the marker)
  against the marker. Invoked once from `drainCellProjections` when any projection
  exists; **absence or false → fail-fast** (`ERR_CELL_INVALID_CONFIG`). This is
  fail-closed-by-absence: a future transport that forgets the method is
  auto-rejected for projections rather than silently unsafe.

**AI-robust rating: Medium** (single axis — runtime invariant guard +
fail-closed-by-absence positive-capability marker). The guarded property is the
runtime concurrent-delivery behavior of an injected `outbox.Subscriber` (wired at
the composition root); Go cannot express "this injected interface value
guarantees serial delivery" at compile time, and the marker is self-attestation
(the framework cannot compile-verify its truth). A runtime drain fail-fast +
fail-closed-by-absence + archtest `PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01`
(marker freeze / exact implementer set `{InMemoryEventBus}` / single guard
callsite) is the strongest achievable form — the same permanent ceiling as
`SPAN-SETATTR-HOLDER-SEAL` (#851) / `HEALTHZ-HOLDER-SEAL` (#893). The **Hard**
path — a sealed framework-owned projection-transport token that AMQP physically
cannot construct — requires reworking the `WithSubscriber` injection surface and
has no concrete consumer in v1 (only the in-memory bus is wired); tracked as
gh #1475.

**Threat-matrix re-evaluation (逐行重评).**

- **Row 4** (out-of-order / concurrent delivery): ✅ → **✅ (strengthened)**. The
  discharge moves from a documented convention ("must not ship") to a machine
  guard (bootstrap fail-fast). No regression.
- **Rows 1, 2, 3, 5, 6, 7**: **unchanged**. #1369 adds only a transport-capability
  gate at the projection drain; it touches no checkpoint / replay / rebuild /
  fail-closed / GAP-8 / multi-pod mechanism. Row 7's multi-pod boundary remains a
  documented v1 limitation (distinct from Row 4's intra-pod concurrency, which this
  amendment closes).

## Amendment 2026-06-02 (#960 — PROJECTION-CONSISTENCY-01 → Hard via contractgen codegen funnel, NOT parser jsonschema)

The original plan (§1 scope table + §7 row `PROJECTION-CONSISTENCY-PARSE-TIME-01`)
proposed upgrading PROJECTION-CONSISTENCY-01 to Hard by calling
`jsonschema.Validate` in the `kernel/metadata` parser's contract load path. On
implementation that framing was **rejected** and the rows above are rewritten
(not annotated) to the delivered mechanism — keeping a single truth source per
the ai-robust "amendment 与原文矛盾的段落同 PR 内重写" requirement.

**Why parser-jsonschema is not Hard.** By the AI-robust charter's own tier
table, a parse-time `jsonschema.Validate` is a *runtime guard executed at parse*
= **Medium**: the offending YAML (`consistencyLevel: L2` on a projection) is
fully representable — it is *rejected by a validator*, not made *unrepresentable*.
It also leaves the in-memory vector uncovered: `metadata.ContractMeta` is an open
exported struct, so `ContractMeta{Kind:"projection", ConsistencyLevel:"L2"}` is
constructible in Go without ever touching the parser. A true Hard must close the
authoring vector by impossibility, which a parse-time validator does not.

**Delivered mechanism (Hard).** contractgen emits, into the projection
`types_gen.go` (shared `types.tmpl`, `{{if eq .Kind "projection"}}` block), a
compile-time guard:

```go
import "github.com/ghbvf/gocell/kernel/cellvocab"

const _ = uint(cellvocab.<level> - cellvocab.L3)
```

For L3/L4 the subtraction is ≥ 0; for L0/L1/L2 it is negative and Go rejects the
`uint(...)` conversion at compile time ("constant -1 overflows uint"). A
`codegen=true` projection contract below L3 therefore cannot produce a buildable
tree — the charter's "codegen funnel + compile-error downstream" Hard archetype,
the same shape saga uses ("contractgen builder delegation = Hard 主门控"). The
`buildContractSpec` projection case validates the level *parses* (`cellvocab.ParseLevel`)
so the template never emits a garbage identifier; the `≥ L3` floor is deliberately
left to the compile-time guard (not the builder) to keep the Hard form downstream.
The governance rule `PROJECTION-CONSISTENCY-01` is **retained** as the **Medium**
backstop for `codegen=false` contracts and in-memory ProjectMeta fixtures.

**Scope.** Contracts only (the one place #960 scopes). No parser change, no
kernel-isolation relaxation, no `pkg/jsonschema` wrapper, no `contract.schema.json`
change. The schema's projection if/then enum stays a documentation/IDE layer
(`TestProjectionConsistencyLevelSchemaEnum`); the Hard gate is the codegen funnel.

### Threat-matrix re-evaluation (ai-robust ADR-amendment requirement)

§6 threat matrix is about projection runtime correctness (exactly-once, ordering,
rebuild) and is unaffected by this grading change. The §7 row is rewritten in
place: the rating *improves* (the delivered Hard codegen funnel is strictly
stronger than the rejected Medium parse-time validator); no row flips to ⚠️/❌.

### Optional follow-up (out of scope, not blocking)

`contract.schema.json` lacks a top-level `triggers` property although 17 real
contracts declare `triggers:`. This is harmless today because the parser does
not run `jsonschema.Validate`. If a broad parse-time schema validator is ever
introduced (an independent **Medium** DX improvement, orthogonal to this Hard
gate), `triggers` must be added to the schema first. Tracked at gh #1486.

## Amendment 2026-06-03 (PR-04c #1368 — production journal-backed Cursor + ReplaySource + corebundle wiring)

Row 1's "production journal-backed `Cursor` lands in PR-04c" is now delivered.

**Position source decision.** The harness Cursor/ReplaySource require a 1-based,
monotonic, gap-allowed stream position (cursor.go invariants). `outbox_entries`
had only a UUID `id` (not insertion-ordered) and `created_at` (TIMESTAMPTZ,
collides under concurrent INSERT) — neither is a sound position. Migration 049
adds `seq BIGINT GENERATED ALWAYS AS IDENTITY` + a unique `idx_outbox_seq`,
mirroring `saga_events.version`. A *derived* position (`ROW_NUMBER() OVER (ORDER
BY created_at, id)`) was **rejected**: it is not stable across deletions — when
CleanupPublished/CleanupDead remove earlier rows, every surviving row's number
shifts down, violating the monotonic/stable contract. The IDENTITY column is
assigned at INSERT and never reused; deletions create gaps, which the Cursor
explicitly tolerates (invariant #3). The column is additive/non-destructive (no
TRUNCATE; IDENTITY back-fills existing rows) and the outbox writer is unchanged
(its INSERT omits `seq`; GENERATED ALWAYS auto-assigns).

**Mechanism.**

- `adapters/postgres.PGProjectionReplaySource` (`Replay`/`Head`, read-only) +
  `PGProjectionCursor` (`Position`, holds the replay source and delegates). The
  seq-by-id query lives in exactly one place (the replay source); the cursor
  declares no SQL of its own, so cursor and replay can never disagree about a
  row's position — the exactly-once foundation expressed structurally (single
  sanctioned seq-SQL holder), stronger than a "both read the same column"
  convention.
- Both reconstruct sealed `outbox.Entry` via `EntryScan.ToEntry`
  (`OUTBOX-RECONSTRUCTION-CALLER-01` allowlist extended to the new file) and share
  the relay's JSONB oversize guards via the extracted `applyEntryJSONB`.
- `cmd/corebundle` wires the four `WithProjection*` options from the shared PG
  capability in PG mode only; memory mode leaves them unwired so a projection
  declared without a durable checkpoint store fails fast in the phase6 drain.
- New `PROJECTION-CURSOR-CONFORMANCE-ENROLL-01` archtest (Medium, symmetric with
  the ReplaySource/CheckpointStore enroll siblings) + `RunCursorConformance`
  suite verifying the four cursor.go invariants. `RunReplaySourceConformance` was
  refactored to a seed-persists contract so a read-only production ReplaySource
  needs no test-only Append method.

**Retention boundary — escalated to a HARD GATE (PR #1509 review C1).** The
outbox is a transient relay: CleanupPublished/CleanupDead delete published/dead
rows after retention, but the journal-backed `Cursor`/`ReplaySource` source the
stream position from those same rows. The review correctly sharpened the original
"rebuild-only" framing: it is **not** limited to full rebuild-from-0 — even the
**live** path resolves `Cursor.Position(entry)` by `SELECT seq WHERE id=…`, so any
event whose row was cleaned before the projection consumes it (consume-lag >
cleanup-retention: projection downtime, backlog, or replaying old history)
resolves to a permanent error → the live event is dead-lettered (dropped), and a
rebuild aborts. Reusing a transient relay as a *durable* projection journal is the
wrong foundation; this is the "transient outbox vs retained event store" gap that
§1's "reuse the existing outbox journal" glossed (open-source corroboration: Axon
tracking tokens / Marten high-water marks sit on a retained event store, not on a
publish-transit outbox).

**Compensation (no un-mitigated ⚠️).** Rather than a doc note, the production
wiring now **fails closed**: `cmd/corebundle` does **not** wire the PG
journal-backed reader by default — a projection declared in PG mode then fails
fast in the phase6 drain (`checkProjectionDeps`). The reader is only wired under
an explicit `GOCELL_PROJECTION_PG_JOURNAL_PREVIEW=true` opt-in (dev/preview only,
with a NOT-production-safe startup WARN). So no production projection can silently
run on the unsound foundation. The durable append-only projection journal that
removes the limitation now has an **accepted design** — ADR
`202606071600-1504-adr-projection-event-journal.md` (a dedicated append-only
`projection_events` table, model-a, mirroring #1609's saga journal; this hard
gate + the outbox-backed reader are deleted by its PR-03/D9, after which the
production wiring is durable-by-default and this "fails closed" compensation is
**superseded**). The capability is delivered by that ADR's PR-01..04 (PR-00 is
the ADR only); the real PG e2e rebuild test T-06-2 is unblocked once its PR-04
lands. (Per-spec
replay filtering — #1482 — has since **landed** at the Coordinator level,
decoupled from the PG reader and the durable journal; see §Amendment 2026-06-04.)
The review also hardened the adapter: a schema_guard
IDENTITY guard on `seq` (F5), a bounded-ctx position lookup (F6), rebuild ctx
identity restore (F4), and a no-duplicate conformance assertion (F7).

### Threat-matrix re-evaluation (ai-robust ADR-amendment requirement — 逐行重评)

- **Row 1** (exactly-once / production Cursor): ✅ → **✅ (delivered, gated)**. The
  compare/skip mechanism is unchanged; PR-04c supplies the production position
  source, and the cursor↔replay single-holder structure makes position agreement
  structural rather than conventional. The transient-journal risk (cleaned row →
  live event dropped / rebuild abort) is **not** an un-mitigated regression: the
  corebundle hard gate keeps the PG reader off by default (fail-fast if a
  projection is declared), so no production projection runs on it until the
  durable journal lands. That journal now has an **accepted design** — ADR
  `202606071600-1504-adr-projection-event-journal.md` (dedicated append-only
  `projection_events`, position from the row's own `global_seq`, never cleaned).
  When its PR-03 wires that source by default, Row 1 **strengthens** transient →
  durable (position no longer sourced from a relay-deletable row); it does **not**
  flip to ⚠️/❌. See the Retention-boundary compensation above.
- **Row 2** (crash recovery): unchanged. Checkpoint persistence semantics are
  independent of the position source; resume-at-offset+1 now runs over a durable
  `seq` (within the retention window).
- **Row 3** (rebuild read consistency): unchanged (Phase()/readyz, PR-03).
- **Row 4** (out-of-order / concurrent delivery): unchanged — the serial-delivery
  guard (PR-04d) still gates which transport may carry a projection; PR-04c adds
  no concurrency vector (Replay reads outside any tx; Position is an immutable
  indexed lookup).
- **Row 5** (fail-closed): unchanged.
- **Row 6** (GAP-8 boundary): unchanged — `seq` is added to the framework-owned
  outbox table, not to any business read-model schema.
- **Row 7** (multi-pod boundary): unchanged (v1 single-pod; owner column still
  reserved/unwritten).

No row flips to ⚠️/❌. The retention boundary is recorded as a new known v1
limitation above, with a backlog follow-up, per the no-silent-deferral rule.

## Amendment 2026-06-03 (PR-04e #1370 — HTTP rebuild endpoint landed; framework-mount, not cellgen)

The rebuild control-plane endpoint frozen in §5 landed. Its **mechanism** changed
from the original "per-cell `contract.yaml` + cellgen handler + host cell to
satisfy `DEAD-CONTRACT-01`" framing to a **framework-owned RouteGroup mounted by
bootstrap** (the §5 forward-contract text is rewritten in place to match). The
**auth model, network boundary, path, and 202·409·404 status semantics are
unchanged** — only the carrier moved.

### Why framework-mount (open-source benchmark + GoCell consistency)

Across mature CQRS/event-sourcing systems the dominant pattern is a **generic
framework/server-provided control plane keyed by projection name**, not a
per-application endpoint: EventStoreDB `POST /projection/{name}/command/reset`
(server-mounted admin HTTP, name is a path param), Axon Server's generic
processor-reset primitives, Marten's `projections rebuild` CLI by name. GoCell's
own health endpoints (`/healthz`·`/readyz`·`/metrics`) already use exactly this
shape: `contractbuild.NewFrameworkHTTP` builds an `http.framework.*` ContractSpec
and bootstrap mounts a framework-owned RouteGroup (`runtime/bootstrap/health.go`),
with **no `contract.yaml`, no codegen, no host cell, and no `DEAD-CONTRACT-01`
participation** (that invariant scans `contracts/*.yaml`, of which there is none
here). The rebuild endpoint is the same kind of framework control-plane surface,
so it reuses that pattern rather than inventing a per-cell contract — eliminating
the host-cell/`DEAD-CONTRACT-01` complication the original framing introduced.

### What landed

- **Endpoint**: `POST /internal/v1/{cell}/projection/{name}/rebuild`, mounted on
  the `cell.InternalListener` by `phase5CollectRouteGroups`, opt-in via
  `bootstrap.WithProjectionRebuildEndpoint(allowedCallers...)`. phase0
  (`validateProjectionRebuildEndpoint`) fails fast when opted in without an
  InternalListener. The handler dispatches by `{cell}/{name}` to
  `b.projectionCoordinators` (the phase6 drain registry, read lazily at request
  time — phase6 runs after phase5 mount but before serving).
- **Auth** (unchanged from §5): `/internal/` + service-token listener chain +
  caller-cell allowlist. The allowlist rides on `ContractSpec.Clients` via the
  extended `contractbuild.NewFrameworkHTTP(id, method, path, clients...)`; an
  `/internal/` path REQUIRES non-empty `Clients` (`ContractSpec.validateHTTP`
  fail-closed — every internal API names its callers), so `auth.Mount`
  auto-injects `RequireCallerCell`. 404 uses the new `errcode.ErrProjectionNotFound`.
- **Kernel surface**: `Coordinator.Snapshot(ctx) (Snapshot, error)` returns the
  `{phase, pendingEvents, replayLagSeconds}` 202 body. Phase is always populated
  (in-memory); pending/lag go through the shared **pure** `computeLagPending`
  helper that the lag readyz probe (`checkLag`) also calls — so the wire snapshot
  can never diverge from the probe (single-source, type-system Hard). A degraded
  snapshot read after admission is logged but never downgrades the 202 (the
  rebuild was already admitted). The bootstrap handler consumes the Coordinator
  through a narrow consumer-local interface (`runtime/bootstrap.rebuildController`
  = `Rebuild` + `Snapshot`; `*Coordinator` satisfies it) — declared at the
  consumer per Go idiom, keeping kernel/projection's exported surface free of a
  bootstrap-only seam.

### Deferred (scope carve-out)

A purer **operator-credential admin surface** (EventStoreDB-style: a dedicated
network-isolated `cell.AdminListener` + an operator-credential `ListenerAuth`,
off `/internal/`) is a cross-cutting auth foundation beyond this endpoint — it
needs a new sealed listener class + a new `ListenerAuth` implementer (operator
basic-auth exists only as the per-cell setup/admin middleware today, FMT-28
restricted). Tracked at **gh #1505**; rebuild migrates to it if/when it lands.
Until then `/internal/` + caller-cell allowlist (GoCell's existing structural
invariant for internal endpoints) is the correct, no-new-foundation carrier.

### Threat-matrix re-evaluation (ai-robust ADR-amendment requirement)

No row flips to ⚠️/❌. Row 3 (rebuild-period read consistency) was already
discharged by `Phase()` + readyz in PR-03; the HTTP trigger is a convenience
surface, not a threat-discharge mechanism (a rebuild is equally triggerable via
`Coordinator.Rebuild`). The discharge mechanism is unchanged by moving the trigger
from a hypothetical cellgen handler to a framework-mounted RouteGroup. Auth is
unchanged (service-token + caller-cell allowlist), so the endpoint's exposure
surface is identical to the §5 freeze. No new threat row is introduced.

## Amendment 2026-06-04 (PR-04g #1372 — funnel Hard-ization closed won't-do)

PR-04g was scoped (Amendment 2026-05-31) as "funnel Hard-ization (cellgen-only
sealed token)" for `PROJECTION-APPLY-HOOK-FUNNEL-01` / `PROJECTION-REGISTER-FUNNEL-01`,
both Medium/Medium. On execution the proposed mechanism was found **not
expressible in Go**, so #1372 is **closed won't-do** (a documentation/governance
reclassification, no code change).

**Why the sealed token cannot exist.** A "cellgen-only sealed token" would gate
`reg.RegisterProjection` / `Coordinator.Subscribe` to callers that hold a value
only cellgen can construct. But cellgen emits these calls into the **cell's own
package** — the generated `cell_gen.go` (`func (c *Cell) Init(...)`) sits beside
the hand-written `cell.go`. Go has no compile-time identity for "generated code";
a hand-written file in the same package can call any constructor the generated
file can. Sealing construction to a *package* (the `internal/topicns` #1247
precedent) cannot separate generated from hand-written files of one cell. This is
the identical permanent Go-language ceiling already documented for `Subscribe` /
`RegisterWebhookReceiver` and the holder-seal family **#851 / #893 / #1282**: a
caller-identity allowlist enforced by archtest (CI fail-closed) is the strongest
achievable upstream form. The §7 rows for both funnels are reworded accordingly.

**What stands unchanged (Hard).** The load-bearing **raw-infra-stays-in-bootstrap**
property remains Hard (type system): cells hold only sealed markers and cannot
construct `CheckpointStore` / `TxRunner`, so the §7 "Hard complement" is untouched.
The two caller allowlists keep their Medium downstream/upstream ratings; nothing
about runtime behavior or the generated wiring changes.

**Note on the sibling #1475.** `PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01` (§7,
Amendment 2026-06-02) is a *different* ceiling — its Hard path (a sealed
framework-owned serial-transport token) *is* expressible but has no concrete
concurrent-transport consumer in v1, so it stays **deferred (open) at gh #1475**,
not won't-do. The two are not conflated.

### Threat-matrix re-evaluation (ai-robust ADR-amendment requirement)

No row flips. This amendment only reclassifies the *upgrade prospect* of two
already-Medium enforcement mechanisms from "pending Hard-ization" to "permanently
Medium (won't-do)". Both archtests remain active and CI fail-closed; their
allowlists, implementations, and enforcement behavior are unchanged, as are the §6
threat discharges they back. The move is `pending → unreachable`, not `✅ → ⚠️/❌`.

## Amendment 2026-06-04 (PR #1505 — operator control-plane migration)

The projection rebuild HTTP trigger — authored by PR-04e (#1370) as a
framework-owned RouteGroup on the **InternalListener** at
`POST /internal/v1/<cellID>/projection/<projectionID>/rebuild` with service-token
auth + a caller-cell allowlist — **migrated to the operator admin plane**: a new
`cell.AdminListener` (loopback-isolated admin port) carrying an `auth.AuthOperator`
operator-credential gate, at `POST /admin/v1/projection/<cellID>/<projectionID>/rebuild`.

**Why.** A projection rebuild is an **operator→system** action (an administrator
or deployment pipeline triggers it), not a **cell→cell** business call. The
`/internal/v1/*` + service-token + caller-cell allowlist model is the cell→cell
control-plane shape; using it for an operator action was a semantic coincidence.
The EventStoreDB projection admin API (a network-isolated admin port + operator
basic-auth credentials) is the right-sized benchmark. This lands the "deferred
operator-credential admin surface" §5 / §Amendment 2026-06-03 already anticipated.
Full decision + threat matrix: ADR
`202606041200-1505-adr-operator-control-plane-auth.md`.

**What changed (§5 rewritten in-place above).**

- Listener: `cell.InternalListener` → new `cell.AdminListener` (`127.0.0.1:9093`
  default; loopback isolation + operator credentials = defense in depth).
- Path: `/internal/v1/{cell}/projection/{name}/rebuild` →
  `/admin/v1/projection/{cell}/{name}/rebuild`.
- Auth: service-token + caller-cell allowlist → `auth.AuthOperator` (HTTP Basic
  Auth over env credentials `GOCELL_OPERATOR_ADMIN_*` + per-IP rate limit +
  constant-time compare). No caller-cell allowlist (operator has no caller cell);
  the admit-time audit log drops the `caller_cell` field accordingly.
- Opt-in: `WithProjectionRebuildEndpoint(callers...)` → `WithProjectionRebuildEndpoint()`
  (bool); phase0 now requires a `cell.AdminListener`.

**What is unchanged.** The framework-owned-RouteGroup mechanism (no `contract.yaml`,
no host cell, never reaches `DEAD-CONTRACT-01`), the async `202`/`409`/`404` status
set, the `{"data":{phase,pendingEvents,replayLagSeconds}}` envelope, and the
programmatic `Coordinator.Rebuild`/`Snapshot` surface.

**Threat-matrix re-evaluation (ai-robust ADR-amendment requirement).** No row
flips in this ADR's §6. Row 3 (rebuild-period read consistency) is discharged by
`Phase()` + readyz and is **independent of the trigger's auth/listener** — the
migration changes *who may reach the trigger*, not *how reads stay consistent*.
The control-plane access-model change (operator-credential gate replacing
service-token + caller-cell allowlist) is a *different* threat surface, matrixed
in the #1505 ADR (operator credential brute-force → per-IP rate limit + constant-
time compare; admin-path ↔ AdminListener affinity → bidirectional router check;
operator-only-on-Admin / Admin-requires-operator → phase0 fail-fast). The
`AuthOperator` plan and `AdminListener` ref inherit the existing sealed
`ListenerAuth` interface + closed `ListenerRef` enum (type-system Hard), so no new
Soft mechanism is introduced.

## Amendment 2026-06-04 (#1482 — per-spec replay filtering landed + multi-stream fan-in reference)

**What landed.** Two things, one structural and one in the reference example:

1. **Per-spec replay filtering at the Coordinator** (`kernel/projection/rebuild.go`).
   The bootstrap ReplaySource is a single whole-journal source shared by every
   projection Coordinator (the Row 1 / §Amendment 2026-06-03 contract). During a
   rebuild, the replay loop invokes the business `applyOne` **only** when
   `entry.RoutingTopic() == c.spec.Topic` — matching the same key live delivery is
   topic-routed on — so the business Apply never sees a foreign stream and needs
   no defensive topic check. Foreign entries route through `advanceOffsetPastForeign`,
   which advances the checkpoint **without** applying. Advancing over foreign is a
   correctness requirement, not an optimization: a rebuild's catchup must drain the
   checkpoint over the journal gap (own applied + foreign advanced) so it reaches
   the catchup cutoff even when foreign entries sit in the range. Filtering thus
   gates the Apply, not the checkpoint.

   The replay loop is the single shared funnel `drainGap(ctx, from, through)`:
   `replayPhase` drains `(0, head0]`; `catchupPhase` drains `(head0, head1]` where
   `head1` is the source `Head` captured at catchup start. Both run on the rebuild
   goroutine with the gate **shut**, so the rebuild goroutine is the SOLE applier
   and the replay→live handoff is strictly sequential.

   **Catchup termination (corrected during #1574 review).** An earlier framing of
   this amendment claimed catchup termination was discharged because "`catchupPhase`
   compares the checkpoint against the whole-journal `Head`" while the foreign
   advance keeps the checkpoint moving. That reasoning was incomplete — it only
   covered foreign entries inside the replay range `[0, head0]`. A foreign entry
   appended *during* catchup raises the whole-journal `Head`, but the prior catchup
   polled `checkpoint >= Head` with the gate **open** and relied on the
   topic-routed live handler to advance the checkpoint; the live handler never sees
   foreign streams, so the poll spun forever (the #1574 C1 hang). The fix makes
   catchup a **bounded self-drain** of `(head0, head1]` (`drainGap`, gate shut,
   advance past foreign), and only **then** opens the gate; the residual
   `(head1, now]` tail is consumed by live delivery. This matches the Marten
   async-daemon (high-water gap skip) + Axon streaming-processor (sequential
   replay-then-live, no replay+live dual-applier path) consensus, and — crucially —
   keeps a **single applier**, preserving the serial-delivery precondition (Row 4).
   codex's proposed open-gate catchup gap-scan was **rejected**: it would run the
   scan while the live handler also consumes, giving two concurrent appliers →
   double-apply, violating that precondition.

2. **Multi-stream fan-in reference** (`examples/todoorder` orderprojection). The
   status-summary read model is now fed by **two** independent single-stream
   projections — `order_status` (order-created) + `order_transition`
   (order-status-changed) — each owning a **disjoint** sub-view (created vs latest)
   with its own checkpoint and `onReset`, composed at query time (latest wins).
   This is the sound realization of the §Amendment 2026-06-02 fan-in clarification:
   disjoint sub-views avoid the reset-互清 hazard a naive shared mutable store
   would have. `event.order-status-changed.v1` flips `draft → active` (publisher
   orderconfirm + the new subscriber satisfy ADV-05 / DEAD-CONTRACT-01).

**Why Coordinator-level, not ReplaySource-level.** The #1482 issue framed the
filter as "global ReplaySource filters replay by spec". Filtering at the
Coordinator (which holds `c.spec`) instead keeps the `ReplaySource` interface
**unchanged** — no churn to the Cursor/ReplaySource/CheckpointStore conformance
suites or their enrollment archtests, and the source stays a pure whole-journal
reader (its documented contract). It also works identically for Mem and PG
sources without the issue's stated #1368 dependency. Pushing the topic filter
down into PG SQL (`WHERE topic = $1`) is a future efficiency optimization for the
PG source only; it is **deferred with a real blocker** — the PG journal-backed
reader is gated off by default (preview-only) and not durably sound until the
retained projection journal (#1504 P1) lands, so SQL-level pruning would optimize
a path not yet production-active.

**AI-robust.** The per-spec filter is a new framework invariant the example's
fan-in soundness (and rebuild termination) depends on, so it ships the three-piece
closure (static guard + documented contract + regression test): archtest
`PROJECTION-REPLAY-PER-SPEC-FILTER-01` form-locks, inside the single `drainGap`
funnel, BOTH (a) the `applyOne` callsite inside the topic gate AND (b) the
`advanceOffsetPastForeign` callsite **outside** the gate (the foreign
fall-through) — plus a `sawForeignAdvance` anti-vacuity fatal so deleting the
foreign advance reds CI; `drainGap`/`advanceOffsetPastForeign` godoc + this
amendment are the contract; `TestRebuild_PerSpecTopicFilter` +
`TestRebuild_ForeignDuringCatchup` (kernel) + `TestOrderProjection_FanInLifecycle`
(example) are the regression tests. Because both phases route through `drainGap`,
the archtest covers replay AND catchup. Rating: **Medium** (single archtest, not a
type-system double-lock). The gate is NAME-ANCHORED AST containment — the
`applyOne` CallExpr must sit inside the `entry.RoutingTopic() == c.spec.Topic`
IfStmt body and `advanceOffsetPastForeign` outside it (token.Pos containment +
structural-name gate form), the same dominance-lite shape as the Medium
`CHANGEPASSWORD-INACTIVE-GATE-01`; it is NOT typed callsite-uniqueness and NOT
CFG dominance, so it is not Hard. (Business code being unable to reach the rebuild
apply at all is incidental Go visibility — `applyOne` is private — not what this
rule enforces; the rule enforces that the FRAMEWORK keeps the gate.) Hard path
(won't-do now, over-engineering for one call site): `TypesInfo.ObjectOf` typed
resolution of `applyOne` à la `SAGA-STEP-RUN-OUTSIDE-TX-01` A1 + CFG/SSA dominance
— the #851 / #893 / #1282 / CHANGEPASSWORD-#1212 ceiling family. The
disjoint-sub-view discipline in the example is guarded by its own unit/cell tests
(a single reference, not a cross-cutting constraint), so it is not in the
AI-robust archtest scope.

### Threat-matrix re-evaluation (ai-robust ADR-amendment requirement — 逐行重评)

- **Row 1** (exactly-once): ✅ → **✅** (per-spec filtering delivered). The
  compare/skip mechanism is unchanged for own-stream entries; foreign entries now
  skip the Apply but still advance the checkpoint via `advanceOffsetPastForeign`,
  which keeps the per-projectionID checkpoint monotonic and catchup-terminating.
  The whole-journal-replay deferral noted in §Amendment 2026-06-03 is discharged.
- **Row 2** (crash recovery): unchanged — checkpoint persistence is independent of
  the apply filter.
- **Row 3** (rebuild read consistency): unchanged — `Phase()` is per-Coordinator;
  the example's two projections each have their own Coordinator/Phase.
- **Row 4** (out-of-order / serial-delivery): unchanged — the filter is intra-
  Coordinator and adds no concurrency vector; the per-projectionID serial-delivery
  precondition (PR-04d) still holds per stream. The #1574 catchup fix deliberately
  drains `(head0, head1]` with the gate **shut** so the rebuild goroutine is the
  sole applier (no concurrent live applier); an open-gate catchup gap-scan would
  have introduced a second applier and violated this row, which is why it was
  rejected.
- **Row 5** (fail-closed): unchanged — `advanceOffsetPastForeign` keeps the same
  1-based / `pos <= current` guards and runs in the rebuild's `RunInTx`.
- **Row 6** (GAP-8 boundary): unchanged — no new framework constraint on the
  business read-model schema; the disjoint-sub-view composition is business code.
- **Row 7** (multi-pod boundary): unchanged (v1 single-pod).
- **Row 8 (NEW — introduced by #1482, fix corrected in #1574)** — *whole-journal
  replay interleaving / catchup termination*: because one whole-journal
  ReplaySource feeds every Coordinator, a rebuild sees foreign streams interleaved
  with its own, and a foreign entry **at any position — including one appended
  during catchup** — must not strand catchup. The initial #1482 discharge (foreign
  advance during replay + an open-gate `checkpoint >= whole-journal Head` poll in
  catchup) was **incomplete**: it only handled foreign entries inside `[0, head0]`;
  a foreign entry arriving during catchup raised `Head` while the topic-routed live
  handler could never advance the checkpoint past it, so the poll spun forever
  (#1574 C1, P1). **Discharged** by making catchup a bounded gate-shut self-drain
  of `(head0, head1]` via `drainGap` (own applied + foreign advanced), which
  reaches `head1` regardless of foreign entries in the range, then opens the gate;
  the residual `(head1, now]` tail is consumed by live delivery. Covered by
  `TestRebuild_ForeignDuringCatchup` (foreign-arrives-during-catchup, the case the
  pre-fix poll hung on) + `TestRebuild_PerSpecTopicFilter` (trailing-foreign in
  replay range) + `TestAdvanceOffsetPastForeign_ErrorBranches`, and form-locked by
  `PROJECTION-REPLAY-PER-SPEC-FILTER-01`'s foreign-advance assertion. This is a v1
  row that did not exist before per-spec filtering; after the #1574 correction it
  is ✅, not ⚠️/❌.

No row flips to ⚠️/❌; the one new row (Row 8) lands ✅ after the #1574 catchup
correction.
