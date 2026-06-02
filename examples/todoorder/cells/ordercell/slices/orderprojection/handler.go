// Package orderprojection implements the public-facing order-projection slice
// (L3 CQRS harness reference): reg.RegisterProjection drives HandleOrderCreated
// (apply) and ResetOrderStatus (onReset) via the framework projection.Coordinator.
// This slice serves the summary query endpoint on the PrimaryListener (/api/v1).
// Rebuild is framework-owned (Coordinator.Rebuild), triggerable via the internal
// control-plane endpoint POST /internal/v1/{cell}/projection/{name}/rebuild; the
// separate orderprojectionrebuild slice has been removed.
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
func (a *SummaryAdapter) ProjectionSummary(
	ctx context.Context, _ *projectionsummary.Request,
) (projectionsummary.ProjectionSummaryResponseObject, error) {
	summary := a.svc.Query(ctx)

	statuses := make([]*projectionsummary.ResponseDataStatusesItem, 0, len(summary.Statuses))
	for _, b := range summary.Statuses {
		ids := make([]string, len(b.OrderIDs))
		copy(ids, b.OrderIDs)
		statuses = append(statuses, &projectionsummary.ResponseDataStatusesItem{
			Status:   b.Status,
			Count:    b.Count,
			OrderIds: ids,
		})
	}

	return projectionsummary.ProjectionSummary200JSONResponse(projectionsummary.Response{
		Data: &projectionsummary.ResponseData{
			Statuses:    statuses,
			TotalOrders: summary.TotalOrders,
		},
	}), nil
}
