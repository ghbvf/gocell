// Package red_b1_unreachable_helper is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B1 (call-graph
// reachability): auth.CheckOwner is called from an unexported helper that
// no exported entry method invokes. AST-existence-only check would pass;
// reachability BFS from exported entries excludes this helper. B1 must
// fire.
package red_b1_unreachable_helper

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

type Service struct{ s store }

// unreachableOwnerCheck is unexported and never called by Logout (or any
// other exported method). The owner-guard wiring is missing in the actual
// execution path.
//
//nolint:unused // intentional dead method for B1 reachability fixture
func (svc *Service) unreachableOwnerCheck(callerID string) error {
	return auth.CheckOwner((*session)(nil),
		func(s *session) string {
			if s == nil {
				return ""
			}
			return s.SubjectID
		},
		callerID, errcode.ErrSessionNotFound)
}

// Logout is the exported entry but does NOT call unreachableOwnerCheck or
// auth.CheckOwner directly — BFS closure does not reach the funnel.
func (svc *Service) Logout(ctx context.Context, sessionID, _ string) error {
	return svc.s.Revoke(ctx, sessionID)
}
