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

// TestFMT42_ModelessRouteRejected: #2020 reverses the former sparse-overlay leniency —
// a modeless active codegen route (no permission, no opt-out) is now an error.
func TestFMT42_ModelessRouteRejected(t *testing.T) {
	project := fmt42Project(func(_ *metadata.HTTPTransportMeta) {})
	errs := fmt42Errors(NewValidator(project, "", clock.Real()).validateFMT42())
	if len(errs) == 0 {
		t.Fatal("FMT-42: a modeless active route must be rejected (#2020 default-ABAC), got none")
	}
	if errs[0].Field != "endpoints.http.auth" {
		t.Errorf("FMT-42: expected finding on endpoints.http.auth, got %q", errs[0].Field)
	}
}

// TestFMT42_ModelessSkippedWhenInactive: draft / non-codegen routes mount no live
// route, so the #2020 mandatory-mode gate does not apply.
func TestFMT42_ModelessSkippedWhenInactive(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(c *metadata.ContractMeta)
	}{
		{"draft", func(c *metadata.ContractMeta) { c.Lifecycle = "draft" }},
		{"non-codegen", func(c *metadata.ContractMeta) { c.Codegen = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := fmt42Project(func(_ *metadata.HTTPTransportMeta) {})
			tc.mutate(project.Contracts["http.config.x.v1"])
			if errs := fmt42Errors(NewValidator(project, "", clock.Real()).validateFMT42()); len(errs) != 0 {
				t.Fatalf("FMT-42: %s route must be skipped, got: %v", tc.name, errs)
			}
		})
	}
}

// TestFMT42_LedgeredModelessExempt: a modeless contract on the frozen #2020 migration
// ledger is exempt (the mechanism lands without blocking #2355/#2358 migration).
func TestFMT42_LedgeredModelessExempt(t *testing.T) {
	ids := metadata.HTTPAuthModeLedgerIDs()
	if len(ids) == 0 {
		t.Skip("ledger drained — exemption path removed at #2020 endgame")
	}
	project := fmt42Project(func(_ *metadata.HTTPTransportMeta) {})
	c := project.Contracts["http.config.x.v1"]
	delete(project.Contracts, "http.config.x.v1")
	c.ID = ids[0]
	project.Contracts[ids[0]] = c
	if errs := fmt42Errors(NewValidator(project, "", clock.Real()).validateFMT42()); len(errs) != 0 {
		t.Fatalf("FMT-42: a ledgered modeless contract must be exempt, got: %v", errs)
	}
}

// TestFMT42_OptOutRequiresReason: an opt-out mode must carry a non-empty auth.reason.
func TestFMT42_OptOutRequiresReason(t *testing.T) {
	t.Run("missing reason", func(t *testing.T) {
		project := fmt42Project(func(h *metadata.HTTPTransportMeta) { h.Auth.Public = true })
		errs := fmt42Errors(NewValidator(project, "", clock.Real()).validateFMT42())
		if len(errs) == 0 {
			t.Fatal("FMT-42: opt-out without reason must error (#2020), got none")
		}
		if errs[0].Field != "endpoints.http.auth.reason" {
			t.Errorf("expected finding on endpoints.http.auth.reason, got %q", errs[0].Field)
		}
	})
	t.Run("with reason", func(t *testing.T) {
		project := fmt42Project(func(h *metadata.HTTPTransportMeta) {
			h.Auth.Public = true
			h.Auth.Reason = "public login entrypoint"
		})
		if errs := fmt42Errors(NewValidator(project, "", clock.Real()).validateFMT42()); len(errs) != 0 {
			t.Fatalf("FMT-42: opt-out with reason must pass, got: %v", errs)
		}
	})
}

// TestFMT42_ReasonWithoutOptOutForbidden: auth.reason on a non-opt-out (ABAC/standard)
// route is forbidden — the reason only justifies an opt-out.
func TestFMT42_ReasonWithoutOptOutForbidden(t *testing.T) {
	project := fmt42Project(func(h *metadata.HTTPTransportMeta) {
		h.Permission = "config:read"
		h.Auth.Reason = "stray reason"
	})
	errs := fmt42Errors(NewValidator(project, "", clock.Real()).validateFMT42())
	if len(errs) == 0 {
		t.Fatal("FMT-42: reason without an opt-out mode must be forbidden (#2020), got none")
	}
	if errs[0].Field != "endpoints.http.auth.reason" {
		t.Errorf("expected finding on endpoints.http.auth.reason, got %q", errs[0].Field)
	}
}
