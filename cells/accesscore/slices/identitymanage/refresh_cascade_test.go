package identitymanage

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Refresh-cascade tests — verify that identitymanage lifecycle events
// (Lock, ChangePassword, Delete) revoke the user's refresh-token chain in
// addition to the access-session revocation. Without this, a stolen refresh
// token would survive a password rotation or account lock.

func newCascadeStore(t *testing.T) (refresh.Store, *storetest.FakeClock) {
	t.Helper()
	clock := storetest.NewFakeClock(time.Now())
	policy := refresh.Policy{ReuseInterval: 100 * time.Millisecond, MaxAge: time.Hour}
	return refreshmem.New(policy, clock, nil), clock
}

func TestService_Lock_RevokesRefreshChain(t *testing.T) {
	ctx := context.Background()
	userRepo := mem.NewUserRepository()
	sessionRepo := mem.NewSessionRepository()
	refreshStore, _ := newCascadeStore(t)

	svc, err := NewService(userRepo, sessionRepo, refreshStore, slog.Default(),
		WithTokenIssuer(minimalStubIssuer))
	require.NoError(t, err)

	user, err := svc.Create(ctx, CreateInput{Username: "dave", Email: "d@e.f", Password: "hash"})
	require.NoError(t, err)

	wire, _, err := refreshStore.Issue(ctx, "sess-dave", user.ID)
	require.NoError(t, err)

	require.NoError(t, svc.Lock(ctx, user.ID))

	// Rotating the pre-lock refresh token must be rejected.
	_, _, err = refreshStore.Rotate(ctx, wire)
	require.Error(t, err)
	assert.True(t, errors.Is(err, refresh.ErrRejected), "refresh token after Lock must be rejected")
}

func TestService_ChangePassword_RevokesRefreshChain(t *testing.T) {
	ctx := context.Background()
	sessionRepo := mem.NewSessionRepository()
	userRepo := mem.NewUserRepository()
	refreshStore, _ := newCascadeStore(t)

	svc, err := NewService(userRepo, sessionRepo, refreshStore, slog.Default(),
		WithTokenIssuer(minimalStubIssuer))
	require.NoError(t, err)

	// Use the service to create so it hashes the password for us.
	user, err := svc.Create(ctx, CreateInput{Username: "eve", Email: "e@f.g", Password: "old-P@ssw0rd!"})
	require.NoError(t, err)

	wire, _, err := refreshStore.Issue(ctx, "sess-eve", user.ID)
	require.NoError(t, err)

	_, err = svc.ChangePassword(ctx, ChangePasswordInput{
		UserID:      user.ID,
		OldPassword: "old-P@ssw0rd!",
		NewPassword: "new-P@ssw0rd!",
	})
	require.NoError(t, err)

	_, _, err = refreshStore.Rotate(ctx, wire)
	require.Error(t, err)
	assert.True(t, errors.Is(err, refresh.ErrRejected),
		"refresh token after ChangePassword must be rejected")
}

func TestService_Delete_RevokesRefreshChain(t *testing.T) {
	ctx := context.Background()
	userRepo := mem.NewUserRepository()
	sessionRepo := mem.NewSessionRepository()
	refreshStore, _ := newCascadeStore(t)

	svc, err := NewService(userRepo, sessionRepo, refreshStore, slog.Default(),
		WithTokenIssuer(minimalStubIssuer))
	require.NoError(t, err)

	user, err := svc.Create(ctx, CreateInput{Username: "frank", Email: "f@g.h", Password: "pwd"})
	require.NoError(t, err)

	wire, _, err := refreshStore.Issue(ctx, "sess-frank", user.ID)
	require.NoError(t, err)

	require.NoError(t, svc.Delete(ctx, user.ID))

	_, _, err = refreshStore.Rotate(ctx, wire)
	require.Error(t, err)
	assert.True(t, errors.Is(err, refresh.ErrRejected),
		"refresh token after Delete must be rejected")
}
