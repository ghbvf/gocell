//go:build ignore_codegen_archtest_fixtures

// Deliberately defines Init on *Demo — used by
// TestCodegenGates_NegativeFixtures/has_init_method to verify that
// CODEGEN-USER-FILE-OVERLAP-01 detects the Init method on the hand-written
// cell file (Init is owned by cell_gen.go after codegen migration).

package demo

import "context"

type Demo struct{}

// Init is intentionally defined here to trigger CODEGEN-USER-FILE-OVERLAP-01.
func (c *Demo) Init(ctx context.Context) error { return nil }
