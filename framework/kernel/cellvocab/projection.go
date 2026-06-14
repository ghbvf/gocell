package cellvocab

import (
	"context"
	"time"
)

// ProjectionEvent is the minimal typed read-only carrier consumed by the
// projection harness (kernel/projection): Apply, ReplaySource.Replay's callback,
// and Cursor.Position all carry a ProjectionEvent rather than the concrete
// kernel/outbox.Entry. Generalizing the carrier lets a saga-journal event source
// (EPIC #1609 PR-03) and the outbox event source flow through one typed funnel
// with no dual outbox.Entry-typed path.
//
// # Why this lives in cellvocab
//
// kernel/projection imports kernel/cell (its Coordinator references
// cell.SubscriptionOption), and kernel/cell declares ProjectionApply — the
// cell-local mirror of projection.Apply — so the carrier type must live in a
// package BOTH import without forming a cycle. cellvocab is that leaf (it imports
// nothing in kernel/): both kernel/cell and kernel/projection alias these types,
// and kernel/outbox can import cellvocab for its `var _ ProjectionEvent =
// Entry{}` compile assertion (kernel/cell cannot host the interface for that
// assertion — kernel/cell already imports kernel/outbox, so the reverse import
// would be a cycle). The ADR (#1609 §4.1) named the package kernel/projection;
// the cell↔projection cycle makes that infeasible — see the §4.1/§6 amendment.
//
// # Not sealed (by design)
//
// All methods are exported and any package may implement ProjectionEvent — the
// carrier source is intentionally open so the PR-03 saga carrier can implement
// it too. It is NOT a sealed/forge-proof funnel; forge protection lives in the
// wiring layer (the Tailer feeding Apply solely from the sealed journal, ADR §5).
// Enforcement that public projection APIs carry ProjectionEvent (not the concrete
// outbox.Entry) is the type system plus archtest PROJECTION-EVENT-CARRIER-TYPED-01.
//
// EventID and Stream are the polymorphic renames of the outbox concepts
// (outbox.Entry.ID / outbox.Entry.RoutingTopic), chosen so the saga carrier's
// distinct identity/stream semantics are unambiguous on the shared interface.
type ProjectionEvent interface {
	// EventID is the event's identifier, which MUST be unique across the entire
	// replay source (not merely within a Stream): the harness Cursor resolves a
	// stable position by matching EventID over the whole source (mem scans all
	// entries; PG does `SELECT seq … WHERE id = $1`), so two distinct events
	// sharing an EventID would resolve to the same position and corrupt the
	// checkpoint. outbox.Entry satisfies this with its global UUID; a future
	// saga-journal carrier (PR-03) must likewise expose a source-global-unique id.
	EventID() string
	// Payload is the raw event body JSON consumed by the business Apply hook.
	Payload() []byte
	// OccurredAt is the domain timestamp (used for the replay-lag metric).
	OccurredAt() time.Time
	// Stream is the routing/topic equivalent (outbox: RoutingTopic); the harness
	// filters a projection to its subscribed stream by comparing against the spec.
	Stream() string
	// RestoreContext returns a context with the event's ambient identity
	// installed before Apply runs (outbox: observability + principal restore). It
	// must be idempotent and must not strip an ambient transaction carried by ctx.
	RestoreContext(ctx context.Context) context.Context
}

// ProjectionApply is the business event→state projection hook: given a consumed
// ProjectionEvent, mutate the read-model within the ambient transaction carried
// by ctx. It is the single underlying type aliased by both
// kernel/cell.ProjectionApply (the cell-local registry surface) and
// kernel/projection.Apply (the harness surface) so the bootstrap drain passes a
// recorded hook to Coordinator.Subscribe with no named-type conversion. The full
// behavioral contract (tx semantics, transient vs permanent error vocabulary)
// lives on the projection.Apply alias godoc.
type ProjectionApply func(ctx context.Context, event ProjectionEvent) error

// ProjectionResetHook is the optional rebuild Reset-phase hook (clear the
// read-model before replay). nil is valid. Aliased by
// kernel/cell.ProjectionResetHook and kernel/projection.OnReset.
type ProjectionResetHook func(ctx context.Context) error

// ProjectionSource selects which input stream drives a projection. It is the
// slice.yaml `projectionSource` selector, threaded through
// kernel/cell.ProjectionRequest.Source so the bootstrap drain branches on the
// transport that feeds the projection's Apply hook (EPIC #1609 PR-05, ADR D2/D4):
//
//   - ProjectionSourceOutbox: the named event contract's outbox topic, driven by
//     a push-based kernel/projection.Coordinator wired into the event router. The
//     ProjectionRequest carries an event-kind Spec (the subscribed topic).
//   - ProjectionSourceSagaJournal: the GLOBAL saga journal (stream saga.journal.v1,
//     every saga instance/type), driven by a pull-based runtime/saga/tailer.Tailer.
//     The ProjectionRequest carries NO event Spec; the subscribe CU's saga-contract
//     anchor is for lineage/governance only — it is NOT a runtime filter (the
//     source is global, so the Apply hook folds the whole journal; see
//     kernel/metadata.ContractUsage.ProjectionSource).
//
// A projection has exactly one source — the two values are mutually exclusive
// transports.
type ProjectionSource string

const (
	ProjectionSourceOutbox      ProjectionSource = "outbox"
	ProjectionSourceSagaJournal ProjectionSource = "saga-journal"
)

// allProjectionSources is the canonical ordered ProjectionSource set. Single
// source for AllProjectionSources(); the slice.schema.json `projectionSource`
// enum and governance validators stay in lockstep with it.
var allProjectionSources = []ProjectionSource{ProjectionSourceOutbox, ProjectionSourceSagaJournal}

// AllProjectionSources returns a copy of the canonical ordered ProjectionSource set.
func AllProjectionSources() []ProjectionSource {
	out := make([]ProjectionSource, len(allProjectionSources))
	copy(out, allProjectionSources)
	return out
}
