package schemas

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
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

func TestContractSchemaAllowsAuthServiceOwnedWithPasswordResetExempt(t *testing.T) {
	schema := compileContractSchemaForTest(t)

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
					"serviceOwned": true,
					"passwordResetExempt": true
				},
				"ownership": {
					"subjectPath": "ctx.userID",
					"resourcePath": "path.sessionId.ownerID"
				}
			}
		}
	}`), &contractDoc))

	assert.NoError(t, schema.Validate(contractDoc),
		"auth.serviceOwned:true must be allowed to combine with auth.passwordResetExempt:true")
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

func compileContractSchemaForTest(t *testing.T) *jsonschema.Schema {
	t.Helper()

	raw, err := FS.ReadFile("contract.schema.json")
	require.NoError(t, err)

	var schemaDoc any
	require.NoError(t, json.Unmarshal(raw, &schemaDoc))

	compiler := jsonschema.NewCompiler()
	const schemaURL = "https://gocell.dev/schemas/contract.schema.json"
	require.NoError(t, compiler.AddResource(schemaURL, schemaDoc))
	schema, err := compiler.Compile(schemaURL)
	require.NoError(t, err)
	return schema
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

// TestContractSchemaOwnershipRequired verifies the schema if/then rule:
// when auth.serviceOwned=true the ownership block is required; when
// auth.serviceOwned=false or absent the ownership block is optional.
//
// The four cases exercise:
//  1. serviceOwned=true + complete ownership block → accept
//  2. serviceOwned=true + no ownership block → reject
//  3. serviceOwned=true + ownership block missing subjectPath → reject
//  4. serviceOwned=false + no ownership block → accept
//
// This test is an independent oracle of the schema if/then; it does NOT call
// metadata.OwnershipPathValid (that is FMT-32's domain). The validator used
// here is the raw JSON Schema so schema structural correctness is proven
// separately from governance rule correctness.
func TestContractSchemaOwnershipRequired(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	baseHTTP := func(authExtra, httpExtra string) string {
		return `{
			"id": "http.ownership.test.v1",
			"kind": "http",
			"consistencyLevel": "L1",
			"lifecycle": "active",
			"endpoints": {
				"server": "testcell",
				"clients": [],
				"http": {
					"method": "GET",
					"path": "/api/v1/ownership/test",
					"successStatus": 200,
					"noContent": false` + authExtra + httpExtra + `
				}
			}
		}`
	}

	tests := []struct {
		name        string
		doc         string
		expectValid bool
	}{
		{
			name: "serviceOwned=true with complete ownership block",
			doc: baseHTTP(
				`, "auth": {"serviceOwned": true}`,
				`, "ownership": {"subjectPath": "ctx.userID", "resourcePath": "path.id.ownerID"}`,
			),
			expectValid: true,
		},
		{
			name: "serviceOwned=true without ownership block",
			doc: baseHTTP(
				`, "auth": {"serviceOwned": true}`,
				``,
			),
			expectValid: false,
		},
		{
			name: "serviceOwned=true with ownership block missing subjectPath",
			doc: baseHTTP(
				`, "auth": {"serviceOwned": true}`,
				`, "ownership": {"resourcePath": "path.id.ownerID"}`,
			),
			expectValid: false,
		},
		{
			name: "serviceOwned=false without ownership block",
			doc: baseHTTP(
				`, "auth": {"serviceOwned": false}`,
				``,
			),
			expectValid: true,
		},
		{
			// Schema only enforces structural presence (minLength on subjectPath /
			// resourcePath) and the if/then ownership-required constraint. DSL
			// validity (ctx.* / path.* prefix, camelCase segments, pathParams
			// referential integrity) is NOT enforced by the JSON Schema — that is
			// solely FMT-32's responsibility (single-source governance rule). This
			// two-layer design is intentional: schema guards structure, FMT-32
			// guards semantics. If both validated DSL, any DSL change would require
			// updating two independent validators.
			name: "serviceOwned=true with DSL-invalid path — schema accepts (minLength only), FMT-32 rejects",
			doc: baseHTTP(
				`, "auth": {"serviceOwned": true}`,
				`, "ownership": {"subjectPath": "foo.bar", "resourcePath": "ctx.userID"}`,
			),
			expectValid: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var contractDoc any
			require.NoError(t, json.Unmarshal([]byte(tc.doc), &contractDoc), "JSON parse failed")
			err := schema.Validate(contractDoc)
			if tc.expectValid && err != nil {
				t.Errorf("expected valid but schema rejected: %v", err)
			}
			if !tc.expectValid && err == nil {
				t.Errorf("expected schema rejection but was accepted")
			}
		})
	}
}

// TestContractSchemaAuthBoolMatrix enumerates all 32 combinations of the
// 5 auth bool fields and asserts schema validation matches metadata.AuthComboLegal
// (the single oracle shared with kernel/governance/rules_fmt.go validateFMT27).
//
// Every contract document explicitly declares every bool field (true or false)
// to guard the "explicit false vs omission" semantic: under the original
// not/required mutex implementation, declaring 5 keys would trigger the
// key-presence rules and reject all 32 cases. Under the if/then const:true
// implementation, only the value-true conflicts are rejected.
//
// INVARIANT: AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01.
func TestContractSchemaAuthBoolMatrix(t *testing.T) {
	schema := compileContractSchemaForTest(t)

	metadata.IterateAuthBoolCombos(func(auth metadata.HTTPAuthMeta, name string) {
		t.Run(name, func(t *testing.T) {
			// When serviceOwned=true the schema if/then requires the ownership
			// block to be present. Include a minimal valid ownership block in all
			// serviceOwned=true cases so this test focuses on auth-field mutex
			// semantics rather than the ownership requirement (which is tested
			// separately by TestContractSchemaOwnershipRequired).
			ownershipFragment := ""
			if auth.ServiceOwned {
				ownershipFragment = `, "ownership": {"subjectPath": "ctx.userID", "resourcePath": "ctx.tenantID"}`
			}
			doc := fmt.Sprintf(`{
				"id": "http.matrix.test.v1",
				"kind": "http",
				"consistencyLevel": "L1",
				"lifecycle": "active",
				"endpoints": {
					"server": "testcell",
					"clients": ["testcell"],
					"http": {
						"method": "POST",
						"path": "/internal/v1/matrix/test",
						"successStatus": 200,
						"noContent": false,
						"auth": {
							"public": %t,
							"passwordResetExempt": %t,
							"serviceOwned": %t,
							"bootstrap": %t,
							"clientsOnly": %t
						}%s
					}
				}
			}`, auth.Public, auth.PasswordResetExempt, auth.ServiceOwned, auth.Bootstrap, auth.ClientsOnly, ownershipFragment)

			var contractDoc any
			require.NoError(t, json.Unmarshal([]byte(doc), &contractDoc))

			err := schema.Validate(contractDoc)
			_, expectedLegal := metadata.LegalAuthComboNames[name]
			if expectedLegal && err != nil {
				t.Errorf("schema rejected legal combo %s: %v", name, err)
			}
			if !expectedLegal && err == nil {
				t.Errorf("schema accepted illegal combo %s; expected reject per LegalAuthComboNames", name)
			}
		})
	})
}
