package sessionlogout

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (*Service, *mem.SessionRepository) {
	repo := mem.NewSessionRepository()
	eb := eventbus.New()
	return NewService(repo, eb, slog.Default()), repo
}

func seedSession(repo *mem.SessionRepository, id, userID string) {
	sess, _ := domain.NewSession(userID, "at-"+id, "rt-"+id, time.Now().Add(time.Hour))
	sess.ID = id
	_ = repo.Create(context.Background(), sess)
}

func TestService_Logout(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(*mem.SessionRepository)
		sessionID    string
		callerUserID string
		wantErr      bool
		wantCode     errcode.Code
	}{
		{
			name:         "valid self logout",
			setup:        func(r *mem.SessionRepository) { seedSession(r, "sess-1", "usr-1") },
			sessionID:    "sess-1",
			callerUserID: "usr-1",
			wantErr:      false,
		},
		{
			name:         "empty session ID",
			setup:        func(_ *mem.SessionRepository) {},
			sessionID:    "",
			callerUserID: "usr-1",
			wantErr:      true,
			wantCode:     errcode.ErrAuthLogoutInvalidInput,
		},
		{
			name:         "empty caller user ID",
			setup:        func(r *mem.SessionRepository) { seedSession(r, "sess-1", "usr-1") },
			sessionID:    "sess-1",
			callerUserID: "",
			wantErr:      true,
			wantCode:     errcode.ErrAuthLogoutInvalidInput,
		},
		{
			name:         "non-existent session",
			setup:        func(_ *mem.SessionRepository) {},
			sessionID:    "sess-missing",
			callerUserID: "usr-1",
			wantErr:      true,
			wantCode:     errcode.ErrSessionNotFound,
		},
		{
			// IDOR guard: caller attempts to revoke a session belonging to
			// another user. Must yield ErrSessionNotFound (same code as
			// missing-session), so no information leaks about whether the
			// session id belongs to someone else.
			name:         "other user's session yields not-found",
			setup:        func(r *mem.SessionRepository) { seedSession(r, "sess-other", "usr-victim") },
			sessionID:    "sess-other",
			callerUserID: "usr-attacker",
			wantErr:      true,
			wantCode:     errcode.ErrSessionNotFound,
		},
		{
			// Double-revoke is idempotent at DB level (UPDATE is no-op after
			// the first revoke); event is emitted again but consumers must
			// already dedupe on event_id.
			name: "already revoked self logout succeeds",
			setup: func(r *mem.SessionRepository) {
				seedSession(r, "sess-rev", "usr-1")
				s, _ := r.GetByID(context.Background(), "sess-rev")
				s.Revoke()
			},
			sessionID:    "sess-rev",
			callerUserID: "usr-1",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			tt.setup(repo)

			err := svc.Logout(context.Background(), tt.sessionID, tt.callerUserID)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantCode != "" {
					var coded *errcode.Error
					require.ErrorAs(t, err, &coded, "expected errcode.Error wrapping %s", tt.wantCode)
					assert.Equal(t, tt.wantCode, coded.Code)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type failingPublisher struct{ err error }

func (f failingPublisher) Publish(_ context.Context, _ string, _ []byte) error { return f.err }
func (f failingPublisher) Close(_ context.Context) error                       { return nil }

func TestService_Logout_PublishError_DoesNotFailLogout(t *testing.T) {
	repo := mem.NewSessionRepository()
	seedSession(repo, "sess-pub", "usr-1")

	fp := failingPublisher{err: fmt.Errorf("broker unavailable")}
	svc := NewService(repo, fp, slog.Default())

	err := svc.Logout(context.Background(), "sess-pub", "usr-1")
	require.NoError(t, err, "publish failure in demo mode should not fail logout")
}

func TestService_LogoutUser(t *testing.T) {
	svc, repo := newTestService()
	seedSession(repo, "s1", "usr-1")
	seedSession(repo, "s2", "usr-1")

	require.NoError(t, svc.LogoutUser(context.Background(), "usr-1"))
}
