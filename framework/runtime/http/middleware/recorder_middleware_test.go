package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorder_CreatesStateInContext(t *testing.T) {
	var gotState *RecorderState

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotState = RecorderStateFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := Recorder(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.NotNil(t, gotState, "Recorder middleware must store RecorderState in context")
	assert.Equal(t, http.StatusOK, gotState.Status())
}

func TestRecorder_DownstreamSeesWrittenStatus(t *testing.T) {
	var gotState *RecorderState

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotState = RecorderStateFrom(r.Context())
		w.WriteHeader(http.StatusCreated)
	})

	handler := Recorder(inner)
	req := httptest.NewRequest(http.MethodPost, "/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.NotNil(t, gotState)
	assert.Equal(t, http.StatusCreated, gotState.Status())
	assert.True(t, gotState.Committed())
}
