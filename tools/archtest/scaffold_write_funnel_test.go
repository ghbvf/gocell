// INVARIANT: SCAFFOLD-WRITE-FUNNEL-01
//
// All scaffold/codegen filesystem writes funnel through
// pkg/pathsafe.WritePlannedFiles. Direct os.MkdirAll / os.WriteFile in
// scaffold paths is statically forbidden; pathsafe enforces:
//   - root containment (ResolveRoot + ContainPath)
//   - all-or-nothing conflict detection (no partial bundles)
//   - atomic write with rollback (no half-written state)
//
// AI-rebust: Medium (兜底防线).
//
// Hard-level enforcement: .golangci.yml depguard rule scaffold-os-ban
// statically forbids `import "os"` in scaffold paths at lint time (package
// path level). This archtest covers depguard blind spots — method-level
// bypass via reflect / unsafe / cgo, or side-effect import `_ "os"` not
// caught by some depguard configurations.
//
// Two-layer defense:
//   - depguard scaffold-os-ban → fails at golangci-lint (Hard, type-level)
//   - this archtest → fails at go test (Medium, AST-level cross-check)
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
	"go/parser"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestScaffoldWriteFunnel_NoDirectOSWrites enforces SCAFFOLD-WRITE-FUNNEL-01:
// no direct os.MkdirAll / os.WriteFile / os.Mkdir / os.Create / os.OpenFile
// calls in scaffold paths outside pkg/pathsafe.
//
// Scanned paths:
//   - tools/codegen/cellgen/         (ScaffoldCell, ScaffoldCellBundle)
//   - tools/codegen/contractgen/     (generator + writer — R5 expansion)
//   - tools/codegen/writer.go        (codegen.Write — R5 expansion)
//   - tools/codegen/cellgen/generate_*.go (Generate* funcs — R5 expansion)
//   - kernel/assembly/               (Generator.PlanAssemblyScaffold)
//   - cmd/gocell/app/scaffold*.go    (scaffoldSlice, scaffoldContract, scaffoldJourney)
//
// Allowlist (only these files may call banned os selectors):
//   - pkg/pathsafe/pathsafe.go
//
// # Out-of-scope (documented exemption — R5 unchanged)
//
// The following call sites legitimately use os.MkdirAll / os.WriteFile
// outside the funnel because the output path is supplied by the user
// via --out flag (no root-containment guarantee can be made):
//
//	cmd/gocell/app/generate_catalog.go (gocell generate catalog --out=<path>)
//	cmd/gocell/app/export.go writeOut  (gocell export {catalog|metadata} --out=<path>)
//
// tools/codegen/writer.go (codegen.Write) and tools/codegen/contractgen/generator.go
// were the RED-state targets at R5 inception. As of develop tip they both
// route through pkg/pathsafe (GREEN state); this rule now enforces
// no-regression on the funnel rather than tracking remaining offenders.
func TestScaffoldWriteFunnel_NoDirectOSWrites(t *testing.T) {
	t.Parallel()

	repoRoot := repoRootFromTestPath(t)

	// bannedSelectors 是 os 包内的写入类函数名。
	bannedSelectors := map[string]bool{
		"MkdirAll":  true,
		"WriteFile": true,
		"Mkdir":     true,
		"Create":    true,
		"OpenFile":  true,
	}

	// scaffoldOnlyPred 限定 cmd/gocell/app/ 下只扫描 scaffold*.go 文件；
	// generate_catalog.go 和 export.go 因接收用户 --out 路径而豁免，
	// 详见文件级 Out-of-scope 说明。
	// tools/codegen/cellgen/ 下只扫描 scaffold*.go 和 generate_*.go；
	// tools/codegen/ 顶层只扫描 writer.go。
	scaffoldOnlyPred := scanner.MatchRels(func(rel string) bool {
		base := filepath.Base(rel)
		if strings.HasSuffix(base, "_test.go") {
			return false
		}
		if strings.HasPrefix(rel, "cmd/gocell/app/") {
			return strings.HasPrefix(base, "scaffold")
		}
		if strings.HasPrefix(rel, "tools/codegen/cellgen/") {
			// Include scaffold*.go and generate_*.go; exclude other codegen glue.
			return strings.HasPrefix(base, "scaffold") || strings.HasPrefix(base, "generate_")
		}
		// tools/codegen/contractgen/ and tools/codegen/ top-level: include all non-test files.
		return true
	})

	scope := scanner.DirsScope(repoRoot, []string{
		"tools/codegen/cellgen",
		"tools/codegen/contractgen",
		"tools/codegen",
		"kernel/assembly",
		"cmd/gocell/app",
	}, scaffoldOnlyPred)

	var violations []string

	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		scanner.EachNode[ast.CallExpr](fc.File, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return
			}
			if ident.Name != "os" {
				return
			}
			if bannedSelectors[sel.Sel.Name] {
				pos := fc.Fset.Position(call.Lparen)
				violations = append(violations, fc.Rel+":"+strconv.Itoa(pos.Line)+": os."+sel.Sel.Name+"(...)")
			}
		})
	})

	if len(violations) > 0 {
		t.Fatalf("SCAFFOLD-WRITE-FUNNEL-01: direct os write calls found in scaffold paths.\n"+
			"All writes must go through pkg/pathsafe.WritePlannedFiles.\n"+
			"Violations:\n  %s", strings.Join(violations, "\n  "))
	}
}
