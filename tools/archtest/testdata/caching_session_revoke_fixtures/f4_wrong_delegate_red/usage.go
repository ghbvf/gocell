// Package f4_wrong_delegate_red is a RED fixture for CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
// Revoke delegates to a different method name (RevokeForSubject) instead of Revoke —
// violates the same-method-name delegate requirement.
// The archtest must detect ≥ 1 violation in this file.
package f4_wrong_delegate_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// CachingSessionStore mimics the real struct shape.
type CachingSessionStore struct {
	inner session.Store
}

// Revoke delegates to the wrong method (RevokeForSubject) — violates CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	return s.inner.RevokeForSubject(ctx, id, session.CredentialEventLock) // VIOLATION: wrong method name
}

// RevokeForSubject is conformant.
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event)
}
