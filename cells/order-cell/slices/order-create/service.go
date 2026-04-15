// Package ordercreate implements the order-create slice: creating orders
// and publishing order.created events via the transactional outbox pattern.
//
// Demo mode injects NoopWriter + NoopTxRunner to exercise the same code
// path as production (zero fork). ref: Watermill GoChannel / Uber fx pattern.
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

// orderCreatedEvent is the event payload DTO for order creation events,
// decoupled from the domain model.
type orderCreatedEvent struct {
	ID        string    `json:"id"`
	Item      string    `json:"item"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

func toOrderCreatedEvent(o *domain.Order) orderCreatedEvent {
	return orderCreatedEvent{
		ID: o.ID, Item: o.Item, Status: o.Status, CreatedAt: o.CreatedAt,
	}
}

// Option configures the order-create Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for event emission.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees.
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// Service handles order creation business logic.
// Both outboxWriter and txRunner are required — the cell Init() enforces this.
// Demo mode uses outbox.NoopWriter + persistence.NoopTxRunner for a unified
// code path with zero fork.
type Service struct {
	repo         domain.OrderRepository
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
}

// NewService creates an order-create Service.
// outboxWriter and txRunner must be set via options; the cell Init()
// validates their presence before constructing the Service.
func NewService(repo domain.OrderRepository, logger *slog.Logger, opts ...Option) *Service {
	s := &Service{
		repo:   repo,
		logger: logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Create creates a new order and writes an outbox entry atomically.
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

	entry, err := s.buildOrderCreatedEntry(order)
	if err != nil {
		return nil, err
	}

	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
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

func (s *Service) buildOrderCreatedEntry(order *domain.Order) (outbox.Entry, error) {
	payload, err := json.Marshal(toOrderCreatedEvent(order))
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
