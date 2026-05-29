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
			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d result(s), got %d: %v", tc.wantErrCount, len(got), got)
			}
			if tc.wantErrCount > 0 {
				r := got[0]
				if r.Severity != SeverityError {
					t.Errorf("expected SeverityError, got %s", r.Severity)
				}
				if r.Field != tc.wantField {
					t.Errorf("expected field %q, got %q", tc.wantField, r.Field)
				}
				if r.Fix == "" {
					t.Error("error finding must carry non-empty Fix guidance (typed-Fix contract)")
				}
			}
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
			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d result(s), got %d: %v", tc.wantErrCount, len(got), got)
			}
			if tc.wantErrCount > 0 {
				r := got[0]
				if r.Severity != SeverityError {
					t.Errorf("expected SeverityError, got %s", r.Severity)
				}
				if r.Field != tc.wantField {
					t.Errorf("expected field %q, got %q", tc.wantField, r.Field)
				}
				if r.Fix == "" {
					t.Error("error finding must carry non-empty Fix guidance (typed-Fix contract)")
				}
			}
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
			name: "three steps with two duplicates → two errors",
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
			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d result(s), got %d: %v", tc.wantErrCount, len(got), got)
			}
			if tc.wantErrCount > 0 {
				r := got[0]
				if r.Severity != SeverityError {
					t.Errorf("expected SeverityError, got %s", r.Severity)
				}
				if r.Field != tc.wantField {
					t.Errorf("expected field %q, got %q", tc.wantField, r.Field)
				}
				if r.Fix == "" {
					t.Error("error finding must carry non-empty Fix guidance (typed-Fix contract)")
				}
			}
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
			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d result(s), got %d: %v", tc.wantErrCount, len(got), got)
			}
			if tc.wantErrCount > 0 {
				r := got[0]
				if r.Severity != SeverityError {
					t.Errorf("expected SeverityError, got %s", r.Severity)
				}
				if r.Field != tc.wantField {
					t.Errorf("expected field %q, got %q", tc.wantField, r.Field)
				}
				if r.Fix == "" {
					t.Error("error finding must carry non-empty Fix guidance (typed-Fix contract)")
				}
			}
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
			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d result(s), got %d: %v", tc.wantErrCount, len(got), got)
			}
			if tc.wantErrCount > 0 {
				r := got[0]
				if r.Severity != SeverityError {
					t.Errorf("expected SeverityError, got %s", r.Severity)
				}
				if r.Field != tc.wantField {
					t.Errorf("expected field %q, got %q", tc.wantField, r.Field)
				}
				if r.Fix == "" {
					t.Error("error finding must carry non-empty Fix guidance (typed-Fix contract)")
				}
			}
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
		{
			name:             "non-saga kind → not checked (http at L2 is fine)",
			consistencyLevel: "L2",
			// use a non-saga contract via separate project
		},
	}

	for _, tc := range tests {
		tc := tc
		// Skip the non-saga test case with a comment; it shares infrastructure.
		if tc.name == "non-saga kind → not checked (http at L2 is fine)" {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				// Build an http contract with L2 — should not trigger saga rule.
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
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project := buildSagaProject(tc.consistencyLevel, wellFormedSaga())
			v := NewValidator(project, "", clock.Real())
			got := filterByCode(v.validateSAGACONTRACTCONSISTENCYL301(), codeSAGACONTRACTCONSISTENCYL301)
			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d result(s), got %d: %v", tc.wantErrCount, len(got), got)
			}
			if tc.wantErrCount > 0 {
				r := got[0]
				if r.Severity != SeverityError {
					t.Errorf("expected SeverityError, got %s", r.Severity)
				}
				if r.Field != tc.wantField {
					t.Errorf("expected field %q, got %q", tc.wantField, r.Field)
				}
				if r.Fix == "" {
					t.Error("error finding must carry non-empty Fix guidance (typed-Fix contract)")
				}
			}
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
			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d result(s), got %d: %v", tc.wantErrCount, len(got), got)
			}
			if tc.wantErrCount > 0 {
				r := got[0]
				if r.Severity != SeverityError {
					t.Errorf("expected SeverityError, got %s", r.Severity)
				}
				if r.Field != tc.wantField {
					t.Errorf("expected field %q, got %q", tc.wantField, r.Field)
				}
				if r.Fix == "" {
					t.Error("error finding must carry non-empty Fix guidance (typed-Fix contract)")
				}
			}
		})
	}
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
			if len(got) != tc.wantErrCount {
				t.Fatalf("expected %d result(s), got %d: %v", tc.wantErrCount, len(got), got)
			}
			if tc.wantErrCount > 0 {
				r := got[0]
				if r.Severity != SeverityError {
					t.Errorf("expected SeverityError, got %s", r.Severity)
				}
				if tc.wantField != "" && !strings.HasPrefix(r.Field, tc.wantField) {
					t.Errorf("expected field with prefix %q, got %q", tc.wantField, r.Field)
				}
				if r.Fix == "" {
					t.Error("error finding must carry non-empty Fix guidance (typed-Fix contract)")
				}
			}
		})
	}
}
