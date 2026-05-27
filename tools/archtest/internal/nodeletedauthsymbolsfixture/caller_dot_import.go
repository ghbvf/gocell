//go:build archtest_fixture

// Form 3: dot-import. Sibling file (Go does not permit `import . "X"` and
// `import "X"` in the same file).
//
// Covers three banned-symbol references in dot-imported bare-Ident form:
//   - 2 consts (RoleInternalAdmin, ServiceNameInternal) — *types.Const
//     bare-Ident; caught by scanner (B) info.Uses type switch (replaces the
//     prior BS-A accepted residual that depended on ResolvePackageRef's
//     typed-callable filter).
//   - 1 func (BuiltinServiceRoles) — *types.Func bare-Ident; caught by (B).
//
// Expected hits: 3.

package nodeletedauthsymbolsfixture

import . "github.com/ghbvf/gocell/tools/archtest/internal/nodeletedauthsymbolsfixture/auth"

// DotImportReferences emits the three banned references in dot-imported
// bare-Ident form.
func DotImportReferences() {
	_ = RoleInternalAdmin
	_ = ServiceNameInternal
	_ = BuiltinServiceRoles("svc")
}
