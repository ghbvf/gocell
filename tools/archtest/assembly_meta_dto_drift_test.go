// INVARIANT: ASSEMBLY-META-DTO-COVERAGE-01
//
// TestAssemblyMetaDTOCoverage enforces that every top-level yaml-bearing
// exported field on metadata.AssemblyMeta is either:
//
//   - mapped onto runtime/devtools/catalog.AssemblySpec, or
//   - listed in catalogExcludedAssemblyFields with a documented reason.
//
// Adding a new top-level metadata.AssemblyMeta field without wire DTO sync
// triggers this test, preventing the "metadata extends but catalog stays stale"
// drift class identified in PR #404 review §F4. The check uses one-level
// reflection rather than recursive AST/type scanning; nested struct field
// equality such as metadata.BuildMeta ↔ catalog.AssemblySpecBuild remains
// outside this invariant and is covered by focused catalog round-trip tests.
//
// Migrated from runtime/devtools/catalog/assembly_field_coverage_test.go
// (TestAssemblySpecCoversAssemblyMeta) into tools/archtest/ for single-source
// INVARIANT governance registration. ref: PR-MD1 plan §9 task 2.

package archtest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
)

// catalogExcludedAssemblyFields lists AssemblyMeta exported fields that are
// intentionally not surfaced via AssemblySpec. Each entry maps the yaml
// field name to a reason string.
var catalogExcludedAssemblyFields = map[string]string{
	// id is the resource identifier; it is surfaced via Entity.Metadata.Name
	// + UID at the catalog envelope layer (build.go buildAssemblyEntity sets
	// Name: a.ID), not inside Spec which is reserved for resource shape.
	"id": "surfaced via EntityMetadata.Name, not Spec",
}

// TestAssemblyMetaDTOCoverage is the ASSEMBLY-META-DTO-COVERAGE-01 top-level
// field-set gate.
func TestAssemblyMetaDTOCoverage(t *testing.T) {
	t.Parallel()
	metaFields := exportedYAMLNames(reflect.TypeOf(metadata.AssemblyMeta{}))
	specFields := exportedJSONNames(reflect.TypeOf(catalog.AssemblySpec{}))

	for fname := range metaFields {
		if catalogExcludedAssemblyFields[fname] != "" {
			continue
		}
		// Match on yaml/json tag first segment value (both are camelCase; assume
		// tag names are byte-equal across yaml and json declarations).
		if _, ok := specFields[fname]; !ok {
			t.Errorf("metadata.AssemblyMeta field %q has no AssemblySpec mapping; "+
				"add it to AssemblySpec, update buildAssemblyEntity, or list it in "+
				"catalogExcludedAssemblyFields with a reason", fname)
		}
	}
}

// exportedYAMLNames returns the lowercased yaml field names for the
// exported, yaml-tag-bearing fields of t. Fields with yaml:"-" or no
// yaml tag are skipped (parser-internal or non-serialized).
func exportedYAMLNames(t reflect.Type) map[string]struct{} {
	out := map[string]struct{}{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" || strings.HasPrefix(tag, "-,") {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = struct{}{}
	}
	return out
}

// exportedJSONNames returns the json field names for the exported, json-tag-
// bearing fields of t.
func exportedJSONNames(t reflect.Type) map[string]struct{} {
	out := map[string]struct{}{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" || strings.HasPrefix(tag, "-,") {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = struct{}{}
	}
	return out
}
