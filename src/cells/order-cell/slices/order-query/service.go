// Package orderquery implements the order-query slice: reading orders.
package orderquery

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
)

// Service handles order query business logic.
type Service struct {
	repo   domain.OrderRepository
	logger *slog.Logger
}

// NewService creates an order-query Service.
func NewService(repo domain.OrderRepository, logger *slog.Logger) *Service {
	return &Service{
		repo:   repo,
		logger: logger,
	}
}

// GetByID returns a single order by ID.
func (s *Service) GetByID(ctx context.Context, id string) (*domain.Order, error) {
	return s.repo.GetByID(ctx, id)
}

// List returns all orders.
func (s *Service) List(ctx context.Context) ([]*domain.Order, error) {
	return s.repo.List(ctx)
}
