// Package f7_cache_in_arg_red is a RED fixture for CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
// Revoke body delegates with a CallExpr as an argument (id derived from cache lookup) —
// violates the args-ident invariant: every argument must be a plain parameter ident,
// not a derived or computed expression.
// The archtest must detect ≥ 1 violation in this file.
package f7_cache_in_arg_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// CachingSessionStore mimics the real struct shape so the receiver-type check works.
type CachingSessionStore struct {
	inner session.Store
	cache interface{ Lookup(string) string }
}

// idFromCache is a helper that simulates deriving an id from a cache lookup.
func idFromCache(c interface{ Lookup(string) string }, id string) string {
	if resolved := c.Lookup(id); resolved != "" {
		return resolved
	}
	return id
}

// Revoke passes a CallExpr as argument instead of the raw parameter ident —
// violates CACHING-SESSION-REVOKE-DELEGATE-ONLY-01 (arg is not a plain ident).
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	return s.inner.Revoke(ctx, idFromCache(s.cache, id)) // VIOLATION: arg is CallExpr, not plain ident
}

// RevokeForSubject is conformant (single return delegate).
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event)
}
