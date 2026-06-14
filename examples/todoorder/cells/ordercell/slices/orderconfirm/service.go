// Package orderconfirm implements the order-confirm slice (L2): PATCH order status
// to confirmed, publishing an order-status-changed event via the transactional outbox.
//
// Demo mode injects NoopWriter + a pass-through TxRunner to exercise the same
// code path as production (zero fork). ref: Watermill GoChannel / Uber fx pattern.
package orderconfirm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	confirmv1 "github.com/ghbvf/gocell/generated/contracts/http/order/confirm/v1"
)

// Compile-time assertion: Service implements the generated interface.
var _ confirmv1.Service = (*Service)(nil)

// TopicOrderStatusChanged is the canonical event topic for order status change events.
const TopicOrderStatusChanged = "event.order-status-changed.v1"

// orderStatusChangedEvent is the event payload DTO for order status change events,
// decoupled from the domain model.
type orderStatusChangedEvent struct {
	ID        string    `json:"id"`
	OldStatus string    `json:"oldStatus"`
	NewStatus string    `json:"newStatus"`
	ChangedAt time.Time `json:"changedAt,omitempty"`
}

func toStatusChangedEvent(id, oldStatus, newStatus string, changedAt time.Time) orderStatusChangedEvent {
	return orderStatusChangedEvent{
		ID:        id,
		OldStatus: oldStatus,
		NewStatus: newStatus,
		ChangedAt: changedAt,
	}
}

// Option configures the order-confirm Service.
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

// Service handles order confirm business logic.
// Cell wiring injects either durable or demo defaults, but the service always
// runs through the same Emitter + TxRunner code path.
type Service struct {
	repo     domain.OrderRepository    `gocell:"required"`
	txRunner persistence.CellTxManager `gocell:"required" gocellErr:"orderconfirm: TxRunner required"`               //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	clk      clock.Clock               `gocell:"required" gocellErr:"orderconfirm.NewService: clock.Clock required"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	emitter  outbox.CellEmitter
	logger   *slog.Logger
}

// NewService creates an order-confirm Service. Returns an error if any required
// dependency is nil (repo, txRunner).
func NewService(clk clock.Clock, repo domain.OrderRepository, logger *slog.Logger, opts ...Option) (*Service, error) {
	clock.MustHaveClock(clk, "orderconfirm.NewService")
	s := &Service{
		repo:    repo,
		clk:     clk,
		emitter: outbox.DemoCellEmitter(),
		logger:  logger,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Confirm transitions an order to confirmed status and publishes an outbox event.
// It implements confirmv1.Service.
//
// Business 4xx paths return typed structs (not errors):
//   - 400 if status != "confirmed"
//   - 404 if order not found
//   - 409 if order is not in pending state
func (s *Service) Confirm(ctx context.Context, req *confirmv1.Request) (confirmv1.ConfirmResponseObject, error) {
	// Validate requested status value
	if req.Status != domain.StatusConfirmed {
		return confirmv1.Confirm400ErrorResponse{
			Body: *errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"order-confirm: status must be \"confirmed\"",
				errcode.WithDetails(errcode.PublicString("status", req.Status))),
		}, nil
	}

	// Load order
	order, err := s.repo.GetByID(ctx, req.ID)
	if err != nil {
		var ecErr *errcode.Error
		if errors.As(err, &ecErr) && ecErr.Kind == errcode.KindNotFound {
			return confirmv1.Confirm404ErrorResponse{
				Body: *errcode.New(errcode.KindNotFound, errcode.ErrOrderNotFound,
					"order-confirm: order not found",
					errcode.WithDetails(errcode.PublicString("orderId", req.ID))),
			}, nil
		}
		return nil, fmt.Errorf("order-confirm: get order: %w", err)
	}

	// Capture old status before domain transition
	oldStatus := order.Status

	// Apply domain transition — returns conflict if not pending (fast-path 409).
	if err := order.Confirm(); err != nil {
		//nolint:nilerr // typed-envelope: business 409 returned as typed response struct, not via error (cell-patterns)
		return s.confirm409(req.ID, oldStatus), nil
	}

	// Build outbox entry before tx (marshal errors should not roll back)
	changedAt := time.Now().UTC()
	entry, err := s.buildStatusChangedEntry(ctx, order.ID, oldStatus, order.Status, changedAt)
	if err != nil {
		return nil, err
	}

	// Atomic: persist status update (CAS: only if still pending) + emit outbox entry.
	// The CAS expectedStatus guard catches concurrent Confirm calls that both observed
	// pending before either committed: the second caller gets KindConflict from the
	// repo, RunInTx rolls back, and Confirm returns a 409 — no duplicate event emitted.
	casConflict, txErr := s.runConfirmTx(ctx, order.ID, entry)
	if txErr != nil {
		if casConflict {
			return s.confirm409(req.ID, oldStatus), nil
		}
		return nil, txErr
	}

	s.logger.Info("order-confirm: event emitted",
		slog.String("order_id", order.ID),
		slog.String("entry_id", entry.ID()),
		slog.String("topic", entry.RoutingTopic()),
		slog.String("old_status", oldStatus),
		slog.String("new_status", order.Status),
	)

	return confirmv1.Confirm200JSONResponse(confirmv1.Response{
		Data: &confirmv1.ResponseData{
			ID:     order.ID,
			Item:   order.Item,
			Status: order.Status,
		},
	}), nil
}

// runConfirmTx executes the transactional CAS update + outbox emit.
// Returns (casConflict=true, err) when the repo signals KindConflict (concurrent
// re-entry). Returns (false, err) for any other infrastructure error.
// Returns (false, nil) on success.
func (s *Service) runConfirmTx(ctx context.Context, orderID string, entry outbox.Entry) (bool, error) {
	var casConflict bool
	err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.UpdateStatus(txCtx, orderID, domain.StatusPending, domain.StatusConfirmed); err != nil {
			var ecErr *errcode.Error
			if errors.As(err, &ecErr) && ecErr.Kind == errcode.KindConflict {
				casConflict = true
				return err // trigger tx rollback
			}
			return fmt.Errorf("order-confirm: update status: %w", err)
		}
		if err := s.emitter.Emit(txCtx, entry); err != nil {
			return fmt.Errorf("order-confirm: emit event: %w", err)
		}
		return nil
	})
	return casConflict, err
}

// confirm409 constructs the typed 409 response for both the fast-path (domain
// Confirm() rejected) and the CAS-conflict path (concurrent re-entry).
func (s *Service) confirm409(orderID, currentStatus string) confirmv1.Confirm409ErrorResponse {
	return confirmv1.Confirm409ErrorResponse{
		Body: *errcode.New(errcode.KindConflict, errcode.ErrConflict,
			"order-confirm: order cannot be confirmed",
			errcode.WithDetails(errcode.PublicString("orderId", orderID), errcode.PublicString("currentStatus", currentStatus))),
	}
}

func (s *Service) buildStatusChangedEntry(ctx context.Context, id, oldStatus, newStatus string, changedAt time.Time) (outbox.Entry, error) {
	payload, err := json.Marshal(toStatusChangedEvent(id, oldStatus, newStatus, changedAt))
	if err != nil {
		return outbox.Entry{}, fmt.Errorf("order-confirm: marshal event: %w", err)
	}
	// changedAt is the producer-domain event time: stamp it as both OccurredAt
	// (domain event time) and CreatedAt (outbox row time) so the domain timestamp
	// is preserved instead of the construction-time clock.Now().
	entry, err := outbox.NewEntry(s.clk, ctx, TopicOrderStatusChanged, payload,
		outbox.WithAggregateID(id),
		outbox.WithAggregateType("order"),
		outbox.WithTopic(TopicOrderStatusChanged),
		outbox.WithOccurredAt(changedAt),
		outbox.WithCreatedAt(changedAt),
	)
	if err != nil {
		return outbox.Entry{}, fmt.Errorf("order-confirm: build outbox entry: %w", err)
	}
	return entry, nil
}
