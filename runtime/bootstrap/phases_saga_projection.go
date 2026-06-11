package bootstrap

// phases_saga_projection.go — saga-journal projection drain (part of phase6).
//
// Covers:
//   - drainCellSagaProjections: iterates cell snapshots, filters to
//     Source == saga-journal projections, and for each such projection builds a
//     runtime/saga/tailer.Tailer (pull) and wires it DIRECTLY into bootstrap's
//     worker / probe / teardown surfaces.
//   - buildOneSagaTailer / wireOneSagaTailer: construct + wire one Tailer.
//   - checkSagaProjectionDeps / sagaJournalSourceOnce / sagaTailerObserverFor:
//     the framework-dep fail-fast, the shared SagaJournalSource cache, and the
//     per-cell metric-observer cache.
//
// # Why a Tailer, not a Coordinator
//
// A saga-journal projection consumes the GLOBAL saga journal (a pull stream),
// not an event-kind outbox topic (a push subscription). The outbox path builds a
// projection.Coordinator subscribed to the event router; the saga-journal path
// builds a Tailer that polls journal.GlobalReader under a per-projection leader
// gate. They are constructed from disjoint deps and wired through different
// surfaces (Coordinator → event router; Tailer → worker loop), so the drain
// branches on cell.ProjectionRequest.Source.
//
// # Why direct wiring, NOT WithManagedResource
//
// expandManagedResources runs as a pre-phase before phase0, but the Tailer is
// constructed here in the phase6 projection drain (it needs the snapshot's
// cellID/projectionID). Appending to b.managedResources in phase6 would be a
// no-op (nothing re-expands) and its probe would miss the phase5 drain window.
// So the Tailer's worker / probe / teardown are wired directly here, exactly as
// wireOneProjection wires the Coordinator: b.workers (consumed at phase8), probes
// registered straight onto b.healthAggregator, and a named Close teardown.

import (
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/saga/sagaprojection"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	metricsmiddleware "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/saga/tailer"
)

// drainCellSagaProjections constructs and wires a Tailer for every saga-journal
// projection declared across the cell snapshots. For each cell that declares at
// least one saga-journal projection it runs checkSagaProjectionDeps once (wrapped
// with the cell context) then builds + wires one Tailer per request. Cells with
// no saga-journal projection are skipped entirely (the outbox Coordinator path in
// drainCellProjections handles their outbox projections).
func (b *Bootstrap) drainCellSagaProjections(s *phaseState) error {
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		reqs := sagaJournalRequests(snap.Projections)
		if len(reqs) == 0 {
			continue
		}
		if err := b.drainOneCellSagaProjections(s, id, reqs); err != nil {
			return err
		}
	}
	return nil
}

// drainOneCellSagaProjections runs the required-dep check once for cell id, then
// builds + wires one Tailer per saga-journal request. Split out of
// drainCellSagaProjections so each function stays within the cognitive-complexity
// budget; mirrors the outbox path's buildCellProjections per-cell split.
func (b *Bootstrap) drainOneCellSagaProjections(s *phaseState, id string, reqs []cell.ProjectionRequest) error {
	// Required-dep check runs once per cell that declares any saga-journal
	// projection; wrap with the cell context so ops sees which cell triggered
	// the missing-option error (mirrors buildCellProjections).
	if err := b.checkSagaProjectionDeps(); err != nil {
		return fmt.Errorf("bootstrap: cell %s: %w", id, err)
	}
	for _, req := range reqs {
		// CellID-drift fail-fast, identical to the outbox path
		// (buildCellProjections): codegen injects req.CellID from cell metadata,
		// and bootstrap cross-checks it against the snapshot owner so a cellgen
		// drift cannot bind this Tailer / checkpoint / probe to the wrong cell.
		if req.CellID != id {
			return fmt.Errorf(
				"bootstrap: cell %s saga-journal projection drift: declared CellID=%q but snapshot owner=%q"+
					" (codegen should inject cellID from cell metadata; check cellgen templates)",
				id, req.CellID, id)
		}
		t, err := b.buildOneSagaTailer(req)
		if err != nil {
			return fmt.Errorf("bootstrap: cell %s saga-journal projection %q: %w",
				req.CellID, req.ProjectionID, err)
		}
		if err := b.wireOneSagaTailer(s, t, req.CellID, req.ProjectionID); err != nil {
			return err
		}
	}
	return nil
}

// sagaJournalRequests filters a snapshot's projection set to the saga-journal
// source. The outbox Coordinator path (buildCellProjections) handles the
// complement.
func sagaJournalRequests(all []cell.ProjectionRequest) []cell.ProjectionRequest {
	var out []cell.ProjectionRequest
	for _, req := range all {
		if req.Source == cellvocab.ProjectionSourceSagaJournal {
			out = append(out, req)
		}
	}
	return out
}

// checkSagaProjectionDeps fails fast (with the missing option named) when a
// saga-journal projection has been declared but a required framework dependency
// was not wired. The TxRunner is shared with the outbox projection path
// (WithProjectionTxRunner), so it is named as such. Mirrors checkProjectionDeps'
// error style/code.
func (b *Bootstrap) checkSagaProjectionDeps() error {
	switch {
	case validation.IsNilInterface(b.sagaJournalReader):
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: saga-journal projection declared but no journal reader configured; "+
				"add WithSagaJournalReader to bootstrap options")
	case validation.IsNilInterface(b.sagaProjOwnerStore):
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: saga-journal projection declared but no owner checkpoint store configured; "+
				"add WithSagaProjectionOwnerCheckpointStore to bootstrap options")
	case validation.IsNilInterface(b.projectionTxRunner):
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: saga-journal projection declared but no tx runner configured; "+
				"add WithProjectionTxRunner to bootstrap options")
	case validation.IsNilInterface(b.sagaProjLocker):
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: saga-journal projection declared but no distlock locker configured; "+
				"add WithSagaProjectionLocker to bootstrap options")
	}
	return nil
}

// sagaJournalSourceOnce builds the shared SagaJournalSource from
// b.sagaJournalReader on first call and caches it on b.sagaJournalSource,
// returning the cached instance thereafter. The source is global + stateless, so
// ONE shared instance serves every saga-journal projection (it is passed as both
// the replay and cursor args of tailer.NewTailer).
func (b *Bootstrap) sagaJournalSourceOnce() (*sagaprojection.SagaJournalSource, error) {
	if b.sagaJournalSource != nil {
		return b.sagaJournalSource, nil
	}
	src, err := sagaprojection.NewSagaJournalSource(b.sagaJournalReader)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: build saga journal source: %w", err)
	}
	b.sagaJournalSource = src
	return src, nil
}

// hasRealMetricsProvider reports whether a non-nil, non-Nop metrics provider is
// configured. When false the saga tailer skips its metric Observer entirely
// (leaving the Tailer's default NopObserver), mirroring the outbox path's nil/Nop
// guard in autoWireCachedCollector.
func (b *Bootstrap) hasRealMetricsProvider() bool {
	if b.metricsProvider == nil {
		return false
	}
	_, isNop := b.metricsProvider.(kernelmetrics.NopProvider)
	return !isNop
}

// sagaTailerObserverFor returns the per-cell Tailer metric Observer, or a
// tailer.NopObserver when no real metrics provider is configured. The nil/Nop
// guard is inlined here (not a caller obligation) so no Soft "callers must guard
// first" contract can drift; the always-non-nil return also keeps callers from
// branching on nil.
//
// On a real provider it builds and caches a SagaTailerCollector once per cellID:
// NewSagaTailerCollector registers a per-cell metric family, so calling it twice
// for the same cell would be a duplicate-registration conflict — the cache makes
// it at most once per cell.
func (b *Bootstrap) sagaTailerObserverFor(cellID string) (tailer.Observer, error) {
	if !b.hasRealMetricsProvider() {
		return tailer.NopObserver{}, nil
	}
	if obs, ok := b.sagaTailerObservers[cellID]; ok {
		return obs, nil
	}
	collector, err := metricsmiddleware.NewSagaTailerCollector(b.metricsProvider, cellID)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: cell %s: register saga tailer metrics: %w", cellID, err)
	}
	if b.sagaTailerObservers == nil {
		b.sagaTailerObservers = make(map[string]tailer.Observer)
	}
	b.sagaTailerObservers[cellID] = collector
	return collector, nil
}

// buildOneSagaTailer constructs the Tailer for one saga-journal ProjectionRequest.
// req.Apply (cell.ProjectionApply) is the same underlying type as projection.Apply
// (both alias cellvocab.ProjectionApply), so it passes straight through with no
// conversion — identical to how buildOneProjection passes req.Apply to the
// Coordinator. req.OnReset is IGNORED on this path (saga-journal rebuild is out of
// scope; NewSagaJournalProjectionRequest never sets it).
func (b *Bootstrap) buildOneSagaTailer(req cell.ProjectionRequest) (*tailer.Tailer, error) {
	src, err := b.sagaJournalSourceOnce()
	if err != nil {
		return nil, err
	}
	var opts []tailer.Option
	if b.sagaTailerConfigSet {
		opts = append(opts, tailer.WithConfig(b.sagaTailerConfig))
	}
	// Always non-nil (real collector or NopObserver); WithObserver(NopObserver{})
	// is equivalent to the Tailer's default, so an unconditional append is safe.
	obs, err := b.sagaTailerObserverFor(req.CellID)
	if err != nil {
		return nil, err
	}
	opts = append(opts, tailer.WithObserver(obs))

	// src is passed as BOTH the replay and cursor args (SagaJournalSource
	// implements both interfaces); the TxRunner is shared with the outbox path.
	return tailer.NewTailer(
		b.clock,
		src,                  // replay (projection.ReplaySource)
		src,                  // cursor (projection.Cursor)
		b.sagaProjOwnerStore, // fenced OwnerCheckpointStore
		b.projectionTxRunner, // shared tx runner
		req.Apply,            // projection.Apply (alias passthrough)
		b.sagaProjLocker,     // per-projection leader gate
		req.CellID,
		req.ProjectionID,
		opts...,
	)
}

// wireOneSagaTailer wires a constructed Tailer directly into bootstrap's lifecycle
// surfaces (see file header for why NOT WithManagedResource): each readiness probe
// is registered onto b.healthAggregator via the sanctioned registerHealthChecker
// wrapper, the Tailer's Worker is appended to b.workers (started at phase8), and a
// named Close teardown is recorded. It does NOT touch the event router or s.sub —
// the Tailer pulls from the journal, it does not subscribe.
func (b *Bootstrap) wireOneSagaTailer(s *phaseState, t *tailer.Tailer, cellID, projectionID string) error {
	for _, p := range t.Probes() {
		if err := s.registerHealthChecker(p.Name(), p.Check, b.healthAggregator); err != nil {
			return fmt.Errorf("bootstrap: cell %s saga-journal projection %q: register probe %q: %w",
				cellID, projectionID, p.Name(), err)
		}
	}
	b.workers = append(b.workers, t.Worker())
	// Named teardown so a Close failure surfaces the offending projection in the
	// phase10 shutdown phaseError (unnamed teardowns log phase="").
	s.addNamedTeardown("saga-tailer:"+cellID+"/"+projectionID, t.Close)
	// Lifecycle Info log (mirrors the outbox Coordinator drain) so ops can confirm
	// the long-running Tailer was registered at startup.
	slog.Info("bootstrap: wiring saga-journal projection tailer",
		slog.String("cell", cellID),
		slog.String("projection", projectionID))
	return nil
}
