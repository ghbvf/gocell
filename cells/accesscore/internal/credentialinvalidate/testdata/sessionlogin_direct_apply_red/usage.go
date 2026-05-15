// Package sessionlogin_direct_apply_red is a RED fixture for the
// CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 archtest: it directly calls
// (*credentialinvalidate.Invalidator).Apply from a simulated sessionlogin
// caller path, which is NOT on the upstream allowlist. The archtest must
// detect this regression.
//
// LOCATION RATIONALE: same as identitymanage_direct_bump_epoch_red — the
// fixture imports cells/accesscore/internal/credentialinvalidate so it must
// live under cells/accesscore/internal/.../testdata to satisfy Go's
// internal-import rule. `testdata/` excludes the package from
// `go build ./...` walks while archtest loads it via explicit pattern.
//
// S4d (PR S4d) introduced this rule. S4e will tighten the allowlist further
// (removing identitymanage / rbacassign in favor of sealed authzmutate funnel).
package sessionlogin_direct_apply_red

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// badApply directly invokes Invalidator.Apply from a slice that is NOT on
// the upstream allowlist (sessionlogin). This is the pattern the archtest
// bans — login-time cascade is never the right shape; epoch must be
// snapshotted onto the new session row instead (ADR §A8).
func badApply(ctx context.Context, inv *credentialinvalidate.Invalidator, userID string) error {
	return inv.Apply(ctx, userID, session.CredentialEventLock)
}
