package credentialinvalidate

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// ---------------------------------------------------------------------------
// Type-safe stubs (follow fake-repo pattern from sessionmint_test.go)
// ---------------------------------------------------------------------------

// stubUserRepo stubs ports.UserRepository for testing. Only BumpAuthzEpoch
// is exercised; every other method panics to catch accidental calls.
type stubUserRepo struct {
	bumpEpoch    int64
	bumpErr      error
	bumpCallsFor []string // captures userID args
}

func (s *stubUserRepo) BumpAuthzEpoch(_ context.Context, userID string) (int64, error) {
	s.bumpCallsFor = append(s.bumpCallsFor, userID)
	return s.bumpEpoch, s.bumpErr
}

func (s *stubUserRepo) Create(_ context.Context, _ *domain.User) error {
	panic("stubUserRepo.Create: unexpected call")
}

func (s *stubUserRepo) GetByID(_ context.Context, _ string) (*domain.User, error) {
	panic("stubUserRepo.GetByID: unexpected call")
}

func (s *stubUserRepo) GetByUsername(_ context.Context, _ string) (*domain.User, error) {
	panic("stubUserRepo.GetByUsername: unexpected call")
}

func (s *stubUserRepo) Update(_ context.Context, _ *domain.User) error {
	panic("stubUserRepo.Update: unexpected call")
}

func (s *stubUserRepo) Delete(_ context.Context, _ string) error {
	panic("stubUserRepo.Delete: unexpected call")
}

func (s *stubUserRepo) UpdatePassword(_ context.Context, _ string, _ string, _ bool, _ int64) (int64, error) {
	panic("stubUserRepo.UpdatePassword: unexpected call")
}

func (s *stubUserRepo) GetByIDForUpdate(_ context.Context, _ string) (*domain.User, error) {
	panic("stubUserRepo.GetByIDForUpdate: unexpected call")
}

func (s *stubUserRepo) GetByUsernameForUpdate(_ context.Context, _ string) (*domain.User, error) {
	panic("stubUserRepo.GetByUsernameForUpdate: unexpected call")
}

var _ ports.UserRepository = (*stubUserRepo)(nil)

// stubSessionStore stubs session.Store for testing. Only RevokeForSubject
// is exercised; other methods panic.
type stubSessionStore struct {
	revokeErr      error
	revokeCallsFor []string // captures subjectID args
}

func (s *stubSessionStore) Create(_ context.Context, _ *session.Session) error {
	panic("stubSessionStore.Create: unexpected call")
}

func (s *stubSessionStore) Get(_ context.Context, _ string) (*session.ValidateView, error) {
	panic("stubSessionStore.Get: unexpected call")
}

func (s *stubSessionStore) Revoke(_ context.Context, _ string) error {
	panic("stubSessionStore.Revoke: unexpected call")
}

func (s *stubSessionStore) RevokeForSubject(_ context.Context, subjectID string, _ session.CredentialEvent) error {
	s.revokeCallsFor = append(s.revokeCallsFor, subjectID)
	return s.revokeErr
}

var _ session.Store = (*stubSessionStore)(nil)

// stubRefreshStore stubs refresh.Store for testing. Only RevokeUser is
// exercised; other methods panic.
type stubRefreshStore struct {
	revokeUserErr      error
	revokeUserCallsFor []string // captures subjectID args
}

func (s *stubRefreshStore) Issue(_ context.Context, _, _ string, _ int64) (string, *refresh.Token, error) {
	panic("stubRefreshStore.Issue: unexpected call")
}

func (s *stubRefreshStore) Peek(_ context.Context, _ string) (*refresh.Token, error) {
	panic("stubRefreshStore.Peek: unexpected call")
}

func (s *stubRefreshStore) Rotate(_ context.Context, _ string) (string, *refresh.Token, error) {
	panic("stubRefreshStore.Rotate: unexpected call")
}

func (s *stubRefreshStore) RevokeSession(_ context.Context, _ string) error {
	panic("stubRefreshStore.RevokeSession: unexpected call")
}

func (s *stubRefreshStore) RevokeSessionDetached(_ context.Context, _ string) error {
	panic("stubRefreshStore.RevokeSessionDetached: unexpected call")
}

func (s *stubRefreshStore) RevokeUser(_ context.Context, subjectID string) error {
	s.revokeUserCallsFor = append(s.revokeUserCallsFor, subjectID)
	return s.revokeUserErr
}

func (s *stubRefreshStore) GC(_ context.Context, _ time.Time) (int, error) {
	panic("stubRefreshStore.GC: unexpected call")
}

var _ refresh.Store = (*stubRefreshStore)(nil)

// ---------------------------------------------------------------------------
// Apply tests
// ---------------------------------------------------------------------------

func TestApply_HappyPath(t *testing.T) {
	users := &stubUserRepo{bumpEpoch: 2}
	sess := &stubSessionStore{}
	ref := &stubRefreshStore{}

	inv, err := New(users, sess, ref)
	require.NoError(t, err)

	err = inv.Apply(context.Background(), "subj-1", session.CredentialEventPasswordReset)
	require.NoError(t, err)

	assert.Equal(t, []string{"subj-1"}, users.bumpCallsFor, "BumpAuthzEpoch must be called once")
	assert.Equal(t, []string{"subj-1"}, sess.revokeCallsFor, "RevokeForSubject must be called once")
	assert.Equal(t, []string{"subj-1"}, ref.revokeUserCallsFor, "RevokeUser must be called once")
}

func TestApply_UserRepoError_ShortCircuits(t *testing.T) {
	bumpErr := errors.New("db: users table gone")
	users := &stubUserRepo{bumpErr: bumpErr}
	sess := &stubSessionStore{}
	ref := &stubRefreshStore{}

	inv, err := New(users, sess, ref)
	require.NoError(t, err)

	err = inv.Apply(context.Background(), "subj-1", session.CredentialEventLock)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bump authz_epoch", "error must mention bump authz_epoch")
	assert.ErrorIs(t, err, bumpErr, "original error must be in chain")
	assert.Empty(t, sess.revokeCallsFor, "sessions must NOT be called when users fails")
	assert.Empty(t, ref.revokeUserCallsFor, "refresh must NOT be called when users fails")
}

func TestApply_SessionStoreError(t *testing.T) {
	sessErr := errors.New("sessions: connection refused")
	users := &stubUserRepo{bumpEpoch: 1}
	sess := &stubSessionStore{revokeErr: sessErr}
	ref := &stubRefreshStore{}

	inv, err := New(users, sess, ref)
	require.NoError(t, err)

	err = inv.Apply(context.Background(), "subj-2", session.CredentialEventDelete)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoke sessions", "error must mention revoke sessions")
	assert.ErrorIs(t, err, sessErr)
	// users was called, refresh was NOT called
	assert.Equal(t, []string{"subj-2"}, users.bumpCallsFor)
	assert.Empty(t, ref.revokeUserCallsFor, "refresh must NOT be called when sessions fails")
}

func TestApply_RefreshStoreError(t *testing.T) {
	refErr := errors.New("refresh: table locked")
	users := &stubUserRepo{bumpEpoch: 1}
	sess := &stubSessionStore{}
	ref := &stubRefreshStore{revokeUserErr: refErr}

	inv, err := New(users, sess, ref)
	require.NoError(t, err)

	err = inv.Apply(context.Background(), "subj-3", session.CredentialEventRoleRevoke)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoke refresh chain", "error must mention revoke refresh chain")
	assert.ErrorIs(t, err, refErr)
	// users and sessions were called
	assert.Equal(t, []string{"subj-3"}, users.bumpCallsFor)
	assert.Equal(t, []string{"subj-3"}, sess.revokeCallsFor)
}

// ---------------------------------------------------------------------------
// New constructor nil-guard tests
// ---------------------------------------------------------------------------

func TestNew_NilUsers_ReturnsKindInvalid(t *testing.T) {
	inv, err := New(nil, &stubSessionStore{}, &stubRefreshStore{})
	require.Error(t, err)
	assert.Nil(t, inv)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.KindInvalid, ec.Kind)
}

func TestNew_NilSessions_ReturnsKindInvalid(t *testing.T) {
	inv, err := New(&stubUserRepo{}, nil, &stubRefreshStore{})
	require.Error(t, err)
	assert.Nil(t, inv)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.KindInvalid, ec.Kind)
}

func TestNew_NilRefresh_ReturnsKindInvalid(t *testing.T) {
	inv, err := New(&stubUserRepo{}, &stubSessionStore{}, nil)
	require.Error(t, err)
	assert.Nil(t, inv)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.KindInvalid, ec.Kind)
}

// ---------------------------------------------------------------------------
// MustNew panic tests (Finding #6: extended to cover all 3 nil deps)
// ---------------------------------------------------------------------------

// TestMustNew_NilPanics verifies that MustNew panics for each nil dependency.
// The panic value is a panicregister.Approved-wrapped errcode.Assertion, so
// we assert via require.Panics + recover rather than require.PanicsWithValue
// (which does a deep-equal comparison that would not match the wrapped type).
func TestMustNew_NilPanics(t *testing.T) {
	cases := []struct {
		name    string
		call    func()
		wantMsg string
	}{
		{
			name: "nil users panics",
			call: func() {
				MustNew(nil, &stubSessionStore{}, &stubRefreshStore{})
			},
			wantMsg: "UserRepository",
		},
		{
			name: "nil sessions panics",
			call: func() {
				MustNew(&stubUserRepo{}, nil, &stubRefreshStore{})
			},
			wantMsg: "session.Store",
		},
		{
			name: "nil refreshStore panics",
			call: func() {
				MustNew(&stubUserRepo{}, &stubSessionStore{}, nil)
			},
			wantMsg: "refresh.Store",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				tc.call()
			}()
			require.NotNil(t, recovered, "MustNew must panic for %s", tc.name)
			// The panic value is panicregister.Approved("credentialinvalidate-mustnew", ...)
			// containing an errcode.Assertion message. Convert to string and check.
			msg := fmt.Sprintf("%v", recovered)
			assert.Contains(t, msg, tc.wantMsg,
				"panic message must mention the missing dependency: %s", tc.name)
		})
	}
}
