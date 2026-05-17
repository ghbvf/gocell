// Package f3_cache_set_red is a RED fixture for CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
// Revoke body calls cache.Set — a violation (body has >1 statement, and touches cache).
// The archtest must detect ≥ 1 violation in this file.
package f3_cache_set_red

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// fakeCache has a Set method to make the fixture compile.
type fakeCache struct{}

func (fakeCache) Set(_ context.Context, _, _ string, _ time.Duration) error { return nil }

// CachingSessionStore mimics the real struct shape.
type CachingSessionStore struct {
	inner session.Store
	cache fakeCache
}

// Revoke calls cache.Set — violates CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	_ = s.cache.Set(ctx, id, "tombstone", time.Second) // VIOLATION: writes to cache
	return s.inner.Revoke(ctx, id)
}

// RevokeForSubject is conformant.
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event)
}
