// INVARIANT: OIDC-JWKS-ROTATION-WORKER-01
//
// (a) fail-open structural Hard — discover() returns early on error without
//     touching a.provider (oidc.go:127-135). Any future edit that makes
//     discover() clear a.provider on error would break this invariant.
//     The fail-open test case (TestRefreshWorker_FailOpen) asserts that the
//     old provider is preserved after a failed refresh.
//
// (b) no readyz gate — gating readyz on JWKS staleness would remove all
//     running instances during a systemic IdP outage, amplifying the incident
//     into a full service outage. Fail-open post-init is the correct policy,
//     consistent with K8s kube-apiserver OIDC (oidc.go#L213: first-time only)
//     and Envoy jwt_authn (init-gate then fail-open). Metrics + alerting are
//     the detection mechanism, not health gates.
//
// (c) no cache_max_age — go-oidc v3.18 RemoteKeySet.verify() re-fetches
//     jwks_uri reactively on kid-miss (jwks.go:152-300). v3 removed the v1/v2
//     time-based cacheUntil entirely. There is no Cache-Control logic to hook.
//     This worker's purpose is full provider re-discovery (endpoints, alg),
//     not JWKS cache management.
//
// (d) double-Start panics on close(workerDone) — runRefreshLoop calls
//     close(a.workerDone) via defer; a second Start on the same Adapter will
//     panic because closing an already-closed channel is undefined. WorkerGroup
//     is responsible for single-Start enforcement; this adapter does not add a
//     guard (see backlog CLOCK-INJECTION-STRUCT-FIELD-CTOR-01 for context).
//
// (e) clock enforcement — oidc.Adapter uses a struct-field Clock rather than a
//     WithClock option, so CLOCK-INJECTION-TEST-CALLSITE-01 archtest (which
//     covers WithClock-option constructors) does not cover oidc.New. Enforcement
//     is instead the runtime clock.MustHaveClock panic in New(). See backlog
//     entry CLOCK-INJECTION-STRUCT-FIELD-CTOR-01 for the pre-existing archtest
//     coverage gap.

package oidc

import (
	"context"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/worker"
)

// oidcRefreshWorker adapts *Adapter to the kernel/worker.Worker contract so
// that bootstrap.WithManagedResource(adapter) auto-starts the refresh loop
// via WorkerGroup.
//
// Stop is idempotent: calling Stop multiple times is safe (signalStop uses
// sync.Once).
//
// ref: adapters/s3.s3HealthWorker — same worker-adapter pattern.
type oidcRefreshWorker struct{ a *Adapter }

// Compile-time assertion: oidcRefreshWorker satisfies kernel/worker.Worker.
var _ worker.Worker = (*oidcRefreshWorker)(nil)

// Start implements worker.Worker. Blocks until the refresh loop exits.
func (w *oidcRefreshWorker) Start(ctx context.Context) error {
	w.a.runRefreshLoop(ctx)
	return nil
}

// Stop implements worker.Worker. Signals the refresh loop to stop and waits
// for it to drain, bounded by ctx. Idempotent.
func (w *oidcRefreshWorker) Stop(ctx context.Context) error {
	w.a.signalStop()
	// Fast path: worker goroutine never started — nothing to drain.
	if !w.a.started.Load() {
		return nil
	}
	select {
	case <-w.a.workerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// refreshInterval returns the configured refresh interval, falling back to
// defaultOIDCRefreshInterval when Config.RefreshInterval is zero.
func (a *Adapter) refreshInterval() time.Duration {
	if a.config.RefreshInterval > 0 {
		return a.config.RefreshInterval
	}
	return defaultOIDCRefreshInterval
}

// runRefreshLoop is the background goroutine body. It ticks every
// refreshInterval, calls Refresh to re-discover the OIDC provider, and
// exits when either ctx is done or stopCh is closed. It closes workerDone
// on exit.
//
// On each tick:
//   - success → reset consecutiveFailures, emit RecordRefresh(true), emit
//     slog.Info("oidc: jwks refresh recovered") if recovering from prior failures.
//   - failure → increment consecutiveFailures, emit RecordRefresh(false),
//     emit slog.Warn with issuer/error/consecutive_failures fields.
//     The fail-open invariant is structural: discover() returns early on
//     error without touching a.provider, so the old provider is preserved.
func (a *Adapter) runRefreshLoop(ctx context.Context) {
	a.started.Store(true)
	defer close(a.workerDone)

	ticker := a.clk.NewTicker(a.refreshInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C():
			_, err := a.Refresh(ctx)
			if err != nil {
				n := a.consecutiveFailures.Add(1)
				a.refreshCollector.RecordRefresh(false)
				slog.Warn("oidc: jwks refresh failed",
					slog.String("issuer", a.config.IssuerURL),
					slog.Any("error", err),
					slog.Int64("consecutive_failures", n),
				)
			} else {
				prev := a.consecutiveFailures.Swap(0)
				a.refreshCollector.RecordRefresh(true)
				if prev > 0 {
					slog.Info("oidc: jwks refresh recovered",
						slog.String("issuer", a.config.IssuerURL),
						slog.Int64("consecutive_failures_reset_from", prev),
					)
				}
			}
		case <-a.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}
