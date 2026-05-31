package reconcile

import "context"

// Reconciler is the single method a consumer implements to converge ONE entity
// toward its desired state. The Loop calls it once per scheduled Request.
//
// Contract:
//   - Return (Result{RequeueAfter: d>0}, nil) to re-observe this entity after d.
//   - Return (Result{}, nil) (RequeueAfter == 0) to re-observe at the default
//     tick interval.
//   - Return (Result{}, err) with a transient err to have the Loop back off and
//     retry (Result is ignored when err != nil).
//   - Return (Result{}, PermanentError(err)) when retrying cannot help; the
//     Loop records a dead-letter metric and stops scheduling this entity until
//     a fresh trigger re-observes it.
//
// Idempotency contract (P1): Reconcile MUST be idempotent. The framework may
// invoke it multiple times for the same entity without an intervening state
// change: on periodic resync (Interval tick), on F5 dirty re-run after coalesced
// duplicate triggers, and on transient-error retry. This is the level-triggered
// convergence contract — the reconciler observes current state and drives toward
// desired state, regardless of how many times it is called.
//
// Reconcile MUST honor ctx cancellation (the Loop cancels ctx on
// shutdown / StopTimeout).
//
// INVARIANT: RECONCILE-INTERFACE-FROZEN-01 — the method set is frozen to exactly
// Reconcile(context.Context, Request) (Result, error). Adding a method, or
// changing this signature, trips an exact-set reflect assertion in CI; it is a
// public-contract change that must be made together with the design ADR.
//
// ref: kubernetes-sigs/controller-runtime pkg/reconcile/reconcile.go (Reconciler)
type Reconciler interface {
	Reconcile(ctx context.Context, req Request) (Result, error)
}

// Request is the minimal locator the Loop hands a Reconciler. Unlike
// controller-runtime's reconcile.Request (which carries a types.NamespacedName),
// a GoCell entity lives in a single cell-local table and is addressed by one
// opaque EntityID — there is no namespace dimension.
//
// INVARIANT: RECONCILE-REQUEST-FIELDS-FROZEN-01 — the field set is frozen to
// exactly { EntityID string }. Adding a field (e.g. a Namespace or Priority)
// trips an exact-set reflect assertion in CI.
type Request struct {
	// EntityID identifies the single entity to reconcile. It is opaque to the
	// Loop and meaningful only to the Reconciler (typically a primary key in the
	// cell's own table).
	//
	// An empty EntityID is the "resync-all" sentinel emitted by TickerTrigger
	// (a context-free interval ticker has no entity to name): a Reconciler wired
	// to a TickerTrigger MUST treat req.EntityID == "" as a "re-observe every
	// entity you own" sweep, fanning out to its own entity set.
	//
	// Bounded-set contract (S1): EntityID MUST come from a bounded set (e.g.
	// primary keys of a cell-local table). The Loop retains a small per-entity
	// backoff counter in memory for every entity that has experienced at least one
	// transient error; the counter is released on success, permanent failure, or
	// Loop restart (mirroring controller-runtime workqueue). An unbounded or
	// high-cardinality EntityID space (e.g. user input strings) would grow this
	// in-memory map without bound. The dirty/processing maps obey the same
	// constraint: they hold at most one entry per in-flight entity and are cleared
	// on completion or Loop restart.
	EntityID string
}
