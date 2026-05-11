package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	prom "github.com/prometheus/client_golang/prometheus"

	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/state/cas"
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
	kp := m.KeyProviderOverride
	if kp == nil {
		providerName, masterKey, prevMasterKey := LoadConfigCoreKeyProvider()
		kp, err = buildKeyProvider(
			shared.Topology.StorageBackend, shared.Topology.AdapterMode,
			providerName, masterKey, prevMasterKey, shared.Clock,
		)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("configcore key provider: %w", err)
		}
	}
	vt := keyProviderToTransformer(kp)

	// 3. Register the stale-cipher counter against the isolated per-run registry.
	// Use Register (not MustRegister) so that repeated Provide calls in the
	// same process (e.g. integration tests with shared registry) are handled
	// gracefully: AlreadyRegisteredError carries the existing collector so we
	// can reuse it instead of creating an orphaned counter.
	staleCipherCounter, err := registerOrReuseCounter(shared.PromStack.registry, configStaleCipherOpts)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configcore: register stale_cipher counter: %w", err)
	}

	// 4. PG pool: read configcore-namespaced env.
	pgCfg, err := LoadPGConfig("CONFIGCORE")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configcore pg config: %w", err)
	}
	modResult, err := buildConfigCoreOpts(ctx, ConfigCoreModuleConfig{
		Topology:         shared.Topology,
		PGConfig:         pgCfg,
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
	pgRes := modResult.PGResource
	cellOpts := modResult.CellOptions
	relayOpts := modResult.BootstrapOpts
	var opts []bootstrap.Option
	var provisional []kernellifecycle.ManagedResource
	if pgRes != nil {
		opts = append(opts, bootstrap.WithManagedResource(pgRes))
		provisional = append(provisional, pgRes)
	}
	rollback := func() {
		for _, v := range slices.Backward(provisional) {
			if closeErr := v.Close(ctx); closeErr != nil {
				slog.Warn("configcore: provisional rollback close failed",
					slog.Any("error", closeErr))
			}
		}
	}
	// Expose the pool through SharedDeps so AccessCoreModule + AuditCoreModule
	// can wire their own outbox.Writer + TxManager from the same pool in
	// postgres mode. In memory mode modResult.PGPool is nil — SharedPGPool
	// stays nil and the downstream modules skip the postgres outbox path.
	shared.SharedPGPool = modResult.PGPool

	// CAS protocol: declares the version-field name and conflict policy used by
	// all 6 CAS write paths in configcore (config Update/Delete/Rollback +
	// flag Update/Toggle/Delete). MustNewProtocol is the composition-root-only
	// constructor (CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest enforces this).
	casProto := cas.MustNewProtocol(cas.WithVersionField(configcore.VersionField))

	baseOpts := []configcore.Option{
		// Outbox wiring is provided by buildConfigCoreOpts (PG adapter includes
		// the transactional writer; memory adapter passes writer=nil).
		configcore.WithClock(shared.Clock),
		configcore.WithCursorCodec(cursorCodec),
		configcore.WithMetricsProvider(shared.PromStack.metricProvider),
		configcore.WithConfigEventCollector(shared.ConfigEventCollector),
		configcore.WithCASProtocol(casProto),
	}
	c := configcore.NewConfigCore(append(baseOpts, cellOpts...)...) //archtest:allow:clock-injection:via-slice WithClock in baseOpts

	// Register Vault diagnostics when the KeyProvider exposes them.
	if err := registerKeyProviderMetrics(kp, shared); err != nil {
		shared.SharedPGPool = nil
		rollback()
		return nil, nil, nil, fmt.Errorf("configcore: register key provider metrics: %w", err)
	}

	// Relay opts: in postgres mode, relayOpts contains WithManagedResource(relay)
	// so the relay worker is independently managed by bootstrap (Worker/Close/Checkers).
	opts = append(opts, relayOpts...)
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

type keyProviderMetricsProvider interface {
	Metrics() []prom.Collector
}

// registerKeyProviderMetrics registers the current KeyProvider's diagnostics.
// These collectors may close over provider instance state (GaugeFunc), so a
// repeated Provide against the same SharedDeps must replace the previous owned
// collector set instead of reusing or silently ignoring duplicates.
func registerKeyProviderMetrics(kp kcrypto.KeyProvider, shared *SharedDeps) error {
	return replaceRegisteredCollectors(
		shared.PromStack.registry,
		&shared.keyProviderMetricCollectors,
		keyProviderMetricCollectors(kp),
		"key provider metric",
	)
}

func keyProviderMetricCollectors(kp kcrypto.KeyProvider) []prom.Collector {
	if mp, ok := kp.(keyProviderMetricsProvider); ok {
		return mp.Metrics()
	}
	rmp, ok := kp.(renewalMetricsProvider)
	if !ok {
		return nil
	}
	return rmp.RenewalMetrics()
}

func replaceRegisteredCollectors(reg prom.Registerer, current *[]prom.Collector, next []prom.Collector, label string) error {
	previous := append([]prom.Collector(nil), (*current)...)
	unregisterCollectors(reg, previous)

	registered, err := registerCollectorSet(reg, next, label)
	if err != nil {
		unregisterCollectors(reg, registered)
		if _, restoreErr := registerCollectorSet(reg, previous, label+" restore"); restoreErr != nil {
			return fmt.Errorf("%w (also failed to restore previous collectors: %w)", err, restoreErr)
		}
		return err
	}

	*current = registered
	return nil
}

func registerCollectorSet(reg prom.Registerer, collectors []prom.Collector, label string) ([]prom.Collector, error) {
	registered := make([]prom.Collector, 0, len(collectors))
	for _, col := range collectors {
		if err := reg.Register(col); err != nil {
			return registered, fmt.Errorf("%s: %w", label, err)
		}
		registered = append(registered, col)
	}
	return registered, nil
}

func unregisterCollectors(reg prom.Registerer, collectors []prom.Collector) {
	for _, col := range collectors {
		reg.Unregister(col)
	}
}

// registerOrReuseCounter registers a new counter with the given opts. If the
// counter is already registered (AlreadyRegisteredError), it reuses the
// existing collector. Any other registration error is returned as-is.
func registerOrReuseCounter(reg prom.Registerer, opts prom.CounterOpts) (prom.Counter, error) {
	c := prom.NewCounter(opts)
	if err := reg.Register(c); err != nil {
		var are prom.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return nil, err
		}
		if existing, ok2 := are.ExistingCollector.(prom.Counter); ok2 {
			return existing, nil
		}
		return nil, fmt.Errorf("existing collector is not a Counter: %w", err)
	}
	return c, nil
}
