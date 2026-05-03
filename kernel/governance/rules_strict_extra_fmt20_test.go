package governance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// FMT-20 (request schema strict additionalProperties; ADR-202605031600).
//
// Per ADR-202605031600 v1 schema evolution, FMT-20 enforces
// additionalProperties:false on request schemas only. Response schemas,
// endpoints.http.responses[*] schemaRefs, and event payload/headers schemas
// are intentionally lenient to allow v1 to grow optional fields without a
// major bump.

// Schema literals shared across the FMT-20 cases. Extracted so each table
// row reads as one line and so the same shape can be reused on both sides
// (request positive cases + response regression cases).

const fmt20SchemaTopLevelMissing = `{
    "type": "object",
    "properties": {
        "data": {
            "type": "object",
            "properties": {"id": {"type": "string"}}
        }
    }
}`

const fmt20SchemaArrayItems = `{
    "type": "object",
    "additionalProperties": false,
    "properties": {
        "list": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {"id": {"type": "string"}}
            }
        }
    }
}`

const fmt20SchemaUnevaluatedItems = `{
    "type": "array",
    "items": {"type": "string"},
    "unevaluatedItems": {
        "type": "object",
        "properties": {"id": {"type": "string"}}
    }
}`

const fmt20SchemaAllOf = `{
    "type": "object",
    "additionalProperties": false,
    "properties": {
        "data": {
            "allOf": [
                {
                    "type": "object",
                    "properties": {"id": {"type": "string"}}
                }
            ]
        }
    }
}`

const fmt20SchemaIfThenElse = `{
    "type": "object",
    "additionalProperties": false,
    "properties": {
        "payload": {
            "if": {"properties": {"kind": {"const": "a"}}},
            "then": {
                "type": "object",
                "properties": {"value": {"type": "string"}}
            },
            "else": {
                "type": "object",
                "properties": {"reason": {"type": "string"}}
            }
        }
    }
}`

const fmt20SchemaLocalRef = `{
    "type": "object",
    "additionalProperties": false,
    "properties": {
        "data": {"$ref": "#/$defs/Wrapper"}
    },
    "$defs": {
        "Wrapper": {
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "choice": {
                    "oneOf": [
                        {
                            "type": "object",
                            "properties": {"a": {"type": "string"}}
                        }
                    ]
                }
            }
        }
    }
}`

const fmt20SchemaClean = `{
    "type": "object",
    "additionalProperties": false,
    "properties": {
        "data": {
            "type": "object",
            "additionalProperties": false,
            "properties": {"id": {"type": "string"}}
        }
    }
}`

// fmt20SchemaExplicitOpen exercises the regression guard for ADR-202605031600:
// `additionalProperties: true` is an explicit open declaration, equivalent to
// missing-key as far as FMT-20 is concerned, and must trip the request-side
// violation. Response side ignores it (FMT-20 only scans request schemas).
const fmt20SchemaExplicitOpen = `{
    "type": "object",
    "additionalProperties": true,
    "properties": {"id": {"type": "string"}}
}`

// TestFMT20_BySchemaSide is the consolidated FMT-20 coverage matrix. Each row
// is run twice — once with the schema mounted as request.schema.json (FMT-20
// must report wantRequestFields) and once as response.schema.json (FMT-20 must
// report nothing per ADR-202605031600). Folding the two directions into one
// table makes it structurally impossible for a new shape to be added on one
// side and forgotten on the other.
func TestFMT20_BySchemaSide(t *testing.T) {
	cases := []struct {
		name              string
		schema            string
		wantRequestFields []string // nil => expect no FMT-20 violations
	}{
		{"TopLevelMissingAP", fmt20SchemaTopLevelMissing, []string{"$", "$.data"}},
		{"ArrayItemsObjectMissingAP", fmt20SchemaArrayItems, []string{"$.list.items"}},
		{"UnevaluatedItemsObjectMissingAP", fmt20SchemaUnevaluatedItems, []string{"$.unevaluatedItems"}},
		{"AllOfMissingAP", fmt20SchemaAllOf, []string{"$.data.allOf[0]"}},
		{"IfThenElseConditional", fmt20SchemaIfThenElse, []string{"$.payload.then", "$.payload.else"}},
		{"LocalRefThroughComposition", fmt20SchemaLocalRef, []string{"$.data.choice.oneOf[0]"}},
		{"ExplicitAdditionalPropertiesTrue", fmt20SchemaExplicitOpen, []string{"$"}},
		{"CleanRequestSchema", fmt20SchemaClean, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/Request", func(t *testing.T) {
			dir := t.TempDir()
			v := NewValidator(fmt20Fixture(t, dir, "case", tc.schema), dir, clock.Real())
			matches := findByCode(v.Validate(), "FMT-20")
			assertFMT20RequiredFields(t, matches, tc.wantRequestFields)
		})
		t.Run(tc.name+"/Response", func(t *testing.T) {
			dir := t.TempDir()
			v := NewValidator(fmt20ResponseFixture(t, dir, "case", tc.schema), dir, clock.Real())
			matches := findByCode(v.Validate(), "FMT-20")
			assert.Empty(t, matches,
				"response schema must not trigger FMT-20 (ADR-202605031600)")
		})
	}
}

// TestFMT20_EndpointResponsesSchemaRefIgnored verifies that the
// endpoints.http.responses[*].schemaRef path (used for per-status error
// schemas) is also out of FMT-20 scope after ADR-202605031600.
func TestFMT20_EndpointResponsesSchemaRefIgnored(t *testing.T) {
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "errtest", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(contractDir, "error-404.schema.json"),
		[]byte(`{"type":"object","properties":{"message":{"type":"string"}}}`),
		0o644))

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
	v := NewValidator(pm, dir, clock.Real())
	matches := findByCode(v.Validate(), "FMT-20")
	assert.Empty(t, matches,
		"endpoints.http.responses[*] must not trigger FMT-20 (ADR-202605031600)")
}

// TestFMT20_NonHTTPContractIgnored verifies that non-HTTP contracts
// (event/projection/command) are not scanned at all.
func TestFMT20_NonHTTPContractIgnored(t *testing.T) {
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
	matches := findByCode(v.Validate(), "FMT-20")
	assert.Empty(t, matches, "non-HTTP contract must not be scanned by FMT-20")
}

// TestFMT20_MalformedRequestSchemaEmitsIssueInvalid: parse error on the
// request schema is a definitive FMT-20 violation (fail-closed).
func TestFMT20_MalformedRequestSchemaEmitsIssueInvalid(t *testing.T) {
	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contracts", "http", "badschema", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(contractDir, "request.schema.json"),
		[]byte(`not valid json {{{`), 0o644))

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
					Request: "request.schema.json",
				},
				Dir:  "contracts/http/badschema/v1",
				File: "contracts/http/badschema/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(pm, dir, clock.Real())
	matches := findByCode(v.Validate(), "FMT-20")
	require.Len(t, matches, 1,
		"malformed request schema must produce 1 FMT-20 violation, got %d: %v", len(matches), matches)
	assert.Equal(t, IssueInvalid, matches[0].IssueType)
	assert.Equal(t, SeverityError, matches[0].Severity)
	assert.Contains(t, matches[0].Message, "failed to parse")
}

// TestFMT20_MissingSchemaFileSkipped: missing request schema file is
// silently skipped (REF rules report it).
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
					Request: "nonexistent.schema.json",
				},
				Dir:  "contracts/http/missing/schema/v1",
				File: "contracts/http/missing/schema/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	v := NewValidator(pm, t.TempDir(), clock.Real())
	matches := findByCode(v.Validate(), "FMT-20")
	assert.Empty(t, matches,
		"missing schema file must produce no FMT-20 (handled by REF rules)")
}

// fmt20Fixture writes request.schema.json under dir/contracts/http/<name>/v1/
// and returns a ProjectMeta whose http contract references it via
// SchemaRefs.Request. Used by tests that want FMT-20 to actually scan the
// supplied schema.
func fmt20Fixture(t *testing.T, dir, name, schema string) *metadata.ProjectMeta {
	t.Helper()
	contractDir := filepath.Join(dir, "contracts", "http", name, "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractDir, "request.schema.json"), []byte(schema), 0o644))
	id := "http." + name + ".v1"
	return &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			id: {
				ID:         id,
				Kind:       "http",
				OwnerCell:  "testcell",
				Lifecycle:  "active",
				SchemaRefs: metadata.SchemaRefsMeta{Request: "request.schema.json"},
				Dir:        "contracts/http/" + name + "/v1",
				File:       "contracts/http/" + name + "/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// fmt20ResponseFixture is the response-side counterpart used to assert that
// response schemas are NOT scanned (regression coverage for ADR-202605031600).
func fmt20ResponseFixture(t *testing.T, dir, name, schema string) *metadata.ProjectMeta {
	t.Helper()
	contractDir := filepath.Join(dir, "contracts", "http", name, "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contractDir, "response.schema.json"), []byte(schema), 0o644))
	id := "http." + name + ".v1"
	return &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			id: {
				ID:         id,
				Kind:       "http",
				OwnerCell:  "testcell",
				Lifecycle:  "active",
				SchemaRefs: metadata.SchemaRefsMeta{Response: "response.schema.json"},
				Dir:        "contracts/http/" + name + "/v1",
				File:       "contracts/http/" + name + "/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

func assertFMT20RequiredFields(t *testing.T, matches []ValidationResult, wantFields []string) {
	t.Helper()
	require.Len(t, matches, len(wantFields), "unexpected FMT-20 fields: %v", fieldList(matches))
	assert.ElementsMatch(t, wantFields, fieldList(matches))
	for _, m := range matches {
		assert.Equal(t, SeverityError, m.Severity)
		assert.Equal(t, IssueRequired, m.IssueType)
	}
}

func fieldList(results []ValidationResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Field)
	}
	return out
}
