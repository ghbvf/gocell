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
// Approved exception to the CLAUDE.md "no bare errors.New across package
// boundaries" rule: cross-package errors.Is matching requires a stable
// pointer identity, and wrapping in *errcode.Error would force every
// caller to Unwrap before running errors.Is. Callers distinguish replay
// (ErrNonceReused in the chain → 401) from store infrastructure failure
// (any other Cause → 500) via errors.Is on this exact sentinel; see
// writeServiceTokenError in servicetoken.go. The HTTP error envelope is
// constructed at the middleware layer.
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
// maxAge must be at least ServiceTokenMaxAge; a shorter value reintroduces the
// replay window the store is designed to close, and is rejected with an error.
func NewInMemoryNonceStore(maxAge time.Duration, opts ...InMemoryNonceOption) (*InMemoryNonceStore, error) {
	if maxAge <= 0 {
		return nil, fmt.Errorf("auth: nonce store maxAge must be positive, got %v", maxAge)
	}
	if maxAge < ServiceTokenMaxAge {
		return nil, fmt.Errorf("auth: nonce store maxAge %v is shorter than ServiceTokenMaxAge %v; a shorter TTL reintroduces the replay window",
			maxAge, ServiceTokenMaxAge)
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

// MaxAge reports the configured nonce retention window.
// Exposed for startup validation (assert store outlives ServiceTokenMaxAge)
// and integration tests.
func (s *InMemoryNonceStore) MaxAge() time.Duration { return s.maxAge }

// ErrNonceStoreFull is returned by InMemoryNonceStore.CheckAndMark when the
// nonce map has reached maxEntries and a forced prune found no expired entries
// to reclaim. Callers should treat this as a transient infrastructure failure
// (503 / Requeue) rather than a replay signal.
var ErrNonceStoreFull = errors.New("auth: nonce store is full; no expired entries to reclaim")

// CheckAndMark checks whether nonce has been seen within its TTL window. If
// not, it records the nonce and returns nil. If the nonce is still live,
// ErrNonceReused is returned. If the store is at capacity and no entries can
// be reclaimed, ErrNonceStoreFull is returned.
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
		// If still at or above cap after pruning, reject to prevent unbounded growth.
		if len(s.seen) >= s.maxEntries {
			return ErrNonceStoreFull
		}
	}

	if exp, exists := s.seen[nonce]; exists && now.Before(exp) {
		return ErrNonceReused
	}

	s.seen[nonce] = now.Add(s.maxAge)
	return nil
}
