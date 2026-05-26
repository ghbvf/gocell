//go:build archtest_fixture

// Form 3: dot-import. Sibling file (Go does not permit `import . "X"` and
// `import "X"` in the same file).
//
//   - 1 func call (BuiltinServiceRoles) — caught via *types.Func bare-Ident
//     branch of ResolvePackageRef.
//
// const references in dot-import form are an accepted blind spot: typeseval
// .ResolvePackageRef returns false for bare-Ident → *types.Const, so a
// `_ = RoleInternalAdmin` line after `import . "<fixture-auth>"` would not
// be detected. Extension of ResolvePackageRef to cover Const/Var bare Idents
// is tracked by #1037 (archtest façade 收缩). The omission is intentional
// and documented; including those references in the fixture would force the
// hit count to mis-reflect what the production scanner actually catches.
//
// Expected hits: 1.

package nodeletedauthsymbolsfixture

import . "github.com/ghbvf/gocell/tools/archtest/internal/nodeletedauthsymbolsfixture/auth"

// DotImportFuncCall emits the func call in dot-imported bare-Ident form.
func DotImportFuncCall() {
	_ = BuiltinServiceRoles("svc")
}
