// Package memstore provides an in-memory implementation of refresh.Store.
//
// This implementation is the oracle for the storetest contract suite and serves
// as a unit-test double for services that depend on refresh.Store. It is NOT
// suitable for production use — state is not persisted across process restarts.
//
// The full implementation will be delivered in B3. All methods currently return
// stub values that intentionally fail the contract suite (TDD red phase).
package memstore

import (
	"context"
	"io"
	"time"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// store is the in-memory implementation of refresh.Store.
type store struct {
	policy refresh.Policy
	clock  refresh.Clock
	rand   io.Reader
	// TODO B3: internal indexes
	//   tokens   map[string]*tokenRecord  // by active token ID
	//   obsolete map[string]*tokenRecord  // by obsolete token ID
	//   sessions map[string][]*tokenRecord // by session ID
	//   mu       sync.RWMutex
}

// New constructs an in-memory refresh.Store.
//
// If rand is nil, crypto/rand.Reader is used. clock must not be nil; use
// storetest.NewFakeClock for deterministic tests or a realClock wrapper for
// production-equivalent tests.
//
// Intended for:
//   - unit tests of services that depend on refresh.Store
//   - the storetest contract suite oracle (B3 will turn the suite green)
func New(policy refresh.Policy, clock refresh.Clock, rand io.Reader) refresh.Store {
	return &store{policy: policy, clock: clock, rand: rand}
}

// Issue creates a new refresh chain. TODO B3: implement.
func (s *store) Issue(_ context.Context, _, _ string) (*refresh.Token, error) {
	return nil, nil // TODO B3
}

// Rotate advances the chain one generation. TODO B3: implement.
func (s *store) Rotate(_ context.Context, _ string) (*refresh.Token, error) {
	return nil, nil // TODO B3
}

// Revoke marks all session tokens as revoked. TODO B3: implement.
func (s *store) Revoke(_ context.Context, _ string) error {
	return nil // TODO B3
}

// GC removes expired or revoked tokens. TODO B3: implement.
func (s *store) GC(_ context.Context, _ time.Time) (int, error) {
	return 0, nil // TODO B3
}
