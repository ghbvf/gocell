// Package string_cast_bypass_red is a synthetic violation fixture for
// PROBENAME-SEALED-FUNNEL-01/B3 (and A2).
//
// It casts a dynamic function-call result to healthz.ProbeName, which bypasses
// the type-system requirement that ProbeName values come from declared consts.
// The archtest scanner must detect this as an A2/B3 violation.
//
// DO NOT use this package in production code.
package string_cast_bypass_red

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/framework/kernel/healthz"
)

// buildProbeWithCast demonstrates the B3/A2 violation: casting a dynamic
// fmt.Sprintf result to healthz.ProbeName to bypass the funnel.
// The correct pattern is to declare a typed const in a sanctioned package
// and reference that const.
func buildProbeWithCast(cellID string) healthz.Probe {
	// VIOLATION B3/A2: ProbeName(callExpr) — dynamic expression cast to ProbeName.
	name := healthz.ProbeName(fmt.Sprintf("outbox_failopen_rate_%s", cellID))
	return healthz.NewProbe(name, func(_ context.Context) error { return nil })
}
