// Package auditappend implements the audit-append slice: consumes events from
// 6 topics and appends them to the hash chain.
package auditappend

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/dto"
	"github.com/ghbvf/gocell/cells/auditcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/google/uuid"
)

// Topics lists the event topics consumed by audit-append. The handler is
// payload-agnostic — it extracts userId when the payload carries one,
// otherwise falls back to "system", so adding a topic here is purely additive.
// Each topic must also list auditcore as a subscriber in its contract.yaml.
var Topics = []string{
	"event.user.created.v1",
	"event.user.locked.v1",
	"event.session.created.v1",
	"event.session.revoked.v1",
	"event.config.entry-written.v1",
	"event.config.version-published.v1",
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

	// Extract userId from payload when present. PR-A6 migrated session/config
	// events to camelCase `userId`; event.user.created.v1 / event.user.locked.v1
	// still publish snake_case `user_id` (out of PR-A6 scope — trailing sweep).
	// Accept either key so actor attribution stays correct across the mix; once
	// user events also migrate, the snake_case alias can be dropped.
	var payload struct {
		UserIDCamel string `json:"userId"`
		UserIDSnake string `json:"user_id"`
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		s.logger.Warn("audit-append: failed to extract actor from payload",
			slog.Any("error", err),
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
	}

	actorID := payload.UserIDCamel
	if actorID == "" {
		actorID = payload.UserIDSnake
	}
	if actorID == "" {
		actorID = "system"
	}

	// Append to hash chain.
	auditEntry := s.chain.Append(entry.ID, entry.EventType, actorID, entry.Payload)
	auditEntry.ID = "audit" + "-" + uuid.NewString()

	appendedEvent := dto.AuditAppendedEvent{
		AuditEntryID: auditEntry.ID,
		EventType:    entry.EventType,
	}

	// Persist + outbox write in a transaction for L2 atomicity.
	persistFn := s.buildPersistFn(auditEntry, appendedEvent)
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
func (s *Service) buildPersistFn(auditEntry *domain.AuditEntry, event dto.AuditAppendedEvent) func(context.Context) error {
	return func(txCtx context.Context) error {
		if err := s.repo.Append(txCtx, auditEntry); err != nil {
			return err
		}
		return outbox.Emit(txCtx, s.emitter, dto.TopicAuditAppended, event)
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
