package bootstrap

// options_projection.go — With* option functions for the L3 CQRS projection
// harness dependency wiring.
//
// Covers: WithProjectionCheckpointStore, WithProjectionTxRunner,
// WithProjectionReplaySource, WithProjectionCursor.
//
// All four are "cumulative builder" options (see runtime-api.md §Option 范式分层):
// a typed-nil or bare-nil input is silently ignored (does not clear a
// previously set value); the final nil check happens inside
// phase6 buildProjectionCoordinators when a cell has actually registered a
// projection.
//
// Design note — fail-fast timing (identical to options_webhook.go): the
// missing-dependency error is raised at the consuming phase (when a projection
// is drained) rather than at option-apply time. A deployment with zero
// projections is a valid configuration that needs none of these deps, so
// requiring them unconditionally at option time would reject correct setups.
// They only become mandatory once a cell has declared a projection — which is
// exactly when the drain can see both the snapshot and the wired options.
//
// These deps are framework-owned raw infrastructure (a CheckpointStore over the
// framework offset table, the cell tx runner, the journal replay source, the
// stream cursor). They are injected into bootstrap by the composition root and
// are NEVER reachable by cell code — cells never hold a checkpoint store.
// This is what makes the reg.RegisterProjection record-only seam safe: a cell
// could not call projection.NewCoordinator even if it tried, because it has no
// store/tx runner.

import (
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/pkg/validation"
)

// WithProjectionCheckpointStore injects the [projection.CheckpointStore] used by
// every projection Coordinator to persist consumed offsets in the framework
// offset table. Typed-nil or bare-nil inputs are not stored (cumulative builder
// semantics); when a cell has declared a projection, the phase6 drain fails fast
// with errcode.ErrCellInvalidConfig naming this option.
//
// For tests, projection.NewMemCheckpointStore() is sufficient. Production
// deployments inject the postgres-backed adapter via the composition root.
func WithProjectionCheckpointStore(store projection.CheckpointStore) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(store) {
			return
		}
		b.projectionStore = store
	}
}

// WithProjectionTxRunner injects the [persistence.TxRunner] used by every
// projection Coordinator to commit the Apply mutation and the checkpoint
// SaveOffset in one transaction (exactly-once). Typed-nil or bare-nil inputs are
// not stored; the phase6 drain fails fast naming this option when a projection
// is declared.
//
// For tests, a pass-through TxRunner whose RunInTx calls fn(ctx) directly is
// sufficient (the mem checkpoint store ignores the ambient transaction).
func WithProjectionTxRunner(txRunner persistence.TxRunner) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(txRunner) {
			return
		}
		b.projectionTxRunner = txRunner
	}
}

// WithProjectionReplaySource injects the [projection.ReplaySource] used by every
// projection Coordinator to replay the event stream during a rebuild. Typed-nil
// or bare-nil inputs are not stored; the phase6 drain fails fast naming this
// option when a projection is declared.
//
// For tests, projection.NewMemReplaySource() is sufficient.
func WithProjectionReplaySource(replay projection.ReplaySource) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(replay) {
			return
		}
		b.projectionReplay = replay
	}
}

// WithProjectionCursor injects the [projection.LiveCursor] used by every projection
// Coordinator to (a) extract a monotonic stream position from each consumed event
// for the exactly-once checkpoint compare, and (b) resolve a bare live-delivered
// entry into a position-bearing carrier at the delivery boundary (ResolveCarrier).
// The parameter is LiveCursor (not the narrower Cursor) so a cursor that cannot
// resolve live carriers is rejected at COMPILE time — every projection has a live
// push path, so live resolution is mandatory. Typed-nil or bare-nil inputs are not
// stored; the phase6 drain fails fast naming this option when a projection is declared.
//
// For tests, projection.NewMemProjectionEventSource() (a full LiveCursor) is
// sufficient. Production deployments inject the postgres-backed
// PGProjectionEventSource via the composition root.
func WithProjectionCursor(cursor projection.LiveCursor) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(cursor) {
			return
		}
		b.projectionCursor = cursor
	}
}

// WithProjectionRebuildEndpoint opts the assembly into the framework projection
// rebuild control-plane endpoint:
//
//	POST /admin/v1/projection/{cell}/{name}/rebuild
//
// mounted by bootstrap on the AdminListener (the framework-owned-RouteGroup
// pattern, like /healthz·/readyz·/metrics — no contract.yaml, no host cell). The
// handler dispatches by {cell}/{name} to the Coordinator wired in the phase6
// drain and returns 202 (rebuild admitted, body carries the {phase,
// pendingEvents, replayLagSeconds} snapshot) / 409 (already running) / 404
// (unknown cell/projection).
//
// This is an operator→system control-plane action (an administrator or
// deployment pipeline rebuilds a projection), NOT a cell→cell call: there is no
// caller-cell allowlist. Authentication is the AdminListener's operator
// credential gate (AuthOperator). The AdminListener MUST be declared via
// WithListener(cell.AdminListener, addr, []kauth.ListenerAuth{operatorAuth}) —
// phase0 (validateProjectionRebuildEndpoint) fails fast otherwise.
//
// Not calling this option leaves the endpoint unmounted with NO error:
// projection rebuilds remain triggerable only programmatically via
// Coordinator.Rebuild. This is a wiring option (idempotent opt-in).
func WithProjectionRebuildEndpoint() Option {
	return func(b *Bootstrap) {
		b.projectionRebuildEnabled = true
	}
}
