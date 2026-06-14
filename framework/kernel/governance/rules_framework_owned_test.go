package governance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// TestValidProject_WithFrameworkContracts_ZeroErrors is the integration safety
// net: starting from the known-clean validProject (0 errors), adding draft
// framework-owned http + event contracts must not introduce ANY error from ANY
// base/strict/dep rule — proving the framework-owner concept is governed end to
// end, not just by the rules we anticipated.
func TestValidProject_WithFrameworkContracts_ZeroErrors(t *testing.T) {
	pm := validProject()
	replayable := true
	pm.Contracts["http.deviceidentity.enroll.v1"] = &metadata.ContractMeta{
		ID:               "http.deviceidentity.enroll.v1",
		Kind:             "http",
		OwnerCell:        metadata.FrameworkOwnerSentinel,
		ConsistencyLevel: "L2",
		Lifecycle:        "draft",
		Transports:       []string{"http"},
		Endpoints: metadata.EndpointsMeta{
			Server: metadata.FrameworkOwnerSentinel,
			// noContent=true requires successStatus 204 (FMT-13). A real enroll
			// returns a cert body; this is a self-contained fixture (no schema
			// file on disk), and the point is the framework OWNER exemption, not
			// the wire shape — framework contracts still obey FMT format rules.
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "POST",
				Path:          "/api/v1/deviceidentity/enroll",
				SuccessStatus: 204,
				NoContent:     true,
			},
		},
		Dir:  "contracts/http/deviceidentity/enroll/v1",
		File: "contracts/http/deviceidentity/enroll/v1/contract.yaml",
	}
	pm.Contracts["event.deviceidentity.cert-issued.v1"] = &metadata.ContractMeta{
		ID:                "event.deviceidentity.cert-issued.v1",
		Kind:              "event",
		OwnerCell:         metadata.FrameworkOwnerSentinel,
		ConsistencyLevel:  "L2",
		Lifecycle:         "draft",
		Transports:        []string{"amqp"},
		Endpoints:         metadata.EndpointsMeta{Publisher: metadata.FrameworkOwnerSentinel},
		Replayable:        &replayable,
		IdempotencyKey:    "eventId",
		DeliverySemantics: "at-least-once",
		Dir:               "contracts/event/deviceidentity/cert-issued/v1",
		File:              "contracts/event/deviceidentity/cert-issued/v1/contract.yaml",
	}
	val := NewValidator(pm, "", clock.Real())
	results, err := val.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	errs := FilterErrors(results)
	assert.Empty(t, errs, "draft framework-owned contracts must add 0 errors, got: %v", errs)
}

// frameworkOwnedProject builds an isolated project with the given contracts and
// a single known cell (accesscore), so cell-owner rules have a populated cell
// set to check against.
func frameworkOwnedProject(contracts ...*metadata.ContractMeta) *metadata.ProjectMeta {
	cm := map[string]*metadata.ContractMeta{}
	for _, c := range contracts {
		cm[c.ID] = c
	}
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {ID: "accesscore", Dir: "accesscore", File: "corecells/accesscore/cell.yaml"},
		},
		Contracts: cm,
		Slices:    map[string]*metadata.SliceMeta{},
	}
}

func fwHTTPContract(id, lifecycle string) *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:               id,
		Kind:             "http",
		OwnerCell:        metadata.FrameworkOwnerSentinel,
		ConsistencyLevel: "L2",
		Lifecycle:        lifecycle,
		Endpoints:        metadata.EndpointsMeta{Server: metadata.FrameworkOwnerSentinel},
		File:             "contracts/http/deviceidentity/enroll/v1/contract.yaml",
		Dir:              "contracts/http/deviceidentity/enroll/v1",
	}
}

func fwEventContract(id, lifecycle string) *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:               id,
		Kind:             "event",
		OwnerCell:        metadata.FrameworkOwnerSentinel,
		ConsistencyLevel: "L2",
		Lifecycle:        lifecycle,
		Endpoints:        metadata.EndpointsMeta{Publisher: metadata.FrameworkOwnerSentinel},
		File:             "contracts/event/deviceidentity/cert-issued/v1/contract.yaml",
		Dir:              "contracts/event/deviceidentity/cert-issued/v1",
	}
}

// TestFrameworkOwnedContract_ExcludedFromCellOwnerRules proves a framework-owned
// (draft) http + event contract trips none of the cell-owner reference rules
// (REF-03, REF-13) nor the cell-slice emit-coupling rule (CCE-01), and is clean
// under the framework-side rule when it is a draft eligible-kind contract.
//
// Anti-vacuity (FIX 6): we also add a cell-owned L2 HTTP contract with a serving
// slice so CCE-01 actually processes cell-owned input. The rule must fire on the
// cell contract (constraint-1: L2 with no triggers), but must NOT produce any
// finding that references the framework contract's ID — confirming the framework
// exclusion is real and not "trivially no input".
func TestFrameworkOwnedContract_ExcludedFromCellOwnerRules(t *testing.T) {
	const fwContractID = "http.deviceidentity.enroll.v1"
	const cellContractID = "http.accesscore.login.v1"

	// Cell-owned L2 HTTP contract with no triggers — CCE-01 constraint-1 will fire on it.
	cellContract := &metadata.ContractMeta{
		ID:               cellContractID,
		Kind:             "http",
		OwnerCell:        "accesscore",
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Endpoints:        metadata.EndpointsMeta{Server: "accesscore"},
		File:             "contracts/http/accesscore/login/v1/contract.yaml",
		Dir:              "contracts/http/accesscore/login/v1",
	}
	// Serving slice for the cell-owned contract.
	serveSlice := &metadata.SliceMeta{
		ID:            "login",
		BelongsToCell: "accesscore",
		ContractUsages: []metadata.ContractUsage{
			{Contract: cellContractID, Role: "serve"},
		},
		File: "corecells/accesscore/slices/login/slice.yaml",
	}

	pm := frameworkOwnedProject(
		fwHTTPContract(fwContractID, "draft"),
		fwEventContract("event.deviceidentity.cert-issued.v1", "draft"),
		cellContract,
	)
	pm.Slices["accesscore/login"] = serveSlice

	val := NewValidator(pm, ".", clock.Real())

	if got := findByCode(val.validateREF03(), "REF-03"); len(got) != 0 {
		t.Errorf("REF-03 must skip framework-owned contracts, got %d findings: %v", len(got), got)
	}
	if got := findByCode(val.validateREF13(), "REF-13"); len(got) != 0 {
		t.Errorf("REF-13 must skip framework-owned contracts, got %d findings: %v", len(got), got)
	}

	// CCE-01 must fire on the cell-owned contract (anti-vacuity: rule actually runs),
	// but must not produce any finding that references the framework contract ID.
	cceFindings := findByCode(val.validateCONTRACTCONSISTENCYEMIT01(), "CONTRACT-CONSISTENCY-EMIT-01")
	if len(cceFindings) == 0 {
		t.Fatal("CCE-01 anti-vacuity: must fire on the cell-owned L2 contract with no triggers, got 0 findings")
	}
	for _, f := range cceFindings {
		if strings.Contains(f.Message, fwContractID) || strings.Contains(f.File, fwContractID) {
			t.Errorf("CCE-01 must not reference the framework contract %q, but got finding: %v", fwContractID, f)
		}
	}

	if got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), "FRAMEWORK-OWNED-CONTRACT-SCOPED-01"); len(got) != 0 {
		t.Errorf("draft http/event framework contracts must be clean, got %d findings: %v", len(got), got)
	}
}

// TestFrameworkOwnedContractScoped_ActiveRejected proves the fail-closed
// lifecycle: an active framework contract is rejected (active serving not wired).
func TestFrameworkOwnedContractScoped_ActiveRejected(t *testing.T) {
	pm := frameworkOwnedProject(fwHTTPContract("http.deviceidentity.enroll.v1", "active"))
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), "FRAMEWORK-OWNED-CONTRACT-SCOPED-01")
	if len(got) != 1 {
		t.Fatalf("active framework contract must produce 1 finding, got %d: %v", len(got), got)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("expected error severity, got %v", got[0].Severity)
	}
}

// TestFrameworkOwnedContractScoped_DeprecatedPermitted proves the fail-closed
// lifecycle only rejects "active": a deprecated framework contract is permitted
// (on its way out, not silently dead).
func TestFrameworkOwnedContractScoped_DeprecatedPermitted(t *testing.T) {
	pm := frameworkOwnedProject(fwHTTPContract("http.deviceidentity.enroll.v1", "deprecated"))
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), "FRAMEWORK-OWNED-CONTRACT-SCOPED-01")
	if len(got) != 0 {
		t.Errorf("deprecated framework contract must produce 0 findings, got %d: %v", len(got), got)
	}
}

// TestFrameworkOwnedContractScoped_IneligibleKind proves only http/event may be
// framework-owned; a framework-owned command is rejected (fail-closed).
func TestFrameworkOwnedContractScoped_IneligibleKind(t *testing.T) {
	cmd := &metadata.ContractMeta{
		ID:               "command.deviceidentity.rotate.v1",
		Kind:             "command",
		OwnerCell:        metadata.FrameworkOwnerSentinel,
		ConsistencyLevel: "L2",
		Lifecycle:        "draft",
		Endpoints:        metadata.EndpointsMeta{Handler: metadata.FrameworkOwnerSentinel},
		File:             "contracts/command/deviceidentity/rotate/v1/contract.yaml",
	}
	pm := frameworkOwnedProject(cmd)
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), "FRAMEWORK-OWNED-CONTRACT-SCOPED-01")
	if len(got) != 1 {
		t.Fatalf("framework-owned command must produce 1 kind finding, got %d: %v", len(got), got)
	}
}

// TestFrameworkOwnedContractScoped_ProviderMustBeFramework proves constraint 3:
// a framework-owned http/event contract whose provider endpoint names a cell (or
// any other non-framework actor) instead of the FrameworkOwnerSentinel is
// rejected. Without this, REF-13's framework skip (rules_ref.go) leaves the
// provider endpoint of a framework-owned contract entirely unvalidated — it could
// point at an arbitrary, even nonexistent, cell/actor and nothing would catch it.
func TestFrameworkOwnedContractScoped_ProviderMustBeFramework(t *testing.T) {
	tests := []struct {
		name     string
		contract *metadata.ContractMeta
		field    string
	}{
		{
			name: "http provider names a cell",
			contract: &metadata.ContractMeta{
				ID:               "http.deviceidentity.enroll.v1",
				Kind:             "http",
				OwnerCell:        metadata.FrameworkOwnerSentinel,
				ConsistencyLevel: "L2",
				Lifecycle:        "draft",
				Endpoints:        metadata.EndpointsMeta{Server: "accesscore"},
				File:             "contracts/http/deviceidentity/enroll/v1/contract.yaml",
				Dir:              "contracts/http/deviceidentity/enroll/v1",
			},
			field: "endpoints.server",
		},
		{
			name: "event provider names an unknown actor",
			contract: &metadata.ContractMeta{
				ID:               "event.deviceidentity.cert-issued.v1",
				Kind:             "event",
				OwnerCell:        metadata.FrameworkOwnerSentinel,
				ConsistencyLevel: "L2",
				Lifecycle:        "draft",
				Endpoints:        metadata.EndpointsMeta{Publisher: "ghostactor"},
				File:             "contracts/event/deviceidentity/cert-issued/v1/contract.yaml",
				Dir:              "contracts/event/deviceidentity/cert-issued/v1",
			},
			field: "endpoints.publisher",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := frameworkOwnedProject(tc.contract)
			val := NewValidator(pm, ".", clock.Real())

			got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), "FRAMEWORK-OWNED-CONTRACT-SCOPED-01")
			if len(got) != 1 {
				t.Fatalf("framework-owned contract with non-framework provider must produce 1 finding, got %d: %v", len(got), got)
			}
			if got[0].Field != tc.field {
				t.Errorf("finding must anchor at %q, got %q", tc.field, got[0].Field)
			}
			if got[0].Severity != SeverityError {
				t.Errorf("expected error severity, got %v", got[0].Severity)
			}
		})
	}
}

// TestREF03_CellOwnerAntiVacuity proves REF-03 still fires for a typo'd cell
// owner — the typed-owner funnel did not blanket-suppress cell-owner checks.
func TestREF03_CellOwnerAntiVacuity(t *testing.T) {
	pm := frameworkOwnedProject(&metadata.ContractMeta{
		ID:        "http.auth.login.v1",
		Kind:      "http",
		OwnerCell: "accescore", // typo of accesscore — not a known cell
		Lifecycle: "draft",
		File:      "contracts/http/auth/login/v1/contract.yaml",
	})
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateREF03(), "REF-03")
	if len(got) != 1 {
		t.Fatalf("REF-03 must still fire for a typo'd cell owner, got %d: %v", len(got), got)
	}
}

// TestCCE01_CellOwnerAntiVacuity proves CCE-01 constraint 1 still fires for a
// cell-owned L2 HTTP contract with no triggers — the framework skip did not
// disable the rule for cell-owned contracts.
func TestCCE01_CellOwnerAntiVacuity(t *testing.T) {
	pm := frameworkOwnedProject(&metadata.ContractMeta{
		ID:               "http.auth.login.v1",
		Kind:             "http",
		OwnerCell:        "accesscore",
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Triggers:         nil,
		Endpoints:        metadata.EndpointsMeta{Server: "accesscore"},
		File:             "contracts/http/auth/login/v1/contract.yaml",
		Dir:              "contracts/http/auth/login/v1",
	})
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateCONTRACTCONSISTENCYEMIT01(), "CONTRACT-CONSISTENCY-EMIT-01")
	if len(got) == 0 {
		t.Fatal("CCE-01 must still fire constraint-1 for a cell-owned L2 HTTP contract with no triggers")
	}
}
