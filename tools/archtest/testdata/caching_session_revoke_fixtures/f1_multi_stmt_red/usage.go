// Package f1_multi_stmt_red is a RED fixture for CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
// Revoke body has >1 statement: a log call before the delegate.
// The archtest must detect ≥ 1 violation in this file.
package f1_multi_stmt_red

import (
	"context"
	"log"

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

// Revoke has >1 statement — violates CACHING-SESSION-REVOKE-DELEGATE-ONLY-01.
func (s *CachingSessionStore) Revoke(ctx context.Context, id string) error {
	log.Print("extra statement") // VIOLATION: not a pure delegate
	return s.inner.Revoke(ctx, id)
}

// RevokeForSubject is conformant (single return delegate) — archtest must NOT flag this.
func (s *CachingSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	return s.inner.RevokeForSubject(ctx, subjectID, event)
}
