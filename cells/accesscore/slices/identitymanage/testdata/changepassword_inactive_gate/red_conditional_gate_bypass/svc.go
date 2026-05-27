// Package red_conditional_gate_bypass is a CHANGEPASSWORD-INACTIVE-GATE-01 RED
// fixture (#1017 F2): credentialauthority.Assert appears textually BEFORE
// UpdatePassword (so a token.Pos-only detector passes) but is nested inside a
// conditional, so it does NOT dominate the mutation — UpdatePassword is
// reachable when cond is false, bypassing the gate. The detector must require
// an UNCONDITIONAL top-level Assert guard and report ≥ 1 diagnostic.
//
// LOCATION RATIONALE: imports cells/accesscore/internal/credentialauthority, so
// Go's internal-import rule requires this fixture under cells/accesscore/.
package red_conditional_gate_bypass

import (
	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialauthority"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
)

type repo interface {
	UpdatePassword(id string) error
}

func changePasswordInTx(user *domain.User, r repo, cond bool) error {
	if cond {
		if err := credentialauthority.Assert(user); err != nil {
			return err
		}
	}
	return r.UpdatePassword(user.ID)
}
