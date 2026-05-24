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
// $ref relative-path depth examples (count ".." segments from the contract dir
// to contracts/shared/cas/v1/):
//
//	contracts/http/config/{action}/v1/ → ../../../../shared/cas/v1/expected_version.schema.json  (4× ..)
//	contracts/http/flags/{action}/v1/ → ../../../../../shared/cas/v1/expected_version.schema.json (5× ..)
//
// AI-robust blind-spot self-check (forms outside the assertions above); each
// has a dedicated reverse-assertion test in TestCASBlindSpotReverseAssertions:
//   - $ref with extra sibling keys (draft-2020-12 $ref+siblings): rejected —
//     the property/param map must contain ONLY the sanctioned keys.
//   - $ref pointing at a non-canonical path: rejected — exact-string match to
//     the per-file relative path computed against the single mixin location.
//   - nested expectedVersion (inside a nested object's properties): scanned —
//     the body walk recurses every "properties" object, not just the top level.
//   - query param carrying inline type/minimum/maximum alongside $ref: rejected
//     — only {$ref, required} keys are allowed on a CAS query param.
//   - non-object expectedVersion form (boolean JSON Schema `expectedVersion: true`):
//     detected by KEY presence (not map type-assertion) and flagged as not-a-$ref,
//     so it cannot silently escape the funnel.
//
// ref: docs/architecture/202605241700-adr-contracts-shared-cas-mixin-funnel.md; gh #829.
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
	// Lock the canonical CAS range EXACTLY (not just minimum≥1 / maximum-present):
	// the mixin is the single source, so a silent typo here (e.g. minimum:0 weakens
	// the v≥1 guard, maximum:9 breaks legitimate version 10+) must fail CI. Changing
	// the CAS range is a deliberate contract change that must update this assertion.
	require.NotNil(t, mixin.Minimum,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: mixin minimum must be set")
	assert.Equal(t, 1.0, *mixin.Minimum,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: mixin minimum must be exactly 1")
	require.NotNil(t, mixin.Maximum,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: mixin maximum must be set")
	assert.Equal(t, 99999.0, *mixin.Maximum,
		"CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: mixin maximum must be exactly 99999")
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
		if rawEV, present := props[casFieldName]; present {
			// Detect by KEY presence, not by map type-assertion: a non-object
			// form (e.g. boolean schema `expectedVersion: true`) must NOT be
			// silently skipped — it bypasses the $ref funnel otherwise. A nil
			// field flows into checkCanonicalRefOnly as "must be a $ref".
			ev, _ := rawEV.(map[string]any)
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
	// Detect by KEY presence, not map type-assertion: a non-object form must not
	// silently escape the funnel. A nil field flows into checkCanonicalRefOnly
	// as "must be a $ref".
	rawEV, present := doc.Endpoints.HTTP.QueryParams[casFieldName]
	if !present {
		return nil, false
	}
	ev, _ := rawEV.(map[string]any)
	return ev, true
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

// checkCanonicalRefOnly is the pure predicate behind assertCanonicalRefOnly.
// It returns a slice of violation strings (empty = valid). allowedExtra is the
// set of keys permitted alongside "$ref" (e.g. {"required": true} for query params).
func checkCanonicalRefOnly(m map[string]any, canonical string, allowedExtra map[string]bool) []string {
	var violations []string
	ref, ok := m["$ref"].(string)
	if !ok {
		violations = append(violations, "must be a $ref to the shared mixin (inline definition banned)")
		return violations
	}
	if ref != canonical {
		violations = append(violations, "\"$ref\" must point at the canonical mixin (got "+ref+")")
	}
	allowed := map[string]bool{"$ref": true}
	for k := range allowedExtra {
		allowed[k] = true
	}
	for k := range m {
		if !allowed[k] {
			violations = append(violations, "carries disallowed inline key \""+k+"\" alongside $ref")
		}
	}
	return violations
}

// assertCanonicalRefOnly asserts m == {"$ref": canonical} plus the allowed extra keys.
func assertCanonicalRefOnly(t *testing.T, relPath, where string, m map[string]any, canonical string, allowedExtra ...string) {
	t.Helper()
	extra := map[string]bool{}
	for _, k := range allowedExtra {
		extra[k] = true
	}
	for _, v := range checkCanonicalRefOnly(m, canonical, extra) {
		t.Errorf("CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01: %s %s: %s", relPath, where, v)
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

// walkContractFiles invokes fn for every file named base under contracts/,
// using the sanctioned archtest content scanner (SCANNER-FRAMEWORK-USAGE-01
// bans filepath.WalkDir in archtest files).
func walkContractFiles(t *testing.T, root, base string, fn func(relPath string, raw []byte)) {
	t.Helper()
	suffix := base[strings.LastIndex(base, "."):]
	scope := DirsScope(root, []string{"contracts"}, MatchRels(func(rel string) bool {
		return filepath.Base(rel) == base
	}))
	EachContentFile(t, scope, []string{suffix}, func(_ *testing.T, fc ContentContext) {
		fn(filepath.ToSlash(fc.Rel), fc.Bytes)
	})
}

// stringInList reports whether want is in a YAML/JSON []any of strings.
// Comparison is case-sensitive: JSON Schema required[] member names are
// CASE-SENSITIVE wire field names.
func stringInList(v any, want string) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if s, ok := item.(string); ok && s == want {
			return true
		}
	}
	return false
}

// TestCASBlindSpotReverseAssertions contains dedicated reverse self-check tests
// for the four AI-robust blind spots listed in the package godoc. Each subtest
// constructs a synthetic bad input, feeds it to the pure helper, and asserts
// the helper REJECTS it — proving the funnel catches that blind-spot form.
// No real contract files are used.
func TestCASBlindSpotReverseAssertions(t *testing.T) {
	t.Parallel()
	const canonical = "../../../../shared/cas/v1/expected_version.schema.json"

	// Blind spot 1: $ref with an extra sibling key (draft-2020-12 $ref+siblings).
	// checkCanonicalRefOnly with no allowedExtra must flag the extra key.
	t.Run("ref-with-extra-sibling-key-rejected", func(t *testing.T) {
		t.Parallel()
		m := map[string]any{
			"$ref":        canonical,
			"description": "should not be here",
		}
		violations := checkCanonicalRefOnly(m, canonical, nil)
		if len(violations) == 0 {
			t.Error("expected violation for extra sibling key 'description', got none")
		}
		found := false
		for _, v := range violations {
			if strings.Contains(v, "description") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected violation mentioning 'description', got: %v", violations)
		}
	})

	// Blind spot 2: $ref pointing at a non-canonical path.
	// checkCanonicalRefOnly must flag a mismatched $ref value.
	t.Run("ref-wrong-path-rejected", func(t *testing.T) {
		t.Parallel()
		m := map[string]any{
			"$ref": "../../wrong/path/expected_version.schema.json",
		}
		violations := checkCanonicalRefOnly(m, canonical, nil)
		if len(violations) == 0 {
			t.Error("expected violation for wrong $ref path, got none")
		}
	})

	// Blind spot 3: nested expectedVersion inside a nested object's properties.
	// findExpectedVersionInProperties must recurse and find it.
	t.Run("nested-expected-version-found", func(t *testing.T) {
		t.Parallel()
		// Simulate: { properties: { foo: { properties: { expectedVersion: {$ref: canonical} },
		//                                   required: ["expectedVersion"] } } }
		inner := map[string]any{
			"properties": map[string]any{
				casFieldName: map[string]any{"$ref": canonical},
			},
			"required": []any{casFieldName},
		}
		doc := map[string]any{
			"properties": map[string]any{
				"foo": inner,
			},
		}
		hits := findExpectedVersionInProperties(doc)
		if len(hits) == 0 {
			t.Error("expected findExpectedVersionInProperties to find nested expectedVersion, got none")
		}
	})

	// Blind spot 4: query param carrying inline type alongside $ref (only {$ref,
	// required} are sanctioned for CAS query params). checkCanonicalRefOnly with
	// allowedExtra={"required"} must flag "type".
	t.Run("query-inline-type-alongside-ref-rejected", func(t *testing.T) {
		t.Parallel()
		m := map[string]any{
			"$ref":     canonical,
			"type":     "integer",
			"required": true,
		}
		allowed := map[string]bool{"required": true}
		violations := checkCanonicalRefOnly(m, canonical, allowed)
		if len(violations) == 0 {
			t.Error("expected violation for inline 'type' key alongside $ref, got none")
		}
		found := false
		for _, v := range violations {
			if strings.Contains(v, "type") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected violation mentioning 'type', got: %v", violations)
		}
	})

	// Blind spot 5: a non-object expectedVersion form (boolean JSON Schema
	// `expectedVersion: true`) must NOT silently escape the funnel. The scan
	// must detect it by key presence and flag it as not-a-$ref.
	t.Run("non-object-expectedVersion-not-skipped", func(t *testing.T) {
		t.Parallel()
		doc := map[string]any{
			"properties": map[string]any{
				casFieldName: true, // boolean schema form — must be caught, not skipped
			},
			"required": []any{casFieldName},
		}
		hits := findExpectedVersionInProperties(doc)
		if len(hits) != 1 {
			t.Fatalf("expected boolean-form expectedVersion to be detected (1 hit), got %d", len(hits))
		}
		violations := checkCanonicalRefOnly(hits[0].field, canonical, nil)
		if len(violations) == 0 {
			t.Error("expected boolean-form expectedVersion to be flagged as non-$ref, got none")
		}
	})
}
