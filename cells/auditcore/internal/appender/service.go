package appender

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/auditcore/internal/dto"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/correlation"
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
// outbox.DemoCellEmitter() stays in place.
func WithEmitter(e outbox.CellEmitter) Option {
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
	txRunner persistence.CellTxManager `gocell:"required" gocellErr:"auditappender: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	emitter  outbox.CellEmitter
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
		emitter:  outbox.DemoCellEmitter(),
		logger:   logger,
		clk:      clk,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
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

	if !json.Valid(entry.Payload()) {
		s.logger.Warn(logPrefix+": invalid JSON payload",
			slog.String("event_id", entry.ID()),
			slog.String("event_type", entry.EventType()))
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"auditappender: invalid JSON payload",
				errcode.WithDetails(errcode.PublicString("slice", s.spec.name))),
		))
	}

	// actor_id is the audited action's actor as declared by the producer in the
	// event payload (extractActor). This is the authoritative domain source and
	// works even when there is no authenticated request principal in ctx — e.g.
	// session.created emitted DURING login, before any auth context exists.
	// The Principal family below (subject/tenant/session/correlation) is the
	// orthogonal request-context carried on the outbox envelope; it is additive
	// (empty when the producing action had no authenticated principal) and does
	// NOT replace the domain actor. Distinct columns, distinct authoritative
	// sources — not a dual-source for one field. (issue #1229 B8)
	actorID, ok := extractActor(entry.Payload(), s.spec.mode)
	if !ok {
		s.logger.Warn(logPrefix+": actor missing — rejecting event",
			slog.String("event_id", entry.ID()),
			slog.String("event_type", entry.EventType()))
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"auditappender: event payload missing required actor identity",
				errcode.WithDetails(errcode.PublicString("slice", s.spec.name))),
		))
	}

	principal := entry.Principal()
	s.tripSingleTenantInvariant(logPrefix, entry, principal)

	corr := correlation.FromObservability(entry.Observability())
	e := &ledger.Entry{
		ID:            auditEntryIDPrefix + uuid.NewString(),
		EventID:       entry.ID(),
		EventType:     entry.EventType(),
		ActorID:       actorID,
		SubjectID:     string(principal.SubjectID),
		TenantID:      string(principal.TenantID),
		SessionID:     string(principal.SessionID),
		CorrelationID: corr.CorrelationID(),
		TraceID:       corr.TraceID(),
		OccurredAt:    entry.OccurredAt(),
		Timestamp:     tsForLedger(entry, s.clk, s.logger, s.spec.name),
		Payload:       entry.Payload(),
	}

	appendedEvent := dto.AuditAppendedEvent{
		AuditEntryID: e.ID,
		EventType:    entry.EventType(),
	}

	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.store.Append(txCtx, e); err != nil {
			return err
		}
		return outbox.Emit(txCtx, s.clk, s.emitter, dto.TopicAuditAppended, appendedEvent)
	}); err != nil {
		// Idempotent replay: the ledger already holds this entry (same
		// content/EventID fingerprint), e.g. outbox redelivery or a parallel
		// consumer-group. errcode.ErrAuditLedgerAlreadyExists is an idempotent
		// SUCCESS — the entry is committed; the contract (pkg/errcode godoc)
		// is "treat as already committed and not retry". Ack (not Requeue,
		// not DLX) and log at Info, before the error-disposition path.
		var dup *errcode.Error
		if errors.As(err, &dup) && dup.Code == errcode.ErrAuditLedgerAlreadyExists {
			s.logger.Info(logPrefix+": entry already appended (idempotent replay)",
				slog.String("event_id", entry.ID()),
				slog.String("event_type", entry.EventType()))
			return outbox.Ack()
		}
		s.logger.Error(logPrefix+": failed to persist entry",
			slog.Any("error", err),
			slog.String("event_id", entry.ID()),
			slog.String("event_type", entry.EventType()))
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
		slog.String("event_type", entry.EventType()),
		slog.String("actor_id", e.ActorID))
	return outbox.Ack()
}

// tripSingleTenantInvariant is the INV-SINGLE-TENANT-ONLY regression tripwire
// (#1289, epic #1296).
//
// This is an OPERATIONAL tripwire, NOT a tenant-isolation security boundary.
// As of the multi-tenancy epic PR-1 (#1339), the auth bridge DOES write
// principal.TenantID from the JWT "tenant_id" claim (CTXKEYS-PRINCIPAL-WRITE-CALLER-01
// now allowlists runtime/auth/middleware.go as a WithTenantID producer), so this
// branch is no longer structurally dead: any audit-producing request carrying a
// valid tenant claim reaches here with a non-empty TenantID. That is the intended
// alarm for the PR-1→PR-2 window — multi-tenant identity now flows, but
// tenant-scoped audit query filtering has NOT yet landed (PR-2), which is a
// security-relevant gap. Trip loudly (Error, alertable) but DO NOT drop the
// audit record: compliance evidence is never discarded (the caller continues to
// Append). PR-2 removes this tripwire and replaces it with tenant-scoped query
// filtering. It guards the persistence-reach path; CTXKEYS-PRINCIPAL-WRITE-CALLER-01
// orthogonally guards the ctx-write path.
//
// Firing cadence: logs once PER event with a non-empty tenant. That is
// intentional for the PR-1→PR-2 window — on a default deployment the gocell
// issuer emits no tenant_id claim, so the branch stays cold; any firing means a
// tenant-bearing token is in use before tenant-scoped audit filtering exists, a
// real gap worth a per-event alert. tenant_id is an opaque, non-credential
// identifier (observability.md), so logging its value server-side is safe and
// aids #1296 debugging.
func (s *Service) tripSingleTenantInvariant(logPrefix string, entry outbox.Entry, principal outbox.PrincipalMetadata) {
	if principal.TenantID == "" {
		return
	}
	s.logger.Error(logPrefix+": INV-SINGLE-TENANT-ONLY violated — non-empty principal.TenantID "+
		"with no tenant-scoped audit query enforcement (epic #1296)",
		slog.String("event_id", entry.ID()),
		slog.String("event_type", entry.EventType()),
		slog.String("tenant_id", string(principal.TenantID)))
}

// tsForLedger picks the audit entry Timestamp (ledger persistence / HMAC time)
// source. It prefers outbox.Entry.CreatedAt (the outbox seal time) over
// clock.Now() (consume time) so the audit row's time anchor reflects when the
// business event was sealed, not when this consumer processed it. The domain
// event time is carried separately in ledger.Entry.OccurredAt (= entry.OccurredAt()).
// Since NewEntry always stamps createdAt, the zero-value branch is defensive and
// should not fire in practice.
func tsForLedger(entry outbox.Entry, clk clock.Clock, logger *slog.Logger, slice string) time.Time {
	if entry.CreatedAt().IsZero() {
		logger.Warn("audit append: outbox.Entry.CreatedAt is zero — falling back to clk.Now()",
			slog.String("slice", slice), slog.String("event_id", entry.ID()))
		return clk.Now()
	}
	return entry.CreatedAt()
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
