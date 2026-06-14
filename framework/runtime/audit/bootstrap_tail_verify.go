package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// bootstrapTailVerifyStartupTimeout caps VerifyBootstrapTailOnStartup so a
// slow or hung store cannot stall k8s readiness indefinitely. Mirrors the
// 30s budget auditcore uses for its own chain (corecells/auditcore/cell.go
// tailVerifyStartupTimeout) so operators see consistent startup latency
// envelopes across both audit chains.
const bootstrapTailVerifyStartupTimeout = 30 * time.Second

// VerifyBootstrapTailOnStartup runs the strict tail-verify protocol against
// the bootstrap audit chain: reads the tail snapshot, then re-computes the
// HMAC for every entry [1, tail.SeqNo] and checks chain linkage.
//
// Called by cmd/corebundle.AuditCoreModule.Provide immediately after the
// bootstrap store is constructed. The bootstrap chain lives outside the
// auditcore cell (it is the only audit chain owned directly by the
// composition root), so its tail-verify cannot reuse corecells/auditcore's
// strictTailVerifyOnStartup helper. The two helpers share the same fail-
// closed posture: a corrupted or tampered bootstrap chain surfaces at
// process startup rather than at the first 401/429, blocking k8s readiness
// instead of silently appending to a forked chain.
//
// Empty chain (tail.SeqNo == 0) returns nil — no entries to verify.
//
// Scope note: this verify covers only the ctx-scoped chain (the "" system
// chain when unscoped at startup). Per-tenant relay sub-chains are verified
// on-demand during relay startup. Full admin enumeration of all per-tenant
// chains is tracked at gh #1755 (blocked by NOBYPASSRLS serving role).
//
// ref: corecells/auditcore/cell.go:strictTailVerifyOnStartup — auditcore-side
// counterpart for the relay-driven chain.
// ref: google/trillian log/sequencer.go IntegrateBatch — tree integrity must
// be verified before accepting new leaves (same fail-fast invariant).
func VerifyBootstrapTailOnStartup(ctx context.Context, store *BootstrapLedgerStore, logger *slog.Logger) error {
	if store == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: VerifyBootstrapTailOnStartup requires non-nil *BootstrapLedgerStore")
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithTimeout(ctx, bootstrapTailVerifyStartupTimeout)
	defer cancel()

	tail, err := store.Tail(ctx)
	if err != nil {
		return fmt.Errorf("audit: bootstrap chain tail recovery failed: %w", err)
	}
	if tail.SeqNo == 0 {
		logger.Info("audit: bootstrap chain tail verify passed (empty chain)")
		return nil
	}
	valid, firstInvalid, err := store.Verify(ctx, 1, tail.SeqNo)
	if err != nil {
		return fmt.Errorf("audit: bootstrap chain tail verify failed: %w", err)
	}
	if !valid {
		return errcode.New(errcode.KindInternal, errcode.ErrAuditChainBroken,
			"audit: bootstrap chain integrity broken on startup",
			errcode.WithDetails(errcode.PublicInt("first_invalid_seq", firstInvalid)))
	}
	logger.Info("audit: bootstrap chain tail verify passed",
		slog.Int64("seq_no", tail.SeqNo))
	return nil
}
