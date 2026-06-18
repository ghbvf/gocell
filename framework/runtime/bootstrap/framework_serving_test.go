package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
)

// fwRoute builds a well-formed FrameworkServedRoute for the given contract id
// (empty CellID, non-nil Register, PrimaryListener) — the happy shape so the
// table cases can perturb exactly one field.
func fwRoute(contractID string) FrameworkServedRoute {
	return FrameworkServedRoute{
		ContractID: contractID,
		Group: cell.RouteGroup{
			Listener: cell.PrimaryListener,
			Register: func(cell.RouteMux) error { return nil },
		},
	}
}

// fwBootstrap builds a Bootstrap whose must-serve EXPECTATION rides on the
// assembly (assembly.Config.FrameworkContracts) — mirroring production, where
// the expected set comes from buildAssembly(generatedFrameworkServedContracts())
// not from the WithFrameworkHTTPServing option. The assembly's eager hook
// dispatcher is drained via t.Cleanup so the unit test leaks no goroutine.
func fwBootstrap(t *testing.T, expected []string, routes []FrameworkServedRoute) *Bootstrap {
	t.Helper()
	asm := assembly.New(clock.Real(), assembly.Config{ID: "fwtest", FrameworkContracts: expected})
	t.Cleanup(asm.Shutdown)
	return &Bootstrap{assemblyCore: asm, frameworkServingRoutes: routes}
}

func TestValidateFrameworkServing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		expected []string
		routes   []FrameworkServedRoute
		wantErr  bool
	}{
		{
			name:     "no framework serving — nil/nil passes",
			expected: nil,
			routes:   nil,
			wantErr:  false,
		},
		{
			name:     "expected matches wired — passes",
			expected: []string{"http.devicestate.v1"},
			routes:   []FrameworkServedRoute{fwRoute("http.devicestate.v1")},
			wantErr:  false,
		},
		{
			name:     "declared active but unwired — fail-closed (DEAD-CONTRACT analog)",
			expected: []string{"http.devicestate.v1"},
			routes:   nil,
			wantErr:  true,
		},
		{
			name:     "wired but undeclared — fail-closed (stale wiring)",
			expected: nil,
			routes:   []FrameworkServedRoute{fwRoute("http.devicestate.v1")},
			wantErr:  true,
		},
		{
			name:     "partial mismatch — one matched, one missing",
			expected: []string{"http.devicestate.v1", "http.deviceidentity.status.v1"},
			routes:   []FrameworkServedRoute{fwRoute("http.devicestate.v1")},
			wantErr:  true,
		},
		{
			name:     "empty ContractID — rejected",
			expected: []string{""},
			routes:   []FrameworkServedRoute{fwRoute("")},
			wantErr:  true,
		},
		{
			name:     "duplicate route for one contract — rejected",
			expected: []string{"http.devicestate.v1"},
			routes:   []FrameworkServedRoute{fwRoute("http.devicestate.v1"), fwRoute("http.devicestate.v1")},
			wantErr:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := fwBootstrap(t, tc.expected, tc.routes)
			err := b.validateFrameworkServing()
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestValidateFrameworkServing_NonEmptyCellID rejects a framework-served route
// that carries a CellID (framework-owned contracts have no cell).
func TestValidateFrameworkServing_NonEmptyCellID(t *testing.T) {
	t.Parallel()
	r := fwRoute("http.devicestate.v1")
	r.Group.CellID = "syscore"
	b := fwBootstrap(t, []string{"http.devicestate.v1"}, []FrameworkServedRoute{r})
	require.Error(t, b.validateFrameworkServing())
}

// TestValidateFrameworkServing_NilRegister rejects a route whose RouteGroup has
// a nil Register (a programmer error that would otherwise surface only at mount).
func TestValidateFrameworkServing_NilRegister(t *testing.T) {
	t.Parallel()
	r := FrameworkServedRoute{ContractID: "http.devicestate.v1", Group: cell.RouteGroup{Listener: cell.PrimaryListener}}
	b := fwBootstrap(t, []string{"http.devicestate.v1"}, []FrameworkServedRoute{r})
	require.Error(t, b.validateFrameworkServing())
}

// TestFrameworkServingRouteGroups_CollectsWithEmptyCellID confirms the collected
// groups preserve the framework-owned empty CellID for phase5 mounting.
func TestFrameworkServingRouteGroups_CollectsWithEmptyCellID(t *testing.T) {
	t.Parallel()
	b := &Bootstrap{frameworkServingRoutes: []FrameworkServedRoute{fwRoute("http.devicestate.v1")}}
	groups := b.frameworkServingRouteGroups()
	require.Len(t, groups, 1)
	assert.Empty(t, groups[0].CellID)
	assert.Equal(t, cell.PrimaryListener, groups[0].Listener)

	assert.Nil(t, (&Bootstrap{}).frameworkServingRouteGroups())
}
