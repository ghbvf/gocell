// Package rfs_wrongdelegate_red is a RED fixture for
// CACHING-SESSION-REVOKE-DELEGATE-ONLY-01: RevokeForSubject delegates to a
// differently-named inner method (s.inner.Revoke), which would silently route
// subject-wide revoke semantics to the single-session sink. The archtest must
// detect ≥ 1 violation.
package rfs_wrongdelegate_red

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

// RevokeForSubject delegates to the wrong inner method — VIOLATION
// (same-method-name invariant).
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, _ session.CredentialEvent, _ credentialfence.FenceToken) error {
	return s.inner.Revoke(ctx, subjectID) // VIOLATION: delegates to Revoke, not RevokeForSubject
}
