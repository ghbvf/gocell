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
	pageReq.Normalize()

	var cursorValues []any
	if pageReq.Cursor != "" {
		cur, err := s.codec.Decode(pageReq.Cursor)
		if err != nil {
			return query.PageResult[*domain.Order]{}, err
		}
		if err := query.ValidateCursorScope(cur, orderSort); err != nil {
			return query.PageResult[*domain.Order]{}, err
		}
		cursorValues = cur.Values
	}

	params := query.ListParams{
		Limit:        pageReq.Limit,
		CursorValues: cursorValues,
		Sort:         orderSort,
	}

	orders, err := s.repo.List(ctx, params)
	if err != nil {
		return query.PageResult[*domain.Order]{}, err
	}

	return query.BuildPageResult(orders, pageReq.Limit, s.codec, orderSort, func(o *domain.Order) []any {
		return []any{o.CreatedAt.Format(time.RFC3339Nano), o.ID}
	})
}
