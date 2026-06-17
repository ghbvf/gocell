package schemas

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const assemblySchemaURL = "https://gocell.dev/schemas/assembly.schema.json"

func compileAssemblySchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := FS.ReadFile("assembly.schema.json")
	require.NoError(t, err)

	var schemaDoc any
	require.NoError(t, json.Unmarshal(raw, &schemaDoc))

	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource(assemblySchemaURL, schemaDoc))

	schema, err := compiler.Compile(assemblySchemaURL)
	require.NoError(t, err)
	return schema
}

func parseAssemblyDoc(t *testing.T, jsonStr string) any {
	t.Helper()
	var doc any
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &doc))
	return doc
}

// TestAssemblySchema_RequiresOwner verifies that a document missing the owner
// field fails schema validation.
func TestAssemblySchema_RequiresOwner(t *testing.T) {
	schema := compileAssemblySchema(t)
	doc := parseAssemblyDoc(t, `{
		"id": "corebundle",
		"cells": ["accesscore"]
	}`)
	assert.Error(t, schema.Validate(doc), "assembly without owner must fail validation")
}

// TestAssemblySchema_DeployTemplateEnum verifies that deployTemplate only
// accepts the declared enum values.
func TestAssemblySchema_DeployTemplateEnum(t *testing.T) {
	schema := compileAssemblySchema(t)

	base := `{
		"id": "corebundle",
		"cells": ["accesscore"],
		"owner": {"team": "platform", "role": "cell-owner"},
		"build": {"deployTemplate": %q}
	}`

	for _, valid := range []string{"k8s", "compose", "binary"} {
		doc := parseAssemblyDoc(t, formatf(base, valid))
		assert.NoError(t, schema.Validate(doc), "deployTemplate=%q must pass", valid)
	}

	invalidDoc := parseAssemblyDoc(t, `{
		"id": "corebundle",
		"cells": ["accesscore"],
		"owner": {"team": "platform", "role": "cell-owner"},
		"build": {"deployTemplate": "invalid"}
	}`)
	assert.Error(t, schema.Validate(invalidDoc), "deployTemplate=invalid must fail")
}

// TestAssemblySchema_BuildOptional verifies that a document with only
// id/cells/owner (no build block) passes schema validation.
func TestAssemblySchema_BuildOptional(t *testing.T) {
	schema := compileAssemblySchema(t)
	doc := parseAssemblyDoc(t, `{
		"id": "corebundle",
		"cells": ["accesscore"],
		"owner": {"team": "platform", "role": "cell-owner"}
	}`)
	assert.NoError(t, schema.Validate(doc), "assembly with id/cells/owner only must pass")
}

// TestAssemblySchema_NoCapabilitiesProperty verifies that assembly.yaml no
// longer accepts a `capabilities` property (Design Y, #855): the field moved to
// cell.requires and additionalProperties:false now rejects it at the assembly
// level. Coverage of the capability enum itself lives in cell_schema_test.go.
func TestAssemblySchema_NoCapabilitiesProperty(t *testing.T) {
	schema := compileAssemblySchema(t)
	doc := parseAssemblyDoc(t, `{
		"id": "corebundle",
		"cells": ["accesscore"],
		"owner": {"team": "platform", "role": "cell-owner"},
		"capabilities": ["postgres", "redis"]
	}`)
	assert.Error(t, schema.Validate(doc),
		"assembly capabilities property was removed; it must be rejected by additionalProperties:false")
}

// TestAssemblySchema_CellRefUnion verifies the cells[] scalar-or-object union
// (#1086) accepts valid forms and rejects the same shapes the Go parser
// (AssemblyCellRef.UnmarshalYAML) rejects — closing the schema↔parser drift
// (F3/cluster C3): empty/non-canonical cell id (both forms), empty module, and
// module paths carrying runes that could break the generated cellmodules import.
func TestAssemblySchema_CellRefUnion(t *testing.T) {
	schema := compileAssemblySchema(t)
	const before = `{"id": "corebundle", "owner": {"team": "platform", "role": "cell-owner"}, "cells": `
	const after = `}`

	accepted := map[string]string{
		"scalar shorthand":      `["configcore"]`,
		"object same-module":    `[{"id": "configcore"}]`,
		"object cross-module":   `[{"id": "payment", "module": "github.com/acme/payment-cell"}]`,
		"mixed scalar + object": `["accesscore", {"id": "payment", "module": "github.com/acme/pay"}]`,
	}
	for name, cells := range accepted {
		t.Run("accept/"+name, func(t *testing.T) {
			doc := parseAssemblyDoc(t, before+cells+after)
			assert.NoError(t, schema.Validate(doc), "%s must pass schema validation", name)
		})
	}

	rejected := map[string]string{
		"empty scalar id":       `[""]`,
		"non-canonical scalar":  `["NotACell"]`,
		"empty object id":       `[{"id": ""}]`,
		"empty module":          `[{"id": "payment", "module": ""}]`,
		"module with space":     `[{"id": "payment", "module": "github.com/a b/c"}]`,
		"module with quote":     `[{"id": "payment", "module": "a\"b"}]`,
		"module with backslash": `[{"id": "payment", "module": "a\\b"}]`,
	}
	for name, cells := range rejected {
		t.Run("reject/"+name, func(t *testing.T) {
			doc := parseAssemblyDoc(t, before+cells+after)
			assert.Error(t, schema.Validate(doc),
				"%s must fail schema validation (parser rejects it too)", name)
		})
	}
}

// TestAssemblySchema_Topology verifies the topology section of assembly.yaml.
func TestAssemblySchema_Topology(t *testing.T) {
	schema := compileAssemblySchema(t)

	const bp = `{"id":"corebundle","cells":["accesscore","auditcore"],"owner":{"team":"platform","role":"cell-owner"}}`
	// topo wraps suffix into a full assembly doc with a topology block.
	topo := func(suffix string) string {
		return bp[:len(bp)-1] + `,"topology":{` + suffix + `}}`
	}

	accepted := map[string]string{
		"no topology block": bp,
		"empty topology":    topo(``),
		"single group":      topo(`"groups":[{"role":"mono","cells":["accesscore","auditcore"],"endpoint":"mono:9000"}]`),
		"two groups": topo(`"groups":[{"role":"core","cells":["accesscore"],"endpoint":"a:1"},` +
			`{"role":"edge","cells":["auditcore"],"endpoint":"b:2"}]`),
	}
	for name, doc := range accepted {
		t.Run("accept/"+name, func(t *testing.T) {
			assert.NoError(t, schema.Validate(parseAssemblyDoc(t, doc)), "%s must pass", name)
		})
	}

	rejected := map[string]string{
		"topology extra property": topo(`"groups":[{"role":"core","cells":["accesscore"],"endpoint":"a:1"}],"unknown":true`),
		// The removed pre-#2278 authoring forms (topology.colocated / topology.remote)
		// must stay rejected: topology now accepts only `groups` (additionalProperties:false).
		// These red cases lock the migration — re-adding either field to the schema turns
		// these green (test fails), surfacing the regression.
		"old colocated authoring field": topo(`"colocated":["accesscore","auditcore"]`),
		"old remote authoring field":    topo(`"remote":[{"cellID":"auditcore","endpoint":"host:9090"}]`),
		"bad cellID in group":           topo(`"groups":[{"role":"core","cells":["BadCell"],"endpoint":"a:1"}]`),
		"group missing role":      topo(`"groups":[{"cells":["accesscore"],"endpoint":"a:1"}]`),
		"group missing cells":     topo(`"groups":[{"role":"core","endpoint":"a:1"}]`),
		"group missing endpoint":  topo(`"groups":[{"role":"core","cells":["accesscore"]}]`),
		"group empty cells":       topo(`"groups":[{"role":"core","cells":[],"endpoint":"a:1"}]`),
		"group extra property":    topo(`"groups":[{"role":"core","cells":["accesscore"],"endpoint":"a:1","extra":"x"}]`),
		"empty endpoint string":   topo(`"groups":[{"role":"core","cells":["accesscore"],"endpoint":""}]`),
		"empty role string":       topo(`"groups":[{"role":"","cells":["accesscore"],"endpoint":"a:1"}]`),
	}
	for name, doc := range rejected {
		t.Run("reject/"+name, func(t *testing.T) {
			assert.Error(t, schema.Validate(parseAssemblyDoc(t, doc)), "%s must fail", name)
		})
	}
}

// formatf is a helper that avoids importing fmt in a test-only file.
func formatf(format, arg string) string {
	out := make([]byte, 0, len(format)+len(arg))
	i := 0
	for i < len(format) {
		if i+1 < len(format) && format[i] == '%' && format[i+1] == 'q' {
			out = append(out, '"')
			out = append(out, []byte(arg)...)
			out = append(out, '"')
			i += 2
			continue
		}
		out = append(out, format[i])
		i++
	}
	return string(out)
}
