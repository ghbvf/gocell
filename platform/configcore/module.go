// Package configcore is the platform composition module for the configcore Cell.
// It implements [composition.CellModule] and wires all configcore-specific
// dependencies from [composition.SharedDeps].
//
// This is a composition-root-layer package: it may import cells/, adapters/,
// and platform/internal/. It must NOT be imported by cells/, runtime/, or
// adapters/.
//
// # Key provider and stale-cipher counter routing
//
// The configcore key provider and stale-cipher prometheus counter are
// constructed in cmd/corebundle (which may import adapters/vault and
// github.com/prometheus/client_golang). They are passed to this module via
// composition.SharedDeps.ConfigKeyProvider and
// composition.SharedDeps.ConfigStaleCipherInc so that platform/configcore
// never imports those adapter-specific packages.
//
// ref: uber-go/fx fx.Module("configcore", ...) — self-contained module.
package configcore

import (
	"context"
	"fmt"

	configcell "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/platform/platformshared"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
	"github.com/ghbvf/gocell/runtime/crypto"
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

	// 2. KeyProvider (test override OR cmd-supplied via SharedDeps).
	kp := m.resolveKeyProvider(shared)
	vt := keyProviderToTransformer(kp)

	// 3. Stale-cipher increment callback (supplied by cmd via SharedDeps).
	staleCipherInc := shared.ConfigStaleCipherInc
	if staleCipherInc == nil {
		staleCipherInc = func() {} // no-op fallback for tests without prom setup
	}

	// 4. PG storage and cell options.
	modResult, err := buildConfigCoreOpts(shared.Clock, configCoreModuleConfig{
		topology:         shared.Topology,
		pg:               shared.PG,
		publisher:        shared.EventBus,
		metricsProvider:  shared.MetricsProvider,
		valueTransformer: vt,
		onStaleCipher: func(_, _, _ string) {
			staleCipherInc()
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

// resolveKeyProvider returns the test override when set, otherwise uses the
// key provider injected via composition.SharedDeps by cmd/corebundle.
func (m *module) resolveKeyProvider(shared *composition.SharedDeps) kcrypto.KeyProvider {
	if m.keyProviderOverride != nil {
		return m.keyProviderOverride
	}
	return shared.ConfigKeyProvider
}

// keyProviderToTransformer wraps a KeyProvider in a ValueTransformer.
// A nil provider means "no key configured" — the NoopTransformer path.
func keyProviderToTransformer(kp kcrypto.KeyProvider) kcrypto.ValueTransformer {
	if kp == nil {
		return crypto.NoopTransformer{}
	}
	return crypto.NewValueTransformer(kp)
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
