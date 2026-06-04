// Package revoke_intx_set_red is a RED fixture for
// CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01: Revoke calls s.cache.Set in the tx
// body (NOT inside a persistence.RegisterAfterCommit hook). Symmetric with the
// Delete case — any in-tx cache write races with re-population. The archtest
// must detect ≥ 1 violation.
package revoke_intx_set_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

type fakeCache struct{}

func (fakeCache) Delete(context.Context, string) error { return nil }
func (fakeCache) Set(context.Context, string) error    { return nil }

type CachingSessionStore struct {
	inner session.Store
	cache fakeCache
}

// Revoke writes to the cache in the tx body — VIOLATION.
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	_ = s.cache.Set(ctx, id) // VIOLATION: cache mutation outside an after-commit hook
	return s.inner.Revoke(ctx, id)
}

// RevokeForSubject is conformant.
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent, tok credentialfence.FenceToken) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event, tok)
}
