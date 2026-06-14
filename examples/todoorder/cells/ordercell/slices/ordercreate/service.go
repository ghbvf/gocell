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
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	createv1 "github.com/ghbvf/gocell/generated/contracts/http/order/create/v1"
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

// WithEmitter sets the event emitter. Accepts outbox.CellEmitter (sealed
// marker); callers in _test.go may use outbox.WrapEmitterForCell(e).
func WithEmitter(e outbox.CellEmitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the CellTxManager for transactional guarantees.
// Callers obtain the sealed marker via persistence.WrapForCell from a
// composition root.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) { s.txRunner = tx }
}

// Service handles order creation business logic.
// Cell wiring injects either durable or demo defaults, but the service always
// runs through the same Emitter + TxRunner code path.
type Service struct {
	repo     domain.OrderRepository    `gocell:"required"`
	txRunner persistence.CellTxManager `gocell:"required" gocellErr:"ordercreate: TxRunner required"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	emitter  outbox.CellEmitter
	logger   *slog.Logger
	clock    clock.Clock
}

// NewService creates an order-create Service. Returns an error if any required
// dependency is nil (repo, txRunner).
// Callers (ordercell.Init) guarantee txRunner is non-nil via resolveOutboxDeps.
func NewService(clk clock.Clock, repo domain.OrderRepository, logger *slog.Logger, opts ...Option) (*Service, error) {
	clock.MustHaveClock(clk, "ordercreate.NewService")
	s := &Service{
		repo:    repo,
		emitter: outbox.DemoCellEmitter(),
		logger:  logger,
		clock:   clk,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Create creates a new order and publishes an outbox event.
// It implements createv1.Service.
func (s *Service) Create(ctx context.Context, req *createv1.Request) (createv1.CreateResponseObject, error) {
	order, err := s.createInternal(ctx, req.Item)
	if err != nil {
		return nil, err
	}
	return createv1.Create201JSONResponse(toCreateResponse(order)), nil
}

// createInternal is the business logic core: creates an order and writes
// an outbox entry atomically.
func (s *Service) createInternal(ctx context.Context, item string) (*domain.Order, error) {
	if item == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "item must not be empty")
	}

	order := &domain.Order{
		ID:        "ord" + "-" + uuid.NewString(),
		Item:      item,
		Status:    "pending",
		CreatedAt: s.clock.Now(),
	}

	entry, err := s.buildOrderCreatedEntry(ctx, order)
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
		slog.String("entry_id", entry.ID()),
		slog.String("topic", entry.RoutingTopic()),
	)
	return order, nil
}

func toCreateResponse(o *domain.Order) createv1.Response {
	return createv1.Response{
		Data: &createv1.ResponseData{
			ID:     o.ID,
			Item:   o.Item,
			Status: o.Status,
		},
	}
}

func (s *Service) buildOrderCreatedEntry(ctx context.Context, order *domain.Order) (outbox.Entry, error) {
	payload, err := json.Marshal(toOrderCreatedEvent(order))
	if err != nil {
		return outbox.Entry{}, fmt.Errorf("order-create: marshal event: %w", err)
	}
	// order.CreatedAt is the producer-domain event time: stamp it as both
	// OccurredAt (domain event time) and CreatedAt (outbox row time) so the
	// domain timestamp is preserved instead of the construction-time clock.Now().
	entry, err := outbox.NewEntry(s.clock, ctx, TopicOrderCreated, payload,
		outbox.WithAggregateID(order.ID),
		outbox.WithAggregateType("order"),
		outbox.WithTopic(TopicOrderCreated),
		outbox.WithOccurredAt(order.CreatedAt),
		outbox.WithCreatedAt(order.CreatedAt),
	)
	if err != nil {
		return outbox.Entry{}, fmt.Errorf("order-create: build outbox entry: %w", err)
	}
	return entry, nil
}
