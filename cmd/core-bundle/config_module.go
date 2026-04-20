package main

import (
	"context"
	"fmt"

	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
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

// Provide resolves all config-core-specific dependencies and returns the
// constructed cell and any bootstrap.Options (e.g. WithManagedResource).
func (m ConfigCoreModule) Provide(ctx context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, error) {
	kp := m.KeyProviderOverride
	if kp == nil {
		var err error
		kp, err = buildKeyProvider(shared.Topology.StorageBackend, shared.Topology.AdapterMode)
		if err != nil {
			return nil, nil, fmt.Errorf("config-core key provider: %w", err)
		}
	}
	vt := keyProviderToTransformer(kp)

	pgRes, cellOpts, err := buildConfigCoreOpts(ctx, shared.Topology, shared.EventBus, shared.PromStack.metricProvider, vt)
	if err != nil {
		return nil, nil, err
	}

	baseOpts := []configcore.Option{
		configcore.WithPublisher(shared.EventBus),
		configcore.WithCursorCodec(shared.CursorCodecs.config),
	}
	if vt != nil {
		baseOpts = append(baseOpts, configcore.WithValueTransformer(vt))
	}
	c := configcore.NewConfigCore(append(baseOpts, cellOpts...)...)

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
