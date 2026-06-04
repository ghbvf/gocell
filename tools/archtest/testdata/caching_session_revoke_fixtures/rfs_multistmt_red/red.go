// Package rfs_multistmt_red is a RED fixture for
// CACHING-SESSION-REVOKE-DELEGATE-ONLY-01: RevokeForSubject has > 1 statement
// (it must be a single pure delegate). The archtest must detect ≥ 1 violation.
package rfs_multistmt_red

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

// Revoke is conformant (pure delegate, no cache op).
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	return s.inner.Revoke(ctx, id)
}

// RevokeForSubject has an extra statement — VIOLATION (not a pure delegate).
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent, tok credentialfence.FenceToken) error {
	_ = subjectID // VIOLATION: extra statement before the delegate
	return s.inner.RevokeForSubject(ctx, subjectID, event, tok)
}
