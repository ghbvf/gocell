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

// TestRouteGroup consolidates all RouteGroup and RouteGroupContributor tests
// into a single table-driven test function (TEST-06).
func TestRouteGroup(t *testing.T) {
	t.Parallel()

	t.Run("zero_value_fields", func(t *testing.T) {
		t.Parallel()
		var rg cell.RouteGroup
		if !rg.Listener.IsZero() {
			t.Error("zero RouteGroup.Listener.IsZero() should be true")
		}
		if rg.Policy != nil {
			t.Error("zero RouteGroup.Policy should be nil")
		}
		if rg.Register != nil {
			t.Error("zero RouteGroup.Register should be nil")
		}
	})

	t.Run("contributor_returns_declared_groups", func(t *testing.T) {
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

		// Verify interface is satisfied.
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
	})

	t.Run("contributor_nil_slice_allowed", func(t *testing.T) {
		t.Parallel()
		contrib := &testContributor{groups: nil}
		got := contrib.RouteGroups()
		// Nil return is valid; no panic.
		if len(got) != 0 {
			t.Errorf("expected 0 groups, got %d", len(got))
		}
	})

	// Table-driven cases covering RouteGroup field combinations.
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
			if tc.rg.Listener.String() != tc.wantRef {
				t.Errorf("Listener = %q, want %q", tc.rg.Listener.String(), tc.wantRef)
			}
			if tc.rg.Prefix != tc.wantPfx {
				t.Errorf("Prefix = %q, want %q", tc.rg.Prefix, tc.wantPfx)
			}
			if (tc.rg.Register == nil) != tc.wantNilR {
				t.Errorf("Register nil = %v, want %v", tc.rg.Register == nil, tc.wantNilR)
			}
		})
	}

	// SingleGroup convenience constructor (DX-05).
	t.Run("single_group_constructor", func(t *testing.T) {
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
	})
}
