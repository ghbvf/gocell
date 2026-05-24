// Package journal provides the durable, append-only Journal for L3
// (WorkflowEventual) saga orchestration: the interface the runtime Coordinator
// (PR-03) and the PostgreSQL store (PR-04) build on, plus an in-memory
// implementation ([MemJournal]) for tests and development. The reusable
// conformance suite that every implementation must pass lives in the sibling
// package kernel/saga/sagajournaltest.
//
// # Two truths, one interface (hybrid model)
//
// A Journal keeps two consistent views of each saga instance:
//
//   - an append-only event log — the source of truth for history and replay;
//   - a coordination projection (status, current event version, lease) — the
//     source of truth for who is driving the instance and where it is.
//
// An implementation mutates the projection in the same critical section /
// transaction as the corresponding event append, so the two cannot drift.
// The projection is intentionally coarse-grained: it tracks the Pending →
// Running → Compensating → terminal lifecycle but NOT the fine-grained step
// cursor (saga.Instance.CurrentStep), which would require the definition's step
// count the Journal does not hold. A caller reconstructs the exact cursor by
// folding the event log from Load.
//
// # Flow
//
//	Enqueue ──► ClaimPending ──► Append* ──► MarkTerminal
//	(producer)   (leader sweep)   (leader)    (leader, terminal)
//
//	Enqueue        lease-free open enrollment; instance starts Pending, version 0.
//	ClaimPending   leases a batch of non-terminal instances under one batch lease.
//	Append         records a step event under the lease; the first forward step
//	               event moves Pending → Running, a StepCompensated event moves
//	               Running → Compensating (validated by the saga state machine).
//	MarkTerminal   commits a terminal status + the closing event, releases lease.
//
// # Lease fencing (asymmetric returns)
//
// Every leader-driven mutator (Append, Heartbeat, MarkTerminal) is CAS-fenced by
// the batch leaseID from ClaimPending. The fencing return is deliberately
// asymmetric: Append returns a KindConflict error on a stale lease (a zombie
// leader must not silently corrupt the version stream), whereas Heartbeat and
// MarkTerminal return ok=false with a nil error (losing a lease is an expected
// race during leader handoff, not a fault). An expired lease is treated as lost:
// the next ClaimPending sweep re-leases the instance.
//
// # Single sanctioned holder (forward note)
//
// Only the runtime Coordinator (PR-03) is meant to hold a Journal field; the
// SAGA-JOURNAL-HOLDER-SEAL-01 archtest seals this once the Coordinator exists.
// Enqueue is the producer-facing entry point; whether to split a narrower
// producer interface is deferred to PR-03, when a real producer exists.
//
// # References
//
// ref: runtime/audit/ledger/mem_store.go (in-repo) — append-only log + clock-
// injected mem store + defensive copies.
// ref: runtime/outbox/outboxtest/store_conformance.go (in-repo) — lease-fencing,
// stale-lease, and leader-handoff conformance template.
// ref: docs/architecture/202605051600-adr-pg-outbox-fencing.md — lease_id CAS.
// ref: eventuate-foundation/eventuate-client-java saga-orchestration — event-
// sourced saga history.
package journal
