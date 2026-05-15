// Package fixture is the RED fixture for SESSIONREFRESH-STALE-EPOCH-REJECT-01.
// It represents the REGRESSED security model where the stale-epoch branch is
// incorrectly conflated with the reuse-attack path: rejectIfStaleEpoch calls
// handleReuseDetected and references CredentialEventRefreshReuse, and does NOT
// call cascadeRevoke. archtest prongs 3 and 4 must detect these violations.
//
// This file is NOT compiled by production; it is parsed only as AST data by
// TestSessionrefreshStaleEpochReject_RedFixtureDetected.
package fixture

import "context"

// CredentialEventRefreshReuse mirrors the production constant name; its
// presence in rejectIfStaleEpoch body is the prong-4 regression signal.
const CredentialEventRefreshReuse = "refresh_reuse"

// Service is a minimal stand-in for sessionrefresh.Service.
type Service struct{}

// Token mirrors the refresh store token shape.
type Token struct {
	AuthzEpochAtIssue int64
	SessionID         string
	SubjectID         string
}

// User mirrors the domain user shape.
type User struct{}

// AuthzEpoch is a method on User, mirroring production.
func (u *User) AuthzEpoch() int64 { return 0 }

// Session mirrors the session store row.
type Session struct {
	ID        string
	SubjectID string
}

// refreshInTx — the call site in the regressed model. It still calls
// rejectIfStaleEpoch and passes both epoch operands, so prong-1 passes.
// The regression is inside rejectIfStaleEpoch itself.
func (s *Service) refreshInTx(ctx context.Context, presented *Token, user *User, sess *Session) error {
	return s.rejectIfStaleEpoch(ctx, presented.AuthzEpochAtIssue, user.AuthzEpoch(), sess.ID, sess.SubjectID)
}

// rejectIfStaleEpoch — the REGRESSED form. It uses the wrong comparison
// operator (!=), routes into handleReuseDetected (conflating stale-epoch with
// reuse attack), directly references CredentialEventRefreshReuse, and does NOT
// call cascadeRevoke. This triggers prong-2, prong-3, and prong-4 violations.
func (s *Service) rejectIfStaleEpoch(ctx context.Context, rowEpoch, userEpoch int64, sessionID, subjectID string) error {
	if rowEpoch != userEpoch {
		// REGRESSED: stale-epoch incorrectly treated as reuse attack.
		// Should call cascadeRevoke("stale-epoch") instead.
		// Direct reference to CredentialEventRefreshReuse triggers prong-4 negative.
		_ = CredentialEventRefreshReuse
		return s.handleReuseDetected(ctx, subjectID, sessionID)
	}
	return nil
}

// handleReuseDetected — mirrors the production security cascade entry point.
func (s *Service) handleReuseDetected(ctx context.Context, subjectID, sessionID string) error {
	return nil
}
