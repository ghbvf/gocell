package audit

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
)

// NewBootstrapAuthFailObserver constructs the runtime/auth.BootstrapAuthFailObserver
// used by composition roots when wiring auth.NewBootstrapMiddleware. The
// returned closure double-writes every failure event:
//
//   - SRE/operations channel: a structured slog Error line carrying event,
//     reason and client_ip (from ctxkeys.RealIPFrom). This preserves the
//     pre-funnel grep / alerting contract.
//   - Compliance channel: a tamper-evident hash-chain entry via
//     AppendBootstrapAuthFail (the downstream Hard half of
//     BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-DOWNSTREAM-HARD-01).
//
// When the audit Append fails, the observer logs a dedicated
// "bootstrap_audit_append_failed" line so operators can correlate the lost
// chain record with the original auth failure. The primary
// "bootstrap_auth_failed" line is always emitted first so SRE alerting keeps
// working even if the ledger backend is unavailable.
//
// All three dependencies are strong wiring; nil and typed-nil inputs are
// rejected at construction so misconfiguration fails fast at startup, not
// on the first 401/429.
func NewBootstrapAuthFailObserver(logger *slog.Logger, store ledger.Store, clk clock.Clock) (auth.BootstrapAuthFailObserver, error) {
	if logger == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: NewBootstrapAuthFailObserver requires non-nil logger")
	}
	if validation.IsNilInterface(store) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: NewBootstrapAuthFailObserver requires non-nil ledger.Store")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: NewBootstrapAuthFailObserver requires non-nil clock")
	}
	return func(ctx context.Context, reason string) {
		ip, _ := ctxkeys.RealIPFrom(ctx)
		logger.ErrorContext(ctx, "bootstrap_auth_failed",
			slog.String("event", "bootstrap_auth_failed"),
			slog.String("reason", reason),
			slog.String("client_ip", ip))
		if err := AppendBootstrapAuthFail(ctx, store, clk, reason, ip); err != nil {
			logger.ErrorContext(ctx, "bootstrap_audit_append_failed",
				slog.String("reason", reason),
				slog.Any("error", err))
		}
	}, nil
}
