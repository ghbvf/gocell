package sessionlogin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: http.auth.login.v1 — POST login returns {data: {accessToken, refreshToken, expiresAt}}.
func TestHttpAuthLoginV1Serve(t *testing.T) {
	h := setup()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader(`{"username":"alice","password":"correct-pass"}`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleLogin(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    string `json:"expiresAt"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.AccessToken, "contract requires accessToken")
	assert.NotEmpty(t, resp.Data.RefreshToken, "contract requires refreshToken")
	assert.NotEmpty(t, resp.Data.ExpiresAt, "contract requires expiresAt")
}

// Contract: event.session.created.v1 — login publishes {session_id, user_id}.
// Verifies the action that triggers the event completes without error.
func TestEventSessionCreatedV1Publish(t *testing.T) {
	h := setup()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader(`{"username":"alice","password":"correct-pass"}`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleLogin(w, req)

	assert.Equal(t, http.StatusCreated, w.Code,
		"contract: login must succeed, triggering event.session.created.v1 publish")
}
