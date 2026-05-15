// Green fixture for SERVICEOWNED-HANDLER-OWNER-CHECK-01.
// Represents the IDOR-safe owner-guard form present in
// cells/accesscore/slices/sessionlogout/service.go.
//
// This file is in testdata/ and is excluded from normal Go builds;
// the archtest rule reads it via go/parser.ParseFile directly.
package serviceownedguardfixture

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// session is a minimal stub to allow the owner-guard pattern to compile
// if ever loaded by a type-aware loader.
type session struct {
	SubjectID string
}

// sessionStore stub interface.
type sessionStore interface {
	Get(ctx context.Context, id string) (session, error)
	Revoke(ctx context.Context, id string) error
}

// GreenService demonstrates the owner-guard pattern that satisfies
// SERVICEOWNED-HANDLER-OWNER-CHECK-01. The service fetches the resource,
// compares the owner field against the caller identity, and returns
// errcode.KindNotFound on mismatch (IDOR-safe 404-collapse).
type GreenService struct {
	store sessionStore
}

// Logout is the GREEN target form: owner-guard present, KindNotFound used.
func (s *GreenService) Logout(ctx context.Context, sessionID, callerUserID string) error {
	sess, err := s.store.Get(ctx, sessionID)
	if err != nil {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
	}
	if sess.SubjectID != callerUserID {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
	}
	return s.store.Revoke(ctx, sessionID)
}
