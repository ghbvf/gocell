package accountlockout

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/authzmutate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// fakeEmitter collects all outbox entries emitted during the test.
type fakeEmitter struct {
	mu      sync.Mutex
	entries []outbox.Entry
}

func (e *fakeEmitter) Emit(_ context.Context, entry outbox.Entry) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries = append(e.entries, entry)
	return nil
}

func (e *fakeEmitter) snapshot() []outbox.Entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]outbox.Entry, len(e.entries))
	copy(out, e.entries)
	return out
}

// fakeMetrics counts calls to IncAccountLockout by reason.
type fakeMetrics struct {
	mu       sync.Mutex
	byReason map[string]int
}

func newFakeMetrics() *fakeMetrics { return &fakeMetrics{byReason: map[string]int{}} }

func (m *fakeMetrics) IncAccountLockout(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byReason[reason]++
}

func (m *fakeMetrics) count(reason string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byReason[reason]
}

// fakeUserRepo is a minimal in-memory UserRepository that records
// UpdateLockoutFields invocations and surfaces the persisted state to
// assertions. It also implements GetByIDForUpdate / GetByUsernameForUpdate
// to satisfy authzmutator.ApplyInTx, and Update to receive status flips.
type fakeUserRepo struct {
	mu                       sync.Mutex
	byID                     map[string]*domain.User
	updateLockoutFieldsCalls int
	updateCalls              int
	bumpEpochCalls           int
}

var _ ports.UserRepository = (*fakeUserRepo)(nil)

func newFakeUserRepo(seed ...*domain.User) *fakeUserRepo {
	r := &fakeUserRepo{byID: map[string]*domain.User{}}
	for _, u := range seed {
		r.byID[u.ID] = cloneForFake(u)
	}
	return r
}

func cloneForFake(u *domain.User) *domain.User {
	clone, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    u.ID,
		Username:              u.Username,
		Email:                 u.Email,
		PasswordHash:          u.PasswordHash,
		PasswordVersion:       u.PasswordVersion,
		PasswordResetRequired: u.PasswordResetRequired(),
		Status:                u.Status(),
		Source:                u.CreationSource,
		AuthzEpoch:            u.AuthzEpoch(),
		CreatedAt:             u.CreatedAt,
		UpdatedAt:             u.UpdatedAt,
		FailedLoginCount:      u.FailedLoginCount(),
		LastFailedAt:          u.LastFailedAt(),
		LockedUntil:           u.AutoLockoutDeadline(),
	})
	if err != nil {
		panic(err)
	}
	return clone
}

func (r *fakeUserRepo) Create(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[u.ID] = cloneForFake(u)
	return nil
}

func (r *fakeUserRepo) GetByID(_ context.Context, id string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return cloneForFake(u), nil
}

func (r *fakeUserRepo) GetByUsername(_ context.Context, username string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.byID {
		if u.Username == username {
			return cloneForFake(u), nil
		}
	}
	return nil, errors.New("not found")
}

func (r *fakeUserRepo) GetByIDForUpdate(ctx context.Context, id string) (*domain.User, error) {
	return r.GetByID(ctx, id)
}

func (r *fakeUserRepo) GetByUsernameForUpdate(ctx context.Context, username string) (*domain.User, error) {
	return r.GetByUsername(ctx, username)
}

func (r *fakeUserRepo) Update(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[u.ID]; !ok {
		return errors.New("not found")
	}
	r.byID[u.ID] = cloneForFake(u)
	r.updateCalls++
	return nil
}

func (r *fakeUserRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, id)
	return nil
}

func (r *fakeUserRepo) UpdatePassword(_ context.Context, _ string, _ string, _ bool, _ int64) (int64, error) {
	return 0, errors.New("not used in accountlockout tests")
}

func (r *fakeUserRepo) BumpAuthzEpoch(_ context.Context, id string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byID[id]
	if !ok {
		return 0, errors.New("not found")
	}
	newEpoch := u.AuthzEpoch() + 1
	bumped, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID: u.ID, Username: u.Username, Email: u.Email,
		PasswordHash: u.PasswordHash, PasswordVersion: u.PasswordVersion,
		PasswordResetRequired: u.PasswordResetRequired(),
		Status:                u.Status(), Source: u.CreationSource,
		AuthzEpoch: newEpoch,
		CreatedAt:  u.CreatedAt, UpdatedAt: u.UpdatedAt,
		FailedLoginCount: u.FailedLoginCount(),
		LastFailedAt:     u.LastFailedAt(),
		LockedUntil:      u.AutoLockoutDeadline(),
	})
	if err != nil {
		return 0, err
	}
	r.byID[id] = bumped
	r.bumpEpochCalls++
	return newEpoch, nil
}

func (r *fakeUserRepo) UpdateLockoutFields(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[u.ID]; !ok {
		return errors.New("not found")
	}
	r.byID[u.ID] = cloneForFake(u)
	r.updateLockoutFieldsCalls++
	return nil
}

// Test-time duration constants extracted to satisfy TEST-TIME-LITERAL-01:
// every time.Duration literal in test code must appear in a package-level
// const initializer. These are site-specific auto-lockout window deltas, not
// cross-cutting timeouts, so they live here rather than in
// pkg/testutil/testtime.
const (
	testFixtureBackdate    = -1 * time.Hour
	testRecentFailureGap   = -1 * time.Minute
	testActiveLockedUntil  = 10 * time.Minute
	testTTLElapsedLastGap  = -30 * time.Minute
	testTTLElapsedUntilGap = -5 * time.Minute
	testInWindowLastGap    = -5 * time.Minute
	testInWindowUntilDelta = 10 * time.Minute
)

// stubSessionStore satisfies the session.Store surface used by
// credentialinvalidate.Invalidator. Only RevokeForSubject is exercised
// in these unit tests; all other methods return zero values / nil errors.
type stubSessionStore struct {
	revokeForSubjectCalls int
	revokedEvents         []session.CredentialEvent
}

var _ session.Store = (*stubSessionStore)(nil)

func (s *stubSessionStore) Create(_ context.Context, _ *session.Session) error { return nil }
func (s *stubSessionStore) Get(_ context.Context, _ string) (*session.ValidateView, error) {
	return nil, errors.New("stubSessionStore: Get unused")
}
func (s *stubSessionStore) Revoke(_ context.Context, _ string) error { return nil }
func (s *stubSessionStore) RevokeForSubject(_ context.Context, _ string, ev session.CredentialEvent) error {
	s.revokeForSubjectCalls++
	s.revokedEvents = append(s.revokedEvents, ev)
	return nil
}
func (s *stubSessionStore) RepoReady(_ context.Context) error { return nil }

// stubRefreshStore satisfies the refresh.Store surface used by
// credentialinvalidate.Invalidator. Only RevokeUser is exercised.
type stubRefreshStore struct {
	revokeUserCalls int
}

var _ refresh.Store = (*stubRefreshStore)(nil)

func (s *stubRefreshStore) Issue(_ context.Context, _, _ string, _ int64) (string, *refresh.Token, error) {
	return "", nil, errors.New("stubRefreshStore: Issue unused")
}

func (s *stubRefreshStore) Peek(_ context.Context, _ string) (*refresh.Token, error) {
	return nil, errors.New("stubRefreshStore: Peek unused")
}

func (s *stubRefreshStore) Rotate(_ context.Context, _ string) (string, *refresh.Token, error) {
	return "", nil, errors.New("stubRefreshStore: Rotate unused")
}
func (s *stubRefreshStore) RevokeSession(_ context.Context, _ string) error         { return nil }
func (s *stubRefreshStore) RevokeSessionDetached(_ context.Context, _ string) error { return nil }
func (s *stubRefreshStore) RevokeUser(_ context.Context, _ string) error {
	s.revokeUserCalls++
	return nil
}
func (s *stubRefreshStore) GC(_ context.Context, _ time.Time) (int, error) { return 0, nil }
func (s *stubRefreshStore) RepoReady(_ context.Context) error              { return nil }

func newTestService(t *testing.T, now time.Time, seed *domain.User) (*Service, *fakeUserRepo, *fakeEmitter, *fakeMetrics) {
	t.Helper()
	repo := newFakeUserRepo(seed)
	inv, err := credentialinvalidate.New(repo, &stubSessionStore{}, &stubRefreshStore{})
	require.NoError(t, err)
	mut, err := authzmutate.New(inv, repo)
	require.NoError(t, err)
	emitter := &fakeEmitter{}
	metrics := newFakeMetrics()
	clk := clockmock.New(now)
	svc, err := NewService(repo, mut, emitter, clk, WithMetrics(metrics))
	require.NoError(t, err)
	return svc, repo, emitter, metrics
}

func newSeedUser(t *testing.T, status domain.UserStatus, failedCount int, lastFailed, lockedUntil *time.Time) *domain.User {
	t.Helper()
	now := time.Now().UTC()
	u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:               "user-1",
		Username:         "alice",
		Email:            "alice@example.com",
		PasswordHash:     "$2a$10$hash",
		Status:           status,
		Source:           domain.UserSourceIdentity,
		AuthzEpoch:       1,
		CreatedAt:        now.Add(testFixtureBackdate),
		UpdatedAt:        now.Add(testFixtureBackdate),
		FailedLoginCount: failedCount,
		LastFailedAt:     lastFailed,
		LockedUntil:      lockedUntil,
	})
	require.NoError(t, err)
	return u
}

func TestService_RecordFailure_BelowThreshold(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	last := now.Add(testRecentFailureGap)
	seed := newSeedUser(t, domain.StatusActive, 1, &last, nil)
	svc, repo, emitter, metrics := newTestService(t, now, seed)

	require.NoError(t, svc.RecordFailure(context.Background(), context.Background(), seed))

	assert.Equal(t, 1, repo.updateLockoutFieldsCalls, "UpdateLockoutFields must be invoked once")
	assert.Equal(t, 0, repo.bumpEpochCalls, "no epoch bump when not locking")
	assert.Empty(t, emitter.snapshot(), "no event emitted below threshold")
	assert.Zero(t, metrics.count("threshold_locked"), "no lock metric below threshold")

	persisted, err := repo.GetByID(context.Background(), seed.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, persisted.FailedLoginCount(), "persisted count incremented")
	assert.Equal(t, domain.StatusActive, persisted.Status(), "status unchanged")
}

func TestService_RecordFailure_TriggersLock(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	last := now.Add(testRecentFailureGap)
	seed := newSeedUser(t, domain.StatusActive, 4, &last, nil)
	svc, repo, emitter, metrics := newTestService(t, now, seed)

	require.NoError(t, svc.RecordFailure(context.Background(), context.Background(), seed))

	assert.Equal(t, 1, repo.updateLockoutFieldsCalls, "UpdateLockoutFields once")
	assert.Equal(t, 1, repo.bumpEpochCalls, "epoch bumped on auto-lock")
	entries := emitter.snapshot()
	require.Len(t, entries, 1, "exactly one event emitted")
	assert.Equal(t, dto.TopicUserLocked, entries[0].EventType)
	var payload dto.UserLockedEvent
	require.NoError(t, json.Unmarshal(entries[0].Payload, &payload))
	assert.Equal(t, seed.ID, payload.UserID)
	assert.Equal(t, SystemActorID, payload.ActorID, "ActorID must be %q for auto-lock", SystemActorID)
	assert.Equal(t, 1, metrics.count("threshold_locked"), "lock metric incremented")

	persisted, err := repo.GetByID(context.Background(), seed.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusLocked, persisted.Status(), "status is Locked after auto-lock")
	require.NotNil(t, persisted.AutoLockoutDeadline())
	assert.True(t, persisted.AutoLockoutDeadline().Equal(now.Add(LockoutTTL)), "lockedUntil = now + TTL")
}

func TestService_RecordFailure_AlreadyLockedIsNoOp(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	last := now.Add(testRecentFailureGap)
	until := now.Add(testActiveLockedUntil)
	seed := newSeedUser(t, domain.StatusLocked, 5, &last, &until)
	svc, repo, emitter, metrics := newTestService(t, now, seed)

	require.NoError(t, svc.RecordFailure(context.Background(), context.Background(), seed))

	assert.Zero(t, repo.updateLockoutFieldsCalls, "no counter update when already locked")
	assert.Zero(t, repo.bumpEpochCalls, "no epoch bump when already locked")
	assert.Empty(t, emitter.snapshot(), "no event when already locked")
	assert.Zero(t, metrics.count("threshold_locked"))
}

func TestService_RecordSuccess_ResetsCounter(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	last := now.Add(testRecentFailureGap)
	seed := newSeedUser(t, domain.StatusActive, 3, &last, nil)
	svc, repo, _, _ := newTestService(t, now, seed)

	require.NoError(t, svc.RecordSuccess(context.Background(), context.Background(), seed))

	assert.Equal(t, 1, repo.updateLockoutFieldsCalls, "UpdateLockoutFields once")
	persisted, err := repo.GetByID(context.Background(), seed.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, persisted.FailedLoginCount())
	assert.Nil(t, persisted.LastFailedAt())
	assert.Nil(t, persisted.AutoLockoutDeadline())
}

func TestService_RecordSuccess_AlreadyCleanIsNoOp(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	seed := newSeedUser(t, domain.StatusActive, 0, nil, nil)
	svc, repo, _, _ := newTestService(t, now, seed)

	require.NoError(t, svc.RecordSuccess(context.Background(), context.Background(), seed))

	assert.Zero(t, repo.updateLockoutFieldsCalls, "no UPDATE when counter already clean")
}

func TestService_TryLazyUnlock_TTLElapsed(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	last := now.Add(testTTLElapsedLastGap)
	until := now.Add(testTTLElapsedUntilGap) // already past
	seed := newSeedUser(t, domain.StatusLocked, 5, &last, &until)
	svc, repo, _, metrics := newTestService(t, now, seed)

	unlocked, err := svc.TryLazyUnlock(context.Background(), context.Background(), seed)
	require.NoError(t, err)
	assert.True(t, unlocked, "should lazy-unlock when TTL elapsed")
	assert.Equal(t, 1, metrics.count("lazy_unlocked"))

	persisted, err := repo.GetByID(context.Background(), seed.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusActive, persisted.Status(), "status flipped back to Active")
	assert.Equal(t, 0, persisted.FailedLoginCount(), "counter reset by ActivateUser mutation")
	assert.Nil(t, persisted.AutoLockoutDeadline())
}

func TestService_TryLazyUnlock_StillInWindow(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	last := now.Add(testInWindowLastGap)
	until := now.Add(testInWindowUntilDelta) // still locked
	seed := newSeedUser(t, domain.StatusLocked, 5, &last, &until)
	svc, repo, _, metrics := newTestService(t, now, seed)

	unlocked, err := svc.TryLazyUnlock(context.Background(), context.Background(), seed)
	require.NoError(t, err)
	assert.False(t, unlocked, "must not unlock while TTL still in the future")
	assert.Zero(t, metrics.count("lazy_unlocked"))

	persisted, err := repo.GetByID(context.Background(), seed.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusLocked, persisted.Status(), "status remains Locked")
}

func TestService_TryLazyUnlock_ManualLockNoTTL(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	seed := newSeedUser(t, domain.StatusLocked, 0, nil, nil) // admin-locked, no TTL
	svc, _, _, metrics := newTestService(t, now, seed)

	unlocked, err := svc.TryLazyUnlock(context.Background(), context.Background(), seed)
	require.NoError(t, err)
	assert.False(t, unlocked, "manual lock (lockedUntil==nil) must not lazy-unlock")
	assert.Zero(t, metrics.count("lazy_unlocked"))
}

func TestService_TryLazyUnlock_NotLocked(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	seed := newSeedUser(t, domain.StatusActive, 0, nil, nil)
	svc, _, _, _ := newTestService(t, now, seed)

	unlocked, err := svc.TryLazyUnlock(context.Background(), context.Background(), seed)
	require.NoError(t, err)
	assert.False(t, unlocked)
}

func TestNewService_RejectsNilDeps(t *testing.T) {
	// Construct one valid set, then test that swapping each dep to nil fails.
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	seed := newSeedUser(t, domain.StatusActive, 0, nil, nil)
	repo := newFakeUserRepo(seed)
	inv, err := credentialinvalidate.New(repo, &stubSessionStore{}, &stubRefreshStore{})
	require.NoError(t, err)
	mut, err := authzmutate.New(inv, repo)
	require.NoError(t, err)
	emitter := &fakeEmitter{}
	clk := clockmock.New(now)

	t.Run("nil repo", func(t *testing.T) {
		_, err := NewService(nil, mut, emitter, clk)
		assert.Error(t, err)
	})
	t.Run("nil mutator", func(t *testing.T) {
		_, err := NewService(repo, nil, emitter, clk)
		assert.Error(t, err)
	})
	t.Run("nil emitter", func(t *testing.T) {
		_, err := NewService(repo, mut, nil, clk)
		assert.Error(t, err)
	})
	t.Run("nil clock", func(t *testing.T) {
		_, err := NewService(repo, mut, emitter, nil)
		assert.Error(t, err)
	})
}

// Sanity: make sure fakeUserRepo conforms to the ports.UserRepository surface
// statically. This guards the test's fake from drifting if the interface
// grows new methods.
func TestFakeUserRepo_StaticConformance(_ *testing.T) {
	var _ ports.UserRepository = (*fakeUserRepo)(nil)
}
