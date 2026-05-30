package governance

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

const sagaTestContractID = "saga.test.order.v1"

// buildSagaProject builds a minimal *metadata.ProjectMeta containing exactly
// one saga contract. saga may be nil to test the Saga==nil skip path.
func buildSagaProject(consistencyLevel string, saga *metadata.SagaMeta) *metadata.ProjectMeta {
	c := &metadata.ContractMeta{
		ID:               sagaTestContractID,
		Kind:             "saga",
		OwnerCell:        metadatatest.CellIDTestCell,
		ConsistencyLevel: consistencyLevel,
		Lifecycle:        "active",
		Endpoints: metadata.EndpointsMeta{
			Server: metadatatest.CellIDTestCell,
		},
		Saga: saga,
		Dir:  "contracts/saga/test/order/v1",
		File: "contracts/saga/test/order/v1/contract.yaml",
	}
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDTestCell: {
				ID:               metadatatest.CellIDTestCell,
				Type:             "core",
				ConsistencyLevel: "L3",
				DurabilityMode:   "durable",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_test"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.testcell.startup"}},
				Dir:              "cells/testcell",
				File:             "cells/testcell/cell.yaml",
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{sagaTestContractID: c},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// wellFormedSaga returns a SagaMeta that satisfies all 8 saga governance rules.
func wellFormedSaga() *metadata.SagaMeta {
	comp := true
	return &metadata.SagaMeta{
		CompensationOrder: "reverse",
		Steps: []metadata.SagaStepMeta{
			{Name: "reserve", Output: "schemas/reserve-output.json", Compensate: &comp},
			{Name: "charge", Output: "schemas/charge-output.json", Compensate: &comp},
		},
	}
}

// filterByCode filters a []ValidationResult slice to only those matching code.
func filterByCode(results []ValidationResult, code RuleCode) []ValidationResult {
	var out []ValidationResult
	for _, r := range results {
		if r.Code == code {
			out = append(out, r)
		}
	}
	return out
}

// assertSagaFinding checks that got contains exactly wantErrCount results and,
// when wantErrCount > 0, that the first result has SeverityError, Field ==
// wantField (empty wantField skips the field check), and a non-empty Fix.
// When wantErrCount > 1, all results are also verified to have SeverityError and
// non-empty Fix (Field varies per-finding so only the first is field-checked).
// Extracted to lower the cognitive complexity of each table-driven saga rule test.
func assertSagaFinding(t *testing.T, got []ValidationResult, wantErrCount int, wantField string) {
	t.Helper()
	if len(got) != wantErrCount {
		t.Fatalf("expected %d result(s), got %d: %v", wantErrCount, len(got), got)
	}
	if wantErrCount == 0 {
		return
	}
	// Verify the first result's field (callers only provide wantField for got[0]).
	r := got[0]
	if r.Severity != SeverityError {
		t.Errorf("got[0]: expected SeverityError, got %s", r.Severity)
	}
	if wantField != "" && r.Field != wantField {
		t.Errorf("got[0]: expected field %q, got %q", wantField, r.Field)
	}
	if r.Fix == "" {
		t.Error("got[0]: error finding must carry non-empty Fix guidance (typed-Fix contract)")
	}
	// For multi-finding cases, verify every result carries Severity + Fix.
	for i, ri := range got[1:] {
		if ri.Severity != SeverityError {
			t.Errorf("got[%d]: expected SeverityError, got %s", i+1, ri.Severity)
		}
		if ri.Fix == "" {
			t.Errorf("got[%d]: error finding must carry non-empty Fix guidance (typed-Fix contract)", i+1)
		}
	}
}

// assertSagaFindingPrefix is like assertSagaFinding but uses strings.HasPrefix
// for the field check (used by retry/timeout tests where field varies by subpath).
func assertSagaFindingPrefix(t *testing.T, got []ValidationResult, wantErrCount int, wantField string) {
	t.Helper()
	if len(got) != wantErrCount {
		t.Fatalf("expected %d result(s), got %d: %v", wantErrCount, len(got), got)
	}
	if wantErrCount == 0 {
		return
	}
	r := got[0]
	if r.Severity != SeverityError {
		t.Errorf("got[0]: expected SeverityError, got %s", r.Severity)
	}
	if wantField != "" && !strings.HasPrefix(r.Field, wantField) {
		t.Errorf("got[0]: expected field with prefix %q, got %q", wantField, r.Field)
	}
	if r.Fix == "" {
		t.Error("got[0]: error finding must carry non-empty Fix guidance (typed-Fix contract)")
	}
	// For multi-finding cases, verify every result carries Severity + Fix.
	for i, ri := range got[1:] {
		if ri.Severity != SeverityError {
			t.Errorf("got[%d]: expected SeverityError, got %s", i+1, ri.Severity)
		}
		if ri.Fix == "" {
			t.Errorf("got[%d]: error finding must carry non-empty Fix guidance (typed-Fix contract)", i+1)
		}
	}
}

// ---- SAGA-CONTRACT-STEPS-NONEMPTY-01 ----------------------------------------

func TestSagaStepsNonempty01(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		saga         *metadata.SagaMeta
		wantErrCount int
		wantField    string
	}{
		{
			name:         "empty steps slice → error",
			saga:         &metadata.SagaMeta{Steps: []metadata.SagaStepMeta{}},
			wantErrCount: 1,
			wantField:    "saga.steps",
		},
		{
			name:         "nil steps (zero-value SagaMeta) → error",
			saga:         &metadata.SagaMeta{},
			wantErrCount: 1,
			wantField:    "saga.steps",
		},
		{
			name:         "nil Saga pointer → skip (no finding)",
			saga:         nil,
			wantErrCount: 0,
		},
		{
			name:         "well-formed saga → no error",
			saga:         wellFormedSaga(),
			wantErrCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project := buildSagaProject("L3", tc.saga)
			v := NewValidator(project, "", clock.Real())
			got := filterByCode(v.validateSAGACONTRACTSTEPSNONEMPTY01(), codeSAGACONTRACTSTEPSNONEMPTY01)
			assertSagaFinding(t, got, tc.wantErrCount, tc.wantField)
		})
	}
}

// ---- SAGA-CONTRACT-STEP-NAME-VALID-01 ---------------------------------------

func TestSagaStepNameValid01(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		steps        []metadata.SagaStepMeta
		wantErrCount int
		wantField    string
	}{
		{
			name: "empty step name → error",
			steps: []metadata.SagaStepMeta{
				{Name: "", Output: "schemas/out.json"},
			},
			wantErrCount: 1,
			wantField:    "saga.steps[0].name",
		},
		{
			name: "step name with spaces → error",
			steps: []metadata.SagaStepMeta{
				{Name: "bad name", Output: "schemas/out.json"},
			},
			wantErrCount: 1,
			wantField:    "saga.steps[0].name",
		},
		{
			name: "valid camelCase names → no error",
			steps: []metadata.SagaStepMeta{
				{Name: "reserve", Output: "schemas/reserve.json"},
				{Name: "chargePayment", Output: "schemas/charge.json"},
				{Name: "step3", Output: "schemas/step3.json"},
			},
			wantErrCount: 0,
		},
		{
			// SafeID permits ._:/- but those break goPascalCase → uncompilable
			// Go; the rule is stricter than IsSafeID. Both must be rejected.
			name: "dot/dash names (valid SafeID but codegen-unsafe) → error",
			steps: []metadata.SagaStepMeta{
				{Name: "charge.v2", Output: "schemas/charge.json"},
				{Name: "step-3", Output: "schemas/step3.json"},
			},
			wantErrCount: 2,
			wantField:    "saga.steps[0].name",
		},
		{
			name:  "nil Saga → no error (skip)",
			steps: nil, // triggers nil saga path via buildSagaProject
		},
		{
			name:  "well-formed saga → no error",
			steps: wellFormedSaga().Steps,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var saga *metadata.SagaMeta
			if tc.steps != nil {
				saga = &metadata.SagaMeta{Steps: tc.steps}
			}
			project := buildSagaProject("L3", saga)
			v := NewValidator(project, "", clock.Real())
			got := filterByCode(v.validateSAGACONTRACTSTEPNAMEVALID01(), codeSAGACONTRACTSTEPNAMEVALID01)
			assertSagaFinding(t, got, tc.wantErrCount, tc.wantField)
		})
	}
}

// ---- SAGA-CONTRACT-STEP-NAME-UNIQUE-01 --------------------------------------

func TestSagaStepNameUnique01(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		steps        []metadata.SagaStepMeta
		wantErrCount int
		wantField    string
	}{
		{
			name: "duplicate step names → error",
			steps: []metadata.SagaStepMeta{
				{Name: "reserve", Output: "schemas/reserve.json"},
				{Name: "reserve", Output: "schemas/reserve2.json"},
			},
			wantErrCount: 1,
			wantField:    "saga.steps[1].name",
		},
		{
			name: "three steps, one duplicate → one error",
			steps: []metadata.SagaStepMeta{
				{Name: "reserve", Output: "schemas/reserve.json"},
				{Name: "charge", Output: "schemas/charge.json"},
				{Name: "reserve", Output: "schemas/reserve3.json"},
			},
			wantErrCount: 1,
			wantField:    "saga.steps[2].name",
		},
		{
			name: "unique names → no error",
			steps: []metadata.SagaStepMeta{
				{Name: "reserve", Output: "schemas/reserve.json"},
				{Name: "charge", Output: "schemas/charge.json"},
			},
			wantErrCount: 0,
		},
		{
			name:  "well-formed saga → no error",
			steps: wellFormedSaga().Steps,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			saga := &metadata.SagaMeta{Steps: tc.steps}
			project := buildSagaProject("L3", saga)
			v := NewValidator(project, "", clock.Real())
			got := filterByCode(v.validateSAGACONTRACTSTEPNAMEUNIQUE01(), codeSAGACONTRACTSTEPNAMEUNIQUE01)
			assertSagaFinding(t, got, tc.wantErrCount, tc.wantField)
		})
	}
}

// ---- SAGA-CONTRACT-STEP-SCHEMA-REF-01 ---------------------------------------

func TestSagaStepSchemaRef01(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		steps        []metadata.SagaStepMeta
		wantErrCount int
		wantField    string
	}{
		{
			name: "empty output ref → error",
			steps: []metadata.SagaStepMeta{
				{Name: "reserve", Output: ""},
			},
			wantErrCount: 1,
			wantField:    "saga.steps[0].output",
		},
		{
			name: "whitespace-only output ref → error",
			steps: []metadata.SagaStepMeta{
				{Name: "reserve", Output: "   "},
			},
			wantErrCount: 1,
			wantField:    "saga.steps[0].output",
		},
		{
			name: "non-empty output ref → no error",
			steps: []metadata.SagaStepMeta{
				{Name: "reserve", Output: "schemas/reserve-output.json"},
			},
			wantErrCount: 0,
		},
		{
			name:         "well-formed saga → no error",
			steps:        wellFormedSaga().Steps,
			wantErrCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			saga := &metadata.SagaMeta{Steps: tc.steps}
			project := buildSagaProject("L3", saga)
			v := NewValidator(project, "", clock.Real())
			got := filterByCode(v.validateSAGACONTRACTSTEPSCHEMAREF01(), codeSAGACONTRACTSTEPSCHEMAREF01)
			assertSagaFinding(t, got, tc.wantErrCount, tc.wantField)
		})
	}
}

// ---- SAGA-CONTRACT-COMPENSATION-ORDER-01 ------------------------------------

func TestSagaCompensationOrder01(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		compensationOrder string
		wantErrCount      int
		wantField         string
	}{
		{
			name:              "unsupported value 'forward' → error",
			compensationOrder: "forward",
			wantErrCount:      1,
			wantField:         "saga.compensationOrder",
		},
		{
			name:              "arbitrary string → error",
			compensationOrder: "breadth-first",
			wantErrCount:      1,
			wantField:         "saga.compensationOrder",
		},
		{
			name:              "empty compensationOrder → no error (treated as reverse)",
			compensationOrder: "",
			wantErrCount:      0,
		},
		{
			name:              "reverse → no error",
			compensationOrder: "reverse",
			wantErrCount:      0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			saga := &metadata.SagaMeta{
				CompensationOrder: tc.compensationOrder,
				Steps:             wellFormedSaga().Steps,
			}
			project := buildSagaProject("L3", saga)
			v := NewValidator(project, "", clock.Real())
			got := filterByCode(v.validateSAGACONTRACTCOMPENSATIONORDER01(), codeSAGACONTRACTCOMPENSATIONORDER01)
			assertSagaFinding(t, got, tc.wantErrCount, tc.wantField)
		})
	}
}

// ---- SAGA-CONTRACT-CONSISTENCY-L3-01 ----------------------------------------

func TestSagaConsistencyL301(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		consistencyLevel string
		wantErrCount     int
		wantField        string
	}{
		{
			name:             "L2 → error",
			consistencyLevel: "L2",
			wantErrCount:     1,
			wantField:        "consistencyLevel",
		},
		{
			name:             "L1 → error",
			consistencyLevel: "L1",
			wantErrCount:     1,
			wantField:        "consistencyLevel",
		},
		{
			name:             "L0 → error",
			consistencyLevel: "L0",
			wantErrCount:     1,
			wantField:        "consistencyLevel",
		},
		{
			name:             "L4 → error (saga must be exactly L3)",
			consistencyLevel: "L4",
			wantErrCount:     1,
			wantField:        "consistencyLevel",
		},
		{
			name:             "empty → error",
			consistencyLevel: "",
			wantErrCount:     1,
			wantField:        "consistencyLevel",
		},
		{
			name:             "L3 → no error",
			consistencyLevel: "L3",
			wantErrCount:     0,
		},
	}

	// non-saga contract at L2 must not trigger the saga rule (rule only fires for kind=saga).
	t.Run("non-saga kind → not checked (http at L2 is fine)", func(t *testing.T) {
		t.Parallel()
		c := &metadata.ContractMeta{
			ID:               "http.test.action.v1",
			Kind:             "http",
			OwnerCell:        metadatatest.CellIDTestCell,
			ConsistencyLevel: "L2",
			Lifecycle:        "active",
			Endpoints: metadata.EndpointsMeta{
				Server:  metadatatest.CellIDTestCell,
				Clients: []string{metadatatest.CellIDEdgeBFF},
			},
			Dir:  "contracts/http/test/action/v1",
			File: "contracts/http/test/action/v1/contract.yaml",
		}
		project := &metadata.ProjectMeta{
			Cells:      map[string]*metadata.CellMeta{},
			Slices:     map[string]*metadata.SliceMeta{},
			Contracts:  map[string]*metadata.ContractMeta{c.ID: c},
			Journeys:   map[string]*metadata.JourneyMeta{},
			Assemblies: map[string]*metadata.AssemblyMeta{},
		}
		v := NewValidator(project, "", clock.Real())
		got := filterByCode(v.validateSAGACONTRACTCONSISTENCYL301(), codeSAGACONTRACTCONSISTENCYL301)
		if len(got) != 0 {
			t.Fatalf("non-saga contract must not trigger SAGA-CONTRACT-CONSISTENCY-L3-01, got %d: %v", len(got), got)
		}
	})

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project := buildSagaProject(tc.consistencyLevel, wellFormedSaga())
			v := NewValidator(project, "", clock.Real())
			got := filterByCode(v.validateSAGACONTRACTCONSISTENCYL301(), codeSAGACONTRACTCONSISTENCYL301)
			assertSagaFinding(t, got, tc.wantErrCount, tc.wantField)
		})
	}
}

// ---- SAGA-CONTRACT-BLOCK-PRESENT-01 -----------------------------------------

func TestSagaContractBlockPresent01(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		saga         *metadata.SagaMeta
		wantErrCount int
		wantField    string
	}{
		{
			name:         "nil saga block → error",
			saga:         nil,
			wantErrCount: 1,
			wantField:    "saga",
		},
		{
			name:         "non-nil saga block (even empty) → no error from this rule",
			saga:         &metadata.SagaMeta{},
			wantErrCount: 0,
		},
		{
			name:         "well-formed saga → no error",
			saga:         wellFormedSaga(),
			wantErrCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project := buildSagaProject("L3", tc.saga)
			v := NewValidator(project, "", clock.Real())
			got := filterByCode(v.validateSAGACONTRACTBLOCKPRESENT01(), codeSAGACONTRACTBLOCKPRESENT01)
			assertSagaFinding(t, got, tc.wantErrCount, tc.wantField)
		})
	}

	// non-saga kind with nil saga block → no error (rule only fires on kind=saga).
	t.Run("non-saga contract, nil saga block → no error (skip)", func(t *testing.T) {
		t.Parallel()
		project := &metadata.ProjectMeta{
			Cells:    map[string]*metadata.CellMeta{},
			Slices:   map[string]*metadata.SliceMeta{},
			Journeys: map[string]*metadata.JourneyMeta{},
			Contracts: map[string]*metadata.ContractMeta{
				"http.order.v1": {
					ID:   "http.order.v1",
					Kind: "http",
					Saga: nil,
					File: "contracts/http/order/v1/contract.yaml",
				},
			},
			Assemblies: map[string]*metadata.AssemblyMeta{},
		}
		v := NewValidator(project, "", clock.Real())
		got := filterByCode(v.validateSAGACONTRACTBLOCKPRESENT01(), codeSAGACONTRACTBLOCKPRESENT01)
		if len(got) != 0 {
			t.Fatalf("non-saga contract must not trigger BLOCK-PRESENT, got %d: %v", len(got), got)
		}
	})
}

// ---- SAGA-CELL-LEVEL-L3-DECLARE-01 ------------------------------------------

// TestSagaCellLevelL3Declare01 tests the SAGA-CELL-LEVEL-L3-DECLARE-01 rule:
// a slice with contractUsage role=orchestrate must belong to a cell whose
// consistencyLevel is exactly L3.
//
// The rule is intentionally ONE-WAY (orchestrate ⟹ cell.L3); L3 cells that
// have no orchestrate CU are fine (CQRS projections, compliance-tracking cells
// exist as L3 long before saga). A two-way implication would turn those builds
// red. AI-robust evaluation: Medium — gocell validate is the real CI gate and
// covers in-memory ProjectMeta fixtures; schema enum is documentation only.
func TestSagaCellLevelL3Declare01(t *testing.T) {
	t.Parallel()

	// buildCellLevelProject builds a minimal project with one cell (given level)
	// and one slice with one contractUsage of the given role pointing at a saga
	// contract. The rule only fires for role=orchestrate on a kind=saga contract
	// whose cell is not L3 (missing / non-saga contracts are owned by REF/TOPO-01
	// and skipped here).
	buildCellLevelProject := func(cellLevel, role string) *metadata.ProjectMeta {
		return &metadata.ProjectMeta{
			Cells: map[string]*metadata.CellMeta{
				metadatatest.NewCellID("orchestratorcell"): {
					ID:               metadatatest.NewCellID("orchestratorcell"),
					ConsistencyLevel: cellLevel,
					File:             "cells/orchestratorcell/cell.yaml",
				},
			},
			Slices: map[string]*metadata.SliceMeta{
				"orchestratorcell/saga-orchestrate": {
					ID:            "saga-orchestrate",
					BelongsToCell: metadatatest.NewCellID("orchestratorcell"),
					ContractUsages: []metadata.ContractUsage{
						{Contract: "saga.order.v1", Role: role},
					},
					File: "cells/orchestratorcell/slices/saga-orchestrate/slice.yaml",
				},
			},
			Contracts: map[string]*metadata.ContractMeta{
				"saga.order.v1": {ID: "saga.order.v1", Kind: "saga"},
			},
			Journeys:   map[string]*metadata.JourneyMeta{},
			Assemblies: map[string]*metadata.AssemblyMeta{},
		}
	}

	tests := []struct {
		name         string
		cellLevel    string
		role         string
		wantErrCount int
		wantField    string
	}{
		{
			name:         "orchestrate on L3 cell → no error (valid)",
			cellLevel:    "L3",
			role:         "orchestrate",
			wantErrCount: 0,
		},
		{
			name:         "orchestrate on L2 cell → error",
			cellLevel:    "L2",
			role:         "orchestrate",
			wantErrCount: 1,
			wantField:    "contractUsages[0].role",
		},
		{
			name:         "orchestrate on L1 cell → error",
			cellLevel:    "L1",
			role:         "orchestrate",
			wantErrCount: 1,
			wantField:    "contractUsages[0].role",
		},
		{
			name:         "orchestrate on L0 cell → error",
			cellLevel:    "L0",
			role:         "orchestrate",
			wantErrCount: 1,
			wantField:    "contractUsages[0].role",
		},
		{
			name:         "orchestrate on L4 cell → error",
			cellLevel:    "L4",
			role:         "orchestrate",
			wantErrCount: 1,
			wantField:    "contractUsages[0].role",
		},
		{
			name:         "orchestrate on empty level cell → error",
			cellLevel:    "",
			role:         "orchestrate",
			wantErrCount: 1,
			wantField:    "contractUsages[0].role",
		},
		{
			name:         "subscribe role on L2 cell → no error (rule only fires on orchestrate)",
			cellLevel:    "L2",
			role:         "subscribe",
			wantErrCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project := buildCellLevelProject(tc.cellLevel, tc.role)
			v := NewValidator(project, "", clock.Real())
			got := filterByCode(v.validateSAGACELLLEVELL3DECLARE01(), codeSAGACELLLEVELL3DECLARE01)
			assertSagaFinding(t, got, tc.wantErrCount, tc.wantField)
		})
	}

	// belongsToCell not in Cells map → no error (REF already covers missing
	// cell declarations; SAGA-CELL-LEVEL-L3-DECLARE-01 must not double-report).
	t.Run("orchestrate with unknown cell → no error (REF scope)", func(t *testing.T) {
		t.Parallel()
		project := &metadata.ProjectMeta{
			Cells: map[string]*metadata.CellMeta{},
			Slices: map[string]*metadata.SliceMeta{
				"unknowncell/saga-orchestrate": {
					ID:            "saga-orchestrate",
					BelongsToCell: metadatatest.NewCellID("unknowncell"),
					ContractUsages: []metadata.ContractUsage{
						{Contract: "saga.order.v1", Role: "orchestrate"},
					},
					File: "cells/unknowncell/slices/saga-orchestrate/slice.yaml",
				},
			},
			Contracts:  map[string]*metadata.ContractMeta{},
			Journeys:   map[string]*metadata.JourneyMeta{},
			Assemblies: map[string]*metadata.AssemblyMeta{},
		}
		v := NewValidator(project, "", clock.Real())
		got := filterByCode(v.validateSAGACELLLEVELL3DECLARE01(), codeSAGACELLLEVELL3DECLARE01)
		if len(got) != 0 {
			t.Fatalf("unknown cell must not trigger rule (REF scope), got %d: %v", len(got), got)
		}
	})

	// orchestrate role on a non-saga contract → no error. TOPO-01 owns the
	// role/kind legality finding; SAGA-CELL-LEVEL-L3-DECLARE-01 must not
	// double-report on this already-broken config (symmetric with unknown-cell).
	t.Run("orchestrate on non-saga contract → no error (TOPO scope)", func(t *testing.T) {
		t.Parallel()
		project := &metadata.ProjectMeta{
			Cells: map[string]*metadata.CellMeta{
				metadatatest.NewCellID("orchestratorcell"): {
					ID: metadatatest.NewCellID("orchestratorcell"), ConsistencyLevel: "L2",
					File: "cells/orchestratorcell/cell.yaml",
				},
			},
			Slices: map[string]*metadata.SliceMeta{
				"orchestratorcell/bad-orchestrate": {
					ID:            "bad-orchestrate",
					BelongsToCell: metadatatest.NewCellID("orchestratorcell"),
					ContractUsages: []metadata.ContractUsage{
						{Contract: "http.order.v1", Role: "orchestrate"},
					},
					File: "cells/orchestratorcell/slices/bad-orchestrate/slice.yaml",
				},
			},
			Contracts: map[string]*metadata.ContractMeta{
				"http.order.v1": {ID: "http.order.v1", Kind: "http"},
			},
			Journeys:   map[string]*metadata.JourneyMeta{},
			Assemblies: map[string]*metadata.AssemblyMeta{},
		}
		v := NewValidator(project, "", clock.Real())
		got := filterByCode(v.validateSAGACELLLEVELL3DECLARE01(), codeSAGACELLLEVELL3DECLARE01)
		if len(got) != 0 {
			t.Fatalf("orchestrate on non-saga contract must not trigger rule (TOPO scope), got %d: %v", len(got), got)
		}
	})
}

// ---- SAGA-CONTRACT-RETRY-TIMEOUT-01 -----------------------------------------

func TestSagaContractRetryTimeout01(t *testing.T) {
	t.Parallel()

	retryMeta := func(maxAttempts int, base, max string) *metadata.SagaRetryMeta {
		return &metadata.SagaRetryMeta{MaxAttempts: maxAttempts, BaseInterval: base, MaxInterval: max}
	}

	tests := []struct {
		name         string
		saga         *metadata.SagaMeta
		wantErrCount int
		wantField    string
	}{
		{
			name:         "nil saga block → no finding (skip)",
			saga:         nil,
			wantErrCount: 0,
		},
		{
			name:         "well-formed saga, no retries or timeout → no finding",
			saga:         wellFormedSaga(),
			wantErrCount: 0,
		},
		{
			name: "valid saga-level retries → no finding",
			saga: &metadata.SagaMeta{
				Steps:   wellFormedSaga().Steps,
				Retries: retryMeta(3, "1s", "30s"),
			},
			wantErrCount: 0,
		},
		{
			name: "valid saga-level timeout → no finding",
			saga: &metadata.SagaMeta{
				Steps:   wellFormedSaga().Steps,
				Timeout: "5m",
			},
			wantErrCount: 0,
		},
		{
			name: "maxAttempts=0 → no finding (0 means inherit, not unlimited)",
			saga: &metadata.SagaMeta{
				Steps:   wellFormedSaga().Steps,
				Retries: retryMeta(0, "1s", "30s"),
			},
			wantErrCount: 0,
		},
		{
			name: "maxInterval < baseInterval → error",
			saga: &metadata.SagaMeta{
				Steps:   wellFormedSaga().Steps,
				Retries: retryMeta(3, "30s", "1s"),
			},
			wantErrCount: 1,
			wantField:    "saga.retries",
		},
		{
			name: "negative maxAttempts → error (rule triggers RetryPolicy.Validate)",
			saga: &metadata.SagaMeta{
				Steps:   wellFormedSaga().Steps,
				Retries: retryMeta(-1, "1s", "30s"),
			},
			wantErrCount: 1,
			wantField:    "saga.retries",
		},
		{
			name: "unparseable saga-level timeout → error",
			saga: &metadata.SagaMeta{
				Steps:   wellFormedSaga().Steps,
				Timeout: "not-a-duration",
			},
			wantErrCount: 1,
			wantField:    "saga.timeout",
		},
		{
			name: "unparseable step timeout → error",
			saga: &metadata.SagaMeta{
				Steps: []metadata.SagaStepMeta{
					{Name: "reserve", Output: "schemas/reserve-output.json", Timeout: "bad"},
				},
			},
			wantErrCount: 1,
			wantField:    "saga.steps[0].timeout",
		},
		{
			name: "unparseable step retry baseInterval → error",
			saga: &metadata.SagaMeta{
				Steps: []metadata.SagaStepMeta{
					{
						Name: "reserve", Output: "schemas/reserve-output.json",
						Retries: &metadata.SagaRetryMeta{BaseInterval: "bad"},
					},
				},
			},
			wantErrCount: 1,
			wantField:    "saga.steps[0].retries.baseInterval",
		},
		{
			name: "valid step-level retries → no finding",
			saga: &metadata.SagaMeta{
				Steps: []metadata.SagaStepMeta{
					{
						Name: "reserve", Output: "schemas/reserve-output.json",
						Retries: retryMeta(2, "500ms", "10s"),
					},
				},
			},
			wantErrCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project := buildSagaProject("L3", tc.saga)
			v := NewValidator(project, "", clock.Real())
			got := filterByCode(v.validateSAGACONTRACTRETRYTIMEOUT01(), codeSAGACONTRACTRETRYTIMEOUT01)
			assertSagaFindingPrefix(t, got, tc.wantErrCount, tc.wantField)
		})
	}
}
