// INVARIANT: CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01
//
// All configcore mutating-write contracts that S6 added CAS to MUST declare an
// `expectedVersion: integer, minimum: 1, required` field plus a 409 response
// referencing the shared error envelope. The field is hand-crafted into 6
// separate schema files (no $ref shared mixin yet — see backlog
// CONTRACT-SHARED-MIXIN-FUNNEL-01 for the Hard upgrade path). This archtest
// cross-validates the 6 contracts so a future edit that drops the field
// (or violates type/min/required) is caught in CI.
//
// Two carrier shapes are recognized:
//
//   1. Body schema (POST/PUT/PATCH) — `request.schema.json`:
//      properties.expectedVersion.type=integer, minimum=1, in required[].
//
//   2. Query param (DELETE — no body) — `contract.yaml`:
//      endpoints.http.queryParams.expectedVersion.type=integer, required=true,
//      minimum=1.
//
// Each contract MUST also declare a 409 response referencing the shared error
// envelope (`contracts/shared/errors/error-response-v1.schema.json` via
// schemaRef relative path) so client SDKs surface the conflict cleanly.
//
// ref: docs/plans/202605082145-034-pg-corecell-b-route-plan.md §4 S6 (archtest
//      cross-validate, AI-Medium); backlog CONTRACT-SHARED-MIXIN-FUNNEL-01
//      tracks the Hard upgrade to a shared schema mixin via JSON Schema $ref.
package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// casContractTarget describes one contract that S6 added CAS to and where to
// find the expectedVersion declaration. carrier=body checks
// `request.schema.json`; carrier=query checks `contract.yaml` queryParams.
type casContractTarget struct {
	dir     string // relative to module root, e.g. "contracts/http/config/update/v1"
	carrier string // "body" | "query"
}

// casContractTargets is the hardcoded list S6 commits to maintaining. Adding a
// new CAS contract requires appending here AND wiring the schema.
var casContractTargets = []casContractTarget{
	{dir: "contracts/http/config/update/v1", carrier: "body"},
	{dir: "contracts/http/config/delete/v1", carrier: "query"},
	{dir: "contracts/http/config/rollback/v1", carrier: "body"},
	{dir: "contracts/http/config/flags/update/v1", carrier: "body"},
	{dir: "contracts/http/config/flags/toggle/v1", carrier: "body"},
	{dir: "contracts/http/config/flags/delete/v1", carrier: "query"},
}

func TestCASContractExpectedVersionSchema(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	for _, tgt := range casContractTargets {
		tgt := tgt
		t.Run(tgt.dir, func(t *testing.T) {
			t.Parallel()
			switch tgt.carrier {
			case "body":
				assertExpectedVersionInBodySchema(t, root, tgt.dir)
			case "query":
				assertExpectedVersionInQueryParams(t, root, tgt.dir)
			default:
				t.Fatalf("CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: unknown carrier %q for %s",
					tgt.carrier, tgt.dir)
			}
			assert409ResponseDeclared(t, root, tgt.dir)
		})
	}
}

// assertExpectedVersionInBodySchema validates request.schema.json contains
// expectedVersion as integer, minimum:1, listed in required[].
func assertExpectedVersionInBodySchema(t *testing.T, root, contractDir string) {
	t.Helper()
	path := filepath.Join(root, contractDir, "request.schema.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: missing %s", path)

	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Minimum     *int   `json:"minimum"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema),
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s is not valid JSON", path)

	ev, ok := schema.Properties["expectedVersion"]
	require.True(t, ok,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s missing properties.expectedVersion", path)
	assert.Equal(t, "integer", ev.Type,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s expectedVersion.type must be integer", path)
	require.NotNil(t, ev.Minimum,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s expectedVersion.minimum must be set", path)
	assert.GreaterOrEqual(t, *ev.Minimum, 1,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s expectedVersion.minimum must be ≥ 1", path)

	requiredSet := make(map[string]bool, len(schema.Required))
	for _, r := range schema.Required {
		requiredSet[r] = true
	}
	assert.True(t, requiredSet["expectedVersion"],
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s required[] must include expectedVersion", path)
}

// assertExpectedVersionInQueryParams validates contract.yaml has
// endpoints.http.queryParams.expectedVersion as required integer with min:1.
// DELETE endpoints have no body; the CAS guard lives in the query.
func assertExpectedVersionInQueryParams(t *testing.T, root, contractDir string) {
	t.Helper()
	path := filepath.Join(root, contractDir, "contract.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: missing %s", path)

	var doc struct {
		Endpoints struct {
			HTTP struct {
				QueryParams map[string]struct {
					Type     string `yaml:"type"`
					Required bool   `yaml:"required"`
					Minimum  *int   `yaml:"minimum"`
				} `yaml:"queryParams"`
			} `yaml:"http"`
		} `yaml:"endpoints"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc),
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s is not valid YAML", path)

	ev, ok := doc.Endpoints.HTTP.QueryParams["expectedVersion"]
	require.True(t, ok,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s missing endpoints.http.queryParams.expectedVersion", path)
	assert.Equal(t, "integer", ev.Type,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s expectedVersion.type must be integer", path)
	assert.True(t, ev.Required,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s expectedVersion.required must be true", path)
	require.NotNil(t, ev.Minimum,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s expectedVersion.minimum must be set", path)
	assert.GreaterOrEqual(t, *ev.Minimum, 1,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s expectedVersion.minimum must be ≥ 1", path)
}

// assert409ResponseDeclared verifies contract.yaml declares a 409 response
// referencing the shared error envelope. CAS conflicts surface as
// ERR_VERSION_CONFLICT and clients distinguish them via status + code.
func assert409ResponseDeclared(t *testing.T, root, contractDir string) {
	t.Helper()
	path := filepath.Join(root, contractDir, "contract.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: missing %s", path)

	// Status codes live as mapping keys under endpoints.http.responses (the
	// canonical shape across all contract.yaml). Find that mapping and check
	// "409" is one of its keys.
	var doc struct {
		Endpoints struct {
			HTTP struct {
				Responses map[string]any `yaml:"responses"`
			} `yaml:"http"`
		} `yaml:"endpoints"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc),
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s is not valid YAML", path)

	_, ok := doc.Endpoints.HTTP.Responses["409"]
	assert.True(t, ok,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s must declare a 409 response (ERR_VERSION_CONFLICT)", path)
}
