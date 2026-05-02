package governance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/contracts"
)

// --- FMT-20 ---
// FMT-20 tests live in rules_strict_extra_fmt20_test.go (table-driven per
// ADR-202605031600 v1 schema evolution).

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

			v := NewValidator(pm, "", clock.Real())
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

			v := NewValidator(pm, "", clock.Real())
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

			v := NewValidator(pm, "", clock.Real())
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

			v := NewValidator(pm, "", clock.Real())
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

	v := NewValidator(pm, "", clock.Real())
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

			v := NewValidator(pm, "", clock.Real())
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
	_, err = f.WriteString("not valid json {{{")
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
		for range depth {
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

// --- FMT-25 (HTTP input constraint: minLength/maxLength on strings, minimum/maximum on numeric values) ---

// fmt25WriteSchema is a test helper that writes a JSON schema string to the
// standard "contracts/http/test/v1" contract directory. Encapsulates the
// repeated TempDir + MkdirAll + WriteFile dance used across FMT-25 tests.
func fmt25WriteSchema(t *testing.T, dir, body string) {
	t.Helper()
	const contractRel = "contracts/http/test/v1"
	full := filepath.Join(dir, contractRel)
	require.NoError(t, os.MkdirAll(full, 0o755))
	p := filepath.Join(full, "request.schema.json")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

// fmt25Project builds a ProjectMeta containing one HTTP contract with the
// given request schema reference. queryParams / pathParams are optional —
// pass nil to omit. Used by every FMT-25 schema-driven test below.
func fmt25Project(queryParams, pathParams map[string]contracts.ParamSchema) *metadata.ProjectMeta {
	const contractDir = "contracts/http/test/v1"
	const contractID = "http.test.v1"
	cm := &metadata.ContractMeta{
		ID:        contractID,
		Kind:      "http",
		OwnerCell: "testcell",
		Lifecycle: "active",
		SchemaRefs: metadata.SchemaRefsMeta{
			Request: "request.schema.json",
		},
		Dir:  contractDir,
		File: contractDir + "/contract.yaml",
	}
	if queryParams != nil || pathParams != nil {
		var path strings.Builder
		path.WriteString("/x")
		for _, name := range sortedParamKeys(pathParams) {
			path.WriteString("/{" + name + "}")
		}
		cm.Endpoints = metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          path.String(),
				PathParams:    pathParams,
				QueryParams:   queryParams,
				SuccessStatus: 200,
			},
		}
	}
	return &metadata.ProjectMeta{
		Cells:       map[string]*metadata.CellMeta{},
		Slices:      map[string]*metadata.SliceMeta{},
		Contracts:   map[string]*metadata.ContractMeta{contractID: cm},
		Journeys:    map[string]*metadata.JourneyMeta{},
		Assemblies:  map[string]*metadata.AssemblyMeta{},
		StatusBoard: nil,
	}
}

func TestFMT25_RequestSchemaPathEscapeFailsClosed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "contracts", "http", "test"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "contracts", "http", "test", "outside.schema.json"),
		[]byte(`{"type":"object","additionalProperties":false}`), 0o644))
	pm := fmt25Project(nil, nil)
	pm.Contracts["http.test.v1"].SchemaRefs.Request = "../outside.schema.json"

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1)
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, "schemaRefs.request", matches[0].Field)
	assert.Contains(t, matches[0].Message, "failed to resolve")
}

func TestFMT25_RequestSchemaMissingFailsClosed(t *testing.T) {
	dir := t.TempDir()
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1)
	assert.Equal(t, IssueRefNotFound, matches[0].IssueType)
	assert.Equal(t, "schemaRefs.request", matches[0].Field)
	assert.Contains(t, matches[0].Message, "missing file")
}

// TestFMT25_RequestStringMissingMinLength verifies a violation fires when a
// string field in request.schema.json lacks minLength.
func TestFMT25_RequestStringMissingMinLength(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"username": {"type": "string", "maxLength": 128}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	v := NewValidator(pm, dir, clock.Real())
	results := v.Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1, "expected 1 violation for username missing minLength, got %d: %v", len(matches), matches)
	assert.Equal(t, "$.username", matches[0].Field)
	assert.Equal(t, SeverityError, matches[0].Severity)
	assert.Contains(t, matches[0].Message, "minLength")
}

// TestFMT25_RequestStringMissingMaxLength verifies a violation fires when a
// string field lacks maxLength (even if minLength is set).
func TestFMT25_RequestStringMissingMaxLength(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"username": {"type": "string", "minLength": 1}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1, "expected 1 violation for username missing maxLength")
	assert.Contains(t, matches[0].Message, "maxLength")
}

// TestFMT25_RequestIntegerMissingMinimumMaximum verifies violations fire when
// integer fields lack minimum or maximum.
func TestFMT25_RequestIntegerMissingMinimumMaximum(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"version": {"type": "integer", "minimum": 1},
			"page":    {"type": "integer"}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	// version: missing maximum (1 violation)
	// page:    missing minimum + missing maximum (2 violations)
	require.Len(t, matches, 3, "expected 3 violations, got %d: %v", len(matches), matches)
}

// TestFMT25_RequestNumberMissingMinimumMaximum verifies that JSON Schema
// number fields are governed by the same numeric bounds as integers.
func TestFMT25_RequestNumberMissingMinimumMaximum(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"ratio": {"type": "number"}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2, "number fields must require minimum + maximum, got: %v", matches)
	gotMessages := []string{matches[0].Message, matches[1].Message}
	for _, m := range matches {
		assert.Equal(t, "$.ratio", m.Field)
	}
	assert.Condition(t, func() bool {
		return strings.Contains(gotMessages[0], "minimum") || strings.Contains(gotMessages[1], "minimum")
	}, "expected a minimum violation, got %v", gotMessages)
	assert.Condition(t, func() bool {
		return strings.Contains(gotMessages[0], "maximum") || strings.Contains(gotMessages[1], "maximum")
	}, "expected a maximum violation, got %v", gotMessages)
}

// TestFMT25_RequestUnionTypeStringMissingConstraints verifies JSON Schema type
// arrays are interpreted semantically instead of being skipped.
func TestFMT25_RequestUnionTypeStringMissingConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"displayName": {"type": ["string", "null"]}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2, "union string|null must still require length facets, got: %v", matches)
	for _, m := range matches {
		assert.Equal(t, "$.displayName", m.Field)
	}
}

func TestFMT25_RequestExternalRefFailsClosed(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"remote": {"$ref": "https://example.invalid/common.schema.json#/Name"}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1, "non-local refs must fail closed")
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, "$.remote", matches[0].Field)
	assert.Contains(t, matches[0].Message, "non-local $ref")
}

func TestFMT25_RequestUnresolvedLocalRefFailsClosed(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"name": {"$ref": "#/$defs/missing"}
		},
		"$defs": {}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1, "unresolved local refs must fail closed")
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, "$.name", matches[0].Field)
	assert.Contains(t, matches[0].Message, "unresolved local $ref")
}

func TestFMT25_RequestMinGreaterThanMaxInvalid(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"name": {"type": "string", "minLength": 20, "maxLength": 5},
			"ratio": {"type": "number", "minimum": 10, "maximum": 1}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2, "inverted bounds must be invalid, got: %v", matches)
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, "$.name", matches[0].Field)
	assert.Contains(t, matches[0].Message, "minLength")
	assert.Equal(t, IssueInvalid, matches[1].IssueType)
	assert.Equal(t, "$.ratio", matches[1].Field)
	assert.Contains(t, matches[1].Message, "minimum")
}

func TestFMT25_RequestDepthLimitFailsClosed(t *testing.T) {
	dir := t.TempDir()
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
	}
	parent := schema["properties"].(map[string]any)
	for range 34 {
		child := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		}
		parent["nested"] = child
		parent = child["properties"].(map[string]any)
	}
	parent["leaf"] = map[string]any{"type": "string"}
	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	fmt25WriteSchema(t, dir, string(raw))
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1, "depth limit must emit an observable diagnostic")
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Contains(t, matches[0].Message, "depth")
}

// TestFMT25_RequestNestedObjectStringConstraints verifies the walker recurses
// into nested objects.
func TestFMT25_RequestNestedObjectStringConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"user": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"name": {"type": "string"}
				}
			}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	// user.name missing both → 2 violations (one per missing facet)
	require.Len(t, matches, 2)
	for _, m := range matches {
		assert.Equal(t, "$.user.name", m.Field)
	}
}

// TestFMT25_RequestArrayItemsStringConstraints verifies the walker recurses
// into items of array properties.
func TestFMT25_RequestArrayItemsStringConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"tags": {
				"type": "array",
				"items": {"type": "string"}
			}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	// tags.items missing minLength + maxLength → 2 violations at $.tags.items
	require.Len(t, matches, 2)
	for _, m := range matches {
		assert.Equal(t, "$.tags.items", m.Field)
	}
}

// TestFMT25_RequestLocalRefStringConstraints verifies local $ref targets are
// resolved at the referring field path.
func TestFMT25_RequestLocalRefStringConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"name": {"$ref": "#/$defs/name"}
		},
		"$defs": {
			"name": {"type": "string"}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2)
	for _, m := range matches {
		assert.Equal(t, "$.name", m.Field)
	}
}

// TestFMT25_RequestCombinatorStringConstraints verifies common composition
// keywords are traversed instead of hiding unconstrained inputs.
func TestFMT25_RequestCombinatorStringConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"name": {
				"allOf": [
					{"type": "string", "minLength": 1}
				]
			}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1)
	assert.Equal(t, "$.name.allOf[0]", matches[0].Field)
	assert.Contains(t, matches[0].Message, "maxLength")
}

func TestFMT25_RequestUnevaluatedItemsStringConstraints(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "array",
		"items": {"type": "string", "minLength": 1, "maxLength": 64},
		"unevaluatedItems": {"type": "string", "minLength": 1}
	}`
	fmt25WriteSchema(t, dir, body)
	pm := fmt25Project(nil, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1)
	assert.Equal(t, "$.unevaluatedItems", matches[0].Field)
	assert.Contains(t, matches[0].Message, "maxLength")
}

// TestFMT25_QueryParamsStringMissingConstraints verifies that
// contract.yaml.queryParams string fields are also checked.
func TestFMT25_QueryParamsStringMissingConstraints(t *testing.T) {
	dir := t.TempDir()
	// Provide a clean request schema so only the queryParams violation fires.
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	pm := fmt25Project(
		map[string]contracts.ParamSchema{
			"cursor": {Type: "string"}, // missing minLength + maxLength
		}, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2, "expected 2 violations for cursor missing both, got %d: %v", len(matches), matches)
	assert.Equal(t, "endpoints.http.queryParams.cursor.minLength", matches[0].Field)
	assert.Equal(t, "endpoints.http.queryParams.cursor.maxLength", matches[1].Field)
}

// TestFMT25_QueryParamsIntegerMissingConstraints verifies that integer
// queryParams (e.g. limit) without minimum/maximum trigger violations.
func TestFMT25_QueryParamsIntegerMissingConstraints(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	pm := fmt25Project(
		map[string]contracts.ParamSchema{
			"limit": {Type: "integer"}, // missing minimum + maximum
		}, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2)
	assert.Equal(t, "endpoints.http.queryParams.limit.minimum", matches[0].Field)
	assert.Equal(t, "endpoints.http.queryParams.limit.maximum", matches[1].Field)
}

// TestFMT25_QueryParamsNumberMissingConstraints verifies path/query ParamSchema
// type=number is covered by numeric minimum/maximum governance.
func TestFMT25_QueryParamsNumberMissingConstraints(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	pm := fmt25Project(
		map[string]contracts.ParamSchema{
			"ratio": {Type: "number"},
		}, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2)
	assert.Equal(t, "endpoints.http.queryParams.ratio.minimum", matches[0].Field)
	assert.Equal(t, "endpoints.http.queryParams.ratio.maximum", matches[1].Field)
}

func TestFMT25_QueryParamsInvalidBounds(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	one := 1
	ten := 10
	pm := fmt25Project(
		map[string]contracts.ParamSchema{
			"page": {Type: "integer", Minimum: &ten, Maximum: &one},
		}, nil)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 1)
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, "endpoints.http.queryParams.page", matches[0].Field)
	assert.Contains(t, matches[0].Message, "minimum")
}

// TestFMT25_PathParamsStringMissingConstraints verifies pathParams plain
// strings are checked.
func TestFMT25_PathParamsStringMissingConstraints(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	pm := fmt25Project(nil,
		map[string]contracts.ParamSchema{
			"key": {Type: "string"}, // plain string, no format → must be checked
		})

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 2)
	assert.Equal(t, "endpoints.http.pathParams.key.minLength", matches[0].Field)
	assert.Equal(t, "endpoints.http.pathParams.key.maxLength", matches[1].Field)
}

// TestFMT25_ParamFindingsUseLocatableMetadataPaths verifies param-side
// findings use full YAML paths so CLI output can include line/column anchors.
func TestFMT25_ParamFindingsUseLocatableMetadataPaths(t *testing.T) {
	dir := t.TempDir()
	const contractRel = "contracts/http/test/v1"
	fmt25WriteSchema(t, dir, `{"type": "object", "additionalProperties": false}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, contractRel, "contract.yaml"), []byte(`id: http.test.v1
kind: http
ownerCell: testcell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: testcell
  clients: []
  http:
    method: GET
    path: /api/v1/test/{key}
    pathParams:
      key:
        type: string
    queryParams:
      cursor:
        type: string
        required: false
    successStatus: 200
    noContent: false
schemaRefs:
  request: request.schema.json
`), 0o644))
	pm, err := metadata.NewParser(dir).Parse()
	require.NoError(t, err)

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	require.Len(t, matches, 4)
	for _, m := range matches {
		assert.NotZero(t, m.Line, "field %s should locate a YAML line", m.Field)
		assert.NotZero(t, m.Column, "field %s should locate a YAML column", m.Field)
	}
	assert.Equal(t, "endpoints.http.queryParams.cursor.minLength", matches[0].Field)
	assert.Equal(t, "endpoints.http.queryParams.cursor.maxLength", matches[1].Field)
	assert.Equal(t, "endpoints.http.pathParams.key.minLength", matches[2].Field)
	assert.Equal(t, "endpoints.http.pathParams.key.maxLength", matches[3].Field)
}

// TestFMT25_SkipsInvalidPathParams verifies FMT-25 does not add follow-on
// facet noise for pathParams that FMT-13 already rejected.
func TestFMT25_SkipsInvalidPathParams(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	tests := []struct {
		name       string
		path       string
		pathParams map[string]contracts.ParamSchema
	}{
		{
			name: "declaration without placeholder",
			path: "/x",
			pathParams: map[string]contracts.ParamSchema{
				"ghost": {Type: "string"},
			},
		},
		{
			name: "unsupported path param type",
			path: "/x/{id}",
			pathParams: map[string]contracts.ParamSchema{
				"id": {Type: "unsupported"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := fmt25Project(nil, tc.pathParams)
			pm.Contracts["http.test.v1"].Endpoints.HTTP.Path = tc.path

			results := NewValidator(pm, dir, clock.Real()).Validate()
			matches := findByCode(results, "FMT-25")
			assert.Empty(t, matches)
		})
	}
}

// TestFMT25_PathParamsUUIDFormatExempt verifies that pathParams with
// format:"uuid" are exempted from minLength/maxLength enforcement (RFC 4122
// fixes UUIDs at 36 characters; schema-level constraints would be redundant).
func TestFMT25_PathParamsUUIDFormatExempt(t *testing.T) {
	dir := t.TempDir()
	fmt25WriteSchema(t, dir,
		`{"type": "object", "additionalProperties": false}`)
	pm := fmt25Project(nil,
		map[string]contracts.ParamSchema{
			"id": {Type: "string", Format: "uuid"}, // exempt
		})

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	assert.Empty(t, matches, "format:uuid pathParams must be exempt from FMT-25, got: %v", matches)
}

// TestFMT25_CleanSchemaProducesNoViolations verifies that a fully-constrained
// schema and a fully-constrained set of params produce zero FMT-25 violations.
func TestFMT25_CleanSchemaProducesNoViolations(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"name":  {"type": "string", "minLength": 1, "maxLength": 128},
			"limit": {"type": "integer", "minimum": 1, "maximum": 500}
		}
	}`
	fmt25WriteSchema(t, dir, body)
	one := 1
	twoFiftySix := 256
	fiveHundred := 500
	pm := fmt25Project(
		map[string]contracts.ParamSchema{
			"cursor": {Type: "string", MinLength: &one, MaxLength: &twoFiftySix},
			"limit":  {Type: "integer", Minimum: &one, Maximum: &fiveHundred},
			"ratio":  {Type: "number", Minimum: &one, Maximum: &fiveHundred},
		},
		map[string]contracts.ParamSchema{
			"id":  {Type: "string", Format: "uuid"},                           // uuid exempt
			"key": {Type: "string", MinLength: &one, MaxLength: &twoFiftySix}, // plain string with constraints
		})

	results := NewValidator(pm, dir, clock.Real()).Validate()
	matches := findByCode(results, "FMT-25")
	assert.Empty(t, matches, "fully-constrained schema/params must produce no FMT-25, got: %v", matches)
}

// TestFMT25_NonHTTPContractIgnored verifies that non-HTTP contracts (event,
// command, projection) are not scanned by FMT-25.
func TestFMT25_NonHTTPContractIgnored(t *testing.T) {
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

	v := NewValidator(pm, dir, clock.Real())
	results := v.Validate()
	matches := findByCode(results, "FMT-25")
	assert.Empty(t, matches, "non-HTTP contract must not be scanned by FMT-25")
}

// FMT-20 helpers (fmt20Fixture, fmt20ResponseFixture, assertFMT20RequiredFields,
// fieldList) live in rules_strict_extra_fmt20_test.go alongside the table-driven
// tests after ADR-202605031600.
