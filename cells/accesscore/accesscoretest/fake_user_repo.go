package accesscoretest

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// FakeCall records a single method invocation.
type FakeCall struct {
	// Method is the name of the called method.
	Method string
	// Args holds the string-representable arguments (e.g. IDs, usernames).
	Args []string
}

// FakeUserRepo implements ports.UserRepository in memory for unit tests.
// All operations are concurrency-safe.
type FakeUserRepo struct {
	mu    sync.Mutex
	byID  map[string]*domain.User
	calls []FakeCall
}

// NewFakeUserRepo returns an empty FakeUserRepo.
func NewFakeUserRepo() *FakeUserRepo {
	return &FakeUserRepo{byID: make(map[string]*domain.User)}
}

// SeedUser stores u so subsequent reads see it.
// Overwrites any existing entry with the same ID.
func (r *FakeUserRepo) SeedUser(u domain.User) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cloned := cloneUser(&u)
	r.byID[u.ID] = cloned
}

// Snapshot returns a copy of all stored users in non-deterministic order.
func (r *FakeUserRepo) Snapshot() []domain.User {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.User, 0, len(r.byID))
	for _, u := range r.byID {
		out = append(out, *cloneUser(u))
	}
	return out
}

// CallsOf returns all recorded calls for the given method name.
func (r *FakeUserRepo) CallsOf(method string) []FakeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []FakeCall
	for _, c := range r.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (r *FakeUserRepo) record(method string, args ...string) {
	r.calls = append(r.calls, FakeCall{Method: method, Args: args})
}

// Create stores u and records the call.
func (r *FakeUserRepo) Create(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("Create", u.ID)
	r.byID[u.ID] = cloneUser(u)
	return nil
}

// GetByID fetches by ID. Returns ErrAuthUserNotFound when absent.
func (r *FakeUserRepo) GetByID(_ context.Context, id string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("GetByID", id)
	u, ok := r.byID[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found")
	}
	return cloneUser(u), nil
}

// GetByUsername fetches by username. Returns ErrAuthUserNotFound when absent.
func (r *FakeUserRepo) GetByUsername(_ context.Context, username string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("GetByUsername", username)
	for _, u := range r.byID {
		if u.Username == username {
			return cloneUser(u), nil
		}
	}
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found")
}

// GetByIDForUpdate behaves identically to GetByID in the fake (no DB locks).
func (r *FakeUserRepo) GetByIDForUpdate(_ context.Context, id string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("GetByIDForUpdate", id)
	u, ok := r.byID[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found")
	}
	return cloneUser(u), nil
}

// GetByUsernameForUpdate behaves identically to GetByUsername in the fake.
func (r *FakeUserRepo) GetByUsernameForUpdate(_ context.Context, username string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("GetByUsernameForUpdate", username)
	for _, u := range r.byID {
		if u.Username == username {
			return cloneUser(u), nil
		}
	}
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found")
}

// Update overwrites the stored user. Returns ErrAuthUserNotFound when absent.
func (r *FakeUserRepo) Update(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("Update", u.ID)
	if _, ok := r.byID[u.ID]; !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found")
	}
	r.byID[u.ID] = cloneUser(u)
	return nil
}

// Delete removes u from the store.
func (r *FakeUserRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("Delete", id)
	delete(r.byID, id)
	return nil
}

// UpdatePassword applies a CAS-guarded password change.
// Returns ErrVersionConflict when expectedPasswordVersion mismatches.
func (r *FakeUserRepo) UpdatePassword(
	_ context.Context,
	userID, newHash string,
	resetRequired bool,
	expectedPasswordVersion int64,
) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("UpdatePassword", userID)
	u, ok := r.byID[userID]
	if !ok {
		return 0, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found")
	}
	if u.PasswordVersion != expectedPasswordVersion {
		return 0, errcode.New(errcode.KindConflict, errcode.ErrVersionConflict, "password version conflict")
	}
	newVersion := expectedPasswordVersion + 1
	updated, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    u.ID,
		Username:              u.Username,
		Email:                 u.Email,
		PasswordHash:          newHash,
		PasswordVersion:       newVersion,
		PasswordResetRequired: resetRequired,
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
		return 0, err
	}
	r.byID[userID] = updated
	return newVersion, nil
}

// BumpAuthzEpoch atomically increments authz_epoch.
func (r *FakeUserRepo) BumpAuthzEpoch(_ context.Context, userID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("BumpAuthzEpoch", userID)
	u, ok := r.byID[userID]
	if !ok {
		return 0, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found")
	}
	newEpoch := u.AuthzEpoch() + 1
	bumped, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    u.ID,
		Username:              u.Username,
		Email:                 u.Email,
		PasswordHash:          u.PasswordHash,
		PasswordVersion:       u.PasswordVersion,
		PasswordResetRequired: u.PasswordResetRequired(),
		Status:                u.Status(),
		Source:                u.CreationSource,
		AuthzEpoch:            newEpoch,
		CreatedAt:             u.CreatedAt,
		UpdatedAt:             u.UpdatedAt,
		FailedLoginCount:      u.FailedLoginCount(),
		LastFailedAt:          u.LastFailedAt(),
		LockedUntil:           u.AutoLockoutDeadline(),
	})
	if err != nil {
		return 0, err
	}
	r.byID[userID] = bumped
	return newEpoch, nil
}

// UpdateLockoutFields persists auto-lockout state for an existing user.
func (r *FakeUserRepo) UpdateLockoutFields(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("UpdateLockoutFields", u.ID)
	if _, ok := r.byID[u.ID]; !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found")
	}
	r.byID[u.ID] = cloneUser(u)
	return nil
}

// compile-time interface check.
var _ ports.UserRepository = (*FakeUserRepo)(nil)

// cloneUser returns a deep copy of u so mutations on the returned pointer do
// not affect the stored value.
func cloneUser(u *domain.User) *domain.User {
	cloned, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
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
		// ReconstituteUser only fails on invalid input that callers must not
		// produce (empty IDs, zero epoch). Panic-as-assertion here prevents
		// silently returning a corrupt clone.
		panic(err)
	}
	return cloned
}
