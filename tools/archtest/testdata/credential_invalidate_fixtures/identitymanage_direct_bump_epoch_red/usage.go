// Package identitymanage_direct_bump_epoch_red is a RED fixture:
// it directly calls userRepo.BumpAuthzEpoch without going through the
// credentialinvalidate funnel. USER-AUTHZ-EPOCH-BUMP-FUNNEL-01 must detect
// exactly 1 violation in this file.
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
