package projection_register_violate

import "github.com/ghbvf/gocell/kernel/cell"

// rogueRegister calls reg.RegisterProjection from a hand-written, non-generated,
// non-test file. This must trigger PROJECTION-REGISTER-FUNNEL-01 — hand-rolled
// projection declarations bypass the cellgen single source of truth.
func rogueRegister(reg cell.Registrar) {
	// VIOLATION: reg.RegisterProjection called from a non-allowlisted file.
	_ = reg.RegisterProjection(fixtureRequest())
}

// rogueSagaRegister proves the saga-journal constructor is funnel-locked too: a
// reg.RegisterProjection fed by cell.NewSagaJournalProjectionRequest from a
// non-generated file must ALSO fire PROJECTION-REGISTER-FUNNEL-01. The funnel is
// argument-agnostic — it gates the reg.RegisterProjection callsite, so the saga
// constructor cannot be a bypass (EPIC #1609 PR-05).
func rogueSagaRegister(reg cell.Registrar) {
	// VIOLATION: saga-journal projection registered from a non-allowlisted file.
	_ = reg.RegisterProjection(cell.NewSagaJournalProjectionRequest(
		fixtureRequest().Apply, "saga_status", "c1", "sagastatus",
	))
}
