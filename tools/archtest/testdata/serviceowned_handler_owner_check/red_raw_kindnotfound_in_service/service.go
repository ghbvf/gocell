// Package red_raw_kindnotfound_in_service is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B3 (zero-tolerance ban):
// auth.CheckOwner IS called (so B1 stays silent), but the file ALSO
// constructs a raw errcode.New(errcode.KindNotFound, ...) outside the
// funnel — exactly what B3 forbids.
package red_raw_kindnotfound_in_service

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

// Service demonstrates a B3 violation: CheckOwner is called, but a raw
// errcode.New(KindNotFound, ...) escapes the funnel.
type Service struct{ s store }

// Logout is the RED form: keeps the funnel call (passes B1) but also
// returns raw KindNotFound from the lookup-failure path (fails B3).
// The correct refactor collapses both paths through CheckOwner via a
// nil-safe accessor.
func (svc *Service) Logout(ctx context.Context, sessionID, callerUserID string) error {
	sess, err := svc.s.Get(ctx, sessionID)
	if err != nil {
		// BUG: raw errcode.New(KindNotFound, ...) — must collapse through CheckOwner
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
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
