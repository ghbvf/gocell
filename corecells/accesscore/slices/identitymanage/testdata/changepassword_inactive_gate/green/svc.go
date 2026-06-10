// Package green is a CHANGEPASSWORD-INACTIVE-GATE-01 GREEN fixture: the
// credentialauthority.Assert inactive gate runs BEFORE the UpdatePassword
// mutation (the correct ordering). The detector must report 0 diagnostics.
//
// LOCATION RATIONALE: this fixture imports
// corecells/accesscore/internal/credentialauthority, so Go's internal-import rule
// requires it to live under corecells/accesscore/. The testdata/ directory
// excludes the package from `go build ./...`; archtest loads it via an
// explicit packages.Load pattern.
package green

import (
	"github.com/ghbvf/gocell/corecells/accesscore/internal/credentialauthority"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
)

type repo interface {
	UpdatePassword(id string) error
}

func changePasswordInTx(user *domain.User, r repo) error {
	if err := credentialauthority.Assert(user); err != nil {
		return err
	}
	return r.UpdatePassword(user.ID)
}
