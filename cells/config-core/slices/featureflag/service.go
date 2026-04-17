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
	{Name: "key", Direction: query.SortASC},
	{Name: "id", Direction: query.SortASC},
}

// EvaluateResult holds the result of a flag evaluation.
type EvaluateResult struct {
	Key     string
	Enabled bool
}

// Service implements feature flag business logic.
type Service struct {
	repo    ports.FlagRepository
	codec   *query.CursorCodec
	logger  *slog.Logger
	runMode query.RunMode
}

// NewService creates a feature-flag Service. runMode controls cursor
// fail-open vs fail-closed semantics; pass query.RunModeProd unless the
// assembly declares DurabilityDemo.
//
// codec must be non-nil — pagination cannot be served without a cursor codec.
// Passing nil is a caller programming error; NewService returns errcode.ErrCellMissingCodec
// so the cell Init() can propagate a structured error instead of a runtime panic.
func NewService(repo ports.FlagRepository, codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode) (*Service, error) {
	if codec == nil {
		return nil, errcode.New(errcode.ErrCellMissingCodec,
			"featureflag: cursor codec is required")
	}
	return &Service{
		repo:    repo,
		codec:   codec,
		logger:  logger,
		runMode: runMode,
	}, nil
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
	qctx := query.QueryContext("endpoint", "feature-flag")
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*domain.FeatureFlag]{
		Codec:    s.codec,
		Request:  pageReq,
		Sort:     flagSort,
		QueryCtx: qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*domain.FeatureFlag, error) {
			flags, err := s.repo.List(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("feature-flag: list: %w", err)
			}
			return flags, nil
		},
		Extract: func(f *domain.FeatureFlag) []any {
			return []any{f.Key, f.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "featureflag"),
		RunMode:     s.runMode,
	})
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
