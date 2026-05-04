// Package ordercreate implements the order-create slice: creating orders
// and publishing order.created events via the transactional outbox pattern.
//
// Demo mode injects NoopWriter + a pass-through TxRunner to exercise the same
// code path as production (zero fork). ref: Watermill GoChannel / Uber fx pattern.
package ordercreate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	createv1 "github.com/ghbvf/gocell/generated/contracts/http/order/create/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time assertion: Service implements the generated interface.
var _ createv1.Service = (*Service)(nil)

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
	return func(s *Service) { s.txRunner = tx }
}

// WithClock sets the clock used for order timestamps. Defaults to
// clock.Real() when not provided.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) {
		if clk != nil {
			s.clock = clk
		}
	}
}

// Service handles order creation business logic.
// Cell wiring injects either durable or demo defaults, but the service always
// runs through the same Emitter + TxRunner code path.
type Service struct {
	repo     domain.OrderRepository
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	logger   *slog.Logger
	clock    clock.Clock
}

// NewService creates an order-create Service. Returns an error if txRunner is nil.
// Callers (ordercell.Init) guarantee txRunner is non-nil via resolveOutboxDeps.
func NewService(repo domain.OrderRepository, logger *slog.Logger, opts ...Option) (*Service, error) {
	s := &Service{
		repo:    repo,
		emitter: outbox.NewNoopEmitter(),
		logger:  logger,
		clock:   clock.Real(),
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		return nil, errcode.New(errcode.ErrValidationFailed, "ordercreate: TxRunner required")
	}
	return s, nil
}

// Create creates a new order and publishes an outbox event.
// It implements createv1.Service.
func (s *Service) Create(ctx context.Context, req *createv1.Request) (*createv1.Response, error) {
	order, err := s.createInternal(ctx, req.Item)
	if err != nil {
		return nil, err
	}
	return toCreateResponse(order), nil
}

// createInternal is the business logic core: creates an order and writes
// an outbox entry atomically.
func (s *Service) createInternal(ctx context.Context, item string) (*domain.Order, error) {
	if item == "" {
		return nil, errcode.New(errcode.ErrValidationFailed, "item must not be empty")
	}

	order := &domain.Order{
		ID:        "ord" + "-" + uuid.NewString(),
		Item:      item,
		Status:    "pending",
		CreatedAt: s.clock.Now(),
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

func toCreateResponse(o *domain.Order) *createv1.Response {
	if o == nil {
		return nil
	}
	return &createv1.Response{
		Data: &createv1.ResponseData{
			ID:     o.ID,
			Item:   o.Item,
			Status: o.Status,
		},
	}
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
