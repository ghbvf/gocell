// Package reconstitute_user_caller_red is a RED fixture for the
// RECONSTITUTE-USER-CALLER-01 archtest. It calls domain.ReconstituteUser
// from a path that is NOT in the production allowlist
// (mem/, adapters/postgres/, domain/), and is NOT a _test.go file.
// The archtest TestReconstituteUserCallerAllowlist_REDFixture must capture
// exactly 1 violation here, proving the scanner is alive.
//
// LOCATION RATIONALE: domain.ReconstituteUser lives in
// cells/accesscore/internal/domain, which is an internal package. Go's
// internal-import rule requires this fixture to live under cells/accesscore/.
// The testdata/ directory keeps it out of `go build ./...` while archtest
// loads it via an explicit packages.Load pattern. This mirrors the pattern
// used for rbacassign_direct_setstatus_red and identitymanage_direct_bump_epoch_red.
//
// FU-2 compatibility: uses named struct fields so ReconstituteUserParams
// field reorder does not silently break this fixture.
package reconstitute_user_caller_red

import (
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
)

// Bad simulates a future slice writer who tries to bypass the SetStatus funnel
// by directly calling domain.ReconstituteUser from a non-allowlisted path.
// RECONSTITUTE-USER-CALLER-01 MUST flag this call site.
func Bad() (*domain.User, error) {
	return domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    "u-x",
		Username:              "evil-user",
		Email:                 "x@example.com",
		PasswordHash:          "$2a$04$abc",
		PasswordVersion:       1,
		AuthzEpoch:            1,
		PasswordResetRequired: false,
		Status:                domain.StatusActive,
		Source:                domain.UserSourceIdentity,
		CreatedAt:             time.Unix(0, 0),
		UpdatedAt:             time.Unix(0, 0),
	})
}
