# ADR-1821: Reconcile loops run under a system producer identity

**Status**: Accepted
**Date**: 2026-06-10
**Issue**: Closes #1821 (cert-renewal lifecycle producer 显式 system principal + principal/tenant 测试)
**Related**: ADR `202605291600-661-adr-kernel-reconcile-design.md` (reconcile harness), ADR `202605281200-1042-outbox-wire-envelope-principal-occurred-at.md` (principal ctx-injection trust boundary), `kernel/projection/system_principal.go` (saga journal system identity, the sibling pattern), #1808 F5 (cluster C4), #1812 (cert-renewal reconciler)

---

## Context

PR #1812 landed the iotdevice cert-renewal reconciler — the archetype-② producer
(reconcile → async command). It runs on the cell **lifecycle ctx**, which carries
no request principal. `command.EmitAsync` → `outbox.NewEntry` injects the producer
principal from the construction ctx (the single injection trust boundary, ADR-1042);
with an empty ctx the emitted entry's principal is empty, so the Claimer dedup key's
tenant dimension falls to the `_notenant` sentinel.

#1812 only **documented** this single-tenant assumption (a godoc caveat). It left two
gaps (#1808 F5, root-cause cluster C4):

1. **Not a code fact.** The tenantless/system identity was an *implicit* property of
   an empty lifecycle ctx, not a positively asserted one. The reconcile producer had
   no explicit producer-identity boundary — a future ambient principal leaking into
   the lifecycle ctx would silently change the dedup key's tenant dimension (a
   cross-tenant dedup-collision risk in any multi-tenant copy of the archetype).
2. **No assertion test** on the emitted entry's principal/tenant.

The root cause is framework-level: a reconcile loop is a background control loop with
no request principal, but the framework did not give it an identity — it inherited
whatever the startup ctx carried.

## Decision

The **reconcile Loop installs a fixed system producer identity** at its single
reconcile chokepoint (`kernel/reconcile.Loop.process`, immediately before
`recoverReconcile`/`Reconcile`):

```
reconcileCtx = installSystemProducerIdentity(reconcileCtx)
```

`installSystemProducerIdentity` (unexported, `kernel/reconcile/identity.go`)
**overwrites** all four principal ctx keys: actor=subject=`"system"`, tenant="",
session="". This mirrors `kernel/projection.InstallSystemPrincipal` (the saga journal
replay carrier), whose own godoc already names "reconcile worker identity" as a
canonical use of the `"system"` sentinel — this ADR makes that statement a framework
invariant.

Consequence: **every** reconciler (now and future) emits commands/events under a
tenantless system principal **by construction**. The dedup key namespace is
`_notenant` as a code fact, independent of (and overwriting) any ambient
lifecycle-ctx identity. The cert-renewal reconciler itself is unchanged — it inherits
the framework identity.

### Alternatives considered (rejected)

- **A — test-only**: add the assertion test, leave the identity implicit-empty. Does
  not fix the ambient-leak root cause; only characterizes the status quo.
- **B — reuse `projection.InstallSystemPrincipal` from the producer**: broadens a P1
  saga-only funnel against its documented Hard trajectory (#1702 = seal it tighter),
  and couples `examples/iotdevice` → `kernel/projection`.
- **C — new exported `outbox.WithSystemProducerContext` helper + new deny-all
  archtest funnel**: introduces an exported system-identity-laundering surface that
  must then be gated by a brand-new funnel; the actual consumer lives in the
  `examples/iotdevice` go.work satellite which the platform archtest cannot scan
  (ModeModule, gh #1590), so its code fact would only be Medium (test). Heaviest, and
  mis-aligned (Hard on a threat it itself creates; can't reach the real consumer).
- **D (chosen)**: install at the reconcile Loop chokepoint via an unexported helper.
  No exported surface, no new funnel; fixes all reconcilers at the root.

## Threat re-evaluation (ai-robust charter §"ADR amendment 重评威胁矩阵")

The threat for any system-identity install is **audit-identity downgrade /
impersonation**: a caller stripping a real authenticated principal and emitting as
`"system"`. This is the same P1 threat that gates `InstallSystemPrincipal`
(PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01) and the principal ctxkeys writers
(CTXKEYS-PRINCIPAL-WRITE-CALLER-01).

D **does not widen** that threat surface:

- The installer is **unexported** in `kernel/reconcile`. No producer (`cells/*`,
  `examples/*`, `runtime/*`) can reference it — Go visibility forbids it. Only
  `Loop.process` calls it. There is no exported producer-facing inject API (ADR-1042's
  "principal comes from ctx, not the producer call site" invariant is **preserved**,
  not amended — a reconcile ctx is established by the framework, exactly as the auth
  middleware establishes a request ctx and the saga carrier establishes a replay ctx).
- The actual ctxkeys writes are added to the existing
  CTXKEYS-PRINCIPAL-WRITE-CALLER-01 allowlist as a sanctioned writer file.

Threat matrix delta vs. before this change: **none added.** A reconcile producer
previously emitted under an empty/ambient identity (a latent inheritance risk); it now
emits under a fixed, recognizable, auditable `"system"` identity. The change reduces
attack surface (no ambient inheritance) rather than expanding it.

## AI-robust rating (charter §"Funnel 双向锁评级")

- **Upstream Hard**: `installSystemProducerIdentity` is unexported; Go visibility makes
  it unreachable outside `kernel/reconcile`, and only `Loop.process` references it.
  There is no exported surface to gate.
- **Downstream Hard**: the ctxkeys writes are caller-allowlisted by
  CTXKEYS-PRINCIPAL-WRITE-CALLER-01 (go/types object resolution; alias/dot-import safe).
- **Residual blind spot (honest)**: a different function inside `kernel/reconcile`
  could call the unexported installer. Accepted — the package is small trusted
  framework code and the threat model is external producers, not in-package framework
  code; a same-package caller-allowlist would be disproportionate machinery.

## Consequences

- **All reconcilers** now run under the system identity. Today there are two
  (`devicecertrenewal` — the only one that emits; `kernel/command.Sweeper` — emits
  nothing, behaviorally neutral). The behavior is correct for both: reconcilers have
  no request principal.
- The dedup key namespace stays `_notenant`; no wire/contract/event/command-key change,
  no version bump. Only the emitted entry's audit identity changes from empty → `"system"`.
- A **multi-tenant** reconciler must add its own tenant dimension to its command-id /
  store derivation — it cannot rely on an ambient ctx tenant, which the install strips.
  This is documented at the install site and the cert-renewal reconciler godoc.
- Tests: kernel/reconcile proves the framework guarantee (install + overwrite + the
  Loop wires it onto the reconciler ctx); examples/iotdevice proves the cert-renewal
  producer's emitted entry is system/tenantless end-to-end through a real Loop.
