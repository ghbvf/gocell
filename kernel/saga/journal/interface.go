package journal

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// ClaimedInstance is a saga.Instance projection plus the fencing token the
// caller MUST echo to every lease-fenced mutator (Append, Heartbeat,
// MarkTerminal) for that instance. The LeaseID is minted by the ClaimPending
// sweep that returned the instance; once the lease is lost — reclaimed after
// expiry or superseded by a newer claim — every fenced operation presenting
// this LeaseID becomes a no-op (Heartbeat/MarkTerminal return ok=false) or a
// conflict error (Append).
//
// Tip: pass ci.Instance.ID as instanceID and ci.LeaseID as leaseID to the
// fenced mutators (both are idutil.SafeID; order matters).
type ClaimedInstance struct {
	Instance saga.Instance
	LeaseID  idutil.SafeID
}

// Journal is the append-only durable event log plus lease-coordinated instance
// projection that L3 saga orchestration is built on. Two truths live behind one
// interface: the append-only event log is the source of truth for
// history/replay, while the projection (status, current version, lease) is the
// source of truth for coordination. An implementation keeps the two consistent
// by mutating the projection in the same critical section / transaction as the
// event append.
//
// The interface is storage-dialect-neutral: no *sql.Tx or driver type appears
// in any signature, so the in-memory and PostgreSQL implementations satisfy the
// exact same contract and the same conformance suite (see
// kernel/saga/sagajournaltest).
//
// # Interface split: JournalCore + Heartbeater (#1209)
//
// Journal is the union of two narrower interfaces:
//
//   - [JournalCore] — enrollment, the append-only log, claim/projection, and
//     terminal commit: everything a coordinator needs EXCEPT lease renewal.
//   - [Heartbeater] — the single Heartbeat lease-renewal method.
//
// runtime/saga.Coordinator holds only JournalCore, so a centralized heartbeat
// loop becomes a compile error — the persisted field cannot express Heartbeat.
// Per-step lease renewal is funneled through runtime/saga/executor, which
// receives the full Journal value transiently at construction (NewExecutor). See
// archtests SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 and SAGA-JOURNAL-HOLDER-SEAL-01.
// PG and in-memory implementations satisfy the full Journal, hence both halves.
//
// # Time
//
// Unlike the pure saga state machine (saga.AdvanceSaga takes an explicit now),
// the Journal owns lease-expiry arithmetic, so it sources time from a clock
// injected at construction rather than from a per-call now argument.
//
// # Lease fencing
//
// Enqueue is lease-free open enrollment: producers create instances before any
// leader exists. Every mutator that runs under a leader (Append, Heartbeat,
// MarkTerminal) is CAS-fenced by leaseID. The fencing return convention is
// deliberately asymmetric:
//
//   - Append returns a conflict error on a stale lease, because silently
//     dropping a step event would desynchronise the leader's view of the
//     instance version. A zombie leader must fail loudly.
//   - Heartbeat and MarkTerminal return ok=false (no error) on a stale lease,
//     because losing a lease is an expected race during leader handoff, not a
//     fault — the caller simply stops driving that instance.
//
// # Single sanctioned holder
//
// Only runtime/saga.Coordinator is intended to hold a JournalCore field; no
// struct in runtime/saga may persist the Heartbeat-bearing full Journal (or a
// bare Heartbeater) as a field. Both rules are enforced by the
// SAGA-JOURNAL-HOLDER-SEAL-01 archtest. Enqueue is the producer-facing entry
// point; a narrower producer interface may be split out if a real producer ever
// needs less than JournalCore.
type Journal interface {
	JournalCore
	Heartbeater
}

// JournalCore is the Heartbeat-free core of [Journal]: enrollment, the
// append-only event log, claim/projection, and terminal commit. It is the
// interface runtime/saga.Coordinator persists — deliberately WITHOUT Heartbeat,
// so a centralized heartbeat loop cannot be built on a stored JournalCore field
// (the call would not type-check). See the "Interface split" section on
// [Journal] and archtest SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01.
type JournalCore interface {
	// Enqueue enrolls a new saga instance for orchestration. The instance MUST
	// pass saga.Instance.ValidateNew (Pending, CurrentStep 0, no timestamps
	// beyond StartedAt). Enqueue is lease-free: it writes the projection with
	// current version 0 and no lease, and writes no event (events describe step
	// execution, which has not begun). Re-enqueuing an existing ID returns a
	// KindConflict error with code errcode.ErrSagaDuplicateInstance.
	//
	// Load after Enqueue returns an empty, non-nil slice (see Load); a consumer
	// detects a not-yet-started instance by the empty event log.
	Enqueue(ctx context.Context, instance saga.Instance) error

	// Append durably records one Event for the instance under the holder's lease
	// and returns the per-instance version assigned to it (monotonic, starting
	// at 1). It is lease-fenced: leaseID MUST match the instance's current lease,
	// otherwise nothing is recorded and a KindConflict error is returned. The
	// event is validated via Event.ValidateForAppend (terminal event kinds are
	// rejected here — terminal status is committed via MarkTerminal). The Event's
	// Version and CreatedAt are assigned by the Journal; any caller-supplied
	// values are ignored.
	//
	// Append advances the projection's non-terminal Status as a deterministic fold
	// of the event kind, reusing the saga state machine (saga.AdvanceSaga) so every
	// move is transition-validated and an out-of-phase event is rejected without
	// mutation:
	//   - a forward step event (StepStarted/StepCompleted/StepFailed) moves a
	//     Pending instance to Running; on a Running instance it is a no-op; while
	//     Compensating it is rejected.
	//   - KindCompensationStarted moves Running → Compensating (the Coordinator
	//     appends it when it DECIDES to compensate, BEFORE any compensation runs,
	//     so a handoff mid-rollback reads Compensating, not Running); from Pending
	//     it is rejected.
	//   - KindStepCompensated is legal only while Compensating (status unchanged);
	//     elsewhere it is rejected.
	//   - KindStepCompensationFailed is legal only while Compensating (status
	//     unchanged, like KindStepCompensated); elsewhere it is rejected. It records
	//     that a per-step CompensateFunc returned a non-nil error — distinct from
	//     KindStepFailed (a forward-phase failure during Running).
	// Terminal event kinds are rejected by ValidateForAppend — terminal status is
	// committed only via MarkTerminal, which encodes the terminal state in the
	// event kind. Append also does NOT maintain the step cursor
	// (Instance.CurrentStep); see ClaimPending.
	//
	// An unknown instance returns KindNotFound with code errcode.ErrSagaNotFound;
	// a stale lease returns KindConflict with code errcode.ErrSagaStaleLease.
	// A terminal instance (whose lease was released by MarkTerminal) also
	// returns KindConflict with ErrSagaStaleLease — the projection holds no
	// lease, so the caller's leaseID cannot match. Callers needing to
	// distinguish "expired lease" from "already terminal" must consult Load.
	Append(ctx context.Context, instanceID, leaseID idutil.SafeID, event Event) (version int64, err error)

	// Load returns the full ordered event history for an instance (version
	// ascending), for replay / state reconstruction. A never-enqueued instance
	// returns a KindNotFound error with code errcode.ErrSagaNotFound; an
	// enqueued instance with no events yet returns an empty, non-nil slice.
	Load(ctx context.Context, instanceID idutil.SafeID) ([]Event, error)

	// ClaimPending atomically leases up to batchSize non-terminal instances that
	// are currently unleased or whose lease has expired, sets each one's lease to
	// expire at now+leaseDuration, and returns them stamped with a single batch
	// LeaseID that the caller MUST echo on every subsequent fenced call for each
	// claimed instance. When nothing is claimable it returns an empty slice, the
	// zero LeaseID, and a nil error.
	//
	// batchSize and leaseDuration MUST both be positive; a non-positive value
	// returns a KindInvalid error.
	//
	// The returned Instance carries the coordination projection: Status (the
	// coarse-grained Pending/Running/Compensating lifecycle the Journal maintains)
	// and lease, but NOT the fine-grained step cursor — Instance.CurrentStep is
	// not advanced by the Journal (it would require the definition's step count,
	// which the Journal does not hold) and remains 0. A caller resuming an
	// instance reconstructs the exact cursor by folding the event log from Load.
	//
	// Instance.Status is the current projection value (maintained by
	// Append/MarkTerminal) and is guaranteed up-to-date; callers MAY trust it
	// directly while still folding Load for the exact step cursor.
	ClaimPending(ctx context.Context, batchSize int, leaseDuration time.Duration) (claimed []ClaimedInstance, leaseID idutil.SafeID, err error)

	// MarkTerminal transitions the instance projection to finalStatus and appends
	// the matching terminal event atomically, so the log alone replays which
	// terminal state was reached. Terminal event kinds per finalStatus:
	//   - StatusSucceeded     → KindSagaSucceeded     (all steps committed)
	//   - StatusFailed        → KindSagaFailed        (forward failure, no rollback)
	//   - StatusCompensated   → KindSagaCompensated   (rollback completed cleanly)
	//   - StatusExpired       → KindSagaExpired       (overall timeout elapsed)
	//   - StatusCompensationFailed → KindSagaCompensationFailed (rollback itself
	//     failed; reachable only from StatusCompensating)
	//
	// finalStatus MUST be a terminal saga.Status reachable from the current
	// status (validated via saga.AdvanceSaga). It is lease-fenced: ok is false
	// (with a nil error) when leaseID no longer owns the instance. On success
	// the lease is released. A never-enqueued instance also returns ok=false
	// (nil error); callers cannot distinguish it from a stale lease.
	MarkTerminal(ctx context.Context, instanceID, leaseID idutil.SafeID, finalStatus saga.Status) (ok bool, err error)

	// RepoReady is a differentiated readiness check (kernel/healthz.RepoProber):
	// SQL-backed implementations exercise the saga_instances relation directly so
	// schema/migration drift surfaces independently of a pool-level ping;
	// in-memory implementations return nil.
	//
	// Probe registration (name + cellgen path) is the responsibility of the
	// Coordinator cell that holds the Journal (PR-03); a Journal implementation
	// only provides the RepoProber method and does not self-register.
	RepoReady(ctx context.Context) error
}

// Heartbeater is the single lease-renewal method split out of [Journal] (#1209).
//
// runtime/saga/executor owns the only sanctioned Heartbeat caller (the per-step
// heartbeat goroutine) and declares its OWN structurally-identical Heartbeater
// interface rather than importing this one: kernel/ cannot depend on runtime/,
// and the executor must never import kernel/saga/journal
// (SAGA-JOURNAL-HOLDER-SEAL-01). This kernel-side Heartbeater exists solely to
// compose the full [Journal] so that the PG / in-memory implementations satisfy
// it via interface embedding.
type Heartbeater interface {
	// Heartbeat extends the lease on a single claimed instance to
	// now+leaseDuration. leaseDuration MUST be > 0; a non-positive value returns a
	// KindInvalid error. It is lease-fenced: ok is false (with a nil error) when
	// leaseID no longer owns the instance, signaling the holder to stop driving
	// it. A never-enqueued instance also returns ok=false (nil error); callers
	// cannot distinguish it from a stale lease.
	Heartbeat(ctx context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (ok bool, err error)
}
