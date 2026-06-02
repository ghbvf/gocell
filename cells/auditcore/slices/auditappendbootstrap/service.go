// Package auditappendbootstrap is the audit-append-bootstrap slice: it consumes
// event.auth.bootstrap-failed.v1 events and appends them to the bootstrap
// audit ledger via runtime/audit.AppendBootstrapAuthFail.
//
// The bootstrap audit chain is physically isolated from the relay-chain
// (different HMAC key and namespace) so a single compromised chain cannot
// contaminate the other (issue #1121 / ADR 202605270230).
//
// Consumer: cg-auditcore-auditappendbootstrap (auditcore consumerGroup)
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h
// Disposition: Ack on success / Requeue on transient / Reject on permanent
// DLX: broker-native via DispositionReject → Nack(requeue=false)
package auditappendbootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/audit"
)

// bootstrapAuthFailedPayload mirrors the event.auth.bootstrap-failed.v1
// payload wire shape (cells-internal; not exported). Cell isolation requires
// auditcore to maintain its own typed view — the accesscore dto package is
// inaccessible to cells/auditcore by the IMPL-DECL-COVER-01 rule.
//
// Schema: contracts/event/auth/bootstrap-failed/v1/payload.schema.json.
type bootstrapAuthFailedPayload struct {
	Reason   string `json:"reason"`
	ClientIP string `json:"clientIp,omitempty"`
}

// Option configures a Service.
type Option func(*Service)

// WithBootstrapStore injects the sealed *audit.BootstrapLedgerStore.
// Bare-nil inputs are silently ignored (builder-option semantics); the final
// nil validation is in NewService.
func WithBootstrapStore(s *audit.BootstrapLedgerStore) Option {
	return func(svc *Service) {
		if s != nil {
			svc.bootstrapStore = s
		}
	}
}

// Service implements the auditappendbootstrap slice.
type Service struct {
	bootstrapStore *audit.BootstrapLedgerStore
	clk            clock.Clock
	logger         *slog.Logger
	// nilStoreOnce ensures the "no bootstrap store" warning is emitted at most
	// once across all events (F15: avoid per-event warn flood).
	nilStoreOnce sync.Once
}

// NewService constructs a Service. clk is required; bootstrapStore is optional
// (nil → service runs in no-op mode: events are permanently rejected with an
// explanatory error, which routes them to DLX. This is the correct behavior
// because nil store is a permanent misconfiguration, not a transient error —
// retrying would burn the budget to no effect). Production assemblies must
// always inject a real store via WithBootstrapStore; the nil path exists only
// so cell tests that do not need bootstrap-chain coverage can Init the cell
// without wiring the full chain.
//
// Deliberate deviation from REQUIRED-DEP-NIL-GUARD-01: bootstrapStore is
// intentionally optional here (nil = permanent misconfiguration detected at
// event-handle time, not at construction time). The startup fail-fast guard
// for durable assemblies lives in cells/auditcore/cell.go::initSlices, which
// rejects a nil store in DurabilityDurable mode with
// errcode.ErrCellMissingBootstrapStore.
func NewService(clk clock.Clock, opts ...Option) (*Service, error) {
	clock.MustHaveClock(clk, "auditappendbootstrap.NewService")
	svc := &Service{
		clk:    clk,
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(svc)
	}
	// F27: validation.IsNilInterface(svc.clk) is dead code here — clock.MustHaveClock
	// panics on nil above, and there is no WithClock option that could re-set it to nil.
	// Removed to reduce noise.
	return svc, nil
}

// HandleEvent processes event.auth.bootstrap-failed.v1 by appending it to the
// bootstrap ledger via audit.AppendBootstrapAuthFail.
//
// Disposition table:
//   - bootstrapStore == nil (permanent misconfiguration): Reject → DLX (C4)
//   - Permanent unmarshal failure (non-JSON payload) → Reject (routes to DLX)
//   - Unknown/empty reason → Reject (schema violation, non-retryable)
//   - Transient AppendBootstrapAuthFail failure → Requeue (ConsumerBase retries)
//   - Success → Ack
//
// Consumer declaration: see package-level godoc.
func (s *Service) HandleEvent(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	// bootstrapStore is nil in demo/test mode (no bootstrap chain wired).
	// This is a permanent misconfiguration: retrying wastes the retry budget.
	// Reject → DLX so the operator sees the misconfiguration immediately.
	// nilStoreOnce ensures the warning is emitted at most once, not per-event.
	if s.bootstrapStore == nil {
		s.nilStoreOnce.Do(func() {
			s.logger.WarnContext(ctx, "auditappendbootstrap: no bootstrap store wired; rejecting events (permanent misconfiguration)",
				slog.String("namespace", "bootstrap"),
				slog.String("event_id", entry.ID()))
		})
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"auditappendbootstrap: bootstrapStore not configured (permanent misconfiguration)")))
	}

	var payload bootstrapAuthFailedPayload
	if err := json.Unmarshal(entry.Payload(), &payload); err != nil {
		s.logger.ErrorContext(ctx, "auditappendbootstrap: unmarshal payload failed",
			slog.String("namespace", "bootstrap"),
			slog.String("event_id", entry.ID()),
			slog.Int("payload_len", len(entry.Payload())),
			slog.Any("error", err))
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"auditappendbootstrap: unmarshal bootstrap-failed payload",
				errcode.WithInternal(errcode.InternalAttr("event_id", entry.ID())))))
	}
	if payload.Reason == "" {
		s.logger.ErrorContext(ctx, "auditappendbootstrap: empty reason in payload",
			slog.String("namespace", "bootstrap"),
			slog.String("event_id", entry.ID()))
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"auditappendbootstrap: bootstrap-failed payload missing reason")))
	}

	if err := audit.AppendBootstrapAuthFail(ctx, s.bootstrapStore, s.clk, entry.ID(), payload.Reason, payload.ClientIP); err != nil {
		// Idempotent replay: the ledger already holds this entry (same stable
		// EventID = entry.ID(), keyed by IdempotencyContentFingerprint), e.g.
		// outbox redelivery. ErrAuditLedgerAlreadyExists (KindConflict) is an
		// idempotent SUCCESS — the entry is committed; Ack (not Reject/DLX,
		// even though KindConflict is a 4xx) and log at Info. This branch must
		// precede the IsExpected4xx check, which would otherwise route the
		// 409 to DLX. Mirrors cells/auditcore/internal/appender.
		var dup *errcode.Error
		if errors.As(err, &dup) && dup.Code == errcode.ErrAuditLedgerAlreadyExists {
			s.logger.InfoContext(ctx, "auditappendbootstrap: entry already appended (idempotent replay)",
				slog.String("namespace", "bootstrap"),
				slog.String("event_id", entry.ID()),
				slog.String("reason", payload.Reason))
			return outbox.Ack()
		}
		s.logger.ErrorContext(ctx, "auditappendbootstrap: append failed",
			slog.String("namespace", "bootstrap"),
			slog.String("event_id", entry.ID()),
			slog.String("reason", payload.Reason),
			slog.Any("error", err))
		// ErrValidationFailed (unknown reason, nil store/clock) is a permanent
		// schema violation — the payload cannot be corrected by retrying.
		// Any other error (ledger write failure, infra) is treated as transient.
		if errcode.IsExpected4xx(err) {
			return outbox.Reject(outbox.NewPermanentError(
				fmt.Errorf("auditappendbootstrap: append permanent: %w", err)))
		}
		return outbox.Requeue(fmt.Errorf("auditappendbootstrap: append: %w", err))
	}
	return outbox.Ack()
}
