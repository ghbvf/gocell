// Package orderprojection is the shared projection core for the ordercell.
// Both the public orderprojection slice and the internal orderprojectionrebuild
// slice import this package to access the same in-memory projection store.
//
// This package follows the FMT-33 internal-shared-package pattern (see
// cells/configcore/internal/configreader for the canonical prior art): the
// two slices that split a public/internal HTTP surface share their domain
// logic here, and each slice exposes a thin type alias.
//
// The Service is the orderprojection L3 WorkflowEventual projection service.
// It subscribes to order-created and order-status-changed events, maintains
// an in-memory "by-status" read model (OrderStatusSummary), and supports a
// business-level rebuild from the event log.
//
// # Demo note
//
// The event log is unbounded: each handled event appends one projectedEvent
// entry. In production one would snapshot + truncate or replay from a durable
// outbox. This unbounded log is an accepted demo limitation.
package orderprojection

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
	orderstatuschanged "github.com/ghbvf/gocell/generated/contracts/event/order-status-changed/v1"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// projectedEvent is a compacted log record kept in-memory. It does not carry
// timestamps — the Summary has no time dimension in this demo.
type projectedEvent struct {
	seq       int64
	kind      string // "created" or "status-changed"
	orderID   string
	oldStatus string // non-empty for status-changed
	newStatus string
}

// StatusBucket groups the order IDs for one status value.
type StatusBucket struct {
	Status   string
	Count    int64
	OrderIDs []string
}

// Summary is a point-in-time snapshot of the projection.
type Summary struct {
	Statuses       []StatusBucket
	TotalOrders    int64
	LastAppliedSeq int64
}

// RebuildReport describes the outcome of a full replay from the event log.
type RebuildReport struct {
	EventsReplayed  int
	StatusesRebuilt int
	LastAppliedSeq  int64
}

// store is the mutable projection state. All mutations hold mu.Lock();
// reads that need a consistent snapshot hold mu.RLock().
type store struct {
	mu       sync.RWMutex
	byStatus map[string][]string // status → ordered list of orderIDs
	orderAt  map[string]string   // orderID → current status
	log      []projectedEvent    // append-only event log for replay
	nextSeq  int64               // monotonically increasing sequence counter
}

// applyCreated projects an order-created event into the store.
// Must be called with mu held (write lock).
func (s *store) applyCreated(orderID, status string) {
	s.byStatus[status] = append(s.byStatus[status], orderID)
	s.orderAt[orderID] = status
}

// applyStatusChanged projects an order-status-changed event into the store.
// Must be called with mu held (write lock).
func (s *store) applyStatusChanged(orderID, oldStatus, newStatus string) {
	if cur, ok := s.orderAt[orderID]; ok && cur != newStatus {
		// remove from old bucket
		bucket := s.byStatus[cur]
		for i, id := range bucket {
			if id == orderID {
				s.byStatus[cur] = append(bucket[:i], bucket[i+1:]...)
				break
			}
		}
		if len(s.byStatus[cur]) == 0 {
			delete(s.byStatus, cur)
		}
	} else if !ok {
		// order not yet seen (out-of-order): place directly in newStatus bucket
		_ = oldStatus // convergent: ignore oldStatus when order unknown
	}
	// Only add to newStatus if not already there
	if s.orderAt[orderID] != newStatus {
		s.byStatus[newStatus] = append(s.byStatus[newStatus], orderID)
		s.orderAt[orderID] = newStatus
	}
}

// Service is the orderprojection L3 projection service.
type Service struct {
	store  *store
	logger *slog.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithLogger sets the logger. A nil logger silently uses slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// NewService creates a new orderprojection Service.
// Default logger is slog.Default(); use WithLogger to override.
func NewService(opts ...Option) (*Service, error) {
	s := &Service{
		store: &store{
			byStatus: make(map[string][]string),
			orderAt:  make(map[string]string),
		},
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// HandleOrderCreated processes an event.order-created.v1 entry.
//
// Consumer: cg-ordercell-order-created
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h
// Disposition: Ack on success / Requeue on transient / Reject on permanent
// DLX: broker-native via DispositionReject → Nack(requeue=false).
// Demo mode: in-process bus, no DLX exchange; production: set SubscriberConfig.DLXExchange.
//
// Schema-required field validation: after successful JSON decode, id and status
// are validated non-empty. A missing required field is a permanent schema violation
// (retrying cannot fix it), so the entry is Rejected to DLX.
func (s *Service) HandleOrderCreated(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	var payload ordercreated.Payload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		s.logger.Error("orderprojection: failed to decode order-created payload; routing to DLX",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf("orderprojection: decode order-created: %w", err)))
	}

	// Defensive schema-required field validation: id and status are required by
	// the order-created.v1 payload schema and are the fields consumed by this projection.
	// A missing field is a permanent producer-side violation; reject to DLX without retry.
	if payload.ID == "" {
		s.logger.Error("orderprojection: order-created payload missing id; routing to DLX",
			slog.String("entry_id", entry.ID))
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-created payload id is empty (entry %s)", entry.ID)))
	}
	if payload.Status == "" {
		s.logger.Error("orderprojection: order-created payload missing status; routing to DLX",
			slog.String("order_id", payload.ID), slog.String("entry_id", entry.ID))
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-created payload status is empty (entry %s)", entry.ID)))
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	// Idempotency guard: if we already know this order, no-op Ack.
	if _, exists := s.store.orderAt[payload.ID]; exists {
		s.logger.Debug("orderprojection: idempotent ack — already applied",
			slog.String("order_id", payload.ID),
			slog.String("entry_id", entry.ID))
		return outbox.Ack()
	}

	seq := s.store.nextSeq
	s.store.applyCreated(payload.ID, payload.Status)
	s.store.log = append(s.store.log, projectedEvent{
		seq:       seq,
		kind:      "created",
		orderID:   payload.ID,
		newStatus: payload.Status,
	})
	s.store.nextSeq++

	s.logger.Debug("orderprojection: order-created applied",
		slog.String("order_id", payload.ID),
		slog.String("status", payload.Status),
		slog.Int64("seq", seq))

	return outbox.Ack()
}

// HandleOrderStatusChanged processes an event.order-status-changed.v1 entry.
//
// Consumer: cg-ordercell-order-status-changed
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h
// Disposition: Ack on success / Requeue on transient / Reject on permanent
// DLX: broker-native via DispositionReject → Nack(requeue=false).
// Demo mode: in-process bus, no DLX exchange; production: set SubscriberConfig.DLXExchange.
//
// Schema-required field validation: after successful JSON decode, id, oldStatus, and
// newStatus are validated non-empty. A missing required field is a permanent schema
// violation (retrying cannot fix it), so the entry is Rejected to DLX.
func (s *Service) HandleOrderStatusChanged(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	var payload orderstatuschanged.Payload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		s.logger.Error("orderprojection: failed to decode order-status-changed payload; routing to DLX",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf("orderprojection: decode order-status-changed: %w", err)))
	}

	// Defensive schema-required field validation: id, oldStatus, and newStatus are
	// required by the order-status-changed.v1 payload schema and are the fields consumed
	// by this projection. A missing field is a permanent producer-side violation; reject
	// to DLX without retry.
	if payload.ID == "" {
		s.logger.Error("orderprojection: order-status-changed payload missing id; routing to DLX",
			slog.String("entry_id", entry.ID))
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-status-changed payload id is empty (entry %s)", entry.ID)))
	}
	if payload.OldStatus == "" {
		s.logger.Error("orderprojection: order-status-changed payload missing oldStatus; routing to DLX",
			slog.String("order_id", payload.ID), slog.String("entry_id", entry.ID))
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-status-changed payload oldStatus is empty (entry %s)", entry.ID)))
	}
	if payload.NewStatus == "" {
		s.logger.Error("orderprojection: order-status-changed payload missing newStatus; routing to DLX",
			slog.String("order_id", payload.ID), slog.String("entry_id", entry.ID))
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-status-changed payload newStatus is empty (entry %s)", entry.ID)))
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	// Idempotency guard: if this order is already at newStatus, no-op Ack.
	if cur, exists := s.store.orderAt[payload.ID]; exists && cur == payload.NewStatus {
		s.logger.Debug("orderprojection: idempotent ack — already applied",
			slog.String("order_id", payload.ID),
			slog.String("entry_id", entry.ID))
		return outbox.Ack()
	}

	seq := s.store.nextSeq
	s.store.applyStatusChanged(payload.ID, payload.OldStatus, payload.NewStatus)
	s.store.log = append(s.store.log, projectedEvent{
		seq:       seq,
		kind:      "status-changed",
		orderID:   payload.ID,
		oldStatus: payload.OldStatus,
		newStatus: payload.NewStatus,
	})
	s.store.nextSeq++

	s.logger.Debug("orderprojection: order-status-changed applied",
		slog.String("order_id", payload.ID),
		slog.String("old_status", payload.OldStatus),
		slog.String("new_status", payload.NewStatus),
		slog.Int64("seq", seq))

	return outbox.Ack()
}

// Query returns a point-in-time snapshot of the current projection.
// Statuses are sorted alphabetically by status name.
func (s *Service) Query(_ context.Context) Summary {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()

	// collect sorted status names
	names := make([]string, 0, len(s.store.byStatus))
	for name := range s.store.byStatus {
		names = append(names, name)
	}
	sort.Strings(names)

	statuses := make([]StatusBucket, 0, len(names))
	totalOrders := int64(0)
	for _, name := range names {
		ids := s.store.byStatus[name]
		if len(ids) == 0 {
			continue
		}
		cp := make([]string, len(ids))
		copy(cp, ids)
		// Sort order IDs within each bucket for byte-identical rebuild guarantee.
		sort.Strings(cp)
		statuses = append(statuses, StatusBucket{
			Status:   name,
			Count:    int64(len(ids)),
			OrderIDs: cp,
		})
		totalOrders += int64(len(ids))
	}

	lastSeq := int64(-1)
	if s.store.nextSeq > 0 {
		lastSeq = s.store.nextSeq - 1
	}

	return Summary{
		Statuses:       statuses,
		TotalOrders:    totalOrders,
		LastAppliedSeq: lastSeq,
	}
}

// Rebuild replays the event log from the beginning to reconstruct the
// projection from scratch. This demonstrates business-level CQRS replay
// without depending on the kernel.
//
// Returns the replay report: number of events replayed, number of distinct
// statuses rebuilt, and the last applied sequence number.
func (s *Service) Rebuild(_ context.Context) (RebuildReport, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	s.logger.Info("orderprojection: rebuild started",
		slog.Int("log_entries", len(s.store.log)))

	// capture log before clearing
	log := make([]projectedEvent, len(s.store.log))
	copy(log, s.store.log)

	// reset derived state (keep log + nextSeq unchanged)
	s.store.byStatus = make(map[string][]string)
	s.store.orderAt = make(map[string]string)

	// replay
	for _, ev := range log {
		switch ev.kind {
		case "created":
			s.store.applyCreated(ev.orderID, ev.newStatus)
		case "status-changed":
			s.store.applyStatusChanged(ev.orderID, ev.oldStatus, ev.newStatus)
		}
	}

	lastSeq := int64(-1)
	if len(log) > 0 {
		lastSeq = log[len(log)-1].seq
	}

	report := RebuildReport{
		EventsReplayed:  len(log),
		StatusesRebuilt: len(s.store.byStatus),
		LastAppliedSeq:  lastSeq,
	}

	s.logger.Info("orderprojection: rebuild completed",
		slog.Int("events_replayed", report.EventsReplayed),
		slog.Int64("last_seq", report.LastAppliedSeq))

	return report, nil
}
