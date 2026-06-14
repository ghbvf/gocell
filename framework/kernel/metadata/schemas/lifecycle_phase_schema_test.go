package schemas

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sliceSchemaURL = "https://gocell.dev/schemas/slice.schema.json"

func compileSliceSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := FS.ReadFile("slice.schema.json")
	require.NoError(t, err)

	var schemaDoc any
	require.NoError(t, json.Unmarshal(raw, &schemaDoc))

	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource(sliceSchemaURL, schemaDoc))

	schema, err := compiler.Compile(sliceSchemaURL)
	require.NoError(t, err)
	return schema
}

var phaseEnumValues = []string{"experimental", "candidate", "asset", "maintenance", "retired"}

// TestCellSchema_LifecycleEnum verifies cell.yaml `lifecycle` accepts only the
// declared maturity phases and is optional (most cells omit it, defaulting to
// experimental at BaseCell construction).
func TestCellSchema_LifecycleEnum(t *testing.T) {
	schema := compileCellSchema(t)
	withLifecycle := func(v string) string {
		lc := ""
		if v != "" {
			lc = `, "lifecycle": "` + v + `"`
		}
		return `{
			"id": "accesscore",
			"type": "core",
			"consistencyLevel": "L3",
			"owner": {"team": "platform", "role": "cell-owner"},
			"verify": {"smoke": ["smoke.accesscore.startup"]}` + lc + `
		}`
	}

	for _, ph := range phaseEnumValues {
		doc := parseAssemblyDoc(t, withLifecycle(ph))
		assert.NoError(t, schema.Validate(doc), "lifecycle=%q must validate", ph)
	}
	assert.Error(t, schema.Validate(parseAssemblyDoc(t, withLifecycle("stable"))),
		"lifecycle outside the enum must fail")
	assert.NoError(t, schema.Validate(parseAssemblyDoc(t, withLifecycle(""))),
		"cell without lifecycle must pass (optional)")
}

// TestSliceSchema_LifecycleEnum mirrors the cell check for slice.yaml.
func TestSliceSchema_LifecycleEnum(t *testing.T) {
	schema := compileSliceSchema(t)
	withLifecycle := func(v string) string {
		lc := ""
		if v != "" {
			lc = `, "lifecycle": "` + v + `"`
		}
		return `{
			"id": "flagwrite",
			"belongsToCell": "configcore",
			"consistencyLevel": "L1",
			"contractUsages": [{"contract": "http.config.flags.create.v1", "role": "serve"}],
			"verify": {"unit": ["unit.flagwrite.service"], "contract": []}` + lc + `
		}`
	}

	for _, ph := range phaseEnumValues {
		doc := parseAssemblyDoc(t, withLifecycle(ph))
		assert.NoError(t, schema.Validate(doc), "slice lifecycle=%q must validate", ph)
	}
	assert.Error(t, schema.Validate(parseAssemblyDoc(t, withLifecycle("stable"))),
		"slice lifecycle outside the enum must fail")
	assert.NoError(t, schema.Validate(parseAssemblyDoc(t, withLifecycle(""))),
		"slice without lifecycle must pass (optional)")
}
