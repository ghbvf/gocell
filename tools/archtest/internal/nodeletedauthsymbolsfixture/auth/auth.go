//go:build archtest_fixture

// Package auth is a fixture-local fake of runtime/auth whose import path is
// distinct from the real runtime/auth. It defines the three deleted symbols
// (RoleInternalAdmin / ServiceNameInternal / BuiltinServiceRoles) so the
// caller files in the parent fixture can reference them in qualified, alias,
// and dot-import forms. The scanner under test is parametrized over an
// authImportPath argument so it can target this fixture-local path during
// TestNoDeletedAuthSymbols_FixtureCatchesAllForms without redefining the
// production scope.
package auth

// RoleInternalAdmin mirrors the deleted runtime/auth const so the scanner has
// a reachable symbol to bind in const-reference fixture files.
const RoleInternalAdmin = "role:internal-admin"

// ServiceNameInternal mirrors the deleted runtime/auth const.
const ServiceNameInternal = "gocell-internal"

// BuiltinServiceRoles mirrors the deleted runtime/auth func so the scanner
// has a reachable *types.Func to bind in dot-import bare-Ident form.
func BuiltinServiceRoles(name string) []string {
	_ = name
	return []string{RoleInternalAdmin}
}
