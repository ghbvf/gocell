//go:build archtest_fixture

// Package baselinerolefixture is the RED fixture for
// BASELINE-ROLE-STRING-SINGLE-SOURCE-01: it deliberately hard-codes the platform
// role VALUES as bare string literals (instead of runtime/auth.RoleAdmin /
// RoleSuperAdmin) so the scanner's detector path is exercised. Build-tagged
// archtest_fixture so it never compiles into production or default test builds.
package baselinerolefixture

// badRoleCondition mimics an ABAC baseline condition that bypasses the
// single-source role constants — the exact drift the rule forbids.
var badRoleCondition = struct{ Values []string }{
	Values: []string{"admin", "superadmin"}, // RED: must use runtime/auth.Role* constants
}

// Ensure the package-level var is reachable so go vet does not complain about
// unused declarations in archtest_fixture builds.
var _ = badRoleCondition
