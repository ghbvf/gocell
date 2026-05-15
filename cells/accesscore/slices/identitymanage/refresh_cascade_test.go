package identitymanage

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

// Refresh-cascade tests — verify that identitymanage lifecycle events
// (Lock, ChangePassword, Delete) revoke the user's refresh-token chain in
// addition to the access-session revocation. Without this, a stolen refresh
// token would survive a password rotation or account lock.

func newCascadeStore(t *testing.T) refresh.Store {
	t.Helper()
	clk := storetest.NewFakeClock(time.Now())
	policy := refresh.Policy{
		ReuseInterval:  testtime.SlowPoll,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	store, err := refreshmem.New(policy, clk, nil)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return store
}

func TestService_Lock_RevokesRefreshChain(t *testing.T) {
	ctx := auth.TestContext("test-admin", []string{"admin"})
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	refreshStore := newCascadeStore(t)

	svc, err := NewService(userRepo, newInvalidator(t, userRepo, sessionRepo, refreshStore), slog.Default(),
		WithTokenIssuer(minimalStubIssuer), WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(contractTxRunner{})))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{Username: "dave", Email: "d@e.f", Password: "hash"})
	require.NoError(t, err)

	wire, _, err := refreshStore.Issue(ctx, "sess-dave", user.ID, int64(1))
	require.NoError(t, err)

	// F13: issue a second user's wire token first — it must survive the Lock.
	otherWire, _, err := refreshStore.Issue(ctx, "sess-other-lock", "other-user-lock", int64(1))
	require.NoError(t, err)

	require.NoError(t, svc.Lock(auth.TestContext("test-admin", []string{"admin"}), user.ID))

	// Rotating the pre-lock refresh token must be rejected.
	_, _, err = refreshStore.Rotate(ctx, wire)
	require.Error(t, err)
	assert.True(t, errors.Is(err, refresh.ErrRejected), "refresh token after Lock must be rejected")

	// F13: the other user's chain must survive the Lock.
	_, _, err = refreshStore.Rotate(ctx, otherWire)
	assert.NoError(t, err, "other user's refresh chain must survive Lock(user.ID)")
}

func TestService_ChangePassword_RevokesRefreshChain(t *testing.T) {
	ctx := context.Background()
	sessionRepo := testutil.RealSessionRepo(t)
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	refreshStore := newCascadeStore(t)

	svc, err := NewService(userRepo, newInvalidator(t, userRepo, sessionRepo, refreshStore), slog.Default(),
		WithTokenIssuer(minimalStubIssuer), WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(contractTxRunner{})))
	require.NoError(t, err)

	// Use the service to create so it hashes the password for us.
	user, err := svc.Create(adminCtxForService(), CreateInput{Username: "eve", Email: "e@f.g", Password: "old-P@ssw0rd!"})
	require.NoError(t, err)

	wire, _, err := refreshStore.Issue(ctx, "sess-eve", user.ID, int64(1))
	require.NoError(t, err)

	// F13: issue a second user's wire token first — it must survive the ChangePassword.
	otherWire, _, err := refreshStore.Issue(ctx, "sess-other-cp", "other-user-cp", int64(1))
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

	// F13: the other user's chain must survive the ChangePassword.
	_, _, err = refreshStore.Rotate(ctx, otherWire)
	assert.NoError(t, err, "other user's refresh chain must survive ChangePassword(user.ID)")
}

func TestService_Delete_RevokesRefreshChain(t *testing.T) {
	ctx := auth.TestContext("test-admin", []string{"admin"})
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	refreshStore := newCascadeStore(t)

	svc, err := NewService(userRepo, newInvalidator(t, userRepo, sessionRepo, refreshStore), slog.Default(),
		WithTokenIssuer(minimalStubIssuer), WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(contractTxRunner{})))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{Username: "frank", Email: "f@g.h", Password: "pwd"})
	require.NoError(t, err)

	wire, _, err := refreshStore.Issue(ctx, "sess-frank", user.ID, int64(1))
	require.NoError(t, err)

	// F13: issue a second user's wire token first — it must survive the Delete.
	otherWire, _, err := refreshStore.Issue(ctx, "sess-other-del", "other-user-del", int64(1))
	require.NoError(t, err)

	require.NoError(t, svc.Delete(adminCtxForService(), user.ID))

	_, _, err = refreshStore.Rotate(ctx, wire)
	require.Error(t, err)
	assert.True(t, errors.Is(err, refresh.ErrRejected),
		"refresh token after Delete must be rejected")

	// F13: the other user's chain must survive the Delete.
	_, _, err = refreshStore.Rotate(ctx, otherWire)
	assert.NoError(t, err, "other user's refresh chain must survive Delete(user.ID)")
}
