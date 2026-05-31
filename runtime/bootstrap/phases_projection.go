package bootstrap

// phases_projection.go — L3 CQRS projection drain (part of phase6).
//
// Covers:
//   - buildProjectionCoordinators: the unit-testable core — constructs one
//     projection.Coordinator per RegistrySnapshot.Projections entry from the
//     framework-owned deps and captures each Coordinator's wrapped event
//     subscription (no event router needed; the capture adapter stands in for
//     one), mirroring how phase5DrainWebhookReceivers returns RouteGroups.
//   - drainCellProjections: wires each coordinator into the running event
//     router (AddContractHandler), registers its readiness probes, records a
//     Close teardown, and indexes it for the PR-04 rebuild HTTP endpoint.
//   - cellSnapshotsHaveProjections: phase6 "is there work?" predicate.
//
// # Why coordinator construction lives here, not in the cell
//
// projection.Coordinator needs raw framework infrastructure (CheckpointStore /
// TxRunner / Cursor / ReplaySource). Cells must never reach that — they hold
// only sealed markers (cell-raw-infra archtests). So the cell records intent via
// reg.RegisterProjection (record-only) and bootstrap, which legally holds the
// raw deps, constructs the Coordinator here. This is the same record-in-Init /
// drain-in-bootstrap split as Subscribe and webhook receivers.
//
// # Why a capture Registrar
//
// Coordinator.Subscribe internally calls reg.Subscribe (it was designed to run
// during Init against the live RegistryRecorder). By phase6 the recorder is
// finalized, so the drain passes the Coordinator a captureRegistrar instead: its
// Subscribe records the wrapped (spec, handler, consumerGroup, cellID, sliceID)
// rather than appending to a finalized recorder. The drain then hands that
// captured SubscriptionRequest to evtRouter.AddContractHandler — the same sink
// drainCellSubscriptions uses. The Coordinator is unchanged; only its injected
// Registrar differs (a legitimate dependency swap).
//
// # AI-robust
//
// This is the single sanctioned Coordinator.Subscribe call site (allowlisted by
// PROJECTION-APPLY-HOOK-FUNNEL-01). reg.RegisterProjection callers are locked to
// cellgen-derived cell_gen.go by PROJECTION-REGISTER-FUNNEL-01. Both are Medium
// (Go cannot type-gate callers of a public method); the Hard-ification path
// (cellgen-only sealed-token parameter) is tracked as a gh follow-up to #1176.
// The raw-infra-stays-in-bootstrap guarantee is the Hard property (type system):
// cells hold sealed markers, never the CheckpointStore/TxRunner constructed here.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/contractspec"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/eventrouter"
)

// projectionWiring bundles a constructed Coordinator with the wrapped event
// subscription it captured via captureRegistrar.Subscribe. It is the
// intermediate value produced by buildProjectionCoordinators and consumed by
// drainCellProjections: the sub field carries exactly what the event router
// needs (spec, handler, consumerGroup, cellID, sliceID); the coord is retained
// for probe registration, the Close teardown, and the rebuild HTTP endpoint
// (PR-04e) keyed by "<cellID>/<projectionID>".
type projectionWiring struct {
	coord        *projection.Coordinator
	cellID       string
	projectionID string
	sub          cell.SubscriptionRequest
}

// captureRegistrar satisfies projection.SubscribeRegistrar (Subscribe-only): it
// captures what Coordinator.Subscribe forwards instead of recording into a
// (finalized) RegistryRecorder. The Coordinator holds the narrow
// SubscribeRegistrar interface, so there is NO nil-embedded full cell.Registrar
// and no foot-gun — the Coordinator structurally cannot call any other Registrar
// method on this adapter.
//
// Compile-time check that captureRegistrar is a valid Coordinator registrar.
var _ projection.SubscribeRegistrar = (*captureRegistrar)(nil)

type captureRegistrar struct {
	captured *cell.SubscriptionRequest
}

func (c *captureRegistrar) Subscribe(
	spec contractspec.ContractSpec,
	handler outbox.EntryHandler,
	consumerGroup string,
	cellID string,
	opts ...cell.SubscriptionOption,
) error {
	// Defensive: Coordinator.Subscribe is a once-only CAS, so this adapter must
	// capture exactly one subscription. A second call would mean the Coordinator
	// contract changed — surface it instead of silently overwriting.
	if c.captured != nil {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"projection drain: capture registrar received more than one Subscribe call")
	}
	req := cell.SubscriptionRequest{
		Spec:          spec,
		Handler:       handler,
		ConsumerGroup: consumerGroup,
		CellID:        cellID,
	}
	for _, o := range opts {
		if o != nil {
			o(&req)
		}
	}
	c.captured = &req
	return nil
}

// cellSnapshotsHaveProjections reports whether any cell snapshot declared at
// least one projection. Used alongside cellSnapshotsHaveSubscriptions to decide
// whether phase6 must build the event router.
func cellSnapshotsHaveProjections(s *phaseState) bool {
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		if len(snap.Projections) > 0 {
			return true
		}
	}
	return false
}

// buildProjectionCoordinators constructs one projection.Coordinator per
// ProjectionRequest in the cell snapshots and captures each Coordinator's
// wrapped event subscription. It is the unit-testable core of the projection
// drain — it touches no event router.
//
// CellID drift (req.CellID != snapshot owner) is a codegen defect → fail-fast
// (same as drainCellSubscriptions). A non-empty projection set requires all four
// framework deps; a missing one yields an errcode naming the absent option.
func (b *Bootstrap) buildProjectionCoordinators(ctx context.Context, s *phaseState) ([]projectionWiring, error) {
	var out []projectionWiring
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		if len(snap.Projections) == 0 {
			continue
		}
		// Required-dep check runs once per cell that declares any projection
		// (not per request); wrap with the cell context so ops sees which cell
		// triggered the missing-option error.
		if err := b.checkProjectionDeps(); err != nil {
			return nil, fmt.Errorf("bootstrap: cell %s: %w", id, err)
		}
		for _, req := range snap.Projections {
			if req.CellID != id {
				return nil, fmt.Errorf(
					"bootstrap: cell %s projection drift: declared CellID=%q but snapshot owner=%q"+
						" (codegen should inject cellID from cell metadata; check cellgen templates)",
					id, req.CellID, id)
			}
			w, err := b.buildOneProjection(ctx, req)
			if err != nil {
				return nil, err
			}
			out = append(out, w)
		}
	}
	return out, nil
}

// checkProjectionDeps fails fast (with the missing option named) when a
// projection has been declared but a required framework dependency was not
// wired via the corresponding bootstrap option.
func (b *Bootstrap) checkProjectionDeps() error {
	switch {
	case b.projectionStore == nil:
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: projection declared but no checkpoint store configured; "+
				"add WithProjectionCheckpointStore to bootstrap options")
	case b.projectionTxRunner == nil:
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: projection declared but no tx runner configured; "+
				"add WithProjectionTxRunner to bootstrap options")
	case b.projectionReplay == nil:
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: projection declared but no replay source configured; "+
				"add WithProjectionReplaySource to bootstrap options")
	case b.projectionCursor == nil:
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: projection declared but no cursor configured; "+
				"add WithProjectionCursor to bootstrap options")
	}
	return nil
}

// buildOneProjection constructs the Coordinator for one ProjectionRequest and
// drives Coordinator.Subscribe through a captureRegistrar to obtain the wrapped
// subscription. The cell-local ProjectionApply / ProjectionResetHook are
// converted to the identical projection.Apply / projection.OnReset signatures
// (legal named-type conversion; the conversion lives here — runtime/bootstrap
// may import both kernel/cell and kernel/projection — never in kernel/cell,
// which would be an import cycle).
func (b *Bootstrap) buildOneProjection(ctx context.Context, req cell.ProjectionRequest) (projectionWiring, error) {
	tracer := b.wrapperTracer
	if validation.IsNilInterface(tracer) {
		// NewCoordinator requires a non-nil Tracer; degrade to no-op spans
		// (same fallback as NewContractTracingSubscriber when WithTracer is omitted).
		tracer = wrapper.NoopTracer{}
	}

	// Wire metrics when a real (non-Nop) provider is configured; leave cfg.Metrics
	// nil when no provider is set so the Coordinator silently disables instruments.
	// Mirrors the autoWireHTTPMetricsCollector / autoWireEventRouterCollector pattern.
	var projMetrics *projection.Metrics
	if p := b.metricsProvider; p != nil {
		if _, isNop := p.(kernelmetrics.NopProvider); !isNop {
			var regErr error
			projMetrics, regErr = projection.RegisterMetrics(p)
			if regErr != nil {
				slog.Warn("bootstrap: projection metrics registration failed; running without metrics",
					slog.String("cell", req.CellID),
					slog.String("projection", req.ProjectionID),
					slog.String("error", regErr.Error()))
				projMetrics = nil // defensive: treat registration error as no-metrics
			}
		}
	}

	capReg := &captureRegistrar{}
	coord, err := projection.NewCoordinator(b.clock, projection.CoordinatorConfig{
		Registrar:    capReg,
		CellID:       req.CellID,
		ProjectionID: req.ProjectionID,
		TxRunner:     b.projectionTxRunner,
		Store:        b.projectionStore,
		Cursor:       b.projectionCursor,
		Replay:       b.projectionReplay,
		Tracer:       tracer,
		Metrics:      projMetrics,
	})
	if err != nil {
		return projectionWiring{}, fmt.Errorf(
			"bootstrap: cell %s projection %q: construct coordinator: %w", req.CellID, req.ProjectionID, err)
	}

	var opts []projection.Option
	if req.OnReset != nil {
		opts = append(opts, projection.WithOnReset(projection.OnReset(req.OnReset)))
	}
	if err := coord.Subscribe(ctx, req.Spec, projection.Apply(req.Apply), opts...); err != nil {
		return projectionWiring{}, fmt.Errorf(
			"bootstrap: cell %s projection %q: subscribe: %w", req.CellID, req.ProjectionID, err)
	}
	if capReg.captured == nil {
		return projectionWiring{}, fmt.Errorf(
			"bootstrap: cell %s projection %q: coordinator registered no subscription", req.CellID, req.ProjectionID)
	}

	// F2: honour the caller-declared SliceID when present; fall back to the
	// projectionID that Coordinator.Subscribe injected via
	// cell.WithSubscriptionSliceID(projectionID). This keeps the 04a seam safe
	// (no production fill path yet) while giving 04b cellgen a place to
	// land the slice-metadata-derived value without touching the Coordinator.
	sub := *capReg.captured
	if req.SliceID != "" {
		sub.SliceID = req.SliceID
	}

	return projectionWiring{
		coord:        coord,
		cellID:       req.CellID,
		projectionID: req.ProjectionID,
		sub:          sub,
	}, nil
}

// drainCellProjections constructs the projection coordinators and wires each
// into the running event router: registers the captured wrapped handler,
// registers the Coordinator's readiness probes, records a Close teardown, and
// indexes the Coordinator for the rebuild HTTP endpoint (PR-04).
//
// Runs inside phase6 after drainCellSubscriptions and before the router starts,
// so projection handlers are registered before consumption begins.
func (b *Bootstrap) drainCellProjections(ctx context.Context, s *phaseState, evtRouter *eventrouter.Router) error {
	wirings, err := b.buildProjectionCoordinators(ctx, s)
	if err != nil {
		return err
	}
	if len(wirings) > 0 {
		slog.Info("bootstrap: draining projection coordinators",
			slog.Int("count", len(wirings)))
	}
	for _, w := range wirings {
		if err := b.wireOneProjection(s, evtRouter, w); err != nil {
			return err
		}
	}
	return nil
}

// wireOneProjection registers one projection wiring into the running event
// router: the captured wrapped handler, the Coordinator's readiness probes, a
// named Close teardown, and the rebuild-endpoint coordinator index.
func (b *Bootstrap) wireOneProjection(s *phaseState, evtRouter *eventrouter.Router, w projectionWiring) error {
	var opts []cell.SubscriptionOption
	if w.sub.SliceID != "" {
		opts = append(opts, cell.WithSubscriptionSliceID(w.sub.SliceID))
	}
	if err := evtRouter.AddContractHandler(
		w.sub.Spec, w.sub.Handler, w.sub.ConsumerGroup, w.sub.CellID, opts...); err != nil {
		return fmt.Errorf("bootstrap: cell %s projection %q: handler setup failed: %w",
			w.cellID, w.projectionID, err)
	}

	probes, err := w.coord.Probes()
	if err != nil {
		return fmt.Errorf("bootstrap: cell %s projection %q: build probes: %w",
			w.cellID, w.projectionID, err)
	}
	for _, p := range probes {
		// Route through the sanctioned registerHealthChecker wrapper (which
		// performs the single allowlisted Aggregator.Register call) rather than
		// calling b.healthAggregator.Register directly — keeps the
		// PROBENAME-SEALED-FUNNEL-01/A3 allowlist closed to its existing sites.
		if err := s.registerHealthChecker(p.Name(), p.Check, b.healthAggregator); err != nil {
			return fmt.Errorf("bootstrap: cell %s projection %q: register probe %q: %w",
				w.cellID, w.projectionID, p.Name(), err)
		}
	}

	coord := w.coord // capture for the teardown closure
	// Named teardown so a Close failure surfaces the offending projection in the
	// phase10 shutdown phaseError (unnamed teardowns log phase="").
	s.addNamedTeardown("projection:"+w.cellID+"/"+w.projectionID,
		func(c context.Context) error { return coord.Close(c) })

	if b.projectionCoordinators == nil {
		b.projectionCoordinators = make(map[string]*projection.Coordinator)
	}
	b.projectionCoordinators[w.cellID+"/"+w.projectionID] = coord
	return nil
}
