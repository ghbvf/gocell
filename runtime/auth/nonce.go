package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNonceReused is returned by NonceStore.CheckAndMark when a nonce has
// already been consumed within its TTL window.
//
// This uses errors.New (not errcode) because it is an internal sentinel
// for errors.Is matching. The HTTP error code is set at the middleware layer.
var ErrNonceReused = errors.New("auth: nonce already used")

// defaultMaxNonceEntries is the maximum number of live nonce entries before a
// forced prune is triggered in InMemoryNonceStore.CheckAndMark.
const defaultMaxNonceEntries = 100000

// ref: none — no direct framework analog for HMAC nonce store; adopted
// standard sync.Mutex + map-with-TTL pattern (cf. gorilla/securecookie token store).

// NonceStoreKind classifies a NonceStore implementation for startup validation.
// Production deployments must reject NonceStoreKindNoop — the /internal/v1/*
// service-token guard needs a replay-safe store, not a permissive one.
type NonceStoreKind string

const (
	// NonceStoreKindNoop is the explicit disable-replay-check sentinel.
	// Rejected in adapter mode "real" by cmd/corebundle.SharedDeps.Validate.
	NonceStoreKindNoop NonceStoreKind = "noop"
	// NonceStoreKindInMemory is the single-process map-backed implementation.
	// Suitable for single-pod deployments; a shared store is required for
	// multi-pod replay protection.
	NonceStoreKindInMemory NonceStoreKind = "in_memory"
	// NonceStoreKindDistributed is reserved for shared backends (Redis, consul,
	// etc.). Production multi-pod deployments must use this kind.
	NonceStoreKindDistributed NonceStoreKind = "distributed"
)

// NonceStore tracks nonces for replay prevention. Implementations must be
// safe for concurrent use. The store must retain nonces for at least
// ServiceTokenMaxAge (5 minutes) to prevent replay within the token
// validity window; a shorter TTL creates a replay vulnerability.
//
// Kind reports the implementation classification for startup validation.
// Control-plane guards in adapter mode "real" must reject NonceStoreKindNoop
// so replay protection is never silently disabled in production.
type NonceStore interface {
	CheckAndMark(ctx context.Context, nonce string) error
	Kind() NonceStoreKind
}

// NoopNonceStore is an explicit null-object implementation of NonceStore that
// always permits a nonce. It exists so that callers never carry a nil
// NonceStore through the authenticator pipeline — every code path operates on
// a non-nil implementation, and dev-mode opt-out is explicit rather than
// accidental.
//
// cmd/corebundle.SharedDeps.Validate rejects this implementation in adapter
// mode "real" (see errcode.ErrControlplaneNonceStoreMissing).
type NoopNonceStore struct{}

// NewNoopNonceStore returns the sentinel NoopNonceStore value.
func NewNoopNonceStore() NoopNonceStore { return NoopNonceStore{} }

// CheckAndMark always returns nil — replay detection is disabled.
func (NoopNonceStore) CheckAndMark(context.Context, string) error { return nil }

// Kind reports NonceStoreKindNoop.
func (NoopNonceStore) Kind() NonceStoreKind { return NonceStoreKindNoop }

// InMemoryNonceStore is a NonceStore backed by a map with lazy expiry pruning.
// Suitable for single-instance deployments.
type InMemoryNonceStore struct {
	mu         sync.Mutex
	seen       map[string]time.Time // nonce → expiry
	maxAge     time.Duration
	maxEntries int
	now        func() time.Time
}

// InMemoryNonceOption configures an InMemoryNonceStore.
type InMemoryNonceOption func(*InMemoryNonceStore)

// WithNonceClock overrides the time source (for testing).
func WithNonceClock(fn func() time.Time) InMemoryNonceOption {
	return func(s *InMemoryNonceStore) { s.now = fn }
}

// WithMaxNonceEntries overrides the maximum number of live nonce entries before
// a forced prune is triggered. The default is defaultMaxNonceEntries.
func WithMaxNonceEntries(n int) InMemoryNonceOption {
	return func(s *InMemoryNonceStore) { s.maxEntries = n }
}

// NewInMemoryNonceStore creates an InMemoryNonceStore with the given maxAge.
// maxAge must be positive; a zero or negative value makes replay protection
// ineffective and is rejected with an error.
func NewInMemoryNonceStore(maxAge time.Duration, opts ...InMemoryNonceOption) (*InMemoryNonceStore, error) {
	if maxAge <= 0 {
		return nil, fmt.Errorf("auth: nonce store maxAge must be positive, got %v", maxAge)
	}
	s := &InMemoryNonceStore{
		seen:       make(map[string]time.Time),
		maxAge:     maxAge,
		maxEntries: defaultMaxNonceEntries,
		now:        time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Kind reports NonceStoreKindInMemory.
func (*InMemoryNonceStore) Kind() NonceStoreKind { return NonceStoreKindInMemory }

// CheckAndMark checks whether nonce has been seen within its TTL window. If
// not, it records the nonce and returns nil. If the nonce is still live,
// ErrNonceReused is returned.
func (s *InMemoryNonceStore) CheckAndMark(_ context.Context, nonce string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	// Lazy prune when map grows past threshold.
	if len(s.seen) >= s.maxEntries {
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
