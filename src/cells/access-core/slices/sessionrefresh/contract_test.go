package sessionrefresh

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: http.auth.refresh.v1 — POST refresh returns {data: {accessToken, refreshToken, expiresAt}}.
func TestHttpAuthRefreshV1Serve(t *testing.T) {
	h, validToken := setup()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/refresh",
		strings.NewReader(`{"refreshToken":"`+validToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleRefresh(w, req)

	require.Equal(t, http.StatusOK, w.Code)

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

// Contract: http.auth.refresh.v1 — error path returns {error: {code, message}}.
func TestHttpAuthRefreshV1Serve_ErrorEnvelope(t *testing.T) {
	h, _ := setup()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/refresh",
		strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleRefresh(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Error.Code, "contract requires error.code")
	assert.NotEmpty(t, resp.Error.Message, "contract requires error.message")
}
