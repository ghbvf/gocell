package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrNonceReused is returned by NonceStore.CheckAndMark when a nonce has
// already been consumed within its TTL window.
var ErrNonceReused = errors.New("auth: nonce already used")

// NonceStore tracks nonces for replay prevention. Implementations must be
// safe for concurrent use.
type NonceStore interface {
	CheckAndMark(ctx context.Context, nonce string) error
}

// InMemoryNonceStore is a NonceStore backed by a map with lazy expiry pruning.
// Suitable for single-instance deployments.
type InMemoryNonceStore struct {
	mu     sync.Mutex
	seen   map[string]time.Time // nonce → expiry
	maxAge time.Duration
	now    func() time.Time
}

// InMemoryNonceOption configures an InMemoryNonceStore.
type InMemoryNonceOption func(*InMemoryNonceStore)

// WithNonceClock overrides the time source (for testing).
func WithNonceClock(fn func() time.Time) InMemoryNonceOption {
	return func(s *InMemoryNonceStore) { s.now = fn }
}

// NewInMemoryNonceStore creates an InMemoryNonceStore with the given maxAge.
func NewInMemoryNonceStore(maxAge time.Duration, opts ...InMemoryNonceOption) *InMemoryNonceStore {
	s := &InMemoryNonceStore{
		seen:   make(map[string]time.Time),
		maxAge: maxAge,
		now:    time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CheckAndMark checks whether nonce has been seen within its TTL window. If
// not, it records the nonce and returns nil. If the nonce is still live,
// ErrNonceReused is returned.
func (s *InMemoryNonceStore) CheckAndMark(_ context.Context, nonce string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	// Lazy prune when map grows large.
	if len(s.seen) > 1000 {
		for k, exp := range s.seen {
			if now.After(exp) {
				delete(s.seen, k)
			}
		}
	}

	if exp, exists := s.seen[nonce]; exists && now.Before(exp) {
		return ErrNonceReused
	}

	s.seen[nonce] = now.Add(s.maxAge)
	return nil
}
