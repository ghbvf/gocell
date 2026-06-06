package archtest

// rmq_invariants.go — importable RabbitMQ adapter rule logic.
//
// This is the non-test home of the RMQ-* scanner helpers so they can be
// compiled by external Cell repositories through the CellRule pattern (Go
// never compiles a dependency's _test.go, so rule logic that external repos
// must run cannot live in a _test.go file). GoCell's own TestRMQ* functions
// (rmq_invariants_test.go) call the same shared helpers — single source, no
// parallel rule body.
//
// register=no — gocell-internal-layout (scans adapters/rabbitmq); vacuous-pass
// externally; migrated for unified PlatformModulePath parameterization +
// fork-safety, dogfooded via the per-rule Tests.
//
// Platform-symbol paths are anchored to [PlatformModulePath] so a module
// rename updates exactly one place and no bare literal appears here. The scan
// SCOPE (./adapters/rabbitmq/...) is the running module's own adapter package;
// this is intentionally gocell-internal-layout and vacuous-pass in an external
// repo that has no such adapter tree.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/ghbvf/gocell/tools/typesutil"
)

// rmqAdapterPkgPath is the canonical import path of the rabbitmq adapter
// package — derived from PlatformModulePath so no bare literal appears here.
const rmqAdapterPkgPath = PlatformModulePath + "/adapters/rabbitmq"

// ---------------------------------------------------------------------------
// RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01
// ---------------------------------------------------------------------------

// allowedChannelCloseFuncs is the exhaustive whitelist of function names that
// are permitted to contain a direct ch.Close() call on an AMQPChannel.
// All other sites must call Connection.CloseEphemeralChannel instead.
var allowedChannelCloseFuncs = map[string]bool{
	// CloseEphemeralChannel is the canonical single-path API itself.
	"CloseEphemeralChannel": true,
	// waitAndClose contains a nil-conn guard (r.conn == nil branch) that calls
	// r.ch.Close() directly for unit tests that construct subscriptionRun without
	// a real Connection. The guard is unreachable in production (subscribeOnce
	// always passes s.conn). The archtest whitelist entry is intentional and
	// narrowly scoped to this one function.
	"waitAndClose": true,
}

// lookupInterfaceTypeFromPkg resolves a top-level interface declaration by name
// from a *types.Package and returns its types.Interface. Fail-closed when the
// type vanishes or is no longer an interface.
func lookupInterfaceTypeFromPkg(t *testing.T, pkg *types.Package, name string) *types.Interface {
	t.Helper()
	obj := pkg.Scope().Lookup(name)
	if obj == nil {
		t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s not declared in %s", name, pkg.Path())
	}
	tn, ok := obj.(*types.TypeName)
	if !ok {
		t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s is not a type", name)
	}
	iface, ok := tn.Type().Underlying().(*types.Interface)
	if !ok {
		t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s is not an interface", name)
	}
	return iface
}

// checkFileForDirectChannelClose walks every function in f and flags any
// `Close()` call whose receiver type implements AMQPChannel — regardless of
// the receiver variable's name. AMQPConnection close calls slip through
// because AMQPConnection's method set (4 methods) is a strict subset of
// AMQPChannel's (16 methods), so types.Implements rejects it.
func checkFileForDirectChannelClose(
	t *testing.T,
	p *Pass,
	f *ast.File,
	chanIface *types.Interface,
	rel string,
) {
	t.Helper()

	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return
		}
		funcName := fd.Name.Name
		if allowedChannelCloseFuncs[funcName] {
			return
		}

		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Close" {
				return
			}
			recvType := p.TypesInfo.TypeOf(sel.X)
			if recvType == nil {
				return
			}
			if !typesutil.ImplementsInterface(recvType, chanIface) {
				return
			}

			pos := p.Fset.Position(call.Pos())
			receiverHint := receiverHint(sel.X)
			t.Errorf(
				"RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s:%d: %s() contains direct %s.Close() call (receiver type %s implements AMQPChannel).\n"+
					"  All AMQPChannel destruction MUST go through Connection.CloseEphemeralChannel\n"+
					"  to keep inUseChannels in sync with MaxChannelsPerConn.\n"+
					"  Replace: %s.Close() → conn.CloseEphemeralChannel(%s)",
				rel, pos.Line, funcName, receiverHint, recvType.String(), receiverHint, receiverHint,
			)
		})
	})
}

// receiverHint reproduces the source-level receiver expression for use in
// error messages only. Decisions never depend on this string.
func receiverHint(x ast.Expr) string {
	switch e := x.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return receiverHint(e.X) + "." + e.Sel.Name
	}
	return "<expr>"
}

// ---------------------------------------------------------------------------
// RMQ-CHANNEL-MAX-PER-CONN-01
// ---------------------------------------------------------------------------

const expectedDefaultMaxChannelsPerConnConst = "defaultRMQMaxChannelsPerConn"

// ---------------------------------------------------------------------------
// RMQ-PUBLISHER-FAILURE-HANDLING-01
// ---------------------------------------------------------------------------

// scanPublishMissingFailureRecord walks every if-block and select-case in the
// Publish FuncDecl. For each block with a non-nil return that lacks a
// RecordPublishFailure call, it appends a "line N: ..." violation string.
// Exemptions: ctx.Done() cases, nil returns, and the publisher-closed guard.
//
// Extracted to file-level so PR445-FU finding F3's RED sub-test can exercise
// the same checker against fixture files without duplicating the closure
// logic. The behavior is unchanged from the prior in-test closure form;
// Wave 4 extends checkPublishStmtViolations' switch to cover ForStmt,
// RangeStmt, SwitchStmt, TypeSwitchStmt and inlines SelectStmt's CommClause
// iteration to direct-child semantics.
func scanPublishMissingFailureRecord(publish *ast.FuncDecl, fset *token.FileSet) []string {
	var violations []string
	for _, stmt := range publish.Body.List {
		checkPublishStmtViolations(stmt, fset, false, &violations)
	}
	return violations
}

// checkPublishStmtViolations recursively walks stmt searching for if-blocks
// whose top-level Body.List contains an error return without a paired
// RecordPublishFailure call. The switch covers every Go statement container
// that may legally contain an *ast.IfStmt: BlockStmt and the explicit
// container forms (For, Range, Switch, TypeSwitch, Select). The block-list and
// case-clause recursion are factored into recursePublishStmtList /
// recursePublishCaseClauses so each arm stays ≤1 statement (no //nolint).
func checkPublishStmtViolations(stmt ast.Stmt, fset *token.FileSet, inCtxDone bool, violations *[]string) {
	switch s := stmt.(type) {
	case *ast.IfStmt:
		checkPublishIfBlockViolations(s, fset, inCtxDone, violations)
	case *ast.SelectStmt:
		if s.Body != nil {
			EachInChildren[ast.CommClause](s.Body, func(comm *ast.CommClause) {
				recursePublishStmtList(comm.Body, fset, inCtxDone || isCtxDoneCase(comm), violations)
			})
		}
	case *ast.BlockStmt:
		recursePublishStmtList(s.List, fset, inCtxDone, violations)
	case *ast.ForStmt:
		if s.Body != nil {
			recursePublishStmtList(s.Body.List, fset, inCtxDone, violations)
		}
	case *ast.RangeStmt:
		if s.Body != nil {
			recursePublishStmtList(s.Body.List, fset, inCtxDone, violations)
		}
	case *ast.SwitchStmt:
		recursePublishCaseClauses(s.Body, fset, inCtxDone, violations)
	case *ast.TypeSwitchStmt:
		recursePublishCaseClauses(s.Body, fset, inCtxDone, violations)
	}
}

// recursePublishStmtList recurses checkPublishStmtViolations over each statement
// in list. Shared by the BlockStmt / ForStmt / RangeStmt / SelectStmt arms.
func recursePublishStmtList(list []ast.Stmt, fset *token.FileSet, inCtxDone bool, violations *[]string) {
	for _, inner := range list {
		checkPublishStmtViolations(inner, fset, inCtxDone, violations)
	}
}

// recursePublishCaseClauses recurses over every CaseClause body in a switch /
// type-switch block. Nil body is a no-op (matches the original `if s.Body != nil`
// guard). Shared by the SwitchStmt / TypeSwitchStmt arms.
func recursePublishCaseClauses(body *ast.BlockStmt, fset *token.FileSet, inCtxDone bool, violations *[]string) {
	if body == nil {
		return
	}
	EachInChildren[ast.CaseClause](body, func(cc *ast.CaseClause) {
		recursePublishStmtList(cc.Body, fset, inCtxDone, violations)
	})
}

// checkPublishIfBlockViolations is the per-if-block worker for the
// RMQ-PUBLISHER-FAILURE-HANDLING-01-D scan. See scanPublishMissingFailureRecord.
func checkPublishIfBlockViolations(ifStmt *ast.IfStmt, fset *token.FileSet, inCtxDone bool, violations *[]string) {
	if ifStmt == nil || ifStmt.Body == nil {
		return
	}
	body := ifStmt.Body.List

	// Does THIS if-block (top-level Body.List only) contain a non-nil
	// return? Nested returns inside an inner for/if/select are this
	// block's child statements' concern — checkStmt will recurse and
	// reach them via the inner if/select being its own checkIfBlock /
	// SelectStmt handler. Counting them here would double-attribute the
	// violation to two ancestor blocks. FindFirstChild visits only direct
	// children of ifStmt.Body (depth-1).
	_, hasNonNilReturn := FindFirstChild[ast.ReturnStmt](ifStmt.Body, func(ret *ast.ReturnStmt) bool {
		return !isNilReturn(ret)
	})

	// Exempt: if-block guarding the "publisher is closed" early exit.
	// This is not a wire-level failure, so no metric is required.
	// Detected by checking if the condition references a field/method named "closed".
	isClosedGuard := ifCondRefersTo(ifStmt.Cond, "closed")

	if hasNonNilReturn && !inCtxDone && !isClosedGuard {
		// Does this block contain a RecordPublishFailure call?
		hasRecord := blockContainsRecordPublishFailure(body)
		if !hasRecord {
			// Report the first top-level non-nil return.
			if ret, found := FindFirstChild[ast.ReturnStmt](ifStmt.Body, func(ret *ast.ReturnStmt) bool {
				return !isNilReturn(ret)
			}); found {
				pos := fset.Position(ret.Pos())
				*violations = append(*violations,
					fmt.Sprintf("line %d: if-block with error return has no RecordPublishFailure", pos.Line))
			}
		}
	}

	// Recurse into nested if/select within this block.
	for _, inner := range body {
		checkPublishStmtViolations(inner, fset, inCtxDone, violations)
	}

	// Check else branch.
	if ifStmt.Else != nil {
		checkPublishStmtViolations(ifStmt.Else, fset, inCtxDone, violations)
	}
}

// isCtxDoneCase returns true if the CommClause is a `case <-ctx.Done():` or
// `case v := <-ctx.Done():` arm (both ExprStmt and AssignStmt forms).
func isCtxDoneCase(cc *ast.CommClause) bool {
	if cc.Comm == nil {
		return false
	}
	var unary *ast.UnaryExpr
	switch comm := cc.Comm.(type) {
	case *ast.ExprStmt:
		// case <-ctx.Done():
		u, ok := comm.X.(*ast.UnaryExpr)
		if !ok || u.Op != token.ARROW {
			return false
		}
		unary = u
	case *ast.AssignStmt:
		// case v := <-ctx.Done():
		if len(comm.Rhs) != 1 {
			return false
		}
		u, ok := comm.Rhs[0].(*ast.UnaryExpr)
		if !ok || u.Op != token.ARROW {
			return false
		}
		unary = u
	default:
		return false
	}
	call, ok := unary.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "ctx" && sel.Sel.Name == "Done"
}

// isNilReturn returns true if the ReturnStmt returns a single nil literal.
func isNilReturn(ret *ast.ReturnStmt) bool {
	if len(ret.Results) != 1 {
		return false
	}
	ident, ok := ret.Results[0].(*ast.Ident)
	return ok && ident.Name == "nil"
}

// ifCondRefersTo returns true if the condition expression contains an identifier
// or selector with the given name. Used to detect exempted guard patterns like
// `if p.closed.Load()` without full type resolution.
func ifCondRefersTo(cond ast.Expr, name string) bool {
	var found bool
	EachInSubtree[ast.Ident](cond, func(id *ast.Ident) {
		if id.Name == name {
			found = true
		}
	})
	return found
}

// blockContainsRecordPublishFailure returns true if any statement in stmts
// (at any nesting level) is a call to RecordPublishFailure.
func blockContainsRecordPublishFailure(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		var found bool
		EachInSubtree[ast.CallExpr](s, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "RecordPublishFailure" {
				found = true
			}
		})
		if found {
			return true
		}
	}
	return false
}

// findMethod returns the FuncDecl for a method whose name matches `name` and
// which has a non-nil receiver. Returns nil if not found.
//
//nolint:unparam // name is "Publish" in all callers; kept as param for readability
func findMethod(f *ast.File, name string) *ast.FuncDecl {
	var result *ast.FuncDecl
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if result == nil && fd.Name.Name == name && fd.Recv != nil {
			result = fd
		}
	})
	return result
}
