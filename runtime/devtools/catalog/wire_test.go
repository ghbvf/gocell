// Package catalog_test — wire_test.go: tests for wire type structure and constants.
package catalog_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/runtime/devtools/catalog"
)

// ---- TestSchemaVersionFrozen / TestAPIVersionFrozen ----

func TestSchemaVersionFrozen(t *testing.T) {
	if catalog.SchemaVersionV1 != "v1" {
		t.Errorf("SchemaVersionV1 = %q, want \"v1\"", catalog.SchemaVersionV1)
	}
}

func TestAPIVersionFrozen(t *testing.T) {
	if catalog.APIVersionV1 != "gocell.io/v1alpha1" {
		t.Errorf("APIVersionV1 = %q, want \"gocell.io/v1alpha1\"", catalog.APIVersionV1)
	}
}

// TestSchemaVersionLiteral hard-locks the exact literal values independent of
// golden files. Golden files can be regenerated via -update; this test cannot
// be bypassed.
func TestSchemaVersionLiteral(t *testing.T) {
	if catalog.SchemaVersionV1 != "v1" {
		t.Fatalf("SchemaVersionV1 = %q, want \"v1\"", catalog.SchemaVersionV1)
	}
	if catalog.APIVersionV1 != "gocell.io/v1alpha1" {
		t.Fatalf("APIVersionV1 = %q, want \"gocell.io/v1alpha1\"", catalog.APIVersionV1)
	}
}

// TestAllIncluded verifies that AllIncluded returns an IncludeOptions with all
// fields true.
func TestAllIncluded(t *testing.T) {
	inc := catalog.AllIncluded()
	if !inc.Relations {
		t.Error("AllIncluded.Relations must be true")
	}
	if !inc.StatusBoard {
		t.Error("AllIncluded.StatusBoard must be true")
	}
	if !inc.CellDeps {
		t.Error("AllIncluded.CellDeps must be true")
	}
	if !inc.PackageDeps {
		t.Error("AllIncluded.PackageDeps must be true")
	}
}

// TestIncludeOptions_ZeroValue verifies that the zero IncludeOptions has all
// fields false.
func TestIncludeOptions_ZeroValue(t *testing.T) {
	var inc catalog.IncludeOptions
	if inc.Relations || inc.StatusBoard || inc.CellDeps || inc.PackageDeps {
		t.Error("zero IncludeOptions must have all fields false")
	}
}

// TestAllKinds verifies the expected catalog entity kinds.
func TestAllKinds(t *testing.T) {
	want := []string{"Actor", "Assembly", "Cell", "Contract", "Journey", "Slice"}
	if strings.Join(catalog.AllKinds, ",") != strings.Join(want, ",") {
		t.Errorf("AllKinds = %v, want %v", catalog.AllKinds, want)
	}
}

// TestCamelCaseTags verifies all exported wire struct fields have camelCase
// json tags and matching yaml tags.
func TestCamelCaseTags(t *testing.T) {
	roots := []any{
		catalog.Document{},
		catalog.Entity{},
		catalog.EntityMetadata{},
		catalog.Relation{},
		catalog.Dependencies{},
		catalog.CellDepGraph{},
		catalog.CellEdge{},
		catalog.PackageDepsView{},
		catalog.FilterEcho{},
		catalog.CellSpec{},
		catalog.CellSpecOwner{},
		catalog.CellSpecSchema{},
		catalog.CellSpecL0Dep{},
		catalog.SliceSpec{},
		catalog.SliceSpecContractUsage{},
		catalog.ContractSpec{},
		catalog.JourneySpec{},
		catalog.JourneyPassCrit{},
		catalog.AssemblySpec{},
		catalog.AssemblySpecBuild{},
		catalog.ActorSpec{},
		catalog.StatusBoardEntry{},
	}

	for _, root := range roots {
		typ := reflect.TypeOf(root)
		checkExportTags(t, typ, typ.Name())
	}
}

func checkExportTags(t *testing.T, typ reflect.Type, path string) {
	t.Helper()
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		fieldPath := path + "." + f.Name
		jsonTag, hasJSON := f.Tag.Lookup("json")
		yamlTag, hasYAML := f.Tag.Lookup("yaml")

		if !hasJSON || !hasYAML {
			t.Errorf("field %s missing json or yaml tag (json=%v, yaml=%v)", fieldPath, hasJSON, hasYAML)
			continue
		}

		jsonName := strings.Split(jsonTag, ",")[0]
		yamlName := strings.Split(yamlTag, ",")[0]

		if jsonName == "-" || yamlName == "-" {
			continue
		}
		if jsonName == "" || yamlName == "" {
			continue
		}

		// json tag must start with lowercase
		if len(jsonName) > 0 && jsonName[0] >= 'A' && jsonName[0] <= 'Z' {
			t.Errorf("field %s json tag %q starts with uppercase", fieldPath, jsonName)
		}
		// json and yaml tag names must match
		if jsonName != yamlName {
			t.Errorf("field %s json tag %q != yaml tag %q", fieldPath, jsonName, yamlName)
		}
	}
}

// TestPackageDepsView_NoStatusField verifies that PackageDepsView does not have
// a Status field (A4 compliance).
func TestPackageDepsView_NoStatusField(t *testing.T) {
	typ := reflect.TypeOf(catalog.PackageDepsView{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		if strings.EqualFold(f.Name, "status") {
			t.Errorf("PackageDepsView must not have a Status field (A4 compliance); found: %s", f.Name)
		}
		if tag := f.Tag.Get("json"); strings.HasPrefix(tag, "status") || tag == "status" {
			t.Errorf("PackageDepsView must not have a json:'status' tag (A4 compliance)")
		}
	}
}
