// Package red_b1_unmapped_entry is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B1 (handler-adapter
// resolved entry binding): Service.Logout DOES call auth.CheckOwner,
// but handler.go's Adapter.Delete invokes Service.SomeOtherOp instead.
// The contract's real execution path goes through SomeOtherOp (which
// has no CheckOwner), making CheckOwner unreachable from the resolved
// entry set. AST-existence-only check would pass; entry-bound BFS fires.
package red_b1_unmapped_entry

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

type session struct{ SubjectID string }

type store interface {
	Get(ctx context.Context, id string) (*session, error)
	Revoke(ctx context.Context, id string) error
}

type Service struct{ s store }

// SomeOtherOp is the method actually invoked by Adapter.Delete (see
// handler.go). It does NOT call auth.CheckOwner, so the contract's
// real execution path bypasses ownership enforcement.
func (svc *Service) SomeOtherOp(ctx context.Context, sessionID string) error {
	return svc.s.Revoke(ctx, sessionID)
}

// Logout has the auth.CheckOwner wiring but is NOT invoked by the
// handler-adapter — it's dead from the contract's entry perspective.
func (svc *Service) Logout(ctx context.Context, sessionID, callerUserID string) error {
	sess, err := svc.s.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := auth.CheckOwner(sess, func(s *session) string {
		if s == nil {
			return ""
		}
		return s.SubjectID
	}, callerUserID, errcode.ErrSessionNotFound); err != nil {
		return err
	}
	return svc.s.Revoke(ctx, sessionID)
}
