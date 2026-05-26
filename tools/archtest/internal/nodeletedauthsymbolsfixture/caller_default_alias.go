//go:build archtest_fixture

// Form 1: default-alias qualified references.
//   - 2 const references (RoleInternalAdmin, ServiceNameInternal)
//   - 1 func call        (BuiltinServiceRoles)
// Expected hits: 3.
//
// ResolvePackageRef on each SelectorExpr resolves info.Uses[auth] →
// *types.PkgName.Imported().Path() == fixture auth import path. Matches the
// shape that the prior AST matcher caught (`id.Name == "auth"`), so this
// file alone does not exercise the upgrade — it pins behavior parity for
// the baseline form. See caller_custom_alias.go for the upgrade-exclusive
// form.

package nodeletedauthsymbolsfixture

import "github.com/ghbvf/gocell/tools/archtest/internal/nodeletedauthsymbolsfixture/auth"

// DefaultAliasReferences emits the three banned references in default-alias
// form. The function is never called at runtime — fixtures exist for
// AST/type-info analysis only.
func DefaultAliasReferences() {
	_ = auth.RoleInternalAdmin
	_ = auth.ServiceNameInternal
	_ = auth.BuiltinServiceRoles("svc")
}
