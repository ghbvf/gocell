package bootstrap

// deployment_topology_specforrole_test.go — tests for SpecForRole, the
// groups-graph → per-process DeploymentTopologySpec bridge.
//
// PR-2 (#2278) replaces the PR-1 fail-closed placeholder with real role-based
// derivation:
//   - empty role + 0/1 group  → zero spec (all-colocated monolith)
//   - empty role + ≥2 groups  → fail-fast (declared a split but picked no role)
//   - role ∈ declared set     → Colocated = role's cells, Remote = other groups' cells × endpoint
//   - role ∉ declared set     → fail-fast

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// twoGroupGraph is the canonical split topology used across these tests:
// role "core" hosts alpha; role "edge" hosts beta + gamma.
func twoGroupGraph() []TopologyGroup {
	return []TopologyGroup{
		{Role: "core", Cells: []string{"alpha"}, Endpoint: "core.svc:9000"},
		{Role: "edge", Cells: []string{"beta", "gamma"}, Endpoint: "edge.svc:9001"},
	}
}

// TestSpecForRole_EmptyRoleColocatedMonolith verifies that an empty role selects
// the all-colocated monolith (zero spec) when 0 or 1 group is declared — the
// single-binary zero-migration default #1423 preserves.
func TestSpecForRole_EmptyRoleColocatedMonolith(t *testing.T) {
	cases := []struct {
		name   string
		groups []TopologyGroup
	}{
		{name: "no groups", groups: nil},
		{name: "empty groups slice", groups: []TopologyGroup{}},
		{
			name:   "single group, no role",
			groups: []TopologyGroup{{Role: "solo", Cells: []string{"alpha", "beta"}, Endpoint: "solo.svc:9000"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := SpecForRole(tc.groups, "")
			if err != nil {
				t.Fatalf("SpecForRole(_, \"\") unexpected error: %v", err)
			}
			if len(spec.Colocated) != 0 || len(spec.Remote) != 0 {
				t.Fatalf("empty role + 0/1 group must yield the zero (all-colocated) spec, got %+v", spec)
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

// TestSpecForRole_EmptyRoleMultiGroupFailsClosed verifies the PR-2 behavior
// reversal: a multi-group topology declares an intended split, so running it
// with no GOCELL_CELL_ROLE is a misconfiguration — fail-fast rather than
// silently mounting the monolith (which would mask the misconfig). 12-factor:
// the env is consumed or it errors.
func TestSpecForRole_EmptyRoleMultiGroupFailsClosed(t *testing.T) {
	_, err := SpecForRole(twoGroupGraph(), "")
	if err == nil {
		t.Fatal("empty role + ≥2 groups must fail closed, got nil error")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("fail-closed error must be *errcode.Error, got %T", err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Fatalf("fail-closed error code = %v, want ErrValidationFailed", ec.Code)
	}
}

// TestSpecForRole_RoleDerivesColocatedRemote verifies that a declared role
// derives Colocated = the role's cells and Remote = every OTHER group's cells
// mapped to that group's endpoint.
func TestSpecForRole_RoleDerivesColocatedRemote(t *testing.T) {
	spec, err := SpecForRole(twoGroupGraph(), "core")
	if err != nil {
		t.Fatalf("SpecForRole(_, \"core\") unexpected error: %v", err)
	}

	// Colocated = core's cells.
	if got := asSet(spec.Colocated); !got["alpha"] || len(got) != 1 {
		t.Fatalf("Colocated = %v, want {alpha}", spec.Colocated)
	}

	// Remote = edge's cells × edge endpoint.
	remote := map[string]string{}
	for _, r := range spec.Remote {
		remote[r.CellID] = r.Endpoint
	}
	if len(remote) != 2 || remote["beta"] != "edge.svc:9001" || remote["gamma"] != "edge.svc:9001" {
		t.Fatalf("Remote = %+v, want beta+gamma → edge.svc:9001", spec.Remote)
	}
}

// TestSpecForRole_SplitSpecSealsConsumable closes the derive→consume loop: the
// derived split spec must seal into a DeploymentTopology whose IsColocated /
// RemoteEndpoint queries are correct — i.e. the value celltransport.Resolve
// actually consumes routes the right cells in/out of process.
func TestSpecForRole_SplitSpecSealsConsumable(t *testing.T) {
	spec, err := SpecForRole(twoGroupGraph(), "core")
	if err != nil {
		t.Fatalf("SpecForRole: %v", err)
	}
	dt, err := NewDeploymentTopology(spec)
	if err != nil {
		t.Fatalf("NewDeploymentTopology(split spec): %v", err)
	}
	if !dt.IsColocated("alpha") {
		t.Error("IsColocated(alpha) = false, want true (core's own cell)")
	}
	if dt.IsColocated("beta") {
		t.Error("IsColocated(beta) = true, want false (edge cell is remote)")
	}
	ep, ok := dt.RemoteEndpoint("beta")
	if !ok || ep != "edge.svc:9001" {
		t.Errorf("RemoteEndpoint(beta) = (%q,%v), want (edge.svc:9001,true)", ep, ok)
	}
}

// TestSpecForRole_SingleGroupRoleNoRemotes verifies that selecting the only
// declared role yields its cells colocated and no remote cells.
func TestSpecForRole_SingleGroupRoleNoRemotes(t *testing.T) {
	groups := []TopologyGroup{{Role: "solo", Cells: []string{"alpha", "beta"}, Endpoint: "solo.svc:9000"}}
	spec, err := SpecForRole(groups, "solo")
	if err != nil {
		t.Fatalf("SpecForRole(_, \"solo\") unexpected error: %v", err)
	}
	if got := asSet(spec.Colocated); !got["alpha"] || !got["beta"] || len(got) != 2 {
		t.Fatalf("Colocated = %v, want {alpha,beta}", spec.Colocated)
	}
	if len(spec.Remote) != 0 {
		t.Fatalf("Remote = %+v, want empty (no other groups)", spec.Remote)
	}
}

// TestSpecForRole_UnknownRoleFailsClosed verifies that a role not present in the
// declared group set fails fast — never silently degrades to monolith.
func TestSpecForRole_UnknownRoleFailsClosed(t *testing.T) {
	_, err := SpecForRole(twoGroupGraph(), "nope")
	if err == nil {
		t.Fatal("unknown role must fail closed, got nil error")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("fail-closed error must be *errcode.Error, got %T", err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Fatalf("fail-closed error code = %v, want ErrValidationFailed", ec.Code)
	}
}

// asSet is a tiny test helper to assert membership order-independently.
func asSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
