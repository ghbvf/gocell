// RMQ-PUBLISHER-FAILURE-HANDLING-01 — Publisher must distinguish broker NACK
// from confirm timeout / confirm-channel-close, emit slog.Warn on each failure
// branch with structured fields, and record an observability counter so the
// adapter layer's wire-level failure cause is queryable independently of the
// outbox relay's lost-counter.
//
// Gate IDs:
//
//	RMQ-PUBLISHER-FAILURE-HANDLING-01-A  Publish must reference ErrAdapterAMQPNack
//	                                      somewhere in its body (NACK errcode is
//	                                      distinct from ErrAdapterAMQPConfirmTimeout).
//	RMQ-PUBLISHER-FAILURE-HANDLING-01-B  Publish must call slog.Warn at least 3 times
//	                                      (one for each of NACK / timeout / confirmCh
//	                                      closed) — proves all failure branches are
//	                                      logged, not just one.
//	RMQ-PUBLISHER-FAILURE-HANDLING-01-C  Publish must call a publisher-collector
//	                                      RecordPublishFailure method at least once
//	                                      so a metric records the failure reason.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: ThreeDotsLabs/watermill-amqp publisher.go — NACK returns hard error
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestRMQPublisherFailureHandling01_NackErrcodeReferenced(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-A: Publish method not found in %s", src)
	}

	var found bool
	ast.Inspect(publish.Body, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "ErrAdapterAMQPNack" {
			found = true
			return false
		}
		return true
	})

	if !found {
		rel, _ := filepath.Rel(root, src)
		if rel == "" {
			rel = src
		}
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-A: Publish in %s must reference "+
				"ErrAdapterAMQPNack to mark broker-NACK as a distinct error code (vs "+
				"ErrAdapterAMQPConfirmTimeout). Sharing a code makes alerting rules "+
				"unable to tell broker rejection from network timeout.",
			rel,
		)
	}
}

func TestRMQPublisherFailureHandling01_AllBranchesEmitWarn(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-B: Publish method not found in %s", src)
	}

	const requiredWarnCalls = 3
	var warnCount int
	ast.Inspect(publish.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Warn" {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "slog" {
			warnCount++
		}
		return true
	})

	if warnCount < requiredWarnCalls {
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-B: Publish in %s must call slog.Warn at "+
				"least %d times (NACK / confirm timeout / confirm-channel-closed); "+
				"found %d. Silent failure branches make on-call diagnosis impossible.",
			src, requiredWarnCalls, warnCount,
		)
	}
}

func TestRMQPublisherFailureHandling01_RecordsFailureMetric(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-C: Publish method not found in %s", src)
	}

	var calls int
	ast.Inspect(publish.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "RecordPublishFailure" {
			calls++
		}
		return true
	})

	if calls < 1 {
		rel, _ := filepath.Rel(root, src)
		if rel == "" {
			rel = src
		}
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-C: Publish in %s must call "+
				"RecordPublishFailure on the injected PublisherCollector so the failure "+
				"reason is queryable as a metric. Defaulting to NoopPublisherCollector "+
				"keeps the call cheap; production wiring injects the provider-backed "+
				"collector at the composition root.",
			rel,
		)
	}
}

// TestRMQPublisherFailureHandling01_AllReturnsMustRecord verifies that every
// non-success, non-exempt return in Publish is preceded in its enclosing block
// by a RecordPublishFailure call.
//
// Exemptions (not required to record):
//   - The final success `return nil` (no error, no metric needed)
//   - Any return inside a `ctx.Done()` select case (caller-initiated cancel,
//     documented as not a wire-level failure)
//   - The early "publisher is closed" return (precedes wg.Add; not a wire failure)
//
// This prevents a future developer from adding a new failure branch and
// forgetting to record the failure metric — a regression that would create a
// silent gap in the alerting coverage.
func TestRMQPublisherFailureHandling01_AllReturnsMustRecord(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-D: Publish method not found in %s", src)
	}

	// Walk all if-blocks and select-cases in Publish. For each error-returning
	// block (contains at least one non-nil return), verify it also contains a
	// RecordPublishFailure call somewhere in that block.
	// ctx.Done() cases and nil returns are exempt.
	var violations []string

	var checkIfBlock func(ifStmt *ast.IfStmt, inCtxDone bool)

	var checkStmt func(stmt ast.Stmt, inCtxDone bool)
	checkStmt = func(stmt ast.Stmt, inCtxDone bool) {
		switch s := stmt.(type) {
		case *ast.IfStmt:
			checkIfBlock(s, inCtxDone)
		case *ast.SelectStmt:
			for _, c := range s.Body.List {
				if comm, ok := c.(*ast.CommClause); ok {
					isCtxDone := inCtxDone || isCtxDoneCase(comm)
					for _, cs := range comm.Body {
						checkStmt(cs, isCtxDone)
					}
				}
			}
		case *ast.BlockStmt:
			for _, inner := range s.List {
				checkStmt(inner, inCtxDone)
			}
		}
	}

	checkIfBlock = func(ifStmt *ast.IfStmt, inCtxDone bool) {
		if ifStmt == nil || ifStmt.Body == nil {
			return
		}
		body := ifStmt.Body.List

		// Does this if-block contain a non-nil return?
		hasNonNilReturn := false
		for _, s := range body {
			if ret, ok := s.(*ast.ReturnStmt); ok && !isNilReturn(ret) {
				hasNonNilReturn = true
				break
			}
		}

		// Exempt: if-block guarding the "publisher is closed" early exit.
		// This is not a wire-level failure, so no metric is required.
		// Detected by checking if the condition references a field/method named "closed".
		isClosedGuard := ifCondRefersTo(ifStmt.Cond, "closed")

		if hasNonNilReturn && !inCtxDone && !isClosedGuard {
			// Does this block contain a RecordPublishFailure call?
			hasRecord := blockContainsRecordPublishFailure(body)
			if !hasRecord {
				// Find the first non-nil return for error reporting.
				for _, s := range body {
					if ret, ok := s.(*ast.ReturnStmt); ok && !isNilReturn(ret) {
						pos := fset.Position(ret.Pos())
						violations = append(violations,
							fmt.Sprintf("line %d: if-block with error return has no RecordPublishFailure", pos.Line))
						break
					}
				}
			}
		}

		// Recurse into nested if/select within this block.
		for _, inner := range body {
			checkStmt(inner, inCtxDone)
		}

		// Check else branch.
		if ifStmt.Else != nil {
			checkStmt(ifStmt.Else, inCtxDone)
		}
	}

	// Walk top-level statements of Publish body to find if-blocks.
	for _, stmt := range publish.Body.List {
		checkStmt(stmt, false)
	}

	for _, v := range violations {
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-D: Publish in %s: %s. "+
				"All error-returning if-blocks must contain collector.RecordPublishFailure "+
				"so alerting rules can observe the failure reason without log-parsing. "+
				"Exemptions: success `return nil` and returns inside ctx.Done() case.",
			src, v,
		)
	}
}

// isCtxDoneCase returns true if the CommClause is a `case <-ctx.Done():` arm.
func isCtxDoneCase(cc *ast.CommClause) bool {
	if cc.Comm == nil {
		return false
	}
	// Looking for: case <-ctx.Done():
	// Which is an ExprStmt containing a UnaryExpr (<-) of a CallExpr (ctx.Done()).
	recv, ok := cc.Comm.(*ast.ExprStmt)
	if !ok {
		return false
	}
	unary, ok := recv.X.(*ast.UnaryExpr)
	if !ok || unary.Op != token.ARROW {
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
	ast.Inspect(cond, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if id.Name == name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// blockContainsRecordPublishFailure returns true if any statement in stmts
// (at any nesting level) is a call to RecordPublishFailure.
func blockContainsRecordPublishFailure(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		var found bool
		ast.Inspect(s, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "RecordPublishFailure" {
				found = true
				return false
			}
			return true
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
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == name && fd.Recv != nil {
			return fd
		}
	}
	return nil
}
