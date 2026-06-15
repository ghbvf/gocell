package bootstrap

// options_assembly.go — With* option functions covering config loading and
// CoreAssembly construction.
//
// Covers: WithConfig, WithAssembly, WithAssemblyID, WithControlPlaneTopology, WithDeploymentTopology.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.

import (
	"github.com/ghbvf/gocell/framework/kernel/assembly"
)

// WithConfig sets the YAML config path and environment prefix.
func WithConfig(yamlPath, envPrefix string) Option {
	return func(b *Bootstrap) {
		b.configPath = yamlPath
		b.envPrefix = envPrefix
	}
}

// WithAssembly sets a pre-built CoreAssembly.
func WithAssembly(asm *assembly.CoreAssembly) Option {
	return func(b *Bootstrap) {
		b.assemblyCore = asm
	}
}

// WithAssemblyID sets only the `cell_id` label used by the auto-wired HTTP
// metrics collector (R2). It does not change assembly identity, cell metadata,
// routing, health, tracing, or non-HTTP metrics.
//
// Recommended to set this matching asm.ID() when using WithAssembly(asm);
// omit to reuse assembly ID (auto-derived). Explicit value overrides
// assembly-derived.
//
// When neither WithAssemblyID nor WithAssembly is used, Bootstrap defaults
// to "default" (the ID of the auto-built assembly).
func WithAssemblyID(id string) Option {
	return func(b *Bootstrap) {
		b.assemblyID = id
	}
}

// WithDeploymentTopology supplies the codegen-derived deployment placement spec
// so phase0 seals+validates it into the runtime DeploymentTopology queried by
// transport routing. composition.Builder.Build injects this from the assembly's
// generatedDeploymentTopology(). Omitting it leaves the zero DeploymentTopology
// (all cells co-located) — identical to single-process behavior. This is
// DEPLOYMENT topology; distinct from WithControlPlaneTopology (adapter topology).
func WithDeploymentTopology(spec DeploymentTopologySpec) Option {
	return func(b *Bootstrap) { b.deploymentTopologySpec = spec }
}

// WithControlPlaneTopology supplies the resolved deployment Topology so phase0
// can validate that the listener service-token guard's actual NonceStore is
// replay-safe for the topology (#1410 review F1): an in-memory store is rejected
// for real multi-pod deployments, and an unrecognized kind is rejected
// fail-closed. composition.Builder.Build injects this from its trusted
// SharedDeps.Topology so the store that ACTUALLY guards /internal/v1/* — not just
// the declared SharedDeps.NonceStore — is checked at the real usage point
// (mirrors fx.ValidateApp: validate the constructed graph, not a parallel field).
//
// Topology is a sealed value type (NewTopology / TopologyFromEnv only), so a
// caller cannot forge a permissive value via a struct literal. Omitting this
// option leaves the zero Topology (RequireProductionControlPlane()==false), which
// skips the topology-dependent replay check — identical to prior behavior for
// hand-written bootstraps. The always-on nil/noop/ring service-token checks
// (validateAuthServiceTokenPlan) run regardless.
//
// Note: the broker-mandatory split gate (validateSplitTopologyBroker) still
// applies — a zero Topology reads as non-postgres (StorageBackend()==""), so a
// split deployment topology is fail-closed rejected (not skipped). To pass the
// gate with a split topology, supply a postgres Topology AND inject non-nil
// publisher/subscriber via WithPublisher/WithSubscriber.
func WithControlPlaneTopology(topo Topology) Option {
	return func(b *Bootstrap) {
		b.controlPlaneTopology = topo
	}
}
