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
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
)

// TopicOrderCreated is the canonical event topic for order creation events.
const TopicOrderCreated = "event.order-created.v1"

// Option configures the order-create Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for durable event emission.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees.
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// Service handles order creation business logic.
type Service struct {
	repo         domain.OrderRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
}

// NewService creates an order-create Service.
func NewService(repo domain.OrderRepository, publisher outbox.Publisher, logger *slog.Logger, opts ...Option) *Service {
	s := &Service{
		repo:      repo,
		publisher: publisher,
		logger:    logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Create creates a new order and publishes an order.created event.
func (s *Service) Create(ctx context.Context, item string) (*domain.Order, error) {
	if item == "" {
		return nil, errcode.New(errcode.ErrValidationFailed, "item must not be empty")
	}
	if (s.outboxWriter == nil) != (s.txRunner == nil) {
		return nil, errcode.New(errcode.ErrCellMissingOutbox,
			"order-create durable mode requires both outboxWriter and txRunner")
	}

	order := &domain.Order{
		ID:        "ord" + "-" + uuid.NewString(),
		Item:      item,
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	if s.outboxWriter != nil {
		return s.createDurable(ctx, order)
	}
	return s.createDemo(ctx, order)
}

// createDurable persists the order and writes an outbox entry atomically.
func (s *Service) createDurable(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	entry, err := s.buildOrderCreatedEntry(order)
	if err != nil {
		return nil, err
	}
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Create(txCtx, order); err != nil {
			return fmt.Errorf("order-create: persist: %w", err)
		}
		if err := s.outboxWriter.Write(txCtx, entry); err != nil {
			return fmt.Errorf("order-create: write outbox: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("order-create: outbox entry written",
		slog.String("order_id", order.ID),
		slog.String("entry_id", entry.ID),
		slog.String("topic", entry.RoutingTopic()),
	)
	return order, nil
}

// createDemo persists the order and publishes directly (best-effort, no transactional guarantee).
func (s *Service) createDemo(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	if err := s.repo.Create(ctx, order); err != nil {
		return nil, fmt.Errorf("order-create: persist: %w", err)
	}

	payload, err := json.Marshal(order)
	if err != nil {
		s.logger.Error("order-create: marshal event failed", slog.Any("error", err))
		return order, nil // order is created, event publish is best-effort
	}

	if s.publisher == nil {
		s.logger.Warn("order-create: publisher not configured, skipping direct publish",
			slog.String("order_id", order.ID),
		)
		return order, nil
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

func (s *Service) buildOrderCreatedEntry(order *domain.Order) (outbox.Entry, error) {
	payload, err := json.Marshal(order)
	if err != nil {
		return outbox.Entry{}, fmt.Errorf("order-create: marshal event: %w", err)
	}
	entry := outbox.Entry{
		ID:            "evt-" + uuid.NewString(),
		AggregateID:   order.ID,
		AggregateType: "order",
		EventType:     TopicOrderCreated,
		Topic:         TopicOrderCreated,
		Payload:       payload,
		CreatedAt:     order.CreatedAt,
	}
	if err := entry.Validate(); err != nil {
		return outbox.Entry{}, fmt.Errorf("order-create: invalid outbox entry: %w", err)
	}
	return entry, nil
}

func (s *Service) runInTx(ctx context.Context, fn func(context.Context) error) error {
	if s.txRunner != nil {
		return s.txRunner.RunInTx(ctx, fn)
	}
	return fn(ctx)
}
