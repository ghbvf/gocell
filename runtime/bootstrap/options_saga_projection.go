package bootstrap

// options_saga_projection.go — With* option functions for the saga-journal
// projection Tailer dependency wiring (EPIC #1609 PR-05).
//
// These mirror options_projection.go: each is a "cumulative builder" option
// (runtime-api.md §Option 范式) — a typed-nil or bare-nil input is silently
// ignored (does not clear a previously set value); the final nil check happens
// in the phase6 saga-projection drain (checkSagaProjectionDeps) when a cell has
// actually declared a saga-journal projection. A deployment with zero
// saga-journal projections is a valid configuration that needs none of these
// deps, so requiring them unconditionally at option time would reject correct
// setups.
//
// The TxRunner is REUSED from WithProjectionTxRunner (b.projectionTxRunner): the
// saga-journal Tailer commits apply+advance in the same cell tx as the outbox
// Coordinator, so a separate tx-runner option would be a redundant second source.
//
// Like the outbox projection deps, the reader / owner-checkpoint store / locker
// are framework-owned raw infrastructure injected by the composition root and
// never reachable by cell code — cells only record intent via
// reg.RegisterProjection (NewSagaJournalProjectionRequest).

import (
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/saga/tailer"
)

// WithSagaJournalReader injects the [journal.GlobalReader] the saga-journal
// Tailer replays from (wrapped once into a shared sagaprojection.SagaJournalSource
// by the drain). Typed-nil or bare-nil inputs are not stored (cumulative builder
// semantics); when a cell has declared a saga-journal projection, the phase6
// saga-projection drain fails fast with errcode.ErrCellInvalidConfig naming this
// option.
//
// For tests, journal.NewMemJournal(clk) satisfies GlobalReader. Production
// deployments inject the postgres-backed saga journal via the composition root.
func WithSagaJournalReader(gr journal.GlobalReader) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(gr) {
			return
		}
		b.sagaJournalReader = gr
	}
}

// WithSagaProjectionOwnerCheckpointStore injects the fenced
// [projection.OwnerCheckpointStore] (LoadOffset + AdvanceIfOwner) the Tailer uses
// to persist consumed offsets under owner-token CAS. It is distinct from the
// outbox Coordinator's plain CheckpointStore (WithProjectionCheckpointStore):
// the Tailer's leader-handoff fencing depends on the AdvanceIfOwner CAS. Typed-nil
// or bare-nil inputs are not stored; the phase6 saga-projection drain fails fast
// naming this option when a saga-journal projection is declared.
//
// For tests, projection.NewMemOwnerCheckpointStore() is sufficient.
func WithSagaProjectionOwnerCheckpointStore(store projection.OwnerCheckpointStore) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(store) {
			return
		}
		b.sagaProjOwnerStore = store
	}
}

// WithSagaProjectionLocker injects the [distlock.Locker] the Tailer uses as its
// per-projection leader gate (a distinct lock key per projection over the SAME
// shared Locker instance). Unlike the saga Coordinator's optional single-process
// mode, the Tailer REQUIRES a locker — its leader-handoff correctness depends on
// the distlock making the owner-token claim race-free. Typed-nil or bare-nil
// inputs are not stored; the phase6 saga-projection drain fails fast naming this
// option when a saga-journal projection is declared.
func WithSagaProjectionLocker(locker distlock.Locker) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(locker) {
			return
		}
		b.sagaProjLocker = locker
	}
}

// WithSagaTailerConfig overrides the Tailer poll/lease Config for every
// saga-journal projection. Optional: when not called, each Tailer uses
// tailer.DefaultConfig() (1s poll, 30s lease). The zero Config value is NOT a
// valid override (NewTailer.Config.Validate rejects non-positive durations), so
// this option records a "set" flag and the drain only passes WithConfig when the
// flag is true.
func WithSagaTailerConfig(cfg tailer.Config) Option {
	return func(b *Bootstrap) {
		b.sagaTailerConfig = cfg
		b.sagaTailerConfigSet = true
	}
}
