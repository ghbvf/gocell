//go:build archtest_fixture

// Package auth is a fixture-local fake of runtime/auth whose import path is
// distinct from the real runtime/auth. It defines the three deleted symbols
// (RoleInternalAdmin / ServiceNameInternal / BuiltinServiceRoles) so the
// caller files in the parent fixture can reference them in qualified, alias,
// and dot-import forms. The scanner under test is parametrized over an
// authImportPath argument so it can target this fixture-local path during
// TestNO_DELETED_AUTH_SYMBOLS_01_FixtureCatchesAllForms without redefining
// the production scope.
//
// This file also deliberately exercises the BS-同包 closure: the three
// declarations below are package-scope re-declarations that branch (C)
// (info.Defs) must catch, and the BuiltinServiceRoles body uses
// RoleInternalAdmin as a same-package self-reference that branch (B)
// (info.Uses, no package skip) must catch. Together they prove the scanner
// fires when a deleted symbol is reintroduced inside runtime/auth itself —
// the gap PR #1180 review flagged.
package auth

// RoleInternalAdmin mirrors the deleted runtime/auth const so the scanner has
// a reachable symbol to bind in const-reference fixture files AND a (C)
// re-declaration hit inside the auth package.
const RoleInternalAdmin = "role:internal-admin"

// ServiceNameInternal mirrors the deleted runtime/auth const.
const ServiceNameInternal = "gocell-internal"

// BuiltinServiceRoles mirrors the deleted runtime/auth func so the scanner
// has a reachable *types.Func to bind in dot-import bare-Ident form. Body
// references RoleInternalAdmin to also exercise the (B) same-package
// self-reference detection (1 hit) on top of the (C) re-declaration hits
// (3 hits) for the three names above.
func BuiltinServiceRoles(name string) []string {
	_ = name
	return []string{RoleInternalAdmin}
}
