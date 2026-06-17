package governance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// TestValidProject_WithActiveFrameworkContract_ZeroErrors is the integration
// sanity net for the serving-scan path: starting from the known-clean
// validProject (0 errors), adding an active framework-owned http contract
// that is listed in assembly.frameworkContracts + referenced by a journey must
// produce 0 errors under ValidateStrict — proving lifecycle=active is permitted
// when the assembly serving opt-in is satisfied.
func TestValidProject_WithActiveFrameworkContract_ZeroErrors(t *testing.T) {
	pm := validProject()
	const fwID = "http.devicestate.v1"
	pm.Contracts[fwID] = &metadata.ContractMeta{
		ID:               fwID,
		Kind:             "http",
		OwnerCell:        metadata.FrameworkOwnerSentinel,
		ConsistencyLevel: "L0",
		Lifecycle:        "active",
		Transports:       []string{"http"},
		Endpoints: metadata.EndpointsMeta{
			Server: metadata.FrameworkOwnerSentinel,
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          "/api/v1/devicestate",
				SuccessStatus: 200,
			},
		},
		Dir:  "contracts/http/devicestate/v1",
		File: "contracts/http/devicestate/v1/contract.yaml",
	}
	// Per-deployment serving opt-in: the existing corebundle assembly must
	// declare frameworkContracts so FRAMEWORK-OWNED-CONTRACT-SCOPED-01 passes.
	pm.Assemblies["corebundle"].FrameworkContracts = []string{fwID}
	// JOURNEY-CONTRACT-EXISTENCE-01: every active platform contract must be
	// referenced by at least one journey. Add a framework-serving journey with
	// cells: [_framework] (REF-06 exempts the sentinel per TestREF06_FrameworkSentinelPermitted).
	pm.Journeys["J-devicestate"] = &metadata.JourneyMeta{
		ID:        "J-devicestate",
		Goal:      "framework serves device state query",
		Lifecycle: "experimental",
		Owner:     metadata.OwnerMeta{Team: "platform", Role: "journey-owner"},
		Cells:     []string{metadata.FrameworkOwnerSentinel},
		Contracts: []string{fwID},
		File:      "journeys/J-devicestate.yaml",
	}
	val := NewValidator(pm, "", clock.Real())
	results, err := val.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	errs := FilterErrors(results)
	assert.Empty(t, errs, "active framework-owned contract served by assembly must add 0 errors, got: %v", errs)
}

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
			metadatatest.CellIDAccessCore: {ID: metadatatest.CellIDAccessCore, Dir: "accesscore", File: "corecells/accesscore/cell.yaml"},
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
		OwnerCell:        metadatatest.CellIDAccessCore,
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDAccessCore},
		File:             "contracts/http/accesscore/login/v1/contract.yaml",
		Dir:              "contracts/http/accesscore/login/v1",
	}
	// Serving slice for the cell-owned contract.
	serveSlice := &metadata.SliceMeta{
		ID:            "login",
		BelongsToCell: metadatatest.CellIDAccessCore,
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
	// A distinct framework http contract id (devicestate) — deprecated framework
	// contracts are permitted regardless of id.
	pm := frameworkOwnedProject(fwHTTPContract("http.devicestate.v1", "deprecated"))
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
				Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDAccessCore},
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
				Endpoints:        metadata.EndpointsMeta{Publisher: metadatatest.NewCellID("ghostactor")},
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
		OwnerCell: metadatatest.NewCellID("accescore"), // typo of accesscore — not a known cell
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
		OwnerCell:        metadatatest.CellIDAccessCore,
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Triggers:         nil,
		Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDAccessCore},
		File:             "contracts/http/auth/login/v1/contract.yaml",
		Dir:              "contracts/http/auth/login/v1",
	})
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateCONTRACTCONSISTENCYEMIT01(), "CONTRACT-CONSISTENCY-EMIT-01")
	if len(got) == 0 {
		t.Fatal("CCE-01 must still fire constraint-1 for a cell-owned L2 HTTP contract with no triggers")
	}
}

// frameworkOwnedProjectWithAssembly builds an isolated project with the given contracts
// and a corebundle assembly that serves the given framework contract IDs.
func frameworkOwnedProjectWithAssembly(servedIDs []string, contracts ...*metadata.ContractMeta) *metadata.ProjectMeta {
	pm := frameworkOwnedProject(contracts...)
	pm.Assemblies = map[string]*metadata.AssemblyMeta{
		"corebundle": {
			ID:                 "corebundle",
			FrameworkContracts: servedIDs,
			File:               "assemblies/corebundle/assembly.yaml",
		},
	}
	return pm
}

// --- Serving-scan tests (task 1 + 2) ---

// TestFrameworkOwnedContractScoped_ActiveServedByAssembly proves an active framework
// http contract that appears in an assembly's frameworkContracts is permitted (green).
func TestFrameworkOwnedContractScoped_ActiveServedByAssembly(t *testing.T) {
	const id = "http.deviceidentity.enroll.v1"
	pm := frameworkOwnedProjectWithAssembly(
		[]string{id},
		fwHTTPContract(id, "active"),
	)
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), codeFRAMEWORKOWNEDCONTRACTSCOPED01)
	if len(got) != 0 {
		t.Errorf("active framework http contract served by assembly must produce 0 findings, got %d: %v", len(got), got)
	}
}

// TestFrameworkOwnedContractScoped_ActiveNotServed proves an active framework http
// contract absent from all assembly.frameworkContracts is rejected (serving-scan red).
func TestFrameworkOwnedContractScoped_ActiveNotServed(t *testing.T) {
	const id = "http.deviceidentity.enroll.v1"
	// Project has an assembly, but it does not list the contract.
	pm := frameworkOwnedProjectWithAssembly(
		[]string{}, // no contracts served
		fwHTTPContract(id, "active"),
	)
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), codeFRAMEWORKOWNEDCONTRACTSCOPED01)
	if len(got) != 1 {
		t.Fatalf("active framework contract not served by any assembly must produce 1 finding, got %d: %v", len(got), got)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("expected error severity, got %v", got[0].Severity)
	}
	if got[0].Field != "lifecycle" {
		t.Errorf("finding must anchor at lifecycle, got %q", got[0].Field)
	}
}

// TestFrameworkOwnedContractScoped_ActiveNoAssemblies proves that an active framework
// http contract with no assemblies at all (Assemblies is nil) is rejected.
func TestFrameworkOwnedContractScoped_ActiveNoAssemblies(t *testing.T) {
	const id = "http.deviceidentity.enroll.v1"
	// frameworkOwnedProject does not set Assemblies — nil map.
	pm := frameworkOwnedProject(fwHTTPContract(id, "active"))
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), codeFRAMEWORKOWNEDCONTRACTSCOPED01)
	if len(got) != 1 {
		t.Fatalf("active framework contract with no assemblies must produce 1 finding, got %d: %v", len(got), got)
	}
}

// TestFrameworkOwnedContractScoped_DraftStillPermittedWithAssembly proves that a
// draft framework contract is not affected by the serving-scan: draft → 0 findings
// regardless of assembly.frameworkContracts (draft is not active serving).
func TestFrameworkOwnedContractScoped_DraftStillPermittedWithAssembly(t *testing.T) {
	const id = "http.deviceidentity.enroll.v1"
	pm := frameworkOwnedProjectWithAssembly(
		[]string{}, // empty — irrelevant for draft
		fwHTTPContract(id, "draft"),
	)
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), codeFRAMEWORKOWNEDCONTRACTSCOPED01)
	if len(got) != 0 {
		t.Errorf("draft framework contract must produce 0 findings, got %d: %v", len(got), got)
	}
}

// --- Assembly frameworkContracts entry-validation tests (task 2) ---

// TestAssemblyFrameworkContracts_UnknownID proves that an assembly referencing a
// non-existent contract ID produces 1 error.
func TestAssemblyFrameworkContracts_UnknownID(t *testing.T) {
	pm := frameworkOwnedProjectWithAssembly(
		[]string{"http.does.not.exist.v1"},
		// no matching contract in the project
	)
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), codeFRAMEWORKOWNEDCONTRACTSCOPED01)
	if len(got) != 1 {
		t.Fatalf("unknown contract id in frameworkContracts must produce 1 finding, got %d: %v", len(got), got)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("expected error severity, got %v", got[0].Severity)
	}
	if got[0].Field != "frameworkContracts" {
		t.Errorf("finding must anchor at frameworkContracts, got %q", got[0].Field)
	}
}

// TestAssemblyFrameworkContracts_CellOwnedContract proves that listing a cell-owned
// contract in frameworkContracts produces 1 error.
func TestAssemblyFrameworkContracts_CellOwnedContract(t *testing.T) {
	const id = "http.auth.login.v1"
	cellContract := &metadata.ContractMeta{
		ID:               id,
		Kind:             "http",
		OwnerCell:        metadatatest.CellIDAccessCore, // cell-owned, not framework
		ConsistencyLevel: "L1",
		Lifecycle:        "active",
		Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDAccessCore},
		File:             "contracts/http/auth/login/v1/contract.yaml",
		Dir:              "contracts/http/auth/login/v1",
	}
	pm := frameworkOwnedProjectWithAssembly([]string{id}, cellContract)
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), codeFRAMEWORKOWNEDCONTRACTSCOPED01)
	if len(got) != 1 {
		t.Fatalf("cell-owned contract in frameworkContracts must produce 1 finding, got %d: %v", len(got), got)
	}
	if got[0].Field != "frameworkContracts" {
		t.Errorf("finding must anchor at frameworkContracts, got %q", got[0].Field)
	}
}

// TestAssemblyFrameworkContracts_DraftFrameworkContract proves that listing a draft
// framework contract in frameworkContracts (only active is valid) produces 1 error.
func TestAssemblyFrameworkContracts_DraftFrameworkContract(t *testing.T) {
	const id = "http.deviceidentity.enroll.v1"
	pm := frameworkOwnedProjectWithAssembly(
		[]string{id},
		fwHTTPContract(id, "draft"), // draft — only active should appear in frameworkContracts
	)
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), codeFRAMEWORKOWNEDCONTRACTSCOPED01)
	if len(got) != 1 {
		t.Fatalf("draft framework contract in frameworkContracts must produce 1 finding, got %d: %v", len(got), got)
	}
	if got[0].Field != "frameworkContracts" {
		t.Errorf("finding must anchor at frameworkContracts, got %q", got[0].Field)
	}
}

// TestAssemblyFrameworkContracts_IneligibleKindInList proves that listing a framework
// command contract (ineligible kind) in frameworkContracts produces an error.
func TestAssemblyFrameworkContracts_IneligibleKindInList(t *testing.T) {
	const id = "command.deviceidentity.rotate.v1"
	cmd := &metadata.ContractMeta{
		ID:               id,
		Kind:             "command",
		OwnerCell:        metadata.FrameworkOwnerSentinel,
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Endpoints:        metadata.EndpointsMeta{Handler: metadata.FrameworkOwnerSentinel},
		File:             "contracts/command/deviceidentity/rotate/v1/contract.yaml",
	}
	pm := frameworkOwnedProjectWithAssembly([]string{id}, cmd)
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateFRAMEWORKOWNEDCONTRACTSCOPED01(), codeFRAMEWORKOWNEDCONTRACTSCOPED01)
	// At least 1 finding from checkAssemblyFrameworkContracts (kind not eligible),
	// plus 1 from checkFrameworkOwnedKind. Total ≥ 2.
	if len(got) < 1 {
		t.Fatalf("ineligible kind in frameworkContracts must produce findings, got %d: %v", len(got), got)
	}
	// The assembly-level finding must anchor at frameworkContracts.
	var hasAssemblyFinding bool
	for _, r := range got {
		if r.Field == "frameworkContracts" {
			hasAssemblyFinding = true
		}
	}
	if !hasAssemblyFinding {
		t.Errorf("at least one finding must anchor at frameworkContracts; got: %v", got)
	}
}

// --- REF-06 framework sentinel tests (task 3) ---

// TestREF06_FrameworkSentinelPermitted proves that a journey declaring
// cells: [_framework] produces 0 REF-06 findings — _framework is the
// legitimate serving anchor for framework-serving journeys.
func TestREF06_FrameworkSentinelPermitted(t *testing.T) {
	pm := frameworkOwnedProject()
	pm.Journeys = map[string]*metadata.JourneyMeta{
		"J-fw-enroll": {
			ID:        "J-fw-enroll",
			Goal:      "Device enrolls via framework endpoint",
			Lifecycle: "experimental",
			Owner:     metadata.OwnerMeta{Team: "platform", Role: "journey-owner"},
			Cells:     []string{metadata.FrameworkOwnerSentinel}, // _framework serving anchor
			File:      "journeys/J-fw-enroll.yaml",
		},
	}
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateREF06(), codeREF06)
	if len(got) != 0 {
		t.Errorf("_framework cell ref in journey must produce 0 REF-06 findings, got %d: %v", len(got), got)
	}
}

// TestREF06_NonexistentCellStillFails is the anti-vacuity / preservation test:
// a truly nonexistent cell still produces a REF-06 error even after the
// _framework skip is applied.
func TestREF06_NonexistentCellStillFails(t *testing.T) {
	pm := frameworkOwnedProject()
	pm.Journeys = map[string]*metadata.JourneyMeta{
		"J-ghost": {
			ID:    "J-ghost",
			Goal:  "Ghost journey",
			Cells: []string{metadatatest.NewCellID("ghostcell")}, // format-valid but absent from the project
			File:  "journeys/J-ghost.yaml",
		},
	}
	val := NewValidator(pm, ".", clock.Real())

	got := findByCode(val.validateREF06(), codeREF06)
	if len(got) != 1 {
		t.Fatalf("nonexistent cell in journey must produce 1 REF-06 finding, got %d: %v", len(got), got)
	}
}
