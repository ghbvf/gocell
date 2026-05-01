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

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
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

// WithEmitter sets the event emitter.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the TxRunner for transactional guarantees.
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = persistence.RunnerOrNoop(tx) }
}

// Service handles order creation business logic.
// Cell wiring injects either durable or demo defaults, but the service always
// runs through the same Emitter + TxRunner code path.
type Service struct {
	repo     domain.OrderRepository
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	logger   *slog.Logger
}

// NewService creates an order-create Service.
func NewService(repo domain.OrderRepository, logger *slog.Logger, opts ...Option) *Service {
	s := &Service{
		repo:     repo,
		txRunner: persistence.NoopTxRunner{},
		emitter:  outbox.NewNoopEmitter(),
		logger:   logger,
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
		if err := s.emitter.Emit(txCtx, entry); err != nil {
			return fmt.Errorf("order-create: emit event: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("order-create: event emitted",
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
		ID:            outbox.MustNewEntryID(),
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
