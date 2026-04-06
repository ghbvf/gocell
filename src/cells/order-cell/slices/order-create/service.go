// Package ordercreate implements the order-create slice: creating orders
// and publishing order.created events.
package ordercreate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
)

// TopicOrderCreated is the canonical event topic for order creation events.
const TopicOrderCreated = "event.order-created.v1"

// Service handles order creation business logic.
type Service struct {
	repo      domain.OrderRepository
	publisher outbox.Publisher
	logger    *slog.Logger
}

// NewService creates an order-create Service.
func NewService(repo domain.OrderRepository, publisher outbox.Publisher, logger *slog.Logger) *Service {
	return &Service{
		repo:      repo,
		publisher: publisher,
		logger:    logger,
	}
}

// Create creates a new order and publishes an order.created event.
func (s *Service) Create(ctx context.Context, item string) (*domain.Order, error) {
	if item == "" {
		return nil, errcode.New(errcode.ErrValidationFailed, "item must not be empty")
	}

	order := &domain.Order{
		ID:        "ord" + "-" + uuid.NewString(),
		Item:      item,
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	if err := s.repo.Create(ctx, order); err != nil {
		return nil, fmt.Errorf("order-create: persist: %w", err)
	}

	// Publish event (best-effort in demo mode; production would use outbox writer).
	payload, err := json.Marshal(order)
	if err != nil {
		s.logger.Error("order-create: marshal event failed", slog.Any("error", err))
		return order, nil // order is created, event publish is best-effort
	}

	if err := s.publisher.Publish(ctx, TopicOrderCreated, payload); err != nil {
		s.logger.Error("order-create: publish event failed",
			slog.String("order_id", order.ID),
			slog.Any("error", err),
		)
	} else {
		s.logger.Info("order-create: event published",
			slog.String("order_id", order.ID),
			slog.String("topic", TopicOrderCreated),
		)
	}

	return order, nil
}
