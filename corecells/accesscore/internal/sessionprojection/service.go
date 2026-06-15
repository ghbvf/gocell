// Package sessionprojection is the L3 CQRS projection core for the accesscore
// session_registry read model.
//
// It subscribes to event.session.created.v1 and builds a tenant-partitioned
// in-memory set of session IDs. The read-model exposes only the count of
// sessions per tenant — raw session IDs never leave this package to avoid
// leaking identity data over the wire.
//
// Tenant isolation: event.session.created.v1 carries no tenant in its payload;
// the tenant is stamped by the outbox framework in the envelope. The Coordinator
// calls entry.RestoreContext before invoking Apply, so tenant.FromContext(ctx)
// is authoritative inside HandleSessionCreated. A missing tenant is a permanent
// error (the producer side violates the contract), not a transient retry.
package sessionprojection

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	created "github.com/ghbvf/gocell/generated/contracts/event/session/created/v1"
)

// store holds the mutable session_registry read model.
// Each tenant maps to the set of session IDs observed via
// event.session.created.v1. Using a set (map[string]struct{}) makes
// apply idempotent on rebuild replay: writing the same sessionID twice is a
// no-op.
type store struct {
	mu       sync.RWMutex
	sessions map[string]map[string]struct{} // tenantID → set of sessionIDs
}

// Service is the sessionprojection L3 projection service.
type Service struct {
	store  *store
	logger *slog.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithLogger sets the structured logger. A nil logger silently uses slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// NewService creates a new sessionprojection Service.
func NewService(opts ...Option) (*Service, error) {
	s := &Service{
		store: &store{
			sessions: make(map[string]map[string]struct{}),
		},
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// HandleSessionCreated processes an event.session.created.v1 entry.
// Implements cell.ProjectionApply — returns error (not outbox.HandleResult).
//
// Tenant isolation: the framework Coordinator calls entry.RestoreContext(ctx)
// before invoking Apply, so tenant.FromContext(ctx) yields the producing
// principal's tenant. A missing tenant is treated as a permanent error
// (fail-closed) because session.created is always produced by an authenticated
// principal who must have a tenant scope.
//
// A bad/undecodable payload or empty sessionId returns outbox.NewPermanentError
// (routed to DLX by the Coordinator). Transient failures return a plain error
// (Coordinator requeues).
func (s *Service) HandleSessionCreated(ctx context.Context, entry projection.ProjectionEvent) error {
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		s.logger.Error("sessionprojection: missing tenant on session.created entry — permanent error",
			slog.String("entry_id", entry.EventID()),
			slog.Any("error", err))
		return outbox.NewPermanentError(fmt.Errorf(
			"sessionprojection: session.created requires tenant in ctx (entry %s): %w",
			entry.EventID(), err))
	}

	var payload created.Payload
	if err := json.Unmarshal(entry.Payload(), &payload); err != nil {
		s.logger.Error("sessionprojection: failed to decode session.created payload",
			slog.Any("error", err), slog.String("entry_id", entry.EventID()))
		return outbox.NewPermanentError(fmt.Errorf(
			"sessionprojection: decode session.created (entry %s): %w", entry.EventID(), err))
	}

	if payload.SessionID == "" {
		s.logger.Error("sessionprojection: session.created payload missing sessionId",
			slog.String("entry_id", entry.EventID()))
		return outbox.NewPermanentError(fmt.Errorf(
			"sessionprojection: session.created payload sessionId is empty (entry %s)", entry.EventID()))
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	tenantKey := string(tid)
	if _, ok := s.store.sessions[tenantKey]; !ok {
		s.store.sessions[tenantKey] = make(map[string]struct{})
	}
	s.store.sessions[tenantKey][payload.SessionID] = struct{}{}

	s.logger.Debug("sessionprojection: session.created applied",
		slog.String("session_id", payload.SessionID),
		slog.String("tenant_id", tenantKey))

	return nil
}

// ResetSessionRegistry implements cell.ProjectionResetHook for the
// session_registry projection. Clears the entire in-memory store so the
// Coordinator's Rebuild phase can reconstruct it from the full event stream.
func (s *Service) ResetSessionRegistry(_ context.Context) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.sessions = make(map[string]map[string]struct{})
	return nil
}

// Query returns the number of sessions registered for the given tenant.
// Only the count is exposed — raw session IDs are never returned to callers to
// avoid leaking session identity data over the wire.
func (s *Service) Query(_ context.Context, tenantID tenant.TenantID) int64 {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	return int64(len(s.store.sessions[string(tenantID)]))
}
