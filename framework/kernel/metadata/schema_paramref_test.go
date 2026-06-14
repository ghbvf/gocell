package metadata

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalContractFS builds an fstest.MapFS with the minimum cell/slice/journey
// entries needed to keep ParseFS from erroring on unrelated governance paths.
// The mixin file is provided separately per test case.
func contractWithParamRefFS(contractYAML string, extras map[string][]byte) fstest.MapFS {
	fsys := fstest.MapFS{
		"contracts/http/test/delete/v1/contract.yaml": &fstest.MapFile{
			Data: []byte(contractYAML),
		},
	}
	for path, data := range extras {
		fsys[path] = &fstest.MapFile{Data: data}
	}
	return fsys
}

// validMixinJSON is the expected_version mixin content as it exists on disk.
const validMixinJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "integer",
  "minimum": 1,
  "maximum": 99999
}`

// TestParamRefResolvesFromMixin asserts that a queryParam with $ref resolves
// to the mixin's type/minimum/maximum, and that the caller-set required=true
// is preserved after resolution.
func TestParamRefResolvesFromMixin(t *testing.T) {
	contractYAML := `id: http.test.delete.v1
kind: http
ownerCell: testcell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: testcell
  clients:
    - edge-bff
  http:
    method: DELETE
    path: /api/v1/test/{key}
    pathParams:
      key:
        type: string
        minLength: 1
        maxLength: 256
    queryParams:
      expectedVersion:
        $ref: "../../../../shared/cas/v1/expected_version.schema.json"
        required: true
    successStatus: 204
    noContent: true
`
	fsys := contractWithParamRefFS(contractYAML, map[string][]byte{
		"contracts/shared/cas/v1/expected_version.schema.json": []byte(validMixinJSON),
	})

	pm, err := NewParser("").ParseFS(fsys)
	require.NoError(t, err, "parse should succeed")

	c, ok := pm.Contracts["http.test.delete.v1"]
	require.True(t, ok, "contract should be present")
	require.NotNil(t, c.Endpoints.HTTP, "HTTP transport should be set")

	param, ok := c.Endpoints.HTTP.QueryParams["expectedVersion"]
	require.True(t, ok, "expectedVersion param should be present")

	assert.Equal(t, "integer", param.Type, "type should be resolved from mixin")
	require.NotNil(t, param.Minimum, "minimum should be resolved from mixin")
	assert.Equal(t, 1, *param.Minimum)
	require.NotNil(t, param.Maximum, "maximum should be resolved from mixin")
	assert.Equal(t, 99999, *param.Maximum)

	// required=true declared inline must survive resolution
	require.NotNil(t, param.Required, "required should be preserved from inline declaration")
	assert.True(t, *param.Required)
}

// TestParamRefMutualExclusionError asserts that a param with both $ref and
// inline type (or minimum/maximum) is rejected at parse time.
func TestParamRefMutualExclusionError(t *testing.T) {
	tests := []struct {
		name  string
		param string
	}{
		{
			name: "both $ref and type",
			param: `      expectedVersion:
        $ref: "../../../../shared/cas/v1/expected_version.schema.json"
        type: integer
        required: true`,
		},
		{
			name: "both $ref and minimum",
			param: `      expectedVersion:
        $ref: "../../../../shared/cas/v1/expected_version.schema.json"
        minimum: 1
        required: true`,
		},
		{
			name: "both $ref and maximum",
			param: `      expectedVersion:
        $ref: "../../../../shared/cas/v1/expected_version.schema.json"
        maximum: 99999
        required: true`,
		},
		{
			name: "both $ref and minLength",
			param: `      expectedVersion:
        $ref: "../../../../shared/cas/v1/expected_version.schema.json"
        minLength: 1
        required: true`,
		},
		{
			name: "both $ref and maxLength",
			param: `      expectedVersion:
        $ref: "../../../../shared/cas/v1/expected_version.schema.json"
        maxLength: 256
        required: true`,
		},
		{
			name: "both $ref and format",
			param: `      expectedVersion:
        $ref: "../../../../shared/cas/v1/expected_version.schema.json"
        format: int64
        required: true`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contractYAML := `id: http.test.delete.v1
kind: http
ownerCell: testcell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: testcell
  clients:
    - edge-bff
  http:
    method: DELETE
    path: /api/v1/test/{key}
    pathParams:
      key:
        type: string
        minLength: 1
        maxLength: 256
    queryParams:
` + tc.param + `
    successStatus: 204
    noContent: true
`
			fsys := contractWithParamRefFS(contractYAML, map[string][]byte{
				"contracts/shared/cas/v1/expected_version.schema.json": []byte(validMixinJSON),
			})
			_, err := NewParser("").ParseFS(fsys)
			require.Error(t, err, "parse should fail for %s", tc.name)
			assert.Contains(t, err.Error(), "$ref")
		})
	}
}

// TestParamRefMissingFile asserts that a $ref pointing to a non-existent file
// results in a parse error.
func TestParamRefMissingFile(t *testing.T) {
	contractYAML := `id: http.test.delete.v1
kind: http
ownerCell: testcell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: testcell
  clients:
    - edge-bff
  http:
    method: DELETE
    path: /api/v1/test/{key}
    pathParams:
      key:
        type: string
        minLength: 1
        maxLength: 256
    queryParams:
      expectedVersion:
        $ref: "../../../../shared/cas/v1/expected_version.schema.json"
        required: true
    successStatus: 204
    noContent: true
`
	// No mixin file added.
	fsys := contractWithParamRefFS(contractYAML, nil)
	_, err := NewParser("").ParseFS(fsys)
	require.Error(t, err, "parse should fail when mixin file is missing")
}

// TestParamRefPathEscapeRejected asserts that a $ref containing a path that
// escapes the FS root (e.g. ../../../../../../etc/x) is rejected.
func TestParamRefPathEscapeRejected(t *testing.T) {
	contractYAML := `id: http.test.delete.v1
kind: http
ownerCell: testcell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: testcell
  clients:
    - edge-bff
  http:
    method: DELETE
    path: /api/v1/test/{key}
    pathParams:
      key:
        type: string
        minLength: 1
        maxLength: 256
    queryParams:
      expectedVersion:
        $ref: "../../../../../../../../../../etc/passwd"
        required: true
    successStatus: 204
    noContent: true
`
	fsys := contractWithParamRefFS(contractYAML, nil)
	_, err := NewParser("").ParseFS(fsys)
	require.Error(t, err, "parse should fail for path-escaping $ref")
}

// TestParamRefInvalidJSONMixin asserts that a mixin file that exists but
// contains malformed JSON causes ParseFS/ResolveParamRef to return a parse
// error (errcode "param $ref file is not valid JSON").
func TestParamRefInvalidJSONMixin(t *testing.T) {
	contractYAML := `id: http.test.delete.v1
kind: http
ownerCell: testcell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: testcell
  clients:
    - edge-bff
  http:
    method: DELETE
    path: /api/v1/test/{key}
    pathParams:
      key:
        type: string
        minLength: 1
        maxLength: 256
    queryParams:
      expectedVersion:
        $ref: "../../../../shared/cas/v1/expected_version.schema.json"
        required: true
    successStatus: 204
    noContent: true
`
	// Mixin exists but contains malformed JSON.
	fsys := contractWithParamRefFS(contractYAML, map[string][]byte{
		"contracts/shared/cas/v1/expected_version.schema.json": []byte(`{ "type": "integer", INVALID }`),
	})
	_, err := NewParser("").ParseFS(fsys)
	require.Error(t, err, "parse should fail when mixin JSON is malformed")
	// Error() surfaces InternalMessage (param/ref) + cause (JSON parse error).
	// Both confirm the error originated from malformed mixin JSON.
	assert.Contains(t, err.Error(), "expectedVersion", "error should identify the param name")
	assert.Contains(t, err.Error(), "invalid character", "error should include the JSON parse cause")
}

// TestParamRefUnchangedWhenAbsent asserts that a param without $ref is
// loaded identically to before — the resolver is a no-op for such params.
func TestParamRefUnchangedWhenAbsent(t *testing.T) {
	contractYAML := `id: http.test.delete.v1
kind: http
ownerCell: testcell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: testcell
  clients:
    - edge-bff
  http:
    method: DELETE
    path: /api/v1/test/{key}
    pathParams:
      key:
        type: string
        minLength: 1
        maxLength: 256
    queryParams:
      expectedVersion:
        type: integer
        required: true
        minimum: 1
        maximum: 99999
    successStatus: 204
    noContent: true
`
	fsys := contractWithParamRefFS(contractYAML, nil)
	pm, err := NewParser("").ParseFS(fsys)
	require.NoError(t, err)

	c, ok := pm.Contracts["http.test.delete.v1"]
	require.True(t, ok)

	param := c.Endpoints.HTTP.QueryParams["expectedVersion"]
	assert.Equal(t, "integer", param.Type)
	assert.Equal(t, "", param.Ref, "Ref should be empty when not declared")
	require.NotNil(t, param.Minimum)
	assert.Equal(t, 1, *param.Minimum)
	require.NotNil(t, param.Maximum)
	assert.Equal(t, 99999, *param.Maximum)
}
