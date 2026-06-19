package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// Three type-distinguishable ManagedResources (embedding fakeManagedResource so %T
// differs per role) used to observe the registration order runtimeBaseOptions
// produces. The accessor reports type names, so distinct types are required.
type (
	orderPoolMR   struct{ fakeManagedResource }
	orderBrokerMR struct{ fakeManagedResource }
	orderRelayMR  struct{ fakeManagedResource }
)

// TestDefaultRuntimeOptions_ResourceRegistrationOrder pins the corebundle teardown-safety
// contract (#2341 / #1940): runtimeBaseOptions must register managed resources in the order
// pools → broker → relays, so bootstrap's LIFO teardown closes them relays → broker → pools
// (each relay stops before the broker it publishes to and the pool it drains close). bootstrap's
// generic LIFO is tested in framework/runtime/bootstrap; this locks the composition-root append
// order that the generic mechanism relies on. A future edit that reorders the three appends in
// bundle_options.go turns this red.
func TestDefaultRuntimeOptions_ResourceRegistrationOrder(t *testing.T) {
	// Memory topology: no real PG pool and shared.Redis is nil, so the only managed
	// resources are the three fakes injected below.
	shared, locals := newValidatedSharedDepsAndLocals(t, mkTopo("real", "memory", false))

	poolFake := &orderPoolMR{}
	brokerFake := &orderBrokerMR{}
	relayFake := &orderRelayMR{}
	// Inject via the three slices the composition root appends, mirroring how
	// provisionPGInstance populates poolOpts/relayOpts and eventtransport populates
	// brokerResources.
	locals.poolOpts = []bootstrap.Option{bootstrap.WithManagedResource(poolFake)}
	locals.brokerResources = []kernellifecycle.ManagedResource{brokerFake}
	locals.relayOpts = []bootstrap.Option{bootstrap.WithManagedResource(relayFake)}

	opts := runtimeBaseOptions(shared, locals, nil, nil, nil, nil)
	b := newBootstrapFromOptions(shared.Clock, opts)

	require.Equal(t, []string{
		fmt.Sprintf("%T", poolFake),
		fmt.Sprintf("%T", brokerFake),
		fmt.Sprintf("%T", relayFake),
	}, b.ManagedResourceRegistrationOrder(),
		"managed resources must register pools → broker → relays (→ LIFO teardown relays → broker → pools)")
}
