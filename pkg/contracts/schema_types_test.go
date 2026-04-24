package contracts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// roundTrip marshals v to YAML, unmarshals into a new T, and returns both.
func roundTrip[T any](t *testing.T, v T) ([]byte, T) {
	t.Helper()
	data, err := yaml.Marshal(v)
	require.NoError(t, err, "marshal should succeed")

	var got T
	err = yaml.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal should succeed")
	return data, got
}

func TestHTTPTransportYAMLRoundTrip(t *testing.T) {
	orig := HTTPTransport{
		Method:        "POST",
		Path:          "/api/v1/test",
		SuccessStatus: 200,
		NoContent:     false,
		Responses: map[int]HTTPResponse{
			401: {Description: "Unauthorized", SchemaRef: "error.json"},
			403: {Description: "Forbidden", SchemaRef: "error.json"},
		},
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "method: POST")
	assert.Contains(t, string(data), "path: /api/v1/test")
	assert.Contains(t, string(data), "successStatus: 200")
}

func TestHTTPTransportYAMLRoundTrip_NoContent(t *testing.T) {
	orig := HTTPTransport{
		Method:        "DELETE",
		Path:          "/api/v1/users/{userId}",
		SuccessStatus: 204,
		NoContent:     true,
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.NotContains(t, string(data), "responses")
}

func TestHTTPTransportYAMLRoundTrip_PathParams(t *testing.T) {
	orig := HTTPTransport{
		Method:        "GET",
		Path:          "/api/v1/config/{key}",
		PathParams:    map[string]ParamSchema{"key": {Type: "string"}},
		SuccessStatus: 200,
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "pathParams:")
	assert.Contains(t, string(data), "    type: string")
	assert.NotContains(t, string(data), "queryParams")
}

func TestHTTPTransportYAMLRoundTrip_QueryParams(t *testing.T) {
	truthy := true
	falsy := false
	orig := HTTPTransport{
		Method: "GET",
		Path:   "/api/v1/config/",
		QueryParams: map[string]ParamSchema{
			"cursor": {Type: "string", Required: &falsy},
			"limit":  {Type: "integer", Required: &truthy},
			"id":     {Type: "string", Format: "uuid"},
		},
		SuccessStatus: 200,
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Equal(t, "integer", got.QueryParams["limit"].Type)
	require.NotNil(t, got.QueryParams["limit"].Required)
	assert.True(t, *got.QueryParams["limit"].Required)
	require.NotNil(t, got.QueryParams["cursor"].Required)
	assert.False(t, *got.QueryParams["cursor"].Required)
	assert.Equal(t, "uuid", got.QueryParams["id"].Format)
}

func TestHTTPTransportYAMLRoundTrip_PathAndQueryCoexist(t *testing.T) {
	falsy := false
	orig := HTTPTransport{
		Method: "GET",
		Path:   "/api/v1/access/roles/{userID}",
		PathParams: map[string]ParamSchema{
			"userID": {Type: "string", Format: "uuid"},
		},
		QueryParams: map[string]ParamSchema{
			"cursor": {Type: "string", Required: &falsy},
		},
		SuccessStatus: 200,
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestHTTPTransportYAMLOmitEmptyPathQuery(t *testing.T) {
	orig := HTTPTransport{
		Method:        "POST",
		Path:          "/api/v1/access/sessions/login",
		SuccessStatus: 201,
	}
	data, _ := roundTrip(t, orig)
	// Both maps are omitempty — they must not serialize when absent.
	assert.NotContains(t, string(data), "pathParams")
	assert.NotContains(t, string(data), "queryParams")
}

func TestParamSchemaYAMLRoundTrip(t *testing.T) {
	truthy := true
	orig := ParamSchema{Type: "integer", Required: &truthy, Format: "int64"}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestParamTypesWhitelist(t *testing.T) {
	for _, name := range []string{"string", "integer", "number", "boolean", "uuid"} {
		assert.True(t, ParamTypes[name], "%s should be accepted", name)
	}
	for _, name := range []string{"int", "float", "array", "", "object"} {
		assert.False(t, ParamTypes[name], "%s should be rejected", name)
	}
}

func TestHTTPResponseYAMLRoundTrip(t *testing.T) {
	orig := HTTPResponse{
		Description: "Not Found",
		SchemaRef:   "error.json",
	}
	_, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestSchemaRefsYAMLRoundTrip(t *testing.T) {
	orig := SchemaRefs{
		Request:  "request.schema.json",
		Response: "response.schema.json",
		Payload:  "payload.schema.json",
		Headers:  "headers.schema.json",
		Extra:    map[string]string{"custom": "custom.schema.json"},
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "custom: custom.schema.json")
}

func TestSchemaRefsInlinePrecedence(t *testing.T) {
	raw := `request: req.json
response: res.json
custom: extra.json
`
	var sr SchemaRefs
	require.NoError(t, yaml.Unmarshal([]byte(raw), &sr))

	assert.Equal(t, "req.json", sr.Request)
	assert.Equal(t, "res.json", sr.Response)
	assert.Empty(t, sr.Payload)

	assert.Equal(t, map[string]string{"custom": "extra.json"}, sr.Extra)

	_, hasRequest := sr.Extra["request"]
	assert.False(t, hasRequest, "named field 'request' must not leak into Extra")
}

func TestSchemaRefsExtraRoundTrip(t *testing.T) {
	orig := SchemaRefs{
		Request: "req.json",
		Extra:   map[string]string{"custom": "extra.json"},
	}
	data, got := roundTrip(t, orig)
	assert.Equal(t, "req.json", got.Request)
	assert.Equal(t, "extra.json", got.Extra["custom"])
	assert.Contains(t, string(data), "custom: extra.json")
}

func TestSchemaRefsEmpty(t *testing.T) {
	var sr SchemaRefs
	_, got := roundTrip(t, sr)
	assert.Equal(t, sr, got, "empty SchemaRefs round-trip should preserve zero value")
}
