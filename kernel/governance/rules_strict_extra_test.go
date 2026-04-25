package governance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- FMT-20 (HTTP schema strict additionalProperties) ---

// TestFMTResponseStrict01_TopLevelMissingAdditionalProperties tests that
// FMT-20 fires when a top-level object in a schema lacks additionalProperties:false.
func TestFMTResponseStrict01_TopLevelMissingAdditionalProperties(t *testing.T) {
	// Build a temp dir with two schema files:
	// 1. response.schema.json: top-level object missing additionalProperties
	// 2. request.schema.json: has additionalProperties:false (should not fire)
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "test", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))

	// response missing additionalProperties at top level AND nested object
	responsePath := filepath.Join(contractDir, "response.schema.json")
	responseContent := `{
		"type": "object",
		"properties": {
			"data": {
				"type": "object",
				"properties": {
					"id": {"type": "string"}
				}
			}
		}
	}`
	require.NoError(t, os.WriteFile(responsePath, []byte(responseContent), 0o644))

	// request has additionalProperties:false (clean)
	requestPath := filepath.Join(contractDir, "request.schema.json")
	requestContent := `{"type": "object", "additionalProperties": false}`
	require.NoError(t, os.WriteFile(requestPath, []byte(requestContent), 0o644))

	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.test.v1": {
				ID:        "http.test.v1",
				Kind:      "http",
				OwnerCell: "testcell",
				Lifecycle: "active",
				SchemaRefs: metadata.SchemaRefsMeta{
					Request:  "request.schema.json",
					Response: "response.schema.json",
				},
				Dir:  "contracts/http/test/v1",
				File: "contracts/http/test/v1/contract.yaml",
			},
		},
		Journeys:    map[string]*metadata.JourneyMeta{},
		Assemblies:  map[string]*metadata.AssemblyMeta{},
		StatusBoard: nil,
	}

	v := NewValidator(pm, dir)
	results := v.Validate()

	matches := findByCode(results, "FMT-20")
	// Should fire for:
	// - top-level of response.schema.json ("$")
	// - nested $.data object
	// Total: 2 violations
	assert.Len(t, matches, 2,
		"expected 2 FMT-20 violations (top-level + nested data), got %d: %v",
		len(matches), matches)
	for _, r := range matches {
		assert.Equal(t, SeverityError, r.Severity)
	}
}

// TestFMTResponseStrict01_CleanSchema verifies no violation when all objects
// have additionalProperties:false.
func TestFMTResponseStrict01_CleanSchema(t *testing.T) {
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "clean", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))

	responsePath := filepath.Join(contractDir, "response.schema.json")
	responseContent := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"data": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"id": {"type": "string"}
				}
			}
		}
	}`
	require.NoError(t, os.WriteFile(responsePath, []byte(responseContent), 0o644))

	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.clean.v1": {
				ID:        "http.clean.v1",
				Kind:      "http",
				OwnerCell: "testcell",
				Lifecycle: "active",
				SchemaRefs: metadata.SchemaRefsMeta{
					Response: "response.schema.json",
				},
				Dir:  "contracts/http/clean/v1",
				File: "contracts/http/clean/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(pm, dir)
	results := v.Validate()
	matches := findByCode(results, "FMT-20")
	assert.Empty(t, matches, "clean schema should produce no FMT-20 violations")
}

// TestFMTResponseStrict01_NonHTTPContractIgnored verifies that non-HTTP
// contracts are not scanned.
func TestFMTResponseStrict01_NonHTTPContractIgnored(t *testing.T) {
	dir := t.TempDir()
	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.test.v1": {
				ID:        "event.test.v1",
				Kind:      "event",
				OwnerCell: "testcell",
				Lifecycle: "active",
				SchemaRefs: metadata.SchemaRefsMeta{
					Payload: "payload.schema.json",
				},
				Dir:  "contracts/event/test/v1",
				File: "contracts/event/test/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(pm, dir)
	results := v.Validate()
	matches := findByCode(results, "FMT-20")
	assert.Empty(t, matches, "non-HTTP contract should not be scanned by FMT-20")
}

// --- FMT-21 (contract dir ↔ ID match) ---

// TestFMTContractDirIDMatch01_Mismatch verifies that a contract whose Dir does
// not match the ID-derived path emits a FMT-21 violation.
func TestFMTContractDirIDMatch01_Mismatch(t *testing.T) {
	tests := []struct {
		name        string
		contractID  string
		contractDir string
		wantCount   int
	}{
		{
			name:        "correct dir",
			contractID:  "http.auth.login.v1",
			contractDir: "contracts/http/auth/login/v1",
			wantCount:   0,
		},
		{
			name:        "wrong dir",
			contractID:  "http.auth.login.v1",
			contractDir: "contracts/http/auth/register/v1",
			wantCount:   1,
		},
		{
			name:        "event contract correct",
			contractID:  "event.session.created.v1",
			contractDir: "contracts/event/session/created/v1",
			wantCount:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := &metadata.ProjectMeta{
				Cells:  map[string]*metadata.CellMeta{},
				Slices: map[string]*metadata.SliceMeta{},
				Contracts: map[string]*metadata.ContractMeta{
					tc.contractID: {
						ID:        tc.contractID,
						Kind:      "http",
						OwnerCell: "testcell",
						Lifecycle: "active",
						Dir:       tc.contractDir,
						File:      tc.contractDir + "/contract.yaml",
					},
				},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}

			v := NewValidator(pm, "")
			results := v.Validate()
			matches := findByCode(results, "FMT-21")
			assert.Len(t, matches, tc.wantCount,
				"test %q: expected %d violations, got %d: %v",
				tc.name, tc.wantCount, len(matches), matches)
		})
	}
}

// TestFMTContractDirIDMatch01_ExamplesPrefix verifies that contracts living
// under an examples/* subtree are accepted as long as the segment after the
// last "contracts/" separator matches the ID-derived suffix.
func TestFMTContractDirIDMatch01_ExamplesPrefix(t *testing.T) {
	tests := []struct {
		name        string
		contractID  string
		contractDir string
		wantCount   int
	}{
		{
			name:        "examples/iotdevice prefix — correct suffix",
			contractID:  "http.bar.v1",
			contractDir: "examples/foo/contracts/http/bar/v1",
			wantCount:   0,
		},
		{
			name:        "examples/todoorder prefix — correct suffix",
			contractID:  "event.device.registered.v1",
			contractDir: "examples/iotdevice/contracts/event/device/registered/v1",
			wantCount:   0,
		},
		{
			name:        "examples prefix — wrong suffix must still fire",
			contractID:  "http.bar.v1",
			contractDir: "examples/foo/contracts/http/baz/v1",
			wantCount:   1,
		},
		{
			name:        "no contracts/ segment in dir — violation",
			contractID:  "http.bar.v1",
			contractDir: "examples/foo/http/bar/v1",
			wantCount:   1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := &metadata.ProjectMeta{
				Cells:  map[string]*metadata.CellMeta{},
				Slices: map[string]*metadata.SliceMeta{},
				Contracts: map[string]*metadata.ContractMeta{
					tc.contractID: {
						ID:        tc.contractID,
						Kind:      "http",
						OwnerCell: "testcell",
						Lifecycle: "active",
						Dir:       tc.contractDir,
						File:      tc.contractDir + "/contract.yaml",
					},
				},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}

			v := NewValidator(pm, "")
			results := v.Validate()
			matches := findByCode(results, "FMT-21")
			assert.Len(t, matches, tc.wantCount,
				"test %q: expected %d violations, got %d: %v",
				tc.name, tc.wantCount, len(matches), matches)
		})
	}
}

// --- FMT-22 (status-board state enum) ---

// TestStatusBoardStateEnum01 verifies that invalid state values are flagged.
func TestStatusBoardStateEnum01(t *testing.T) {
	tests := []struct {
		name      string
		entries   []metadata.StatusBoardEntry
		wantCount int
	}{
		{
			name: "all valid states",
			entries: []metadata.StatusBoardEntry{
				{JourneyID: "J-login", State: "todo"},
				{JourneyID: "J-audit", State: "doing"},
				{JourneyID: "J-report", State: "done"},
			},
			wantCount: 0,
		},
		{
			name: "invalid WIP state",
			entries: []metadata.StatusBoardEntry{
				{JourneyID: "J-login", State: "WIP"},
			},
			wantCount: 1,
		},
		{
			name: "multiple invalid states",
			entries: []metadata.StatusBoardEntry{
				{JourneyID: "J-a", State: "in-progress"},
				{JourneyID: "J-b", State: "doing"},
				{JourneyID: "J-c", State: "pending"},
			},
			wantCount: 2,
		},
		{
			name:      "empty board",
			entries:   nil,
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := &metadata.ProjectMeta{
				Cells:       map[string]*metadata.CellMeta{},
				Slices:      map[string]*metadata.SliceMeta{},
				Contracts:   map[string]*metadata.ContractMeta{},
				Journeys:    map[string]*metadata.JourneyMeta{},
				Assemblies:  map[string]*metadata.AssemblyMeta{},
				StatusBoard: tc.entries,
			}

			v := NewValidator(pm, "")
			results := v.Validate()
			matches := findByCode(results, "FMT-22")
			assert.Len(t, matches, tc.wantCount,
				"test %q: expected %d violations, got %d: %v",
				tc.name, tc.wantCount, len(matches), matches)
			for _, r := range matches {
				assert.Equal(t, SeverityError, r.Severity)
			}
		})
	}
}

// --- FMT-23 (contract deprecated cleanup) ---

// TestContractDeprecatedCleanup01 verifies the three deprecation violation cases.
func TestContractDeprecatedCleanup01(t *testing.T) {
	tests := []struct {
		name         string
		lifecycle    string
		deprecatedAt string
		wantCount    int
		wantSev      Severity
		wantField    string
	}{
		{
			name:      "active contract, no deprecatedAt — no violation",
			lifecycle: "active",
			wantCount: 0,
		},
		{
			name:         "deprecated missing deprecatedAt — error",
			lifecycle:    "deprecated",
			deprecatedAt: "",
			wantCount:    1,
			wantSev:      SeverityError,
			wantField:    "deprecatedAt",
		},
		{
			name:         "deprecated malformed date — error",
			lifecycle:    "deprecated",
			deprecatedAt: "not-a-date",
			wantCount:    1,
			wantSev:      SeverityError,
			wantField:    "deprecatedAt",
		},
		{
			name:         "deprecated >90d old — warning",
			lifecycle:    "deprecated",
			deprecatedAt: "2020-01-01",
			wantCount:    1,
			wantSev:      SeverityWarning,
			wantField:    "lifecycle",
		},
		{
			name:         "deprecated recent (<90d) — no violation",
			lifecycle:    "deprecated",
			deprecatedAt: time.Now().AddDate(0, 0, -30).Format("2006-01-02"),
			wantCount:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := &metadata.ProjectMeta{
				Cells:  map[string]*metadata.CellMeta{},
				Slices: map[string]*metadata.SliceMeta{},
				Contracts: map[string]*metadata.ContractMeta{
					"http.test.deprecated.v1": {
						ID:           "http.test.deprecated.v1",
						Kind:         "http",
						OwnerCell:    "testcell",
						Lifecycle:    tc.lifecycle,
						DeprecatedAt: tc.deprecatedAt,
						Dir:          "contracts/http/test/deprecated/v1",
						File:         "contracts/http/test/deprecated/v1/contract.yaml",
					},
				},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}

			v := NewValidator(pm, "")
			results := v.Validate()
			matches := findByCode(results, "FMT-23")
			require.Len(t, matches, tc.wantCount,
				"test %q: expected %d violations, got %d: %v",
				tc.name, tc.wantCount, len(matches), matches)
			if tc.wantCount > 0 {
				assert.Equal(t, tc.wantSev, matches[0].Severity,
					"test %q: wrong severity", tc.name)
				assert.Equal(t, tc.wantField, matches[0].Field,
					"test %q: wrong field", tc.name)
			}
		})
	}
}

// TestFMT20_ArrayItemsObjectMissingAdditionalProperties verifies FMT-20 fires
// for an "items" object inside an array property that lacks additionalProperties.
func TestFMT20_ArrayItemsObjectMissingAdditionalProperties(t *testing.T) {
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "arraytest", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))

	// Schema: top-level object with additionalProperties:false, has an "items"
	// array whose items is an object missing additionalProperties.
	responsePath := filepath.Join(contractDir, "response.schema.json")
	responseContent := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"list": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "string"}
					}
				}
			}
		}
	}`
	require.NoError(t, os.WriteFile(responsePath, []byte(responseContent), 0o644))

	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.arraytest.v1": {
				ID:        "http.arraytest.v1",
				Kind:      "http",
				OwnerCell: "testcell",
				Lifecycle: "active",
				SchemaRefs: metadata.SchemaRefsMeta{
					Response: "response.schema.json",
				},
				Dir:  "contracts/http/arraytest/v1",
				File: "contracts/http/arraytest/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(pm, dir)
	results := v.Validate()
	matches := findByCode(results, "FMT-20")
	// Should fire for $.list.items — the array items object lacks additionalProperties.
	assert.Len(t, matches, 1,
		"expected 1 FMT-20 violation at $.list.items, got %d: %v", len(matches), matches)
	if len(matches) == 1 {
		assert.Equal(t, "$.list.items", matches[0].Field,
			"violation field must point to the items object path")
	}
}

// TestFMT22_EmptyStateViolation verifies FMT-22 fires when state is empty string.
func TestFMT22_EmptyStateViolation(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		StatusBoard: []metadata.StatusBoardEntry{
			{JourneyID: "J-empty", State: ""},
		},
	}

	v := NewValidator(pm, "")
	results := v.Validate()
	matches := findByCode(results, "FMT-22")
	assert.Len(t, matches, 1,
		"empty state must produce 1 FMT-22 violation, got %d: %v", len(matches), matches)
	if len(matches) == 1 {
		assert.Equal(t, SeverityError, matches[0].Severity)
	}
}

// TestFMT23_DeprecatedCleanup_BoundaryCheck verifies the 90-day boundary.
// Note: the check uses time.Parse (midnight UTC) vs time.Now().UTC() (current time),
// so "N days ago" means midnight of that date. With 89 days the difference is
// < 90 days + intraday remainder, guaranteeing no warning. With 91 days the
// difference exceeds 90 days even at midnight, guaranteeing a warning.
func TestFMT23_DeprecatedCleanup_BoundaryCheck(t *testing.T) {
	tests := []struct {
		name      string
		daysAgo   int
		wantCount int
	}{
		{
			name:      "89 days ago — no warning (well within 90d)",
			daysAgo:   89,
			wantCount: 0,
		},
		{
			name:      "91 days ago — warning fires (>90d)",
			daysAgo:   91,
			wantCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deprecatedDate := time.Now().UTC().AddDate(0, 0, -tc.daysAgo).Format("2006-01-02")
			pm := &metadata.ProjectMeta{
				Cells:  map[string]*metadata.CellMeta{},
				Slices: map[string]*metadata.SliceMeta{},
				Contracts: map[string]*metadata.ContractMeta{
					"http.test.old.v1": {
						ID:           "http.test.old.v1",
						Kind:         "http",
						OwnerCell:    "testcell",
						Lifecycle:    "deprecated",
						DeprecatedAt: deprecatedDate,
						Dir:          "contracts/http/test/old/v1",
						File:         "contracts/http/test/old/v1/contract.yaml",
					},
				},
				Journeys:   map[string]*metadata.JourneyMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			}

			v := NewValidator(pm, "")
			results := v.Validate()
			matches := findByCode(results, "FMT-23")
			// Filter to warnings only (we don't want IssueRequired or IssueInvalid counts).
			var warnings []ValidationResult
			for _, r := range matches {
				if r.Severity == SeverityWarning {
					warnings = append(warnings, r)
				}
			}
			assert.Len(t, warnings, tc.wantCount,
				"test %q: expected %d FMT-23 warnings, got %d: %v",
				tc.name, tc.wantCount, len(warnings), warnings)
		})
	}
}

// --- scanSchemaForStrictMissing helper (unit) ---

// TestScanSchemaForStrictMissing_FileNotFound verifies that a non-existent schema
// path returns an error from scanSchemaForStrictMissing.
func TestScanSchemaForStrictMissing_FileNotFound(t *testing.T) {
	_, err := scanSchemaForStrictMissing("/nonexistent/path/schema.json")
	require.Error(t, err, "missing file must return an error")
}

// TestScanSchemaForStrictMissing_InvalidJSON verifies that malformed JSON content
// returns an error from scanSchemaForStrictMissing.
func TestScanSchemaForStrictMissing_InvalidJSON(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "bad-*.json")
	require.NoError(t, err)
	_, err = f.Write([]byte("not valid json {{{"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, err = scanSchemaForStrictMissing(f.Name())
	require.Error(t, err, "malformed JSON must return an error")
	assert.Contains(t, err.Error(), "invalid JSON schema")
}

// TestWalkSchemaObjectDepth_DepthGuard verifies that walkSchemaObjectDepth
// terminates cleanly at depth > 32 and does not recurse indefinitely.
func TestWalkSchemaObjectDepth_DepthGuard(t *testing.T) {
	// Build a deeply nested schema (34 levels) that would recurse infinitely
	// without the depth guard. Each level wraps the next in a "nested" property.
	buildNested := func(depth int) map[string]any {
		inner := map[string]any{
			"type": "object",
			// No additionalProperties — would be a violation at every level.
		}
		current := inner
		for i := 0; i < depth; i++ {
			next := map[string]any{
				"type": "object",
				"properties": map[string]any{
					"nested": current,
				},
			}
			current = next
		}
		return current
	}

	deepSchema := buildNested(35)
	var missing []string
	// Should not panic or run forever; returns after the depth guard fires.
	walkSchemaObject(deepSchema, "$", &missing)
	// Some violations found (top levels) but the guard stops infinite recursion.
	assert.NotPanics(t, func() {
		walkSchemaObject(deepSchema, "$", &missing)
	})
}

// TestCheckAdditionalProperties_ObjectValueTreatedAsMissing verifies that
// additionalProperties set to an object (not bool false) is treated as a violation.
func TestCheckAdditionalProperties_ObjectValueTreatedAsMissing(t *testing.T) {
	node := map[string]any{
		"type": "object",
		// additionalProperties is a schema object, not bool false — counts as missing.
		"additionalProperties": map[string]any{"type": "string"},
	}
	var missing []string
	checkAdditionalProperties(node, "$", &missing)
	assert.Equal(t, []string{"$"}, missing,
		"additionalProperties as object value must be treated as missing")
}

// TestCheckAdditionalProperties_TrueValueAccepted verifies that an explicit
// additionalProperties: true is accepted (author chose open schema intentionally).
func TestCheckAdditionalProperties_TrueValueAccepted(t *testing.T) {
	node := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
	}
	var missing []string
	checkAdditionalProperties(node, "$", &missing)
	assert.Empty(t, missing,
		"additionalProperties:true must be accepted — author explicitly declared open schema")
}

// TestCheckAdditionalProperties_FalseValueAccepted verifies that an explicit
// additionalProperties: false is accepted (author chose strict schema).
func TestCheckAdditionalProperties_FalseValueAccepted(t *testing.T) {
	node := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}
	var missing []string
	checkAdditionalProperties(node, "$", &missing)
	assert.Empty(t, missing,
		"additionalProperties:false must be accepted — author explicitly declared strict schema")
}

// TestCheckAdditionalProperties_MissingViolation verifies that a missing
// additionalProperties key triggers a violation.
func TestCheckAdditionalProperties_MissingViolation(t *testing.T) {
	node := map[string]any{
		"type": "object",
	}
	var missing []string
	checkAdditionalProperties(node, "$", &missing)
	assert.Equal(t, []string{"$"}, missing,
		"missing additionalProperties must emit a violation")
}

// TestFMT20_EndpointResponseSchemaRef verifies that FMT-20 also scans schemas
// declared in endpoints.http.responses[*].schemaRef (A.1).
func TestFMT20_EndpointResponseSchemaRef(t *testing.T) {
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "errtest", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))

	// Error response schema missing additionalProperties:false at top level.
	errSchemaPath := filepath.Join(contractDir, "error-404.schema.json")
	require.NoError(t, os.WriteFile(errSchemaPath, []byte(`{"type":"object","properties":{"message":{"type":"string"}}}`), 0o644))

	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.errtest.v1": {
				ID:        "http.errtest.v1",
				Kind:      "http",
				OwnerCell: "testcell",
				Lifecycle: "active",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "GET",
						Path:          "/test",
						SuccessStatus: 200,
						Responses: map[int]metadata.HTTPResponseMeta{
							404: {Description: "Not found", SchemaRef: "error-404.schema.json"},
						},
					},
				},
				Dir:  "contracts/http/errtest/v1",
				File: "contracts/http/errtest/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(pm, dir)
	results := v.Validate()
	matches := findByCode(results, "FMT-20")
	assert.Len(t, matches, 1,
		"FMT-20 must fire for endpoints.http.responses[404].schemaRef missing additionalProperties:false, got %d: %v",
		len(matches), matches)
}

// TestFMT20_MalformedSchemaEmitsIssueInvalid verifies that A.2: a malformed JSON
// schema (not a missing file) produces a FMT-20 violation with IssueInvalid.
func TestFMT20_MalformedSchemaEmitsIssueInvalid(t *testing.T) {
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "badschema", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))

	// Write a malformed JSON file.
	badSchemaPath := filepath.Join(contractDir, "response.schema.json")
	require.NoError(t, os.WriteFile(badSchemaPath, []byte(`not valid json {{{`), 0o644))

	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.badschema.v1": {
				ID:        "http.badschema.v1",
				Kind:      "http",
				OwnerCell: "testcell",
				Lifecycle: "active",
				SchemaRefs: metadata.SchemaRefsMeta{
					Response: "response.schema.json",
				},
				Dir:  "contracts/http/badschema/v1",
				File: "contracts/http/badschema/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(pm, dir)
	results := v.Validate()
	matches := findByCode(results, "FMT-20")
	require.Len(t, matches, 1,
		"malformed JSON schema must produce 1 FMT-20 violation, got %d: %v", len(matches), matches)
	assert.Equal(t, IssueInvalid, matches[0].IssueType,
		"malformed schema violation must use IssueInvalid")
	assert.Equal(t, SeverityError, matches[0].Severity)
	assert.Contains(t, matches[0].Message, "failed to parse",
		"violation message must mention parse failure")
}

// TestFMT20_MissingSchemaFileSkipped verifies that FMT-20 silently skips contracts
// whose schema files don't exist (those are caught by FMT-09 / REF rules).
func TestFMT20_MissingSchemaFileSkipped(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.missing.schema.v1": {
				ID:        "http.missing.schema.v1",
				Kind:      "http",
				OwnerCell: "testcell",
				Lifecycle: "active",
				SchemaRefs: metadata.SchemaRefsMeta{
					Response: "nonexistent.schema.json",
				},
				Dir:  "contracts/http/missing/schema/v1",
				File: "contracts/http/missing/schema/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	// root points to a temp dir — the schema file won't exist there.
	v := NewValidator(pm, t.TempDir())
	results := v.Validate()
	matches := findByCode(results, "FMT-20")
	assert.Empty(t, matches, "missing schema file must produce no FMT-20 (handled by REF rules)")
}

// TestScanSchemaForStrictMissing_Basic verifies the helper returns correct
// JSON-pointer paths for missing additionalProperties.
func TestScanSchemaForStrictMissing_Basic(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"data": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{"type": "string"},
				},
			},
		},
	}

	raw, err := json.Marshal(schema)
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "schema-*.json")
	require.NoError(t, err)
	_, err = f.Write(raw)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	paths, err := scanSchemaForStrictMissing(f.Name())
	require.NoError(t, err)
	// Top-level object missing additionalProperties → "$"
	// $.data has it set → no violation
	assert.Equal(t, []string{"$"}, paths)
}
