// Red fixture for SERVICEOWNED-HANDLER-OWNER-CHECK-01: owner-guard present but
// returns KindPermissionDenied (403) instead of KindNotFound (404).
// Returning 403 leaks the existence of the session to unauthorized callers,
// enabling cross-user session enumeration (IDOR regression).
//
// This file is in testdata/ and is excluded from normal Go builds;
// the archtest rule reads it via go/parser.ParseFile directly.
package serviceownedguardfixture

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// RedWrongKindService demonstrates an owner-guard that uses the wrong error
// Kind, violating SERVICEOWNED-HANDLER-OWNER-CHECK-01.
type RedWrongKindService struct {
	store sessionStore
}

// Logout is the RED form: owner-guard IS present, but returns KindPermissionDenied
// (403 Forbidden) instead of KindNotFound (404) — this leaks the existence of the
// session to the unauthorized caller, enabling IDOR enumeration.
func (s *RedWrongKindService) Logout(ctx context.Context, sessionID, callerUserID string) error {
	sess, err := s.store.Get(ctx, sessionID)
	if err != nil {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
	}
	if sess.SubjectID != callerUserID {
		// BUG: should be KindNotFound to prevent IDOR enumeration
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "access denied")
	}
	return s.store.Revoke(ctx, sessionID)
}
