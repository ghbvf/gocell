// Package auditappend implements the audit-append slice: consumes events and
// appends them to the hash chain.
package auditappend

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/dto"
	"github.com/ghbvf/gocell/cells/auditcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Topics lists the event topics consumed by audit-append. The handler is
// payload-agnostic — it extracts userId when the payload carries one,
// otherwise falls back to "system", so adding a topic here is purely additive.
// Each topic must also list auditcore as a subscriber in its contract.yaml.
var Topics = []string{
	"event.user.created.v1",
	"event.user.locked.v1",
	"event.user.updated.v1",
	"event.user.deleted.v1",
	"event.user.unlocked.v1",
	"event.session.created.v1",
	"event.session.revoked.v1",
	"event.config.entry-upserted.v1",
	"event.config.entry-deleted.v1",
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
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// WithClock sets the clock used for audit entry timestamps.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) {
		if clk != nil {
			s.clock = clk
		}
	}
}

// Service appends events to the hash chain and persists them.
type Service struct {
	mu       sync.Mutex
	repo     ports.AuditRepository
	chain    *domain.HashChain
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	logger   *slog.Logger
	clock    clock.Clock
}

// NewService creates an audit-append Service. Returns an error if txRunner is nil.
// TxRunner must be provided via WithTxManager; nil txRunner is rejected to
// prevent silent loss of L2 atomicity guarantees.
func NewService(
	repo ports.AuditRepository,
	hmacKey []byte,
	logger *slog.Logger,
	clk clock.Clock,
	opts ...Option,
) (*Service, error) {
	clock.MustHaveClock(clk, "auditappend.NewService")
	// Pass the domain error through unchanged — cell.initSlices owns the
	// "auditappend: %w" wrapping for slice ownership; double-wrapping here
	// would render "auditappend: auditappend: …".
	chain, err := domain.NewHashChain(hmacKey)
	if err != nil {
		return nil, err
	}
	s := &Service{
		repo:    repo,
		chain:   chain,
		emitter: outbox.NewNoopEmitter(),
		logger:  logger,
		clock:   clk,
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"auditappend: TxRunner required; use WithTxManager (demo callers must inject an explicit pass-through TxRunner)")
	}
	return s, nil
}

// HandleEvent processes an incoming event by appending it to the hash chain.
//
// Consumer: cg-auditcore-audit-append
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h.
// Disposition: Ack on success / Requeue on transient / Reject on permanent.
// DLX: broker-native via DispositionReject → Nack(requeue=false).
func (s *Service) HandleEvent(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Reject invalid JSON payloads immediately — an unparseable payload can
	// never be audited correctly and retrying will not fix it. Route to DLX
	// via DispositionReject so operators can inspect the dead letter.
	if !json.Valid(entry.Payload) {
		s.logger.Warn("audit-append: invalid JSON payload",
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
		return outbox.Reject(outbox.NewPermanentError(errors.New("audit-append: invalid JSON payload")))
	}

	// Extract actorId from payload. PR-CFG-G1 G.2 made actorId required for all
	// admin-write events (config + user.{deleted,updated,unlocked,locked,created}).
	// session.* events use userId (no actorId — system action attributed to the
	// session owner). Producer-side decoders (configcore/internal/events,
	// accesscore/internal/dto) reject empty actorId, so reaching the "system"
	// fallback here means the producer bypassed validation — record at Error
	// level so data-quality dashboards surface the regression.
	var actorID string
	{
		var payload struct {
			ActorID string `json:"actorId"`
			UserID  string `json:"userId"`
		}
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			s.logger.Warn("audit-append: failed to extract actor from payload",
				slog.Any("error", err),
				slog.String("event_id", entry.ID),
				slog.String("event_type", entry.EventType))
		} else {
			actorID = payload.ActorID
			if actorID == "" {
				actorID = payload.UserID
			}
		}
	}
	if actorID == "" {
		s.logger.Error("audit-append: actor extraction fell back to \"system\" — "+
			"event payload contained neither actorId nor userId; producer-side "+
			"validation regression suspected",
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
		// Fail-safe fallback — see ADR Q5 (at-least-once audit > actor traceability).
		actorID = "system"
	}

	// Append to hash chain.
	auditEntry := s.chain.Append(entry.ID, entry.EventType, actorID, entry.Payload, s.clock.Now())
	auditEntry.ID = "audit" + "-" + uuid.NewString()

	appendedEvent := dto.AuditAppendedEvent{
		AuditEntryID: auditEntry.ID,
		EventType:    entry.EventType,
	}

	// Persist + outbox write in a transaction for L2 atomicity.
	persistFn := s.buildPersistFn(auditEntry, appendedEvent)
	if persistErr := s.runPersist(ctx, persistFn); persistErr != nil {
		s.logger.Error("audit-append: failed to persist entry",
			slog.Any("error", persistErr),
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
		// Transient failure — ConsumerBase will back-off and retry.
		return outbox.Requeue(persistErr)
	}

	s.logger.Info("audit entry appended",
		slog.String("entry_id", auditEntry.ID),
		slog.String("event_type", entry.EventType))
	return outbox.Ack()
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
