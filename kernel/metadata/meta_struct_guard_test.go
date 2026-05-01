package metadata_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/contracts"
)

// allowedInlineFields lists struct fields that are explicitly permitted to
// carry a yaml:",inline" tag or a map[string]any / map[string]interface{}
// type.  Adding an entry here requires a justification comment explaining why
// the KnownFields(true) invariant (G-1) is preserved despite the exception.
//
// Key format: "TypeName.FieldName".
var allowedInlineFields = map[string]bool{
	// SchemaRefs.Extra uses yaml:",inline" with map[string]string (not any),
	// so it cannot smuggle arbitrary YAML keys past the decoder; all values
	// are typed strings.  It is allowed here because KnownFields(true) is
	// not applied to SchemaRefs directly—it is embedded inside ContractMeta
	// which has its own decoder, and the parser uses yaml.Decoder with
	// KnownFields(true) only at the ContractMeta level.  Extra captures only
	// string-valued additional schema ref keys beyond the four known ones.
	"SchemaRefs.Extra": true,
}

// TestMetaStructs_NoMapCatchall asserts that none of the core metadata structs
// (and their nested fields, up to depth 3) carry:
//
//   - a field of type map[string]any or map[string]interface{}
//   - a yaml tag containing ",inline"
//
// These patterns bypass yaml.v3's KnownFields(true) strict decoder and would
// break the G-1 invariant that unknown YAML fields are rejected at parse time.
//
// If a future PR needs a legitimate exception, add it to allowedInlineFields
// with an explanatory comment rather than relaxing this test.
func TestMetaStructs_NoMapCatchall(t *testing.T) {
	roots := []any{
		metadata.CellMeta{},
		metadata.SliceMeta{},
		metadata.ContractMeta{},
		metadata.AssemblyMeta{},
		metadata.JourneyMeta{},
		metadata.StatusBoardEntry{},
		metadata.ActorMeta{},
	}
	for _, root := range roots {
		typ := reflect.TypeOf(root)
		checkStruct(t, typ, typ.Name(), 0, 3)
	}
}

// TestContracts_NoMapCatchall covers the contracts package types that are
// embedded as type aliases in kernel/metadata (HTTPTransportMeta,
// HTTPResponseMeta, SchemaRefsMeta).  They participate in the same parse-time
// strictness guarantee.
func TestContracts_NoMapCatchall(t *testing.T) {
	roots := []any{
		contracts.HTTPTransport{},
		contracts.HTTPResponse{},
		contracts.SchemaRefs{},
	}
	for _, root := range roots {
		typ := reflect.TypeOf(root)
		checkStruct(t, typ, typ.Name(), 0, 3)
	}
}

// checkStruct recursively inspects all exported (and unexported) struct fields.
// depth is the current recursion depth; maxDepth caps the recursion.
func checkStruct(t *testing.T, typ reflect.Type, path string, depth, maxDepth int) {
	t.Helper()
	if depth > maxDepth {
		return
	}
	// Dereference pointer types.
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}

	for i := range typ.NumField() {
		f := typ.Field(i)
		fieldPath := path + "." + f.Name

		// Check for map[string]any / map[string]interface{} — these bypass
		// KnownFields because all unknown keys are silently absorbed.
		if isCatchallMap(f.Type) {
			if !allowedInlineFields[shortPath(fieldPath)] {
				t.Errorf(
					"field %s has type %s which acts as a catch-all and may bypass KnownFields(true); "+
						"add to allowedInlineFields with justification if intentional",
					fieldPath, f.Type,
				)
			}
		}

		// Check for yaml:",inline" — when combined with a map type (even
		// map[string]string) it absorbs unknown YAML keys.
		if tag, ok := f.Tag.Lookup("yaml"); ok {
			if strings.Contains(tag, ",inline") {
				if !allowedInlineFields[shortPath(fieldPath)] {
					t.Errorf(
						"field %s has yaml:\",inline\" tag which absorbs unknown YAML keys and may bypass KnownFields(true); "+
							"add to allowedInlineFields with justification if intentional",
						fieldPath,
					)
				}
			}
		}

		// Recurse into struct fields (and slices/maps whose elements are structs).
		recurseType(t, f.Type, fieldPath, depth+1, maxDepth)
	}
}

// recurseType unwraps slices, arrays, maps, and pointers and recurses into any
// struct type it finds.
func recurseType(t *testing.T, typ reflect.Type, path string, depth, maxDepth int) {
	t.Helper()
	for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Map {
		// Recurse into map value type (not key, which is always a string here).
		recurseType(t, typ.Elem(), path+"[value]", depth, maxDepth)
		return
	}
	if typ.Kind() == reflect.Struct {
		checkStruct(t, typ, path, depth, maxDepth)
	}
}

// isCatchallMap returns true if typ is map[string]any or map[string]interface{}.
func isCatchallMap(typ reflect.Type) bool {
	if typ.Kind() != reflect.Map {
		return false
	}
	if typ.Key().Kind() != reflect.String {
		return false
	}
	elem := typ.Elem()
	return elem.Kind() == reflect.Interface
}

// shortPath extracts the last "TypeName.FieldName" segment from a dotted path,
// which is the format used as the key in allowedInlineFields.
func shortPath(path string) string {
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return path
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}
