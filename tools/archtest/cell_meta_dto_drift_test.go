//go:build archtest

// INVARIANT: CELL-META-DTO-COVERAGE-01
//
// TestCellMetaDTOCoverage enforces that every top-level yaml-bearing exported
// field on metadata.CellMeta is either:
//
//   - mapped onto runtime/devtools/catalog.CellSpec, or
//   - listed in catalogExcludedCellFields with a documented reason.
//
// Sibling of ASSEMBLY-META-DTO-COVERAGE-01 (assembly_meta_dto_drift_test.go),
// sharing its exportedYAMLNames / exportedJSONNames helpers. This guard was
// MISSING when #855 added CellMeta.Requires: the field landed without a
// corresponding CellSpec projection and nothing flagged the drift, so
// `gocell export` silently dropped the new authoritative capability declaration
// from the catalog/metadata wire surface. (The assembly side did have this
// guard, which is exactly why #854's analogous AssemblyMeta.Capabilities gap
// surfaced as a red test.) Adding a new top-level CellMeta field now triggers
// this test, closing the cell-side leg of the "metadata extends but catalog
// stays stale" drift class.
//
// One-level reflection (matching the assembly invariant): nested struct field
// equality (metadata.OwnerMeta ↔ catalog.CellSpecOwner, etc.) stays outside
// this gate and is covered by focused catalog round-trip tests.

package archtest

import (
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/runtime/devtools/catalog"
)

// catalogExcludedCellFields lists CellMeta exported yaml-bearing fields that are
// intentionally not surfaced (by matching tag name) via CellSpec. Each entry
// maps the yaml field name to a reason string.
var catalogExcludedCellFields = map[string]string{
	// id is the resource identifier; surfaced via Entity.Metadata.Name + UID
	// (build.go buildCellEntity sets Name: c.ID), not inside Spec.
	"id": "surfaced via EntityMetadata.Name, not Spec",
	// verify is flattened: CellMeta.Verify.Smoke projects to CellSpec.VerifySmoke
	// (json/yaml tag "verifySmoke"), so the top-level "verify" tag has no
	// same-named Spec field by design.
	"verify": "flattened to CellSpec.VerifySmoke",
	// goStructName is a codegen-only extension (tools/codegen consumes it to
	// render cell_gen.go); it is not part of the catalog resource shape.
	"goStructName": "codegen-internal extension; not catalog Spec shape",
}

// TestCellMetaDTOCoverage is the CELL-META-DTO-COVERAGE-01 top-level field-set gate.
func TestCellMetaDTOCoverage(t *testing.T) {
	t.Parallel()
	metaFields := exportedYAMLNames(reflect.TypeOf(metadata.CellMeta{}))
	specFields := exportedJSONNames(reflect.TypeOf(catalog.CellSpec{}))

	for fname := range metaFields {
		if catalogExcludedCellFields[fname] != "" {
			continue
		}
		if _, ok := specFields[fname]; !ok {
			t.Errorf("metadata.CellMeta field %q has no CellSpec mapping; "+
				"add it to CellSpec, update buildCellEntity, or list it in "+
				"catalogExcludedCellFields with a reason", fname)
		}
	}
}
