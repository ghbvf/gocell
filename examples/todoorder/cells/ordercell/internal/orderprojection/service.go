// Package orderprojection is the shared projection core for the ordercell.
//
// This package is the L3 CQRS harness reference for the todoorder example.
// HandleOrderCreated implements cell.ProjectionApply — it is called by the
// framework projection.Coordinator, which guarantees exactly-once delivery via
// checkpoint+txRunner (no per-handler idempotency guard needed). ResetOrderStatus
// implements cell.ProjectionResetHook — it is called during a Coordinator.Rebuild
// Reset phase to clear the read model before replay.
//
// The readyz probe for this projection is registered by the framework
// Coordinator (via Coordinator.Probes() in bootstrap), not by this Service.
//
// Demo note: the read model is in-memory only; events are discarded by the
// NoopWriter, so live consumption is best-effort (in-process bus). The harness
// cold-starts and the Coordinator registers its readyz probe automatically.
// Faithful PG-backed replay is tracked in #1368 (production PG ReplaySource/Cursor)
// and #1370 (real-PG integration test). Status-transition projection (consuming
// order-status-changed events) is tracked in #1482.
package orderprojection

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
	"github.com/ghbvf/gocell/kernel/outbox"
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
// The harness owns the stream offset; the service no longer tracks sequence numbers.
type Summary struct {
	Statuses    []StatusBucket
	TotalOrders int64
}

// store is the mutable projection state. All mutations hold mu.Lock();
// reads that need a consistent snapshot hold mu.RLock().
type store struct {
	mu       sync.RWMutex
	byStatus map[string][]string // status → ordered list of orderIDs
	orderAt  map[string]string   // orderID → current status
}

// applyCreated projects an order-created event into the store.
// Must be called with mu held (write lock).
func (s *store) applyCreated(orderID, status string) {
	s.byStatus[status] = append(s.byStatus[status], orderID)
	s.orderAt[orderID] = status
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
// Implements cell.ProjectionApply — returns error (not outbox.HandleResult).
// The Coordinator guarantees exactly-once delivery via checkpoint position before
// calling apply, so no per-handler idempotency guard is needed here.
//
// A bad/undecodable payload returns outbox.NewPermanentError — the Coordinator
// classifies this as DispositionReject and routes to DLX. Transient failures
// return a plain error (Coordinator requeues).
func (s *Service) HandleOrderCreated(ctx context.Context, entry outbox.Entry) error {
	var payload ordercreated.Payload
	if err := json.Unmarshal(entry.Payload(), &payload); err != nil {
		s.logger.Error("orderprojection: failed to decode order-created payload",
			slog.Any("error", err), slog.String("entry_id", entry.ID()))
		return outbox.NewPermanentError(fmt.Errorf("orderprojection: decode order-created: %w", err))
	}

	// Defensive schema-required field validation: id and status are required by the
	// order-created.v1 payload schema and are the fields consumed by this projection.
	// A missing field is a permanent producer-side violation.
	if payload.ID == "" {
		s.logger.Error("orderprojection: order-created payload missing id",
			slog.String("entry_id", entry.ID()))
		return outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-created payload id is empty (entry %s)", entry.ID()))
	}
	if payload.Status == "" {
		s.logger.Error("orderprojection: order-created payload missing status",
			slog.String("order_id", payload.ID), slog.String("entry_id", entry.ID()))
		return outbox.NewPermanentError(fmt.Errorf(
			"orderprojection: order-created payload status is empty (entry %s)", entry.ID()))
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	// TODO(#1482): demonstrate status-transition aggregation (consume order-status-changed)
	// once the unified lifecycle stream / multi-stream harness lands.
	s.store.applyCreated(payload.ID, payload.Status)

	s.logger.Debug("orderprojection: order-created applied",
		slog.String("order_id", payload.ID),
		slog.String("status", payload.Status))

	return nil
}

// ResetOrderStatus implements cell.ProjectionResetHook. It clears the in-memory
// read model (byStatus + orderAt) so the Coordinator's rebuild Replay phase can
// reconstruct it from scratch.
func (s *Service) ResetOrderStatus(_ context.Context) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.byStatus = make(map[string][]string)
	s.store.orderAt = make(map[string]string)
	return nil
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
