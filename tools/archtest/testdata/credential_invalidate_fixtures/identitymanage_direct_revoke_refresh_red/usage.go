// Package identitymanage_direct_revoke_refresh_red is a RED fixture:
// it directly calls refreshStore.RevokeUser without going through the
// credentialinvalidate funnel. REFRESH-REVOKE-USER-FUNNEL-01 must detect
// exactly 1 violation in this file.
package identitymanage_direct_revoke_refresh_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// badRefreshRevoke directly calls RevokeUser on a refresh.Store — bypassing
// the credentialinvalidate funnel. This is the pattern the archtest bans.
func badRefreshRevoke(ctx context.Context, store refresh.Store, subjectID string) error {
	return store.RevokeUser(ctx, subjectID)
}
