package projection

import (
	"context"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// SystemPrincipalActor is the actor identity installed in a context by
// InstallSystemPrincipal. Saga journal replay events run under this sentinel so
// the business Apply function never sees an unauthenticated empty principal or a
// forwarded admin identity from the Rebuild trigger context.
//
// Value "system" is consistent with the framework-internal sentinel used
// elsewhere (e.g. reconcile worker identity) — a stable, recognisable string
// that audit log consumers can filter on.
const SystemPrincipalActor = "system"

// InstallSystemPrincipal overwrites ALL four principal ctx keys (actor/subject/
// tenant/session) unconditionally: it sets actor and subject to
// SystemPrincipalActor and clears tenant and session to the empty string.
//
// This is the OVERWRITE variant of principal restore — contrast with
// outbox.PrincipalMetadata.RestoreToContext which is no-overwrite (existing ctx
// values win). The overwrite semantic is intentional for the saga journal path:
// saga events carry no per-event principal identity, so the carrier must
// positively assert a known system identity rather than leaving the ambient one
// (which might be the triggering admin's) in place.
//
// # Caller allowlist (PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01)
//
// Only kernel/saga/sagaprojection/source.go is sanctioned to call this function.
// Any other callsite fails the archtest in CI. See the archtest for the
// AI-robust rating and Hard-upgrade tracking (gh #1628).
func InstallSystemPrincipal(ctx context.Context) context.Context {
	ctx = ctxkeys.WithActorID(ctx, SystemPrincipalActor)
	ctx = ctxkeys.WithSubjectID(ctx, SystemPrincipalActor)
	ctx = ctxkeys.WithTenantID(ctx, "")
	ctx = ctxkeys.WithSessionID(ctx, "")
	return ctx
}

// clearAmbientPrincipal returns a new context with all four principal ctx keys
// zeroed out (set to the empty string). It is the detach-boundary hook for
// Rebuild (coordinator.go): context.WithoutCancel preserves ALL ctx values
// including any ambient principal from the triggering request; clearAmbientPrincipal
// strips that ambient identity so the replay carrier can install the correct
// per-event identity without the no-overwrite guard blocking it.
//
// For the outbox path: RestoreToContext is no-overwrite — after clearAmbientPrincipal
// the event's own wire identity is installed cleanly (ADR #1609 §5, F1 flip).
// For the saga journal path: InstallSystemPrincipal is called by the carrier's
// RestoreContext unconditionally, so clearAmbientPrincipal is a safety measure
// (the overwrite would win regardless).
//
// This function is unexported: it is ONLY called at the Rebuild detach boundary
// in this package. No other call site is valid.
func clearAmbientPrincipal(ctx context.Context) context.Context {
	ctx = ctxkeys.WithActorID(ctx, "")
	ctx = ctxkeys.WithSubjectID(ctx, "")
	ctx = ctxkeys.WithTenantID(ctx, "")
	ctx = ctxkeys.WithSessionID(ctx, "")
	return ctx
}
