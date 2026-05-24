// Package orderprojection implements the public-facing order-projection slice
// (L3): subscribes to order-created and order-status-changed events, maintains
// an in-memory "by-status" read model, and serves the summary query endpoint on
// the PrimaryListener (/api/v1). The internal control-plane rebuild endpoint is
// kept in a separate slice (orderprojectionrebuild) per governance rule
// SLICE-HTTP-VISIBILITY-SEGREGATION-01 (FMT-33).
package orderprojection

import (
	"context"

	projectionsummary "github.com/ghbvf/gocell/generated/contracts/http/order/projection-summary/v1"
)

// Compile-time assertion: SummaryAdapter implements the generated Service interface.
var _ projectionsummary.Service = (*SummaryAdapter)(nil)

// SummaryAdapter bridges Service to the generated projectionsummary.Service interface.
type SummaryAdapter struct {
	S *Service
}

// NewSummaryAdapter creates a SummaryAdapter.
func NewSummaryAdapter(s *Service) *SummaryAdapter {
	return &SummaryAdapter{S: s}
}

// ProjectionSummary implements projectionsummary.Service.
func (a *SummaryAdapter) ProjectionSummary(
	ctx context.Context, _ *projectionsummary.Request,
) (projectionsummary.ProjectionSummaryResponseObject, error) {
	summary := a.S.Query(ctx)

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
			Statuses:       statuses,
			TotalOrders:    summary.TotalOrders,
			LastAppliedSeq: summary.LastAppliedSeq,
		},
	}), nil
}
