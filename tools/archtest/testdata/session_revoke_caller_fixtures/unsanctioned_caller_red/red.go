// Package unsanctioned_caller_red is a RED fixture for
// SESSION-REVOKE-CALLER-INTX-01: a function that calls (session.Store).Revoke
// without being in the sanctioned caller allowlist. The archtest must detect
// ≥1 violation when scanning this package with an empty allowlist.
package unsanctioned_caller_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// unsanctionedRevoke calls session.Store.Revoke without any RunInTx scope.
// This is NOT in sessionRevokeAllowlist — the archtest must flag it as a
// violation.
func unsanctionedRevoke(ctx context.Context, s session.Store, id string) error {
	return s.Revoke(ctx, id)
}
