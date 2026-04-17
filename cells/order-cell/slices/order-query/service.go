// Package orderquery implements the order-query slice: reading orders.
package orderquery

import (
	"context"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/query"
)

// orderSort defines the default sort for order listings.
var orderSort = []query.SortColumn{
	{Name: "created_at", Direction: query.SortDESC},
	{Name: "id", Direction: query.SortASC},
}

// Service handles order query business logic.
type Service struct {
	repo   domain.OrderRepository
	codec  *query.CursorCodec
	logger *slog.Logger
}

// NewService creates an order-query Service.
func NewService(repo domain.OrderRepository, codec *query.CursorCodec, logger *slog.Logger) *Service {
	return &Service{
		repo:   repo,
		codec:  codec,
		logger: logger,
	}
}

// GetByID returns a single order by ID.
func (s *Service) GetByID(ctx context.Context, id string) (*domain.Order, error) {
	return s.repo.GetByID(ctx, id)
}

// List returns a paginated page of orders.
func (s *Service) List(ctx context.Context, pageReq query.PageRequest) (query.PageResult[*domain.Order], error) {
	qctx := query.QueryContext("endpoint", "order-query")
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*domain.Order]{
		Codec:    s.codec,
		Request:  pageReq,
		Sort:     orderSort,
		QueryCtx: qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*domain.Order, error) {
			return s.repo.List(ctx, params)
		},
		Extract: func(o *domain.Order) []any {
			return []any{o.CreatedAt.Format(time.RFC3339Nano), o.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "order-query"),
		DemoMode:    s.codec.IsDemoKey(query.KnownDemoKeys()...),
	})
}
