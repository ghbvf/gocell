// Package auditappend implements the audit-append slice: consumes events from
// 6 topics and appends them to the hash chain.
package auditappend

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/google/uuid"
)

const (
	TopicAuditAppended = "event.audit.appended.v1"
)

// Topics lists the event topics consumed by audit-append. The handler is
// payload-agnostic — it extracts actor_id when the payload carries user_id,
// otherwise falls back to "system", so adding a topic here is purely additive.
// Each topic must also list auditcore as a subscriber in its contract.yaml.
var Topics = []string{
	"event.user.created.v1",
	"event.user.locked.v1",
	"event.session.created.v1",
	"event.session.revoked.v1",
	"event.config.changed.v1",
	"event.config.rollback.v1",
	"event.role.assigned.v1",
	"event.role.revoked.v1",
}

// Option configures an audit-append Service.
type Option func(*Service)

// WithEmitter sets the event emitter.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = persistence.RunnerOrNoop(tx) }
}

// Service appends events to the hash chain and persists them.
type Service struct {
	mu       sync.Mutex
	repo     ports.AuditRepository
	chain    *domain.HashChain
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	logger   *slog.Logger
}

// NewService creates an audit-append Service.
func NewService(
	repo ports.AuditRepository,
	hmacKey []byte,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		repo:     repo,
		chain:    domain.NewHashChain(hmacKey),
		txRunner: persistence.NoopTxRunner{},
		emitter:  outbox.NewNoopEmitter(),
		logger:   logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// HandleEvent processes an incoming event by appending it to the hash chain.
//
// Consumer: cg-auditcore-audit-append
// Idempotency key: entry:{group}:{event-id}, TTL 24h
// ACK timing: after hash chain append + repo persist
// Retry: transient errors -> NACK+backoff / permanent errors -> dead letter
func (s *Service) HandleEvent(ctx context.Context, entry outbox.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Extract actor_id from payload if present. Legacy snake_case events
	// (session.created, user.created, etc.) carry user_id; new camelCase
	// events (role.assigned, role.revoked) carry userId — accept either so
	// actor attribution works uniformly across event schemas.
	var payload struct {
		UserIDSnake string `json:"user_id"`
		UserIDCamel string `json:"userId"`
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		s.logger.Warn("audit-append: failed to extract actor from payload",
			slog.Any("error", err),
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
	}

	actorID := payload.UserIDSnake
	if actorID == "" {
		actorID = payload.UserIDCamel
	}
	if actorID == "" {
		actorID = "system"
	}

	// Append to hash chain.
	auditEntry := s.chain.Append(entry.ID, entry.EventType, actorID, entry.Payload)
	auditEntry.ID = "audit" + "-" + uuid.NewString()

	// Publish audit.appended event.
	appendedPayload, err := json.Marshal(map[string]any{
		"audit_entry_id": auditEntry.ID,
		"event_type":     entry.EventType,
	})
	if err != nil {
		s.logger.Error("audit-append: failed to marshal appended event payload",
			slog.Any("error", err),
			slog.String("event_id", entry.ID))
		return err
	}

	// Persist + outbox write in a transaction for L2 atomicity.
	persistFn := s.buildPersistFn(auditEntry, appendedPayload)
	persistErr := s.runPersist(ctx, persistFn)
	if persistErr != nil {
		s.logger.Error("audit-append: failed to persist entry",
			slog.Any("error", persistErr), slog.String("event_id", entry.ID))
		return persistErr
	}

	s.logger.Info("audit entry appended",
		slog.String("entry_id", auditEntry.ID),
		slog.String("event_type", entry.EventType))
	return nil
}

// buildPersistFn returns a transaction function that persists the audit entry
// and writes the outbox event.
func (s *Service) buildPersistFn(auditEntry *domain.AuditEntry, appendedPayload []byte) func(context.Context) error {
	return func(txCtx context.Context) error {
		if err := s.repo.Append(txCtx, auditEntry); err != nil {
			return err
		}
		return s.emitter.Emit(txCtx, outbox.Entry{
			ID:        outbox.NewEntryID(),
			EventType: TopicAuditAppended,
			Payload:   appendedPayload,
		})
	}
}

func (s *Service) runPersist(ctx context.Context, fn func(context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}

// ChainLen returns the number of entries in the chain (for testing).
func (s *Service) ChainLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chain.Len()
}
