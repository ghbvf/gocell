// Package reconcile is GoCell's L4 desired-state convergence harness, a
// level-triggered control loop modeled on Kubernetes controller-runtime's
// Reconciler. It is delivered across stacked PRs: PR-A2 landed the minimal core
// documented below (the Reconciler interface, Request / Result, and the
// PermanentError classifier); PR-A3 landed the scheduling Loop, which is present
// from that commit on; PR-A4 landed the Trigger source abstraction
// (TickerTrigger / ChannelTrigger).
// Comments here describe how each type is consumed by the Loop to pin its
// contract. PR-A5 (#1166) refined scheduling: it added the shared heap-based
// delaying queue (F6, one waitingLoop goroutine) + dirty/processing dedup (F5,
// coalescing in-flight triggers into a single re-run) + per-entity exponential
// backoff (5ms..1000s, no jitter). PR-A7 (#1168) privatized the Loop constructor
// behind a Builder DSL: reconcile.New(r).With*().Build() is the sole public
// construction entry; all Loop config fields are unexported (Hard upstream funnel);
// a Trigger is required by Build; defaulting runs through a single applyDefaults()
// funnel at Start with no lazy getter methods. The Builder is a pure wiring layer:
// it injects config and validates required deps and introduces NO scheduling logic
// (all scheduling lives in Loop).
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
// A consumer implements Reconciler and wires a Loop via reconcile.New(r).With*().Build(). The
// whole public surface a consumer must learn at PR-A2 is:
//
//	Reconciler  — Reconcile(ctx, Request) (Result, error)
//	Request     — { EntityID string }
//	Result      — { RequeueAfter time.Duration }
//	PermanentError / IsPermanent — non-retryable error classification
//
// controller-runtime abstractions deliberately dropped: Informer / CRD watch,
// Predicate, Manager, Source.Informer (informer-bound watch),
// Builder.For·Owns·Watches, Result.Requeue (bool), Result.Priority. GoCell
// entities live in a cell-local table addressed by a single opaque EntityID —
// there is no namespace dimension and no K8s control plane to watch.
//
// # Trigger (PR-A4)
//
// A Trigger is the source of reconcile work — GoCell's minimal analog of
// controller-runtime's source.Source, feeding Requests into the Loop's queue:
//
//	Trigger       — Start(ctx, chan<- Request) error  (non-blocking; block-don't-drop)
//	TickerTrigger — emits a zero-value Request{} resync pulse every interval, off
//	                an injected clock.Clock (deterministically testable; replaces
//	                the informer resync GoCell lacks)
//	ChannelTrigger— forwards Requests from an external <-chan (e.g. an outbox
//	                consumer waking specific entities)
//
// The Loop's source field is the seam a Trigger feeds; the Builder (PR-A7)
// wires the Trigger's output channel into it via WithTrigger(t).Build().
// The Builder's .WithTrigger(t) DSL owns production wiring — a Trigger is
// required by Build(), ensuring every Loop has a work source.
//
// # Reconciler implementation pattern
//
//	type certRenewer struct{ repo CertRepo }
//
//	func (r *certRenewer) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
//		// A zero-value Request{} (empty EntityID) is the TickerTrigger resync
//		// pulse — "re-observe everything you own". Fan out to every owned entity;
//		// the targeted branch below handles each concrete EntityID. The ticker
//		// re-emits the pulse each interval, so a sweep that aborts early is
//		// re-driven on the next tick (level-triggered — nothing is lost).
//		if req.EntityID == "" {
//			ids, err := r.repo.ListPending(ctx)
//			if err != nil {
//				return reconcile.Result{}, err // transient: Loop backs off + retries
//			}
//			for _, id := range ids {
//				if _, err := r.Reconcile(ctx, reconcile.Request{EntityID: id}); err != nil {
//					return reconcile.Result{}, err // re-driven on the next resync pulse
//				}
//			}
//			return reconcile.Result{}, nil
//		}
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
//   - PROD-CLOCK-INJECTION-01 (PR-A3): the Loop's control-plane probe / requeue
//     timers are confined to the sealed controlPlaneClock type; kernel/reconcile
//     becomes the sanctioned control-plane host when the Loop + clock carve-out
//     land in PR-A3 — it is NOT yet enforced for this package at the PR-A2
//     commit. The TickerTrigger (PR-A4) does NOT use this carve-out: its
//     cadence is driven by an injected clock.Clock, so it makes no stdlib
//     time.* call.
//   - RECONCILE-TRIGGER-INTERFACE-FROZEN-01 (PR-A4): Trigger's method set is
//     exactly Start(context.Context, chan<- Request) error (send-only sink).
//   - RECONCILE-RESULT-LABEL-VALUES-FROZEN-01 (PR-A5): the result* const value
//     set is frozen to {success, transient, permanent, skipped}; recovered panics
//     map to "transient" — no 5th "panic" label.
//   - RECONCILE-REQUEUE-ENQUEUE-CALLER-01 (PR-A5): every channel send in
//     kernel/reconcile must be inside one of the three sanctioned functions
//     (drainReadyItems / feedFromSource / enqueueDelayed).
//   - RECONCILE-LEADER-INTERFACE-FROZEN-01 (PR-A6): the LeaderElector method set
//     (AcquireLease / ReleaseLease / RenewLease) and the LeaseToken field set
//     (incl. the monotonic Epoch uint64 fencing token) are reflect golden-locked.
//   - RECONCILE-FENCED-WRITE-FUNNEL-01 (PR-A6): the epoch-bound FencedWriter is
//     the reconciler's sole write surface — its fields + constructor are sealed
//     (unexported) so a consumer cannot forge an arbitrary-epoch writer; the mint
//     sites are pinned to loop.go / fenced.go.
//   - RECONCILE-LEADER-IMPL-FUNNEL-01 (PR-A6): LeaderElector is implemented only
//     in adapters/{redis,postgres} + the reconciletest fake (layering hygiene).
//
// Leader election (PR-A6) is whole-loop: when a LeaderElector is wired only the
// lease holder dispatches Reconcile; a lost lease cancels the lease-scoped ctx to
// interrupt the in-flight Reconcile. Leader election is NOT fencing — cross-replica
// correctness comes from the monotonic LeaseToken.Epoch threaded into a FencedWriter
// (write-path CAS) plus consumer idempotency, never the lease. A nil LeaderElector
// is single-process mode (always leader, Epoch 0, no fencing).
//
// ref: kubernetes-sigs/controller-runtime pkg/reconcile/reconcile.go
// ref: kubernetes/client-go tools/leaderelection/leaderelection.go
// ref: docs/architecture/202605291600-661-adr-kernel-reconcile-design.md
package reconcile
