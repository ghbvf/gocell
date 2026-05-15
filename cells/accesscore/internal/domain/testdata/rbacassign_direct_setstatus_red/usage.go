// Package rbacassign_direct_setstatus_red is a RED fixture for the
// AUTHZ-MUTATION-APPLY-FUNNEL-01 archtest (Rule a): it directly calls
// domain.User.SetStatus from a simulated rbacassign caller path, which is NOT
// on the SetStatus allowlist. The archtest must detect this regression.
//
// LOCATION RATIONALE: the fixture imports
// cells/accesscore/internal/domain.User so Go's internal-import rule requires
// this fixture to live under cells/accesscore/. The `testdata/` directory
// excludes the package from `go build ./...` while archtest loads it via an
// explicit packages.Load pattern. This mirrors the pattern used for
// identitymanage_direct_bump_epoch_red.
package rbacassign_direct_setstatus_red

import (
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
)

// badSetStatus directly calls domain.User.SetStatus from a non-allowlisted
// caller. Callers outside {authzmutate/, adminprovision/, domain/,
// identitymanage/} MUST route through authzmutate.Mutator.Apply.
func badSetStatus(u *domain.User) {
	u.SetStatus(domain.StatusLocked, time.Now())
}
