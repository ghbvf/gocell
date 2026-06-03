package bootstrap

// projection_rebuild.go — the framework-owned projection rebuild control-plane
// endpoint.
//
//	POST /admin/v1/projection/{cell}/{name}/rebuild
//
// It is mounted by bootstrap itself (not a cell) on the AdminListener, the same
// framework-owned-RouteGroup pattern as the health endpoints in health.go — no
// contract.yaml, no codegen, no host cell. Opt-in via
// WithProjectionRebuildEndpoint(); phase5CollectRouteGroups appends the
// RouteGroup when enabled, and phase0 guarantees an AdminListener is declared.
//
// This is an operator→system action (an administrator / deployment pipeline
// rebuilds a projection), NOT a cell→cell call. Authentication is the
// AdminListener's operator-credential gate (AuthOperator: env credentials over a
// network-isolated loopback port); there is no caller-cell allowlist, so the
// framework ContractSpec carries no Clients (#1505 — migrated off the prior
// /internal/v1/* service-token + RequireCallerCell model).
//
// The endpoint dispatches by {cell}/{name} path params to the projection
// Coordinator registered in the phase6 drain (b.projectionRebuilds, read
// lazily at request time — phase6 runs after phase5 mounts the route but before
// any request is served). It honors the ADR-frozen status semantics: 202
// Accepted (rebuild admitted; body carries the {phase, pendingEvents,
// replayLagSeconds} snapshot) / 409 Conflict (a rebuild is already running) / 404
// Not Found (unknown cell/projection).
//
// ref: EventStoreDB projections HTTP admin API (POST /projection/{name}/command/
// reset) — network-isolated admin port + operator credentials, keyed by
// projection name.
// ref: runtime/bootstrap/health.go — framework-owned RouteGroup mount pattern.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/internal/contractbuild"
)

// rebuildController is the narrow control-plane surface the rebuild HTTP handler
// consumes from a projection Coordinator: trigger a background rebuild and read a
// status snapshot. *projection.Coordinator satisfies it.
//
// It is declared here — the consumer — rather than in kernel/projection: the
// accept-interfaces-at-the-consumer idiom keeps kernel's exported surface free of
// a bootstrap-only seam while keeping the handler honest (it only triggers + reads,
// never touches the raw checkpoint store / tx runner) and unit-testable with a fake.
type rebuildController interface {
	// Rebuild triggers a background full rebuild; nil = admitted (caller → 202),
	// projection.ErrRebuildInProgress = already running (caller → 409).
	Rebuild(ctx context.Context) error
	// Snapshot returns the current phase plus best-effort pending/lag. On a
	// store/replay read error the Phase is still valid; only PendingEvents and
	// ReplayLagSeconds are zeroed, and the error is returned for the caller to log
	// (a degraded snapshot never downgrades an already-admitted rebuild).
	Snapshot(ctx context.Context) (projection.Snapshot, error)
}

// Compile-time assertion that *projection.Coordinator satisfies rebuildController.
var _ rebuildController = (*projection.Coordinator)(nil)

const (
	projectionRebuildContractID = "http.framework.projection.rebuild.v1"
	projectionRebuildPath       = "/admin/v1/projection/{cell}/{name}/rebuild"
	// maxPathParamReport bounds the cell/projection path-param length echoed in
	// the 404 body / logs. Path params reach the handler unvalidated; registered
	// projection keys are far shorter (cell ≤32, projection snake_case), so a
	// value beyond this is necessarily a miss — truncating bounds the response
	// body and log line against an oversized (authorized-caller) path.
	maxPathParamReport = 64
)

// clampReport truncates an unvalidated path-param value to maxPathParamReport
// runes for safe echoing in the 404 body / logs.
func clampReport(s string) string {
	if len(s) <= maxPathParamReport {
		return s
	}
	return s[:maxPathParamReport]
}

// projectionRebuildResponseData is the data object of the 202 response body.
// JSON field names are camelCase per the API convention.
type projectionRebuildResponseData struct {
	Phase            string  `json:"phase"`
	PendingEvents    int64   `json:"pendingEvents"`
	ReplayLagSeconds float64 `json:"replayLagSeconds"`
}

// projectionRebuildResponse is the unified {"data": {...}} single-resource
// envelope for the 202 response.
type projectionRebuildResponse struct {
	Data projectionRebuildResponseData `json:"data"`
}

// validateProjectionRebuildEndpoint fails fast in phase0 when the rebuild
// endpoint was opted in (WithProjectionRebuildEndpoint) but no AdminListener is
// declared to mount it on. Not opting in is a no-op (the endpoint stays
// unmounted), so it needs no listener and passes here.
func (b *Bootstrap) validateProjectionRebuildEndpoint() error {
	if !b.projectionRebuildEnabled {
		return nil
	}
	if _, ok := b.listenerConfigs[cell.AdminListener]; !ok {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"bootstrap: WithProjectionRebuildEndpoint requires a cell.AdminListener; "+
				"declare it via WithListener(cell.AdminListener, addr, []kauth.ListenerAuth{operatorAuth})")
	}
	return nil
}

// projectionRebuildRouteGroup builds the framework-owned RouteGroup for the
// rebuild endpoint on the AdminListener. It is only called when
// WithProjectionRebuildEndpoint opted in (b.projectionRebuildEnabled) and phase0
// verified an AdminListener exists. Authentication is the listener's operator
// credential gate (AuthOperator); there is no caller-cell allowlist, so the
// framework ContractSpec carries no Clients (NewFrameworkHTTP with no callers —
// /admin/v1/* is an ordinary non-internal path, and validateHTTP forbids Clients
// on non-internal paths, so the empty set is correct).
//
// The RouteGroup carries no CellID (like the health groups), so HTTP metrics
// attribute it to cell="_runtime" (RuntimeCellSentinel) — correct for a
// framework control-plane endpoint that is not owned by a business cell. Filter
// rebuild traffic by route template, not by the cell label.
func (b *Bootstrap) projectionRebuildRouteGroup() cell.RouteGroup {
	spec := contractbuild.NewFrameworkHTTP(
		projectionRebuildContractID, http.MethodPost, projectionRebuildPath)
	handler := b.newProjectionRebuildHandler()
	return cell.RouteGroup{
		Listener: cell.AdminListener,
		Register: func(mux cell.RouteMux) error {
			return auth.Mount(mux, auth.Route{Contract: spec, Handler: handler})
		},
	}
}

// newProjectionRebuildHandler returns the http.Handler for the rebuild endpoint.
// It reads b.projectionRebuilds lazily (populated by the phase6 drain), so it
// captures the receiver, not a snapshot of the map.
func (b *Bootstrap) newProjectionRebuildHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		cellID := r.PathValue("cell")
		projID := r.PathValue("name")

		ctrl, ok := b.projectionRebuilds[cellID+"/"+projID]
		if !ok {
			httputil.WriteError(ctx, w, errcode.New(errcode.KindNotFound, errcode.ErrProjectionNotFound,
				"projection not found",
				errcode.WithDetails(
					errcode.PublicString("cell", clampReport(cellID)),
					errcode.PublicString("projection", clampReport(projID)),
				)))
			return
		}

		if err := ctrl.Rebuild(ctx); err != nil {
			if errors.Is(err, projection.ErrRebuildInProgress) {
				httputil.WriteError(ctx, w, err) // KindConflict → 409
				return
			}
			// Any other Rebuild error is unreachable for a registered Coordinator
			// (the phase6 drain always Subscribes before registering, so the
			// Subscribe-not-called invariant cannot fire here). Treat it as a
			// framework fault (500), not a client 400 — the caller did nothing
			// wrong. Keeps the wire status set to the ADR-frozen 202/409/404 plus
			// the implicit framework 5xx.
			httputil.WriteError(ctx, w, errcode.New(errcode.KindInternal, errcode.ErrInternal,
				"projection rebuild: unexpected coordinator state",
				errcode.WithInternal(errcode.InternalAttr("error", err.Error()))))
			return
		}

		// Rebuild admitted (202). Audit-log the control-plane action (the access
		// log correlates the rest via request_id, but a rebuild is an operator
		// action worth an explicit Info record). This is an operator→system
		// action authenticated by the AdminListener's operator credentials; there
		// is no caller-cell identity to record (that was the prior /internal/v1/*
		// cell→cell model, #1505).
		admitAttrs := httputil.AppendCorrelationAttrs(ctx, []any{
			slog.String("cell", cellID),
			slog.String("projection", projID),
		})
		slog.InfoContext(ctx, "projection rebuild admitted", admitAttrs...)

		// Read a best-effort snapshot for the body; a degraded read (store/replay
		// error) is logged but never downgrades an already-admitted rebuild to a
		// 5xx — Phase is always valid.
		snap, snapErr := ctrl.Snapshot(ctx)
		if snapErr != nil {
			attrs := httputil.AppendCorrelationAttrs(ctx, []any{
				slog.String("cell", cellID),
				slog.String("projection", projID),
				slog.Any("error", snapErr),
			})
			slog.WarnContext(ctx, "projection rebuild: snapshot read degraded after admission", attrs...)
		}

		httputil.WriteJSON(w, http.StatusAccepted, projectionRebuildResponse{
			Data: projectionRebuildResponseData{
				Phase:            snap.Phase.String(),
				PendingEvents:    snap.PendingEvents,
				ReplayLagSeconds: snap.ReplayLagSeconds,
			},
		})
	})
}
