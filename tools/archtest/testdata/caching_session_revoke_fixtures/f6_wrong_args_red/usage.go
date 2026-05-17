// Package f6_wrong_args_red is a RED fixture for CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
// Revoke body delegates with literal arguments instead of the method's own parameters —
// violates the args-ident invariant: every argument must be a plain parameter ident.
// The archtest must detect ≥ 1 violation in this file.
package f6_wrong_args_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// CachingSessionStore mimics the real struct shape so the receiver-type check works.
type CachingSessionStore struct {
	inner session.Store
}

// Revoke passes literal values instead of the incoming parameters —
// violates CACHING-SESSION-REVOKE-DELEGATE-ONLY-01 (args are not param idents).
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	return s.inner.Revoke(context.Background(), "") // VIOLATION: literal args, not param idents
}

// RevokeForSubject is conformant (single return delegate).
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event)
}
