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
	"github.com/ghbvf/gocell/pkg/nilutil"
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
// Zero value retains DefaultStartTimeout (30s). Negative disables default timeout
// (hooks without own StartTimeout will block indefinitely).
func WithLifecycleDefaultStartTimeout(d time.Duration) Option {
	return func(b *Bootstrap) { b.defaultStartTimeout = d }
}

// WithLifecycleDefaultStopTimeout mirrors WithLifecycleDefaultStartTimeout for StopTimeout.
// Zero value retains DefaultStopTimeout (10s). Negative disables default timeout.
func WithLifecycleDefaultStopTimeout(d time.Duration) Option {
	return func(b *Bootstrap) { b.defaultStopTimeout = d }
}

// WithManagedCloser registers an adapter or resource that implements
// lifecycle.ContextCloser for LIFO teardown during graceful shutdown.
// The shared shutCtx budget propagates directly to c.Close(ctx), so the
// resource participates in the same shutdown deadline as all other components.
//
// Use this instead of a bare defer c.Close() so that:
//
//   - The resource is closed in LIFO order after HTTP and worker shutdown.
//   - The shared shutdownTimeout ctx is honored (not an arbitrary timeout).
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
		if nilutil.IsNil(c) {
			b.closerNil = true
			return
		}
		b.closers = append(b.closers, c)
	}
}

// WithShutdownTimeout overrides the default graceful shutdown timeout.
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
// The delay counts toward the total shutdownTimeout budget (not additive).
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
// shutdownTimeout + 10s, phase0 emits a slog.Warn so operators can spot a
// misalignment between the bootstrap budget and the pod-spec grace window
// before SIGKILL truncates a real shutdown. preShutdownDelay does not
// appear in the formula because it is consumed inside shutdownTimeout (see
// WithPreShutdownDelay godoc).
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
