// Package mdm_test wires the GoCell standard cell-rule archtest suite for the
// external mdm module (Operator-SDK mode, ADR 202605281200). It is the consumer
// side of tools/archtest's external façade: RunStandardCellRules scans this
// module's tree + composition root for the platform invariants that apply to an
// external repo (PROD-MAIN-WIRING-NOOP-REJECT-01, PANIC-REGISTERED-01,
// ERRCODE-KIND-LITERAL-01, MESSAGE-CONST-LITERAL-01, EXPORTED-ERROR-NEW-01,
// SCAFFOLD-DERIVED-FORCEOVERWRITE-01).
package mdm_test

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

func TestStandardCellRules(t *testing.T) {
	archtest.RunStandardCellRules(t, archtest.ConfigForExternalCell{
		// The composition root: PROD-MAIN-WIRING-NOOP-REJECT-01 scans ONLY these
		// packages and rejects a raw kernel/outbox noop event sink in production
		// wiring. A pattern matching zero packages fails loud (false-green guard).
		ProductionMainPkgs: []string{"./cmd/mdmd"},
	})
}
