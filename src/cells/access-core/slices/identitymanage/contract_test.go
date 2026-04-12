package identitymanage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: event.user.created.v1 — user creation publishes {user_id, username}.
// Verifies the action that triggers the event completes successfully.
func TestEventUserCreatedV1Publish(t *testing.T) {
	h := setup()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"username":"contract-user","email":"c@d.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code,
		"contract: user creation must succeed, triggering event.user.created.v1 publish")
}

// Contract: event.user.locked.v1 — user lock publishes {user_id}.
// Verifies the lock action completes successfully.
func TestEventUserLockedV1Publish(t *testing.T) {
	h := setup()

	// Create user first.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"username":"lockme","email":"l@m.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	// Lock the user.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/"+created.Data.ID+"/lock", nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code,
		"contract: user lock must succeed, triggering event.user.locked.v1 publish")
}

// Contract: http.auth.me.v1 — identity CRUD returns {data: {id, username, email, status, createdAt, updatedAt}}.
func TestHttpAuthMeV1Serve(t *testing.T) {
	h := setup()

	// Create a user.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"username":"alice","email":"a@b.com","password":"secret123"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var created struct {
		Data struct {
			ID        string `json:"id"`
			Username  string `json:"username"`
			Email     string `json:"email"`
			Status    string `json:"status"`
			CreatedAt string `json:"createdAt"`
			UpdatedAt string `json:"updatedAt"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.NotEmpty(t, created.Data.ID, "contract requires id")
	assert.Equal(t, "alice", created.Data.Username, "contract requires username")
	assert.Equal(t, "a@b.com", created.Data.Email, "contract requires email")
	assert.NotEmpty(t, created.Data.Status, "contract requires status")
	assert.NotEmpty(t, created.Data.CreatedAt, "contract requires createdAt")
	assert.NotEmpty(t, created.Data.UpdatedAt, "contract requires updatedAt")

	// GET the created user — verify same response shape.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/"+created.Data.ID, nil)
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var got struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, created.Data.ID, got.Data.ID)
	assert.Equal(t, "alice", got.Data.Username)
}
