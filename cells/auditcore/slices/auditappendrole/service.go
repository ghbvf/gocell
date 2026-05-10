// Package auditappendrole implements the audit-append-role slice:
// consumes role assignment events and appends them to the ledger store.
//
// Subscribed topics: event.role.assigned.v1, event.role.revoked.v1.
package auditappendrole

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/auditcore/internal/dto"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

const auditEntryIDPrefix = "audit-"

// Option configures a Service.
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
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// Service appends role assignment events to the ledger store.
type Service struct {
	store    ledger.Store
	protocol *ledger.Protocol
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	logger   *slog.Logger
	clk      clock.Clock
}

// NewService creates an auditappendrole Service.
func NewService(
	store ledger.Store,
	protocol *ledger.Protocol,
	logger *slog.Logger,
	clk clock.Clock,
	opts ...Option,
) (*Service, error) {
	clock.MustHaveClock(clk, "auditappendrole.NewService")
	s := &Service{
		store:    store,
		protocol: protocol,
		emitter:  outbox.NewNoopEmitter(),
		logger:   logger,
		clk:      clk,
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"auditappendrole: TxRunner required; use WithTxManager")
	}
	return s, nil
}

// HandleEvent processes a role assignment event by appending it to the ledger.
//
// Consumer: cg-auditcore-role-append
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h.
// Disposition: Ack on success / Requeue on transient / Reject on permanent.
// DLX: broker-native via DispositionReject → Nack(requeue=false).
func (s *Service) HandleEvent(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	if !json.Valid(entry.Payload) {
		s.logger.Warn("auditappend-role: invalid JSON payload",
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"auditappend-role: invalid JSON payload")))
	}

	actorID, ok := extractActorID(entry.Payload)
	if !ok {
		s.logger.Warn("auditappend-role: actor missing — rejecting event",
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"auditappend-role: event payload missing required actor identity")))
	}

	e := &ledger.Entry{
		ID:        auditEntryIDPrefix + uuid.NewString(),
		EventID:   entry.ID,
		EventType: entry.EventType,
		ActorID:   actorID,
		Timestamp: s.clk.Now(),
		Payload:   entry.Payload,
	}

	appendedEvent := dto.AuditAppendedEvent{
		AuditEntryID: e.ID,
		EventType:    entry.EventType,
	}

	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.store.Append(txCtx, e); err != nil {
			return err
		}
		return outbox.Emit(txCtx, s.emitter, dto.TopicAuditAppended, appendedEvent)
	}); err != nil {
		s.logger.Error("auditappend-role: failed to persist entry",
			slog.Any("error", err),
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
		return outbox.Requeue(err)
	}

	s.logger.Info("audit entry appended",
		slog.String("entry_id", e.ID),
		slog.String("event_type", entry.EventType),
		slog.String("actor_id", e.ActorID))
	return outbox.Ack()
}

// extractActorID extracts the actor identity from a role event payload.
// Role events require an explicit actorId (the admin/operator who performed the
// action). userId in role events identifies the target, not the actor.
// B2-C-05 fail-closed: missing actorId → Reject(PermanentError).
func extractActorID(payload []byte) (string, bool) {
	var p struct {
		ActorID string `json:"actorId"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", false
	}
	if p.ActorID != "" {
		return p.ActorID, true
	}
	return "", false
}
