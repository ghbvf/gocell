// Package newprobename_dynamic_red is a synthetic violation fixture for
// PROBENAME-SEALED-FUNNEL-01/A4.
//
// It calls healthz.NewProbeName from a file that is NOT in the sanctioned
// caller allowlist (only kernel/healthz/probename.go). The archtest scanner
// must detect this as an A4 violation.
//
// DO NOT use this package in production code.
package newprobename_dynamic_red

import "github.com/ghbvf/gocell/kernel/healthz"

// buildNameDynamically demonstrates the A4 violation: calling
// healthz.NewProbeName from outside the sanctioned allowlist.
// Only kernel/healthz/probename.go may call NewProbeName directly in
// production code (it is the implementation of EmitterFailOpenProbeName
// and other composed constructors).
func buildNameDynamically(suffix string) (healthz.ProbeName, error) {
	// VIOLATION A4: NewProbeName called from non-sanctioned production file.
	// Tests should use healthz.MustProbeName or handle the error directly.
	// Production composed-name paths should go through EmitterFailOpenProbeName
	// or the cellgen-generated RegisterReadiness.
	return healthz.NewProbeName("outbox_failopen_rate_" + suffix)
}
