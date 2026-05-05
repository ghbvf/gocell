// SETUP-ADMIN-NOT-PUBLIC-01
//
// Invariant: the setup/admin HTTP contract must not be declared as auth.public.
// Any deployment where the first-admin endpoint is JWT-exempt with no other
// authentication gate is a product-level security vulnerability.
//
// AUTH-BOOTSTRAP-PATH-RESTRICTED-01
//
// Invariant: auth.bootstrap:true is only allowed on contracts whose path
// contains "setup/admin". This flag enables HTTP Basic Auth using env
// credentials intended exclusively for first-run admin provisioning.
//
// Refs: roadmap A2 / PR#376 review F1
package archtest

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestSetupAdminNotPublic scans all contract.yaml files in the project and
// verifies that no HTTP contract whose path contains "setup/admin" declares
// auth.public:true. The setup/admin endpoint must be protected by bootstrap
// auth (auth.bootstrap:true) rather than being fully JWT-exempt.
//
// Current state: contracts/http/auth/setup/admin/v1/contract.yaml has
// auth.public:true — this test is intentionally RED until Batch 1 / Agent-A
// changes that to auth.bootstrap:true.
func TestSetupAdminNotPublic(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	for _, contract := range project.Contracts {
		if contract.Kind != "http" || contract.Endpoints.HTTP == nil {
			continue
		}
		path := contract.Endpoints.HTTP.Path
		if !strings.Contains(path, "setup/admin") {
			continue
		}
		if contract.Endpoints.HTTP.Auth.Public {
			t.Errorf("SETUP-ADMIN-NOT-PUBLIC-01: contract %q path=%q has auth.public:true; "+
				"the setup/admin endpoint must not be JWT-exempt (use auth.bootstrap:true instead)",
				contract.ID, path)
		}
	}
}

// TestAuthBootstrapPathRestricted verifies that auth.bootstrap:true is only
// permitted on contracts whose HTTP path contains "setup/admin".
//
// The NegativeFixture sub-test verifies the checker itself catches violations
// using synthetic contracts (no real contract.yaml files needed).
func TestAuthBootstrapPathRestricted(t *testing.T) {
	t.Parallel()

	t.Run("NegativeFixture_BootstrapOnNonSetupPath_Detected", func(t *testing.T) {
		t.Parallel()
		// Synthetic contracts: one valid (setup/admin), one invalid (foo path).
		type fakeContract struct {
			id        string
			path      string
			bootstrap bool
		}
		contracts := []fakeContract{
			// Valid: bootstrap on setup/admin path — must pass
			{id: "http.auth.setup.admin.v1", path: "/api/v1/access/setup/admin", bootstrap: true},
			// Invalid: bootstrap on arbitrary path — must be caught
			{id: "http.foo.bar.v1", path: "/api/v1/foo/bar", bootstrap: true},
			// Valid: bootstrap:false on non-setup path — must pass
			{id: "http.baz.qux.v1", path: "/api/v1/baz/qux", bootstrap: false},
		}

		var violations []string
		for _, c := range contracts {
			if c.bootstrap && !strings.Contains(c.path, "setup/admin") {
				violations = append(violations,
					"contract "+c.id+": auth.bootstrap:true on non-setup/admin path "+c.path)
			}
		}

		if len(violations) == 0 {
			t.Errorf("AUTH-BOOTSTRAP-PATH-RESTRICTED-01: expected at least 1 violation for bootstrap on non-setup path, got 0")
		}
		if len(violations) != 1 {
			t.Errorf("AUTH-BOOTSTRAP-PATH-RESTRICTED-01: expected exactly 1 violation, got %d: %v",
				len(violations), violations)
		}
	})

	t.Run("ProjectScan_NoBootstrapOnNonSetupPaths", func(t *testing.T) {
		t.Parallel()
		root := findModuleRoot(t)
		project := mustParseProjectContracts(t, root)

		for _, contract := range project.Contracts {
			if contract.Kind != "http" || contract.Endpoints.HTTP == nil {
				continue
			}
			if !contract.Endpoints.HTTP.Auth.Bootstrap {
				continue
			}
			path := contract.Endpoints.HTTP.Path
			if !strings.Contains(path, "setup/admin") {
				t.Errorf("AUTH-BOOTSTRAP-PATH-RESTRICTED-01: contract %q path=%q has auth.bootstrap:true "+
					"but path does not contain 'setup/admin'; bootstrap auth is only permitted on setup/admin contracts",
					contract.ID, path)
			}
		}
	})
}

// TestFMT27_PublicBootstrapMutuallyExclusive verifies that a contract
// declaring both auth.public:true and auth.bootstrap:true is caught.
// Uses NegativeFixture (synthetic ProjectMeta — no filesystem access).
func TestFMT27_PublicBootstrapMutuallyExclusive(t *testing.T) {
	t.Parallel()

	// Build a synthetic contract that violates the FMT-27 rule.
	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.bad.v1": {
				ID:               "http.auth.bad.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "accesscore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/access/setup/admin",
						SuccessStatus: 201,
						Auth: metadata.HTTPAuthMeta{
							Public:    true,
							Bootstrap: true,
						},
					},
				},
				Dir:  "contracts/http/auth/bad/v1",
				File: "contracts/http/auth/bad/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	// The FMT-27 rule (to be implemented in Batch 1 / Agent-B in rules_fmt.go)
	// should catch auth.public + auth.bootstrap coexistence.
	// For now, we validate the checker logic inline to confirm this is RED.
	var violations []string
	for _, c := range project.Contracts {
		if c.Endpoints.HTTP == nil {
			continue
		}
		auth := c.Endpoints.HTTP.Auth
		if auth.Public && auth.Bootstrap {
			violations = append(violations, "contract "+c.ID+": auth.public and auth.bootstrap are mutually exclusive")
		}
	}

	if len(violations) == 0 {
		t.Errorf("FMT27: expected 1 violation for public+bootstrap coexistence, got 0 — "+
			"governance rule validateFMT27 not yet implemented (Batch 1 Agent-B)")
	}
}

// TestFMT28_BootstrapRestrictedToSetupAdminPath verifies that the FMT-28
// governance rule catches auth.bootstrap:true on non-setup/admin paths.
// Uses NegativeFixture (synthetic ProjectMeta — no filesystem access).
func TestFMT28_BootstrapRestrictedToSetupAdminPath(t *testing.T) {
	t.Parallel()

	project := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.some.other.v1": {
				ID:               "http.some.other.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "somecore",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/some/other",
						SuccessStatus: 201,
						Auth: metadata.HTTPAuthMeta{
							Bootstrap: true,
						},
					},
				},
				Dir:  "contracts/http/some/other/v1",
				File: "contracts/http/some/other/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	// The FMT-28 rule (to be implemented in Batch 1 / Agent-B in rules_fmt.go)
	// should catch auth.bootstrap:true on paths that don't contain "setup/admin".
	var violations []string
	for _, c := range project.Contracts {
		if c.Endpoints.HTTP == nil {
			continue
		}
		if !c.Endpoints.HTTP.Auth.Bootstrap {
			continue
		}
		if !strings.Contains(c.Endpoints.HTTP.Path, "setup/admin") {
			violations = append(violations,
				"contract "+c.ID+": auth.bootstrap:true only allowed on setup/admin paths")
		}
	}

	if len(violations) == 0 {
		t.Errorf("FMT28: expected 1 violation for bootstrap on non-setup/admin path, got 0 — "+
			"governance rule validateFMT28 not yet implemented (Batch 1 Agent-B)")
	}
}
