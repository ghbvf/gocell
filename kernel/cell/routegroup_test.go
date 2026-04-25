package cell_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
)

// testContributor is a test-local implementation of RouteGroupContributor.
type testContributor struct {
	groups []cell.RouteGroup
}

func (c *testContributor) RouteGroups() []cell.RouteGroup { return c.groups }

// TestRouteGroup_ZeroValueFields verifies the zero RouteGroup has zero-value
// Listener / Policy and a nil Register.
func TestRouteGroup_ZeroValueFields(t *testing.T) {
	t.Parallel()
	var rg cell.RouteGroup
	if !rg.Listener.IsZero() {
		t.Error("zero RouteGroup.Listener.IsZero() should be true")
	}
	if !rg.Policy.IsZero() {
		t.Error("zero RouteGroup.Policy should be zero value")
	}
	if rg.Register != nil {
		t.Error("zero RouteGroup.Register should be nil")
	}
}

// TestRouteGroup_ContributorReturnsDeclaredGroups verifies a Contributor
// returns the exact slice it was constructed with, in order.
func TestRouteGroup_ContributorReturnsDeclaredGroups(t *testing.T) {
	t.Parallel()
	groups := []cell.RouteGroup{
		{
			Listener: cell.PrimaryListener,
			Prefix:   "/api/v1/foo",
			Register: func(mux cell.RouteMux) {},
		},
		{
			Listener: cell.InternalListener,
			Prefix:   "/internal/v1/foo",
			Register: func(mux cell.RouteMux) {},
		},
	}

	contrib := &testContributor{groups: groups}

	// Compile-time interface check.
	var _ cell.RouteGroupContributor = contrib

	got := contrib.RouteGroups()
	if len(got) != 2 {
		t.Fatalf("RouteGroups() returned %d groups, want 2", len(got))
	}
	if got[0].Listener.String() != "primary" {
		t.Errorf("group[0].Listener = %q, want %q", got[0].Listener.String(), "primary")
	}
	if got[1].Listener.String() != "internal" {
		t.Errorf("group[1].Listener = %q, want %q", got[1].Listener.String(), "internal")
	}
}

// TestRouteGroup_ContributorNilSliceAllowed verifies a Contributor returning
// a nil slice is valid (no panic, length 0).
func TestRouteGroup_ContributorNilSliceAllowed(t *testing.T) {
	t.Parallel()
	contrib := &testContributor{groups: nil}
	got := contrib.RouteGroups()
	if len(got) != 0 {
		t.Errorf("expected 0 groups, got %d", len(got))
	}
}

// TestRouteGroup_FieldCombinations is the table-driven case sweep for
// RouteGroup field combinations (TEST-06).
func TestRouteGroup_FieldCombinations(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rg       cell.RouteGroup
		wantRef  string
		wantPfx  string
		wantNilR bool
	}{
		{
			name:     "primary_listener_with_prefix",
			rg:       cell.RouteGroup{Listener: cell.PrimaryListener, Prefix: "/api/v1/x", Register: func(cell.RouteMux) {}},
			wantRef:  "primary",
			wantPfx:  "/api/v1/x",
			wantNilR: false,
		},
		{
			name:     "internal_listener_with_prefix",
			rg:       cell.RouteGroup{Listener: cell.InternalListener, Prefix: "/internal/v1/y", Register: func(cell.RouteMux) {}},
			wantRef:  "internal",
			wantPfx:  "/internal/v1/y",
			wantNilR: false,
		},
		{
			name:     "health_listener_no_prefix",
			rg:       cell.RouteGroup{Listener: cell.HealthListener, Prefix: "", Register: func(cell.RouteMux) {}},
			wantRef:  "health",
			wantPfx:  "",
			wantNilR: false,
		},
		{
			name:     "nil_register_is_programmer_error_value",
			rg:       cell.RouteGroup{Listener: cell.PrimaryListener, Prefix: "/api/v1/z"},
			wantRef:  "primary",
			wantPfx:  "/api/v1/z",
			wantNilR: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertRouteGroupFields(t, tc.rg, tc.wantRef, tc.wantPfx, tc.wantNilR)
		})
	}
}

// assertRouteGroupFields compares one RouteGroup against expected
// listener-string / prefix / Register-nilness. Extracted so the table-driven
// case body has trivial cognitive complexity.
func assertRouteGroupFields(t *testing.T, rg cell.RouteGroup, wantRef, wantPfx string, wantNilR bool) {
	t.Helper()
	if rg.Listener.String() != wantRef {
		t.Errorf("Listener = %q, want %q", rg.Listener.String(), wantRef)
	}
	if rg.Prefix != wantPfx {
		t.Errorf("Prefix = %q, want %q", rg.Prefix, wantPfx)
	}
	if (rg.Register == nil) != wantNilR {
		t.Errorf("Register nil = %v, want %v", rg.Register == nil, wantNilR)
	}
}

// TestRouteGroup_SingleGroupConstructor covers the DX-05 SingleGroup helper.
func TestRouteGroup_SingleGroupConstructor(t *testing.T) {
	t.Parallel()
	rg := cell.SingleGroup(cell.PrimaryListener, "/api/v1/sg", func(cell.RouteMux) {})
	if rg.Listener.String() != "primary" {
		t.Errorf("SingleGroup Listener = %q, want primary", rg.Listener.String())
	}
	if rg.Prefix != "/api/v1/sg" {
		t.Errorf("SingleGroup Prefix = %q, want /api/v1/sg", rg.Prefix)
	}
	if rg.Register == nil {
		t.Error("SingleGroup Register must not be nil")
	}
}
