package featureflag

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tests/contracttest"
)

// TestHttpConfigFlagsListV1_QueryParamConstraints asserts that the cursor query
// param schema rejects values exceeding maxLength: 4096, and the limit query
// param rejects 0 (minimum: 1) and 501 (maximum: 500).
func TestHttpConfigFlagsListV1_QueryParamConstraints(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.flags.list.v1")
	c.MustRejectQueryParam(t, "cursor", string(make([]byte, 4097))) // violates maxLength: 4096
	c.ValidateQueryParam(t, "limit", "1")
	c.MustRejectQueryParam(t, "limit", "0")   // violates minimum: 1
	c.MustRejectQueryParam(t, "limit", "501") // violates maximum: 500
}

// TestHttpConfigFlagsGetV1_PathParamConstraints asserts that the key path param
// schema rejects empty string (violates minLength: 1).
func TestHttpConfigFlagsGetV1_PathParamConstraints(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.flags.get.v1")
	c.ValidatePathParam(t, "key", "valid-key")
	c.MustRejectPathParam(t, "key", "") // violates minLength: 1
}

// TestHttpConfigFlagsEvaluateV1_PathParamConstraints asserts that the key path
// param schema rejects empty string (violates minLength: 1).
func TestHttpConfigFlagsEvaluateV1_PathParamConstraints(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.flags.evaluate.v1")
	c.ValidatePathParam(t, "key", "valid-key")
	c.MustRejectPathParam(t, "key", "") // violates minLength: 1
}

func TestHttpConfigFlagsListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.flags.list.v1")

	// PR-CFG-C contract-as-auth-truth: route is admin-gated.
	_, has403 := c.HTTP.Responses[403]
	assert.True(t, has403, "http.config.flags.list.v1 must declare 403 (route is RoleAdmin-gated)")
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"access denied","details":[]}}`))

	c.ValidateResponse(t, []byte(`{"data":[{"id":"f-1","key":"dark-mode","type":"boolean",`+
		`"enabled":true,"rolloutPercentage":100,"description":"Dark mode toggle",`+
		`"version":1,"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:00:00Z"}],`+
		`"nextCursor":"","hasMore":false}`))
	c.MustRejectResponse(t, []byte(`{"data":[{"id":"f-1","key":"dark-mode","type":"boolean",`+
		`"enabled":true,"rolloutPercentage":100}],"nextCursor":"","hasMore":false}`))
	c.MustRejectResponse(t, []byte(`{"data":"not-array","hasMore":false}`))
	// D5: type constraint — version must be integer (minimum:1), not string.
	c.MustRejectResponse(t, []byte(`{"data":[{"id":"f-1","key":"dark-mode","type":"boolean",`+
		`"enabled":true,"rolloutPercentage":100,"description":"Dark mode toggle",`+
		`"version":"not-a-number","createdAt":"2024-01-01T00:00:00Z",`+
		`"updatedAt":"2024-01-01T00:00:00Z"}],"nextCursor":"","hasMore":false}`))
}

func TestHttpConfigFlagsGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.flags.get.v1")

	_, has403 := c.HTTP.Responses[403]
	assert.True(t, has403, "http.config.flags.get.v1 must declare 403 (route is RoleAdmin-gated)")
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"access denied","details":[]}}`))

	c.ValidateResponse(t, []byte(`{"data":{"id":"f-1","key":"dark-mode","type":"boolean",`+
		`"enabled":true,"rolloutPercentage":100,"description":"Dark mode toggle",`+
		`"version":1,"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"data":{"id":"f-1","key":"dark-mode","type":"boolean",`+
		`"enabled":true,"rolloutPercentage":100}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
	// D5: type constraint — version must be integer (minimum:1), not string.
	c.MustRejectResponse(t, []byte(`{"data":{"id":"f-1","key":"dark-mode","type":"boolean",`+
		`"enabled":true,"rolloutPercentage":100,"description":"Dark mode toggle",`+
		`"version":"not-a-number","createdAt":"2024-01-01T00:00:00Z",`+
		`"updatedAt":"2024-01-01T00:00:00Z"}}`))
}

func TestHttpConfigFlagsEvaluateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.flags.evaluate.v1")

	_, has403 := c.HTTP.Responses[403]
	assert.True(t, has403, "http.config.flags.evaluate.v1 must declare 403 (route is RoleAdmin-gated)")
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"access denied","details":[]}}`))

	c.ValidateRequest(t, []byte(`{"subject":"user-123"}`))
	c.ValidateResponse(t, []byte(`{"data":{"key":"dark-mode","enabled":true}}`))
	c.MustRejectRequest(t, []byte(`{"subject":"x","extra":"bad"}`))
}
