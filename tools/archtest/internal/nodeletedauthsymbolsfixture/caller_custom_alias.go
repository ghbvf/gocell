//go:build archtest_fixture

// Form 2: custom-alias qualified references — the bypass form that the prior
// AST matcher (`id.Name == "auth"`) silently passed.
//   - 2 const references (RoleInternalAdmin, ServiceNameInternal)
//   - 1 func call        (BuiltinServiceRoles)
// Expected hits: 3.
//
// `authz` resolves to the same import path as the default `auth` alias would,
// because ResolvePackageRef looks at info.Uses[X].(*types.PkgName).Imported()
// .Path() rather than the syntactic identifier name. This file is the primary
// witness of the Soft → Medium upgrade: the pre-PR matcher would record 0 hits
// here.

package nodeletedauthsymbolsfixture

import authz "github.com/ghbvf/gocell/tools/archtest/internal/nodeletedauthsymbolsfixture/auth"

// CustomAliasReferences emits the three banned references through a non-default
// import alias.
func CustomAliasReferences() {
	_ = authz.RoleInternalAdmin
	_ = authz.ServiceNameInternal
	_ = authz.BuiltinServiceRoles("svc")
}
