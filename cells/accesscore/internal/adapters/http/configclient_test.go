package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/runtime/auth"
)

// newTestRing creates a test HMAC key ring with a 32-byte key.
func newTestRing(t *testing.T) *auth.HMACKeyRing {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("test-hmac-key-32-bytes-long-xxxxx"), nil)
	require.NoError(t, err)
	return ring
}

func TestHTTPConfigGetter_GetEntry_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/internal/v1/config/app.name", r.URL.Path)
		// Service token header must be present.
		assert.NotEmpty(t, r.Header.Get("Authorization"))
		assert.Contains(t, r.Header.Get("Authorization"), "ServiceToken")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":        "cfg-1",
				"key":       "app.name",
				"value":     "gocell",
				"sensitive": false,
				"version":   3,
				"createdAt": "2024-01-01T00:00:00Z",
				"updatedAt": "2024-01-02T00:00:00Z",
			},
		})
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetterWithHTTPClient(srv.URL, ring, srv.Client(), clock.Real())
	entry, err := client.GetEntry(context.Background(), "app.name")
	require.NoError(t, err)
	assert.Equal(t, "app.name", entry.Key)
	assert.Equal(t, "gocell", entry.Value)
	assert.False(t, entry.Sensitive)
	assert.Equal(t, 3, entry.Version)
}

func TestHTTPConfigGetter_GetEntry_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "ERR_CONFIG_NOT_FOUND", "message": "key not found"},
		})
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetterWithHTTPClient(srv.URL, ring, srv.Client(), clock.Real())
	_, err := client.GetEntry(context.Background(), "missing.key")
	errcodetest.AssertCode(t, err, errcode.ErrConfigNotFound)
}

func TestHTTPConfigGetter_GetEntry_SensitiveEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":        "cfg-2",
				"key":       "db.password",
				"value":     "s3cret!",
				"sensitive": true,
				"version":   1,
				"createdAt": "2024-01-01T00:00:00Z",
				"updatedAt": "2024-01-01T00:00:00Z",
			},
		})
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetterWithHTTPClient(srv.URL, ring, srv.Client(), clock.Real())
	entry, err := client.GetEntry(context.Background(), "db.password")
	require.NoError(t, err)
	assert.Equal(t, "db.password", entry.Key)
	assert.True(t, entry.Sensitive)
}

func TestHTTPConfigGetter_GetEntry_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetterWithHTTPClient(srv.URL, ring, srv.Client(), clock.Real())
	_, err := client.GetEntry(context.Background(), "any.key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status 500")
}

func TestHTTPConfigGetter_GetEntry_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetterWithHTTPClient(srv.URL, ring, srv.Client(), clock.Real())
	_, err := client.GetEntry(context.Background(), "some.key")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUnauthorized, ec.Code)
}

func TestHTTPConfigGetter_GetEntry_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetterWithHTTPClient(srv.URL, ring, srv.Client(), clock.Real())
	_, err := client.GetEntry(context.Background(), "some.key")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthForbidden, ec.Code)
}

func TestNewHTTPConfigGetter_Constructor(t *testing.T) {
	ring := newTestRing(t)
	g := NewHTTPConfigGetter("http://localhost:9090", ring, clock.Real())
	require.NotNil(t, g)
	assert.Equal(t, "http://localhost:9090", g.baseURL)
}

// TestHTTPConfigGetter_GetEntry_EmptyToken covers the token=="" branch in
// GetEntry: when the ring is nil, GenerateServiceToken returns "" and GetEntry
// returns ErrInternal without making an HTTP call.
func TestHTTPConfigGetter_GetEntry_EmptyToken(t *testing.T) {
	// nil ring causes GenerateServiceToken to return "".
	client := NewHTTPConfigGetterWithHTTPClient("http://localhost:19090", nil, http.DefaultClient, clock.Real())
	_, err := client.GetEntry(context.Background(), "any.key")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrInternal, ec.Code)
}

// TestHTTPConfigGetter_GetEntry_BadResponseBody covers the json decode error path.
func TestHTTPConfigGetter_GetEntry_BadResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json-at-all"))
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetterWithHTTPClient(srv.URL, ring, srv.Client(), clock.Real())
	_, err := client.GetEntry(context.Background(), "any.key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}
