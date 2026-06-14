package schemas

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cellSchemaURL = "https://gocell.dev/schemas/cell.schema.json"

func compileCellSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := FS.ReadFile("cell.schema.json")
	require.NoError(t, err)

	var schemaDoc any
	require.NoError(t, json.Unmarshal(raw, &schemaDoc))

	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource(cellSchemaURL, schemaDoc))

	schema, err := compiler.Compile(cellSchemaURL)
	require.NoError(t, err)
	return schema
}

// TestCellSchema_RequiresEnum verifies that the cell `requires` array only
// accepts the declared enum values (postgres/redis/rabbitmq) and rejects
// out-of-enum / duplicate values. cell.requires is the authoritative single
// source for the assembly's derived capability set (Design Y, #855); this is
// the schema-shape upstream-Hard half (the const-parity half lives in
// schema_const_consistency_test.go).
func TestCellSchema_RequiresEnum(t *testing.T) {
	schema := compileCellSchema(t)

	valid := parseAssemblyDoc(t, `{
		"id": "accesscore",
		"type": "core",
		"consistencyLevel": "L3",
		"owner": {"team": "platform", "role": "cell-owner"},
		"verify": {"smoke": ["smoke.accesscore.startup"]},
		"requires": ["postgres", "redis"]
	}`)
	assert.NoError(t, schema.Validate(valid), "requires with declared enum values must pass")

	bogus := parseAssemblyDoc(t, `{
		"id": "accesscore",
		"type": "core",
		"consistencyLevel": "L3",
		"owner": {"team": "platform", "role": "cell-owner"},
		"verify": {"smoke": ["smoke.accesscore.startup"]},
		"requires": ["postgres", "bogus"]
	}`)
	assert.Error(t, schema.Validate(bogus), "requires with a value outside the enum must fail")

	dup := parseAssemblyDoc(t, `{
		"id": "accesscore",
		"type": "core",
		"consistencyLevel": "L3",
		"owner": {"team": "platform", "role": "cell-owner"},
		"verify": {"smoke": ["smoke.accesscore.startup"]},
		"requires": ["postgres", "postgres"]
	}`)
	assert.Error(t, schema.Validate(dup), "duplicate requires must fail (uniqueItems)")
}

// TestCellSchema_RequiresOptional verifies a cell without a requires field
// (most cells) still validates — requires is optional with a [] default.
func TestCellSchema_RequiresOptional(t *testing.T) {
	schema := compileCellSchema(t)
	doc := parseAssemblyDoc(t, `{
		"id": "ordercell",
		"type": "core",
		"consistencyLevel": "L2",
		"owner": {"team": "platform", "role": "cell-owner"},
		"verify": {"smoke": ["smoke.ordercell.startup"]}
	}`)
	assert.NoError(t, schema.Validate(doc), "cell without requires must pass (optional field)")
}
