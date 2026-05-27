// Package red_missing_check_owner is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B1: no auth.CheckOwner
// call at all. Also no raw KindNotFound errcode.New (so B3 stays silent
// and the failure is unambiguously B1).
package red_missing_check_owner

import (
	"context"
)

type session struct{ SubjectID string }

type store interface {
	Get(ctx context.Context, id string) (*session, error)
	Revoke(ctx context.Context, id string) error
}

// Service demonstrates the MISSING auth.CheckOwner funnel call.
type Service struct{ s store }

// Logout is the RED form: no auth.CheckOwner call, no ownership decision.
// Any authenticated caller can revoke any session by ID (IDOR).
func (svc *Service) Logout(ctx context.Context, sessionID, _ string) error {
	_, err := svc.s.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	// BUG: missing auth.CheckOwner — should funnel ownership decision
	return svc.s.Revoke(ctx, sessionID)
}
