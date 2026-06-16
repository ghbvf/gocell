// Package projection_consumerbase_sagajournal_ok is a synthetic GREEN fixture
// for PROJECTION-CONSUMERBASE-WIRING-01. It models a saga-journal-only
// composition root (the examples/orderfulfillment shape): it wires
// bootstrap.WithProjectionTxRunner — shared infra reused by the saga-journal
// Tailer path — but NO outbox-coordinator option and NO ConsumerBase. The rule
// MUST NOT fire here: phase6 drains the saga-journal Tailer independently of the
// ConsumerBase router, so such a root boots fine without WithConsumerBase.
//
// This pins the fix for the WithProjectionTxRunner false positive: if a future
// edit re-adds WithProjectionTxRunner to projectionWiringOptions, the rule would
// fire on this fixture and TestProjectionConsumerBaseWiring_SagaJournalOnly_NoFire
// turns red. The option argument is nil — the fixture is only type-checked and
// AST-scanned, never executed (the rule resolves callee identity via go/types).
//
// DO NOT use this package in production code.
package projection_consumerbase_sagajournal_ok

import "github.com/ghbvf/gocell/framework/runtime/bootstrap"

// sagaJournalOnlyWiring assembles a composition-root option slice for a
// saga-journal Tailer projection: the shared WithProjectionTxRunner without any
// outbox-coordinator option and without WithConsumerBase. The rule must treat
// this as clean.
func sagaJournalOnlyWiring() []bootstrap.Option {
	return []bootstrap.Option{
		bootstrap.WithProjectionTxRunner(nil),
	}
}
