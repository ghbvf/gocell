package identitymanage

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturePub struct {
	mu      sync.Mutex
	entries []capturedEvent
}
type capturedEvent struct {
	Topic   string
	Payload json.RawMessage
}

func (p *capturePub) Publish(_ context.Context, topic string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries = append(p.entries, capturedEvent{Topic: topic, Payload: payload})
	return nil
}

func setupWithCapture() (http.Handler, *capturePub) {
	pub := &capturePub{}
	svc := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), pub, slog.Default())
	mux := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(mux)
	return mux, pub
}

// Contract: event.user.created.v1 — user creation publishes {user_id, username}.
func TestEventUserCreatedV1Publish(t *testing.T) {
	h, pub := setupWithCapture()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"username":"contract-user","email":"c@d.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	require.Len(t, pub.entries, 1, "expected 1 published event")
	assert.Equal(t, TopicUserCreated, pub.entries[0].Topic)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(pub.entries[0].Payload, &payload))
	assert.NotEmpty(t, payload["user_id"], "payload requires user_id")
	assert.Equal(t, "contract-user", payload["username"], "payload requires username")
}

// Contract: event.user.locked.v1 — user lock publishes {user_id}.
func TestEventUserLockedV1Publish(t *testing.T) {
	h, pub := setupWithCapture()

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

	// Clear create event, then lock.
	pub.mu.Lock()
	pub.entries = nil
	pub.mu.Unlock()

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/"+created.Data.ID+"/lock", nil)
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	require.Len(t, pub.entries, 1, "expected 1 published event")
	assert.Equal(t, TopicUserLocked, pub.entries[0].Topic)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(pub.entries[0].Payload, &payload))
	assert.NotEmpty(t, payload["user_id"], "payload requires user_id")
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

// Contract: http.auth.me.v1 — error path returns {error: {code, message}}.
func TestHttpAuthMeV1Serve_ErrorEnvelope(t *testing.T) {
	h := setup()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)

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
