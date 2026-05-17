// Package f2_cache_delete_red is a RED fixture for CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
// Revoke body calls cache.Delete before delegating — violates the pure-delegate contract.
// The archtest must detect ≥ 1 violation in this file.
package f2_cache_delete_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// fakeCache has a Delete method to make the fixture compile.
type fakeCache struct{}

func (fakeCache) Delete(_ context.Context, _ string) error { return nil }

// CachingSessionStore mimics the real struct shape.
type CachingSessionStore struct {
	inner session.Store
	cache fakeCache
}

// Revoke calls cache.Delete — violates CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	_ = s.cache.Delete(ctx, id) // VIOLATION: touches cache before inner delegate
	return s.inner.Revoke(ctx, id)
}

// RevokeForSubject is conformant.
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event)
}
