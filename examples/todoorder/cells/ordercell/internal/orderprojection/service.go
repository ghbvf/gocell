// Package orderprojection is the shared projection core for the ordercell.
//
// This package is the L3 CQRS harness reference for the todoorder example. It
// demonstrates multi-stream fan-in within the framework's single-stream
// projection.Coordinator model (#1482): the status-summary read model is fed by
// TWO independent single-stream projections that the cell registers separately,
// composed at query time —
//
//   - order_status      consumes event.order-created.v1        → HandleOrderCreated
//     (owns the created sub-view; onReset = ResetOrderStatus)
//   - order_transition  consumes event.order-status-changed.v1 → HandleOrderStatusChanged
//     (owns the transition sub-view; onReset = ResetOrderTransition)
//
// Each projection owns a DISJOINT region of the store (created vs latest) and
// has its own checkpoint + onReset, so rebuilding one never clears the other's
// data. Query composes them: an order's current status is its latest transition
// if any, else its created status. The framework Coordinator guarantees
// exactly-once delivery via checkpoint+txRunner (no per-handler idempotency
// guard needed) and applies only each projection's own stream during a rebuild
// (per-spec replay filter, kernel/projection/rebuild.go), so the handlers need
// no defensive topic check.
//
// The readyz probe for each projection is registered by the framework
// Coordinator (via Coordinator.Probes() in bootstrap), not by this Service.
//
// Demo note: the read model is in-memory only (a single MemProjectionEventSource wired
// as both replay source and cursor); events are discarded by the NoopWriter, so live
// consumption is best-effort (in-process bus). The harness cold-starts and the Coordinator
// registers its readyz probe automatically. The durable PG-backed source
// (PGProjectionEventSource) is delivered and gated in corebundle (EPIC #1504); todoorder's
// own durable wiring is tracked in backlog.
package orderprojection

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
	orderstatuschanged "github.com/ghbvf/gocell/generated/contracts/event/order-status-changed/v1"
)

// maxOrderIDsPerStatus bounds the per-status orderIds slice returned by Query
// so the summary stays within the project list-response cap (limit ≤ 500,
// .claude/rules/gocell/go-standards.md). Count carries the true total, so a
// truncated bucket is observable (Count > len(OrderIDs)) rather than silent.
const maxOrderIDsPerStatus = 500

// StatusBucket groups the order IDs for one status value. OrderIDs is a bounded
// sample (≤ maxOrderIDsPerStatus); Count is the unbounded true total.
type StatusBucket struct {
	Status   string
	Count    int64
	OrderIDs []string
}

// Summary is a point-in-time snapshot of the projection.
// The harness owns the stream offsets; the service no longer tracks sequence numbers.
type Summary struct {
	Statuses    []StatusBucket
	TotalOrders int64
}

// store is the mutable projection state. It holds two DISJOINT sub-views, each
// owned by exactly one single-stream projection so their rebuild Reset phases
// never collide:
//
//   - created: orderID → initial status (owned by order_status / order-created)
//   - latest:  orderID → newest transitioned status (owned by order_transition /
//     order-status-changed)
//
// Query composes them (latest wins over created). All mutations hold mu.Lock();
// reads that need a consistent snapshot hold mu.RLock().
type store struct {
	mu      sync.RWMutex
	created map[string]string // orderID → initial status (order-created)
	latest  map[string]string // orderID → newest status (order-status-changed)
}

// applyCreated projects an order-created event into the created sub-view.
// Must be called with mu held (write lock).
func (s *store) applyCreated(orderID, status string) {
	s.created[orderID] = status
}

// applyStatusChanged projects an order-status-changed event into the latest
// sub-view. Must be called with mu held (write lock). The stream is delivered
// in order (SerialInOrderGuarantor), so the last write wins = current status.
func (s *store) applyStatusChanged(orderID, newStatus string) {
	s.latest[orderID] = newStatus
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
			created: make(map[string]string),
			latest:  make(map[string]string),
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
// Implements cell.ProjectionApply — returns error (not outbox.HandleResult).
// The Coordinator guarantees exactly-once delivery via checkpoint position before
// calling apply, and the per-spec replay filter ensures this handler only ever
// sees order-created entries (live delivery is topic-routed; rebuild is
// topic-filtered), so no idempotency or topic guard is needed here.
//
// A bad/undecodable payload returns outbox.NewPermanentError — the Coordinator
// classifies this as DispositionReject and routes to DLX. Transient failures
// return a plain error (Coordinator requeues).
func (s *Service) HandleOrderCreated(ctx context.Context, entry projection.ProjectionEvent) error {
	var payload ordercreated.Payload
	if err := json.Unmarshal(entry.Payload(), &payload); err != nil {
		s.logger.Error("orderprojection: failed to decode order-created payload",
			slog.Any("error", err), slog.String("entry_id", entry.EventID()))
		return outbox.NewPermanentError(fmt.Errorf("orderprojection: decode order-created: %w", err))
	}

	// Defensive schema-required field validation: id and status are required by the
	// order-created.v1 payload schema and are the fields consumed by this projection.
	// A missing field is a permanent producer-side violation.
	if payload.ID == "" {
		s.logger.Error("orderprojection: order-created payload missing id",
			slog.String("entry_id", entry.EventID()))
		return outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-created payload id is empty (entry %s)", entry.EventID()))
	}
	if payload.Status == "" {
		s.logger.Error("orderprojection: order-created payload missing status",
			slog.String("order_id", payload.ID), slog.String("entry_id", entry.EventID()))
		return outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-created payload status is empty (entry %s)", entry.EventID()))
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.applyCreated(payload.ID, payload.Status)

	s.logger.Debug("orderprojection: order-created applied",
		slog.String("order_id", payload.ID),
		slog.String("status", payload.Status))

	return nil
}

// HandleOrderStatusChanged processes an event.order-status-changed.v1 entry.
// Implements cell.ProjectionApply for the order_transition projection. It records
// the order's newest status into the latest sub-view; Query composes it over the
// created sub-view (latest wins). Same exactly-once + per-spec delivery contract
// as HandleOrderCreated.
func (s *Service) HandleOrderStatusChanged(ctx context.Context, entry projection.ProjectionEvent) error {
	var payload orderstatuschanged.Payload
	if err := json.Unmarshal(entry.Payload(), &payload); err != nil {
		s.logger.Error("orderprojection: failed to decode order-status-changed payload",
			slog.Any("error", err), slog.String("entry_id", entry.EventID()))
		return outbox.NewPermanentError(fmt.Errorf("orderprojection: decode order-status-changed: %w", err))
	}

	// Defensive schema-required field validation: id and newStatus are required by
	// the order-status-changed.v1 payload schema and are the fields consumed by this
	// projection. A missing field is a permanent producer-side violation.
	// Note: oldStatus is schema-required but deliberately NOT validated or consumed
	// here — only newStatus feeds the latest sub-view, so a missing oldStatus does
	// not corrupt the projection.
	if payload.ID == "" {
		s.logger.Error("orderprojection: order-status-changed payload missing id",
			slog.String("entry_id", entry.EventID()))
		return outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-status-changed payload id is empty (entry %s)", entry.EventID()))
	}
	if payload.NewStatus == "" {
		s.logger.Error("orderprojection: order-status-changed payload missing newStatus",
			slog.String("order_id", payload.ID), slog.String("entry_id", entry.EventID()))
		return outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-status-changed payload newStatus is empty (entry %s)", entry.EventID()))
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.applyStatusChanged(payload.ID, payload.NewStatus)

	s.logger.Debug("orderprojection: order-status-changed applied",
		slog.String("order_id", payload.ID),
		slog.String("old_status", payload.OldStatus),
		slog.String("new_status", payload.NewStatus))

	return nil
}

// ResetOrderStatus implements cell.ProjectionResetHook for the order_status
// projection. It clears ONLY the created sub-view so the order_status rebuild
// Replay phase can reconstruct it from order-created events without touching the
// order_transition projection's data.
func (s *Service) ResetOrderStatus(_ context.Context) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.created = make(map[string]string)
	return nil
}

// ResetOrderTransition implements cell.ProjectionResetHook for the order_transition
// projection. It clears ONLY the latest sub-view, symmetric to ResetOrderStatus.
func (s *Service) ResetOrderTransition(_ context.Context) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.latest = make(map[string]string)
	return nil
}

// Query returns a point-in-time snapshot of the current projection, composing
// the created and latest sub-views: an order's current status is its newest
// transition (latest) if one exists, else its created status. Statuses are
// sorted alphabetically and order IDs within each bucket are sorted, so a
// rebuild produces a byte-identical snapshot.
//
// TotalOrders counts the union created ∪ latest; under eventual consistency an
// order observed only via a transition (latest) before its creation (created)
// still contributes to TotalOrders — the two independent projections converge.
func (s *Service) Query(_ context.Context) Summary {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()

	// Compose: order universe = created ∪ latest (latest may transiently lead
	// created while the two independent projections converge). currentStatus =
	// latest[id] if present, else created[id].
	current := make(map[string]string, len(s.store.created))
	for id, st := range s.store.created {
		current[id] = st
	}
	for id, st := range s.store.latest {
		current[id] = st
	}

	byStatus := make(map[string][]string)
	for id, st := range current {
		byStatus[st] = append(byStatus[st], id)
	}

	names := make([]string, 0, len(byStatus))
	for name := range byStatus {
		names = append(names, name)
	}
	sort.Strings(names)

	statuses := make([]StatusBucket, 0, len(names))
	totalOrders := int64(0)
	for _, name := range names {
		ids := byStatus[name]
		if len(ids) == 0 {
			continue
		}
		cp := make([]string, len(ids))
		copy(cp, ids)
		// Sort order IDs within each bucket for byte-identical rebuild guarantee.
		sort.Strings(cp)
		// Bound the returned slice (Count keeps the true total). The full list
		// of orders is available via the orderquery endpoint, not this summary.
		if len(cp) > maxOrderIDsPerStatus {
			cp = cp[:maxOrderIDsPerStatus]
		}
		statuses = append(statuses, StatusBucket{
			Status:   name,
			Count:    int64(len(ids)),
			OrderIDs: cp,
		})
		totalOrders += int64(len(ids))
	}

	return Summary{
		Statuses:    statuses,
		TotalOrders: totalOrders,
	}
}
