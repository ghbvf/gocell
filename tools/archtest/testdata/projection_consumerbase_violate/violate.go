// Package projection_consumerbase_violate is a synthetic fixture for the
// PROJECTION-CONSUMERBASE-WIRING-01 archtest. It wires bootstrap.WithProjection*
// options WITHOUT bootstrap.WithConsumerBase — the exact PR #1483 regression
// shape — which the rule MUST flag, proving the rule is not fail-open.
//
// The option arguments are nil: the fixture is only type-checked and
// AST-scanned by the archtest, never executed (the rule resolves the callee
// identity via go/types and ignores arguments).
//
// DO NOT use this package in production code.
package projection_consumerbase_violate

import "github.com/ghbvf/gocell/framework/runtime/bootstrap"

// violatingWiring assembles a composition-root option slice that registers
// projection infrastructure but omits WithConsumerBase. This is the violation
// the archtest must detect.
func violatingWiring() []bootstrap.Option {
	return []bootstrap.Option{
		bootstrap.WithProjectionCheckpointStore(nil),
		bootstrap.WithProjectionReplaySource(nil),
		bootstrap.WithProjectionCursor(nil),
	}
}
