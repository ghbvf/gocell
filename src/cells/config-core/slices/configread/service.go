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
	pageReq.Normalize()

	var cursorValues []any
	if pageReq.Cursor != "" {
		cur, err := s.codec.Decode(pageReq.Cursor)
		if err != nil {
			return query.PageResult[*domain.ConfigEntry]{}, err
		}
		cursorValues = cur.Values
	}

	params := query.ListParams{
		Limit:        pageReq.Limit,
		CursorValues: cursorValues,
		Sort:         configSort,
	}

	entries, err := s.repo.List(ctx, params)
	if err != nil {
		return query.PageResult[*domain.ConfigEntry]{}, fmt.Errorf("config-read: list: %w", err)
	}

	return query.BuildPageResult(entries, pageReq.Limit, s.codec, func(e *domain.ConfigEntry) []any {
		return []any{e.Key, e.ID}
	})
}
