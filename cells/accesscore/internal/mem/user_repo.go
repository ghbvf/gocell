// Package mem provides in-memory repository implementations for accesscore.
package mem

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var _ ports.UserRepository = (*UserRepository)(nil)

const msgUserNotFound = "user not found"

// UserRepository is an in-memory implementation of ports.UserRepository.
type UserRepository struct {
	mu      sync.RWMutex
	byID    map[string]*domain.User
	byName  map[string]*domain.User
	byEmail map[string]*domain.User
}

// NewUserRepository creates an empty in-memory UserRepository.
func NewUserRepository() *UserRepository {
	return &UserRepository{
		byID:    make(map[string]*domain.User),
		byName:  make(map[string]*domain.User),
		byEmail: make(map[string]*domain.User),
	}
}

func (r *UserRepository) Create(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byName[user.Username]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "username already exists",
			errcode.WithInternal(fmt.Sprintf("username=%q", user.Username)))
	}
	if _, exists := r.byEmail[user.Email]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrAuthEmailDuplicate, "email already exists",
			errcode.WithInternal(fmt.Sprintf("email=%q", user.Email)))
	}

	c := cloneUser(user)
	if c.Version == 0 {
		c.Version = 1
	}
	r.byID[user.ID] = c
	r.byName[user.Username] = c
	r.byEmail[user.Email] = c
	return nil
}

func (r *UserRepository) GetByID(_ context.Context, id string) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	u, ok := r.byID[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", id)))
	}
	return cloneUser(u), nil
}

// GetByIDForUpdate is the row-locking variant used by flows that must
// serialize against credential issuance. The mem implementation holds the
// write lock while cloning the user, which serializes with ApplyPatch/Create.
func (r *UserRepository) GetByIDForUpdate(_ context.Context, id string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	u, ok := r.byID[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", id)))
	}
	return cloneUser(u), nil
}

func (r *UserRepository) GetByUsername(_ context.Context, username string) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	u, ok := r.byName[username]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("username=%q", username)))
	}
	return cloneUser(u), nil
}

// GetByUsernameForUpdate is the row-locking variant used by login flows.
// The mem implementation holds the write lock while cloning the user, which
// serializes with ApplyPatch/Create. Callers must invoke this inside a
// TxRunner.RunInTx closure.
func (r *UserRepository) GetByUsernameForUpdate(_ context.Context, username string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	u, ok := r.byName[username]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("username=%q", username)))
	}
	return cloneUser(u), nil
}

// ApplyPatch updates the user atomically gated on p.CurrentVersion.
//
// Returns the post-update User on success; ErrAuthConcurrentUpdate when the
// stored version no longer matches p.CurrentVersion; ErrAuthUserDuplicate /
// ErrAuthEmailDuplicate on uniqueness collision; ErrAuthUserNotFound when no
// row matches p.ID.
func (r *UserRepository) ApplyPatch(_ context.Context, p ports.UserPatch) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	row, ok := r.byID[p.ID]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", p.ID)))
	}

	if row.Version != p.CurrentVersion {
		return nil, errcode.New(errcode.KindConflict, errcode.ErrAuthConcurrentUpdate,
			"user was modified by another request, please retry",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s version_expected=%d version_actual=%d",
				p.ID, p.CurrentVersion, row.Version)))
	}

	// Username uniqueness: check before mutating index.
	if p.Username != nil && *p.Username != row.Username {
		if existing, found := r.byName[*p.Username]; found && existing.ID != p.ID {
			return nil, errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "username already exists",
				errcode.WithInternal(fmt.Sprintf("username=%q", *p.Username)))
		}
		delete(r.byName, row.Username)
		row.Username = *p.Username
		r.byName[row.Username] = row
	}

	// Email uniqueness: check before mutating index.
	if p.Email != nil && *p.Email != row.Email {
		if existing, found := r.byEmail[*p.Email]; found && existing.ID != p.ID {
			return nil, errcode.New(errcode.KindConflict, errcode.ErrAuthEmailDuplicate, "email already exists",
				errcode.WithInternal(fmt.Sprintf("email=%q", *p.Email)))
		}
		delete(r.byEmail, row.Email)
		row.Email = *p.Email
		r.byEmail[row.Email] = row
	}

	if p.PasswordHash != nil {
		row.PasswordHash = *p.PasswordHash
	}
	if p.PasswordResetRequired != nil {
		row.PasswordResetRequired = *p.PasswordResetRequired
	}
	if p.Status != nil {
		row.Status = *p.Status
	}
	row.UpdatedAt = p.UpdatedAt
	row.Version++

	return cloneUser(row), nil
}

// cloneUser creates a deep copy of a User to avoid sharing pointers across map entries.
func cloneUser(u *domain.User) *domain.User {
	return &domain.User{
		ID:                    u.ID,
		Username:              u.Username,
		Email:                 u.Email,
		PasswordHash:          u.PasswordHash,
		PasswordResetRequired: u.PasswordResetRequired,
		Status:                u.Status,
		CreationSource:        u.CreationSource,
		CreatedAt:             u.CreatedAt,
		UpdatedAt:             u.UpdatedAt,
		Version:               u.Version,
	}
}

func (r *UserRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	u, ok := r.byID[id]
	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", id)))
	}
	delete(r.byName, u.Username)
	delete(r.byEmail, u.Email)
	delete(r.byID, id)
	return nil
}
