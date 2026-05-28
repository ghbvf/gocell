// Package red_b3_wrap_kindnotfound is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B3 (zero-tolerance ban):
// the service.go uses errcode.Wrap (not errcode.New) with
// errcode.KindNotFound to construct an IDOR-collapse envelope. B3 must
// detect both Kind-bearing constructors; New-only detection would miss
// this entirely.
package red_b3_wrap_kindnotfound

import (
	"context"
	"errors"
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

type session struct{ SubjectID string }

type store interface {
	Get(ctx context.Context, id string) (*session, error)
	Revoke(ctx context.Context, id string) error
}

type Service struct{ s store }

// Logout uses errcode.Wrap (not errcode.New) for the not-found path —
// same Kind, different constructor. B3 must report this as a raw
// KindNotFound construction outside the CheckOwner funnel.
func (svc *Service) Logout(ctx context.Context, sessionID, callerUserID string) error {
	sess, err := svc.s.Get(ctx, sessionID)
	if err != nil {
		// BUG: errcode.Wrap with KindNotFound bypasses the funnel
		return errcode.Wrap(errcode.KindNotFound, errcode.ErrSessionNotFound,
			"session not found", fmt.Errorf("get: %w", errors.New("inner")))
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
