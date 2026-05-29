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
			[]string{"properties", "id", "pattern"},
			"CellIDPattern", metadata.CellIDPattern,
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

	// CapabilityEnum now lives on cell.schema.json `requires` — the per-cell
	// declaration is the authoritative single source from which the assembly's
	// provisioned capability set is derived (Design Y, #855). assembly.schema.json
	// no longer carries a `capabilities` property.
	t.Run("cell.schema.json#requires.enum", func(t *testing.T) {
		t.Parallel()
		got := readSchemaStringSlice(t, "cell.schema.json",
			[]string{"properties", "requires", "items", "enum"})
		require.True(t, reflect.DeepEqual(metadata.CapabilityEnum, got),
			"schemas/cell.schema.json requires.items.enum drifted from metadata.CapabilityEnum: schema=%v const=%v",
			got, metadata.CapabilityEnum)
	})

	// gRPC streamingType enum: the schema if/then literal is locked byte-equal
	// to metadata.GRPCStreamingTypeEnum so schema, governance FMT-37, and runtime
	// contractspec.validateGRPC share one source for the accepted streaming
	// patterns.
	t.Run("contract.schema.json#GRPCStreamingTypeEnum", func(t *testing.T) {
		t.Parallel()
		leaf := walkGRPCEndpointField(t, "streamingType", "enum")
		got := asStringSlice(t, leaf)
		require.True(t, reflect.DeepEqual(metadata.GRPCStreamingTypeEnum, got),
			"schemas/contract.schema.json grpc streamingType enum drifted from metadata.GRPCStreamingTypeEnum: schema=%v const=%v",
			got, metadata.GRPCStreamingTypeEnum)
	})

	// gRPC proto path prefix: the schema proto.pattern is an anchored literal
	// prefix ("^"+prefix). GRPCProtoPathPrefix contains no regex metacharacters,
	// so equality with "^"+const is an exact byte lock.
	t.Run("contract.schema.json#GRPCProtoPathPrefix", func(t *testing.T) {
		t.Parallel()
		leaf := walkGRPCEndpointField(t, "proto", "pattern")
		got, ok := leaf.(string)
		require.True(t, ok, "grpc proto.pattern is not a string: %T", leaf)
		require.Equal(t, "^"+metadata.GRPCProtoPathPrefix, got,
			"schemas/contract.schema.json grpc proto.pattern drifted from metadata.GRPCProtoPathPrefix")
	})
}

// walkGRPCEndpointField returns the leaf value at
// then.properties.endpoints.properties.grpc.properties.<field>.<leaf> inside the
// contract.schema.json allOf branch guarded by kind=grpc. The branch is located
// by its if.properties.kind.const == "grpc" guard rather than a fixed allOf
// index, so reordering the allOf array does not break this test.
func walkGRPCEndpointField(t *testing.T, field, leaf string) any {
	t.Helper()
	branch := findContractKindBranch(t, "grpc")
	cur := descend(t, branch,
		"then", "properties", "endpoints", "properties", "grpc",
		"properties", field, leaf)
	return cur
}

// findContractKindBranch returns the allOf entry of contract.schema.json whose
// if.properties.kind.const equals kindConst.
func findContractKindBranch(t *testing.T, kindConst string) map[string]any {
	t.Helper()
	raw, err := schemas.FS.ReadFile("contract.schema.json")
	require.NoError(t, err, "read contract.schema.json")
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc), "unmarshal contract.schema.json")
	allOf, ok := doc["allOf"].([]any)
	require.True(t, ok, "contract.schema.json allOf is not an array: %T", doc["allOf"])
	for _, entry := range allOf {
		branch, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		ifBlock, ok := branch["if"].(map[string]any)
		if !ok {
			continue
		}
		props, ok := ifBlock["properties"].(map[string]any)
		if !ok {
			continue
		}
		kind, ok := props["kind"].(map[string]any)
		if !ok {
			continue
		}
		if c, _ := kind["const"].(string); c == kindConst {
			return branch
		}
	}
	t.Fatalf("contract.schema.json: no allOf branch with if.properties.kind.const == %q", kindConst)
	return nil
}

// descend walks map keys from cur, failing the test if any key is missing or a
// non-object is encountered mid-path.
func descend(t *testing.T, cur any, keys ...string) any {
	t.Helper()
	for _, key := range keys {
		obj, ok := cur.(map[string]any)
		require.True(t, ok, "expected object before key %q, got %T", key, cur)
		cur, ok = obj[key]
		require.True(t, ok, "key %q missing", key)
	}
	return cur
}

// asStringSlice coerces a JSON array leaf into []string.
func asStringSlice(t *testing.T, leaf any) []string {
	t.Helper()
	arr, ok := leaf.([]any)
	require.True(t, ok, "leaf is not an array: %T", leaf)
	out := make([]string, 0, len(arr))
	for i, v := range arr {
		s, ok := v.(string)
		require.True(t, ok, "leaf[%d] is not a string: %T", i, v)
		out = append(out, s)
	}
	return out
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
