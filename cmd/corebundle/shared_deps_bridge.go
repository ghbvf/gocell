package main

import (
	"github.com/ghbvf/gocell/runtime/composition"
)

// toCompositionSharedDeps converts the cmd-private SharedDeps to the public
// composition.SharedDeps expected by platform/ cell modules. This is the
// single bridge between cmd's internal representation and the public API.
//
// Fields on composition.SharedDeps map from cmd SharedDeps as follows:
//
//	JWTIssuer          ← JWTDeps.issuer
//	JWTVerifier        ← JWTDeps.verifier
//	MetricsProvider    ← PromStack.metricProvider
//	InternalHMACRing   ← InternalGuard.ring
//	(all others)       ← same-named flat fields
//
// The BootstrapLedgerStore field is intentionally NOT set here — it is wired
// by AuditCoreModule.Provide during composition.Builder.Build (same rationale
// as the cmd-side BOOTSTRAP-AUDIT-CHAIN-WIRING-01 comment in shared_deps.go).
func toCompositionSharedDeps(shared *SharedDeps) *composition.SharedDeps {
	cs := &composition.SharedDeps{
		Clock:                  shared.Clock,
		Topology:               shared.Topology,
		JWTIssuer:              shared.JWTDeps.issuer,
		JWTVerifier:            shared.JWTDeps.verifier,
		MetricsProvider:        shared.PromStack.metricProvider,
		EventBus:               shared.EventBus,
		ConfigEventCollector:   shared.ConfigEventCollector,
		EventbusCacheCollector: shared.EventbusCacheCollector,
		ConsumerClaimer:        shared.ConsumerClaimer,
		PG:                     shared.PG,
		Redis:                  shared.Redis,
		AssemblyID:             "", // filled by caller if needed
		PrimaryHTTPAddr:        shared.PrimaryHTTPAddr,
		InternalHTTPAddr:       shared.InternalHTTPAddr,
		HealthHTTPAddr:         shared.HealthHTTPAddr,
		MetricsToken:           shared.MetricsToken,
		VerboseToken:           shared.VerboseToken,
		VerboseDisabled:        shared.VerboseDisabled,
		HealthLocalOnly:        shared.HealthLocalOnly,
		ProjectRoot:            shared.ProjectRoot,
	}
	if shared.InternalGuard != nil {
		cs.InternalHMACRing = shared.InternalGuard.ring
	}
	return cs
}

// buildCmdLocals extracts cmd-private locals from a fully-populated SharedDeps.
// Must be called after buildSharedMetricsDeps (PromStack) and
// buildSharedReplayDeps (ConsumerClaimerKind) have populated shared.
func buildCmdLocals(shared *SharedDeps) *cmdLocals {
	l := &cmdLocals{
		registry:            shared.PromStack.registry,
		hookObserver:        shared.PromStack.hookObserver,
		metricProvider:      shared.PromStack.metricProvider,
		internalGuard:       shared.InternalGuard,
		consumerClaimerKind: shared.ConsumerClaimerKind,
		metricsHandler:      shared.metricsHandler,
	}
	l.initVaultMetricsFactory()
	return l
}
