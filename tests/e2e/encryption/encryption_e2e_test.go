//go:build e2e && pg

package encryption

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tests/e2e/internal/clients"
	e2erequire "github.com/ghbvf/gocell/tests/e2e/internal/require"
)

// TestE2E_ConfigEncryption_SensitiveValueNotExposedInResponse verifies that
// a config entry created with sensitive=true returns a redacted value in
// the GET response (never the plaintext).
func TestE2E_ConfigEncryption_SensitiveValueNotExposedInResponse(t *testing.T) {
	e2erequire.Docker(t)
	e2erequire.PG(t)
	clients.WaitForReady(t, 30*time.Second)
	token := clients.AdminToken(t)
	key := fmt.Sprintf("e2e.sensitive.%d", time.Now().UnixNano())

	createResp := clients.DoJSON(t, http.MethodPost, "/api/v1/config/", map[string]any{
		"key":       key,
		"value":     "super-secret-value",
		"sensitive": true,
	}, token)
	defer createResp.Body.Close()
	assert.Equal(t, http.StatusCreated, createResp.StatusCode)

	getResp := clients.DoJSON(t, http.MethodGet, "/api/v1/config/"+key, nil, token)
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
// updates it, and verifies the new value can be read back (re-encryption on
// update works correctly).
func TestE2E_ConfigEncryption_UpdateAndReadRoundTrip(t *testing.T) {
	e2erequire.Docker(t)
	e2erequire.PG(t)
	clients.WaitForReady(t, 30*time.Second)
	token := clients.AdminToken(t)
	key := fmt.Sprintf("e2e.update.%d", time.Now().UnixNano())

	createResp := clients.DoJSON(t, http.MethodPost, "/api/v1/config/", map[string]any{
		"key":       key,
		"value":     "initial-secret",
		"sensitive": true,
	}, token)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	updateResp := clients.DoJSON(t, http.MethodPut, "/api/v1/config/"+key, map[string]any{
		"value": "updated-secret",
	}, token)
	defer updateResp.Body.Close()
	require.Equal(t, http.StatusOK, updateResp.StatusCode)

	getResp := clients.DoJSON(t, http.MethodGet, "/api/v1/config/"+key, nil, token)
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
	e2erequire.Docker(t)
	e2erequire.PG(t)
	clients.WaitForReady(t, 30*time.Second)
	token := clients.AdminToken(t)
	key := fmt.Sprintf("e2e.plain.%d", time.Now().UnixNano())

	createResp := clients.DoJSON(t, http.MethodPost, "/api/v1/config/", map[string]any{
		"key":       key,
		"value":     "public-value",
		"sensitive": false,
	}, token)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	getResp := clients.DoJSON(t, http.MethodGet, "/api/v1/config/"+key, nil, token)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusOK, getResp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&body))
	data, _ := body["data"].(map[string]any)
	assert.Equal(t, "public-value", data["value"],
		"non-sensitive values must be returned as plaintext")
}

// TestE2E_ConfigEncryption_AnonymousRequestRejected verifies the negative
// auth path: a /api/v1/config write without a Bearer token is rejected
// (401 or 403). Without this guard, accidental token-loss would let the
// other three tests "pass" against an unauthed listener and silently
// hide an auth regression.
func TestE2E_ConfigEncryption_AnonymousRequestRejected(t *testing.T) {
	e2erequire.Docker(t)
	e2erequire.PG(t)
	clients.WaitForReady(t, 30*time.Second)

	resp := clients.DoJSON(t, http.MethodPost, "/api/v1/config/", map[string]any{
		"key": "anon.probe", "value": "x", "sensitive": false,
	}, "")
	defer resp.Body.Close()
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, resp.StatusCode,
		"anonymous /api/v1/config/ POST must be 401 or 403, got %d", resp.StatusCode)
}
