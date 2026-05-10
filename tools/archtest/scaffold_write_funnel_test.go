// INVARIANT: SCAFFOLD-WRITE-FUNNEL-01
//
// All scaffold/codegen filesystem writes funnel through
// pkg/pathsafe.WritePlannedFiles. Direct os.MkdirAll / os.WriteFile in
// scaffold paths is statically forbidden; pathsafe enforces:
//   - root containment (ResolveRoot + ContainPath)
//   - all-or-nothing conflict detection (no partial bundles)
//   - atomic write with rollback (no half-written state)
//
// AI-rebust: Hard. Bypass requires (a) modifying this archtest's allowlist
// AND (b) introducing a new os.* call in a scaffold path — both visible in
// diff review. The funnel itself is the typed function call defense; the
// archtest defense layer prevents accidental drift through new os imports.
//
// Cannot be Soft: this is real-source AST scan (scanner.EachFile) with
// concrete-package allowlist, not string anchor or comment exemption.
//
// Extension contract: when adding a new scaffold sub-package that writes
// files, add it to the scope in TestScaffoldWriteFunnel_NoDirectOSWrites
// and update this comment.
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
//   - kernel/assembly/               (Generator.Scaffold)
//   - cmd/gocell/app/scaffold*.go    (scaffoldSlice, scaffoldContract, scaffoldJourney)
//
// Allowlist (only these files may call banned os selectors):
//   - pkg/pathsafe/pathsafe.go
func TestScaffoldWriteFunnel_NoDirectOSWrites(t *testing.T) {
	t.Parallel()

	repoRoot := repoRootFromTestPath(t)

	// bannedSelectors は os パッケージ内の書き込み系関数名
	bannedSelectors := map[string]bool{
		"MkdirAll":  true,
		"WriteFile": true,
		"Mkdir":     true,
		"Create":    true,
		"OpenFile":  true,
	}

	// cmd/gocell/app 内では scaffold*.go だけを対象にする
	scaffoldOnlyPred := scanner.MatchRels(func(rel string) bool {
		base := filepath.Base(rel)
		// cmd/gocell/app/ 下のファイルは scaffold プレフィックスのものだけ
		if strings.HasPrefix(rel, "cmd/gocell/app/") {
			return strings.HasPrefix(base, "scaffold") && !strings.HasSuffix(base, "_test.go")
		}
		// 他のディレクトリは _test.go 以外全ファイル対象
		return !strings.HasSuffix(base, "_test.go")
	})

	scope := scanner.DirsScope(repoRoot, []string{
		"tools/codegen/cellgen",
		"kernel/assembly",
		"cmd/gocell/app",
	}, scaffoldOnlyPred)

	var violations []string

	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		ast.Inspect(fc.File, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name != "os" {
				return true
			}
			if bannedSelectors[sel.Sel.Name] {
				pos := fc.Fset.Position(call.Lparen)
				violations = append(violations, fc.Rel+":"+strconv.Itoa(pos.Line)+": os."+sel.Sel.Name+"(...)")
			}
			return true
		})
	})

	if len(violations) > 0 {
		t.Fatalf("SCAFFOLD-WRITE-FUNNEL-01: direct os write calls found in scaffold paths.\n"+
			"All writes must go through pkg/pathsafe.WritePlannedFiles.\n"+
			"Violations:\n  %s", strings.Join(violations, "\n  "))
	}
}

