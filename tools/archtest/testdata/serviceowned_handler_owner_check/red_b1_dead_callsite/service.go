// Package red_b1_dead_callsite is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B1 (call-graph
// reachability): auth.CheckOwner appears at file scope inside a value
// expression (`var _ = func() { ... }`) that is never invoked from the
// exported entry method. Bare AST existence would pass; reachability
// from an exported FuncDecl is required. B1 must fire.
package red_b1_dead_callsite

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

type session struct{ SubjectID string }

type store interface {
	Get(ctx context.Context, id string) (*session, error)
	Revoke(ctx context.Context, id string) error
}

// dead-code CheckOwner — file scope, never invoked from any FuncDecl.
//
//nolint:unused // intentional dead code for B1 reachability fixture
var _ = func() error {
	return auth.CheckOwner((*session)(nil),
		func(s *session) string { return "" },
		"placeholder", errcode.ErrSessionNotFound)
}

// Service exposes Logout as the only exported entry; Logout omits the
// owner-guard call, so the BFS closure from exported entries does not
// reach auth.CheckOwner — B1 fires.
type Service struct{ s store }

// Logout is the IDOR-vulnerable form: no auth.CheckOwner call.
func (svc *Service) Logout(ctx context.Context, sessionID, _ string) error {
	return svc.s.Revoke(ctx, sessionID)
}
