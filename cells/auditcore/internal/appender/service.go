package appender

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/auditcore/internal/dto"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

const auditEntryIDPrefix = "audit-"

// Option configures a Service. Builder-style noop on nil — final validation
// happens in NewService (see runtime-api.md "Option 范式分层" — accumulative
// builder option category, fail-fast on nil at factory time).
type Option func(*Service)

// WithEmitter sets the outbox emitter used to publish event.audit.appended.v1
// after each successful Append. Nil is a silent noop; the default
// outbox.NewNoopEmitter() stays in place.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager wires the CellTxManager that brackets store.Append +
// outbox.Emit in one transaction (L2 OutboxFact pattern). NewService fails
// fast when no CellTxManager is wired (OUTBOX-SERVICE-01). Callers obtain
// the sealed marker via persistence.WrapForCell from a composition root.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// Service is the single-source audit-append behavior shared by all four
// auditappend{user,config,session,role} slice packages via type alias.
type Service struct {
	spec     Spec
	store    ledger.Store
	protocol *ledger.Protocol
	txRunner persistence.CellTxManager
	emitter  outbox.Emitter
	logger   *slog.Logger
	clk      clock.Clock
}

// NewService constructs an audit-append service for the slice identified by
// spec. The slice's actor-extraction strategy and log/error prefix are
// derived from spec; all other behavior (hash chain append + transactional
// outbox emit) is shared.
//
// OUTBOX-SERVICE-01: TxRunner must be supplied via WithTxManager —
// constructor returns ErrValidationFailed otherwise. The error message
// includes spec.Name() so callers can identify which slice mis-wired.
func NewService(
	spec Spec,
	store ledger.Store,
	protocol *ledger.Protocol,
	logger *slog.Logger,
	clk clock.Clock,
	opts ...Option,
) (*Service, error) {
	clock.MustHaveClock(clk, fmt.Sprintf("%s.NewService", spec.name))
	s := &Service{
		spec:     spec,
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
			"auditappender: TxRunner required; use WithTxManager",
			errcode.WithDetails(slog.String("slice", spec.name)))
	}
	return s, nil
}

// HandleEvent processes one source event by appending it to the ledger and
// publishing event.audit.appended.v1 inside the same transaction (L2
// OutboxFact pattern).
//
// Consumer: cg-auditcore-{slice}-append (slice from spec.Name()).
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h.
// Disposition: Ack on success / Requeue on transient / Reject on permanent.
// DLX: broker-native via DispositionReject → Nack(requeue=false).
func (s *Service) HandleEvent(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	logPrefix := slicePrefix(s.spec.name) // e.g. "auditappend-user"

	if !json.Valid(entry.Payload) {
		s.logger.Warn(logPrefix+": invalid JSON payload",
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"auditappender: invalid JSON payload",
				errcode.WithDetails(slog.String("slice", s.spec.name)))))
	}

	actorID, ok := extractActor(entry.Payload, s.spec.mode)
	if !ok {
		s.logger.Warn(logPrefix+": actor missing — rejecting event",
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"auditappender: event payload missing required actor identity",
				errcode.WithDetails(slog.String("slice", s.spec.name)))))
	}

	e := &ledger.Entry{
		ID:        auditEntryIDPrefix + uuid.NewString(),
		EventID:   entry.ID,
		EventType: entry.EventType,
		ActorID:   actorID,
		Timestamp: tsForLedger(entry, s.clk, s.logger, s.spec.name),
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
		s.logger.Error(logPrefix+": failed to persist entry",
			slog.Any("error", err),
			slog.String("event_id", entry.ID),
			slog.String("event_type", entry.EventType))
		// Disposition 收口 (ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01):
		// adapter classifiers now mark retry-safe failures via
		// errcode.WrapInfra. A positively-transient error Requeues. A
		// positively-permanent error (domain/validation/auth — classified,
		// not infra) short-circuits to Reject → DLX instead of burning the
		// whole retry budget. Unknown/ambiguous infra errors stay on the
		// Requeue (retry-then-budget-DLX) path — fail-closed toward not
		// losing an event on a transient blip. Mirrors the configreceive
		// positive-permanent precedent.
		if !errcode.IsTransient(err) && !errcode.IsInfraError(err) {
			return outbox.Reject(outbox.NewPermanentError(err))
		}
		return outbox.Requeue(err)
	}

	s.logger.Info("audit entry appended",
		slog.String("entry_id", e.ID),
		slog.String("event_type", entry.EventType),
		slog.String("actor_id", e.ActorID))
	return outbox.Ack()
}

// tsForLedger picks the audit entry timestamp source. Prefers outbox.Entry.CreatedAt
// (original event production time) over clock.Now() (consume time) so the audit ledger
// reflects when the business event happened, not when this consumer processed it.
// Zero CreatedAt is defensive fallback — outbox publishers always populate it, but
// we guard against unintended zero-value injection.
func tsForLedger(entry outbox.Entry, clk clock.Clock, logger *slog.Logger, slice string) time.Time {
	if entry.CreatedAt.IsZero() {
		logger.Warn("audit append: outbox.Entry.CreatedAt is zero — falling back to clk.Now()",
			slog.String("slice", slice), slog.String("event_id", entry.ID))
		return clk.Now()
	}
	return entry.CreatedAt
}

// slicePrefix turns "auditappenduser" into "auditappend-user". The kebab
// form preserves the log/error prefix the four predecessor service.go's
// used (auditappend-user / auditappend-config / auditappend-session /
// auditappend-role), keeping operator dashboards and grep patterns stable.
func slicePrefix(name string) string {
	const prefix = "auditappend"
	if len(name) > len(prefix) {
		return prefix + "-" + name[len(prefix):]
	}
	return name
}
