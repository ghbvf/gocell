// INVARIANT: SCAFFOLD-WRITE-FUNNEL-01
//
// All scaffold/codegen filesystem writes funnel through
// pkg/pathsafe.WritePlannedFiles. Direct os.MkdirAll / os.WriteFile in
// scaffold paths is statically forbidden; pathsafe enforces:
//   - root containment (ResolveRoot + ContainPath)
//   - all-or-nothing conflict detection (no partial bundles)
//   - atomic write with rollback (no half-written state)
//
// AI-rebust: Medium (type-aware via go/types Info — see PR454 round-2 F1
// for the upgrade rationale).
//
// # Recognition: type-aware, not name-based
//
// The scanner identifies the os package by go/types canonical resolution
// (info.Uses[ident].Pkg().Path() == "os"), not by ident.Name == "os".
// Three AST forms map to the same callee and are all caught:
//
//  1. `os.WriteFile(...)`           — normal import, SelectorExpr.X.Name == "os"
//  2. `osx.WriteFile(...)`          — `import osx "os"` alias, SelectorExpr.X.Name == "osx"
//  3. `WriteFile(...)`              — `import . "os"` dot-import, Fun is *ast.Ident
//
// The pre-PR454-round-2 implementation matched form (1) only by string
// name and silently passed forms (2) and (3). The new resolver-based
// matcher closes that bypass.
//
// # Two-layer defense and scope asymmetry
//
//   - depguard scaffold-os-ban (Hard, package-level): bans `import "os"` at
//     lint time in 4 pure-render files — scaffold_bundle.go,
//     contractgen/generator.go, contractgen/scope.go, scaffold_assembly.go.
//     These files have no legitimate os.* use; the ban is total.
//   - this archtest (Medium, type-aware method-call level): scans ALL
//     scaffold paths listed in TestScaffoldWriteFunnel_NoDirectOSWrites,
//     including paths intentionally excluded from the depguard ban list
//     because they have legitimate non-write os.* calls:
//   - tools/codegen/cellgen/scaffold.go  (no os import today; future guard)
//   - tools/codegen/writer.go            (os.ReadFile for drift detection)
//   - kernel/assembly/generator.go       (os.ReadFile/Stat for go.mod metadata)
//   - cmd/gocell/app/scaffold.go         (os.Stat for target dir checks)
//     For these paths depguard would produce false-positives; archtest
//     enforces only the write-method subset (MkdirAll/WriteFile/Mkdir/
//     Create/OpenFile).
//
// Extension contract: when adding a new scaffold sub-package that writes
// files, add it to the scope in TestScaffoldWriteFunnel_NoDirectOSWrites
// and update this comment.
//
// # Out-of-scope (documented exemption)
//
// The following call sites legitimately use os.MkdirAll / os.WriteFile
// outside the funnel because the output path is supplied by the user
// via --out flag (no root-containment guarantee can be made):
//
//	cmd/gocell/app/generate_catalog.go (gocell generate catalog --out=<path>)
//	cmd/gocell/app/export.go writeOut  (gocell export {catalog|metadata} --out=<path>)
//
// Adding any NEW file under cmd/gocell/app/ must either:
//  1. Match the scaffold*.go prefix → mandatory funnel through pathsafe.
//  2. Justify exemption in this comment block before merging.
//
// The scaffoldOnlyPred predicate enforces #1; #2 is the human-review gate.
package archtest

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// bannedOSWriteSelectors is the closed set of os package functions whose
// direct call inside scaffold paths violates SCAFFOLD-WRITE-FUNNEL-01.
// These are the write-side primitives the funnel exists to encapsulate;
// read-side helpers (os.ReadFile / os.Stat / os.Open) are intentionally
// out of scope so legitimate drift-detection / metadata-read paths can
// keep direct os import.
var bannedOSWriteSelectors = map[string]bool{
	"MkdirAll":  true,
	"WriteFile": true,
	"Mkdir":     true,
	"Create":    true,
	"OpenFile":  true,
}

// scaffoldFunnelPred limits which files inside the loaded packages are
// scanned. cmd/gocell/app/ may only match scaffold*.go (generate_catalog.go
// and export.go are exempt — see file-level Out-of-scope godoc).
// tools/codegen/cellgen/ may match scaffold*.go and generate_*.go.
// Everywhere else (tools/codegen/contractgen, tools/codegen top-level,
// kernel/assembly): all non-test .go files.
func scaffoldFunnelPred(rel string) bool {
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	if strings.HasSuffix(base, "_test.go") {
		return false
	}
	if strings.HasPrefix(rel, "cmd/gocell/app/") {
		return strings.HasPrefix(base, "scaffold")
	}
	if strings.HasPrefix(rel, "tools/codegen/cellgen/") {
		return strings.HasPrefix(base, "scaffold") || strings.HasPrefix(base, "generate_")
	}
	return true
}

// canonicalOSWriteCall returns the banned os function name if the given
// call resolves (via go/types) to one of bannedOSWriteSelectors; otherwise
// "". Mirrors canonicalCalledFunc in wrapper_location_test.go: info.Uses
// canonicalizes alias imports (`import osx "os"`) and dot-imports
// (`import . "os"`) to the same *types.Func with Pkg().Path() == "os".
func canonicalOSWriteCall(info *types.Info, call *ast.CallExpr) string {
	var ident *ast.Ident
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		ident = fn.Sel
	case *ast.Ident:
		ident = fn
	default:
		return ""
	}
	obj := info.Uses[ident]
	if obj == nil {
		return ""
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return ""
	}
	pkg := fn.Pkg()
	if pkg == nil || pkg.Path() != "os" {
		return ""
	}
	if !bannedOSWriteSelectors[fn.Name()] {
		return ""
	}
	return fn.Name()
}

// TestScaffoldWriteFunnel_NoDirectOSWrites enforces SCAFFOLD-WRITE-FUNNEL-01:
// no direct os.MkdirAll / os.WriteFile / os.Mkdir / os.Create / os.OpenFile
// calls in scaffold paths outside pkg/pathsafe.
//
// Scanned paths (predicate-filtered after package load):
//   - tools/codegen/cellgen/         (ScaffoldCell, ScaffoldCellBundle, generate_*)
//   - tools/codegen/contractgen/     (generator + writer)
//   - tools/codegen/writer.go        (codegen.Write — top-level only)
//   - kernel/assembly/               (Generator.PlanAssemblyScaffold)
//   - cmd/gocell/app/scaffold*.go    (scaffoldSlice, scaffoldContract, scaffoldJourney)
//
// Allowlist (only these files may call banned os selectors):
//   - pkg/pathsafe/pathsafe.go (not loaded by this test; out of scope by construction)
func TestScaffoldWriteFunnel_NoDirectOSWrites(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	resolver, err := typeseval.SharedResolver(root, false, nil,
		"./tools/codegen/...",
		"./kernel/assembly/...",
		"./cmd/gocell/app/...",
	)
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}

	var diags []scanner.Diagnostic
	for _, pkg := range resolver.Packages() {
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			rel, err := filepath.Rel(root, absPath)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !scaffoldFunnelPred(rel) {
				continue
			}
			scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				name := canonicalOSWriteCall(pkg.TypesInfo, call)
				if name == "" {
					return
				}
				diags = append(diags, scanner.Diagnostic{
					Rel:  rel,
					Line: pkg.Fset.Position(call.Pos()).Line,
					Message: "SCAFFOLD-WRITE-FUNNEL-01: direct os." + name +
						" call — must funnel through pkg/pathsafe.WritePlannedFiles",
				})
			})
		}
	}
	scanner.Report(t, "SCAFFOLD-WRITE-FUNNEL-01", diags)
}
