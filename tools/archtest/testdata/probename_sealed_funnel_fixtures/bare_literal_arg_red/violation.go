// Package bare_literal_arg_red is a synthetic violation fixture for
// PROBENAME-SEALED-FUNNEL-01/A2.
//
// It calls healthz.NewProbe with a bare-string type conversion
// (healthz.ProbeName("my_probe")) instead of a declared const.
// The archtest scanner must detect this as an A2 violation.
//
// DO NOT use this package in production code.
package bare_literal_arg_red

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/healthz"
)

// buildProbeFromBareString demonstrates the A2 violation: passing a
// ProbeName type-conversion of a string literal to NewProbe instead of
// referencing a declared ProbeName const.
func buildProbeFromBareString() healthz.Probe {
	// VIOLATION A2: NewProbe name arg is not a declared ProbeName const.
	// The correct pattern is: declare `const myProbe healthz.ProbeName = "my_probe"`
	// in a sanctioned package and reference that const here.
	return healthz.NewProbe(healthz.ProbeName("my_probe"), func(_ context.Context) error { return nil })
}
