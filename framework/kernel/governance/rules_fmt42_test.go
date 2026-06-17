package governance

import (
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// fmt42Project builds a minimal project with one http contract whose HTTP block is
// customized by mutate (#2205 FMT-42 tests).
func fmt42Project(mutate func(h *metadata.HTTPTransportMeta)) *metadata.ProjectMeta {
	h := &metadata.HTTPTransportMeta{
		Method:        "GET",
		Path:          "/api/v1/config/x",
		SuccessStatus: 200,
	}
	mutate(h)
	return &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.config.x.v1": {
				ID:               "http.config.x.v1",
				Kind:             "http",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Codegen:          true,
				Endpoints: metadata.EndpointsMeta{
					Server: metadatatest.CellIDConfigCore,
					HTTP:   h,
				},
				Dir:  "contracts/http/config/x/v1",
				File: "contracts/http/config/x/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

func fmt42Errors(results []ValidationResult) []ValidationResult {
	var out []ValidationResult
	for _, r := range results {
		if r.Code == "FMT-42" && r.Severity == SeverityError {
			out = append(out, r)
		}
	}
	return out
}

// TestFMT42_UnknownPermission: a permission outside the closed authz registry → error.
func TestFMT42_UnknownPermission(t *testing.T) {
	project := fmt42Project(func(h *metadata.HTTPTransportMeta) { h.Permission = "bogus:action" })
	results := NewValidator(project, "", clock.Real()).validateFMT42()
	errs := fmt42Errors(results)
	if len(errs) == 0 {
		t.Fatal("FMT-42: expected error for unregistered permission, got none")
	}
	if errs[0].Field != "endpoints.http.permission" {
		t.Errorf("FMT-42: expected finding on endpoints.http.permission, got %q", errs[0].Field)
	}
}

// TestFMT42_PermissionWithNoGateMode: permission + any no-gate auth mode → mutex error.
// Table-driven: each of the 4 no-gate modes (Public / Bootstrap / ClientsOnly /
// ServiceOwned) is mutually exclusive with a permission overlay.
func TestFMT42_PermissionWithNoGateMode(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(h *metadata.HTTPTransportMeta)
	}{
		{
			name: "Public",
			mutate: func(h *metadata.HTTPTransportMeta) {
				h.Permission = "config:read"
				h.Auth = metadata.HTTPAuthMeta{Public: true}
			},
		},
		{
			name: "Bootstrap",
			mutate: func(h *metadata.HTTPTransportMeta) {
				h.Permission = "config:read"
				h.Auth = metadata.HTTPAuthMeta{Bootstrap: true}
			},
		},
		{
			name: "ClientsOnly",
			mutate: func(h *metadata.HTTPTransportMeta) {
				h.Permission = "config:read"
				h.Auth = metadata.HTTPAuthMeta{ClientsOnly: true}
			},
		},
		{
			name: "ServiceOwned",
			mutate: func(h *metadata.HTTPTransportMeta) {
				h.Permission = "config:read"
				h.Auth = metadata.HTTPAuthMeta{ServiceOwned: true}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := fmt42Project(tc.mutate)
			results := NewValidator(project, "", clock.Real()).validateFMT42()
			if len(fmt42Errors(results)) == 0 {
				t.Fatalf("FMT-42: expected mutex error for permission + auth.%s, got none", tc.name)
			}
		})
	}
}

// TestFMT42_ValidStandardPermission: a registered permission on a standard route passes.
func TestFMT42_ValidStandardPermission(t *testing.T) {
	project := fmt42Project(func(h *metadata.HTTPTransportMeta) { h.Permission = "config:read" })
	results := NewValidator(project, "", clock.Real()).validateFMT42()
	if errs := fmt42Errors(results); len(errs) != 0 {
		t.Fatalf("FMT-42: registered permission on standard route must pass, got: %v", errs)
	}
}

// TestFMT42_AbsentPermissionLegal: a standard route without a permission overlay is
// legal during the #2205 migration (sparse overlay) — FMT-42 must NOT flag it.
func TestFMT42_AbsentPermissionLegal(t *testing.T) {
	project := fmt42Project(func(_ *metadata.HTTPTransportMeta) {})
	results := NewValidator(project, "", clock.Real()).validateFMT42()
	if errs := fmt42Errors(results); len(errs) != 0 {
		t.Fatalf("FMT-42: absent permission must be legal (sparse overlay), got: %v", errs)
	}
}
