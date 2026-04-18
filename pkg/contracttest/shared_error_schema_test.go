package contracttest

import (
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
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	// thisFile = .../pkg/contracttest/shared_error_schema_test.go
	// walk up 2 dirs to project root, then into contracts/shared/errors/
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	return filepath.Join(projectRoot, "contracts", "shared", "errors", "error-response-v1.schema.json")
}

func loadSharedErrorSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schemaPath := sharedErrorSchemaPath(t)
	data, err := os.ReadFile(schemaPath)
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
		`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"token expired","details":{}}}`,
		`{"error":{"code":"ERR_VALIDATION_FAILED","message":"bad","details":{"field":"x"},"request_id":"req-1"}}`,
		`{"error":{"code":"ERR_CONFIG_NOT_FOUND","message":"config not found","details":{"key":"app.name"}}}`,
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
			body: `{"code":"ERR_AUTH_INVALID_TOKEN","message":"bad","details":{}}`,
		},
		{
			name: "missing code field",
			body: `{"error":{"message":"token expired","details":{}}}`,
		},
		{
			name: "bad code pattern — no ERR_ prefix",
			body: `{"error":{"code":"NOT_ERR_PREFIX","message":"bad","details":{}}}`,
		},
		{
			name: "extra top-level key",
			body: `{"error":{"code":"ERR_INTERNAL","message":"oops","details":{}},"extra":"field"}`,
		},
		{
			name: "missing details",
			body: `{"error":{"code":"ERR_INTERNAL","message":"oops"}}`,
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
