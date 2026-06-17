package bootstrap

// deployment_topology_specforrole_test.go — tests for SpecForRole, the
// groups-graph → per-process DeploymentTopologySpec bridge (#1423 PR-1).

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// TestSpecForRole_EmptyRoleIsColocatedMonolith verifies that an empty role
// selects the all-colocated monolith (zero spec) regardless of the declared
// group graph — the single-binary deployment #1423 preserves.
func TestSpecForRole_EmptyRoleIsColocatedMonolith(t *testing.T) {
	cases := []struct {
		name   string
		groups []TopologyGroup
	}{
		{name: "no groups", groups: nil},
		{
			name: "multi-group graph, no role selected",
			groups: []TopologyGroup{
				{Role: "core", Cells: []string{"alpha"}, Endpoint: "core.svc:9000"},
				{Role: "edge", Cells: []string{"beta"}, Endpoint: "edge.svc:9001"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := SpecForRole(tc.groups, "")
			if err != nil {
				t.Fatalf("SpecForRole(_, \"\") unexpected error: %v", err)
			}
			if len(spec.Colocated) != 0 || len(spec.Remote) != 0 {
				t.Fatalf("empty role must yield the zero (all-colocated) spec, got %+v", spec)
			}
			// The zero spec seals to an all-colocated DeploymentTopology.
			topo, err := NewDeploymentTopology(spec)
			if err != nil {
				t.Fatalf("NewDeploymentTopology(zero spec): %v", err)
			}
			if !topo.IsColocated("anything") {
				t.Fatalf("all-colocated default must report IsColocated()==true")
			}
		})
	}
}

// TestSpecForRole_NonEmptyRoleFailsClosed verifies that role-based derivation is
// fail-closed in PR-1 (it lands in PR-2 with GOCELL_CELL_ROLE) — a non-empty role
// returns an error rather than silently mounting the monolith.
func TestSpecForRole_NonEmptyRoleFailsClosed(t *testing.T) {
	groups := []TopologyGroup{
		{Role: "core", Cells: []string{"alpha"}, Endpoint: "core.svc:9000"},
		{Role: "edge", Cells: []string{"beta"}, Endpoint: "edge.svc:9001"},
	}
	_, err := SpecForRole(groups, "core")
	if err == nil {
		t.Fatal("non-empty role must fail closed in PR-1, got nil error")
	}
	// fail-closed must be a typed errcode (ErrValidationFailed), not a bare error —
	// the error type/code is the contract callers branch on.
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("fail-closed error must be *errcode.Error, got %T", err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Fatalf("fail-closed error code = %v, want ErrValidationFailed", ec.Code)
	}
}
