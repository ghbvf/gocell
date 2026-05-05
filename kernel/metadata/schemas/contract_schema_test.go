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

func TestContractSchemaAllowsAuthPublic(t *testing.T) {
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
		"id": "http.auth.login.v1",
		"kind": "http",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"server": "accesscore",
			"clients": [],
			"http": {
				"method": "POST",
				"path": "/api/v1/auth/sessions",
				"successStatus": 201,
				"noContent": false,
				"auth": {
					"public": true
				}
			}
		}
	}`), &contractDoc))

	assert.NoError(t, schema.Validate(contractDoc), "contract with auth.public:true must pass strict validation")
}

func TestContractSchemaAllowsAuthPasswordResetExempt(t *testing.T) {
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
		"id": "http.auth.session.delete.v1",
		"kind": "http",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"server": "accesscore",
			"clients": [],
			"http": {
				"method": "DELETE",
				"path": "/api/v1/auth/sessions/{sessionId}",
				"pathParams": {
					"sessionId": {
						"type": "string",
						"format": "uuid"
					}
				},
				"successStatus": 204,
				"noContent": true,
				"auth": {
					"passwordResetExempt": true
				}
			}
		}
	}`), &contractDoc))

	assert.NoError(t, schema.Validate(contractDoc), "contract with auth.passwordResetExempt:true must pass strict validation")
}

func TestContractSchemaRejectsAuthPublicAndPasswordResetExemptBoth(t *testing.T) {
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
		"id": "http.auth.bad.v1",
		"kind": "http",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"server": "accesscore",
			"clients": [],
			"http": {
				"method": "POST",
				"path": "/api/v1/auth/bad",
				"successStatus": 200,
				"noContent": false,
				"auth": {
					"public": true,
					"passwordResetExempt": true
				}
			}
		}
	}`), &contractDoc))

	assert.Error(t,
		schema.Validate(contractDoc),
		"contract with both auth.public:true and auth.passwordResetExempt:true "+
			"must fail schema validation (mutually exclusive)")
}
