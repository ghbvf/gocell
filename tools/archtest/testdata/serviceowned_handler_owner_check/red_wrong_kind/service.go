// Package red_wrong_kind is a RED fixture for SERVICEOWNED-HANDLER-OWNER-CHECK-01.
// The owner-guard IS present but uses KindPermissionDenied (403) instead of
// KindNotFound (404). Returning 403 leaks session existence to unauthorized
// callers, enabling cross-user session enumeration (IDOR regression).
package red_wrong_kind

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

type session struct{ SubjectID string }

type store interface {
	Get(ctx context.Context, id string) (session, error)
	Revoke(ctx context.Context, id string) error
}

// Service demonstrates an owner-guard that returns the WRONG Kind.
type Service struct{ s store }

// Logout is the RED form: owner-guard present but returns KindPermissionDenied
// (403 Forbidden) instead of KindNotFound (404). This leaks session existence.
func (svc *Service) Logout(ctx context.Context, sessionID, callerUserID string) error {
	sess, err := svc.s.Get(ctx, sessionID)
	if err != nil {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
	}
	if sess.SubjectID != callerUserID {
		// BUG: KindPermissionDenied (403) leaks existence — must be KindNotFound (404)
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "access denied")
	}
	return svc.s.Revoke(ctx, sessionID)
}
