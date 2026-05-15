package sessionlogout

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Refresh-cascade tests — verify that Logout revokes the associated
// refresh-token chains, not just the access sessions. Without this cascade,
// a stolen refresh token survives logout.

func newCascadeStore(t *testing.T) refresh.Store {
	t.Helper()
	clock := storetest.NewFakeClock(time.Now())
	store, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.SlowPoll,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clock, nil)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return store
}

func TestService_Logout_RevokesRefreshChain(t *testing.T) {
	ctx := context.Background()
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newCascadeStore(t)

	sessionID := "sess-logout-1"
	userID := "user-logout-1"
	require.NoError(t, sessionStore.Create(ctx, &session.Session{
		ID:                sessionID,
		SubjectID:         userID,
		JTI:               "jti-" + sessionID,
		AuthzEpochAtIssue: 1,
		CreatedAt:         time.Now(),
		ExpiresAt:         time.Now().Add(time.Hour),
	}))

	wire, _, err := refreshStore.Issue(ctx, sessionID, userID, int64(1))
	require.NoError(t, err)

	svc := MustNewService(sessionStore, refreshStore, slog.Default(), WithTxManager(persistence.WrapForCell(noopTxRunner{})))
	require.NoError(t, svc.Logout(ctx, sessionID, userID))

	_, _, err = refreshStore.Rotate(ctx, wire)
	require.Error(t, err)
	assert.True(t, errors.Is(err, refresh.ErrRejected),
		"refresh token after Logout must be rejected")
}
