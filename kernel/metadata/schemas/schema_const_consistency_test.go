package schemas_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/schemas"
)

// TestSchemaConstantsMatchSchemaLiterals verifies that JSON Schema files
// retain pattern/enum literals byte-equal to the Go constants in
// kernel/metadata/contract_constraints.go.
//
// Drift in either direction is a hard failure: schema is the on-disk
// authoritative literal, the constants are the runtime authority used by
// governance and typed-identifier boundary types (GoIdentifier). Both must
// agree, or schema-aware tooling and CLI users see different contracts.
func TestSchemaConstantsMatchSchemaLiterals(t *testing.T) {
	t.Parallel()

	patternCases := []struct {
		schemaFile string
		path       []string
		constName  string
		want       string
	}{
		{
			"assembly.schema.json",
			[]string{"properties", "id", "pattern"},
			"AssemblyIDPattern", metadata.AssemblyIDPattern,
		},
		{
			"cell.schema.json",
			[]string{"properties", "goStructName", "pattern"},
			"GoStructNamePattern", metadata.GoStructNamePattern,
		},
	}
	for _, tc := range patternCases {
		tc := tc
		t.Run(tc.schemaFile+"#"+tc.constName, func(t *testing.T) {
			t.Parallel()
			got := readSchemaString(t, tc.schemaFile, tc.path)
			require.Equal(t, tc.want, got,
				"schemas/%s pattern at %v drifted from metadata.%s",
				tc.schemaFile, tc.path, tc.constName)
		})
	}

	t.Run("assembly.schema.json#DeployTemplateEnum", func(t *testing.T) {
		t.Parallel()
		got := readSchemaStringSlice(t, "assembly.schema.json",
			[]string{"properties", "build", "properties", "deployTemplate", "enum"})
		require.True(t, reflect.DeepEqual(metadata.DeployTemplateEnum, got),
			"schemas/assembly.schema.json deployTemplate enum drifted from metadata.DeployTemplateEnum: schema=%v const=%v",
			got, metadata.DeployTemplateEnum)
	})
}

// readSchemaString walks the JSON path and returns the string at the leaf.
func readSchemaString(t *testing.T, file string, path []string) string {
	t.Helper()
	leaf := walkSchema(t, file, path)
	s, ok := leaf.(string)
	require.True(t, ok, "schemas/%s leaf at %v is not string: %T", file, path, leaf)
	return s
}

// readSchemaStringSlice walks the JSON path and returns the []string at the leaf.
func readSchemaStringSlice(t *testing.T, file string, path []string) []string {
	t.Helper()
	leaf := walkSchema(t, file, path)
	arr, ok := leaf.([]any)
	require.True(t, ok, "schemas/%s leaf at %v is not array: %T", file, path, leaf)
	out := make([]string, 0, len(arr))
	for i, v := range arr {
		s, ok := v.(string)
		require.True(t, ok, "schemas/%s leaf[%d] at %v is not string: %T", file, i, path, v)
		out = append(out, s)
	}
	return out
}

func walkSchema(t *testing.T, file string, path []string) any {
	t.Helper()
	raw, err := schemas.FS.ReadFile(file)
	require.NoError(t, err, "read %s", file)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc), "unmarshal %s", file)
	var cur any = doc
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		require.True(t, ok, "schemas/%s: expected object at path %v, got %T", file, path, cur)
		cur, ok = obj[key]
		require.True(t, ok, "schemas/%s: key %q missing at path %v", file, key, path)
	}
	return cur
}
