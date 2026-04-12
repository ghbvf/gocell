// Package featureflag implements the feature-flag slice: Get/Evaluate feature
// flags.
package featureflag

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// flagSort defines the default sort for flag listings.
var flagSort = []query.SortColumn{
	{Name: "key", Direction: "ASC"},
	{Name: "id", Direction: "ASC"},
}

// EvaluateResult holds the result of a flag evaluation.
type EvaluateResult struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

// Service implements feature flag business logic.
type Service struct {
	repo   ports.FlagRepository
	codec  *query.CursorCodec
	logger *slog.Logger
}

// NewService creates a feature-flag Service.
func NewService(repo ports.FlagRepository, codec *query.CursorCodec, logger *slog.Logger) *Service {
	return &Service{
		repo:   repo,
		codec:  codec,
		logger: logger,
	}
}

// GetByKey retrieves a feature flag by key.
func (s *Service) GetByKey(ctx context.Context, key string) (*domain.FeatureFlag, error) {
	flag, err := s.repo.GetByKey(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("feature-flag: get: %w", err)
	}
	return flag, nil
}

// List returns a paginated page of feature flags.
func (s *Service) List(ctx context.Context, pageReq query.PageRequest) (query.PageResult[*domain.FeatureFlag], error) {
	pageReq.Normalize()

	var cursorValues []any
	if pageReq.Cursor != "" {
		cur, err := s.codec.Decode(pageReq.Cursor)
		if err != nil {
			return query.PageResult[*domain.FeatureFlag]{}, err
		}
		cursorValues = cur.Values
	}

	params := query.ListParams{
		Limit:        pageReq.Limit,
		CursorValues: cursorValues,
		Sort:         flagSort,
	}

	flags, err := s.repo.List(ctx, params)
	if err != nil {
		return query.PageResult[*domain.FeatureFlag]{}, fmt.Errorf("feature-flag: list: %w", err)
	}

	return s.buildResult(flags, pageReq.Limit)
}

func (s *Service) buildResult(items []*domain.FeatureFlag, limit int) (query.PageResult[*domain.FeatureFlag], error) {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	var result query.PageResult[*domain.FeatureFlag]
	result.Items = items
	result.HasMore = hasMore

	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cur := query.Cursor{Values: []any{
			last.Key,
			last.ID,
		}}
		token, err := s.codec.Encode(cur)
		if err != nil {
			return query.PageResult[*domain.FeatureFlag]{}, err
		}
		result.NextCursor = token
	}

	if result.Items == nil {
		result.Items = []*domain.FeatureFlag{}
	}

	return result, nil
}

// Evaluate checks if a flag is enabled for the given subject.
func (s *Service) Evaluate(ctx context.Context, key, subject string) (*EvaluateResult, error) {
	if key == "" {
		return nil, errcode.New(errcode.ErrFlagInvalidInput, "key is required")
	}
	if subject == "" {
		return nil, errcode.New(errcode.ErrFlagInvalidInput, "subject is required")
	}

	flag, err := s.repo.GetByKey(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("feature-flag: evaluate: %w", err)
	}

	return &EvaluateResult{
		Key:     key,
		Enabled: flag.Evaluate(subject),
	}, nil
}
