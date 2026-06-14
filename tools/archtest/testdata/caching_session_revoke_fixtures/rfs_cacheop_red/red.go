// Package rfs_cacheop_red is a RED fixture for
// CACHING-SESSION-REVOKE-DELEGATE-ONLY-01: RevokeForSubject touches the cache.
// RevokeForSubject must do NO cache op (the epoch bump is its invalidation). The
// archtest must detect ≥ 1 violation.
package rfs_cacheop_red

import (
	"context"

	"github.com/ghbvf/gocell/framework/runtime/auth/credentialfence"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
)

type fakeCache struct{}

func (fakeCache) Delete(context.Context, string) error { return nil }
func (fakeCache) Set(context.Context, string) error    { return nil }

type CachingSessionStore struct {
	inner session.Store
	cache fakeCache
}

// Revoke is conformant (pure delegate).
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	return s.inner.Revoke(ctx, id)
}

// RevokeForSubject touches the cache — VIOLATION (delegate-only requires none).
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent, tok credentialfence.FenceToken) error {
	_ = s.cache.Delete(ctx, subjectID) // VIOLATION: cache op in a delegate-only method
	return s.inner.RevokeForSubject(ctx, subjectID, event, tok)
}
