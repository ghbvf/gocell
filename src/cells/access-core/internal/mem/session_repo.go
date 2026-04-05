package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var _ ports.SessionRepository = (*SessionRepository)(nil)

// SessionRepository is an in-memory implementation of ports.SessionRepository.
type SessionRepository struct {
	mu              sync.RWMutex
	byID            map[string]*domain.Session
	byRefresh       map[string]*domain.Session
	byPrevRefresh   map[string]*domain.Session
}

// NewSessionRepository creates an empty in-memory SessionRepository.
func NewSessionRepository() *SessionRepository {
	return &SessionRepository{
		byID:          make(map[string]*domain.Session),
		byRefresh:     make(map[string]*domain.Session),
		byPrevRefresh: make(map[string]*domain.Session),
	}
}

func (r *SessionRepository) Create(_ context.Context, session *domain.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	clone := *session
	r.byID[session.ID] = &clone
	r.byRefresh[session.RefreshToken] = &clone
	if session.PreviousRefreshToken != "" {
		r.byPrevRefresh[session.PreviousRefreshToken] = &clone
	}
	return nil
}

func (r *SessionRepository) GetByID(_ context.Context, id string) (*domain.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.byID[id]
	if !ok {
		return nil, errcode.New(errcode.ErrSessionNotFound, "session not found: "+id)
	}
	clone := *s
	return &clone, nil
}

func (r *SessionRepository) GetByRefreshToken(_ context.Context, token string) (*domain.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.byRefresh[token]
	if !ok {
		return nil, errcode.New(errcode.ErrSessionNotFound, "session not found by refresh token")
	}
	clone := *s
	return &clone, nil
}

func (r *SessionRepository) GetByPreviousRefreshToken(_ context.Context, token string) (*domain.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.byPrevRefresh[token]
	if !ok {
		return nil, errcode.New(errcode.ErrSessionNotFound, "session not found by previous refresh token")
	}
	clone := *s
	return &clone, nil
}

func (r *SessionRepository) Update(_ context.Context, session *domain.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	old, ok := r.byID[session.ID]
	if !ok {
		return errcode.New(errcode.ErrSessionNotFound, "session not found: "+session.ID)
	}

	// Remove old refresh-token index entry.
	delete(r.byRefresh, old.RefreshToken)
	// Remove old previous-refresh-token index entry.
	if old.PreviousRefreshToken != "" {
		delete(r.byPrevRefresh, old.PreviousRefreshToken)
	}

	clone := *session
	r.byID[session.ID] = &clone
	r.byRefresh[session.RefreshToken] = &clone
	if session.PreviousRefreshToken != "" {
		r.byPrevRefresh[session.PreviousRefreshToken] = &clone
	}
	return nil
}

func (r *SessionRepository) RevokeByUserID(_ context.Context, userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range r.byID {
		if s.UserID == userID {
			s.Revoke()
		}
	}
	return nil
}

func (r *SessionRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.byID[id]
	if !ok {
		return errcode.New(errcode.ErrSessionNotFound, "session not found: "+id)
	}
	delete(r.byRefresh, s.RefreshToken)
	if s.PreviousRefreshToken != "" {
		delete(r.byPrevRefresh, s.PreviousRefreshToken)
	}
	delete(r.byID, id)
	return nil
}
