package configread

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHttpConfigGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.get.v1")

	// Lock the wire-level contract: drift in contract.yaml method/path would
	// silently break handlers registered via cells/configcore/cell.go.
	require.NotNil(t, c.HTTP, "http.config.get.v1 must declare endpoints.http")
	assert.Equal(t, "GET", c.HTTP.Method)
	assert.Equal(t, "/api/v1/config/{key}", c.HTTP.Path)

	// Non-sensitive entry: sensitive=false, value is the real value.
	c.ValidateResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp","sensitive":false,"version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	// Sensitive entry: sensitive=true, value must be redacted.
	c.ValidateResponse(t, []byte(`{"data":{"id":"c-2","key":"db.password","value":"******","sensitive":true,"version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
	// PR-A9: list-shape payloads belong to http.config.list.v1; the single-entry
	// contract must reject array data.
	c.MustRejectResponse(t, []byte(`{"data":[],"nextCursor":"","hasMore":false}`))
	// Missing sensitive field must be rejected (schema requires it).
	c.MustRejectResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp","version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
}

func TestHttpConfigListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.list.v1")

	require.NotNil(t, c.HTTP, "http.config.list.v1 must declare endpoints.http")
	assert.Equal(t, "GET", c.HTTP.Method)
	assert.Equal(t, "/api/v1/config/", c.HTTP.Path)

	// Non-sensitive entry: sensitive=false.
	c.ValidateResponse(t, []byte(`{"data":[{"id":"c-1","key":"app.name","value":"myapp","sensitive":false,"version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}],"nextCursor":"","hasMore":false}`))
	// Sensitive entry: sensitive=true, value redacted.
	c.ValidateResponse(t, []byte(`{"data":[{"id":"c-2","key":"db.password","value":"******","sensitive":true,"version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}],"nextCursor":"","hasMore":false}`))
	// Single-entry payload belongs to http.config.get.v1.
	c.MustRejectResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp","sensitive":false,"version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	// Missing pagination envelope must be rejected.
	c.MustRejectResponse(t, []byte(`{"data":[]}`))
	// Missing sensitive field must be rejected (schema requires it for each item).
	c.MustRejectResponse(t, []byte(`{"data":[{"id":"c-1","key":"app.name","value":"myapp","version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}],"nextCursor":"","hasMore":false}`))
}
