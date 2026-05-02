package bootstrap

// bootstrap_phases.go — phaseState type and its helper methods.
//
// phaseState holds phase-local values shared across phases but not part of
// the teardown/error-channel core (runState). The phase method implementations
// are split across per-concern files:
//
//	phases_assembly.go  — phases 0–4  (config, assembly, auth, watcher)
//	phases_lifecycle.go — phase 3b    (LifecycleContributor + health checkers)
//	phases_http.go      — phase 5     (router construction, auth validation)
//	phases_events.go    — phase 6     (event router startup)
//	phases_workers.go   — phase 8     (worker group startup)
//	phases_shutdown.go  — phases 9–10 (signal wait, graceful shutdown)
//
// ref: uber-go/fx app.go (Run splits startup/shutdown via StartTimeout/StopTimeout)
// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go (engageStopProcedure LIFO teardown)

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/router"
)

// PR-258 RES-5: frameworkPrimaryWhitelist is gone. The internal-prefix
// isolation 404 used to require a policy-coverage whitelist entry because
// the handler was a chi route registered without auth.Mount. After
// RES-5 the isolation runs as router early-responder middleware before
// FinalizeAuth's policy-coverage walk reaches it, so no whitelist hole is
// needed.
// Health probes (/healthz, /readyz) are declared via auth.MustMount(Public:true)
// inside HealthRouteGroups and do not need any whitelist entry.

// phaseState extends runState with phase-local values that must be shared
// across phases but are not part of the teardown/error-channel core.
// All fields are set during their respective phase and read by later phases.
type phaseState struct {
	*runState

	// set by phase1
	cfg        config.Config
	cfgWatcher *config.Watcher

	// set by phase2
	pub outbox.Publisher
	sub outbox.Subscriber

	// set by phase3
	asm     *assembly.CoreAssembly
	reloads *reloadGate

	// set by phase3 (after StartWithConfig succeeds): per-cell RegistrySnapshot
	// produced during cell Init. Keyed by cell ID. Later phases drain the fields
	// of each snapshot instead of type-asserting on the live cell instances.
	cellSnapshots map[string]cell.RegistrySnapshot

	// set by phase5
	hh                   *health.Handler
	healthRouteGroupOpts []HealthRouteGroupOption            // resolved HealthRouteGroupOption stack (from WithHealthRoutes)
	rtr                  *router.Router                      // primary listener's router (may be nil when no primary)
	routers              map[cell.ListenerRef]*router.Router // all per-listener routers

	// set by phase7; consumed by phase10 as an explicit drain stage BEFORE LIFO
	// teardown so workers / event router / assembly stop only AFTER HTTP intake
	// closes and in-flight requests have completed. Mirrors kube-apiserver's
	// genericapiserver.go RunWithContext signal graph (NotAcceptingNewRequest →
	// InFlightRequestsDrained → stopHttpServerCtx → listenerStoppedCh).
	//
	// nil when no listeners declared (phase7 noop) or in tests that skip phase7.
	httpDrain func(context.Context) error

	// registeredCheckers guards against duplicate health checker names across
	// phases. Keyed by checker name; value is struct{}.
	registeredCheckers map[string]struct{}
}

// newPhaseState creates a phaseState wrapping a fresh runState and returns
// the owned run context alongside. Callers that only need the state (e.g.
// teardown-only tests) may discard the context via blank identifier.
func newPhaseState() (context.Context, *phaseState) {
	runCtx, rs := newRunState()
	return runCtx, &phaseState{
		runState:           rs,
		registeredCheckers: make(map[string]struct{}),
	}
}

// registerHealthChecker adds a named readiness checker to hh, returning an
// error on duplicate names (instead of panicking like hh.RegisterChecker).
func (s *phaseState) registerHealthChecker(name string, fn func(context.Context) error) error {
	if _, exists := s.registeredCheckers[name]; exists {
		return fmt.Errorf("bootstrap: duplicate health checker %q", name)
	}
	if err := s.hh.RegisterChecker(name, fn); err != nil {
		return err
	}
	s.registeredCheckers[name] = struct{}{}
	return nil
}

// addCloser registers a resource for teardown, preferring kernellifecycle.ContextCloser
// over io.Closer so that the shared shutCtx budget flows through to the resource.
//
// Priority:
//  1. kernellifecycle.ContextCloser: Close(ctx) — budget propagated directly.
//  2. io.Closer: wrapped via kernellifecycle.IgnoreCtx (ctx discarded at boundary).
//  3. Neither: silently skipped.
//
// ref: uber-go/fx Lifecycle.Append — OnStop hook receives the shared StopTimeout ctx.
func (s *phaseState) addCloser(res any) {
	if res == nil {
		return
	}
	name := fmt.Sprintf("%T", res)
	if cc, ok := res.(kernellifecycle.ContextCloser); ok {
		s.addNamedTeardown(name, cc.Close)
		return
	}
	if ic, ok := res.(io.Closer); ok {
		// F20: io.Closer fallback — the shared shutCtx budget is NOT propagated
		// to this resource. All GoCell adapters implement ContextCloser; this
		// path is only reached by external or legacy resources.
		slog.Warn("bootstrap: resource registered as io.Closer only; shutdown budget will NOT apply",
			slog.String("type", name))
		s.addNamedTeardown(name, kernellifecycle.IgnoreCtx(ic).Close)
	}
	// else: resource has no Close method — silently skip.
}
