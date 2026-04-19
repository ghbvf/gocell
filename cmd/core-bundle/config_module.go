package main

import (
	"context"
	"fmt"

	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// ConfigCoreModule wires config-core: KeyProvider → ValueTransformer →
// PGResource/cellOpts (storage-backend specific) → configcore.ConfigCore.
//
// ref: uber-go/fx fx.Module("config-core", ...) — self-contained module.
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type ConfigCoreModule struct{}

// ID returns the stable identifier used in error messages.
func (ConfigCoreModule) ID() string { return "config-core" }

// Provide resolves all config-core-specific dependencies and returns the
// constructed cell and any bootstrap.Options (e.g. WithManagedResource).
func (ConfigCoreModule) Provide(ctx context.Context, sharedProv bootstrap.SharedDepsProvider) (cell.Cell, []bootstrap.Option, error) {
	s, ok := sharedProv.(*SharedDeps)
	if !ok {
		return nil, nil, fmt.Errorf("config-core: expected *SharedDeps, got %T", sharedProv)
	}

	kp, err := buildKeyProvider(s.Topology.StorageBackend)
	if err != nil {
		return nil, nil, fmt.Errorf("config-core key provider: %w", err)
	}
	vt := keyProviderToTransformer(kp)

	pgRes, cellOpts, err := buildConfigCoreOpts(ctx, s.Topology, s.EventBus, s.PromStack.metricProvider, vt)
	if err != nil {
		return nil, nil, err
	}

	baseOpts := []configcore.Option{
		configcore.WithPublisher(s.EventBus),
		configcore.WithCursorCodec(s.CursorCodecs.config),
	}
	if vt != nil {
		baseOpts = append(baseOpts, configcore.WithValueTransformer(vt))
	}
	c := configcore.NewConfigCore(append(baseOpts, cellOpts...)...)

	var opts []bootstrap.Option
	if pgRes != nil {
		opts = append(opts, bootstrap.WithManagedResource(pgRes))
	}
	return c, opts, nil
}

var _ bootstrap.CellModule = ConfigCoreModule{}
