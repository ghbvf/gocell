// Package outside_caller_red is a RED fixture for
// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (downstream prong). It calls
// credentialauthority.Assert from a non-allowlisted path, which violates
// the caller allowlist (sessionlogin/, sessionrefresh/, sessionvalidate/,
// or the funnel package itself).
//
// LOCATION RATIONALE: the fixture imports
// cells/accesscore/internal/credentialauthority, so Go's internal-import
// rule requires this fixture to live under cells/accesscore/. The
// `testdata/` directory excludes the package from `go build ./...` while
// archtest loads it via an explicit packages.Load pattern.
package outside_caller_red

import (
	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialauthority"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
)

// badCaller invokes credentialauthority.Assert from a non-allowlisted file.
// The downstream prong of CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 must flag
// this call site (≥ 1 violation expected from the RED fixture).
func badCaller(u *domain.User) error {
	return credentialauthority.Assert(u)
}
