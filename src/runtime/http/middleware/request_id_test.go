package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestID_ExistingHeader(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := ctxkeys.RequestIDFrom(r.Context())
		assert.True(t, ok)
		assert.Equal(t, "my-request-id", id)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "my-request-id")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	assert.Equal(t, "my-request-id", rec.Header().Get("X-Request-Id"))
}

func TestRequestID_GeneratesUUID(t *testing.T) {
	var capturedID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := ctxkeys.RequestIDFrom(r.Context())
		assert.True(t, ok)
		capturedID = id
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.NotEmpty(t, capturedID)
	assert.Len(t, capturedID, 36) // UUID format: 8-4-4-4-12
	assert.Equal(t, capturedID, rec.Header().Get("X-Request-Id"))
}

func TestRequestID_UniquenessAcrossRequests(t *testing.T) {
	ids := make(map[string]bool)
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	for range 100 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		id := rec.Header().Get("X-Request-Id")
		assert.False(t, ids[id], "duplicate request ID: %s", id)
		ids[id] = true
	}
}
