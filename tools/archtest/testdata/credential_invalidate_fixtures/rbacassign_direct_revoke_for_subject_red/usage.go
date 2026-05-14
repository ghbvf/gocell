// Package rbacassign_direct_revoke_for_subject_red is a RED fixture:
// it directly calls sessionStore.RevokeForSubject without going through the
// credentialinvalidate funnel. CREDENTIAL-INVALIDATE-FUNNEL-01 must detect
// exactly 1 violation in this file.
package rbacassign_direct_revoke_for_subject_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// badRevoke directly calls RevokeForSubject on a session.Store — bypassing
// the credentialinvalidate funnel. This is the pattern the archtest bans.
func badRevoke(ctx context.Context, store session.Store, subjectID string) error {
	return store.RevokeForSubject(ctx, subjectID, session.CredentialEventLock)
}
