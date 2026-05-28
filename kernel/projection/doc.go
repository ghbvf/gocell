// Package projection declares the kernel-level contracts for the CQRS
// projection lifecycle harness: the business event→state Apply hook, the
// CheckpointStore offset abstraction, the CellCheckpointStore sealed marker,
// the functional Option seam, and the rebuild-lifecycle Phase enum.
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
// # PR-00 boundary (this package, today)
//
// This package currently declares contracts only — there is NO Coordinator
// implementation or consume loop here yet. The decisions these declarations
// freeze are the subject of the ADR; the implementations land across the epic:
//
//   - PR-01: Coordinator + Subscribe + consume loop + checkpoint write
//     (kernel/projection/coordinator.go); mem CheckpointStore + conformance.
//   - PR-02: postgres CheckpointStore adapter (adapters/postgres).
//   - PR-03: rebuild state machine (consumes Phase) + metrics + readyz.
//   - PR-04: cellgen kind:projection derivation (the only sanctioned Subscribe
//     callsite).
//
// Subscribe's frozen forward signature (implemented in PR-01):
//
//	func (c *Coordinator) Subscribe(
//		ctx context.Context,
//		spec contractspec.ContractSpec,
//		projectionID string,
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
// # Governance (lands across the epic; see each archtest godoc for ratings)
//
//   - PROJECTION-STATE-PHASE-FROZEN-01 — freezes the Phase enum membership
//     (green from PR-00).
//   - PROJECTION-APPLY-HOOK-FUNNEL-01 — Subscribe is callable only from
//     cellgen-derived wiring (PR-01 stub → PR-04 green).
//   - PROJECTION-CHECKPOINT-TX-BOUND-01 — SaveOffset must use the ambient tx,
//     no raw db handle (PR-01 stub → PR-02 green).
//
// ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md
// ref: AxonFramework TokenStore / @ResetHandler — checkpoint-in-tx + 4-phase rebuild.
// ref: JasperFx/marten async-daemon — IDocumentOperations apply shape.
// ref: ThreeDotsLabs/watermill components/cqrs — EventProcessor baseline.
package projection
