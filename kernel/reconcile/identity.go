package reconcile

import (
	"context"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// systemProducerActor is the principal actor/subject sentinel installed on every
// reconcile ctx by installSystemProducerIdentity. A reconcile Loop is a
// background control loop with no request principal, so "system" is the correct,
// recognizable identity for any work it drives — audit log consumers can filter
// on it.
//
// Value "system" mirrors projection.SystemPrincipalActor (the saga journal
// replay sentinel); kernel/reconcile cannot import kernel/projection (projection
// imports kernel/outbox, which would risk a cycle), so the sentinel is duplicated
// here as a package-local const. Both sites intentionally use the same string so
// the audit trail is uniform across framework-internal system identities.
const systemProducerActor = "system"

// installSystemProducerIdentity returns ctx with the system producer identity
// positively installed: it OVERWRITES all four principal ctx keys —
// actor=subject=systemProducerActor, tenant="", session="".
//
// # Why
//
// A reconcile Loop runs on the cell lifecycle ctx, which carries no request
// principal. outbox.NewEntry injects the producer principal from the construction
// ctx (the single injection trust boundary), so without this install a
// reconciler's emitted commands/events would derive their identity from whatever
// the startup ctx happens to carry: empty today, but a future ambient principal
// leaking into the lifecycle ctx would silently change the Claimer dedup key's
// tenant dimension (issue #1821, #1808 F5). Installing a fixed system identity at
// the Loop's single reconcile chokepoint (Loop.process) makes the tenantless
// system semantics a code fact for every reconciler — current and future — rather
// than an implicit property of an empty ctx. The overwrite is deliberate: it must
// positively assert the system identity, not defer to a (possibly leaked) ambient
// one. Mirror of kernel/projection.InstallSystemPrincipal, which does the same for
// the saga journal replay carrier.
//
// # Dedup-key consequence
//
// outbox.ContextPrincipal reads exactly these four ctx keys. With tenant cleared,
// the principal's TenantID is empty, so a command's Claimer key
// (idemkey.DeriveCommandKey) lands under the "_notenant" sentinel namespace. That
// is correct for the single-tenant reconcile archetype; a MULTI-TENANT reconciler
// MUST add a tenant dimension to its own command-id / store derivation (it cannot
// rely on an ambient ctx tenant, which this install strips).
//
// # Scope-fallback boundary
//
// ContextPrincipal falls back to tenant.ScopeFromContext only when ctxkeys tenant
// is absent. This install sets ctxkeys tenant to "" (present-but-empty), which is
// the realistic lifecycle-ctx channel; it does NOT strip a tenant.WithScope (a
// tx-scope concept absent on the reconcile path). Reconcile ctx never carries a
// tenant scope, so the dedup namespace is "_notenant" in practice.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Upstream Hard: this function is UNEXPORTED in kernel/reconcile. Go
//     visibility makes it uncallable outside the package, and only Loop.process
//     references it — no producer (cells/* or examples/*) can install or forge a
//     system identity through this path. No exported producer-facing surface
//     exists to gate.
//   - Downstream Hard: the actual ctxkeys writes below are caller-allowlisted by
//     CTXKEYS-PRINCIPAL-WRITE-CALLER-01 (go/types object resolution); this file
//     is a sanctioned principal writer alongside the auth bridge, the outbox
//     consumer restore, and the projection system-principal carrier.
//
// Residual blind spot (honest self-check): another function inside
// kernel/reconcile could call this unexported installer. That is accepted — the
// package is small trusted framework code and the threat model is external
// producers, not in-package framework code; a same-package caller-allowlist would
// be disproportionate.
func installSystemProducerIdentity(ctx context.Context) context.Context {
	ctx = ctxkeys.WithActorID(ctx, systemProducerActor)
	ctx = ctxkeys.WithSubjectID(ctx, systemProducerActor)
	ctx = ctxkeys.WithTenantID(ctx, "")
	ctx = ctxkeys.WithSessionID(ctx, "")
	return ctx
}
