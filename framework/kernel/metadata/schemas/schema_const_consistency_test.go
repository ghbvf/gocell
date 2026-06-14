package schemas_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/schemas"
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

	// contract.schema.json kind enum is byte-locked to cellvocab.AllContractKinds().
	// Both the schema and governance validKinds derive from the same ordered slice,
	// so drift in either direction is caught here at test time.
	t.Run("contract.schema.json#kindEnum", func(t *testing.T) {
		t.Parallel()
		got := readSchemaStringSlice(t, "contract.schema.json",
			[]string{"properties", "kind", "enum"})
		want := make([]string, 0, len(cellvocab.AllContractKinds()))
		for _, k := range cellvocab.AllContractKinds() {
			want = append(want, string(k))
		}
		require.True(t, reflect.DeepEqual(want, got),
			"schemas/contract.schema.json kind enum drifted from cellvocab.AllContractKinds: schema=%v want=%v",
			got, want)
	})

	// contract.schema.json transports.items.enum is byte-locked to
	// cellvocab.AllTransports(). The schema enum, governance FMT-39, and runtime
	// metadata.IsKnownTransport all derive from the same ordered slice, so drift
	// in either direction is caught here at test time.
	t.Run("contract.schema.json#transportEnum", func(t *testing.T) {
		t.Parallel()
		got := readSchemaStringSlice(t, "contract.schema.json",
			[]string{"properties", "transports", "items", "enum"})
		want := make([]string, 0, len(cellvocab.AllTransports()))
		for _, tr := range cellvocab.AllTransports() {
			want = append(want, string(tr))
		}
		require.True(t, reflect.DeepEqual(want, got),
			"schemas/contract.schema.json transports.items.enum drifted from cellvocab.AllTransports: schema=%v want=%v",
			got, want)
	})

	// slice.schema.json contractUsages role enum is byte-locked to
	// cellvocab.AllContractRoles(). Drift means schema validation and governance
	// accept different role strings.
	t.Run("slice.schema.json#roleEnum", func(t *testing.T) {
		t.Parallel()
		got := readSchemaStringSlice(t, "slice.schema.json",
			[]string{"properties", "contractUsages", "items", "properties", "role", "enum"})
		want := make([]string, 0, len(cellvocab.AllContractRoles()))
		for _, r := range cellvocab.AllContractRoles() {
			want = append(want, string(r))
		}
		require.True(t, reflect.DeepEqual(want, got),
			"schemas/slice.schema.json contractUsages role enum drifted from cellvocab.AllContractRoles: schema=%v want=%v",
			got, want)
	})

	// slice.schema.json contractUsages projectionSource enum is byte-locked to
	// cellvocab.AllProjectionSources(). The schema enum, the parser validator
	// (isValidProjectionSource) and the cellgen builder all derive from the same
	// ordered slice, so drift in either direction is caught here at test time.
	t.Run("slice.schema.json#projectionSourceEnum", func(t *testing.T) {
		t.Parallel()
		got := readSchemaStringSlice(t, "slice.schema.json",
			[]string{"properties", "contractUsages", "items", "properties", "projectionSource", "enum"})
		want := make([]string, 0, len(cellvocab.AllProjectionSources()))
		for _, s := range cellvocab.AllProjectionSources() {
			want = append(want, string(s))
		}
		require.True(t, reflect.DeepEqual(want, got),
			"schemas/slice.schema.json contractUsages projectionSource enum drifted from cellvocab.AllProjectionSources: schema=%v want=%v",
			got, want)
	})

	// contract.schema.json saga step-name pattern is byte-locked to
	// metadata.SagaStepNamePattern. The pattern lives at the top-level saga
	// properties block (properties.saga.properties.steps.items.properties.name.pattern).
	t.Run("contract.schema.json#SagaStepNamePattern", func(t *testing.T) {
		t.Parallel()
		got := readSchemaString(t, "contract.schema.json",
			[]string{"properties", "saga", "properties", "steps", "items", "properties", "name", "pattern"})
		require.Equal(t, metadata.SagaStepNamePattern, got,
			"schemas/contract.schema.json saga step name pattern drifted from metadata.SagaStepNamePattern")
	})
}

// TestAssemblyCellRefSchemaPatternsMatchConstants byte-locks the assembly.yaml
// `cells[]` union patterns (#1086) to their Go single-source constants:
//   - oneOf[0] (scalar shorthand) .pattern        == metadata.CellIDPattern
//   - oneOf[1] (object) .properties.id.pattern    == metadata.CellIDPattern
//   - oneOf[1] .properties.module.pattern         == metadata.AssemblyModulePathPattern
//
// The cells.items.oneOf branches are addressed by array index (scalar = 0,
// object = 1), the stable shape declared in assembly.schema.json. Drift in
// either direction (schema looser/stricter than the parser's accepted set) is a
// hard failure: the schema is the on-disk authority for IDE/standalone tooling,
// the constants are the runtime authority used by AssemblyCellRef.decodeMapping
// (via metadata.MatchAssemblyModulePath) and governance cell-existence checks.
func TestAssemblyCellRefSchemaPatternsMatchConstants(t *testing.T) {
	t.Parallel()

	oneOf, ok := walkSchema(t, "assembly.schema.json",
		[]string{"properties", "cells", "items", "oneOf"}).([]any)
	require.True(t, ok, "assembly.schema.json cells.items.oneOf must be an array")
	require.Len(t, oneOf, 2, "cells.items.oneOf must have exactly 2 branches (scalar, object)")

	scalar, ok := oneOf[0].(map[string]any)
	require.True(t, ok, "oneOf[0] (scalar shorthand) must be an object")
	require.Equal(t, metadata.CellIDPattern, scalar["pattern"],
		"assembly.schema.json cells scalar-shorthand pattern drifted from metadata.CellIDPattern")

	object, ok := oneOf[1].(map[string]any)
	require.True(t, ok, "oneOf[1] (object form) must be an object")
	objProps, ok := object["properties"].(map[string]any)
	require.True(t, ok, "oneOf[1].properties must be an object")

	idProp, ok := objProps["id"].(map[string]any)
	require.True(t, ok, "oneOf[1].properties.id must be an object")
	require.Equal(t, metadata.CellIDPattern, idProp["pattern"],
		"assembly.schema.json cells object-form id.pattern drifted from metadata.CellIDPattern")

	moduleProp, ok := objProps["module"].(map[string]any)
	require.True(t, ok, "oneOf[1].properties.module must be an object")
	require.Equal(t, metadata.AssemblyModulePathPattern, moduleProp["pattern"],
		"assembly.schema.json cells object-form module.pattern drifted from metadata.AssemblyModulePathPattern")
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

// TestBuildMetaFieldsCoveredBySchema guards BuildMeta ↔ assembly.schema.json
// build.properties drift. The build block declares additionalProperties:false,
// so every yaml-tagged exported BuildMeta field MUST appear as a declared
// property — otherwise a valid assembly.yaml using that field would fail strict
// schema validation, and the schema (the on-disk authority) silently diverges
// from the Go struct (the runtime authority). Enumerating from the struct toward
// the schema means "add a BuildMeta field, forget the schema property" fails
// here instead of surfacing in a downstream tool.
func TestBuildMetaFieldsCoveredBySchema(t *testing.T) {
	t.Parallel()

	props, ok := walkSchema(t, "assembly.schema.json",
		[]string{"properties", "build", "properties"}).(map[string]any)
	require.True(t, ok, "assembly.schema.json build.properties must be an object")

	rt := reflect.TypeOf(metadata.BuildMeta{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		name := tag
		if idx := indexByte(tag, ','); idx >= 0 {
			name = tag[:idx]
		}
		if name == "" || name == "-" {
			continue
		}
		_, declared := props[name]
		require.True(t, declared,
			"metadata.BuildMeta.%s (yaml:%q) is missing from assembly.schema.json build.properties; "+
				"build has additionalProperties:false so an assembly.yaml using it would fail strict validation",
			f.Name, name)
	}
}

// indexByte returns the index of the first occurrence of b in s, or -1.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
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
