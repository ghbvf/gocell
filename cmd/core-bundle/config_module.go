package main

import (
	"context"
	"fmt"

	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	prom "github.com/prometheus/client_golang/prometheus"
)

// ConfigCoreModule wires config-core: KeyProvider → ValueTransformer →
// PGResource/cellOpts (storage-backend specific) → configcore.ConfigCore.
//
// ref: uber-go/fx fx.Module("config-core", ...) — self-contained module.
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type ConfigCoreModule struct {
	// KeyProviderOverride bypasses env-based KeyProvider construction when
	// non-nil. Production code leaves this unset; tests use it to inject a
	// fake KeyProvider (e.g. one that also implements
	// kernel/lifecycle.ManagedResource) and assert wiring behaviour without
	// touching GOCELL_KEY_PROVIDER / GOCELL_MASTER_KEY / Vault.
	KeyProviderOverride kcrypto.KeyProvider
}

// ID returns the stable identifier used in error messages.
func (ConfigCoreModule) ID() string { return "config-core" }

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

// Provide resolves all config-core-specific dependencies and returns the
// constructed cell and any bootstrap.Options (e.g. WithManagedResource).
func (m ConfigCoreModule) Provide(ctx context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, error) {
	kp := m.KeyProviderOverride
	if kp == nil {
		var err error
		kp, err = buildKeyProvider(shared.Topology.StorageBackend, shared.Topology.AdapterMode, shared.KeyProviderName)
		if err != nil {
			return nil, nil, fmt.Errorf("config-core key provider: %w", err)
		}
	}
	vt := keyProviderToTransformer(kp)

	pgRes, cellOpts, err := buildConfigCoreOpts(ctx, shared.Topology, shared.EventBus, shared.PromStack.metricProvider, vt)
	if err != nil {
		return nil, nil, err
	}

	// Register the stale-cipher counter against the isolated per-run registry.
	// Use Register (not MustRegister) so that repeated Provide calls in the
	// same process (e.g. integration tests with shared registry) are handled
	// gracefully: AlreadyRegisteredError carries the existing collector so we
	// can reuse it instead of creating an orphaned counter.
	staleCipherCounter := prom.NewCounter(configStaleCipherOpts)
	if regErr := shared.PromStack.registry.Register(staleCipherCounter); regErr != nil {
		if are, ok := regErr.(prom.AlreadyRegisteredError); ok {
			if existing, ok2 := are.ExistingCollector.(prom.Counter); ok2 {
				staleCipherCounter = existing
			}
		}
		// Non-AlreadyRegisteredError: counter is still usable without registration;
		// metrics simply won't appear in /metrics scrapes for this run.
	}

	baseOpts := []configcore.Option{
		configcore.WithPublisher(shared.EventBus),
		configcore.WithCursorCodec(shared.CursorCodecs.config),
		configcore.WithOnStaleCipherMetric(staleCipherCounter),
	}
	if vt != nil {
		baseOpts = append(baseOpts, configcore.WithValueTransformer(vt))
	}
	c := configcore.NewConfigCore(append(baseOpts, cellOpts...)...)

	// A13: register vault token renewal counters when the KeyProvider exposes
	// them. Uses the same Register-not-MustRegister pattern as staleCipherCounter
	// so repeated Provide calls in integration tests are handled gracefully.
	registerRenewalMetrics(kp, shared.PromStack.registry)

	var opts []bootstrap.Option
	if pgRes != nil {
		opts = append(opts, bootstrap.WithManagedResource(pgRes))
	}
	// A19: when the KeyProvider opts into lifecycle.ManagedResource (today:
	// vault-transit via TransitKeyProvider.Checkers()["vault_transit_ready"]),
	// register it with bootstrap so its probes flow into /readyz. Local-aes
	// has no external dependency and does not implement the interface — it is
	// naturally skipped here; future backends (AWS-KMS, GCP-KMS) opt in by
	// implementing ManagedResource themselves.
	if kpRes, ok := kp.(kernellifecycle.ManagedResource); ok {
		opts = append(opts, bootstrap.WithManagedResource(kpRes))
	}
	return c, opts, nil
}

var _ CellModule = ConfigCoreModule{}

// renewalMetricsProvider is a local interface satisfied by vault.TransitKeyProvider
// (and any future KeyProvider that exposes Prometheus renewal metrics). Using an
// interface avoids importing the vault adapter package directly from config_module.go.
type renewalMetricsProvider interface {
	RenewalMetrics() []prom.Collector
}

// registerRenewalMetrics registers per-collector metrics exposed by KeyProvider
// implementations that satisfy renewalMetricsProvider. Uses Register (not
// MustRegister) so repeated Provide calls in integration tests are graceful:
// AlreadyRegisteredError and other non-fatal errors leave counters usable
// in memory while simply omitting them from /metrics scrapes.
func registerRenewalMetrics(kp kcrypto.KeyProvider, reg prom.Registerer) {
	rmp, ok := kp.(renewalMetricsProvider)
	if !ok {
		return
	}
	for _, col := range rmp.RenewalMetrics() {
		// Non-fatal: AlreadyRegisteredError is benign on integration-test re-run;
		// other errors leave the counter usable in memory only.
		//nolint:errcheck
		_ = reg.Register(col)
	}
}
