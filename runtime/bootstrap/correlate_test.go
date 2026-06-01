package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/correlation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/audit/ledger/storetest"
	"github.com/ghbvf/gocell/runtime/observability/correlate"
)

// bootstrapCorrelateService builds a minimal *correlate.Service for bootstrap tests.
func bootstrapCorrelateService(t *testing.T) *correlate.Service {
	t.Helper()
	proto := storetest.NewTestProtocol(t)
	store, err := ledger.NewMemStore(proto, clock.Real())
	require.NoError(t, err, "NewMemStore")
	svc, err := correlate.NewService(store, correlation.Topology{}, nil)
	require.NoError(t, err, "NewService")
	return svc
}

// TestWithCorrelateRoutes_NilNoop verifies that passing nil to WithCorrelateRoutes
// is a silent noop: b.correlateSvc stays nil and no panic occurs.
func TestWithCorrelateRoutes_NilNoop(t *testing.T) {
	t.Parallel()
	b := New(clock.Real(), WithCorrelateRoutes(nil))
	assert.Nil(t, b.correlateSvc, "nil svc must leave correlateSvc unset")
}

// TestWithCorrelateRoutes_SetsSvc verifies that a non-nil svc is stored.
func TestWithCorrelateRoutes_SetsSvc(t *testing.T) {
	t.Parallel()
	svc := bootstrapCorrelateService(t)
	b := New(clock.Real(), WithCorrelateRoutes(svc))
	assert.Same(t, svc, b.correlateSvc, "non-nil svc must be stored on Bootstrap")
}

// TestPhase5CollectRouteGroups_AppendsCorrelate verifies that when correlateSvc is
// non-nil, phase5CollectRouteGroups appends one RouteGroup targeting InternalListener.
func TestPhase5CollectRouteGroups_AppendsCorrelate(t *testing.T) {
	t.Parallel()
	b := New(clock.Real())
	s := buildPhase5State(t)

	// Baseline: no correlate svc — count the route groups without it.
	withoutCorrelate := b.phase5CollectRouteGroups(s)
	baselineCount := len(withoutCorrelate)

	// Install correlate svc.
	svc := bootstrapCorrelateService(t)
	b.correlateSvc = svc
	withCorrelate := b.phase5CollectRouteGroups(s)

	// Should have exactly one extra group.
	assert.Len(t, withCorrelate, baselineCount+1,
		"installing correlate svc must append exactly one route group")

	// Verify the new group targets InternalListener.
	var found bool
	for _, rg := range withCorrelate {
		if rg.Listener == cell.InternalListener {
			found = true
		}
	}
	require.True(t, found, "correlate RouteGroup must target InternalListener")
}

// TestPhase5CollectRouteGroups_NoCorrelate verifies that the baseline state
// (no correlateSvc) produces no InternalListener groups.
func TestPhase5CollectRouteGroups_NoCorrelate(t *testing.T) {
	t.Parallel()
	b := New(clock.Real())
	// b.correlateSvc is nil by default.
	s := buildPhase5State(t)

	groups := b.phase5CollectRouteGroups(s)

	// In the baseline state (no cells declare internal routes, no correlateSvc),
	// no InternalListener groups should be present.
	for _, rg := range groups {
		assert.NotEqual(t, cell.InternalListener, rg.Listener,
			"no InternalListener groups expected in baseline without correlate svc")
	}
}
