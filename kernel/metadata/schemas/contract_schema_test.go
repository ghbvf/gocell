package schemas

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContractSchemaAllowsParamConstraintFacets(t *testing.T) {
	raw, err := FS.ReadFile("contract.schema.json")
	require.NoError(t, err)

	var schemaDoc any
	require.NoError(t, json.Unmarshal(raw, &schemaDoc))

	compiler := jsonschema.NewCompiler()
	const schemaURL = "https://gocell.dev/schemas/contract.schema.json"
	require.NoError(t, compiler.AddResource(schemaURL, schemaDoc))
	schema, err := compiler.Compile(schemaURL)
	require.NoError(t, err)

	var contractDoc any
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "http.test.v1",
		"kind": "http",
		"ownerCell": "testcell",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"server": "testcell",
			"clients": [],
			"http": {
				"method": "GET",
				"path": "/api/v1/test/{key}",
				"pathParams": {
					"key": {
						"type": "string",
						"minLength": 1,
						"maxLength": 128
					}
				},
				"queryParams": {
					"limit": {
						"type": "integer",
						"required": false,
						"minimum": 1,
						"maximum": 500
					}
				},
				"successStatus": 200,
				"noContent": false
			}
		},
		"schemaRefs": {
			"request": "request.schema.json"
		}
	}`), &contractDoc))

	assert.NoError(t, schema.Validate(contractDoc))
}
