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

// countListener returns how many groups target the given listener.
func countListener(groups []cell.RouteGroup, ref cell.ListenerRef) int {
	n := 0
	for _, rg := range groups {
		if rg.Listener == ref {
			n++
		}
	}
	return n
}

// TestPhase5CollectRouteGroups_AppendsCorrelate verifies that when correlateSvc is
// non-nil, phase5CollectRouteGroups appends exactly one RouteGroup, and that the
// added group targets PrimaryListener (the correlate endpoint is admin-role-gated
// on /api/v1/observability/correlate, issue #1048; it is NOT on InternalListener).
func TestPhase5CollectRouteGroups_AppendsCorrelate(t *testing.T) {
	t.Parallel()
	b := New(clock.Real())
	s := buildPhase5State(t)

	// Baseline: no correlate svc — count the route groups without it.
	withoutCorrelate := b.phase5CollectRouteGroups(s)
	baselineCount := len(withoutCorrelate)
	baselinePrimary := countListener(withoutCorrelate, cell.PrimaryListener)

	// Install correlate svc.
	svc := bootstrapCorrelateService(t)
	b.correlateSvc = svc
	withCorrelate := b.phase5CollectRouteGroups(s)

	// Should have exactly one extra group.
	assert.Len(t, withCorrelate, baselineCount+1,
		"installing correlate svc must append exactly one route group")

	// The added group must target PrimaryListener.
	assert.Equal(t, baselinePrimary+1, countListener(withCorrelate, cell.PrimaryListener),
		"correlate RouteGroup must target PrimaryListener")
}

// TestPhase5CollectRouteGroups_CorrelateNotInternal is a regression guard for
// issue #1048: the correlate endpoint moved off /internal/v1/* (which requires a
// cell-caller Clients allowlist and crashed startup) onto PrimaryListener behind
// an admin-role Policy. It must never target InternalListener again.
func TestPhase5CollectRouteGroups_CorrelateNotInternal(t *testing.T) {
	t.Parallel()
	b := New(clock.Real())
	b.correlateSvc = bootstrapCorrelateService(t)
	s := buildPhase5State(t)

	groups := b.phase5CollectRouteGroups(s)

	for _, rg := range groups {
		assert.NotEqual(t, cell.InternalListener, rg.Listener,
			"correlate must not target InternalListener (would require a Clients allowlist; #1048)")
	}
}
