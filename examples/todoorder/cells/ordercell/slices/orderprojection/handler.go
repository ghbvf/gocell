package orderprojection

import (
	"context"

	projectionsummary "github.com/ghbvf/gocell/generated/contracts/http/order/projection-summary/v1"
	projectionrebuild "github.com/ghbvf/gocell/generated/contracts/http/order/projection-rebuild/v1"
)

// Compile-time assertions: adapters implement the generated Service interfaces.
var (
	_ projectionsummary.Service = (*SummaryAdapter)(nil)
	_ projectionrebuild.Service = (*RebuildAdapter)(nil)
)

// SummaryAdapter bridges Service to the generated projectionsummary.Service interface.
type SummaryAdapter struct {
	S *Service
}

// NewSummaryAdapter creates a SummaryAdapter.
func NewSummaryAdapter(s *Service) *SummaryAdapter {
	return &SummaryAdapter{S: s}
}

// ProjectionSummary implements projectionsummary.Service.
func (a *SummaryAdapter) ProjectionSummary(ctx context.Context, _ *projectionsummary.Request) (projectionsummary.ProjectionSummaryResponseObject, error) {
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

// RebuildAdapter bridges Service to the generated projectionrebuild.Service interface.
type RebuildAdapter struct {
	S *Service
}

// NewRebuildAdapter creates a RebuildAdapter.
func NewRebuildAdapter(s *Service) *RebuildAdapter {
	return &RebuildAdapter{S: s}
}

// ProjectionRebuild implements projectionrebuild.Service.
func (a *RebuildAdapter) ProjectionRebuild(ctx context.Context, _ *projectionrebuild.Request) (projectionrebuild.ProjectionRebuildResponseObject, error) {
	report, err := a.S.Rebuild(ctx)
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
