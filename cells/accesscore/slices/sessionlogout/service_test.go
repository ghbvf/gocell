package sessionlogout

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

func newLogoutRefreshStore() refresh.Store {
	clock := storetest.NewFakeClock(time.Now())
	return refreshmem.MustNew(refresh.Policy{ReuseInterval: testtime.D2s, MaxAge: time.Hour}, clock, nil)
}

type typedNilRefreshStore struct {
	refresh.Store
}

func newTestService(t testing.TB) (*Service, ports.SessionRepository) {
	t.Helper()
	repo := testutil.RealSessionRepo(t)
	return MustNewService(repo, newLogoutRefreshStore(), slog.Default(), WithTxManager(noopTxRunner{})), repo
}

func TestNewService_RejectsTypedNilDependencies(t *testing.T) {
	sessionRepo := testutil.RealSessionRepo(t)
	refreshStore := newLogoutRefreshStore()

	cases := []struct {
		name string
		run  func() (*Service, error)
	}{
		{
			name: "typed nil sessionRepo",
			run: func() (*Service, error) {
				var typedNil *mem.SessionRepository
				return NewService(typedNil, refreshStore, slog.Default())
			},
		},
		{
			name: "typed nil refreshStore",
			run: func() (*Service, error) {
				var typedNil *typedNilRefreshStore
				return NewService(sessionRepo, typedNil, slog.Default())
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.run()
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
		})
	}
}

func seedSession(repo ports.SessionRepository, id, userID string) {
	sess, _ := domain.NewSession(userID, "at-"+id, time.Now().Add(time.Hour), time.Now())
	sess.ID = id
	_ = repo.Create(context.Background(), sess)
}

func TestService_Logout(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(ports.SessionRepository)
		sessionID    string
		callerUserID string
		wantErr      bool
		wantCode     errcode.Code
	}{
		{
			name:         "valid self logout",
			setup:        func(r ports.SessionRepository) { seedSession(r, "sess-1", "usr-1") },
			sessionID:    "sess-1",
			callerUserID: "usr-1",
			wantErr:      false,
		},
		{
			name:         "empty session ID",
			setup:        func(_ ports.SessionRepository) {},
			sessionID:    "",
			callerUserID: "usr-1",
			wantErr:      true,
			wantCode:     errcode.ErrAuthLogoutInvalidInput,
		},
		{
			name:         "empty caller user ID",
			setup:        func(r ports.SessionRepository) { seedSession(r, "sess-1", "usr-1") },
			sessionID:    "sess-1",
			callerUserID: "",
			wantErr:      true,
			wantCode:     errcode.ErrAuthLogoutInvalidInput,
		},
		{
			name:         "non-existent session",
			setup:        func(_ ports.SessionRepository) {},
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
			setup:        func(r ports.SessionRepository) { seedSession(r, "sess-other", "usr-victim") },
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
			setup: func(r ports.SessionRepository) {
				seedSession(r, "sess-rev", "usr-1")
				s, _ := r.GetByID(context.Background(), "sess-rev")
				s.Revoke(time.Now())
			},
			sessionID:    "sess-rev",
			callerUserID: "usr-1",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService(t)
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
	repo := testutil.RealSessionRepo(t)
	seedSession(repo, "sess-pub", "usr-1")

	fp := failingPublisher{err: fmt.Errorf("broker unavailable")}
	emitter, err := outbox.NewDirectEmitter(
		fp, outbox.DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "accesscore",
		outbox.WithLogger(slog.Default()))
	require.NoError(t, err)
	svc := MustNewService(repo, newLogoutRefreshStore(), slog.Default(), WithEmitter(emitter), WithTxManager(noopTxRunner{}))

	err = svc.Logout(context.Background(), "sess-pub", "usr-1")
	require.NoError(t, err, "publish failure in demo mode should not fail logout")
}

func TestService_LogoutUser(t *testing.T) {
	svc, repo := newTestService(t)
	seedSession(repo, "s1", "usr-1")
	seedSession(repo, "s2", "usr-1")

	require.NoError(t, svc.LogoutUser(context.Background(), "usr-1"))
}
