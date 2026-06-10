// Package red_no_gate is a CHANGEPASSWORD-INACTIVE-GATE-01 RED fixture: the
// password mutation (UpdatePassword) runs with NO credentialauthority.Assert
// gate at all. The detector must report ≥ 1 diagnostic (missing gate).
package red_no_gate

type repo interface {
	UpdatePassword(id string) error
}

func changePasswordInTx(r repo) error {
	return r.UpdatePassword("x")
}
