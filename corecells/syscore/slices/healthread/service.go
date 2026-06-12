// Package healthread serves the aggregated cell-health contract
// http.admin.health.cells.v1 (#1860). It is the read-only HTTP face over the
// runtime HealthView (assembly + healthz aggregator) that bootstrap injects into
// request context. The slice holds NO cross-cell references — it never imports a
// sibling cell; the cross-cell aggregate is a framework-provided read view.
package healthread

import (
	"context"

	cells "github.com/ghbvf/gocell/generated/contracts/http/admin/health/cells/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/syshealth"
)

// msgHealthViewUnavailable is the const wire message for the fail-closed 503 when
// the runtime HealthView was not injected into request context.
const msgHealthViewUnavailable = "runtime health view unavailable"

// Service implements the generated cells.Service. It is stateless: the runtime
// HealthView is read per request from context (injected by bootstrap, mirroring
// the ABAC Authorizer funnel), so the service holds no assembly/aggregator
// reference and no sibling-cell import.
type Service struct{}

// NewService constructs the stateless healthread service. It calls the generated
// validateRequired() (REQUIRED-DEP-NIL-GUARD-01 convention); the service has no
// required deps so it never errors, but the call keeps the funnel uniform and
// future-proof if a dependency is ever added.
func NewService() (*Service, error) {
	s := &Service{}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Cells implements cells.Service: read the request-scoped HealthView and project
// it to the wire DTO. Absence of the view is fail-closed (503) — never a silent
// empty report (an empty cells array would read as "all cells gone").
func (s *Service) Cells(ctx context.Context, _ *cells.Request) (cells.CellsResponseObject, error) {
	view, ok := syshealth.HealthViewFromContext(ctx)
	if !ok {
		return cells.Cells503ErrorResponse{
			Body: *errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, msgHealthViewUnavailable),
		}, nil
	}
	return cells.Cells200JSONResponse{Data: toResponseData(view.Report(ctx))}, nil
}

// toResponseData projects a syshealth.Report onto the generated wire DTO. Slices
// are always non-nil (make(..., 0, ...)) so an empty report serializes cells /
// adapters / deps as `[]` rather than `null` — the wire arrays are required and a
// consumer can iterate them unconditionally.
func toResponseData(rep syshealth.Report) *cells.ResponseData {
	out := &cells.ResponseData{
		Overall:  rep.Overall,
		Cells:    make([]*cells.ResponseDataCellsItem, 0, len(rep.Cells)),
		Adapters: make([]*cells.ResponseDataAdaptersItem, 0, len(rep.Adapters)),
	}
	for _, c := range rep.Cells {
		out.Cells = append(out.Cells, &cells.ResponseDataCellsItem{
			ID:     c.ID,
			Live:   c.Live,
			Ready:  c.Ready,
			Status: c.Status,
			Deps:   toDepItems(c.Deps),
		})
	}
	for _, a := range rep.Adapters {
		out.Adapters = append(out.Adapters, &cells.ResponseDataAdaptersItem{
			Name:       a.Name,
			Status:     a.Status,
			DurationMs: a.DurationMs,
		})
	}
	return out
}

// toDepItems projects a cell's probe slice onto the generated dep DTO. Returns a
// non-nil empty slice for a cell with no deps (serializes as `[]`, not `null`).
func toDepItems(deps []syshealth.ProbeHealth) []*cells.ResponseDataCellsItemDepsItem {
	out := make([]*cells.ResponseDataCellsItemDepsItem, 0, len(deps))
	for _, d := range deps {
		out = append(out, &cells.ResponseDataCellsItemDepsItem{
			Name:       d.Name,
			Status:     d.Status,
			DurationMs: d.DurationMs,
		})
	}
	return out
}

// Handler wires the generated cells.Handler with the system:read PDP gate. The
// permission-based gate (not a role literal) follows tenancy.md §"ABAC authz 接线":
// the baseline grants system:read to admin/super-admin.
type Handler struct {
	h *cells.Handler
}

// NewHandler builds the route handler with the system:read permission policy.
func NewHandler(svc *Service) *Handler {
	return &Handler{h: cells.NewHandler(svc, auth.RequirePermission(authz.PermSystemRead()))}
}

// RegisterRoutes mounts the contract on mux via the generated handler.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	return h.h.RegisterRoutes(mux)
}
