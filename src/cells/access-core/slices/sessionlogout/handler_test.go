package sessionlogout

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

func setup() (*Handler, func()) {
	sessionRepo := mem.NewSessionRepository()
	sess, _ := domain.NewSession("usr-1", "access-tok", "refresh-tok", time.Now().Add(time.Hour))
	sess.ID = "sess-1"
	_ = sessionRepo.Create(context.Background(), sess)

	svc := NewService(sessionRepo, eventbus.New(), slog.Default())
	return NewHandler(svc), func() {}
}

func TestHandleLogout(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{
			name:       "existing session returns 204",
			path:       "/sess-1",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "nonexistent session returns 404",
			path:       "/no-such-sess",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := setup()
			w := httptest.NewRecorder()
			sessionID := strings.TrimPrefix(tc.path, "/")
			req := httptest.NewRequest(http.MethodDelete, tc.path, nil)
			req.SetPathValue("id", sessionID)
			h.HandleLogout(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}
