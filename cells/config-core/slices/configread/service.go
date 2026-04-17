// Package configread implements the config-read slice: Get/List config entries.
package configread

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/query"
)

// configSort defines the default sort for config listings.
var configSort = []query.SortColumn{
	{Name: "key", Direction: query.SortASC},
	{Name: "id", Direction: query.SortASC},
}

// Service implements config read business logic.
type Service struct {
	repo   ports.ConfigRepository
	codec  *query.CursorCodec
	logger *slog.Logger
}

// NewService creates a config-read Service.
func NewService(repo ports.ConfigRepository, codec *query.CursorCodec, logger *slog.Logger) *Service {
	return &Service{
		repo:   repo,
		codec:  codec,
		logger: logger,
	}
}

// GetByKey retrieves a config entry by key.
func (s *Service) GetByKey(ctx context.Context, key string) (*domain.ConfigEntry, error) {
	entry, err := s.repo.GetByKey(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("config-read: get: %w", err)
	}
	return entry, nil
}

// List returns a paginated page of config entries.
func (s *Service) List(ctx context.Context, pageReq query.PageRequest) (query.PageResult[*domain.ConfigEntry], error) {
	qctx := query.QueryContext("endpoint", "config-read")
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*domain.ConfigEntry]{
		Codec:    s.codec,
		Request:  pageReq,
		Sort:     configSort,
		QueryCtx: qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*domain.ConfigEntry, error) {
			entries, err := s.repo.List(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("config-read: list: %w", err)
			}
			return entries, nil
		},
		Extract: func(e *domain.ConfigEntry) []any {
			return []any{e.Key, e.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "configread"),
	})
}
