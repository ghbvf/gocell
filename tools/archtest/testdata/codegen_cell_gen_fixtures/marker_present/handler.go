//go:build ignore_codegen_archtest_fixtures

// Deliberately contains a marker comment — used by
// TestCodegenGates_NegativeFixtures/marker_present to verify that
// CODEGEN-MARKER-NONE-01 detects // +cell: marker syntax (reserved for K#05).

package demo

// +cell:foo=bar
// The above marker is intentionally present for fixture purposes.
