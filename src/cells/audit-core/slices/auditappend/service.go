// Package auditappend implements the audit-append slice: consumes events from
// 6 topics and appends them to the hash chain.
package auditappend

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/id"
)

const (
	TopicAuditAppended = "event.audit.appended.v1"
)

// Topics lists the 6 event topics consumed by audit-append.
var Topics = []string{
	"event.user.created.v1",
	"event.user.locked.v1",
	"event.session.created.v1",
	"event.session.revoked.v1",
	"event.config.changed.v1",
	"event.config.rollback.v1",
}

// Service appends events to the hash chain and persists them.
type Service struct {
	mu        sync.Mutex
	repo      ports.AuditRepository
	chain     *domain.HashChain
	publisher outbox.Publisher
	logger    *slog.Logger
}

// NewService creates an audit-append Service.
func NewService(
	repo ports.AuditRepository,
	hmacKey []byte,
	pub outbox.Publisher,
	logger *slog.Logger,
) *Service {
	return &Service{
		repo:      repo,
		chain:     domain.NewHashChain(hmacKey),
		publisher: pub,
		logger:    logger,
	}
}

// HandleEvent processes an incoming event by appending it to the hash chain.
//
// Consumer: cg-audit-core-audit-append
// Idempotency key: entry:{group}:{event-id}, TTL 24h
// ACK timing: after hash chain append + repo persist
// Retry: transient errors -> NACK+backoff / permanent errors -> dead letter
func (s *Service) HandleEvent(ctx context.Context, entry outbox.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Extract actor_id from payload if present.
	var payload struct {
		UserID string `json:"user_id"`
	}
	_ = json.Unmarshal(entry.Payload, &payload)

	actorID := payload.UserID
	if actorID == "" {
		actorID = "system"
	}

	// Append to hash chain.
	auditEntry := s.chain.Append(entry.ID, entry.EventType, actorID, entry.Payload)
	auditEntry.ID = id.New("audit")

	// Persist.
	if err := s.repo.Append(ctx, auditEntry); err != nil {
		s.logger.Error("audit-append: failed to persist entry",
			slog.Any("error", err), slog.String("event_id", entry.ID))
		return err // transient, will be retried
	}

	// Publish audit.appended event.
	appendedPayload, _ := json.Marshal(map[string]any{
		"audit_entry_id": auditEntry.ID,
		"event_type":     entry.EventType,
	})
	if pubErr := s.publisher.Publish(ctx, TopicAuditAppended, appendedPayload); pubErr != nil {
		s.logger.Error("audit-append: failed to publish appended event",
			slog.Any("error", pubErr))
	}

	s.logger.Info("audit entry appended",
		slog.String("entry_id", auditEntry.ID),
		slog.String("event_type", entry.EventType))
	return nil
}

// ChainLen returns the number of entries in the chain (for testing).
func (s *Service) ChainLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chain.Len()
}
