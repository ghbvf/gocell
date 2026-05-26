// Package helper_wrapper_red is a synthetic violation fixture for
// PROBENAME-SEALED-FUNNEL-01/B2.
//
// It defines a helper function that wraps reg.RegisterReadiness in a file that
// is NOT in the sanctioned wrapper set (only kernel/cell/healthz.go and cellgen
// healthz_gen.go are sanctioned). The archtest B2 blind-spot scanner must detect
// this unsanctioned wrapper.
//
// DO NOT use this package in production code.
package helper_wrapper_red

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/healthz"
)

// RegisterMyProbe is an unsanctioned helper wrapper around reg.RegisterReadiness.
// VIOLATION B2: this function provides a registration indirection layer outside
// the sanctioned set (kernel/cell/healthz.go + cellgen healthz_gen.go).
// The correct pattern is for cells to call either:
//   - cellgen-generated RegisterReadiness(reg, repoProber)
//   - cell.RegisterEmitterHealthProbes(reg, emitter)
//
// Both of these are in the sanctioned set; this helper is not.
func RegisterMyProbe(reg cell.Registrar, name healthz.ProbeName, prober healthz.Prober) error {
	// VIOLATION B2: wraps RegisterReadiness from outside sanctioned set.
	return reg.RegisterReadiness(name, prober)
}
