package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
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
func TestFrameworkOwnedContract_ExcludedFromCellOwnerRules(t *testing.T) {
	pm := frameworkOwnedProject(
		fwHTTPContract("http.deviceidentity.enroll.v1", "draft"),
		fwEventContract("event.deviceidentity.cert-issued.v1", "draft"),
	)
	val := NewValidator(pm, ".", clock.Real())

	if got := findByCode(val.validateREF03(), "REF-03"); len(got) != 0 {
		t.Errorf("REF-03 must skip framework-owned contracts, got %d findings: %v", len(got), got)
	}
	if got := findByCode(val.validateREF13(), "REF-13"); len(got) != 0 {
		t.Errorf("REF-13 must skip framework-owned contracts, got %d findings: %v", len(got), got)
	}
	if got := findByCode(val.validateCONTRACTCONSISTENCYEMIT01(), "CONTRACT-CONSISTENCY-EMIT-01"); len(got) != 0 {
		t.Errorf("CCE-01 must skip framework-owned contracts, got %d findings: %v", len(got), got)
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
