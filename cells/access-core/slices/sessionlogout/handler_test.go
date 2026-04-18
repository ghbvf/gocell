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
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

func setup() *Handler {
	sessionRepo := mem.NewSessionRepository()
	sess, _ := domain.NewSession("usr-1", "access-tok", "refresh-tok", time.Now().Add(time.Hour))
	sess.ID = "sess-1"
	_ = sessionRepo.Create(context.Background(), sess)
	// Victim session owned by a different user — used to prove IDOR guard.
	other, _ := domain.NewSession("usr-victim", "at-v", "rt-v", time.Now().Add(time.Hour))
	other.ID = "sess-victim"
	_ = sessionRepo.Create(context.Background(), other)

	svc := NewService(sessionRepo, eventbus.New(), slog.Default())
	return NewHandler(svc)
}

func TestHandleLogout(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		caller     string // subject injected into ctx; empty = no auth ctx
		wantStatus int
	}{
		{
			name:       "own session returns 204",
			path:       "/sess-1",
			caller:     "usr-1",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "nonexistent session returns 404",
			path:       "/no-such-sess",
			caller:     "usr-1",
			wantStatus: http.StatusNotFound,
		},
		{
			// IDOR: another user's existing session id must look identical
			// to a non-existent id — 404, not 403 — so attackers cannot
			// enumerate session ownership.
			name:       "other user's session returns 404 not 403",
			path:       "/sess-victim",
			caller:     "usr-attacker",
			wantStatus: http.StatusNotFound,
		},
		{
			// Defense-in-depth: if the route is accidentally declared public
			// (no auth middleware injected subject), the handler must fail
			// closed rather than allow anonymous revokes.
			name:       "missing subject in ctx returns 401",
			path:       "/sess-1",
			caller:     "",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := setup()
			w := httptest.NewRecorder()
			sessionID := strings.TrimPrefix(tc.path, "/")
			ctx := context.Background()
			if tc.caller != "" {
				ctx = ctxkeys.WithSubject(ctx, tc.caller)
			}
			req := httptest.NewRequest(http.MethodDelete, tc.path, nil).WithContext(ctx)
			req.SetPathValue("id", sessionID)
			h.HandleLogout(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}
