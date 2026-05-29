// Package reconcile is GoCell's L4 desired-state convergence harness, a
// level-triggered control loop modeled on Kubernetes controller-runtime's
// Reconciler. It is delivered across stacked PRs: PR-A2 landed the minimal core
// documented below (the Reconciler interface, Request / Result, and the
// PermanentError classifier); PR-A3 landed the scheduling Loop — transplanted
// from runtime/command.SweeperLifecycle — which is present from that commit on.
// Comments here describe how each type is consumed by the Loop to pin its
// contract. Later PRs refine scheduling (PR-A5 adds a rate-limited delaying
// queue + dirty dedup) and construction (PR-A7 privatizes the Loop constructor
// behind a Builder); see the design ADR for the staged plan.
//
// # When to use
//
// reconcile is for L4 DeviceLatent convergence — periodically observing a set
// of non-terminal entities a cell OWNS (device commands, certificate rows,
// trust scores in the cell's own persistence) and driving each toward its
// desired state. It is deliberately NOT a business orchestrator (that is saga,
// #969) and NOT a CQRS read-model builder (that is the projection harness,
// #1079). See the design ADR for the boundary.
//
// The convergence authority is the consuming cell's, not the framework's: the
// framework provides the Loop harness (PR-A3); the cell exercises write
// authority over its own entity table inside Reconcile. This is distinct from
// GoCell's own compile/CI-time self-convergence (ADR 202605041430 §3.3), which
// this package does not touch.
//
// # Three-piece minimal core
//
// A consumer implements Reconciler and, once PR-A3 lands, wires a Loop. The
// whole public surface a consumer must learn at PR-A2 is:
//
//	Reconciler  — Reconcile(ctx, Request) (Result, error)
//	Request     — { EntityID string }
//	Result      — { RequeueAfter time.Duration }
//	PermanentError / IsPermanent — non-retryable error classification
//
// controller-runtime abstractions deliberately dropped: Informer / CRD watch,
// Predicate, Manager, Source, Builder.For·Owns·Watches, Result.Requeue (bool),
// Result.Priority. GoCell entities live in a cell-local table addressed by a
// single opaque EntityID — there is no namespace dimension and no K8s control
// plane to watch.
//
// # Reconciler implementation pattern
//
//	type certRenewer struct{ repo CertRepo }
//
//	func (r *certRenewer) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
//		cert, err := r.repo.Get(ctx, req.EntityID)
//		if err != nil {
//			return reconcile.Result{}, err // transient: Loop backs off + retries
//		}
//		if cert.Revoked {
//			// Nothing more to do, and retrying cannot help → do not retry.
//			return reconcile.Result{}, reconcile.PermanentError(errors.New("cert revoked"))
//		}
//		if cert.ExpiresAt.After(r.now().Add(30 * 24 * time.Hour)) {
//			return reconcile.Result{RequeueAfter: time.Hour}, nil // healthy: re-check later
//		}
//		if err := r.repo.Renew(ctx, cert); err != nil {
//			return reconcile.Result{}, err
//		}
//		return reconcile.Result{RequeueAfter: time.Hour}, nil
//	}
//
// # Enforced invariants (tools/archtest)
//
// Each invariant is tagged with the stacked PR that delivers it (A1←A2←A3 merge
// independently; an invariant is CI-enforced for this package only once its
// delivering PR is on develop):
//
//   - RECONCILE-INTERFACE-FROZEN-01 (PR-A2): Reconciler's method set is exactly
//     Reconcile(context.Context, Request) (Result, error).
//   - RECONCILE-REQUEST-FIELDS-FROZEN-01 (PR-A2): Request's field set is exactly
//     { EntityID string }.
//   - RECONCILE-RESULT-FIELDS-FROZEN-01 (PR-A2): Result's field set is exactly
//     { RequeueAfter time.Duration } (no Requeue bool / Priority int).
//   - PROD-CLOCK-INJECTION-01 (PR-A3): the Loop's control-plane ticker / probe
//     timers are confined to the sealed controlPlaneClock type; kernel/reconcile
//     becomes a sanctioned control-plane host (alongside runtime/command) when
//     the Loop + clock carve-out land in PR-A3 — it is NOT yet enforced for this
//     package at the PR-A2 commit.
//
// ref: kubernetes-sigs/controller-runtime pkg/reconcile/reconcile.go
// ref: docs/architecture/202605291600-661-adr-kernel-reconcile-design.md
package reconcile
