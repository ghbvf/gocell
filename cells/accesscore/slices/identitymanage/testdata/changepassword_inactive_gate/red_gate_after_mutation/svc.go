// Package red_gate_after_mutation is a CHANGEPASSWORD-INACTIVE-GATE-01 RED
// fixture: the credentialauthority.Assert gate is present but runs AFTER the
// UpdatePassword mutation — exactly the issue #1017 post-commit-gate shape that
// lets a suspended/locked account's password be rewritten before the 403. The
// detector must report ≥ 1 diagnostic (gate after mutation).
//
// LOCATION RATIONALE: imports cells/accesscore/internal/credentialauthority, so
// Go's internal-import rule requires this fixture under cells/accesscore/.
package red_gate_after_mutation

import (
	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialauthority"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
)

type repo interface {
	UpdatePassword(id string) error
}

func changePasswordInTx(user *domain.User, r repo) error {
	if err := r.UpdatePassword(user.ID); err != nil {
		return err
	}
	return credentialauthority.Assert(user)
}
