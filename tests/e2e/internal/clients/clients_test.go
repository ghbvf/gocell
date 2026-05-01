//go:build e2e

package clients_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/tests/e2e/internal/clients"
)

func TestBaseURL_DefaultWhenEnvUnsetOrEmpty(t *testing.T) {
	t.Setenv("E2E_BASE_URL", "")
	assert.Equal(t, "http://localhost:8080", clients.BaseURL())
}

func TestBaseURL_OverridenViaEnv(t *testing.T) {
	t.Setenv("E2E_BASE_URL", "http://primary.example:9000")
	assert.Equal(t, "http://primary.example:9000", clients.BaseURL())
}

func TestHealthURL_DefaultWhenEnvUnsetOrEmpty(t *testing.T) {
	t.Setenv("E2E_HEALTH_URL", "")
	assert.Equal(t, "http://localhost:9091", clients.HealthURL())
}

func TestHealthURL_OverridenViaEnv(t *testing.T) {
	t.Setenv("E2E_HEALTH_URL", "http://health.example:7000")
	assert.Equal(t, "http://health.example:7000", clients.HealthURL())
}

func TestAdminToken_NonEmptyEnv_Returned(t *testing.T) {
	t.Setenv("E2E_ADMIN_TOKEN", "tok-abc-123")
	assert.Equal(t, "tok-abc-123", clients.AdminToken(t))
}

// Empty-env fail-fast path is intentionally not unit-tested here:
// require.NotEmpty calls t.FailNow which propagates through testing.T.Run
// and would mark the parent test failed regardless of the assertion logic
// wrapping it. The contract is structurally enforced by require.NotEmpty
// itself; the integration evidence lives in the encryption tests, where
// a missing E2E_ADMIN_TOKEN aborts the test before any anonymous request
// can sneak through.

func TestDoJSON_AddsAuthHeaderAndJSONBodyWhenTokenPresent(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotCT     string
		gotBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("E2E_BASE_URL", srv.URL)

	resp := clients.DoJSON(t, http.MethodPost, "/api/v1/x", map[string]string{"k": "v"}, "tok-xyz")
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/v1/x", gotPath)
	assert.Equal(t, "Bearer tok-xyz", gotAuth)
	assert.Equal(t, "application/json", gotCT)
	assert.JSONEq(t, `{"k":"v"}`, string(gotBody))
}

func TestDoJSON_OmitsAuthHeaderAndBodyWhenTokenAndBodyAreEmpty(t *testing.T) {
	var (
		gotAuth string
		gotBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("E2E_BASE_URL", srv.URL)

	resp := clients.DoJSON(t, http.MethodGet, "/y", nil, "")
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, gotAuth, "no Authorization header when token is empty")
	assert.Empty(t, gotBody, "no body bytes when body argument is nil")
}

func TestWaitForReady_ReturnsWhenReadyzReturns200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/readyz", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("E2E_HEALTH_URL", srv.URL)

	// Should return promptly (well under timeout) since the server replies 200.
	clients.WaitForReady(t, testtime.EventuallyLong)
}

func TestWaitForReady_RetriesUntilReady(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			// First call replies 503 — WaitForReady must retry.
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("E2E_HEALTH_URL", srv.URL)

	clients.WaitForReady(t, testtime.EventuallyLong)
	assert.GreaterOrEqual(t, calls, 2, "WaitForReady must poll past a transient non-200 response")
}
