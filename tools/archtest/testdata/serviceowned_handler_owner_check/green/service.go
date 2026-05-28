// Package green is a GREEN fixture for SERVICEOWNED-HANDLER-OWNER-CHECK-01.
// It uses the auth.CheckOwner funnel for ownership decisions; no raw
// errcode.New(KindNotFound, ...) appears in this file.
package green

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

// Service demonstrates the GREEN auth.CheckOwner funnel form.
type Service struct{ s store }

// Logout funnels the ownership decision through auth.CheckOwner.
func (svc *Service) Logout(ctx context.Context, sessionID, callerUserID string) error {
	sess, err := svc.s.Get(ctx, sessionID)
	if err != nil && errcode.IsInfraError(err) {
		return errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthLogoutUnavailable,
			"session lookup unavailable", err)
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
