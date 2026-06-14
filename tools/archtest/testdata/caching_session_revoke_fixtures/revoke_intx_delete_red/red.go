// Package revoke_intx_delete_red is a RED fixture for
// CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01: Revoke calls s.cache.Delete in the
// transaction body (NOT inside a persistence.RegisterAfterCommit hook) — the
// 2×TTL re-population race. The archtest must detect ≥ 1 violation.
package revoke_intx_delete_red

import (
	"context"

	"github.com/ghbvf/gocell/framework/runtime/auth/credentialfence"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
)

type fakeCache struct{}

func (fakeCache) Delete(context.Context, string) error { return nil }
func (fakeCache) Set(context.Context, string) error    { return nil }

// CachingSessionStore mimics the real struct shape (inner + cache fields).
type CachingSessionStore struct {
	inner session.Store
	cache fakeCache
}

// Revoke deletes from the cache in the tx body — VIOLATION (in-tx mutation).
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	_ = s.cache.Delete(ctx, id) // VIOLATION: cache mutation outside an after-commit hook
	return s.inner.Revoke(ctx, id)
}

// RevokeForSubject is conformant (single-statement delegate).
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent, tok credentialfence.FenceToken) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event, tok)
}
