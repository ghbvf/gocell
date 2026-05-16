//go:build archtest_fixture

// Package refreshinvariantsfixture contains an intentionally-violating
// Refresh method that exercises the type-aware detector in
// refresh_invariants_test.go (REFRESH-CROSS-STORE-TX-01).
//
// Gated by the archtest_fixture build tag; production builds never see this
// file. The fixture is loaded by TestRefreshCrossStoreTX01_RedFixtureDetected
// via typeseval.SharedResolver with tags=[]string{"archtest_fixture"}.
//
// # Violation shapes
//
// The fixture's Refresh method:
//   - Calls s.txRunner.RunInTx(ctx, func(...){...}) exactly once (rule
//     structural prerequisite).
//   - Inside the closure makes a method call on s (rule prerequisite that
//     the closure is not empty).
//   - OUTSIDE the closure makes two banned calls that must be guarded:
//     1. s.refreshStore.Peek(ctx, token)   — caught by Soft AND Medium
//     2. s.sessionStore.Get(ctx, sid)      — caught only by Medium (Soft
//     rule's stale guardedCalls map listed "sessionRepo.GetByID", which
//     no longer exists in production code post-PR #482)
//
// The receiver field types match production sessionrefresh.Service so the
// type-aware ResolveMethodCall path returns the right *types.Func; the rule
// validates fn.Pkg().Path() against runtime/auth/{session,refresh} +
// cells/accesscore/internal/ports.
package refreshinvariantsfixture

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// txRunner is a fixture-local interface that mirrors the shape the rule
// looks for: s.txRunner.RunInTx(ctx, func(ctx context.Context) error).
type txRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Service mirrors cells/accesscore/slices/sessionrefresh.Service field
// names and types so the rule's bare-receiver-field match (`s.sessionStore`
// etc.) lines up. Method receiver `s` matches the rule's hardcoded
// receiver identifier.
type Service struct {
	sessionStore session.Store
	refreshStore refresh.Store
	txRunner     txRunner
}

// Refresh deliberately violates REFRESH-CROSS-STORE-TX-01 by calling
// guarded methods outside the RunInTx closure.
func (s *Service) Refresh(ctx context.Context, token string) error {
	// VIOLATION 1: refreshStore.Peek outside closure (Soft + Medium both catch).
	_, _ = s.refreshStore.Peek(ctx, token)

	// VIOLATION 2: sessionStore.Get outside closure (only Medium catches —
	// Soft rule's guardedCalls map listed stale sessionRepo.GetByID).
	_, _ = s.sessionStore.Get(ctx, "")

	return s.txRunner.RunInTx(ctx, func(ctx context.Context) error {
		// Required: ≥ 1 method call on s inside the closure so the rule
		// considers it non-trivial. This call itself is NOT a violation
		// because it lives inside the closure.
		_, _, _ = s.refreshStore.Rotate(ctx, token)
		return nil
	})
}
