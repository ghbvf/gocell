// Package configread implements the config-read slice: Get/List config entries.
package configread

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
)

// Service implements config read business logic.
type Service struct {
	repo   ports.ConfigRepository
	logger *slog.Logger
}

// NewService creates a config-read Service.
func NewService(repo ports.ConfigRepository, logger *slog.Logger) *Service {
	return &Service{
		repo:   repo,
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

// List returns all config entries.
func (s *Service) List(ctx context.Context) ([]*domain.ConfigEntry, error) {
	entries, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("config-read: list: %w", err)
	}
	return entries, nil
}
