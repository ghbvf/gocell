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
)

const (
	// ErrFlagInvalidInput indicates invalid input for flag operations.
	ErrFlagInvalidInput errcode.Code = "ERR_FLAG_INVALID_INPUT"
)

// EvaluateResult holds the result of a flag evaluation.
type EvaluateResult struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

// Service implements feature flag business logic.
type Service struct {
	repo   ports.FlagRepository
	logger *slog.Logger
}

// NewService creates a feature-flag Service.
func NewService(repo ports.FlagRepository, logger *slog.Logger) *Service {
	return &Service{
		repo:   repo,
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

// List returns all feature flags.
func (s *Service) List(ctx context.Context) ([]*domain.FeatureFlag, error) {
	flags, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("feature-flag: list: %w", err)
	}
	return flags, nil
}

// Evaluate checks if a flag is enabled for the given subject.
func (s *Service) Evaluate(ctx context.Context, key, subject string) (*EvaluateResult, error) {
	if key == "" {
		return nil, errcode.New(ErrFlagInvalidInput, "key is required")
	}
	if subject == "" {
		return nil, errcode.New(ErrFlagInvalidInput, "subject is required")
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
