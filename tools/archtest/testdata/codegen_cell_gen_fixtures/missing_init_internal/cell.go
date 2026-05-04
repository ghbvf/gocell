//go:build ignore_codegen_archtest_fixtures

// Deliberately omits initInternal — used by
// TestCodegenGates_NegativeFixtures/missing_init_internal to verify that
// CODEGEN-INIT-INTERNAL-01 detects the absence of the required hook.

package demo

type Demo struct{}
