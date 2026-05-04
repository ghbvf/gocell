// Package orderquery implements the order-query slice: reading orders.
package orderquery

import (
	"context"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	getv1 "github.com/ghbvf/gocell/generated/contracts/http/order/get/v1"
	listv1 "github.com/ghbvf/gocell/generated/contracts/http/order/list/v1"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// Compile-time assertions: Service implements both generated interfaces.
var _ getv1.Service = (*Service)(nil)
var _ listv1.Service = (*Service)(nil)

// orderSort defines the default sort for order listings.
var orderSort = []query.SortColumn{
	{Name: "created_at", Direction: query.SortDESC},
	{Name: "id", Direction: query.SortASC},
}

// Service handles order query business logic.
type Service struct {
	repo    domain.OrderRepository
	codec   *query.CursorCodec
	logger  *slog.Logger
	runMode query.RunMode
}

// NewService creates an order-query Service. runMode controls cursor
// fail-open vs fail-closed semantics; pass query.RunModeProd unless the
// assembly declares DurabilityDemo.
//
// codec must be non-nil — pagination cannot be served without a cursor codec.
// Passing nil is a caller programming error; NewService returns errcode.ErrCellMissingCodec
// so the cell Init() can propagate a structured error instead of a runtime panic.
func NewService(repo domain.OrderRepository, codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode) (*Service, error) {
	if codec == nil {
		return nil, errcode.New(errcode.ErrCellMissingCodec,
			"order-query: cursor codec is required")
	}
	return &Service{
		repo:    repo,
		codec:   codec,
		logger:  logger,
		runMode: runMode,
	}, nil
}

// GetByID returns a single order by ID.
func (s *Service) GetByID(ctx context.Context, id string) (*domain.Order, error) {
	return s.repo.GetByID(ctx, id)
}

// list is the internal paginated query implementation.
func (s *Service) list(ctx context.Context, pageReq query.PageParams) (query.PageResult[*domain.Order], error) {
	qctx := query.QueryContext("endpoint", "order-query")
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*domain.Order]{
		Codec:      s.codec,
		PageParams: pageReq,
		Sort:       orderSort,
		QueryCtx:   qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*domain.Order, error) {
			return s.repo.List(ctx, params)
		},
		Extract: func(o *domain.Order) []any {
			return []any{o.CreatedAt.Format(time.RFC3339Nano), o.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "order-query"),
		RunMode:     s.runMode,
	})
}

// Get implements getv1.Service.
func (s *Service) Get(ctx context.Context, req *getv1.Request) (*getv1.Response, error) {
	order, err := s.GetByID(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return toGetResponse(order), nil
}

// List implements listv1.Service.
func (s *Service) List(ctx context.Context, req *listv1.Request) (*listv1.Response, error) {
	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}
	result, err := s.list(ctx, pageReq)
	if err != nil {
		return nil, err
	}
	return toListResponse(result), nil
}

func toGetResponse(o *domain.Order) *getv1.Response {
	if o == nil {
		return nil
	}
	return &getv1.Response{
		Data: &getv1.ResponseData{
			ID:        o.ID,
			Item:      o.Item,
			Status:    o.Status,
			CreatedAt: o.CreatedAt.Format(time.RFC3339),
		},
	}
}

func toListResponse(result query.PageResult[*domain.Order]) *listv1.Response {
	items := make([]*listv1.ResponseDataItem, 0, len(result.Items))
	for _, o := range result.Items {
		items = append(items, &listv1.ResponseDataItem{
			ID:        o.ID,
			Item:      o.Item,
			Status:    o.Status,
			CreatedAt: o.CreatedAt.Format(time.RFC3339),
		})
	}
	return &listv1.Response{
		Data:       items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}
}
