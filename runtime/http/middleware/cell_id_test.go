package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

func TestCellAttribution_WritesCtxkey(t *testing.T) {
	handler := CellAttribution(func(method, path string) (string, bool) {
		assert.Equal(t, http.MethodGet, method)
		assert.Equal(t, "/api/v1/access/users", path)
		return "accesscore", true
	})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		v, ok := ctxkeys.CellIDFrom(r.Context())
		assert.True(t, ok)
		assert.Equal(t, "accesscore", v)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/access/users", nil))
}

func TestCellAttribution_UnresolvedLeavesCtxkeyAbsent(t *testing.T) {
	handler := CellAttribution(func(string, string) (string, bool) {
		return "", false
	})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, ok := ctxkeys.CellIDFrom(r.Context())
		assert.False(t, ok)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
}

func TestRuntimeCellIDSentinel_IsExportedConstant(t *testing.T) {
	assert.Equal(t, "_runtime", RuntimeCellIDSentinel)
}
