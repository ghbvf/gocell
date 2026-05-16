// Package green is a GREEN fixture for SERVICEOWNED-HANDLER-OWNER-CHECK-01.
// Represents the IDOR-safe owner-guard form present in
// cells/accesscore/slices/sessionlogout/service.go.
//
// The rule expects: IfStmt with != condition (neither side nil) and body
// returning errcode.New(errcode.KindNotFound, ...).
package green

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

type session struct{ SubjectID string }

type store interface {
	Get(ctx context.Context, id string) (session, error)
	Revoke(ctx context.Context, id string) error
}

// Service demonstrates the GREEN owner-guard form.
type Service struct{ s store }

// Logout contains the canonical owner-guard: fetches session, compares
// SubjectID against callerUserID, returns KindNotFound on mismatch.
func (svc *Service) Logout(ctx context.Context, sessionID, callerUserID string) error {
	sess, err := svc.s.Get(ctx, sessionID)
	if err != nil {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
	}
	if sess.SubjectID != callerUserID {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
	}
	return svc.s.Revoke(ctx, sessionID)
}
