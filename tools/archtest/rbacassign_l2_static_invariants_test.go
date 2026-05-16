// INVARIANT: RBACASSIGN-L2-STATIC-01
//
// # RBACASSIGN-L2-STATIC-01
//
// The rbacassign slice registration in `cells/accesscore/cell_init.go` must
// declare consistency level as the literal `cellvocab.L2` (selector
// expression). It must NOT be a variable, conditional expression, or any
// other Level value.
//
// Why: rbacassign emits role.assigned/role.revoked outbox facts inside a
// transactional outbox. That is by definition L2 OutboxFact — the slice's
// behavioral contract is independent of demo-vs-durable runtime. Other L2
// slices in accesscore (sessionlogin, sessionlogout, setup) all use
// `cellvocab.L2` literally. A historic L0/L2 toggle via the
// `c.rbacEmitterMode` flag (removed by S4c-T1) drifted slice consistency
// level from its true behavioral contract; this archtest locks rbacassign
// to L2 literal so a future contributor cannot reintroduce the same
// outlier shape.
//
// AI-rebust grade: Medium (type-aware AST literal lock).
// Hard upgrade path: when kernel/cell introduces a typed factory
// `cell.NewL2SliceWithEmit(name, cellID, emitter)` with mandatory emitter
// argument, the rule degrades to a compile-time constraint. Tracked as a
// trigger-type backlog item (no follow-up issue today).
//
// Blind-spot inventory:
//   - Adding a *second* NewBaseSlice("rbacassign", ...) call elsewhere —
//     out of scope; only the existing call in cell_init.go is locked.
//   - Renaming cellvocab.L2 → cellvocab.OutboxFact — would be caught at
//     compile (production code can't reference both); this archtest
//     would then be updated as part of the rename PR.
//   - Indirection through a constant alias `const myL2 = cellvocab.L2;
//     NewBaseSlice(..., myL2)` — explicitly rejected: the third argument
//     must be the *direct* selector expression `cellvocab.L2`.
//
// ref: docs/plans/202605082145-034-pg-corecell-b-route-plan.md §S4c T1
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// rbacassignCellInitPath is the file under inspection (single-file rule).
const rbacassignCellInitPath = "cells/accesscore/cell_init.go"

// TestRBACASSIGN_L2_STATIC_01 scans cell_init.go and fails if the rbacassign
// AddSlice call uses anything other than the `cellvocab.L2` literal as the
// consistency-level argument.
func TestRBACASSIGN_L2_STATIC_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	path := filepath.Join(root, rbacassignCellInitPath)
	fset := token.NewFileSet()
	violations := scanForRbacassignLevelLiteral(t, fset, path, rbacassignCellInitPath)
	for _, v := range violations {
		t.Errorf("RBACASSIGN-L2-STATIC-01: %s", v)
	}
}

// scanForRbacassignLevelLiteral parses the given file and returns violation
// strings if any cell.NewBaseSlice("rbacassign", ...) call has a third
// argument that is not exactly the selector `cellvocab.L2`.
func scanForRbacassignLevelLiteral(t *testing.T, fset *token.FileSet, path, rel string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}

	var matched bool
	var violations []string
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if !isNewBaseSliceCall(call) {
			return
		}
		if len(call.Args) != 3 {
			return
		}
		firstLit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || firstLit.Kind != token.STRING {
			return
		}
		if strings.Trim(firstLit.Value, `"`) != "rbacassign" {
			return
		}
		matched = true
		levelArg := call.Args[2]
		sel, ok := levelArg.(*ast.SelectorExpr)
		if !ok {
			pos := fset.Position(levelArg.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: third argument is not a selector expression (got %T) — "+
					"must be `cellvocab.L2` literal",
				rel, pos.Line, levelArg,
			))
			return
		}
		xIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			pos := fset.Position(levelArg.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: third argument selector base is not a simple Ident — "+
					"must be `cellvocab.L2` literal",
				rel, pos.Line,
			))
			return
		}
		if xIdent.Name != "cellvocab" || sel.Sel.Name != "L2" {
			pos := fset.Position(levelArg.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: third argument is `%s.%s`, must be exactly `cellvocab.L2`",
				rel, pos.Line, xIdent.Name, sel.Sel.Name,
			))
		}
	})

	if !matched {
		violations = append(violations, fmt.Sprintf(
			"%s: no cell.NewBaseSlice(\"rbacassign\", ...) call found — rule scope drifted",
			rel,
		))
	}
	return violations
}

// isNewBaseSliceCall returns true when call is `cell.NewBaseSlice(...)`.
// Detects the form `<ident>.NewBaseSlice(...)` where <ident> is named "cell"
// (the conventional import alias for kernel/cell in accesscore).
func isNewBaseSliceCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return xIdent.Name == "cell" && sel.Sel.Name == "NewBaseSlice"
}

// TestRBACASSIGN_L2_STATIC_01_RedFixture_VariableLevel verifies the scanner
// flags the legacy form where the third argument is a variable carrying
// the level (the form S4c-T1 deletes).
func TestRBACASSIGN_L2_STATIC_01_RedFixture_VariableLevel(t *testing.T) {
	t.Parallel()
	src := `package p
type Cell struct{}
type Level int
func (Cell) AddSlice(any) {}
var c Cell
var cell = struct {
	NewBaseSlice func(string, string, Level) any
}{
	NewBaseSlice: func(string, string, Level) any { return nil },
}
func init() {
	rbacAssignLevel := Level(0)
	c.AddSlice(cell.NewBaseSlice("rbacassign", "accesscore", rbacAssignLevel))
}
`
	violations := scanSrcForRbacassignLevelLiteral(t, src, rbacassignCellInitPath)
	if len(violations) == 0 {
		t.Errorf("RED fixture (variable level arg) must produce a violation; got 0")
	}
}

// TestRBACASSIGN_L2_STATIC_01_RedFixture_WrongLevel verifies the scanner
// flags cellvocab.L0 (or any non-L2 literal).
func TestRBACASSIGN_L2_STATIC_01_RedFixture_WrongLevel(t *testing.T) {
	t.Parallel()
	src := `package p
type Cell struct{}
func (Cell) AddSlice(any) {}
var c Cell
var cellvocab = struct{ L0, L2 int }{L0: 0, L2: 2}
var cell = struct {
	NewBaseSlice func(string, string, int) any
}{
	NewBaseSlice: func(string, string, int) any { return nil },
}
func init() {
	c.AddSlice(cell.NewBaseSlice("rbacassign", "accesscore", cellvocab.L0))
}
`
	violations := scanSrcForRbacassignLevelLiteral(t, src, rbacassignCellInitPath)
	if len(violations) == 0 {
		t.Errorf("RED fixture (cellvocab.L0) must produce a violation; got 0")
	}
}

// TestRBACASSIGN_L2_STATIC_01_GreenFixture_L2Literal verifies the scanner
// does NOT flag the canonical L2 literal form.
func TestRBACASSIGN_L2_STATIC_01_GreenFixture_L2Literal(t *testing.T) {
	t.Parallel()
	src := `package p
type Cell struct{}
func (Cell) AddSlice(any) {}
var c Cell
var cellvocab = struct{ L0, L2 int }{L0: 0, L2: 2}
var cell = struct {
	NewBaseSlice func(string, string, int) any
}{
	NewBaseSlice: func(string, string, int) any { return nil },
}
func init() {
	c.AddSlice(cell.NewBaseSlice("rbacassign", "accesscore", cellvocab.L2))
}
`
	violations := scanSrcForRbacassignLevelLiteral(t, src, rbacassignCellInitPath)
	if len(violations) != 0 {
		t.Errorf("GREEN fixture (cellvocab.L2) must produce 0 violations; got %d: %v",
			len(violations), violations)
	}
}

// scanSrcForRbacassignLevelLiteral writes src to a temp file and runs the
// scanner with the given relative path.
func scanSrcForRbacassignLevelLiteral(t *testing.T, src, rel string) []string {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "rbac_l2_*.go")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := tmp.WriteString(src); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}
	return scanForRbacassignLevelLiteral(t, token.NewFileSet(), tmp.Name(), rel)
}
