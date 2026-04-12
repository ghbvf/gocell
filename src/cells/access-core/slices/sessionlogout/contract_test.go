package sessionlogout

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
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

// Contract: event.session.revoked.v1 — logout publishes {session_id, user_id}.
func TestEventSessionRevokedV1Publish(t *testing.T) {
	pub := &capturePub{}
	sessionRepo := mem.NewSessionRepository()
	sess, _ := domain.NewSession("usr-1", "access-tok", "refresh-tok", time.Now().Add(time.Hour))
	sess.ID = "sess-cap-1"
	_ = sessionRepo.Create(context.Background(), sess)

	svc := NewService(sessionRepo, pub, slog.Default())
	h := NewHandler(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/sess-cap-1", nil)
	req.SetPathValue("id", "sess-cap-1")
	h.HandleLogout(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.Len(t, pub.entries, 1, "expected 1 published event")
	assert.Equal(t, TopicSessionRevoked, pub.entries[0].Topic)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(pub.entries[0].Payload, &payload))
	assert.Equal(t, "sess-cap-1", payload["session_id"], "payload requires session_id")
	assert.Equal(t, "usr-1", payload["user_id"], "payload requires user_id")
}
