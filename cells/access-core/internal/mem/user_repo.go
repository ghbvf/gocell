// Package mem provides in-memory repository implementations for access-core.
package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var _ ports.UserRepository = (*UserRepository)(nil)

// UserRepository is an in-memory implementation of ports.UserRepository.
type UserRepository struct {
	mu     sync.RWMutex
	byID   map[string]*domain.User
	byName map[string]*domain.User
}

// NewUserRepository creates an empty in-memory UserRepository.
func NewUserRepository() *UserRepository {
	return &UserRepository{
		byID:   make(map[string]*domain.User),
		byName: make(map[string]*domain.User),
	}
}

func (r *UserRepository) Create(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byName[user.Username]; exists {
		return errcode.New(errcode.ErrAuthUserDuplicate, "username already exists: "+user.Username)
	}

	c := cloneUser(user)
	r.byID[user.ID] = c
	r.byName[user.Username] = c
	return nil
}

func (r *UserRepository) GetByID(_ context.Context, id string) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	u, ok := r.byID[id]
	if !ok {
		return nil, errcode.New(errcode.ErrAuthUserNotFound, "user not found: "+id)
	}
	return cloneUser(u), nil
}

func (r *UserRepository) GetByUsername(_ context.Context, username string) (*domain.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	u, ok := r.byName[username]
	if !ok {
		return nil, errcode.New(errcode.ErrAuthUserNotFound, "user not found: "+username)
	}
	return cloneUser(u), nil
}

func (r *UserRepository) Update(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byID[user.ID]; !exists {
		return errcode.New(errcode.ErrAuthUserNotFound, "user not found: "+user.ID)
	}

	c := cloneUser(user)
	r.byID[user.ID] = c
	r.byName[user.Username] = c
	return nil
}

// cloneUser creates a deep copy of a User to avoid sharing pointers across map entries.
func cloneUser(u *domain.User) *domain.User {
	return &domain.User{
		ID:           u.ID,
		Username:     u.Username,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		Status:       u.Status,
		CreatedAt:    u.CreatedAt,
		UpdatedAt:    u.UpdatedAt,
	}
}

func (r *UserRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	u, ok := r.byID[id]
	if !ok {
		return errcode.New(errcode.ErrAuthUserNotFound, "user not found: "+id)
	}
	delete(r.byName, u.Username)
	delete(r.byID, id)
	return nil
}
