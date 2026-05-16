// Package red_missing_guard is a RED fixture for SERVICEOWNED-HANDLER-OWNER-CHECK-01.
// This service fetches the session and immediately revokes it without checking
// ownership — an IDOR vulnerability (any authenticated caller can revoke any session).
package red_missing_guard

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

type session struct{ SubjectID string }

type store interface {
	Get(ctx context.Context, id string) (session, error)
	Revoke(ctx context.Context, id string) error
}

// Service demonstrates the MISSING owner-guard form.
type Service struct{ s store }

// Logout is the RED form: no SubjectID != callerUserID guard.
// Any authenticated caller can revoke any session by ID.
func (svc *Service) Logout(ctx context.Context, sessionID, _ string) error {
	_, err := svc.s.Get(ctx, sessionID)
	if err != nil {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
	}
	// BUG: missing owner-guard — should compare SubjectID against callerUserID
	return svc.s.Revoke(ctx, sessionID)
}
