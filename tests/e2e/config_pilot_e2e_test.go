//go:build e2e

// Package e2e contains end-to-end tests that exercise the full config-core
// PG pilot including value encryption. These tests require a live 3-container
// environment (PostgreSQL + Vault + core-bundle HTTP server) started via:
//
//	docker compose -f tests/e2e/docker-compose.e2e.yaml up -d
//
// Run with:
//
//	go test -tags=e2e -timeout=120s ./tests/e2e/...
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eBaseURL returns the base URL of the running core-bundle, defaulting to
// localhost:8080. Override via E2E_BASE_URL environment variable.
func e2eBaseURL() string {
	if u := os.Getenv("E2E_BASE_URL"); u != "" {
		return u
	}
	return "http://localhost:8080"
}

// e2eAdminToken returns the admin JWT for bootstrapped e2e environment.
// Override via E2E_ADMIN_TOKEN environment variable.
func e2eAdminToken() string {
	return os.Getenv("E2E_ADMIN_TOKEN")
}

// waitForReady polls /healthz until the server is up or the timeout elapses.
func waitForReady(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(e2eBaseURL() + "/healthz") //nolint:noctx
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready within %s", e2eBaseURL(), timeout)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func doJSON(t *testing.T, method, path string, body any, token string) *http.Response {
	t.Helper()
	var reqBody bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&reqBody).Encode(body))
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e2eBaseURL()+path, &reqBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestE2E_ConfigEncryption_SensitiveValueNotExposedInResponse verifies that
// a config entry created with sensitive=true returns a redacted value in
// the GET response (never the plaintext).
//
// Container requirements:
//   - PostgreSQL (GOCELL_DATABASE_URL set in compose env)
//   - core-bundle with GOCELL_KEY_PROVIDER=local-aes + GOCELL_MASTER_KEY set
//   - GOCELL_CELL_ADAPTER_MODE=postgres
func TestE2E_ConfigEncryption_SensitiveValueNotExposedInResponse(t *testing.T) {
	t.Skip("e2e: requires docker compose environment — run: docker compose -f tests/e2e/docker-compose.e2e.yaml up -d")
	waitForReady(t, 30*time.Second)
	token := e2eAdminToken()
	key := fmt.Sprintf("e2e.sensitive.%d", time.Now().UnixNano())

	// Create a sensitive config entry.
	createResp := doJSON(t, http.MethodPost, "/api/v1/config/", map[string]any{
		"key":       key,
		"value":     "super-secret-value",
		"sensitive": true,
	}, token)
	defer createResp.Body.Close()
	assert.Equal(t, http.StatusCreated, createResp.StatusCode)

	// Read it back — value must be redacted (not the original plaintext).
	getResp := doJSON(t, http.MethodGet, "/api/v1/config/"+key, nil, token)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusOK, getResp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&body))
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "response must have data object")
	assert.NotEqual(t, "super-secret-value", data["value"],
		"plaintext must not be returned for sensitive entries")
}

// TestE2E_ConfigEncryption_UpdateAndReadRoundTrip creates a sensitive entry,
// updates it, and verifies the new value can be read back (demonstrating that
// re-encryption on update works correctly).
func TestE2E_ConfigEncryption_UpdateAndReadRoundTrip(t *testing.T) {
	t.Skip("e2e: requires docker compose environment — run: docker compose -f tests/e2e/docker-compose.e2e.yaml up -d")
	waitForReady(t, 30*time.Second)
	token := e2eAdminToken()
	key := fmt.Sprintf("e2e.update.%d", time.Now().UnixNano())

	createResp := doJSON(t, http.MethodPost, "/api/v1/config/", map[string]any{
		"key":       key,
		"value":     "initial-secret",
		"sensitive": true,
	}, token)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	updateResp := doJSON(t, http.MethodPut, "/api/v1/config/"+key, map[string]any{
		"value": "updated-secret",
	}, token)
	defer updateResp.Body.Close()
	require.Equal(t, http.StatusOK, updateResp.StatusCode)

	// Read back — the entry must exist and not expose plaintext.
	getResp := doJSON(t, http.MethodGet, "/api/v1/config/"+key, nil, token)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusOK, getResp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&body))
	data, _ := body["data"].(map[string]any)
	assert.NotEqual(t, "updated-secret", data["value"],
		"updated plaintext must not be returned for sensitive entries")
}

// TestE2E_ConfigEncryption_NonSensitiveValueVisibleInResponse verifies that
// non-sensitive entries are stored and returned as plaintext.
func TestE2E_ConfigEncryption_NonSensitiveValueVisibleInResponse(t *testing.T) {
	t.Skip("e2e: requires docker compose environment — run: docker compose -f tests/e2e/docker-compose.e2e.yaml up -d")
	waitForReady(t, 30*time.Second)
	token := e2eAdminToken()
	key := fmt.Sprintf("e2e.plain.%d", time.Now().UnixNano())

	createResp := doJSON(t, http.MethodPost, "/api/v1/config/", map[string]any{
		"key":       key,
		"value":     "public-value",
		"sensitive": false,
	}, token)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	getResp := doJSON(t, http.MethodGet, "/api/v1/config/"+key, nil, token)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusOK, getResp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&body))
	data, _ := body["data"].(map[string]any)
	assert.Equal(t, "public-value", data["value"],
		"non-sensitive values must be returned as plaintext")
}
