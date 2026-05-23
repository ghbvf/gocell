// Package foo is a synthetic fixture for HEALTHZ-TYPED-REGISTER-01.
// It simulates a cell package that illegally calls reg.Healthz() directly
// from a hand-written file instead of using the cellgen-generated
// RegisterRepoReady / RegisterEmitterProbes helpers.
//
// DO NOT use this package in production code.
package foo

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/healthz"
)

// registerRepoReadyBad demonstrates the HEALTHZ-TYPED-REGISTER-01 violation:
// calling reg.Healthz() from cell_init.go (not healthz_gen.go).
// This must be detected as a violation.
func registerRepoReadyBad(reg cell.Registrar) error {
	// VIOLATION HEALTHZ-TYPED-REGISTER-01: reg.Healthz() called from
	// cell_init.go, not from healthz_gen.go. Hand-written cells/ code must
	// use RegisterRepoReady / RegisterEmitterProbes instead.
	return reg.Healthz().Register(healthz.NewProbe("foo_repo_ready", func(_ context.Context) error {
		return nil
	}))
}
