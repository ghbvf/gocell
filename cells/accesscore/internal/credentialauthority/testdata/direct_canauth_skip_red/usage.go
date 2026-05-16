// Package direct_canauth_skip_red is a RED fixture for
// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (upstream prong). It simulates a
// slice file that bypasses the funnel by directly calling
// user.CanAuthenticate() and reading user.PasswordVersion — both must be
// detected by the upstream prong of the archtest (≥ 1 violation expected).
//
// LOCATION RATIONALE: the fixture imports
// cells/accesscore/internal/domain (and runtime/auth/session), so Go's
// internal-import rule requires this fixture to live under cells/accesscore/.
// The `testdata/` directory excludes the package from `go build ./...` while
// archtest loads it via an explicit packages.Load pattern.
//
// To exercise both fields (PasswordVersion + RevokedAt) and the method
// (CanAuthenticate), this fixture does all three directly. The upstream
// prong scans for each and must flag at least one — multiple flags is fine.
package direct_canauth_skip_red

import (
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// badDirectCanAuth bypasses credentialauthority.Assert and reads authz state
// directly. The upstream prong must flag each occurrence.
func badDirectCanAuth(u *domain.User, view *session.ValidateView, expected int64) bool {
	if !u.CanAuthenticate() { // direct method call — violation
		return false
	}
	if u.PasswordVersion != expected { // direct field read — violation
		return false
	}
	if view.RevokedAt != nil { // direct field read on session view — violation
		return false
	}
	return true
}
