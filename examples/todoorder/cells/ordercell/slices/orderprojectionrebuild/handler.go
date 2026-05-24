// Package orderprojectionrebuild implements the internal control-plane
// projection-rebuild slice: POST /internal/v1/orders/projection/rebuild,
// mounted on the InternalListener (service-token + caller-cell auth). It
// shares the projection store with the public orderprojection slice via the
// injected *orderprojection.Service. Public and internal HTTP surfaces are
// kept in separate slices per governance rule
// SLICE-HTTP-VISIBILITY-SEGREGATION-01 (FMT-33).
package orderprojectionrebuild

import (
	"context"

	orderprojection "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/slices/orderprojection"
	projectionrebuild "github.com/ghbvf/gocell/generated/contracts/http/order/projection-rebuild/v1"
)

// Compile-time assertion: RebuildAdapter implements the generated Service interface.
var _ projectionrebuild.Service = (*RebuildAdapter)(nil)

// RebuildAdapter bridges orderprojection.Service to the generated
// projectionrebuild.Service interface for the internal rebuild endpoint.
type RebuildAdapter struct {
	svc *orderprojection.Service
}

// NewRebuildAdapter creates a RebuildAdapter.
func NewRebuildAdapter(s *orderprojection.Service) *RebuildAdapter {
	return &RebuildAdapter{svc: s}
}

// ProjectionRebuild implements projectionrebuild.Service.
func (a *RebuildAdapter) ProjectionRebuild(
	ctx context.Context, _ *projectionrebuild.Request,
) (projectionrebuild.ProjectionRebuildResponseObject, error) {
	report, err := a.svc.Rebuild(ctx)
	if err != nil {
		return nil, err
	}

	return projectionrebuild.ProjectionRebuild200JSONResponse(projectionrebuild.Response{
		Data: &projectionrebuild.ResponseData{
			EventsReplayed:  int64(report.EventsReplayed),
			StatusesRebuilt: int64(report.StatusesRebuilt),
			LastAppliedSeq:  report.LastAppliedSeq,
		},
	}), nil
}
