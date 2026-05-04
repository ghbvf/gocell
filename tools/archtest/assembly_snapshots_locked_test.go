// ASSEMBLY-SNAPSHOTS-LOCKED-01 — invariant gate.
//
// Invariant: every write to *CoreAssembly.snapshots in kernel/assembly/
// production code must be lexically inside a Lock()/Unlock() critical section
// on the receiver's mu. Detection prevents regression of the fatal map race
// fixed in PR-V1-030-K01-ASSEMBLY-SNAPSHOTS-RACE-FIX (G1-01, review
// 20260504): Phase 1 of startInternal previously wrote a.snapshots[c.ID()]
// without holding a.mu, racing against Snapshots() readers that hold a.mu.
//
// Detection model: walk every FuncDecl/FuncLit body in kernel/assembly/*.go
// (production only) maintaining a `lockDepth` counter. Lock() increments,
// Unlock() decrements; `defer Unlock()` does NOT decrement (the lock is held
// until function exit). Composite literal initializers (`&CoreAssembly{...
// snapshots: make(...)}`) are NOT writes — single-threaded constructor-time
// initialization is exempt by construction.
//
// Flagged statements (when lockDepth == 0):
//   - assignments where any LHS is `<x>.snapshots[...]` (per-key write)
//   - assignments where any LHS is `<x>.snapshots`     (whole-map replace)
//   - calls to delete(<x>.snapshots, ...)              (per-key remove)
//
// Reads of a.snapshots (range, len, indexed read) are not flagged — readers
// already hold the lock in Snapshots(); only racy writers cause map races.
//
// Allow-list: none. New() initializes via composite literal which is not an
// AssignStmt; the gate naturally exempts it without an explicit allow-list.
package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const ruleAssemblySnapshotsLocked01 = "ASSEMBLY-SNAPSHOTS-LOCKED-01"

// TestAssemblySnapshotsLocked enforces ASSEMBLY-SNAPSHOTS-LOCKED-01 by walking
// every production .go file under kernel/assembly/ and flagging unlocked
// writes to *.snapshots.
func TestAssemblySnapshotsLocked(t *testing.T) {
	root := asnFindModuleRoot(t)
	files, err := asnFindAssemblyProductionGoFiles(root)
	if err != nil {
		t.Fatalf("walking kernel/assembly/: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no production .go files found under kernel/assembly/")
	}

	var violations []string
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		vs, err := asnCheckFile(path, rel)
		if err != nil {
			t.Fatalf("parsing %s: %v", rel, err)
		}
		violations = append(violations, vs...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s", v)
	}
}

// TestAssemblySnapshotsLocked_DetectsViolation is a self-test ensuring the
// detector flags an unlocked write. Without this guard a buggy detector
// could silently pass and let the original race regress.
func TestAssemblySnapshotsLocked_DetectsViolation(t *testing.T) {
	src := `package x
type A struct { snapshots map[string]int }
func (a *A) bug() {
    a.snapshots["k"] = 1
}`
	vs, err := asnCheckSource("<fixture-bug>", src)
	if err != nil {
		t.Fatalf("asnCheckSource: %v", err)
	}
	if len(vs) == 0 {
		t.Error("detector did not flag unlocked a.snapshots[k] = ...")
	}
}

// TestAssemblySnapshotsLocked_DetectsWholeMapWrite verifies whole-map
// replacement (`a.snapshots = ...`) outside a lock is flagged.
func TestAssemblySnapshotsLocked_DetectsWholeMapWrite(t *testing.T) {
	src := `package x
type A struct { snapshots map[string]int }
func (a *A) bug() {
    a.snapshots = map[string]int{}
}`
	vs, err := asnCheckSource("<fixture-whole>", src)
	if err != nil {
		t.Fatalf("asnCheckSource: %v", err)
	}
	if len(vs) == 0 {
		t.Error("detector did not flag unlocked whole-map write a.snapshots = ...")
	}
}

// TestAssemblySnapshotsLocked_DetectsUnlockedDelete verifies that an unlocked
// delete(a.snapshots, k) is flagged.
func TestAssemblySnapshotsLocked_DetectsUnlockedDelete(t *testing.T) {
	src := `package x
type A struct { snapshots map[string]int }
func (a *A) bug() {
    delete(a.snapshots, "k")
}`
	vs, err := asnCheckSource("<fixture-delete>", src)
	if err != nil {
		t.Fatalf("asnCheckSource: %v", err)
	}
	if len(vs) == 0 {
		t.Error("detector did not flag unlocked delete(a.snapshots, ...)")
	}
}

// TestAssemblySnapshotsLocked_AllowsLockedWrite verifies the detector treats
// writes inside a Lock()/Unlock() pair as compliant.
func TestAssemblySnapshotsLocked_AllowsLockedWrite(t *testing.T) {
	src := `package x
import "sync"
type A struct {
    mu        sync.Mutex
    snapshots map[string]int
}
func (a *A) ok() {
    a.mu.Lock()
    a.snapshots["k"] = 1
    a.mu.Unlock()
}`
	vs, err := asnCheckSource("<fixture-locked>", src)
	if err != nil {
		t.Fatalf("asnCheckSource: %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("expected no violations for locked write, got: %v", vs)
	}
}

// TestAssemblySnapshotsLocked_AllowsDeferUnlock verifies that
// `defer a.mu.Unlock()` keeps the lock held for the remainder of the body.
func TestAssemblySnapshotsLocked_AllowsDeferUnlock(t *testing.T) {
	src := `package x
import "sync"
type A struct {
    mu        sync.Mutex
    snapshots map[string]int
}
func (a *A) ok() {
    a.mu.Lock()
    defer a.mu.Unlock()
    a.snapshots["k"] = 1
    delete(a.snapshots, "x")
    a.snapshots = map[string]int{}
}`
	vs, err := asnCheckSource("<fixture-defer>", src)
	if err != nil {
		t.Fatalf("asnCheckSource: %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("expected no violations for defer-unlock pattern, got: %v", vs)
	}
}

// TestAssemblySnapshotsLocked_AllowsCompositeLiteralInit verifies that struct
// literal initialization (`&A{snapshots: make(...)}`) is not flagged. This is
// how New() seeds the field without requiring a lock.
func TestAssemblySnapshotsLocked_AllowsCompositeLiteralInit(t *testing.T) {
	src := `package x
type A struct { snapshots map[string]int }
func New() *A {
    return &A{snapshots: map[string]int{}}
}`
	vs, err := asnCheckSource("<fixture-init>", src)
	if err != nil {
		t.Fatalf("asnCheckSource: %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("expected no violations for composite-literal init, got: %v", vs)
	}
}

// TestAssemblySnapshotsLocked_AllowsLockedWriteInsideIf verifies that locks
// taken inside a nested block (e.g. an if-error path) are tracked correctly.
func TestAssemblySnapshotsLocked_AllowsLockedWriteInsideIf(t *testing.T) {
	src := `package x
import "sync"
type A struct {
    mu        sync.Mutex
    snapshots map[string]int
}
func (a *A) ok(err error) {
    if err != nil {
        a.mu.Lock()
        a.snapshots = map[string]int{}
        a.mu.Unlock()
        return
    }
}`
	vs, err := asnCheckSource("<fixture-if>", src)
	if err != nil {
		t.Fatalf("asnCheckSource: %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("expected no violations for if-body locked write, got: %v", vs)
	}
}

// asnCheckFile parses a single file and returns ASSEMBLY-SNAPSHOTS-LOCKED-01
// violations.
func asnCheckFile(path, rel string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	return asnCheckAST(fset, f, rel), nil
}

// asnCheckSource parses a Go source string and returns violations. label is
// used in violation messages in place of a file path.
func asnCheckSource(label, src string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, label, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	return asnCheckAST(fset, f, label), nil
}

func asnCheckAST(fset *token.FileSet, f *ast.File, label string) []string {
	var violations []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		violations = append(violations, asnScanStmts(fset, fn.Body.List, 0, label)...)
	}
	return violations
}

// asnScanStmts walks a flat list of statements with `lockDepth` counter,
// recursing into nested blocks while preserving the parent's lock state.
// Lock() increments depth; Unlock() decrements; defer Unlock() does not
// affect depth (the lock is held until the function returns, after every
// statement in the body has executed).
func asnScanStmts(fset *token.FileSet, stmts []ast.Stmt, lockDepth int, label string) []string {
	var violations []string
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *ast.ExprStmt:
			switch {
			case asnIsLockCall(s.X):
				lockDepth++
			case asnIsUnlockCall(s.X):
				lockDepth--
			default:
				if line, ok := asnUnlockedDeleteSnapshots(s.X, fset, lockDepth); ok {
					violations = append(violations, asnViolation(label, line))
				}
			}
		case *ast.DeferStmt:
			// `defer a.mu.Unlock()` keeps the lock held for the rest of the
			// body; do not change lockDepth here.
		case *ast.AssignStmt:
			if line, ok := asnUnlockedSnapshotsWrite(s, fset, lockDepth); ok {
				violations = append(violations, asnViolation(label, line))
			}
		case *ast.IfStmt:
			if s.Body != nil {
				violations = append(violations, asnScanStmts(fset, s.Body.List, lockDepth, label)...)
			}
			if s.Else != nil {
				violations = append(violations, asnScanStmts(fset, []ast.Stmt{s.Else}, lockDepth, label)...)
			}
		case *ast.ForStmt:
			if s.Body != nil {
				violations = append(violations, asnScanStmts(fset, s.Body.List, lockDepth, label)...)
			}
		case *ast.RangeStmt:
			if s.Body != nil {
				violations = append(violations, asnScanStmts(fset, s.Body.List, lockDepth, label)...)
			}
		case *ast.BlockStmt:
			violations = append(violations, asnScanStmts(fset, s.List, lockDepth, label)...)
		case *ast.SwitchStmt:
			if s.Body != nil {
				violations = append(violations, asnScanStmts(fset, s.Body.List, lockDepth, label)...)
			}
		case *ast.TypeSwitchStmt:
			if s.Body != nil {
				violations = append(violations, asnScanStmts(fset, s.Body.List, lockDepth, label)...)
			}
		case *ast.CaseClause:
			violations = append(violations, asnScanStmts(fset, s.Body, lockDepth, label)...)
		case *ast.SelectStmt:
			if s.Body != nil {
				violations = append(violations, asnScanStmts(fset, s.Body.List, lockDepth, label)...)
			}
		case *ast.CommClause:
			violations = append(violations, asnScanStmts(fset, s.Body, lockDepth, label)...)
		}
	}
	return violations
}

// asnIsLockCall returns true if expr is `<x>.mu.Lock()`.
func asnIsLockCall(expr ast.Expr) bool {
	return asnIsMuMethodCall(expr, "Lock")
}

// asnIsUnlockCall returns true if expr is `<x>.mu.Unlock()`.
func asnIsUnlockCall(expr ast.Expr) bool {
	return asnIsMuMethodCall(expr, "Unlock")
}

// asnIsMuMethodCall returns true if expr is a call of form `<x>.mu.<methodName>()`.
// The .mu suffix limits matches to mutex-shaped accesses; reflection-style
// false positives on identically named methods are not a concern in
// kernel/assembly/.
func asnIsMuMethodCall(expr ast.Expr, methodName string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != methodName {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return inner.Sel.Name == "mu"
}

// asnUnlockedSnapshotsWrite returns (line, true) when assign's LHS targets
// `<x>.snapshots` or `<x>.snapshots[...]` and lockDepth == 0.
func asnUnlockedSnapshotsWrite(assign *ast.AssignStmt, fset *token.FileSet, lockDepth int) (int, bool) {
	if lockDepth > 0 {
		return 0, false
	}
	for _, lhs := range assign.Lhs {
		if asnIsSnapshotsTarget(lhs) {
			return fset.Position(assign.Pos()).Line, true
		}
	}
	return 0, false
}

// asnUnlockedDeleteSnapshots returns (line, true) when expr is
// `delete(<x>.snapshots, ...)` and lockDepth == 0.
func asnUnlockedDeleteSnapshots(expr ast.Expr, fset *token.FileSet, lockDepth int) (int, bool) {
	if lockDepth > 0 {
		return 0, false
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return 0, false
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok || id.Name != "delete" || len(call.Args) == 0 {
		return 0, false
	}
	if asnIsSnapshotsTarget(call.Args[0]) {
		return fset.Position(call.Pos()).Line, true
	}
	return 0, false
}

// asnIsSnapshotsTarget reports whether expr is a SelectorExpr `<x>.snapshots`
// (whole-map) or an IndexExpr whose collection is `<x>.snapshots` (per-key).
func asnIsSnapshotsTarget(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel.Name == "snapshots"
	case *ast.IndexExpr:
		if sel, ok := e.X.(*ast.SelectorExpr); ok {
			return sel.Sel.Name == "snapshots"
		}
	}
	return false
}

// asnViolation formats an ASSEMBLY-SNAPSHOTS-LOCKED-01 violation message.
func asnViolation(file string, line int) string {
	return ruleAssemblySnapshotsLocked01 + ": " + file + ":" + strconv.Itoa(line) +
		": write to *.snapshots without holding mu.Lock(); wrap with " +
		"a.mu.Lock()/a.mu.Unlock() (or rely on defer Unlock). " +
		"ref: PR-V1-030-K01-ASSEMBLY-SNAPSHOTS-RACE-FIX, G1-01"
}

// asnFindModuleRoot walks up from CWD to locate go.mod.
func asnFindModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from working directory")
		}
		dir = parent
	}
}

// asnFindAssemblyProductionGoFiles returns all non-test .go files under
// kernel/assembly/.
func asnFindAssemblyProductionGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "kernel", "assembly"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "worktrees", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}
