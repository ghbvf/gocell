package assembly

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// synthesisFieldExemptions lists AssemblyMeta yaml-bearing field paths that
// synthesizeAssemblyMeta intentionally does not populate. Each entry maps
// the dotted yaml path (e.g. "build.binary") to a documented reason. The
// map MUST be empty under the current implementation — all yaml-bearing
// fields have a synthesizable spec source. Future additions that genuinely
// cannot be derived from AssemblyScaffoldSpec should add an entry with a
// reason before the new field reaches AssemblyMeta.
var synthesisFieldExemptions = map[string]string{}

// TestAssemblyMetaSynthesisFieldGuard uses reflection to assert that
// synthesizeAssemblyMeta populates every yaml-bearing field on
// metadata.AssemblyMeta (recursively, through nested structs like
// BuildMeta / OwnerMeta) — except those explicitly listed in
// synthesisFieldExemptions with a documented reason.
//
// Without this gate, adding a field to metadata.AssemblyMeta (or any nested
// struct) silently produces zero-valued synthesized values that flow into
// GenerateBoundary / GenerateModulesGen / GenerateEntrypoint as
// "intentionally omitted" — the failure class identified in 041 plan B8.
//
// Field-coverage invariant pattern: same shape as
// kernel/outbox.TestObservabilityMetadata_IsZero_FieldCoverageInvariant
// and TestSourceFingerprint_AnyFieldChange in this package — same-package
// unit test that exercises an unexported function via reflection. Not an
// archtest (tools/archtest/ is reserved for cross-package static-alignment
// enforcement with INVARIANT ID registration; behavioral field-coverage
// tests like this one stay in the package under test).
//
// Blind-spot inventory:
//   - top-level yaml-bearing fields — handled by reflect
//   - nested struct fields (BuildMeta / OwnerMeta) — handled by recursion
//   - yaml:"-" fields (MaxConsistencyLevel / Dir / File) — intentionally
//     skipped (parser-internal, not synthesizable input state)
//
// Reverse self-test fixtures (TestAssemblyMetaSynthesisFieldGuard_DetectsViolation)
// prove the detector fires on:
//  1. top-level fields missing
//  2. nested BuildMeta field missing
//  3. exemption suppresses target only
func TestAssemblyMetaSynthesisFieldGuard(t *testing.T) {
	t.Parallel()

	spec := AssemblyScaffoldSpec{
		ID:        "myasm",
		Cells:     []string{"mycell"},
		OwnerTeam: "platform",
		OwnerRole: "maintainer",
		Deploy:    "binary", // pick non-default so DeployTemplate is non-zero
	}
	meta := synthesizeAssemblyMeta(spec)
	if meta == nil {
		t.Fatal("synthesizeAssemblyMeta returned nil")
	}

	missing := findMissingYAMLFields(reflect.ValueOf(*meta), "", synthesisFieldExemptions)
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	t.Fatalf("ASSEMBLY-META-SYNTHESIS-FIELD-GUARD: synthesizeAssemblyMeta left "+
		"%d yaml-bearing field(s) unpopulated.\n"+
		"Missing paths: %s\n"+
		"Either (a) populate the field in synthesizeAssemblyMeta, or "+
		"(b) add the path to synthesisFieldExemptions with a documented reason.",
		len(missing), strings.Join(missing, ", "))
}

// TestAssemblyMetaSynthesisFieldGuard_DetectsViolation is the reverse self-
// test. Each fixture constructs a *partial* AssemblyMeta and asserts that
// findMissingYAMLFields detects exactly the omissions. Without these
// fixtures, a regression in the reflect walker (e.g. nested struct recursion
// silently dropped) would let real synthesizeAssemblyMeta drift undetected.
func TestAssemblyMetaSynthesisFieldGuard_DetectsViolation(t *testing.T) {
	t.Parallel()

	t.Run("blind_spot_1_top_level_fields_missing", func(t *testing.T) {
		t.Parallel()
		partial := metadata.AssemblyMeta{ID: "only-id"}
		missing := findMissingYAMLFields(reflect.ValueOf(partial), "", nil)
		// Expect: cells, owner.team, owner.role, build.entrypoint,
		// build.binary, build.deployTemplate (six missing paths)
		if got := len(missing); got < 3 {
			t.Errorf("walker must detect ≥3 missing top-level paths on a "+
				"partial AssemblyMeta{ID:...}; got %d: %v", got, missing)
		}
		if !containsPath(missing, "cells") {
			t.Errorf("walker must surface missing top-level path 'cells'; got %v", missing)
		}
	})

	t.Run("blind_spot_2_nested_field_missing", func(t *testing.T) {
		t.Parallel()
		// All top-level set but BuildMeta.Binary missing.
		partial := metadata.AssemblyMeta{
			ID:    "x",
			Cells: []string{"y"},
			Owner: metadata.OwnerMeta{Team: "t", Role: "r"},
			Build: metadata.BuildMeta{Entrypoint: "cmd/x/main.go", DeployTemplate: "k8s"},
		}
		missing := findMissingYAMLFields(reflect.ValueOf(partial), "", nil)
		if !containsPath(missing, "build.binary") {
			t.Errorf("walker must surface nested missing path 'build.binary'; got %v", missing)
		}
	})

	t.Run("blind_spot_3_exemption_suppresses_target_only", func(t *testing.T) {
		t.Parallel()
		// Same fixture as blind_spot_2 but exempt build.binary; the walker
		// must no longer report build.binary, and a separately-missing
		// sibling must still surface.
		partial := metadata.AssemblyMeta{
			ID:    "x",
			Cells: []string{"y"},
			Owner: metadata.OwnerMeta{Team: "t", Role: "r"},
			Build: metadata.BuildMeta{Entrypoint: "cmd/x/main.go"}, // missing Binary and DeployTemplate
		}
		exemptions := map[string]string{
			"build.binary": "test: intentionally exempted by reverse self-test",
		}
		missing := findMissingYAMLFields(reflect.ValueOf(partial), "", exemptions)
		if containsPath(missing, "build.binary") {
			t.Errorf("exemption must suppress 'build.binary'; got %v", missing)
		}
		if !containsPath(missing, "build.deployTemplate") {
			t.Errorf("non-exempted sibling 'build.deployTemplate' must still surface; got %v", missing)
		}
	})
}

// findMissingYAMLFields recursively walks v (a struct) and returns the
// dotted yaml field paths whose values are zero AND not exempted. yaml:"-"
// fields are skipped; nested structs recurse into. Slices are treated as
// "populated" when non-empty (single-level — no element walk needed for
// AssemblyMeta's shape).
func findMissingYAMLFields(v reflect.Value, prefix string, exemptions map[string]string) []string {
	var missing []string
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		if _, exempt := exemptions[path]; exempt {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Struct:
			missing = append(missing, findMissingYAMLFields(fv, path, exemptions)...)
		default:
			if fv.IsZero() {
				missing = append(missing, path)
			}
		}
	}
	return missing
}

func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}
