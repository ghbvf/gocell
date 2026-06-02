// Package configcore is the platform composition module for the configcore Cell.
// It implements [composition.CellModule] and wires all configcore-specific
// dependencies from [composition.SharedDeps].
//
// This is a composition-root-layer package: it may import cells/, adapters/,
// and cellmodules/cellsecrets/. It must NOT be imported by cells/, runtime/, or
// adapters/.
//
// # Key provider routing
//
// The configcore key provider is constructed in cmd/corebundle (which may import
// adapters/vault and github.com/prometheus/client_golang) and passed via
// composition.SharedDeps.ConfigKeyProvider, so cellmodules/configcore never
// imports those adapter-specific packages. This is the last cmd-supplied
// configcore dependency; its removal is gated on #885 (vault TransitMetrics →
// kernel MetricsProvider). The stale-cipher and eventbus-cache collectors were
// moved here (#1413): they route through the kernel MetricsProvider, so this
// module self-builds them from SharedDeps.MetricsProvider via
// runtime/observability/metrics without importing client_golang.
//
// ref: uber-go/fx fx.Module("configcore", ...) — self-contained module.
package configcore

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	configcell "github.com/ghbvf/gocell/cells/configcore"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/composition"
	"github.com/ghbvf/gocell/runtime/crypto"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// ModuleOption configures a configcore module.
type ModuleOption func(*module)

// WithKeyProviderOverride injects a pre-built KeyProvider, bypassing env-based
// construction. Used by tests to inject a fake KeyProvider.
func WithKeyProviderOverride(kp kcrypto.KeyProvider) ModuleOption {
	return func(m *module) {
		m.keyProviderOverride = kp
	}
}

type module struct {
	keyProviderOverride kcrypto.KeyProvider
}

// Module returns a composition.CellModule that wires the configcore Cell.
//
// Pass [WithKeyProviderOverride] in tests that inject a fake KeyProvider.
// The key provider and stale-cipher callback are read from
// composition.SharedDeps (populated by cmd/corebundle).
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
// constructed cell, non-resource bootstrap options, and the single-source
// ManagedResource list (Builder derives WithManagedResource + rollback from it).
func (m *module) Provide(
	_ context.Context, shared *composition.SharedDeps,
) (composition.ModuleResult, error) {
	// 1. Cursor codec.
	cfgPrimary, cfgPrevious := cellsecrets.LoadCursorKeys("CONFIGCORE")
	cursorCodec, err := cellsecrets.BuildCursorCodec(cellsecrets.CursorCodecConfig{
		AdapterMode: shared.Topology.AdapterMode(),
		EnvName:     "GOCELL_CONFIGCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_CONFIGCORE_CURSOR_PREVIOUS_KEY",
		Primary:     cfgPrimary,
		Previous:    cfgPrevious,
		DevDefault:  "corebundle-cfg-cursor-key--32bb!",
		Label:       "config",
	})
	if err != nil {
		return composition.ModuleResult{}, fmt.Errorf("configcore cursor codec: %w", err)
	}

	// 2. KeyProvider (test override OR cmd-supplied via SharedDeps).
	kp := m.resolveKeyProvider(shared)
	vt, err := resolveValueTransformer(kp, shared.Topology.StorageBackend() == "postgres")
	if err != nil {
		return composition.ModuleResult{}, err
	}

	// 3. Config observability collectors — self-built from the shared kernel
	// MetricsProvider (#1413). These route through the kernel Provider (not raw
	// github.com/prometheus/client_golang), so configcore owns them without
	// tripping the "no client_golang in cellmodules" posture. Metric names are
	// unchanged (gocell_config_stale_cipher_total / gocell_eventbus_cache_*).
	staleCipherCollector, err := obmetrics.NewProviderConfigStaleCipherCollector(shared.MetricsProvider)
	if err != nil {
		return composition.ModuleResult{}, fmt.Errorf("configcore stale-cipher collector: %w", err)
	}
	ebcCollector, err := obmetrics.NewProviderEventbusCacheCollector(shared.MetricsProvider)
	if err != nil {
		return composition.ModuleResult{}, fmt.Errorf("configcore eventbus-cache collector: %w", err)
	}

	// 4. PG storage and cell options.
	modResult, err := buildConfigCoreOpts(shared.Clock, configCoreModuleConfig{
		topology:         shared.Topology,
		pg:               shared.PG,
		publisher:        shared.EventBus,
		metricsProvider:  shared.MetricsProvider,
		valueTransformer: vt,
		onStaleCipher: func(_, _, _ string) {
			// onStaleCipher fires deep in the PG repo read path with no request
			// context, so a background ctx is intentional here: this is an
			// unlabeled global counter with no per-request trace correlation.
			staleCipherCollector.RecordStaleCipher(context.Background())
		},
	})
	if err != nil {
		return composition.ModuleResult{}, err
	}

	// 5. CAS protocol (CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest).
	casProto, err := newConfigCoreCASProtocol()
	if err != nil {
		return composition.ModuleResult{}, err
	}

	baseOpts := []configcell.Option{
		configcell.WithCursorCodec(cursorCodec),
		configcell.WithMetricsProvider(shared.MetricsProvider),
		configcell.WithConfigEventCollector(shared.ConfigEventCollector),
		configcell.WithEventbusCacheCollector(ebcCollector),
		configcell.WithCASProtocol(casProto),
	}
	baseOpts = append(baseOpts, modResult.cellOptions...)
	c := configcell.NewConfigCore(shared.Clock, baseOpts...)

	builtCell, opts, res := buildConfigCoreResult(c, kp, modResult)
	return composition.ModuleResult{Cell: builtCell, Opts: opts, Resources: res}, nil
}

// resolveKeyProvider returns the test override when set, otherwise uses the
// key provider injected via composition.SharedDeps by cmd/corebundle.
func (m *module) resolveKeyProvider(shared *composition.SharedDeps) kcrypto.KeyProvider {
	if m.keyProviderOverride != nil {
		return m.keyProviderOverride
	}
	return shared.ConfigKeyProvider
}

// resolveValueTransformer maps a KeyProvider to its ValueTransformer. A nil
// provider is permitted only when config values are not persisted to disk
// (memory storage), where it resolves to an explicit NoopTransformer (no
// encryption). With postgres storage a nil provider is rejected fail-closed:
// persisting config values unencrypted is a security fault, not a silent
// fallback. (postgres storage implies real adapter mode via the Topology
// coupling rule, so this is the production-persistence guard.)
//
// This is the sole sanctioned construction site for crypto.NoopTransformer{},
// guarded by archtest CONFIG-NOOP-TRANSFORMER-FUNNEL-01 — the explicit branch
// makes the no-encryption path greppable and the postgres rejection closes the
// silent-plaintext-persistence hole (F3).
func resolveValueTransformer(kp kcrypto.KeyProvider, postgresStorage bool) (kcrypto.ValueTransformer, error) {
	if kp == nil {
		if postgresStorage {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"configcore: postgres storage requires a ConfigKeyProvider; NoopTransformer "+
					"is dev-only (refusing to persist config values unencrypted)")
		}
		return crypto.NoopTransformer{}, nil
	}
	return crypto.NewValueTransformer(kp), nil
}

// newConfigCoreCASProtocol builds the CAS protocol used by configcore.
func newConfigCoreCASProtocol() (*cas.Protocol, error) {
	p, err := cas.NewProtocol(cas.WithVersionField(configcell.VersionField))
	if err != nil {
		return nil, fmt.Errorf("configcore cas protocol: %w", err)
	}
	return p, nil
}

var _ composition.CellModule = (*module)(nil)
