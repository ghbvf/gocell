package main

import (
	"context"
	"fmt"

	prom "github.com/prometheus/client_golang/prometheus"

	promadapter "github.com/ghbvf/gocell/adapters/prometheus"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// ConfigCoreModule wires configcore: KeyProvider → ValueTransformer →
// PoolResource/cellOpts (storage-backend specific) → configcore.ConfigCore.
//
// ref: uber-go/fx fx.Module("configcore", ...) — self-contained module.
type ConfigCoreModule struct {
	// KeyProviderOverride bypasses env-based KeyProvider construction when
	// non-nil. Production code leaves this unset; tests use it to inject a
	// fake KeyProvider (e.g. one that also implements
	// kernel/lifecycle.ManagedResource) and assert wiring behavior without
	// touching GOCELL_CONFIGCORE_KEY_PROVIDER / GOCELL_CONFIGCORE_MASTER_KEY / Vault.
	KeyProviderOverride kcrypto.KeyProvider
}

// ID returns the stable identifier used in error messages.
func (ConfigCoreModule) ID() string { return "configcore" }

// configStaleCipherOpts is the Prometheus counter descriptor for M3 stale-key
// observability. The counter is registered against the isolated per-run
// Prometheus registry (shared.PromStack.registry) inside Provide so it
// never touches the global default registry and remains isolated between tests.
var configStaleCipherOpts = prom.CounterOpts{
	Namespace: "gocell",
	Subsystem: "config",
	Name:      "stale_cipher_total",
	Help:      "Number of config values read that are encrypted with a non-current key version.",
}

// Provide resolves all configcore-specific dependencies and returns the
// constructed cell, any bootstrap.Options (e.g. WithManagedResource), and the
// provisional resources that BuildApp must close if a subsequent module's
// Provide fails. It reads configcore-specific environment variables directly
// via the LoadPGConfig / LoadCursorKeys / LoadConfigCoreKeyProvider helpers.
func (m ConfigCoreModule) Provide(
	ctx context.Context, shared *SharedDeps,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	// 1. Cursor codec: read configcore-namespaced env via LoadCursorKeys then build.
	cfgPrimary, cfgPrevious := LoadCursorKeys("CONFIGCORE")
	cursorCodec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: shared.Topology.AdapterMode,
		EnvName:     "GOCELL_CONFIGCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_CONFIGCORE_CURSOR_PREVIOUS_KEY",
		Primary:     cfgPrimary,
		Previous:    cfgPrevious,
		DevDefault:  "corebundle-cfg-cursor-key--32bb!",
		Label:       "config",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configcore cursor codec: %w", err)
	}

	// 2. KeyProvider: read configcore-namespaced env (or use test override).
	kp, err := resolveConfigKeyProvider(m.KeyProviderOverride, shared)
	if err != nil {
		return nil, nil, nil, err
	}
	vt := keyProviderToTransformer(kp)

	// 3. Register the stale-cipher counter against the isolated per-run registry.
	// Use Register (not MustRegister) so that repeated Provide calls in the
	// same process (e.g. integration tests with shared registry) are handled
	// gracefully: AlreadyRegisteredError carries the existing collector so we
	// can reuse it instead of creating an orphaned counter.
	staleCipherCounter, err := promadapter.RegisterOrReuseCounter(shared.PromStack.registry, configStaleCipherOpts)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configcore: register stale_cipher counter: %w", err)
	}

	// 4. PG storage: consume the assembly's postgres capability provider
	// (provisionCapabilities opened the pool before BuildApp). In memory mode
	// shared.PG is nil and buildConfigCoreOpts takes the in-memory path.
	modResult, err := buildConfigCoreOpts(ConfigCoreModuleConfig{
		Topology:         shared.Topology,
		PG:               shared.PG,
		Publisher:        shared.EventBus,
		MetricsProvider:  shared.PromStack.metricProvider,
		ValueTransformer: vt,
		OnStaleCipher: func(_, _, _ string) {
			staleCipherCounter.Inc()
		},
		Clock: shared.Clock,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	// CAS protocol: declares the version-field name and conflict policy used by
	// all 6 CAS write paths in configcore (config Update/Delete/Rollback +
	// flag Update/Toggle/Delete). NewProtocol is the composition-root-only
	// constructor (CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest enforces this).
	casProto, err := newConfigCoreCASProtocol()
	if err != nil {
		return nil, nil, nil, err
	}

	baseOpts := []configcore.Option{
		// Outbox wiring is provided by buildConfigCoreOpts (PG adapter includes
		// the transactional writer; memory adapter passes writer=nil).
		configcore.WithClock(shared.Clock),
		configcore.WithCursorCodec(cursorCodec),
		configcore.WithMetricsProvider(shared.PromStack.metricProvider),
		configcore.WithConfigEventCollector(shared.ConfigEventCollector),
		configcore.WithEventbusCacheCollector(shared.EventbusCacheCollector),
		configcore.WithCASProtocol(casProto),
	}
	baseOpts = append(baseOpts, modResult.CellOptions...)
	c := configcore.NewConfigCore(baseOpts...)

	return buildConfigCoreResult(c, kp, modResult)
}

// resolveConfigKeyProvider returns m.KeyProviderOverride when set, otherwise
// builds the key provider from the environment. Extracted to reduce Provide's
// cognitive complexity.
func resolveConfigKeyProvider(override kcrypto.KeyProvider, shared *SharedDeps) (kcrypto.KeyProvider, error) {
	if override != nil {
		return override, nil
	}
	providerName, masterKey, prevMasterKey := LoadConfigCoreKeyProvider()
	kp, err := buildKeyProvider(
		shared.Topology.StorageBackend, shared.Topology.AdapterMode,
		providerName, masterKey, prevMasterKey, shared.Clock,
		shared.ProvideVaultTransitMetrics,
	)
	if err != nil {
		return nil, fmt.Errorf("configcore key provider: %w", err)
	}
	return kp, nil
}

// buildConfigCoreResult wires the provisional resources, relay opts, and
// key-provider managed resource. It is extracted from Provide to keep that
// function's cognitive complexity within the project limit.
func buildConfigCoreResult(
	c cell.Cell,
	kp kcrypto.KeyProvider,
	modResult ConfigCoreModuleResult,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	var opts []bootstrap.Option
	var provisional []kernellifecycle.ManagedResource

	// Relay opts: in postgres mode, BootstrapOpts carries WithRelay(relay) which
	// is the sole sanctioned path that hands the relay to bootstrap's managed-
	// resource pipeline (Worker/Close/Checkers via the package-private adapter
	// in runtime/bootstrap/relay_adapter.go).
	opts = append(opts, modResult.BootstrapOpts...)

	// A19: when the KeyProvider opts into lifecycle.ManagedResource (today:
	// vault-transit via TransitKeyProvider.Checkers()["vault_transit_ready"]),
	// register it with bootstrap so its probes flow into /readyz. Local-aes
	// has no external dependency and does not implement the interface — it is
	// naturally skipped here; future backends (AWS-KMS, GCP-KMS) opt in by
	// implementing ManagedResource themselves.
	if kpRes, ok := kp.(kernellifecycle.ManagedResource); ok {
		opts = append(opts, bootstrap.WithManagedResource(kpRes))
		provisional = append(provisional, kpRes)
	}
	return c, opts, provisional, nil
}

var _ CellModule = ConfigCoreModule{}

// newConfigCoreCASProtocol builds the CAS protocol used by configcore.
// Extracted from Provide to keep its cognitive complexity below the
// project lint ceiling (gocognit > 15).
func newConfigCoreCASProtocol() (*cas.Protocol, error) {
	p, err := cas.NewProtocol(cas.WithVersionField(configcore.VersionField))
	if err != nil {
		return nil, fmt.Errorf("configcore cas protocol: %w", err)
	}
	return p, nil
}
