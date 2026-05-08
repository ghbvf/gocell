package credentialrevoke

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

func TestUserRevokesSessionsAndRefreshChains(t *testing.T) {
	ctx := context.Background()
	sessionRepo := &fakeSessionRepo{}
	refreshStore := &fakeRefreshStore{}

	err := User(ctx, sessionRepo, refreshStore, "usr-1", "lock user")
	require.NoError(t, err)

	assert.Equal(t, []string{"usr-1"}, sessionRepo.revokedUsers)
	assert.Equal(t, []string{"usr-1"}, refreshStore.revokedUsers)
}

func TestUserStopsWhenSessionRevokeFails(t *testing.T) {
	ctx := context.Background()
	sessionErr := errors.New("session repo down")
	sessionRepo := &fakeSessionRepo{revokeUserErr: sessionErr}
	refreshStore := &fakeRefreshStore{}

	err := User(ctx, sessionRepo, refreshStore, "usr-1", "lock user")
	require.ErrorIs(t, err, sessionErr)
	assert.Contains(t, err.Error(), "lock user revoke sessions")
	assert.Empty(t, refreshStore.revokedUsers)
}

func TestUserReturnsRefreshRevokeError(t *testing.T) {
	ctx := context.Background()
	refreshErr := errors.New("refresh store down")
	sessionRepo := &fakeSessionRepo{}
	refreshStore := &fakeRefreshStore{revokeUserErr: refreshErr}

	err := User(ctx, sessionRepo, refreshStore, "usr-1", "lock user")
	require.ErrorIs(t, err, refreshErr)
	assert.Contains(t, err.Error(), "lock user revoke refresh chains")
	assert.Equal(t, []string{"usr-1"}, sessionRepo.revokedUsers)
}

type fakeSessionRepo struct {
	revokedUsers  []string
	revokeUserErr error
}

func (r *fakeSessionRepo) Create(context.Context, *domain.Session) error {
	panic("unexpected Create")
}

func (r *fakeSessionRepo) GetByID(context.Context, string) (*domain.Session, error) {
	panic("unexpected GetByID")
}

func (r *fakeSessionRepo) Update(context.Context, *domain.Session) error {
	panic("unexpected Update")
}

func (r *fakeSessionRepo) RevokeByIDAndOwner(context.Context, string, string) error {
	panic("unexpected RevokeByIDAndOwner")
}

func (r *fakeSessionRepo) RevokeByUserID(_ context.Context, userID string) error {
	r.revokedUsers = append(r.revokedUsers, userID)
	return r.revokeUserErr
}

func (r *fakeSessionRepo) Delete(context.Context, string) error {
	panic("unexpected Delete")
}

type fakeRefreshStore struct {
	revokedUsers  []string
	revokeUserErr error
}

func (s *fakeRefreshStore) Issue(context.Context, string, string) (string, *refresh.Token, error) {
	panic("unexpected Issue")
}

func (s *fakeRefreshStore) Peek(context.Context, string) (*refresh.Token, error) {
	panic("unexpected Peek")
}

func (s *fakeRefreshStore) Rotate(context.Context, string) (string, *refresh.Token, error) {
	panic("unexpected Rotate")
}

func (s *fakeRefreshStore) RevokeSession(context.Context, string) error {
	panic("unexpected RevokeSession")
}

func (s *fakeRefreshStore) RevokeSessionDetached(context.Context, string) error {
	panic("unexpected RevokeSessionDetached")
}

func (s *fakeRefreshStore) RevokeUser(_ context.Context, userID string) error {
	s.revokedUsers = append(s.revokedUsers, userID)
	return s.revokeUserErr
}

func (s *fakeRefreshStore) GC(context.Context, time.Time) (int, error) {
	panic("unexpected GC")
}
