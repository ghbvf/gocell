package sessionlogout

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

// Refresh-cascade tests — verify that Logout and LogoutUser revoke the
// associated refresh-token chains, not just the access sessions. Without
// this cascade, a stolen refresh token survives logout.

func newCascadeStore(t *testing.T) refresh.Store {
	t.Helper()
	clock := storetest.NewFakeClock(time.Now())
	return refreshmem.MustNew(refresh.Policy{ReuseInterval: testtime.SlowPoll, MaxAge: time.Hour}, clock, nil)
}

func TestService_Logout_RevokesRefreshChain(t *testing.T) {
	ctx := context.Background()
	sessionRepo := mem.NewSessionRepository(clock.Real())
	refreshStore := newCascadeStore(t)

	sessionID := "sess-logout-1"
	userID := "user-logout-1"
	session := &domain.Session{
		ID:          sessionID,
		UserID:      userID,
		AccessToken: "at",
		ExpiresAt:   time.Now().Add(time.Hour),
		CreatedAt:   time.Now(),
	}
	require.NoError(t, sessionRepo.Create(ctx, session))

	wire, _, err := refreshStore.Issue(ctx, sessionID, userID)
	require.NoError(t, err)

	svc := MustNewService(sessionRepo, refreshStore, slog.Default())
	require.NoError(t, svc.Logout(ctx, sessionID, userID))

	_, _, err = refreshStore.Rotate(ctx, wire)
	require.Error(t, err)
	assert.True(t, errors.Is(err, refresh.ErrRejected),
		"refresh token after Logout must be rejected")
}

func TestService_LogoutUser_RevokesAllRefreshChains(t *testing.T) {
	ctx := context.Background()
	sessionRepo := mem.NewSessionRepository(clock.Real())
	refreshStore := newCascadeStore(t)

	userID := "user-multi-logout"
	// Two distinct sessions for the same user.
	for _, sid := range []string{"sess-a", "sess-b"} {
		require.NoError(t, sessionRepo.Create(ctx, &domain.Session{
			ID: sid, UserID: userID, AccessToken: "at-" + sid,
			ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
		}))
	}
	wire1, _, err := refreshStore.Issue(ctx, "sess-a", userID)
	require.NoError(t, err)
	wire2, _, err := refreshStore.Issue(ctx, "sess-b", userID)
	require.NoError(t, err)

	// Another user's chain must survive.
	otherWire, _, err := refreshStore.Issue(ctx, "sess-other", "other-user")
	require.NoError(t, err)

	svc := MustNewService(sessionRepo, refreshStore, slog.Default())
	require.NoError(t, svc.LogoutUser(ctx, userID))

	_, _, err = refreshStore.Rotate(ctx, wire1)
	assert.True(t, errors.Is(err, refresh.ErrRejected), "session-a refresh must be rejected")
	_, _, err = refreshStore.Rotate(ctx, wire2)
	assert.True(t, errors.Is(err, refresh.ErrRejected), "session-b refresh must be rejected")

	// Other user's refresh chain must still rotate successfully.
	_, _, err = refreshStore.Rotate(ctx, otherWire)
	assert.NoError(t, err, "other user's refresh chain must survive LogoutUser")
}
