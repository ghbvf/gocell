package sessionlogout

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// mustNewService is a test-only construction helper that panics on error.
func mustNewService(sessionStore session.Store, refreshStore refresh.Store, logger *slog.Logger, opts ...Option) *Service {
	s, err := NewService(sessionStore, refreshStore, logger, opts...)
	if err != nil {
		panic("mustNewService: " + err.Error())
	}
	return s
}

func newLogoutRefreshStore() refresh.Store {
	clk := storetest.NewFakeClock(time.Now())
	store, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clk, nil)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return store
}

type typedNilRefreshStore struct {
	refresh.Store
}

func newTestService(t testing.TB) (*Service, session.Store) {
	t.Helper()
	store := testutil.RealSessionRepo(t)
	return mustNewService(store, newLogoutRefreshStore(), slog.Default(), WithTxManager(persistence.WrapForCell(noopTxRunner{}))), store
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	store := testutil.RealSessionRepo(t)
	refreshStore := newLogoutRefreshStore()
	_, err := NewService(store, refreshStore, slog.Default() /* no WithTxManager */)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}

func TestNewService_RejectsTypedNilDependencies(t *testing.T) {
	store := testutil.RealSessionRepo(t)
	refreshStore := newLogoutRefreshStore()

	cases := []struct {
		name string
		run  func() (*Service, error)
	}{
		{
			name: "typed nil sessionStore",
			run: func() (*Service, error) {
				var typedNil *session.MemStore
				return NewService(typedNil, refreshStore, slog.Default())
			},
		},
		{
			name: "typed nil refreshStore",
			run: func() (*Service, error) {
				var typedNil *typedNilRefreshStore
				return NewService(store, typedNil, slog.Default())
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

// seedSession creates a session in the store for the given sessionID and userID.
// JTI is set to a non-empty value so FingerprintJTIRef validation passes.
func seedSession(store session.Store, id, userID string) {
	_ = store.Create(context.Background(), &session.Session{
		ID:                id,
		SubjectID:         userID,
		JTI:               "jti-" + id,
		AuthzEpochAtIssue: 1,
		CreatedAt:         time.Now(),
		ExpiresAt:         time.Now().Add(time.Hour),
	})
}

func TestService_Logout(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(session.Store)
		sessionID    string
		callerUserID string
		wantErr      bool
		wantCode     errcode.Code
	}{
		{
			name:         "valid self logout",
			setup:        func(s session.Store) { seedSession(s, "sess-1", "usr-1") },
			sessionID:    "sess-1",
			callerUserID: "usr-1",
			wantErr:      false,
		},
		{
			name:         "empty session ID",
			setup:        func(_ session.Store) {},
			sessionID:    "",
			callerUserID: "usr-1",
			wantErr:      true,
			wantCode:     errcode.ErrAuthLogoutInvalidInput,
		},
		{
			name:         "empty caller user ID",
			setup:        func(s session.Store) { seedSession(s, "sess-1", "usr-1") },
			sessionID:    "sess-1",
			callerUserID: "",
			wantErr:      true,
			wantCode:     errcode.ErrAuthLogoutInvalidInput,
		},
		{
			name:         "non-existent session",
			setup:        func(_ session.Store) {},
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
			setup:        func(s session.Store) { seedSession(s, "sess-other", "usr-victim") },
			sessionID:    "sess-other",
			callerUserID: "usr-attacker",
			wantErr:      true,
			wantCode:     errcode.ErrSessionNotFound,
		},
		{
			// owner-mismatch and missing-session produce the same error code —
			// callers cannot distinguish between the two (防枚举).
			name: "owner mismatch and not-found are indistinguishable",
			setup: func(s session.Store) {
				// sess-owned exists but belongs to usr-owner, not usr-other.
				seedSession(s, "sess-owned", "usr-owner")
			},
			sessionID:    "sess-owned",
			callerUserID: "usr-other",
			wantErr:      true,
			wantCode:     errcode.ErrSessionNotFound,
		},
		{
			// Double-revoke is idempotent at store level (Revoke is a no-op after
			// the first revoke); event is emitted again but consumers must
			// already dedupe on event_id.
			name: "already revoked self logout succeeds",
			setup: func(s session.Store) {
				seedSession(s, "sess-rev", "usr-1")
				// Pre-revoke so the session is already revoked when Logout runs.
				_ = s.Revoke(context.Background(), "sess-rev")
			},
			sessionID:    "sess-rev",
			callerUserID: "usr-1",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store := newTestService(t)
			tt.setup(store)

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

// infraFailingSessionStore wraps session.Store and overrides Get to return a
// caller-supplied infra error so the logout fail-fast path can be exercised.
type infraFailingSessionStore struct {
	session.Store
	err error
}

func (f *infraFailingSessionStore) Get(context.Context, string) (*session.ValidateView, error) {
	return nil, f.err
}

// TestService_Logout_InfraErrorOnGet_ReturnsUnavailable verifies that a
// PG / connection failure on sessionStore.Get surfaces as 503 / ErrAuthLogout
// Unavailable rather than being silently squashed into ErrSessionNotFound.
// Squashing all errors hides outages and lets clients incorrectly assume the
// session was already gone — leaving refresh chains uncascaded.
func TestService_Logout_InfraErrorOnGet_ReturnsUnavailable(t *testing.T) {
	inner := testutil.RealSessionRepo(t)
	store := &infraFailingSessionStore{
		Store: inner,
		err:   errcode.New(errcode.KindInternal, errcode.ErrInternal, "session store down"),
	}
	svc := mustNewService(store, newLogoutRefreshStore(), slog.Default(), WithTxManager(persistence.WrapForCell(noopTxRunner{})))

	err := svc.Logout(context.Background(), "sess-infra", "usr-1")
	require.Error(t, err, "logout must surface infra failures, not squash to not-found")

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "error must be an *errcode.Error")
	assert.Equal(t, errcode.ErrAuthLogoutUnavailable, ec.Code,
		"infra error must surface as ErrAuthLogoutUnavailable, not ErrSessionNotFound")
	assert.Equal(t, errcode.KindUnavailable, ec.Kind,
		"KindUnavailable so HTTP layer returns 503 + client retries")
}

func TestService_Logout_PublishError_DoesNotFailLogout(t *testing.T) {
	store := testutil.RealSessionRepo(t)
	seedSession(store, "sess-pub", "usr-1")

	fp := failingPublisher{err: fmt.Errorf("broker unavailable")}
	emitter, err := outbox.NewDirectEmitter(
		fp, outbox.DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "accesscore",
		outbox.WithLogger(slog.Default()))
	require.NoError(t, err)
	svc := mustNewService(store, newLogoutRefreshStore(), slog.Default(),
		WithEmitter(emitter), WithTxManager(persistence.WrapForCell(noopTxRunner{})))

	err = svc.Logout(context.Background(), "sess-pub", "usr-1")
	require.NoError(t, err, "publish failure in demo mode should not fail logout")
}
