//go:build archtest

// INVARIANT: PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01
//
// This file owns ONE invariant: a responseProjection item schema declares EVERY
// item property in its `required` set — i.e. a projection item has NO optional
// (not-in-`required`) column. Semantically this is "every projection column's
// zero/empty value is schema-valid on the wire" (the issue #2359 framing), realized
// in its strongest, false-positive-free form: forbid the optional category outright.
//
// Why "no optional column" IS the zero-value-validity guarantee. The generated
// ToMap() (PROJECTION-TOMAP-FULL-COLUMN-SET-01) emits EVERY column key
// unconditionally, so on the wire a projection column key is NEVER absent. An
// "optional" (not-in-`required`) column is therefore a category error: schema-
// validating clients are told the key MAY be absent (false — full-column-set keeps
// it present), and its Go zero/nil value reaches the wire where it can silently
// violate the schema — `""` for a `format`-constrained string, `null` for an
// array/object/`*bool`. The faithful model: a column is EITHER always-present-and-
// valid (in `required`, non-nullable) OR present-with-a-schema-valid-null (in
// `required`, nullable `["scalar","null"]` per #1875). Value-optionality is expressed
// by `nullable`, never by absence from `required`. So forbidding the optional category
// is exactly the zero-value-validity guarantee — stronger and simpler than a per-type
// "optional+format/array/object must be nullable" carve-out, and with no false
// positive on a legitimately always-set required `format` column (notBefore / notAfter
// / observedAt), which #1875 explicitly says needs no nullable treatment.
//
// History: #1350 (PR-12) restored full-column-set ToMap but left the wire contract
// only in generated Go, not in the schema `required` (5 contracts under-claimed,
// re-surfaced by #2340 F2/F3). #2359 closes the schema side: required = full column
// set. This archtest is the CI-time second leg of that closure.
//
// # Two legs (one invariant), per charter §载体选择
//
//   - Hard (primary, codegen build-fail): contractgen applyResponseProjection rejects
//     a responseProjection item DTO with any non-required column at GENERATE time, so
//     the drift cannot be materialized into generated code. That is the truth-source
//     closure.
//   - Medium (this archtest, schema-file scan): the SAME invariant verified directly
//     on the contract schema files in CI, so it trips on a schema edit even before the
//     generator is re-run, and carries the RED/GREEN fixtures that prove the detector
//     genuinely distinguishes an optional column from an all-required item. This
//     mirrors PROJECTION-TOMAP-FULL-COLUMN-SET-01's "golden Hard + archtest Medium"
//     two-leg structure.
//
// # Tool blind spots (charter §强制盲区自检)
//
//   - Top-level item columns only. The check covers the projection item's DIRECT
//     properties (the keys ToMap emits); a nested object/array column's OWN optional
//     sub-fields are NOT recursed. A nested value serializes via its struct (not
//     ToMap), so its sub-field presence is a normal schema concern, not the
//     full-column-set invariant this rule guards.
//   - It guarantees "no optional column", NOT that a required column's PRODUCER always
//     populates it. Residual: a required non-nullable `array` (`[]T` nil) / `object`
//     (`*T` nil) still marshals nil to JSON `null` and violates `type` if the producer
//     leaves it empty; `["array"/"object","null"]` is rejected by contractgen
//     (jsonschema.go nullableScalarType, #2340 F1) and this PR adds no nil→`[]`/`{}`
//     normalization (zero such column exists today). Tracked as a follow-up
//     (codegen nil-normalization, gated on the first array/object column that needs an
//     empty-or-null value). A required non-nullable `format` column whose producer
//     emits `""` is likewise out of schema reach — declare such empty-able columns
//     nullable (as occurredAt is). See ADR 202606112000-1350 §Amendment #2359.
//   - Single-source schema parse: the item-shape navigation resolves the response
//     schema through the SAME parser the generator uses (contractgen.Parse, recursive
//     $ref), not a second hand-rolled JSON walk — so a `data` / `data.items` $ref is
//     resolved identically to the generator and cannot skip enforcement.
//   - Platform-scope (isPlatformContract): examples/* projects are out of the platform
//     masking scope, same as RESOURCE-PROJECTION-COVERAGE-01.
//   - Anti-vacuity: minExpectedProjectionItemScans is the non-empty floor. If the
//     enumeration or the item-shape navigation silently collapses (zero items scanned)
//     the test FAILS rather than passing vacuously. The RED/GREEN fixture self-check
//     proves the detector distinguishes a violation from a clean item.
package archtest

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/contractgen"
)

// minExpectedProjectionItemScans is the anti-vacuity floor for the responseProjection
// item scan. 16 responseProjection platform contracts are marked today and all 16
// resolve to an item object, so the live scan count is 16. This floor is a deliberate
// LOWER BOUND (not the exact count): its job is to catch a navigation/enumeration
// regression that collapses the scan toward zero (which would let the production check
// pass vacuously), NOT to track the exact contract count — pinning the exact count
// would make CI flap on every legitimate add/remove. The −2 slack tolerates that churn.
// Maintenance: raise this only when a BULK addition makes 14 itself look vacuous; a
// single add/remove needs no change (the lower bound still holds).
const minExpectedProjectionItemScans = 14

// TestProjectionOptionalColumnZeroSchemaValid01 asserts every responseProjection
// platform contract's item schema lists all of its properties in `required`
// (no optional column), and that the scan is non-vacuous.
func TestProjectionOptionalColumnZeroSchemaValid01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	var diags []Diagnostic
	scanned := 0
	for _, c := range project.Contracts {
		if !isResponseProjectionPlatformContract(c) {
			continue
		}
		refPath := filepath.Join(c.Dir, c.SchemaRefs.Response)
		optional, itemFound, err := projectionItemOptionalColumns(root, refPath)
		if err != nil {
			diags = append(diags, Diagnostic{Message: fmt.Sprintf(
				"PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01: responseProjection contract %q response schema %s "+
					"could not be parsed: %v", c.ID, c.SchemaRefs.Response, err)})
			continue
		}
		if !itemFound {
			// A responseProjection contract whose `data` is not an object / array-of-object
			// is a RESOURCE-PROJECTION-COVERAGE-01 concern, not this rule's; don't count it
			// toward the anti-vacuity floor.
			continue
		}
		scanned++
		if len(optional) > 0 {
			diags = append(diags, projectionOptionalColumnDiag(c.ID, optional))
		}
	}
	if scanned < minExpectedProjectionItemScans {
		t.Fatalf("PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01: only %d responseProjection item schemas scanned "+
			"(floor: %d) — the enumeration or item-shape navigation may have regressed (vacuous scan). Fix the "+
			"parse/navigation, or update minExpectedProjectionItemScans if reads were intentionally removed.",
			scanned, minExpectedProjectionItemScans)
	}
	Report(t, "PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01", diags)
}

// isResponseProjectionPlatformContract reports whether c is an in-scope platform http
// contract with endpoints.http.responseProjection set.
func isResponseProjectionPlatformContract(c *metadata.ContractMeta) bool {
	if c.Kind != "http" || c.Endpoints.HTTP == nil || !c.Endpoints.HTTP.ResponseProjection {
		return false
	}
	return isPlatformContract(c)
}

// projectionItemOptionalColumns parses the response schema at refPath (relative to
// root) through contractgen.Parse and returns the item-object properties NOT present
// in the item's `required` set (the optional columns), whether a projectable item
// object was found, and any parse error. The item object is `data` (single resource)
// or `data.items` (resource list); $ref is resolved by the parser.
func projectionItemOptionalColumns(root, refPath string) (optional []string, itemFound bool, err error) {
	schema, err := contractgen.Parse(root, refPath)
	if err != nil {
		return nil, false, err
	}
	item := projectionItemSchema(schema)
	if item == nil {
		return nil, false, nil
	}
	required := map[string]bool{}
	for _, r := range item.Required {
		required[r] = true
	}
	for _, key := range item.PropertyOrder {
		if !required[key] {
			optional = append(optional, key)
		}
	}
	sort.Strings(optional)
	return optional, true, nil
}

// projectionItemSchema returns the projection item OBJECT schema from a parsed
// response schema: the top-level `data` when it is an object, or `data.items` when
// `data` is an array of objects. Returns nil for any other shape.
func projectionItemSchema(schema *contractgen.Schema) *contractgen.Schema {
	data := schema.Properties["data"]
	if data == nil {
		return nil
	}
	switch data.Type {
	case "object":
		return data
	case "array":
		if data.Items != nil && data.Items.Type == "object" {
			return data.Items
		}
	}
	return nil
}

// projectionOptionalColumnDiag is the single diagnostic constructor for a
// responseProjection item with one or more optional (not-in-required) columns.
func projectionOptionalColumnDiag(contractID string, optional []string) Diagnostic {
	return Diagnostic{Message: fmt.Sprintf(
		"PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01: responseProjection contract %q item schema has optional "+
			"(not-in-`required`) column(s) %v — the full-column-set ToMap emits every key unconditionally, so a "+
			"projection column is never absent on the wire and its zero/nil value (\"\" / null) can silently violate "+
			"the schema. Add the column(s) to the item `required` (and declare nullable `[\"scalar\",\"null\"]` for any "+
			"genuinely empty-able value), then regenerate (`gocell generate`). See ADR 202606112000-1350 §Amendment #2359.",
		contractID, optional)}
}

// TestProjectionOptionalColumnZeroSchemaValid01_ScannerCatchesViolation is the
// reverse self-check: it drives the SAME detector over synthetic fixtures. The RED
// fixtures — a `data` object with an optional `format` string, and a `data` array
// whose item object has an optional array column — must BOTH surface an optional
// column; the GREEN control (an all-required item) must surface none. The array RED
// also exercises the data.items navigation path.
func TestProjectionOptionalColumnZeroSchemaValid01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	relBase := filepath.Join("testdata", "projection_optional_column_zero_schema_valid_fixtures")

	// red_optional_array_ref exercises the data.items=$ref path (resolved by
	// contractgen.Parse) that production policy.list.v1 uses — proving the navigation
	// detects an optional column behind a $ref, not only inline item objects.
	reds := []string{"red_optional_format", "red_optional_array", "red_optional_array_ref"}
	for _, name := range reds {
		refPath := filepath.Join(relBase, name, "response.schema.json")
		optional, itemFound, err := projectionItemOptionalColumns(archDir, refPath)
		if err != nil {
			t.Fatalf("PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01 self-check: red fixture %q parse error: %v", name, err)
		}
		if !itemFound {
			t.Fatalf("PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01 self-check: red fixture %q has a projectable "+
				"item object but navigation did not find it — the detector is vacuous.", name)
		}
		if len(optional) == 0 {
			t.Errorf("PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01 self-check: red fixture %q has an optional column "+
				"but the detector found none — it would not flag a real optional projection column.", name)
		}
	}

	greenRef := filepath.Join(relBase, "green_all_required", "response.schema.json")
	optional, itemFound, err := projectionItemOptionalColumns(archDir, greenRef)
	if err != nil {
		t.Fatalf("PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01 self-check: green fixture parse error: %v", err)
	}
	if !itemFound {
		t.Fatal("PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01 self-check: green fixture item object not found")
	}
	if len(optional) != 0 {
		t.Errorf("PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01 self-check: green control (all-required item) "+
			"reported optional columns %v — the detector over-flags a clean item.", optional)
	}
}

// compile-time assertion that the contractgen.Schema fields this rule relies on exist
// (keeps the rule honest if the schema shape drifts).
var _ = func(s contractgen.Schema) (string, []string, []string, map[string]*contractgen.Schema, *contractgen.Schema) {
	return s.Type, s.Required, s.PropertyOrder, s.Properties, s.Items
}
