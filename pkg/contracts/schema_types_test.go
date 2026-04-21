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
