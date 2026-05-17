package main

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

func runtimeBaseOptions(
	shared *SharedDeps,
	asm *assembly.CoreAssembly,
	consumerBase *outbox.ConsumerBase,
	metricsHandler http.Handler,
	adapterInfo map[string]string,
) []bootstrap.Option {
	healthRouteOpts := []bootstrap.HealthRouteGroupOption{
		bootstrap.WithMetricsHandler(metricsHandler),
	}
	if shared.VerboseToken != "" {
		// PR269 round-3: verbose-mode gating is a disclosure concern owned by
		// the health handler, not an authentication scheme. WithReadyzVerboseToken
		// plumbs the token to health.Handler.SetVerboseToken, which on mismatch
		// produces the canonical 401 ErrReadyzVerboseDenied envelope.
		healthRouteOpts = append(healthRouteOpts,
			bootstrap.WithReadyzVerboseToken(shared.VerboseToken),
		)
	}
	if shared.VerboseDisabled {
		healthRouteOpts = append(healthRouteOpts, bootstrap.WithReadyzVerboseDisabled())
	}

	opts := []bootstrap.Option{
		// Single composition-root clock: the same clock is on the assembly
		// (see buildAssembly above) and on the bootstrap; it threads through
		// the lifecycle and default-assembly fallback. The pair is the
		// load-bearing invariant of PROD-CLOCK-INJECTION-01 — never
		// default-fallback in adapters or cells.
		bootstrap.WithClock(shared.Clock),
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(shared.EventBus),
		bootstrap.WithSubscriber(shared.EventBus),
		// ConsumerBase is field-injected into SubscriberWithMiddleware (not a
		// middleware list entry). It is the explicit EntryHandler→SubscriberHandler
		// conversion boundary: idempotency Claim/Commit/Release + retry live here,
		// after the business middleware chain.
		bootstrap.WithConsumerBase(consumerBase),
		bootstrap.WithConsumerMiddleware(consumerMiddlewares(shared)...),
		bootstrap.WithSubscriptionValidator(obmetrics.ConfigEventOwnerValidator),
		bootstrap.WithAdapterInfo(adapterInfo),
		bootstrap.WithHealthRoutes(healthRouteOpts...),
		bootstrap.WithMetricsProvider(shared.PromStack.metricProvider),
	}
	if shared.RedisClient != nil {
		opts = append(opts,
			bootstrap.WithManagedResource(shared.RedisClient),
		)
	}
	return opts
}

func consumerMiddlewares(shared *SharedDeps) []outbox.SubscriptionMiddleware {
	return []outbox.SubscriptionMiddleware{
		configEventConsumerMiddleware(shared.ConfigEventCollector),
	}
}

func configEventConsumerMiddleware(collector obmetrics.ConfigEventCollector) outbox.SubscriptionMiddleware {
	return obmetrics.ConfigEventMiddleware(collector)
}

// newBootstrapFromOptions creates a bootstrap.Bootstrap from a pre-built option
// slice. CLOCK-INJECTION-TEST-CALLSITE-01 archtest flags any bootstrap.New
// call inside _test.go files; tests that need a bootstrap instance must route
// through this wrapper (it is a production file, not a test file, so the
// archtest does not match it). run.go also calls bootstrap.New directly with
// an //archtest:allow:clock-injection:via-slice trailer because runtime
// startup intentionally lives in run.go for grep-locality.
//
// runtimeBaseOptions always includes bootstrap.WithClock so the clock is
// never missing — this wrapper does not impose any additional contract.
func newBootstrapFromOptions(opts []bootstrap.Option) *bootstrap.Bootstrap {
	return bootstrap.New(opts...) //archtest:allow:clock-injection:via-slice opts assembled by defaultRuntimeOptions includes WithClock
}

// defaultRuntimeOptions constructs the ordered bootstrap.Option slice from the
// shared cross-cutting deps, a pre-built assembly, a ConsumerBase, a metrics
// handler, and the adapter info map. Called by runCorebundle after BuildApp returns.
//
// PoolResource options are contributed per-Cell by CellModule.Provide (via
// BuildApp opts). This function covers only the cross-cutting concerns:
// HTTP addr, publisher/subscriber, public/exempt endpoints, metrics, etc.
func defaultRuntimeOptions(
	shared *SharedDeps,
	asm *assembly.CoreAssembly,
	consumerBase *outbox.ConsumerBase,
	metricsHandler http.Handler,
	adapterInfo map[string]string,
) []bootstrap.Option {
	// PR-A14b: three-listener topology — primary (business routes + JWT auth),
	// internal (/internal/v1/* + service-token auth), health (/healthz /readyz
	// /metrics on a dedicated port).
	//
	// Primary listener: AuthJWTFromAssembly discovers IntentTokenVerifier from
	// accesscore post-Init (lazy phase4 resolution, fail-closed).
	// Internal listener: AuthServiceToken from InternalGuard. The listener is
	// always registered in the runtime path; SharedDeps.Validate requires both
	// InternalHTTPAddr and InternalGuard before runCorebundle reaches this point.
	// Health listener: framework-owned /healthz, /readyz, /metrics route groups;
	// when shared.VerboseToken is set, the health handler's strict-gate path
	// (WithReadyzVerboseToken → SetVerboseToken) requires a matching X-Readyz-Token
	// for ?verbose=true requests; mismatches return 401 ErrReadyzVerboseDenied.
	//
	// ref: go-kratos/kratos app.go — per-server option pattern.
	opts := runtimeBaseOptions(shared, asm, consumerBase, metricsHandler, adapterInfo)
	if shared.PrimaryHTTPAddr != "" {
		opts = append(opts, bootstrap.WithListener(
			cell.PrimaryListener, shared.PrimaryHTTPAddr,
			[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)},
		))
	}
	opts = append(opts, bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr, buildInternalAuthChain(shared.InternalGuard)))
	if shared.HealthHTTPAddr != "" {
		opts = append(opts, bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr, []cell.ListenerAuth{cell.AuthNone{}}))
	}
	opts = append(opts, devtoolsOption(shared))
	return opts
}

// buildInternalAuthChain constructs the auth chain for the internal listener.
// guard is always non-nil after SEC-FAIL-CLOSED: internalGuardFromEnv now
// returns an error rather than a nil guard in all adapter modes when
// GOCELL_SERVICE_SECRET is unset, so SharedDeps.Validate fails fast before
// this function is reached with a nil guard.
//
// See docs/ops/listener-topology.md for the deployment topology, threat boundaries,
// and single-listener migration guide that frame this auth-chain composition.
func buildInternalAuthChain(guard *internalGuard) []cell.ListenerAuth {
	return []cell.ListenerAuth{cell.MustNewAuthServiceToken(guard.NonceStore(), guard.ring)}
}
