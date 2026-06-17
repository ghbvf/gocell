package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cell"
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
			b := &Bootstrap{frameworkServedContractIDs: tc.expected, frameworkServingRoutes: tc.routes}
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
	b := &Bootstrap{
		frameworkServedContractIDs: []string{"http.devicestate.v1"},
		frameworkServingRoutes:     []FrameworkServedRoute{r},
	}
	require.Error(t, b.validateFrameworkServing())
}

// TestValidateFrameworkServing_NilRegister rejects a route whose RouteGroup has
// a nil Register (a programmer error that would otherwise surface only at mount).
func TestValidateFrameworkServing_NilRegister(t *testing.T) {
	t.Parallel()
	r := FrameworkServedRoute{ContractID: "http.devicestate.v1", Group: cell.RouteGroup{Listener: cell.PrimaryListener}}
	b := &Bootstrap{
		frameworkServedContractIDs: []string{"http.devicestate.v1"},
		frameworkServingRoutes:     []FrameworkServedRoute{r},
	}
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
