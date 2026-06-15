// Package orderprojection implements the public-facing order-projection slice
// (L3 CQRS harness reference): reg.RegisterProjection drives HandleOrderCreated
// (apply) and ResetOrderStatus (onReset) via the framework projection.Coordinator.
// This slice serves the summary query endpoint on the PrimaryListener (/api/v1).
// Rebuild is framework-owned (Coordinator.Rebuild), triggerable via the operator
// control-plane endpoint POST /admin/v1/projection/{cell}/{name}/rebuild on the
// AdminListener (#1505); the separate orderprojectionrebuild slice has been removed.
package orderprojection

import (
	"context"

	projectionsummary "github.com/ghbvf/gocell/generated/contracts/http/order/projection-summary/v1"
)

// Compile-time assertion: SummaryAdapter implements the generated Service interface.
var _ projectionsummary.Service = (*SummaryAdapter)(nil)

// SummaryAdapter bridges Service to the generated projectionsummary.Service interface.
type SummaryAdapter struct {
	svc *Service
}

// NewSummaryAdapter creates a SummaryAdapter.
func NewSummaryAdapter(s *Service) *SummaryAdapter {
	return &SummaryAdapter{svc: s}
}

// ProjectionSummary implements projectionsummary.Service.
//
// The response exposes only the per-status aggregate (status + count) and the
// global TotalOrders — NOT the per-order ids. This endpoint is coarse-gated
// (RequirePermission(order:list)) rather than owner-scoped, so emitting every
// owner's order ids here would leak order existence across owners; the aggregate
// counts are non-identifying and keep the CQRS read-model demo intact. The internal
// projection still tracks ids (Service.Query), they are simply not put on the wire.
func (a *SummaryAdapter) ProjectionSummary(
	ctx context.Context, _ *projectionsummary.Request,
) (projectionsummary.ProjectionSummaryResponseObject, error) {
	summary := a.svc.Query(ctx)

	statuses := make([]*projectionsummary.ResponseDataStatusesItem, 0, len(summary.Statuses))
	for _, b := range summary.Statuses {
		statuses = append(statuses, &projectionsummary.ResponseDataStatusesItem{
			Status: b.Status,
			Count:  b.Count,
		})
	}

	return projectionsummary.ProjectionSummary200JSONResponse(projectionsummary.Response{
		Data: &projectionsummary.ResponseData{
			Statuses:    statuses,
			TotalOrders: summary.TotalOrders,
		},
	}), nil
}
