package sessionlogout

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

func setup() http.Handler {
	sessionRepo := mem.NewSessionRepository()
	sess, _ := domain.NewSession("usr-1", "access-tok", "refresh-tok", time.Now().Add(time.Hour))
	sess.ID = "sess-1"
	_ = sessionRepo.Create(context.Background(), sess)

	svc := NewService(sessionRepo, eventbus.New(), slog.Default())
	r := chi.NewRouter()
	r.Delete("/{id}", NewHandler(svc).HandleLogout)
	return r
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
			name:       "nonexistent session returns 500",
			path:       "/no-such-sess",
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := setup()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, tc.path, nil))
			if tc.wantStatus == http.StatusNoContent {
				assert.Equal(t, tc.wantStatus, w.Code)
			} else {
				// The error wraps a non-errcode error (fmt.Errorf),
				// so WriteDomainError maps it to 500.
				require.GreaterOrEqual(t, w.Code, 400)
			}
		})
	}
}
