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
| **PROJECTION-CONSISTENCY-01 → Hard** | **new** — parser load-time `jsonschema.Validate` (covers #960) |
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

### Q3 — kind:projection codegen funnel shape → **A** (single slice → single projection)

- **Options.** A: one slice declares one projection (its subscribe set is that
  projection's input stream). B: multiple slices share one projection (needs a
  new metadata node).
- **Argument.** A covers both roadmap-committed scenarios (winmdm
  `unified_device_id`, zerotrust `trustscore`). B is a v1.1 extension point;
  building the multi-slice metadata node now is speculative generality.
- **Decision.** A. cellgen derives the projection wiring from a single slice's
  `contractUsages[role=subscribe]` (PR-04).
- **Risk.** A projection that genuinely needs to fan in from multiple event
  streams must, in v1, route them through one slice's subscribe set — a single
  slice may list multiple `contractUsages[role=subscribe]` entries (the existing
  cellgen Subscribe loop already handles N contracts per slice), so v1 fan-in is
  expressible without the multi-slice node. **Rollback
  condition:** v1.1 adds a multi-slice projection metadata node; no v1 data
  migration needed (the checkpoint key is `(cell_id, projection_id)`, agnostic
  to slice count).
- **ref:** Axon processor-to-event-handler grouping.

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

### Rebuild control-plane endpoint (PR-03 forward contract)

The rebuild trigger is an internal HTTP endpoint. Its `contract.yaml` + handler
land in **PR-03 (T-03-3)**; this section freezes the forward contract so PR-03
does not drift and `/internal/v1/` declaration requirements
(`.claude/rules/gocell/go-standards.md` §安全检查点) are satisfied at design time:

- **Endpoint**: `POST /internal/v1/<cellID>/projection/<projectionID>/rebuild`.
- **Caller / auth**: service-token authentication + caller-cell allowlist (the
  caller's cell ID must be in the contract's `clients` allowlist). No public
  access. Same shape as other `/internal/v1/` control-plane endpoints.
- **Network boundary**: internal-only — never mounted on a public listener.
- **HTTP semantics**: `202 Accepted` (rebuild is async — the state machine runs
  in the background; the response acknowledges acceptance, not completion) /
  `409 Conflict` (a rebuild is already in progress for this projection;
  `Phase() != PhaseLive`) / `404 Not Found` (unknown cell/projection).
- **Response envelope**: the unified `{"data": {...}}` shape
  (`.claude/rules/gocell/error-handling.md`); the `data` object carries the
  current `{phase, replayLagSeconds, pendingEvents}` snapshot. Errors use the
  shared error envelope.

The full `contract.yaml` (request/response schema, `clients` allowlist) is
authored in PR-03; PR-00 freezes only the auth model + network boundary + status
semantics above.

> **Amendment 2026-05-31 (PR-03 #1175) — HTTP endpoint moved to PR-04.** The
> rebuild *trigger* is a per-cell HTTP endpoint, but a GoCell contract is
> per-cell (fixed `path` + `ownerCell` + `clients`) and PR-03 ships only the
> kernel/runtime harness — there is **no host cell or internal listener** to
> mount it on until cellgen `kind: projection` derivation lands (PR-04, #1176;
> todoorder migration in PR-06). A platform-level `active` contract with no cell
> impl would trip `DEAD-CONTRACT-01`. Therefore:
>
> - **PR-03 freezes the programmatic trigger surface**: `Coordinator.Rebuild(ctx)
>   error` (admission CAS `PhaseLive→PhaseStopped`; returns
>   `projection.ErrRebuildInProgress` when a rebuild is already running — the
>   value the HTTP adapter will map to **409**; runs the state machine on a
>   background goroutine — the **202** semantics) + `Coordinator.Close(ctx)` for
>   graceful drain. The `{phase, replayLagSeconds, pendingEvents}` snapshot is
>   computed by the readyz probe / metrics path that **does** land in PR-03.
> - **PR-04 authors the HTTP wrapper**: the `POST /internal/v1/<cell>/projection/
>   <name>/rebuild` contract.yaml + per-cell handler (cellgen output) calling
>   `Coordinator.Rebuild`, with the **unchanged** auth model / network boundary /
>   202·409·404 status semantics frozen above.
>
> **Threat-matrix re-evaluation (per ai-robust.md "ADR amendment 落地必查").** No
> cell flips: row 3 (rebuild-period read consistency) is discharged by `Phase()`
> + readyz, **both delivered in PR-03** as planned — the HTTP *trigger* is a
> convenience surface, not a threat-discharge mechanism (a rebuild is equally
> triggerable via `Coordinator.Rebuild`). The forward contract above is
> unchanged; only its *delivery PR* moves PR-03→PR-04.

## 6. Threat matrix

Each row names the threat, the v1 mechanism, and **which PR discharges it**
(PR-00 freezes the contracts; behavior is proven downstream — answering "can an
ADR-only PR be verified": the type-level decisions are verified now, behavior is
verified by the listed PR).

| # | Threat | v1 mechanism | Discharged by |
|---|---|---|---|
| 1 | **exactly-once** (apply runs once per offset) | apply + `SaveOffset` in one `CellTx` (Q1); the harness compares each event's stream position (from the `Cursor` contract defined in PR-01 — not an `outbox.Entry` field) against the stored checkpoint and skips when ≤ checkpoint | PR-01 defines the `Cursor` contract and verifies the compare/skip logic with a test fake (cold-start / out-of-order / forward-gap unit tests). The production journal-backed `Cursor` lands in PR-04 (#1176, where cellgen wiring first needs a concrete Cursor); PR-06 real-PG integration |
| 2 | **crash recovery** (no replay window after restart) | checkpoint persisted in the apply tx; restart loads checkpoint, resumes at offset+1 | PR-01 crash-recovery unit test; PR-06 real-PG integration (kill → restart) |
| 3 | **rebuild-period read consistency** | non-blocking by design (§5); `Phase()` lets business opt into 503; stale read is a business concern | PR-03 `Phase()` + readyz; ADR §5 contract. See §5 Amendment 2026-05-31: the HTTP trigger moved to PR-04 but the Phase()/readyz discharge mechanism lands in PR-03 as planned. |
| 4 | **out-of-order / concurrent delivery** (broker redelivery-reorder OR intra-consumer-group concurrency, e.g. AMQP prefetch>1 dispatching a goroutine per delivery) | checkpoint is monotonic; a redelivered/late event whose replay-cursor position ≤ checkpoint does NOT invoke apply (exactly-once delivery to apply); ConsumerBase's Claimer idempotency layer (keyed per event-ID) sits above the Coordinator as defense-in-depth. **PRECONDITION (PR-01 amendment):** this skip is only sound under STRICTLY SERIAL, IN-ORDER delivery of the stream — a single consumer group does NOT provide it. Under concurrent delivery a higher position can commit the checkpoint before a lower position is applied, silently dropping the lower event's distinct apply (projection gap). This is distinct from row 7's multi-pod boundary (it bites within a single pod via prefetch>1). The per-event-ID Claimer does NOT serialize positions, so it gives no protection here. **Compensation:** v1 is safe because cmd/* wires only the serial in-memory bus (`runtime/eventbus`, single-goroutine consume); serial-delivery enforcement (prefetch=1 / single-goroutine dispatch for projection subscriptions) is a HARD prerequisite of the production wiring in PR-04 — no concurrent transport may carry a projection subscription until it lands. | PR-01 reorder-hazard characterization unit test (`TestCoordinator_ReorderDropsLowerPosition`) + `applyOne` `pos<1` guard + doc.go "Ordering precondition"; **serial-delivery enforcement deferred to PR-04 (#1176)** |
| 5 | **fail-closed** (checkpoint store failure) | `SaveOffset` failure rolls back the whole `CellTx` (apply not committed); Coordinator requeues; never advances offset past an un-applied event | PR-01 fail-closed unit test |
| 6 | **GAP-8 boundary** (harness must not prescribe read-model schema) | CellTx-offset design touches only the framework offset table; apply body + read-model schema stay business-owned (§4) | This PR (§4 record) + PR-02 schema review |
| 7 | **multi-pod concurrency (v1 boundary)** | v1 single-pod (Q5); `owner` column reserved, **write-guarded** (reads harmless/unused); multi-pod safety = upper-layer leader election; **2+ replicas without leader election is unsafe in v1** | PR-02 `owner`-reserved archtest (write-scoped, landed); `schema_guard.verifyDefaults` asserts the load-bearing `owner DEFAULT ''`; documented v1 limitation (Q5) |

## 7. AI-robust ratings

The epic introduces five enforcement mechanisms. Ratings follow
`.claude/rules/gocell/ai-robust.md`; symbol inventories live in each archtest's
package godoc (not duplicated here).

| ID | PR (stub → green) | Funnel direction / rating | Hard-template (§"Hard 范本目录") |
|---|---|---|---|
| **PROJECTION-STATE-PHASE-FROZEN-01** | PR-00 (green now) | n/a (membership freeze) — **Medium** (AST const-set + String-arm lock; Go enums are not reflectable as a set, so an AST/golden lock is the ceiling — same shape as `OUTBOX-STATE-TRANSITION-COMPLETENESS-01`). The orthogonal "zero value invalid" guarantee is Hard via `iota+1` + `Phase.Valid()` (type system). | enum-set golden lock |
| **PROJECTION-APPLY-HOOK-FUNNEL-01** | PR-01 active (genuinely-green) → PR-04 cellgen callsite | 下游 **Medium** / 上游 **Medium** — archtest caller-allowlist locks `Coordinator.Subscribe` callers to {kernel/projection self, `_test.go`, cellgen `DO NOT EDIT`}. **Correction (PR-01 amendment):** the original "下游 Hard" overstated the achievable ceiling — Go cannot type-system-gate who calls a *public* method, and `Subscribe` must stay public for cellgen, so the archtest caller-identity allowlist is the strongest achievable form (same shape and **Medium** rating as `HEALTHZ-WRITE-01` A2). True Hard downstream would require a cellgen-only sealed-token parameter, which would change the PR-00-frozen `Subscribe` signature; deferred to PR-04 (#1176). Transitional Medium → gh #1100 / #1176 tracked (named in the archtest godoc). | single sanctioned holder / caller-identity allowlist |
| **PROJECTION-CHECKPOINT-TX-BOUND-01** | PR-01 stub → PR-02 green | **Medium** (`SaveOffset` impl must obtain tx via `persistence.TxFromContext`; raw `db.Exec` / `*sql.Tx` form fails). Rides the existing `PG-REPO-AMBIENT-TX-01` Hard funnel for the PG adapter. | typed-param / ambient-tx form |
| **PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01** | PR-02 | **Medium** (SQL-literal scan rejects `owner` in INSERT/UPDATE write paths; v1 scope — removed in the same PR that enables v1.1 claim). | input-struct field exclusion (SQL-write variant) |
| **PROJECTION-CONSISTENCY-PARSE-TIME-01** | PR-05 | 下游 **Hard** (parser load path must call `jsonschema.Validate`; callsite identity locked) — upgrades #960 from Medium governance rule to Hard parse-time gate. | codegen/parse funnel + callsite identity |

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
