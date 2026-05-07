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

func TestContractSchemaAllowsAuthBootstrapWithResponses(t *testing.T) {
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
		"id": "http.auth.setup.admin.v1",
		"kind": "http",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"server": "accesscore",
			"clients": [],
			"http": {
				"method": "POST",
				"path": "/api/v1/access/setup/admin",
				"successStatus": 201,
				"noContent": false,
				"auth": {
					"bootstrap": true,
					"responses": [401, 429]
				}
			}
		}
	}`), &contractDoc))

	assert.NoError(t, schema.Validate(contractDoc),
		"contract with auth.bootstrap:true and auth.responses must pass strict validation")
}

func TestContractSchemaRejectsAuthBootstrapAndPublicBoth(t *testing.T) {
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
		"id": "http.auth.bad2.v1",
		"kind": "http",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"server": "accesscore",
			"clients": [],
			"http": {
				"method": "POST",
				"path": "/api/v1/access/setup/admin",
				"successStatus": 201,
				"noContent": false,
				"auth": {
					"bootstrap": true,
					"public": true
				}
			}
		}
	}`), &contractDoc))

	assert.Error(t,
		schema.Validate(contractDoc),
		"contract with both auth.bootstrap:true and auth.public:true "+
			"must fail schema validation (mutually exclusive)")
}

func TestContractSchemaRejectsAuthBootstrapAndPasswordResetExemptBoth(t *testing.T) {
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
		"id": "http.auth.bad3.v1",
		"kind": "http",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"server": "accesscore",
			"clients": [],
			"http": {
				"method": "POST",
				"path": "/api/v1/access/setup/admin",
				"successStatus": 201,
				"noContent": false,
				"auth": {
					"bootstrap": true,
					"passwordResetExempt": true
				}
			}
		}
	}`), &contractDoc))

	assert.Error(t,
		schema.Validate(contractDoc),
		"contract with both auth.bootstrap:true and auth.passwordResetExempt:true "+
			"must fail schema validation (mutually exclusive)")
}

func TestContractSchemaAllowsAuthClientsOnly(t *testing.T) {
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
		"id": "http.internal.sample.list.v1",
		"kind": "http",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"server": "testcell",
			"clients": ["testcell"],
			"http": {
				"method": "GET",
				"path": "/internal/v1/sample/list",
				"successStatus": 200,
				"noContent": false,
				"auth": {
					"clientsOnly": true
				}
			}
		}
	}`), &contractDoc))

	assert.NoError(t, schema.Validate(contractDoc),
		"contract with auth.clientsOnly:true must pass strict validation")
}

func TestContractSchemaRejectsAuthClientsOnlyAndPublicBoth(t *testing.T) {
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
		"id": "http.internal.bad4.v1",
		"kind": "http",
		"consistencyLevel": "L1",
		"lifecycle": "active",
		"endpoints": {
			"server": "testcell",
			"clients": ["testcell"],
			"http": {
				"method": "GET",
				"path": "/internal/v1/bad4",
				"successStatus": 200,
				"noContent": false,
				"auth": {
					"clientsOnly": true,
					"public": true
				}
			}
		}
	}`), &contractDoc))

	assert.Error(t,
		schema.Validate(contractDoc),
		"contract with both auth.clientsOnly:true and auth.public:true "+
			"must fail schema validation (mutually exclusive)")
}
