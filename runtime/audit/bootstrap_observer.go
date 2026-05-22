package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/ctxutil"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
)

// bootstrapAppendDetachedTimeout caps the audit-append write so a stalled
// PG ledger cannot block the bootstrap rate-limited endpoint indefinitely.
// 2s mirrors the single-statement INSERT budget under healthy PG; a longer
// budget shifts setup-endpoint P99 without recovering more writes (the
// connection or txn is already faulted by then).
//
// ref: hashicorp/vault vault/token_store.go::quitContext detached pattern.
const bootstrapAppendDetachedTimeout = 2 * time.Second

// NewBootstrapAuthFailObserver constructs the runtime/auth.BootstrapAuthFailObserver
// used by composition roots when wiring auth.NewBootstrapMiddleware. The
// returned closure double-writes every failure event:
//
//   - SRE/operations channel: a structured slog Error line carrying event,
//     reason and client_ip (from ctxkeys.RealIPFrom). The "event" attr
//     mirrors the pre-funnel ssobffBootstrapAuthFailLogger shape so SRE
//     alerting can pivot on either msg or attr (kept until SSOBFF migrates).
//   - Compliance channel: a tamper-evident hash-chain entry via
//     AppendBootstrapAuthFail (the downstream Hard half of
//     BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-DOWNSTREAM-HARD-01) under a detached
//     context (ctxutil.WithDetachedTimeout) — runtime/auth fires the observer
//     AFTER writing 401/429 so client disconnect would otherwise cancel
//     r.Context() and abort the ledger write mid-flight. Audit chain is
//     fail-closed: completion guaranteed up to bootstrapAppendDetachedTimeout
//     (2s), which is the setup-endpoint P99 floor when PG ledger is degraded.
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
		// Detached ctx: runtime/auth invokes the observer AFTER writing the
		// 401/429, so client disconnect cancels r.Context() and would abort
		// the ledger write mid-flight. Audit chain is fail-closed (compliance
		// over availability), so we inherit Values (trace/request id) but
		// break the cancel chain, capped by bootstrapAppendDetachedTimeout to
		// prevent unbounded waits on a stuck PG.
		appendCtx, cancel := ctxutil.WithDetachedTimeout(ctx, bootstrapAppendDetachedTimeout)
		defer cancel()
		if err := AppendBootstrapAuthFail(appendCtx, store, clk, reason, ip); err != nil {
			logger.ErrorContext(ctx, "bootstrap_audit_append_failed",
				slog.String("event", "bootstrap_audit_append_failed"),
				slog.String("reason", reason),
				slog.String("client_ip", ip),
				slog.Any("error", err))
		}
	}, nil
}
