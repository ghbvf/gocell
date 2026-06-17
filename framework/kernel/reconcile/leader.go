package reconcile

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// LeaderElector is the leader-election seam a multi-replica reconcile deployment
// wires into a Loop. The interface is declared in kernel/reconcile and
// implemented in adapters (adapters/redis SETNX+EXPIRE + epoch key,
// adapters/postgres row-TTL CAS in reconcile_leases) — the kernel-declares /
// adapter-implements layering that mirrors outbox.Emitter and
// persistence.CellTxManager.
//
// CRITICAL — leader election is NOT fencing. client-go's own
// tools/leaderelection documents: "This implementation does not guarantee that
// only one client is acting as a leader (a.k.a. fencing)." STW GC pauses, clock
// skew, and renew/acquire races all leave a residual dual-execution window: an
// old leader paused mid-Reconcile, its lease expired and taken over, then it
// wakes and finishes its write. The Loop uses the lease only to NARROW that
// window (single dispatcher in steady state + lost-lease ctx cancel). Cross-replica
// correctness is the job of the monotonic LeaseToken.Epoch fed into a
// FencedWriter (see fenced.go) plus consumer idempotency — never the lease.
// Adapter choice matters for the epoch provenance: Redis keeps the epoch in an
// evictable key and is best-effort under allkeys-* eviction, while Postgres keeps
// the epoch in a durable row and is the strong-fencing adapter when eviction
// residuals are unacceptable.
//
// A nil LeaderElector on a Loop means single-process mode: the Loop is always
// the leader, Epoch is 0, and no fencing is applied (the single-replica
// deployment has no zombie-leader window to fence).
//
// INVARIANT: RECONCILE-LEADER-INTERFACE-FROZEN-01 — the method set is frozen to
// exactly AcquireLease / ReleaseLease / RenewLease with the signatures below, and
// LeaseToken's field set is frozen (in particular the monotonic Epoch uint64).
// A reflect golden assertion in CI rejects any added method or changed field; it
// is a public-contract change that must be made together with the design ADR.
//
// ref: kubernetes/client-go tools/leaderelection/leaderelection.go (lease/renew
// model + the explicit "not fencing" disclaimer).
type LeaderElector interface {
	// AcquireLease attempts to become the leader for reconcilerID. On success it
	// returns a LeaseToken whose ExpiresAt is now+LeaseDuration and whose Epoch is
	// the monotonic fencing token (incremented on every holder CHANGE, see
	// LeaseToken.Epoch). On contention (another holder owns a live lease) it
	// returns a non-nil error; the Loop treats any acquire error as fail-closed
	// (it does NOT dispatch) and retries after a backoff.
	AcquireLease(ctx context.Context, reconcilerID string) (LeaseToken, error)
	// ReleaseLease relinquishes the lease for a graceful handoff (the next
	// AcquireLease by any holder takes over immediately rather than waiting for
	// TTL expiry). Idempotent; a release of an already-lost lease is not an error.
	ReleaseLease(ctx context.Context, token LeaseToken) error
	// RenewLease extends the lease's TTL while keeping its Epoch unchanged. It
	// returns ErrReconcileLeaseLost (errors.Is-matchable) when the lease is no
	// longer owned by this holder (expired or taken over) — the Loop cancels its
	// lease-scoped ctx the instant this happens to interrupt the in-flight
	// Reconcile. Other (I/O) errors are transient and also drive a re-acquire.
	RenewLease(ctx context.Context, token LeaseToken) error
}

// LeaseToken is the opaque, value-typed proof of leadership an adapter mints and
// the Loop threads back into ReleaseLease / RenewLease. It is NOT sealed-construction:
// adapters in different packages must build it, so its fields are exported. The
// frozen field set is enforced by RECONCILE-LEADER-INTERFACE-FROZEN-01.
type LeaseToken struct {
	// ReconcilerID is the leader-election key (one lease per reconciler identity).
	ReconcilerID string
	// HolderID identifies the replica that holds this lease — minted once per
	// elector instance so two replicas competing for the same ReconcilerID are
	// distinguishable (renew ownership CAS, handoff epoch bump).
	HolderID string
	// Epoch is the monotonic fencing token. The lease store increments it on
	// every holder CHANGE (a successful AcquireLease that takes the lease from
	// free/expired/another-holder) and keeps it UNCHANGED across RenewLease. The
	// Loop binds it to a FencedWriter so a zombie leader's late write — carrying a
	// strictly lower Epoch — is rejected by the resource's write-path CAS. This is
	// monotonic (Kleppmann) fencing: reject ALL epoch < highest-seen, not the
	// outbox UUID identity-fencing (equal-or-reject), because a device write may
	// arrive after several L1→L2→L3 handoffs and only "older than highest" — not
	// "not equal to current" — can reject an out-of-order late write.
	Epoch uint64
	// AcquiredAt is when this holder acquired (or re-acquired) the lease.
	AcquiredAt time.Time
	// ExpiresAt is when the lease lapses absent a successful RenewLease.
	ExpiresAt time.Time
}

// ErrReconcileLeaseLost is the sentinel RenewLease returns (errors.Is-matchable)
// when the lease is no longer owned by this holder. It drives the Loop's
// lost-lease ctx cancel. KindConflict so an accidental HTTP surface is a 409
// rather than a misleading 500.
var ErrReconcileLeaseLost = errcode.New(errcode.KindConflict, errcode.ErrReconcileLeaseLost,
	"reconcile: lease lost (no longer held by this holder)")

// ErrLeaseHeld is the sentinel AcquireLease returns (errors.Is-matchable) when
// another holder owns a live lease (contention). The Loop logs it at Debug — it
// is the expected steady-state signal a follower polls into — and retries after
// leaderRetryPeriod. Adapters and the fake elector MUST wrap their contention
// path with this sentinel so logLeaderAcquireSkip can classify it.
var ErrLeaseHeld = errcode.New(errcode.KindConflict, errcode.ErrReconcileLeaseHeld,
	"reconcile: lease held by another holder")
