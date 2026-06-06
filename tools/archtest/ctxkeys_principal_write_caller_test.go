// ctxkeys_principal_write_caller_test.go — closes the DOWNSTREAM side of the
// principal ctx-injection trust boundary.
//
//   - INVARIANT: CTXKEYS-PRINCIPAL-WRITE-CALLER-01
//
// Detector logic, allowlist, and Check* function live in
// ctxkeys_principal_write_caller.go (non-test) so they can be compiled by
// external Cell repositories. This file dogfoods that shared logic against
// GoCell itself — single source, no parallel rule body.
package archtest

import (
	"testing"
)

// TestCtxkeysPrincipalWriteCaller01 asserts that every production callsite of the
// four principal ctx-key setters sits in the per-setter allowlist, and that no
// allowlist entry is stale (anti-vacuity reverse check).
func TestCtxkeysPrincipalWriteCaller01(t *testing.T) {
	t.Parallel()
	// testing.Short() skip is inside CheckCtxkeysPrincipalWriteCaller01.
	Report(t, "CTXKEYS-PRINCIPAL-WRITE-CALLER-01",
		CheckCtxkeysPrincipalWriteCaller01(t, ConfigForExternalCell{}))
}
