package typeseval

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// INVARIANT: TYPESINFO-AST-SAME-SOURCE
//
// EachFileInPackage is the single entry point for archtest checks that need
// go/types information. The callback receives the *ast.File, *types.Info, and
// *token.FileSet from the same packages.Load result, so info.Types[node] /
// info.Uses[node] / info.Selections[node] are guaranteed to resolve for every
// node found inside file.
//
// scanner.EachFile parses each source file with a fresh token.FileSet,
// producing AST nodes whose pointer identity differs from the nodes that
// pkg.TypesInfo was built from. Combining scanner.EachFile with a captured
// pkg.TypesInfo silently fails open: every Types/Uses/Selections lookup
// misses, and any "type-aware" check degrades to a name-only AST match.
//
// Decision rule for archtest authors:
//   - need go/types info (receiver type, const identity, interface
//     implementation, expr type) → EachFileInPackage
//   - pure AST shape / import path / filename pattern → scanner.EachFile
//
// Mixing the two paths in one check is a bug — there is no scenario where
// scanner-parsed nodes and a packages-loaded TypesInfo can be combined
// meaningfully.
//
// ref: golang.org/x/tools/go/analysis (Pass{Fset,Files,TypesInfo} single
// source); dominikh/go-tools staticcheck (analysis.Pass-based, no re-parse
// API); go-critic CheckerContext (single TypesInfo per ctx).
func EachFileInPackage(
	root string,
	pkg *packages.Package,
	skipTestFiles bool,
	fn func(file *ast.File, relPath string, info *types.Info, fset *token.FileSet),
) {
	if pkg == nil || pkg.TypesInfo == nil {
		return
	}
	for i, file := range pkg.Syntax {
		if i >= len(pkg.GoFiles) {
			continue
		}
		absPath := pkg.GoFiles[i]
		if skipTestFiles && strings.HasSuffix(filepath.Base(absPath), "_test.go") {
			continue
		}
		relPath := absPath
		if rel, err := filepath.Rel(root, absPath); err == nil {
			relPath = filepath.ToSlash(rel)
		}
		fn(file, relPath, pkg.TypesInfo, pkg.Fset)
	}
}
