package contracttest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedErrorSchemaPath returns the absolute path to error-response-v1.schema.json.
func sharedErrorSchemaPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(contractTestProjectRoot(t), "contracts", "shared", "errors", "error-response-v1.schema.json")
}

func contractTestProjectRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	// thisFile = .../tests/contracttest/shared_error_schema_test.go
	// walk up 2 dirs to project root, then into contracts/shared/errors/
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

func loadSharedErrorSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schemaPath := sharedErrorSchemaPath(t)
	data, err := os.ReadFile(filepath.Clean(schemaPath))
	require.NoError(t, err, "read error-response-v1.schema.json")

	var doc any
	require.NoError(t, json.Unmarshal(data, &doc), "parse schema JSON")

	compiler := jsonschema.NewCompiler()
	url := "file:///error-response-v1.schema.json"
	require.NoError(t, compiler.AddResource(url, doc))

	schema, err := compiler.Compile(url)
	require.NoError(t, err, "compile schema")
	return schema
}

// TestSharedErrorSchema_ValidSamples asserts that well-formed error envelopes pass.
func TestSharedErrorSchema_ValidSamples(t *testing.T) {
	schema := loadSharedErrorSchema(t)

	valid := []string{
		`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"token expired","details":[]}}`,
		`{"error":{"code":"ERR_VALIDATION_FAILED","message":"bad","details":[{"key":"field","value":"x"}],"request_id":"req-1"}}`,
		`{"error":{"code":"ERR_VALIDATION_FAILED","message":"bad","details":[{"key":"limit","value":100},{"key":"retry","value":true}]}}`,
		`{"error":{"code":"ERR_CONFIG_NOT_FOUND","message":"config not found","details":[{"key":"key","value":"app.name"}]}}`,
	}

	for _, sample := range valid {
		t.Run(sample[:40], func(t *testing.T) {
			var v any
			require.NoError(t, json.Unmarshal([]byte(sample), &v))
			assert.NoError(t, schema.Validate(v), "expected valid sample to pass schema")
		})
	}
}

// TestSharedErrorSchema_InvalidSamples asserts that malformed envelopes fail.
func TestSharedErrorSchema_InvalidSamples(t *testing.T) {
	schema := loadSharedErrorSchema(t)

	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing error wrapper",
			body: `{"code":"ERR_AUTH_INVALID_TOKEN","message":"bad","details":[]}`,
		},
		{
			name: "missing code field",
			body: `{"error":{"message":"token expired","details":[]}}`,
		},
		{
			name: "bad code pattern — no ERR_ prefix",
			body: `{"error":{"code":"NOT_ERR_PREFIX","message":"bad","details":[]}}`,
		},
		{
			name: "extra top-level key",
			body: `{"error":{"code":"ERR_INTERNAL","message":"oops","details":[]},"extra":"field"}`,
		},
		{
			name: "missing details",
			body: `{"error":{"code":"ERR_INTERNAL","message":"oops"}}`,
		},
		{
			name: "details as object (legacy form, must be rejected)",
			body: `{"error":{"code":"ERR_INTERNAL","message":"oops","details":{"key":"value"}}}`,
		},
		{
			name: "details value as object",
			body: `{"error":{"code":"ERR_VALIDATION_FAILED","message":"bad","details":[{"key":"field","value":{"name":"x"}}]}}`,
		},
		{
			name: "details value as array",
			body: `{"error":{"code":"ERR_VALIDATION_FAILED","message":"bad","details":[{"key":"field","value":["x"]}]}}`,
		},
		{
			name: "details value as null",
			body: `{"error":{"code":"ERR_VALIDATION_FAILED","message":"bad","details":[{"key":"field","value":null}]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v any
			require.NoError(t, json.Unmarshal([]byte(tt.body), &v))
			err := schema.Validate(v)
			assert.Error(t, err, "expected invalid sample to fail schema: %s", tt.body)
		})
	}
}

func TestSharedErrorSchema_CopiesInSync(t *testing.T) {
	root := contractTestProjectRoot(t)
	canonicalPath := sharedErrorSchemaPath(t)
	canonical, err := os.ReadFile(filepath.Clean(canonicalPath))
	require.NoError(t, err)

	for _, rel := range []string{
		"examples/iotdevice/contracts/shared/errors/error-response-v1.schema.json",
		"examples/todoorder/contracts/shared/errors/error-response-v1.schema.json",
		"tests/contracttest/testdata/contracts/shared/errors/error-response-v1.schema.json",
	} {
		t.Run(rel, func(t *testing.T) {
			got, err := os.ReadFile(filepath.Clean(filepath.Join(root, rel)))
			require.NoError(t, err)
			assert.True(t, bytes.Equal(canonical, got), "%s must match canonical shared error schema", rel)
		})
	}
}
