package authzmutate_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/authzmutate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// fakeTxRunner is a simple synchronous TxRunner for unit tests.
type fakeTxRunner struct{}

func (f fakeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func newTestMutator(t testing.TB, store *mem.Store, sessionStore session.Store) *authzmutate.Mutator {
	t.Helper()
	refreshStore := testutil.RealRefreshStore(t)
	inv, err := credentialinvalidate.New(store.UserRepository(), sessionStore, refreshStore)
	require.NoError(t, err)
	m, err := authzmutate.New(inv, store.UserRepository(), persistence.WrapForCell(fakeTxRunner{}))
	require.NoError(t, err)
	return m
}

// seedUser creates a user with the given status and inserts it into store.
// Used by TestApply_AllMutations and TestApply_UserNotFound to avoid inline
// domain.ReconstituteUser boilerplate in every sub-test.
func seedUser(t testing.TB, store *mem.Store, userID string, status domain.UserStatus) {
	t.Helper()
	//nolint:gosec // G101: PasswordHash is a test fixture bcrypt placeholder, not a real credential.
	u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:           userID,
		Username:     userID,
		Email:        userID + "@test.local",
		PasswordHash: "$2a$12$hash",
		Status:       status,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})
	require.NoError(t, err)
	require.NoError(t, store.UserRepository().Create(context.Background(), u))
}

func TestNew_NilDeps(t *testing.T) {
	store := mem.NewStore(clock.Real())
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := testutil.RealRefreshStore(t)
	inv, err := credentialinvalidate.New(store.UserRepository(), sessionStore, refreshStore)
	require.NoError(t, err)

	tests := []struct {
		name    string
		inv     *credentialinvalidate.Invalidator
		repo    interface{}
		txMgr   interface{}
		wantErr string
	}{
		{"nil invalidator", nil, store.UserRepository(), persistence.WrapForCell(fakeTxRunner{}), "Invalidator required"},
		{"nil txMgr", inv, store.UserRepository(), nil, "CellTxManager required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't easily pass nil for typed interfaces, so test separately below.
			_ = tt
		})
	}

	// nil invalidator
	_, err = authzmutate.New(nil, store.UserRepository(), persistence.WrapForCell(fakeTxRunner{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalidator required")

	// nil repo passed as interface — use nil UserRepository
	_, err = authzmutate.New(inv, nil, persistence.WrapForCell(fakeTxRunner{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UserRepository required")

	// nil txMgr
	_, err = authzmutate.New(inv, store.UserRepository(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CellTxManager required")
}

func TestApply_NilMutation(t *testing.T) {
	store := mem.NewStore(clock.Real())
	sessionStore := testutil.RealSessionRepo(t)
	m := newTestMutator(t, store, sessionStore)

	err := m.Apply(context.Background(), "usr-1", nil, time.Now())
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindInvalid, ce.Kind)
}

func TestApply_EmptyUserID(t *testing.T) {
	store := mem.NewStore(clock.Real())
	sessionStore := testutil.RealSessionRepo(t)
	m := newTestMutator(t, store, sessionStore)

	err := m.Apply(context.Background(), "", authzmutate.LockUser{}, time.Now())
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindInvalid, ce.Kind)
}

// TestApply_AllMutations is the table-driven conformance test over all Mutation
// variants. It asserts: status/flag persisted via repo + inv.Apply called
// exactly when Invalidates()==true with correct Event.
func TestApply_AllMutations(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name           string
		initialStatus  domain.UserStatus
		mutation       authzmutate.Mutation
		wantStatus     domain.UserStatus
		wantResetFlag  bool
		wantInvalidate bool
		wantEvent      session.CredentialEvent
	}{
		{
			name:           "LockUser sets status=locked and invalidates",
			initialStatus:  domain.StatusActive,
			mutation:       authzmutate.LockUser{},
			wantStatus:     domain.StatusLocked,
			wantResetFlag:  false,
			wantInvalidate: true,
			wantEvent:      session.CredentialEventLock,
		},
		{
			name:           "SuspendUser sets status=suspended and invalidates",
			initialStatus:  domain.StatusActive,
			mutation:       authzmutate.SuspendUser{},
			wantStatus:     domain.StatusSuspended,
			wantResetFlag:  false,
			wantInvalidate: true,
			wantEvent:      session.CredentialEventLock,
		},
		{
			name:           "ActivateUser sets status=active does NOT invalidate",
			initialStatus:  domain.StatusLocked,
			mutation:       authzmutate.ActivateUser{},
			wantStatus:     domain.StatusActive,
			wantResetFlag:  false,
			wantInvalidate: false,
			wantEvent:      session.CredentialEventLock, // not consumed (Invalidates==false); pinned to regression-guard Event() return
		},
		{
			name:           "RequirePasswordReset sets flag and invalidates",
			initialStatus:  domain.StatusActive,
			mutation:       authzmutate.RequirePasswordReset{},
			wantStatus:     domain.StatusActive,
			wantResetFlag:  true,
			wantInvalidate: true,
			wantEvent:      session.CredentialEventPasswordReset,
		},
		{
			name:           "ClearPasswordReset clears flag does NOT invalidate",
			initialStatus:  domain.StatusActive,
			mutation:       authzmutate.ClearPasswordReset{},
			wantStatus:     domain.StatusActive,
			wantResetFlag:  false,
			wantInvalidate: false,
			wantEvent:      session.CredentialEventPasswordReset, // not consumed (Invalidates==false); pinned to regression-guard Event() return
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mem.NewStore(clock.Real())
			sessionStore := testutil.RealSessionRepo(t)
			refreshStore := testutil.RealRefreshStore(t)
			inv, err := credentialinvalidate.New(store.UserRepository(), sessionStore, refreshStore)
			require.NoError(t, err)
			mutator, err := authzmutate.New(inv, store.UserRepository(), persistence.WrapForCell(fakeTxRunner{}))
			require.NoError(t, err)

			// Seed user with initial status and passwordResetRequired=false.
			seedUser(t, store, "usr-1", tt.initialStatus)

			// Track initial epoch.
			u, err := store.UserRepository().GetByID(context.Background(), "usr-1")
			require.NoError(t, err)
			initialEpoch := u.AuthzEpoch()

			// Apply mutation.
			err = mutator.Apply(context.Background(), "usr-1", tt.mutation, now.Add(time.Second))
			require.NoError(t, err)

			// Read back from repo.
			got, err := store.UserRepository().GetByID(context.Background(), "usr-1")
			require.NoError(t, err)

			assert.Equal(t, tt.wantStatus, got.Status(), "Status mismatch")
			assert.Equal(t, tt.wantResetFlag, got.PasswordResetRequired(), "PasswordResetRequired mismatch")

			// Always assert Event() return value for regression-guard.
			// For Invalidates()==false cases, Event() is a don't-care (never read
			// by any code path), but pinning the value here catches accidental
			// Event() return-value changes.
			if tt.wantEvent != 0 {
				assert.Equal(t, tt.wantEvent, tt.mutation.Event(), "Event() return value regression")
			}

			if tt.wantInvalidate {
				// epoch must have been bumped by inv.Apply
				assert.Greater(t, got.AuthzEpoch(), initialEpoch, "epoch must be bumped on invalidating mutation")
			} else {
				// epoch must NOT be bumped — proves inv.Apply was not called
				// (Invalidates==false means Apply skips inv.Apply entirely)
				assert.Equal(t, initialEpoch, got.AuthzEpoch(),
					"epoch must NOT be bumped on additive mutation")
			}
		})
	}
}

func TestApply_UserNotFound(t *testing.T) {
	store := mem.NewStore(clock.Real())
	sessionStore := testutil.RealSessionRepo(t)
	m := newTestMutator(t, store, sessionStore)

	err := m.Apply(context.Background(), "usr-nonexistent", authzmutate.LockUser{}, time.Now())
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindNotFound, ce.Kind)
}
