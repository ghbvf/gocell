// RMQ-STOPINTAKE-INFLIGHT-WAIT-01 — Subscriber.StopIntake must wait for in-flight
// processDelivery goroutines to settle before returning, so callers that follow
// StopIntake with Close() do not race with active broker ack/nack work.
//
// Gate IDs:
//
//	RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A  StopIntake body must call run.localWg.Wait
//	                                    (or equivalent run.wgDone helper) on every
//	                                    snapshotted run before returning.
//	RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B  drainRemaining body must NOT contain a
//	                                    bare `case <-ctx.Done()` arm; drain runs
//	                                    on a detached context bounded by
//	                                    currentDrainDeadline so prefetch is
//	                                    fully drained even if the parent ctx
//	                                    is canceled mid-shutdown.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: ThreeDotsLabs/watermill subscriber.Close — wg.Wait inside close path
// ref: rabbitmq/amqp091-go channel.go — Cancel→drain→wg.Wait→ch.Close ordering
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestRMQStopIntakeInflightWait01_StopIntakeWaitsForInflight(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "subscriber.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var stopIntake *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "StopIntake" && fd.Recv != nil {
			stopIntake = fd
			break
		}
	}
	if stopIntake == nil {
		t.Fatalf("RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A: StopIntake method not found in %s", src)
	}

	// Look for an inflight-wait sentinel in the function body. Accept any of:
	//   - a call ending in `.localWg.Wait()` / `.inflightWg.Wait()` (waitgroup style)
	//   - a call to `run.waitInflight(...)` / `r.waitInflight(...)` helper
	//   - a call to a wgDone() helper on a subscriptionRun
	//   - a call to `inflightCount()` / `r.inflightCount()` (atomic-poll style)
	//   - a call to the package-level `waitInflightDrain(...)` helper
	//
	// The atomic-poll style is the canonical implementation today: it avoids
	// the Add-after-Wait race that direct localWg.Wait suffers when
	// drainRemaining concurrently calls registerDelivery (= Add(1)). The Wait
	// helpers are kept in the accepted set so that future refactors that
	// re-introduce a wait-style API (e.g. behind a sync.Cond) still satisfy
	// the gate without needing to update this test.
	var found bool
	ast.Inspect(stopIntake.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Bare identifier call form, e.g. `waitInflightDrain(...)`.
		if id, ok := call.Fun.(*ast.Ident); ok {
			if id.Name == "waitInflightDrain" {
				found = true
				return false
			}
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Wait":
			// Accept any selector ending in localWg.Wait or inflightWg.Wait.
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if inner.Sel.Name == "localWg" || inner.Sel.Name == "inflightWg" {
				found = true
				return false
			}
		case "waitInflight", "waitDrained", "wgDone", "inflightCount":
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
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A: StopIntake in %s must wait for in-flight "+
				"processDelivery goroutines (run.localWg.Wait / run.waitInflight / run.wgDone) "+
				"before returning, otherwise Close() can race with active broker I/O.",
			rel,
		)
	}
}

func TestRMQStopIntakeInflightWait01_DrainNoParentCtxDone(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "subscriber.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var drain *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "drainRemaining" && fd.Recv != nil {
			drain = fd
			break
		}
	}
	if drain == nil {
		t.Fatalf("RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining method not found in %s", src)
	}

	// Reject any select case clause containing `<-ctx.Done()`. Drain must run on
	// a detached context (context.WithoutCancel) so a parent ctx cancel does not
	// silently drop prefetched-but-unacked deliveries.
	var violations []token.Pos
	ast.Inspect(drain.Body, func(n ast.Node) bool {
		comm, ok := n.(*ast.CommClause)
		if !ok {
			return true
		}
		// CommClause.Comm is one of: SendStmt, AssignStmt, ExprStmt (for receive-only).
		// The "case <-ctx.Done():" appears as ExprStmt with UnaryExpr Op=ARROW
		// and X=CallExpr(ctx.Done).
		expr, ok := comm.Comm.(*ast.ExprStmt)
		if !ok {
			return true
		}
		unary, ok := expr.X.(*ast.UnaryExpr)
		if !ok || unary.Op != token.ARROW {
			return true
		}
		call, ok := unary.X.(*ast.CallExpr)
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
		if ident.Name == "ctx" && sel.Sel.Name == "Done" {
			violations = append(violations, comm.Pos())
		}
		return true
	})

	for _, p := range violations {
		t.Errorf(
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining at %s contains `case <-ctx.Done()`; "+
				"drain MUST run on a detached context (context.WithoutCancel) bounded by "+
				"currentDrainDeadline timer, otherwise parent ctx cancel drops prefetched messages.",
			fset.Position(p),
		)
	}
}

// Cross-check: verify drainRemaining (or the caller that derives its ctx,
// consumeLoop) creates a detached context via context.WithoutCancel so the
// test above cannot be satisfied by simply removing the ctx.Done arm while
// still passing the parent ctx unchanged. We accept the WithoutCancel call in
// either drainRemaining or consumeLoop because either is the legitimate
// boundary (consumeLoop derives drain ctx, drainRemaining receives it).
func TestRMQStopIntakeInflightWait01_DrainUsesDetachedContext(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "subscriber.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	bodyHasWithoutCancel := func(body *ast.BlockStmt) bool {
		var found bool
		ast.Inspect(body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == "context" && sel.Sel.Name == "WithoutCancel" {
				found = true
				return false
			}
			return true
		})
		return found
	}

	var found bool
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil {
			continue
		}
		if fd.Name.Name != "drainRemaining" && fd.Name.Name != "consumeLoop" {
			continue
		}
		if bodyHasWithoutCancel(fd.Body) {
			found = true
			break
		}
	}

	if !found {
		rel, _ := filepath.Rel(root, src)
		if rel == "" {
			rel = src
		}
		t.Errorf(
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining or consumeLoop in %s must "+
				"use `context.WithoutCancel` to derive the drain ctx, so prefetched "+
				"deliveries are processed independently of the parent ctx cancel.",
			strings.TrimPrefix(rel, "./"),
		)
	}
}

// TestRMQStopIntakeInflightWait01_StopIntakeAvoidsLocalWgWait reinforces 01-A
// by inverting the assertion: StopIntake's body must NOT contain a textual
// `localWg.Wait()` call. The Add-after-Wait race is fundamentally caused by
// invoking Wait while drainRemaining can still register new deliveries; the
// only correct shape today is to poll inflightCount(). 01-A already accepts
// inflightCount, but a future refactor that adds a Wait alongside the poll
// would silently re-introduce the race without tripping 01-A. This negative
// test closes that loophole.
func TestRMQStopIntakeInflightWait01_StopIntakeAvoidsLocalWgWait(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "subscriber.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var stopIntake *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "StopIntake" && fd.Recv != nil {
			stopIntake = fd
			break
		}
	}
	if stopIntake == nil {
		t.Fatalf("StopIntake method not found in %s", src)
	}

	rel, _ := filepath.Rel(root, src)
	if rel == "" {
		rel = src
	}
	ast.Inspect(stopIntake.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wait" {
			return true
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if inner.Sel.Name != "localWg" {
			return true
		}
		t.Errorf(
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01: %s:%s — StopIntake body must not call "+
				"localWg.Wait(); poll inflightCount() instead. drainRemaining "+
				"concurrently calls localWg.Add(1) on every prefetched delivery, "+
				"and Wait racing that Add panics with "+
				"\"WaitGroup misuse: Add called concurrently with Wait\".",
			rel, fset.Position(call.Pos()),
		)
		return true
	})
}
