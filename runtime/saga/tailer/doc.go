// Package tailer implements the saga-journal projection catch-up tailing live
// driver (EPIC #1609 PR-04). harness's existing live path (ConsumerBase bus
// subscription) cannot drive a saga-journal projection: the saga journal is not
// a bus, and compensated/failed saga terminals exist only in the append-only
// saga_events journal. The Tailer fills that gap with the model-A live form —
// periodic poll Head + drain (checkpoint, Head] into projection Apply, advancing
// a fenced checkpoint (Axon catch-up processor / EventStoreDB persistent
// subscription shape).
//
// # Design (ADR docs/architecture/202606051200-1609 D4/D5)
//
// The Tailer is an INDEPENDENT component (ADR D4 "Option B"), NOT a graft onto
// the saga Coordinator's tick/leader-gate: the Coordinator's distlock is
// per-saga-instance and offers no reusable global/per-projection leader. The
// Tailer shares the same distlock.Locker INSTANCE but acquires a distinct
// per-projection key (tailerLockKey, namespace "saga-journal-tailer:"), honoring
// the issue's "fewest new machines" intent at per-projection granularity.
//
// Checkpoint advance is funneled through exactly one function, Tailer.commitEvent
// (the sole sanctioned caller of projection.OwnerCheckpointStore.AdvanceIfOwner,
// locked by archtest SAGA-TAILER-CHECKPOINT-ADVANCER-CALLER-01). apply + advance
// commit in the same transaction (exactly-once within an owner, D5(a)); the
// AdvanceIfOwner CAS fences checkpoint regression across leader handoff (D5(b),
// mem this PR; PG owner-column fence token deferred to PR-PG).
//
// The Tailer self-carries its operational surface (ADR §4.3): a readiness probe
// (<cell>_saga_tailer_<proj>_ready) and metrics via the Observer sink
// (lock-acquire-failure / drain-error / checkpoint-advance-failure / pending-lag
// / last-success-timestamp) — it does not inherit ConsumerBase/Coordinator
// signals.
//
// Wiring into an assembly is PR-05; the first real consumer (orderfulfillment)
// is PR-06. This package delivers the mechanism, verified by unit/conformance
// tests.
package tailer
