package main

import (
	"context"
	"fmt"

	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	prom "github.com/prometheus/client_golang/prometheus"
)

// ConfigCoreModule wires configcore: KeyProvider → ValueTransformer →
// PGResource/cellOpts (storage-backend specific) → configcore.ConfigCore.
//
// ref: uber-go/fx fx.Module("configcore", ...) — self-contained module.
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type ConfigCoreModule struct {
	// KeyProviderOverride bypasses env-based KeyProvider construction when
	// non-nil. Production code leaves this unset; tests use it to inject a
	// fake KeyProvider (e.g. one that also implements
	// kernel/lifecycle.ManagedResource) and assert wiring behaviour without
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
func (m ConfigCoreModule) Provide(ctx context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	// 1. Cursor codec: read configcore-namespaced env via LoadCursorKeys then build.
	cfgPrimary, cfgPrevious := LoadCursorKeys("CONFIGCORE")
	cursorCodec, err := buildCursorCodec(shared.Topology.AdapterMode,
		"GOCELL_CONFIGCORE_CURSOR_KEY", "GOCELL_CONFIGCORE_CURSOR_PREVIOUS_KEY",
		cfgPrimary, cfgPrevious,
		"corebundle-cfg-cursor-key--32bb!", "config")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configcore cursor codec: %w", err)
	}

	// 2. KeyProvider: read configcore-namespaced env (or use test override).
	kp := m.KeyProviderOverride
	if kp == nil {
		providerName, masterKey, prevMasterKey := LoadConfigCoreKeyProvider()
		kp, err = buildKeyProvider(shared.Topology.StorageBackend, shared.Topology.AdapterMode, providerName, masterKey, prevMasterKey)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("configcore key provider: %w", err)
		}
	}
	vt := keyProviderToTransformer(kp)

	// 3. PG pool: read configcore-namespaced env.
	pgCfg, err := LoadPGConfig("CONFIGCORE")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configcore pg config: %w", err)
	}
	pgRes, cellOpts, err := buildConfigCoreOpts(ctx, shared.Topology, pgCfg, shared.EventBus, shared.PromStack.metricProvider, vt)
	if err != nil {
		return nil, nil, nil, err
	}

	// Register the stale-cipher counter against the isolated per-run registry.
	// Use Register (not MustRegister) so that repeated Provide calls in the
	// same process (e.g. integration tests with shared registry) are handled
	// gracefully: AlreadyRegisteredError carries the existing collector so we
	// can reuse it instead of creating an orphaned counter.
	staleCipherCounter, err := registerOrReuseCounter(shared.PromStack.registry, configStaleCipherOpts)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configcore: register stale_cipher counter: %w", err)
	}

	baseOpts := []configcore.Option{
		configcore.WithPublisher(shared.EventBus),
		configcore.WithCursorCodec(cursorCodec),
		configcore.WithOnStaleCipherMetric(staleCipherCounter),
	}
	if vt != nil {
		baseOpts = append(baseOpts, configcore.WithValueTransformer(vt))
	}
	c := configcore.NewConfigCore(append(baseOpts, cellOpts...)...)

	// A13: register vault token renewal counters when the KeyProvider exposes them.
	if err := registerRenewalMetrics(kp, shared.PromStack.registry); err != nil {
		return nil, nil, nil, fmt.Errorf("configcore: register renewal metrics: %w", err)
	}

	var opts []bootstrap.Option
	var provisional []kernellifecycle.ManagedResource
	if pgRes != nil {
		opts = append(opts, bootstrap.WithManagedResource(pgRes))
		provisional = append(provisional, pgRes)
	}
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

// renewalMetricsProvider is a local interface satisfied by vault.TransitKeyProvider
// (and any future KeyProvider that exposes Prometheus renewal metrics). Using an
// interface avoids importing the vault adapter package directly from config_module.go.
type renewalMetricsProvider interface {
	RenewalMetrics() []prom.Collector
}

// registerRenewalMetrics registers per-collector metrics exposed by KeyProvider
// implementations that satisfy renewalMetricsProvider. Returns error on
// registration failures other than AlreadyRegisteredError.
func registerRenewalMetrics(kp kcrypto.KeyProvider, reg prom.Registerer) error {
	rmp, ok := kp.(renewalMetricsProvider)
	if !ok {
		return nil
	}
	for _, col := range rmp.RenewalMetrics() {
		if err := reg.Register(col); err != nil {
			if _, ok := err.(prom.AlreadyRegisteredError); !ok {
				return fmt.Errorf("vault renewal metric: %w", err)
			}
		}
	}
	return nil
}

// registerOrReuseCounter registers a new counter with the given opts. If the
// counter is already registered (AlreadyRegisteredError), it reuses the
// existing collector. Any other registration error is returned as-is.
func registerOrReuseCounter(reg prom.Registerer, opts prom.CounterOpts) (prom.Counter, error) {
	c := prom.NewCounter(opts)
	if err := reg.Register(c); err != nil {
		are, ok := err.(prom.AlreadyRegisteredError)
		if !ok {
			return nil, err
		}
		if existing, ok2 := are.ExistingCollector.(prom.Counter); ok2 {
			return existing, nil
		}
		return nil, fmt.Errorf("existing collector is not a Counter: %w", err)
	}
	return c, nil
}
