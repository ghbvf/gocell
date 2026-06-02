package bootstrap

// projection_rebuild.go — the framework-owned projection rebuild control-plane
// endpoint.
//
//	POST /internal/v1/{cell}/projection/{name}/rebuild
//
// It is mounted by bootstrap itself (not a cell) on the InternalListener, the
// same framework-owned-RouteGroup pattern as the health endpoints in health.go —
// no contract.yaml, no codegen, no host cell. Opt-in via
// WithProjectionRebuildEndpoint(callers...); phase5CollectRouteGroups appends the
// RouteGroup when callers are set, and phase0 guarantees an InternalListener is
// declared.
//
// The endpoint dispatches by {cell}/{name} path params to the projection
// Coordinator registered in the phase6 drain (b.projectionCoordinators, read
// lazily at request time — phase6 runs after phase5 mounts the route but before
// any request is served). It honors the ADR-frozen status semantics: 202
// Accepted (rebuild admitted; body carries the {phase, pendingEvents,
// replayLagSeconds} snapshot) / 409 Conflict (a rebuild is already running) / 404
// Not Found (unknown cell/projection). The caller-cell allowlist (service-token
// + RequireCallerCell) is carried on the framework ContractSpec.Clients, so
// auth.Mount auto-injects the guard.
//
// ref: EventStoreDB projections HTTP admin API (POST /projection/{name}/command/
// reset) — generic server-provided control-plane keyed by projection name.
// ref: runtime/bootstrap/health.go — framework-owned RouteGroup mount pattern.

import (
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/internal/contractbuild"
)

const (
	projectionRebuildContractID = "http.framework.projection.rebuild.v1"
	projectionRebuildPath       = "/internal/v1/{cell}/projection/{name}/rebuild"
)

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
// endpoint was opted in (WithProjectionRebuildEndpoint with ≥1 caller) but no
// InternalListener is declared to mount it on. A no-caller opt-in is a no-op
// (the endpoint stays unmounted), so it needs no listener and passes here.
func (b *Bootstrap) validateProjectionRebuildEndpoint() error {
	if len(b.projectionRebuildCallers) == 0 {
		return nil
	}
	if _, ok := b.listenerConfigs[cell.InternalListener]; !ok {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"bootstrap: WithProjectionRebuildEndpoint requires a cell.InternalListener; "+
				"declare it via WithListener(cell.InternalListener, addr, authChain)")
	}
	return nil
}

// projectionRebuildRouteGroup builds the framework-owned RouteGroup for the
// rebuild endpoint on the InternalListener. It is only called when
// WithProjectionRebuildEndpoint opted in (b.projectionRebuildCallers non-empty)
// and phase0 verified an InternalListener exists. The caller-cell allowlist rides
// on ContractSpec.Clients (NewFrameworkHTTP variadic), so auth.Mount auto-injects
// RequireCallerCell — no explicit Route.Policy needed.
func (b *Bootstrap) projectionRebuildRouteGroup() cell.RouteGroup {
	spec := contractbuild.NewFrameworkHTTP(
		projectionRebuildContractID, http.MethodPost, projectionRebuildPath,
		b.projectionRebuildCallers...)
	handler := b.newProjectionRebuildHandler()
	return cell.RouteGroup{
		Listener: cell.InternalListener,
		Register: func(mux cell.RouteMux) error {
			return auth.Mount(mux, auth.Route{Contract: spec, Handler: handler})
		},
	}
}

// newProjectionRebuildHandler returns the http.Handler for the rebuild endpoint.
// It reads b.projectionCoordinators lazily (populated by the phase6 drain), so it
// captures the receiver, not a snapshot of the map.
func (b *Bootstrap) newProjectionRebuildHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		cellID := r.PathValue("cell")
		projID := r.PathValue("name")

		ctrl, ok := b.projectionCoordinators[cellID+"/"+projID]
		if !ok {
			httputil.WriteError(ctx, w, errcode.New(errcode.KindNotFound, errcode.ErrProjectionNotFound,
				"projection not found",
				errcode.WithDetails(
					errcode.PublicString("cell", cellID),
					errcode.PublicString("projection", projID),
				)))
			return
		}

		if err := ctrl.Rebuild(ctx); err != nil {
			// ErrRebuildInProgress is KindConflict → 409. Any other error (e.g. the
			// unreachable Subscribe-not-called invariant) maps via its own Kind.
			httputil.WriteError(ctx, w, err)
			return
		}

		// Rebuild admitted (202). Read a best-effort snapshot for the body; a
		// degraded read (store/replay error) is logged but never downgrades an
		// already-admitted rebuild to a 5xx — Phase is always valid.
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
