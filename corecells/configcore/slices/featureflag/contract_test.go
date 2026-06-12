package featureflag

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.True(t, has403, "http.config.flags.list.v1 must declare 403 (route is flag:read-gated (PDP))")
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
	assert.True(t, has403, "http.config.flags.get.v1 must declare 403 (route is flag:read-gated (PDP))")
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
	assert.True(t, has403, "http.config.flags.evaluate.v1 must declare 403 (route is flag:read-gated (PDP))")
	c.ValidateErrorResponse(t, 403, []byte(`{"error":{"code":"ERR_AUTH_FORBIDDEN","message":"access denied","details":[]}}`))

	c.ValidateRequest(t, []byte(`{"subject":"user-123"}`))
	c.ValidateResponse(t, []byte(`{"data":{"key":"dark-mode","enabled":true}}`))
	c.MustRejectRequest(t, []byte(`{"subject":"x","extra":"bad"}`))
}

// TestFeatureFlag_Anonymous_Returns401 covers the no-principal (anonymous) →
// 401 ERR_AUTH_UNAUTHORIZED negative path for the flag:read-gated endpoints.
// A nil principal causes auth.RequirePermission to short-circuit before reaching
// the PDP (no Authorizer needed). Mirrors the configread slice's pattern.
func TestFeatureFlag_Anonymous_Returns401(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	handler, _ := setupHandler()

	endpoints := []struct {
		name       string
		contractID string
		method     string
		path       string
	}{
		{"list", "http.config.flags.list.v1", http.MethodGet, flagsBasePath + "/"},
		{"get", "http.config.flags.get.v1", http.MethodGet, flagsBasePath + "/some-key"},
		{"evaluate", "http.config.flags.evaluate.v1", http.MethodPost, flagsBasePath + "/some-key/evaluate"},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			c := contracttest.LoadByID(t, root, ep.contractID)
			_, has401 := c.HTTP.Responses[401]
			assert.True(t, has401, "%s must declare 401 in contract.yaml", ep.contractID)

			rec := httptest.NewRecorder()
			// No principal injected — anonymous request.
			req := httptest.NewRequest(ep.method, ep.path, nil)
			handler.ServeHTTP(rec, req)

			require.Equal(t, http.StatusUnauthorized, rec.Code,
				"anonymous request to %s %s must be 401; got body=%s",
				ep.method, ep.path, rec.Body)
			var resp struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", resp.Error.Code)
			c.ValidateErrorResponse(t, http.StatusUnauthorized, rec.Body.Bytes())
		})
	}
}
