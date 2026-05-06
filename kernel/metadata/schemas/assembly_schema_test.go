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

// formatf is a helper that avoids importing fmt in a test-only file.
func formatf(format string, arg string) string {
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
