package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// schemaRoundTrip marshals v to YAML, unmarshals into a new T, and returns both.
func schemaRoundTrip[T any](t *testing.T, v T) ([]byte, T) {
	t.Helper()
	data, err := yaml.Marshal(v)
	require.NoError(t, err, "marshal should succeed")

	var got T
	err = yaml.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal should succeed")
	return data, got
}

func TestHTTPTransportYAMLRoundTrip(t *testing.T) {
	orig := HTTPTransportMeta{
		Method:        "POST",
		Path:          "/api/v1/test",
		SuccessStatus: 200,
		NoContent:     false,
		Responses: map[int]HTTPResponseMeta{
			401: {Description: "Unauthorized", SchemaRef: "error.json"},
			403: {Description: "Forbidden", SchemaRef: "error.json"},
		},
	}
	data, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "method: POST")
	assert.Contains(t, string(data), "path: /api/v1/test")
	assert.Contains(t, string(data), "successStatus: 200")
}

func TestHTTPTransportYAMLRoundTrip_NoContent(t *testing.T) {
	orig := HTTPTransportMeta{
		Method:        "DELETE",
		Path:          "/api/v1/users/{userId}",
		SuccessStatus: 204,
		NoContent:     true,
	}
	data, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.NotContains(t, string(data), "responses")
}

func TestHTTPTransportYAMLRoundTrip_PathParams(t *testing.T) {
	orig := HTTPTransportMeta{
		Method:        "GET",
		Path:          "/api/v1/config/{key}",
		PathParams:    map[string]ParamSchema{"key": {Type: "string"}},
		SuccessStatus: 200,
	}
	data, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "pathParams:")
	assert.Contains(t, string(data), "    type: string")
	assert.NotContains(t, string(data), "queryParams")
}

func TestHTTPTransportYAMLRoundTrip_QueryParams(t *testing.T) {
	truthy := true
	falsy := false
	orig := HTTPTransportMeta{
		Method: "GET",
		Path:   "/api/v1/config/",
		QueryParams: map[string]ParamSchema{
			"cursor": {Type: "string", Required: &falsy},
			"limit":  {Type: "integer", Required: &truthy},
			"id":     {Type: "string", Format: "uuid"},
		},
		SuccessStatus: 200,
	}
	_, got := schemaRoundTrip(t, orig)
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
	orig := HTTPTransportMeta{
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
	_, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestHTTPTransportYAMLOmitEmptyPathQuery(t *testing.T) {
	orig := HTTPTransportMeta{
		Method:        "POST",
		Path:          "/api/v1/access/sessions/login",
		SuccessStatus: 201,
	}
	data, _ := schemaRoundTrip(t, orig)
	// Both maps are omitempty — they must not serialize when absent.
	assert.NotContains(t, string(data), "pathParams")
	assert.NotContains(t, string(data), "queryParams")
}

func TestParamSchemaYAMLRoundTrip(t *testing.T) {
	truthy := true
	orig := ParamSchema{Type: "integer", Required: &truthy, Format: "int64"}
	_, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
}

// TestParamSchemaRequiredThreeStates locks in the three-state Required
// semantics documented on the ParamSchema godoc: nil means "not declared",
// false means "explicit optional", true means "explicit required". YAML
// omitempty must emit the field for false and true, and omit it for nil.
// FMT-13 depends on this distinction to reject `required: false` on path
// parameters while accepting an omitted `required:` there.
func TestParamSchemaRequiredThreeStates(t *testing.T) {
	truthy := true
	falsy := false

	t.Run("nil required is omitted", func(t *testing.T) {
		data, got := schemaRoundTrip(t, ParamSchema{Type: "string"})
		assert.Nil(t, got.Required)
		assert.NotContains(t, string(data), "required:")
	})

	t.Run("false required is emitted and preserved", func(t *testing.T) {
		data, got := schemaRoundTrip(t, ParamSchema{Type: "string", Required: &falsy})
		require.NotNil(t, got.Required)
		assert.False(t, *got.Required)
		assert.Contains(t, string(data), "required: false")
	})

	t.Run("true required is emitted and preserved", func(t *testing.T) {
		data, got := schemaRoundTrip(t, ParamSchema{Type: "string", Required: &truthy})
		require.NotNil(t, got.Required)
		assert.True(t, *got.Required)
		assert.Contains(t, string(data), "required: true")
	})
}

// TestParamSchemaConstraintsRoundTrip locks in YAML round-trip semantics for
// the four new constraint fields (MinLength, MaxLength, Minimum, Maximum).
// They are *int so omitted/zero/non-zero are three distinct states, mirroring
// the three-state Required pattern (FMT-25 governance rule depends on the
// distinction between "no declaration" and "declared as zero").
func TestParamSchemaConstraintsRoundTrip(t *testing.T) {
	zero := 0
	nonZero := 500
	t.Run("nil constraints are omitted", func(t *testing.T) {
		data, got := schemaRoundTrip(t, ParamSchema{Type: "string"})
		assert.Nil(t, got.MinLength)
		assert.Nil(t, got.MaxLength)
		assert.Nil(t, got.Minimum)
		assert.Nil(t, got.Maximum)
		assert.NotContains(t, string(data), "minLength")
		assert.NotContains(t, string(data), "maxLength")
		assert.NotContains(t, string(data), "minimum")
		assert.NotContains(t, string(data), "maximum")
	})
	t.Run("zero values are emitted (not omitted)", func(t *testing.T) {
		data, got := schemaRoundTrip(t, ParamSchema{
			Type:      "string",
			MinLength: &zero,
		})
		require.NotNil(t, got.MinLength)
		assert.Equal(t, 0, *got.MinLength)
		assert.Contains(t, string(data), "minLength: 0")
	})
	t.Run("non-zero string constraints round-trip", func(t *testing.T) {
		one := 1
		twoFiftySix := 256
		data, got := schemaRoundTrip(t, ParamSchema{
			Type:      "string",
			MinLength: &one,
			MaxLength: &twoFiftySix,
		})
		require.NotNil(t, got.MinLength)
		require.NotNil(t, got.MaxLength)
		assert.Equal(t, 1, *got.MinLength)
		assert.Equal(t, 256, *got.MaxLength)
		assert.Contains(t, string(data), "minLength: 1")
		assert.Contains(t, string(data), "maxLength: 256")
	})
	t.Run("integer constraints round-trip", func(t *testing.T) {
		one := 1
		data, got := schemaRoundTrip(t, ParamSchema{
			Type:    "integer",
			Minimum: &one,
			Maximum: &nonZero,
		})
		require.NotNil(t, got.Minimum)
		require.NotNil(t, got.Maximum)
		assert.Equal(t, 1, *got.Minimum)
		assert.Equal(t, 500, *got.Maximum)
		assert.Contains(t, string(data), "minimum: 1")
		assert.Contains(t, string(data), "maximum: 500")
	})
}

func TestParamTypesWhitelist(t *testing.T) {
	for _, name := range []string{"string", "integer", "number", "boolean"} {
		assert.True(t, ParamTypes[name], "%s should be accepted", name)
	}
	for _, name := range []string{"int", "float", "array", "", "object", "uuid"} {
		assert.False(t, ParamTypes[name], "%s should be rejected", name)
	}
}

func TestHTTPResponseYAMLRoundTrip(t *testing.T) {
	orig := HTTPResponseMeta{
		Description: "Not Found",
		SchemaRef:   "error.json",
	}
	_, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
}

func TestSchemaRefsYAMLRoundTrip(t *testing.T) {
	orig := SchemaRefsMeta{
		Request:  "request.schema.json",
		Response: "response.schema.json",
		Payload:  "payload.schema.json",
		Headers:  "headers.schema.json",
		Extra:    map[string]string{"custom": "custom.schema.json"},
	}
	data, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "custom: custom.schema.json")
}

func TestSchemaRefsEmpty(t *testing.T) {
	var sr SchemaRefsMeta
	_, got := schemaRoundTrip(t, sr)
	assert.Equal(t, sr, got, "empty SchemaRefs round-trip should preserve zero value")
}
