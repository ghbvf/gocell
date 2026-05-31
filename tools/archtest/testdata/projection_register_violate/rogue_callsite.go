package projection_register_violate

import "github.com/ghbvf/gocell/kernel/cell"

// rogueRegister calls reg.RegisterProjection from a hand-written, non-generated,
// non-test file. This must trigger PROJECTION-REGISTER-FUNNEL-01 — hand-rolled
// projection declarations bypass the cellgen single source of truth.
func rogueRegister(reg cell.Registrar) {
	// VIOLATION: reg.RegisterProjection called from a non-allowlisted file.
	_ = reg.RegisterProjection(fixtureRequest())
}
