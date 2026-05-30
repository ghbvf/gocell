// Package configcore is the platform composition module for the configcore Cell.
// It implements [composition.CellModule] and wires all configcore-specific
// dependencies from [composition.SharedDeps].
//
// This is a composition-root-layer package: it may import cells/, adapters/,
// and platform/internal/. It must NOT be imported by cells/, runtime/, or
// adapters/.
//
// # Vault-transit metrics routing
//
// composition.SharedDeps intentionally does NOT carry a *prometheus.Registry
// (that would import prometheus/client_golang into runtime/composition). The
// vault-transit metrics lazy construction therefore lives here and needs the
// registry injected at module construction time.
//
// Design decision: [Module] accepts a [VaultMetricsFactory] option. When nil
// (i.e. the local-aes or passthrough path), the vault metric set is never
// registered. This keeps the no-arg [Module] shape valid for local-aes and
// memory modes:
//
//	configcore.Module()                           // local-aes / mem
//	configcore.Module(configcore.WithVaultMetrics(factory)) // vault-transit
//
// ref: uber-go/fx fx.Module("configcore", ...) — self-contained module.
package configcore

import (
	"context"
	"fmt"
	"sync"

	prom "github.com/prometheus/client_golang/prometheus"

	promadapter "github.com/ghbvf/gocell/adapters/prometheus"
	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	configcell "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/platform/platformshared"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// VaultMetricsFactory lazily provides the vault-transit metric set.
// Only the vault-transit branch invokes it; local-aes / no-key deployments
// never register gocell_vault_* series.
//
// Typically wired to a sync.Once-guarded factory in cmd/corebundle that holds
// the isolated *prometheus.Registry.
type VaultMetricsFactory func() (*adaptervault.TransitMetrics, error)

// ModuleOption configures a configcore module.
type ModuleOption func(*module)

// WithVaultMetrics sets the vault-transit metrics factory. Required when
// GOCELL_CONFIGCORE_KEY_PROVIDER=vault-transit.
//
// Typical usage from cmd/corebundle:
//
//	factory := func() (*adaptervault.TransitMetrics, error) {
//	    return adaptervault.NewTransitMetrics(registry)  // once-guarded by caller
//	}
//	configcore.Module(configcore.WithVaultMetrics(factory))
func WithVaultMetrics(f VaultMetricsFactory) ModuleOption {
	return func(m *module) {
		m.vaultMetricsFactory = f
	}
}

// WithKeyProviderOverride injects a pre-built KeyProvider, bypassing env-based
// construction. Used by tests to inject a fake KeyProvider.
func WithKeyProviderOverride(kp kcrypto.KeyProvider) ModuleOption {
	return func(m *module) {
		m.keyProviderOverride = kp
	}
}

// WithPrometheusRegistry provides the isolated *prometheus.Registry needed to
// register the stale-cipher counter and (via WithVaultMetrics) any vault-transit
// metrics. cmd/corebundle calls this with its isolated per-run registry.
//
// When not set the stale-cipher counter is a no-op (acceptable in tests that
// do not exercise PG + configcore encryption paths).
func WithPrometheusRegistry(reg *prom.Registry) ModuleOption {
	return func(m *module) {
		m.registry = reg
	}
}

type module struct {
	vaultMetricsFactory VaultMetricsFactory
	keyProviderOverride kcrypto.KeyProvider
	registry            *prom.Registry
	// once / metrics / metricsErr implement lazy idempotent construction for
	// the vault-transit metrics set: only the vault-transit branch of
	// buildKeyProvider calls provideVaultTransitMetrics; local-aes and
	// passthrough never register gocell_vault_* series.
	once       sync.Once
	metrics    *adaptervault.TransitMetrics
	metricsErr error
}

// Module returns a composition.CellModule that wires the configcore Cell.
//
// Pass [WithVaultMetrics] when GOCELL_CONFIGCORE_KEY_PROVIDER=vault-transit;
// pass [WithKeyProviderOverride] in tests that inject a fake KeyProvider.
func Module(opts ...ModuleOption) composition.CellModule {
	m := &module{}
	for _, o := range opts {
		o(m)
	}
	return m
}

// ID returns the stable identifier used in error messages and logs.
func (*module) ID() string { return "configcore" }

// Provide resolves all configcore-specific dependencies and returns the
// constructed cell, bootstrap options, and provisional resources.
func (m *module) Provide(
	ctx context.Context, shared *composition.SharedDeps,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	// 1. Cursor codec.
	cfgPrimary, cfgPrevious := platformshared.LoadCursorKeys("CONFIGCORE")
	cursorCodec, err := platformshared.BuildCursorCodec(platformshared.CursorCodecConfig{
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

	// 2. KeyProvider (env or test override).
	kp, err := m.resolveConfigKeyProvider(shared)
	if err != nil {
		return nil, nil, nil, err
	}
	vt := keyProviderToTransformer(kp)

	// 3. Register the stale-cipher counter.
	// When m.registry is nil (tests without a prometheus setup) the counter is
	// a no-op — acceptable in environments that do not exercise PG + encryption
	// paths. Production callers pass WithPrometheusRegistry.
	staleCipherCounter, err := m.registerStaleCipherCounter()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configcore: register stale_cipher counter: %w", err)
	}

	// 4. PG storage and cell options.
	modResult, err := buildConfigCoreOpts(shared.Clock, configCoreModuleConfig{
		topology:         shared.Topology,
		pg:               shared.PG,
		publisher:        shared.EventBus,
		metricsProvider:  shared.MetricsProvider,
		valueTransformer: vt,
		onStaleCipher: func(_, _, _ string) {
			staleCipherCounter.Inc()
		},
	})
	if err != nil {
		return nil, nil, nil, err
	}

	// 5. CAS protocol (CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest).
	casProto, err := newConfigCoreCASProtocol()
	if err != nil {
		return nil, nil, nil, err
	}

	baseOpts := []configcell.Option{
		configcell.WithCursorCodec(cursorCodec),
		configcell.WithMetricsProvider(shared.MetricsProvider),
		configcell.WithConfigEventCollector(shared.ConfigEventCollector),
		configcell.WithEventbusCacheCollector(shared.EventbusCacheCollector),
		configcell.WithCASProtocol(casProto),
	}
	baseOpts = append(baseOpts, modResult.cellOptions...)
	c := configcell.NewConfigCore(shared.Clock, baseOpts...)

	return buildConfigCoreResult(c, kp, modResult)
}

// resolveConfigKeyProvider returns the override when set, otherwise builds
// the key provider from the environment.
func (m *module) resolveConfigKeyProvider(shared *composition.SharedDeps) (kcrypto.KeyProvider, error) {
	if m.keyProviderOverride != nil {
		return m.keyProviderOverride, nil
	}
	providerName, masterKey, prevMasterKey := platformshared.LoadConfigCoreKeyProvider()
	kp, err := buildKeyProvider(
		shared.Topology.StorageBackend, shared.Topology.AdapterMode,
		providerName, masterKey, prevMasterKey, shared.Clock,
		m.provideVaultTransitMetrics,
	)
	if err != nil {
		return nil, fmt.Errorf("configcore key provider: %w", err)
	}
	return kp, nil
}

// provideVaultTransitMetrics lazily constructs vault-transit metrics using the
// injected factory. Returns an error when the vault-transit path is taken but
// no factory was provided.
func (m *module) provideVaultTransitMetrics() (*adaptervault.TransitMetrics, error) {
	m.once.Do(func() {
		if m.vaultMetricsFactory == nil {
			m.metricsErr = fmt.Errorf("configcore: vault-transit key provider requires a VaultMetricsFactory; " +
				"pass configcore.WithVaultMetrics(factory) to Module()")
			return
		}
		m.metrics, m.metricsErr = m.vaultMetricsFactory()
	})
	return m.metrics, m.metricsErr
}

// registerStaleCipherCounter registers the stale-cipher counter against
// m.registry. When m.registry is nil, returns a no-op counter.
func (m *module) registerStaleCipherCounter() (interface{ Inc() }, error) {
	if m.registry == nil {
		return noopCounter{}, nil
	}
	staleCipherOpts := prom.CounterOpts{
		Namespace: "gocell",
		Subsystem: "config",
		Name:      "stale_cipher_total",
		Help:      "Number of config values read that are encrypted with a non-current key version.",
	}
	counter, err := promadapter.RegisterOrReuseCounter(m.registry, staleCipherOpts)
	if err != nil {
		return nil, err
	}
	return counter, nil
}

// noopCounter is a no-op implementation for non-prometheus test environments.
type noopCounter struct{}

func (noopCounter) Inc() {}

// newConfigCoreCASProtocol builds the CAS protocol used by configcore.
func newConfigCoreCASProtocol() (*cas.Protocol, error) {
	p, err := cas.NewProtocol(cas.WithVersionField(configcell.VersionField))
	if err != nil {
		return nil, fmt.Errorf("configcore cas protocol: %w", err)
	}
	return p, nil
}

var _ composition.CellModule = (*module)(nil)
