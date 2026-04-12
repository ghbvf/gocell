package sessionlogin

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

// Contract: http.auth.login.v1 — error path returns {error: {code, message}}.
func TestHttpAuthLoginV1Serve_ErrorEnvelope(t *testing.T) {
	h := setup()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleLogin(w, req)

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

// Contract: event.session.created.v1 — login publishes {session_id, user_id}.
func TestEventSessionCreatedV1Publish(t *testing.T) {
	pub := &capturePub{}
	userRepo := mem.NewUserRepository()
	seedUser(userRepo, "alice", "correct-pass") // reuse service_test helper

	svc := NewService(userRepo, mem.NewSessionRepository(), mem.NewRoleRepository(), pub, testIssuer, slog.Default())
	h := NewHandler(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader(`{"username":"alice","password":"correct-pass"}`))
	req.Header.Set("Content-Type", "application/json")
	h.HandleLogin(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	require.Len(t, pub.entries, 1, "expected 1 published event")
	assert.Equal(t, TopicSessionCreated, pub.entries[0].Topic)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(pub.entries[0].Payload, &payload))
	assert.NotEmpty(t, payload["session_id"], "payload requires session_id")
	assert.Equal(t, "usr-alice", payload["user_id"], "payload requires user_id")
}
