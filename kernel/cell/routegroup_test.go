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

func TestRouteGroup_ZeroValue(t *testing.T) {
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
}

func TestRouteGroupContributor(t *testing.T) {
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
}

func TestRouteGroupContributor_EmptySlice(t *testing.T) {
	t.Parallel()

	contrib := &testContributor{groups: nil}
	got := contrib.RouteGroups()
	// Nil return is valid; no panic.
	if len(got) != 0 {
		t.Errorf("expected 0 groups, got %d", len(got))
	}
}
