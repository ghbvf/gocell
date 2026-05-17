package bootstrap

// options_lifecycle.go — With* option functions covering kernel/cell Lifecycle,
// ManagedResource closers, and shutdown budgets.
//
// Covers: WithLifecycle, WithLifecycleDefaultStartTimeout,
// WithLifecycleDefaultStopTimeout, WithManagedCloser, WithShutdownTimeout,
// WithPreShutdownDelay.
//
// ref: uber-go/fx Lifecycle.Append OnStop(ctx) — managed teardown registration.
// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go engageStopProcedure LIFO.

import (
	"time"

	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/pkg/validation"
)

// WithLifecycle registers a hook-registration callback invoked during New()
// (after all options are applied, as part of lifecycle initialisation). Use
// for composition-root Hook registration without needing a Bootstrap reference.
// Multiple WithLifecycle options and direct b.Lifecycle().Append() calls
// accumulate in the order they are applied.
func WithLifecycle(fn func(lc Lifecycle)) Option {
	return func(b *Bootstrap) {
		if fn != nil {
			b.lifecycleRegistrars = append(b.lifecycleRegistrars, fn)
		}
	}
}

// WithLifecycleDefaultStartTimeout overrides the per-hook default StartTimeout.
// Zero value retains DefaultStartTimeout (30s).
//
// Since ADR 202605170000 §D-B, StartTimeout is NOT enforced as an OnStart ctx
// deadline — it is only the slow-start warning threshold (warn when an OnStart
// elapses ≥ 80% of it). A hook whose OnStart never returns is bounded by the
// orchestration-layer backstop (caller ctx + WithStartupTimeout), not by this
// value. Negative therefore only disables the slow-start warning; it does NOT
// remove a startup deadlock backstop. The actual startup deadlock backstop is
// WithStartupTimeout (orchestration-layer); this option only affects the
// slow-start warning threshold.
func WithLifecycleDefaultStartTimeout(d time.Duration) Option {
	return func(b *Bootstrap) { b.defaultStartTimeout = d }
}

// WithStartupTimeout sets the whole-Start orchestration budget. Bootstrap.Run
// supervises lifecycle.Start in a goroutine and aborts if it has not completed
// when EITHER the caller ctx is canceled OR this budget elapses; on abort it
// cancels ownerCtx (unblocking a wedged OnStart) then rolls back, returning
// ErrBootstrapStartupTimeout (timer path) or the caller ctx error.
//
// This is the deadlock backstop for the ADR 202605170000 §D-B contract
// (OnStart = owner ctx, no per-hook deadline): without it a hook whose OnStart
// never returns would wedge Run() forever (review P1-1).
//
// Zero retains DefaultStartupTimeout (30s). Negative disables the timer — the
// caller ctx remains the abort path (matches controller-runtime mgr.Start,
// which is unblocked only by its caller ctx). It is NOT a per-hook SLA; the
// per-hook slow-start warning is WithLifecycleDefaultStartTimeout.
//
// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go — Start
// returns when the caller ctx is canceled.
func WithStartupTimeout(d time.Duration) Option {
	return func(b *Bootstrap) { b.startupTimeout = d }
}

// WithLifecycleDefaultStopTimeout mirrors WithLifecycleDefaultStartTimeout for StopTimeout.
// Zero value retains DefaultStopTimeout (10s). Negative disables default timeout.
func WithLifecycleDefaultStopTimeout(d time.Duration) Option {
	return func(b *Bootstrap) { b.defaultStopTimeout = d }
}

// WithManagedCloser registers an adapter or resource that implements
// lifecycle.ContextCloser for LIFO teardown during graceful shutdown.
// The tearCtx budget (stage 3 — independent from drainCtx, see
// WithShutdownTimeout godoc) propagates directly to c.Close(ctx), so the
// resource participates in the same teardown deadline as all other LIFO
// components.
//
// Use this instead of a bare defer c.Close() so that:
//
//   - The resource is closed in LIFO order after HTTP and worker shutdown.
//   - The tearCtx (stage 3 budget) is honored (not an arbitrary timeout).
//   - Startup rollback also triggers the teardown on phase failures.
//
// Both bare-nil and typed-nil (non-nil interface holding a nil pointer) are
// rejected at phase0 with a fatal error, mirroring the WithManagedResource
// fail-fast pattern. This prevents a silent wiring bug from panicking at
// Close() call time during shutdown or rollback.
//
// ref: uber-go/fx Lifecycle.Append OnStop(ctx) — managed teardown registration.
// ref: kubernetes-sigs/controller-runtime pkg/manager/manager.go — strong
// dependency fail-fast.
func WithManagedCloser(c kernellifecycle.ContextCloser) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(c) {
			b.closerNil = true
			return
		}
		b.closers = append(b.closers, c)
	}
}

// WithShutdownTimeout overrides the default graceful shutdown timeout.
//
// Semantics: each phase10 stage bucket — drainCtx (readiness flip + HTTP
// drain) and tearCtx (LIFO teardown) — is allotted this duration
// independently. Worst-case wall clock for the entire shutdown is therefore
// approximately 2 × shutdownTimeout + finalize overhead.
//
// K8s deployments must set terminationGracePeriodSeconds >=
// 2 × shutdownTimeout + 10s (safety margin). See
// warnTerminationGracePeriodInsufficient,
// docs/ops/graceful-shutdown-k8s.md, and ADR
// docs/architecture/202605101730-adr-shutdown-budget-decouple.md.
func WithShutdownTimeout(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.shutdownTimeout = d
	}
}

// WithPreShutdownDelay sets a delay between marking /readyz as 503 and
// starting the HTTP server shutdown. This gives load balancers (e.g.,
// Kubernetes kube-proxy) time to observe the unhealthy readiness probe
// and stop routing new traffic before the server closes connections.
//
// Default is 0 (no delay). Typical Kubernetes deployments use 3-5 seconds.
// The delay is consumed INSIDE drainCtx (stage 1+2 budget), not on top of
// shutdownTimeout — a long preShutdownDelay narrows the remaining budget
// available to HTTP drain. LIFO teardown (stage 3) is unaffected because
// it owns an independent tearCtx.
//
// ref: Kubernetes pod shutdown — preStop counts toward terminationGracePeriodSeconds
func WithPreShutdownDelay(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.preShutdownDelay = d
	}
}

// WithTerminationGracePeriod declares the operator's expected K8s pod
// terminationGracePeriodSeconds for phase0 sanity-check ONLY. The value does
// NOT change runtime behavior — actual graceful-shutdown windows must be set
// in the Kubernetes pod spec.
//
// When the declared value is positive but smaller than
// 2 × shutdownTimeout + 10s, phase0 emits a slog.Warn so operators can spot
// a misalignment between the bootstrap budget and the pod-spec grace window
// before SIGKILL truncates a real shutdown. The 2× multiplier reflects the
// budget-isolation contract: phase10 drainCtx + tearCtx each own a
// shutdownTimeout-sized bucket. preShutdownDelay does not appear in the
// formula because it is consumed inside drainCtx (see WithPreShutdownDelay
// godoc and ADR 202605101730-adr-shutdown-budget-decouple).
//
// Zero value (default) skips the sanity check entirely. Callers that do not
// run on Kubernetes can omit this option without consequence.
//
// ref: Kubernetes pod lifecycle — terminationGracePeriodSeconds bounds the
// total time between SIGTERM and SIGKILL.
func WithTerminationGracePeriod(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.terminationGracePeriod = d
	}
}
