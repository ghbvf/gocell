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
// are NEVER reachable by cell code — a cell holds only the sealed
// projection.CellCheckpointStore marker, never the raw store. This is what makes
// the reg.RegisterProjection record-only seam safe: a cell could not call
// projection.NewCoordinator even if it tried, because it has no store/tx runner.

import (
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/pkg/validation"
)

// WithProjectionCheckpointStore injects the [projection.CheckpointStore] used by
// every projection Coordinator to persist consumed offsets in the framework
// offset table. A nil or typed-nil value is silently ignored; the final nil
// check happens during phase6 when any cell has declared a projection.
//
// For tests, projection.NewMemCheckpointStore is sufficient. Production
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
// SaveOffset in one transaction (exactly-once). A nil or typed-nil value is
// silently ignored; the final nil check happens during phase6.
func WithProjectionTxRunner(txRunner persistence.TxRunner) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(txRunner) {
			return
		}
		b.projectionTxRunner = txRunner
	}
}

// WithProjectionReplaySource injects the [projection.ReplaySource] used by every
// projection Coordinator to replay the event stream during a rebuild. A nil or
// typed-nil value is silently ignored; the final nil check happens during
// phase6.
func WithProjectionReplaySource(replay projection.ReplaySource) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(replay) {
			return
		}
		b.projectionReplay = replay
	}
}

// WithProjectionCursor injects the [projection.Cursor] used by every projection
// Coordinator to extract a monotonic stream position from each consumed event
// (for the exactly-once checkpoint compare). A nil or typed-nil value is
// silently ignored; the final nil check happens during phase6.
func WithProjectionCursor(cursor projection.Cursor) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(cursor) {
			return
		}
		b.projectionCursor = cursor
	}
}
