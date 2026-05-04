package mem

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var _ ports.SessionRepository = (*SessionRepository)(nil)

// SessionRepository is an in-memory implementation of ports.SessionRepository.
type SessionRepository struct {
	mu    sync.RWMutex
	byID  map[string]*domain.Session
	clock clock.Clock
}

// NewSessionRepository creates an empty in-memory SessionRepository.
// clk is the clock used for timestamping revocations.
func NewSessionRepository(clk clock.Clock) *SessionRepository {
	clock.MustHaveClock(clk, "mem.NewSessionRepository")
	return &SessionRepository{
		byID:  make(map[string]*domain.Session),
		clock: clk,
	}
}

// Health returns nil for in-memory repositories (always available).
func (r *SessionRepository) Health(_ context.Context) error {
	return nil
}

func (r *SessionRepository) Create(_ context.Context, session *domain.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	clone := *session
	if clone.Version == 0 {
		clone.Version = 1
	}
	r.byID[session.ID] = &clone
	return nil
}

func (r *SessionRepository) GetByID(_ context.Context, id string) (*domain.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.byID[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found: "+id,
			errcode.WithCategory(errcode.CategoryDomain))
	}
	clone := *s
	return &clone, nil
}

func (r *SessionRepository) Update(_ context.Context, session *domain.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	old, ok := r.byID[session.ID]
	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found: "+session.ID,
			errcode.WithCategory(errcode.CategoryDomain))
	}

	// Optimistic lock: reject if version mismatch.
	if session.Version != old.Version {
		return errcode.New(errcode.KindConflict, errcode.ErrSessionConflict,
			"session was modified by another request, please retry",
			errcode.WithInternal(fmt.Sprintf("version conflict: expected %d, got %d", old.Version, session.Version)),
			errcode.WithCategory(errcode.CategoryDomain))
	}

	session.Version++
	clone := *session
	r.byID[session.ID] = &clone
	return nil
}

func (r *SessionRepository) RevokeByIDAndOwner(_ context.Context, id, ownerUserID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.byID[id]
	if !ok || s.UserID != ownerUserID {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found: "+id,
			errcode.WithCategory(errcode.CategoryDomain))
	}
	s.Revoke(r.clock.Now())
	return nil
}

func (r *SessionRepository) RevokeByUserID(_ context.Context, userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.clock.Now()
	for _, s := range r.byID {
		if s.UserID == userID {
			s.Revoke(now)
		}
	}
	return nil
}

func (r *SessionRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, ok := r.byID[id]
	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found: "+id,
			errcode.WithCategory(errcode.CategoryDomain))
	}
	delete(r.byID, id)
	return nil
}
