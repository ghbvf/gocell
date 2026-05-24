// INVARIANT: CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01
//
// The CAS guard field `expectedVersion` has exactly ONE definition: the shared
// mixin contracts/shared/cas/v1/expected_version.schema.json (type: integer,
// minimum: 1, maximum: 99999). Every contract that guards a mutating write with
// CAS MUST reference that single source via $ref — inline copies are banned.
// This collapses the former "6 hand-crafted copies + cross-validate" (AI-Medium)
// into a single-source funnel (value single-source = Hard; conformance =
// closed auto-scan archtest funnel).
//
// Two carrier shapes, both resolving the SAME mixin file:
//
//  1. Body schema (POST/PUT/PATCH) — `request.schema.json`:
//     properties.expectedVersion MUST be exactly {"$ref": "<rel-to-mixin>"} and
//     listed in required[]. contractgen flattens the $ref into the generated DTO
//     and bundles it into the embedded runtime schema.
//
//  2. Query param (DELETE — no body) — `contract.yaml`:
//     endpoints.http.queryParams.expectedVersion MUST be exactly
//     {$ref: "<rel-to-mixin>", required: true}. kernel/metadata resolves the
//     $ref at parse time, back-filling type/minimum/maximum from the mixin so
//     governance (FMT-25) and contractgen consume a fully-resolved ParamSchema.
//
// Enforcement is a CLOSED, auto-enrolling funnel — NOT a hardcoded target list.
// Every `expectedVersion` carrier under contracts/ is scanned, mirroring the
// accepted CONTRACT-WIRE-FIELD-CAMELCASE-01 wire-field-name scan. A brand-new
// CAS contract that inlines expectedVersion (instead of $ref) fails CI without
// any test edit: there is no enumeration escape hatch (up-stream) and inline is
// unexpressible-without-failure (down-stream).
//
// Each CAS contract (body or query carrier) MUST also declare a 409 response so
// clients surface ERR_VERSION_CONFLICT; "is a CAS contract" is DERIVED from the
// presence of the expectedVersion field, not from a maintained list.
//
// AI-robust blind-spot self-check (forms outside the assertions above, each with
// a reverse assertion below):
//   - $ref with extra sibling keys (draft-2020-12 $ref+siblings): rejected —
//     the property/param map must contain ONLY the sanctioned keys.
//   - $ref pointing at a non-canonical path: rejected — exact-string match to
//     the per-file relative path computed against the single mixin location.
//   - nested expectedVersion (inside a nested object's properties): scanned —
//     the body walk recurses every "properties" object, not just the top level.
//   - query param carrying inline type/minimum/maximum alongside $ref: rejected
//     — only {$ref, required} keys are allowed on a CAS query param.
//
// ref: docs/architecture/*-adr-contracts-shared-cas-mixin-funnel.md; gh #829.
package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// casMixinRel is the single-source mixin path relative to the module root.
// Hardcoding the SOURCE location is funnel-correct: it is the one definition,
// not an enumeration of consumers.
const casMixinRel = "contracts/shared/cas/v1/expected_version.schema.json"

// casFieldName is the wire field name scanned across all contracts.
const casFieldName = "expectedVersion"

func TestCASContractExpectedVersionSchema(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	t.Run("mixin-shape", func(t *testing.T) {
		t.Parallel()
		assertCASMixinShape(t, root)
	})
	t.Run("body-ref-funnel", func(t *testing.T) {
		t.Parallel()
		scanBodyExpectedVersionFunnel(t, root)
	})
	t.Run("query-ref-funnel", func(t *testing.T) {
		t.Parallel()
		scanQueryExpectedVersionFunnel(t, root)
	})
	t.Run("409-declared", func(t *testing.T) {
		t.Parallel()
		scanCASContracts409(t, root)
	})
}

// assertCASMixinShape locks the single source's own shape so it cannot drift.
func assertCASMixinShape(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(casMixinRel))
	raw, err := os.ReadFile(path) //nolint:gosec // G304: archtest reads a fixed contract path
	require.NoError(t, err, "CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: missing mixin %s", casMixinRel)

	var mixin struct {
		Type    string   `json:"type"`
		Minimum *float64 `json:"minimum"`
		Maximum *float64 `json:"maximum"`
	}
	require.NoError(t, json.Unmarshal(raw, &mixin),
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: mixin %s is not valid JSON", casMixinRel)
	assert.Equal(t, "integer", mixin.Type,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: mixin type must be integer")
	require.NotNil(t, mixin.Minimum,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: mixin minimum must be set")
	assert.GreaterOrEqual(t, *mixin.Minimum, 1.0,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: mixin minimum must be ≥ 1")
	require.NotNil(t, mixin.Maximum,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: mixin maximum must be set")
}

// scanBodyExpectedVersionFunnel walks every request.schema.json under contracts/
// and asserts that any expectedVersion property (at any nesting depth) is the
// canonical $ref to the mixin and is listed in the enclosing required[].
func scanBodyExpectedVersionFunnel(t *testing.T, root string) {
	t.Helper()
	walkContractFiles(t, root, "request.schema.json", func(relPath string, raw []byte) {
		var doc map[string]any
		require.NoError(t, json.Unmarshal(raw, &doc),
			"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s is not valid JSON", relPath)

		canonical := canonicalRef(t, relPath)
		for _, hit := range findExpectedVersionInProperties(doc) {
			assertCanonicalRefOnly(t, relPath, "properties.expectedVersion", hit.field, canonical)
			assert.True(t, hit.inRequired,
				"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s required[] must include expectedVersion", relPath)
		}
	})
}

// scanQueryExpectedVersionFunnel walks every contract.yaml under contracts/ and
// asserts that any expectedVersion query param is exactly {$ref, required: true}.
func scanQueryExpectedVersionFunnel(t *testing.T, root string) {
	t.Helper()
	walkContractFiles(t, root, "contract.yaml", func(relPath string, raw []byte) {
		ev, ok := queryExpectedVersion(t, relPath, raw)
		if !ok {
			return
		}
		canonical := canonicalRef(t, relPath)
		assertCanonicalRefOnly(t, relPath, "queryParams.expectedVersion", ev, canonical, "required")

		req, hasReq := ev["required"]
		assert.True(t, hasReq && req == true,
			"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s queryParams.expectedVersion must declare required: true", relPath)
	})
}

// scanCASContracts409 asserts every CAS contract (body or query carrier) declares
// a 409 response. "Is a CAS contract" is derived from field presence.
func scanCASContracts409(t *testing.T, root string) {
	t.Helper()
	walkContractFiles(t, root, "contract.yaml", func(relPath string, raw []byte) {
		dir := filepath.Dir(relPath)
		bodyHasEV := dirRequestSchemaHasExpectedVersion(t, root, dir)
		_, queryHasEV := queryExpectedVersion(t, relPath, raw)
		if !bodyHasEV && !queryHasEV {
			return
		}
		var doc struct {
			Endpoints struct {
				HTTP struct {
					Responses map[string]any `yaml:"responses"`
				} `yaml:"http"`
			} `yaml:"endpoints"`
		}
		require.NoError(t, yaml.Unmarshal(raw, &doc),
			"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s is not valid YAML", relPath)
		_, ok := doc.Endpoints.HTTP.Responses["409"]
		assert.True(t, ok,
			"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s is a CAS contract and must declare a 409 response", relPath)
	})
}

// expectedVersionHit is one expectedVersion property found in a body schema.
type expectedVersionHit struct {
	field      map[string]any
	inRequired bool
}

// findExpectedVersionInProperties recursively finds every properties.expectedVersion
// schema node and whether it appears in the enclosing object's required[].
func findExpectedVersionInProperties(node any) []expectedVersionHit {
	var out []expectedVersionHit
	obj, ok := node.(map[string]any)
	if !ok {
		return out
	}
	if props, ok := obj["properties"].(map[string]any); ok {
		if ev, ok := props[casFieldName].(map[string]any); ok {
			out = append(out, expectedVersionHit{field: ev, inRequired: stringInList(obj["required"], casFieldName)})
		}
	}
	// Recurse into every child value to catch nested object/array schemas.
	for _, v := range obj {
		out = append(out, findExpectedVersionInProperties(v)...)
	}
	return out
}

// queryExpectedVersion returns the raw map for endpoints.http.queryParams.expectedVersion.
func queryExpectedVersion(t *testing.T, relPath string, raw []byte) (map[string]any, bool) {
	t.Helper()
	var doc struct {
		Endpoints struct {
			HTTP struct {
				QueryParams map[string]any `yaml:"queryParams"`
			} `yaml:"http"`
		} `yaml:"endpoints"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc),
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s is not valid YAML", relPath)
	ev, ok := doc.Endpoints.HTTP.QueryParams[casFieldName].(map[string]any)
	return ev, ok
}

// dirRequestSchemaHasExpectedVersion reports whether the contract dir's
// request.schema.json declares an expectedVersion property.
func dirRequestSchemaHasExpectedVersion(t *testing.T, root, dir string) bool {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(dir), "request.schema.json")
	raw, err := os.ReadFile(path) //nolint:gosec // G304: archtest reads fixed contract paths
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	return len(findExpectedVersionInProperties(doc)) > 0
}

// assertCanonicalRefOnly asserts m == {"$ref": canonical} plus the allowed extra keys.
func assertCanonicalRefOnly(t *testing.T, relPath, where string, m map[string]any, canonical string, allowedExtra ...string) {
	t.Helper()
	ref, ok := m["$ref"].(string)
	require.True(t, ok,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s %s must be a $ref to the shared mixin (inline definition banned)", relPath, where)
	assert.Equal(t, canonical, ref,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s %s $ref must point at the canonical mixin", relPath, where)

	allowed := map[string]bool{"$ref": true}
	for _, k := range allowedExtra {
		allowed[k] = true
	}
	for k := range m {
		assert.True(t, allowed[k],
			"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s %s carries disallowed inline key %q alongside $ref", relPath, where, k)
	}
}

// canonicalRef computes the expected $ref string (forward-slash) from a contract
// file's directory to the single mixin.
func canonicalRef(t *testing.T, fileRel string) string {
	t.Helper()
	rel, err := filepath.Rel(filepath.Dir(fileRel), filepath.FromSlash(casMixinRel))
	require.NoError(t, err)
	return filepath.ToSlash(rel)
}

// walkContractFiles invokes fn for every file named base under contracts/.
func walkContractFiles(t *testing.T, root, base string, fn func(relPath string, raw []byte)) {
	t.Helper()
	contractsRoot := filepath.Join(root, "contracts")
	err := filepath.WalkDir(contractsRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || d.Name() != base {
			return nil
		}
		raw, err := os.ReadFile(path) //nolint:gosec // G304: archtest reads fixed contract paths
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fn(filepath.ToSlash(rel), raw)
		return nil
	})
	require.NoError(t, err)
}

// stringInList reports whether want is in a YAML/JSON []any of strings.
func stringInList(v any, want string) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if s, ok := item.(string); ok && strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
