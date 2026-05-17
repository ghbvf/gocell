// Package f5_wrong_receiver_red is a RED fixture for CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
// Revoke body delegates via a different variable (not the method receiver) —
// violates the receiver-ident invariant: the callee's X must be the method's own receiver.
// The archtest must detect ≥ 1 violation in this file.
package f5_wrong_receiver_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// fakeInner satisfies session.Store (all methods needed to compile).
type fakeInner struct{}

func (fakeInner) Create(context.Context, *session.Session) error             { return nil }
func (fakeInner) Get(context.Context, string) (*session.ValidateView, error) { return nil, nil }
func (fakeInner) Revoke(context.Context, string) error                       { return nil }
func (fakeInner) RevokeForSubject(context.Context, string, session.CredentialEvent) error {
	return nil
}
func (fakeInner) RepoReady(context.Context) error { return nil }

// CachingSessionStore mimics the real struct shape so the receiver-type check works.
type CachingSessionStore struct {
	inner session.Store
}

// other is a package-level variable that lets the code compile while
// using a different receiver in the delegate call.
var other = &CachingSessionStore{inner: fakeInner{}}

// Revoke delegates via 'other' instead of the method receiver 's' —
// violates CACHING-SESSION-REVOKE-DELEGATE-ONLY-01 (wrong receiver ident).
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	return other.inner.Revoke(ctx, id) // VIOLATION: receiver is 'other', not 's'
}

// RevokeForSubject is conformant (single return delegate).
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event)
}
