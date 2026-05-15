// Red fixture for SERVICEOWNED-HANDLER-OWNER-CHECK-01: missing owner-guard.
// This service fetches the session and immediately revokes it without checking
// that the caller owns the session — an IDOR vulnerability.
//
// This file is in testdata/ and is excluded from normal Go builds;
// the archtest rule reads it via go/parser.ParseFile directly.
package serviceownedguardfixture

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// RedMissingGuardService demonstrates the MISSING owner-guard pattern that
// violates SERVICEOWNED-HANDLER-OWNER-CHECK-01.
type RedMissingGuardService struct {
	store sessionStore
}

// Logout is the RED form: NO owner-guard — fetches then immediately revokes.
// Any authenticated caller can revoke any session by ID (IDOR).
func (s *RedMissingGuardService) Logout(ctx context.Context, sessionID, _ string) error {
	_, err := s.store.Get(ctx, sessionID)
	if err != nil {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
	}
	// BUG: no SubjectID != callerUserID guard here
	return s.store.Revoke(ctx, sessionID)
}
