// Package revoke_nodelegate_red is a RED fixture for
// CACHING-SESSION-REVOKE-AFTERCOMMIT-DEL-01 check (A): Revoke does not delegate
// to s.inner.Revoke, so the revoke never reaches the system of record. The
// archtest must detect ≥ 1 violation.
package revoke_nodelegate_red

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

// Revoke omits the inner delegate — VIOLATION (check A).
func (s *CachingSessionStore) Revoke(_ context.Context, _ string) error {
	return nil // VIOLATION: no s.inner.Revoke delegation
}

// RevokeForSubject is conformant.
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent, tok credentialfence.FenceToken) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event, tok)
}
