// Package projection declares the kernel-level contracts for the CQRS
// projection lifecycle harness: the business event→state Apply hook, the
// CheckpointStore offset abstraction, the MemCursor / MemReplaySource demo
// helpers, the functional Option seam, and the rebuild-lifecycle Phase enum.
//
// # Scope
//
// L3 (WorkflowEventual): a projection consumes a cell's event stream and
// maintains a derived read-model. The harness owns the mechanical parts —
// checkpoint/offset persistence, exactly-once delivery to Apply, rebuild
// orchestration, metrics, readyz — so business cells only write the Apply
// function body and (optionally) an OnReset hook. The read-model schema and the
// Apply body itself stay business-owned (GAP-8 seal; see ADR §4).
//
// # Delivery status (PR-03 landed)
//
// PR-01 delivered Coordinator + Subscribe + consume loop + checkpoint write
// (kernel/projection/coordinator.go), MemCheckpointStore, and the projectiontest
// conformance harness. PR-02 delivered the postgres CheckpointStore adapter
// (adapters/postgres). PR-03 has delivered the rebuild state machine (Phase
// enum, Rebuild/Close lifecycle), MemReplaySource + ReplaySource interface,
// metrics, readyz probes (Probes() returning store-ready + lag probes), and the
// PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01 archtest. Remaining items:
//
//   - PR-04a (landed): the reg.RegisterProjection record-only seam + the
//     bootstrap projection drain — the single sanctioned Coordinator.Subscribe
//     callsite is now runtime/bootstrap/phases_projection.go (Option A, ADR
//     §Amendment 2026-05-31). cellgen emits record-only reg.RegisterProjection,
//     never Coordinator.Subscribe (raw infra may not reach cell code).
//   - PR-04b: cellgen kind:projection derivation of reg.RegisterProjection.
//   - PR-04c: production journal-backed Cursor + ReplaySource.
//   - PR-04e (landed; migrated to the operator admin plane by #1505): the
//     framework-owned HTTP rebuild endpoint is POST
//     /admin/v1/projection/<cell>/<name>/rebuild on the loopback
//     cell.AdminListener, gated by an auth.AuthOperator operator-credential
//     ListenerAuth (no caller-cell allowlist). It was originally framed for the
//     InternalListener (POST /internal/v1/<cell>/projection/<name>/rebuild,
//     service-token + caller-cell); #1505 moved it because a rebuild is an
//     operator→system action, not cell→cell (see ADR 202606041200-1505 and
//     202605261620 §Amendment 2026-06-04). It stays a framework-owned RouteGroup
//     (no contract.yaml, never trips DEAD-CONTRACT-01).
//
// Subscribe's frozen forward signature (projectionID moved to NewCoordinator
// in PR-03):
//
//	func (c *Coordinator) Subscribe(
//		ctx context.Context,
//		spec contractspec.ContractSpec,
//		apply Apply,
//		opts ...Option,
//	) error
//
// # Ambient transaction model
//
// Apply and CheckpointStore take ctx only and obtain the transaction ambiently
// via persistence.TxFromContext — the established GoCell idiom (mirrors
// outbox.Writer.Write and is enforced by PG-REPO-AMBIENT-TX-01). The Coordinator
// wraps each event in persistence.TxRunner.RunInTx so the Apply mutation and the
// checkpoint SaveOffset commit in one transaction (exactly-once). Decided in
// ADR §3 Q1/Q2 against an explicit tx-handle parameter.
//
// # Ordering precondition (exactly-once requires serial in-order delivery)
//
// The exactly-once guarantee rests on a cumulative monotonic checkpoint: applyOne
// skips any event whose Cursor position is ≤ the stored checkpoint. This is only
// SOUND when the projection's stream is delivered strictly serially and in order.
// Under concurrent delivery (the production AMQP subscriber dispatches one
// goroutine per delivery, prefetch defaulting to 10) or broker redelivery-reorder,
// a higher position can commit the checkpoint before a lower position is applied;
// the lower event's distinct Apply is then silently skipped (pos ≤ checkpoint),
// leaving a projection gap. A single consumer GROUP does NOT by itself provide
// this ordering — it only prevents cross-cell fanout.
//
// This serial in-order delivery is now ENFORCED at the bootstrap projection
// drain (PR-04d, #1369): a transport opts in by implementing
// outbox.SerialInOrderGuarantor and returning true; the drain rejects wiring a
// projection onto any subscriber that does not (fail-closed-by-absence). Only
// runtime/eventbus.InMemoryEventBus qualifies today (single-goroutine consume);
// AMQP/MQTT (concurrent dispatch) fail fast rather than silently dropping
// positions. This intra-consumer-group ordering precondition is distinct from
// the multi-pod boundary below. See ADR §6 threat row 4 + §Amendment 2026-06-02.
//
// # v1 operational boundaries
//
//   - Single-pod only. v1 has no distributed claim: running two pods that
//     consume the same projection without external leader election is UNSAFE —
//     both advance the same checkpoint and double-apply. Multi-pod safety is the
//     upper layer's responsibility (cmd/* leader election). The PG checkpoint
//     schema reserves an `owner` column (ref: Axon token_entry.owner) for a v1.1
//     pessimistic claim, but v1 never reads or writes it. See ADR §3 Q5.
//   - Full rebuild only — no snapshot / partial replay in v1 (ADR §3 Q4).
//
// # Governance (see each archtest file for ratings)
//
//   - PROJECTION-STATE-PHASE-FROZEN-01 — freezes the Phase enum membership.
//     Archtest: tools/archtest/projection_state_phase_frozen_test.go. Green from PR-00.
//   - PROJECTION-APPLY-HOOK-FUNNEL-01 — Subscribe callable only from
//     cellgen-derived wiring or allowlisted callers.
//     Archtest: tools/archtest/projection_apply_hook_funnel_test.go.
//     PR-01 vacuous-active (zero external callsites); PR-04 load-bearing.
//   - PROJECTION-CHECKPOINT-TX-BOUND-01 — SaveOffset must use the ambient tx,
//     no raw db handle.
//     Archtest: tools/archtest/projection_checkpoint_tx_bound_test.go.
//     PR-01 vacuous-active (MemCheckpointStore has no pool); PR-02 load-bearing.
//   - PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01 — every CheckpointStore impl
//     must enroll in projectiontest.RunCheckpointConformance.
//     Archtest: tools/archtest/projection_checkpoint_conformance_enroll_test.go.
//     Green from PR-01 (MemCheckpointStore enrolled).
//   - PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01 — a projection may only be
//     carried by a transport implementing outbox.SerialInOrderGuarantor (true);
//     the bootstrap drain fail-fasts otherwise. Marker freeze + exact implementer
//     set {InMemoryEventBus} + single guard callsite.
//     Archtest: tools/archtest/projection_serial_delivery_enforcement_test.go.
//     Green from PR-04d (#1369).
//
// ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md
// ref: AxonFramework TokenStore / @ResetHandler — checkpoint-in-tx + 4-phase rebuild.
// ref: JasperFx/marten async-daemon — IDocumentOperations apply shape.
// ref: ThreeDotsLabs/watermill components/cqrs — EventProcessor baseline.
package projection
