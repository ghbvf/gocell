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
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit"
)

// bootstrapAuthFailedPayload mirrors the event.auth.bootstrap-failed.v1
// payload wire shape (cells-internal; not exported). Cell isolation requires
// auditcore to maintain its own typed view — the accesscore dto package is
// inaccessible to cells/auditcore by the IMPL-DECL-COVER-01 rule.
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
}

// NewService constructs a Service. clk is required; bootstrapStore is optional
// (nil → service runs in no-op mode: events are Requeued with an explanatory
// error, which is safe but retryable until the store is wired correctly in
// production). Production assemblies must always inject a real store via
// WithBootstrapStore; the nil path exists only so cell tests that do not need
// bootstrap-chain coverage can Init the cell without wiring the full chain.
//
// Deliberate deviation from REQUIRED-DEP-NIL-GUARD-01: bootstrapStore is
// intentionally optional here (nil = no-op mode, not a wiring error). The
// production nil-guard lives in cellmodules/auditcore, which always injects
// a real store before bootstrap.Run.
func NewService(clk clock.Clock, opts ...Option) (*Service, error) {
	clock.MustHaveClock(clk, "auditappendbootstrap.NewService")
	svc := &Service{
		clk:    clk,
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(svc)
	}
	if validation.IsNilInterface(svc.clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"auditappendbootstrap: clock required")
	}
	return svc, nil
}

// HandleEvent processes event.auth.bootstrap-failed.v1 by appending it to the
// bootstrap ledger via audit.AppendBootstrapAuthFail.
//
// Disposition table:
//   - bootstrapStore == nil (no-op mode): Requeue (until store is wired)
//   - Permanent unmarshal failure (non-JSON payload) → Reject (routes to DLX)
//   - Unknown/empty reason → Reject (schema violation, non-retryable)
//   - Transient AppendBootstrapAuthFail failure → Requeue (ConsumerBase retries)
//   - Success → Ack
//
// Consumer declaration: see package-level godoc.
func (s *Service) HandleEvent(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	// bootstrapStore is nil in demo/test mode (no bootstrap chain wired).
	// Requeue so events are not permanently lost while the store is absent;
	// production assemblies must always wire a real store.
	if s.bootstrapStore == nil {
		s.logger.WarnContext(ctx, "auditappendbootstrap: no bootstrap store wired; requeuing event",
			slog.String("event_id", entry.ID()))
		return outbox.Requeue(fmt.Errorf("auditappendbootstrap: bootstrapStore not configured"))
	}

	var payload bootstrapAuthFailedPayload
	if err := json.Unmarshal(entry.Payload(), &payload); err != nil {
		s.logger.ErrorContext(ctx, "auditappendbootstrap: unmarshal payload failed",
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
			slog.String("event_id", entry.ID()))
		return outbox.Reject(outbox.NewPermanentError(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"auditappendbootstrap: bootstrap-failed payload missing reason")))
	}

	if err := audit.AppendBootstrapAuthFail(ctx, s.bootstrapStore, s.clk, payload.Reason, payload.ClientIP); err != nil {
		s.logger.ErrorContext(ctx, "auditappendbootstrap: append failed",
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
