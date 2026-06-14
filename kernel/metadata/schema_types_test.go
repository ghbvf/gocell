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

func TestHTTPTransportYAMLRoundTrip_Headers(t *testing.T) {
	truthy := true
	orig := HTTPTransportMeta{
		Method: "POST",
		Path:   "/api/v1/access/sessions/login",
		Headers: map[string]ParamSchema{
			"X-Tenant-ID": {Type: "string", Format: "uuid", Required: &truthy},
		},
		SuccessStatus: 201,
	}
	data, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "headers:")
	assert.Equal(t, "string", got.Headers["X-Tenant-ID"].Type)
	assert.Equal(t, "uuid", got.Headers["X-Tenant-ID"].Format)
	require.NotNil(t, got.Headers["X-Tenant-ID"].Required)
	assert.True(t, *got.Headers["X-Tenant-ID"].Required)
}

func TestHTTPTransportYAMLOmitEmptyHeaders(t *testing.T) {
	orig := HTTPTransportMeta{
		Method:        "POST",
		Path:          "/api/v1/access/sessions/login",
		SuccessStatus: 201,
	}
	data, _ := schemaRoundTrip(t, orig)
	// headers is omitempty — it must not serialize when absent.
	assert.NotContains(t, string(data), "headers")
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

// TestIdempotencyFrameworkStatuses verifies the single-source derivation of the
// framework-injected {409, 422} set (#1469/#1450 review F4 + #1591): the
// idempotency middleware (extractIdentity) only claims for a PrincipalUser with a
// non-empty Subject. A mutating non-exempt route reachable by a PrincipalUser
// returns 409 (ClaimBusy) + 422 (key-reused); a route whose auth shape resolves to
// a non-PrincipalUser principal — public (anonymous), bootstrap (basic-auth), or
// internal (service-token) — is bypassed by the middleware and returns nil, as do
// exempt routes and non-mutating methods. The set is the SOLE computed source (never
// hand-authored per contract), so the declaration surface cannot drift from the
// middleware (bound to idemhttp.FrameworkStatuses() by archtest).
func TestIdempotencyFrameworkStatuses(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		exempt bool
		auth   HTTPAuthMeta
		want   []int
	}{
		// PrincipalUser-reachable (JWT, primary listener, non-internal path) → {409,422}.
		{"POST jwt → 409,422", "POST", "/api/v1/x", false, HTTPAuthMeta{}, []int{409, 422}},
		{"PUT jwt → 409,422", "PUT", "/api/v1/x", false, HTTPAuthMeta{}, []int{409, 422}},
		{"PATCH jwt → 409,422", "PATCH", "/api/v1/x", false, HTTPAuthMeta{}, []int{409, 422}},
		{"DELETE jwt → 409,422", "DELETE", "/api/v1/x", false, HTTPAuthMeta{}, []int{409, 422}},
		{"POST serviceOwned (still PrincipalUser) → 409,422", "POST", "/api/v1/x", false, HTTPAuthMeta{ServiceOwned: true}, []int{409, 422}},
		{"POST pwResetExempt → 409,422", "POST", "/api/v1/x", false, HTTPAuthMeta{PasswordResetExempt: true}, []int{409, 422}},
		{"POST admin path (operator JWT) → 409,422", "POST", "/admin/v1/x", false, HTTPAuthMeta{}, []int{409, 422}},
		// Non-PrincipalUser auth shapes → middleware bypass → nil (#1591).
		{"POST public (anonymous) → none", "POST", "/api/v1/x", false, HTTPAuthMeta{Public: true}, nil},
		{"PUT public → none", "PUT", "/api/v1/x", false, HTTPAuthMeta{Public: true}, nil},
		{"POST bootstrap (basic-auth) → none", "POST", "/api/v1/x/setup/admin", false, HTTPAuthMeta{Bootstrap: true}, nil},
		{"POST internal path (service-token) → none", "POST", "/internal/v1/x", false, HTTPAuthMeta{}, nil},
		{"POST internal clientsOnly → none", "POST", "/internal/v1/x", false, HTTPAuthMeta{ClientsOnly: true}, nil},
		// Method / exempt gating (unchanged).
		{"GET jwt → none", "GET", "/api/v1/x", false, HTTPAuthMeta{}, nil},
		{"HEAD jwt → none", "HEAD", "/api/v1/x", false, HTTPAuthMeta{}, nil},
		{"POST exempt → none", "POST", "/api/v1/x", true, HTTPAuthMeta{}, nil},
		{"DELETE exempt → none", "DELETE", "/api/v1/x", true, HTTPAuthMeta{}, nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := &HTTPTransportMeta{
				Method:      tc.method,
				Path:        tc.path,
				Auth:        tc.auth,
				Idempotency: HTTPIdempotencyMeta{Exempt: tc.exempt},
			}
			assert.Equal(t, tc.want, h.IdempotencyFrameworkStatuses())
		})
	}

	// nil receiver is safe (governance may call on an absent HTTP block).
	var nilHTTP *HTTPTransportMeta
	assert.Nil(t, nilHTTP.IdempotencyFrameworkStatuses())
}

// TestFrameworkIdempotencyStatuses pins the package-level canonical set — the
// single literal source the oracle and CH-07 both reference (no third hardcode).
func TestFrameworkIdempotencyStatuses(t *testing.T) {
	assert.Equal(t, []int{409, 422}, FrameworkIdempotencyStatuses())
}

// TestHTTPIdempotencyMeta_RoundTrip verifies the idempotency block is a sibling
// of auth under endpoints.http and survives YAML round-trip (#1469 review F7).
func TestHTTPIdempotencyMeta_RoundTrip(t *testing.T) {
	in := HTTPTransportMeta{
		Method:      "POST",
		Path:        "/api/v1/sample/test",
		Auth:        HTTPAuthMeta{Public: true},
		Idempotency: HTTPIdempotencyMeta{Exempt: true},
	}
	data, got := schemaRoundTrip(t, in)
	assert.Equal(t, in, got, "HTTPTransportMeta round-trip must preserve idempotency.exempt")
	assert.Contains(t, string(data), "idempotency:", "idempotency must serialize as a sibling of auth")
}

// TestGRPCTransportYAMLRoundTrip_Methods verifies the per-method auth overlay
// (#1675) survives YAML round-trip: the sparse methods[] slice with a public
// flag marshals and unmarshals losslessly.
func TestGRPCTransportYAMLRoundTrip_Methods(t *testing.T) {
	orig := GRPCTransportMeta{
		Service: "device.command.v1.DeviceCommandService",
		Proto:   "contracts/grpc/device/command/v1/device_command.proto",
		Methods: []GRPCMethodMeta{
			{Name: "Check", Public: true},
		},
	}
	data, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Contains(t, string(data), "name: Check")
	assert.Contains(t, string(data), "public: true")
}

// TestGRPCTransportYAMLRoundTrip_NoMethods verifies the overlay is optional: a
// transport with no methods[] serializes without the key and unmarshals to a
// nil slice (the fail-closed default — every RPC authed).
func TestGRPCTransportYAMLRoundTrip_NoMethods(t *testing.T) {
	orig := GRPCTransportMeta{
		Service: "x.v1.S",
		Proto:   "contracts/grpc/x/v1/x.proto",
	}
	data, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.Nil(t, got.Methods)
	assert.NotContains(t, string(data), "methods")
}

// TestGRPCMethodMeta_PublicOmitEmpty verifies public:false (the authed default)
// omits from YAML so a sparse overlay entry serializes cleanly.
func TestGRPCMethodMeta_PublicOmitEmpty(t *testing.T) {
	orig := GRPCMethodMeta{Name: "Check"}
	data, got := schemaRoundTrip(t, orig)
	assert.Equal(t, orig, got)
	assert.NotContains(t, string(data), "public")
}
