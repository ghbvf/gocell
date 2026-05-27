//go:build archtest_fixture

// Package notauth defines the same identifier names as the fixture's auth
// package but lives under a distinct import path. The negative-case caller
// imports notauth under the local alias `auth` to exercise the false-positive
// risk that the AST-only matcher (`id.Name == "auth"`) failed: a same-named
// alias for an unrelated package must NOT trigger the rule, because the
// typed scanner resolves to the canonical *types.PkgName.Imported().Path()
// rather than the local alias identifier.
package notauth

// RoleInternalAdmin is a same-named-but-different-package decoy. The scanner
// must NOT report references to this constant — its owning package path is
// notauth, not auth.
const RoleInternalAdmin = "decoy:not-the-real-thing"

// ServiceNameInternal is a same-named-but-different-package decoy.
const ServiceNameInternal = "decoy-service"

// BuiltinServiceRoles is a same-named-but-different-package decoy.
func BuiltinServiceRoles(name string) []string {
	_ = name
	return nil
}
