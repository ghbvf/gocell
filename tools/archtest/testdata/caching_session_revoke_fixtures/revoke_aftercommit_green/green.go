// Package revoke_aftercommit_green is the GREEN fixture for
// CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01: the sanctioned production shape.
// Revoke delegates to s.inner.Revoke, then evicts the cache ONLY inside a
// persistence.RegisterAfterCommit hook (fires after the commit is durable, so no
// 2×TTL race). The archtest must detect ZERO violations.
package revoke_aftercommit_green

import (
	"context"

	"github.com/ghbvf/gocell/kernel/persistence"
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

// Revoke delegates, then schedules the cache DEL inside an after-commit hook —
// the sanctioned shape, MUST be 0 violations.
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	if err := s.inner.Revoke(ctx, id); err != nil {
		return err
	}
	persistence.RegisterAfterCommit(ctx, func(hookCtx context.Context) {
		_ = s.cache.Delete(hookCtx, id)
	})
	return nil
}

// RevokeForSubject is conformant (delegate-only).
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent, tok credentialfence.FenceToken) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event, tok)
}
