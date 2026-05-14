// Package identitymanage_direct_bump_epoch_red is a RED fixture for the
// USER-AUTHZ-EPOCH-BUMP-FUNNEL-01 archtest: it directly calls
// userRepo.BumpAuthzEpoch without routing through credentialinvalidate.
//
// LOCATION RATIONALE: the fixture must import
// cells/accesscore/internal/ports.UserRepository so the archtest's
// type-aware scanner resolves the receiver to that exact package. Go's
// internal-import rule rejects this from anywhere outside cells/accesscore/,
// which is why this fixture lives under cells/accesscore/internal/.../testdata
// rather than under tools/archtest/testdata/ (the previous location was a
// permanent load failure that the prior verifyRedFixtureDetected helper
// silently skipped — Finding #9 PR #490 review surfaced the leak; the strict
// converter now turns that into a hard fail, so the fixture had to move).
//
// `testdata/` excludes the package from `go build ./...` walks while still
// being loadable via an explicit packages.Load pattern from archtest.
package identitymanage_direct_bump_epoch_red

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
)

// badBump directly calls BumpAuthzEpoch on a ports.UserRepository — bypassing
// the credentialinvalidate funnel. This is the pattern the archtest bans.
func badBump(ctx context.Context, repo ports.UserRepository, userID string) (int64, error) {
	return repo.BumpAuthzEpoch(ctx, userID)
}
