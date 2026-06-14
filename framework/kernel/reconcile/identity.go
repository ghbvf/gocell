package reconcile

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
)

// systemProducerActor is the principal actor/subject sentinel installed on every
// reconcile ctx by installSystemProducerIdentity. A reconcile Loop is a
// background control loop with no request principal, so "system" is the correct,
// recognizable identity for any work it drives — audit log consumers can filter
// on it.
//
// Value "system" mirrors projection.SystemPrincipalActor (the saga journal replay
// sentinel). It is duplicated here as a package-local const rather than imported:
// reconcile importing kernel/projection (a CQRS/saga read-model package) merely to
// borrow a sentinel string would be an inverted, gratuitous dependency — projection
// is a higher-level concern than the reconcile harness, not a shared-vocabulary
// package. (There is no import CYCLE — projection does not depend on reconcile — so
// this is a layering/coupling choice, not a compile constraint.) Both sites
// intentionally use the same string so the audit trail is uniform across
// framework-internal system identities; the duplication is two occurrences, below
// the rule-of-three extraction threshold. If a third system-identity install site
// appears, hoist the sentinel into a shared pkg/ constant.
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
// rely on an ambient ctx tenant, which this install strips). This MUST is now
// MACHINE-ENFORCED at construction (#1954): reconcile.New takes a REQUIRED sealed
// Tenancy stance (reconcile.SingleTenant / reconcile.TenantScoped), so a reconciler
// cannot be built without consciously declaring whether "_notenant" Claimer keys
// are correct for it — omitting the stance is a compile error, the zero value is a
// Build fail-fast, and the stance set is frozen by RECONCILE-TENANCY-DECLARED-01.
// Residual blind spot: the guard forces the declaration but cannot verify a
// TenantScoped reconciler's body actually encodes the tenant (consumer correctness,
// out of framework reach — see the reconcile.Tenancy godoc blind spots).
//
// # Scope-fallback boundary (#1824 F1)
//
// outbox.ContextPrincipal resolves the tenant from ctxkeys.TenantID, falling back to
// tenant.ScopeFromContext ONLY when the ctxkeys tenant key is ABSENT. This install
// positively writes the ctxkeys tenant as "" (present-but-empty), and ContextPrincipal
// treats a present key — even empty — as an AUTHORITATIVE tenantless assertion that
// wins over (suppresses) the scope fallback. So the emitted entry's principal tenant is
// empty and the Claimer key lands under "_notenant" BY CONSTRUCTION — not merely because
// a reconcile ctx happens to carry no tenant.WithScope. Even if a tenant scope is present
// on the ctx (the RLS "SET LOCAL" boundary the TxManager injects into the reconciler's
// own tx, opened AFTER this install — or a future ambient leak), the install's empty
// principal tenant still wins, keeping the tenantless outcome the code fact #1808 F5 asks
// for (proven end-to-end by the cert-renewal Loop test, and by the scoped-ctx unit test).
// This holds for EVERY reconciler: a multi-tenant reconciler that runs inside a tenant
// scope still emits under the tenantless system principal, so it MUST encode its
// per-tenant dimension in its own command-id / store derivation (see "# Dedup-key
// consequence" above) — it cannot rely on the ambient scope to color the principal tenant.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Upstream Hard: this function is UNEXPORTED in kernel/reconcile. Go
//     visibility makes it uncallable outside the package, and only Loop.process
//     references it — no producer (cells/* or examples/*) can install or forge a
//     system identity through this path. No exported producer-facing surface
//     exists to gate.
//   - Downstream Hard: the actual ctxkeys writes below are caller-allowlisted by
//     CTXKEYS-PRINCIPAL-WRITE-CALLER-01 (go/types object resolution); this file is
//     a sanctioned principal writer alongside the auth bridge, the outbox consumer
//     restore, and the projection system-principal carrier. Granularity note: that
//     allowlist is FILE-level (key = "kernel/reconcile/identity.go"), NOT the
//     function/callsite-level lock of PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01
//     — so a second function added to THIS file could call the setters without
//     tripping the archtest. That is the accepted same-file residual blind spot
//     below, not an equivalence claim with the projection funnel.
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
