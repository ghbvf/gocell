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

// --- FMT-RESPONSE-STRICT-01 ---

// TestFMTResponseStrict01_TopLevelMissingAdditionalProperties tests that
// FMT-RESPONSE-STRICT-01 fires when a top-level object in a schema lacks
// additionalProperties:false.
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

	matches := findByCode(results, "FMT-RESPONSE-STRICT-01")
	// Should fire for:
	// - top-level of response.schema.json ("$")
	// - nested $.data object
	// Total: 2 violations
	assert.Len(t, matches, 2,
		"expected 2 FMT-RESPONSE-STRICT-01 violations (top-level + nested data), got %d: %v",
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
	matches := findByCode(results, "FMT-RESPONSE-STRICT-01")
	assert.Empty(t, matches, "clean schema should produce no FMT-RESPONSE-STRICT-01 violations")
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
	matches := findByCode(results, "FMT-RESPONSE-STRICT-01")
	assert.Empty(t, matches, "non-HTTP contract should not be scanned by FMT-RESPONSE-STRICT-01")
}

// --- FMT-CONTRACT-DIR-ID-MATCH-01 ---

// TestFMTContractDirIDMatch01_Mismatch verifies that a contract whose Dir does
// not match the ID-derived path emits a FMT-CONTRACT-DIR-ID-MATCH-01 violation.
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
			matches := findByCode(results, "FMT-CONTRACT-DIR-ID-MATCH-01")
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
			matches := findByCode(results, "FMT-CONTRACT-DIR-ID-MATCH-01")
			assert.Len(t, matches, tc.wantCount,
				"test %q: expected %d violations, got %d: %v",
				tc.name, tc.wantCount, len(matches), matches)
		})
	}
}

// --- STATUSBOARD-STATE-ENUM-01 ---

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
			matches := findByCode(results, "STATUSBOARD-STATE-ENUM-01")
			assert.Len(t, matches, tc.wantCount,
				"test %q: expected %d violations, got %d: %v",
				tc.name, tc.wantCount, len(matches), matches)
			for _, r := range matches {
				assert.Equal(t, SeverityError, r.Severity)
			}
		})
	}
}

// --- CONTRACT-DEPRECATED-CLEANUP-01 ---

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
			matches := findByCode(results, "CONTRACT-DEPRECATED-CLEANUP-01")
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

// --- scanSchemaForStrictMissing helper (unit) ---

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
