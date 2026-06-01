// INVARIANT: MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01
//
// mqtt_dlx_failure_signal_funnel_test.go — the alertable dead-letter outcome
// signal in (*Subscriber).routeDeadLetter can never be silently dropped.
//
// # Why this exists (gh #1356)
//
// MQTT has no broker-native dead-letter exchange, so adapters/mqtt routes a
// permanently-rejected / poison message to the app-level sink "$dead/<topic>".
// When that $dead publish ITSELF fails (topic unmintable / broker publish error)
// the message is acked-as-poison and dropped from the processing flow — a
// deliberate fail-closed-and-drop (Kafka Connect KIP-298 model), because the
// only no-loss alternative on MQTT (leave-unacked → reconnect-redeliver) would
// reintroduce the paho strict-PUBACK-ordering head-of-line stall that ADR-050 §6
// (Option C) avoids, and would infinitely re-loop a permanent Reject.
//
// Because the message IS dropped on $dead failure, the ONLY operator-recovery
// hook is the alertable metric mqtt_dlx_failed_total (RecordDeadLetterFailure).
// If a future edit removed that metric call from any drop path, the drop would
// become silent and unrecoverable. This invariant makes that recovery signal a
// machine-enforced contract: every exit path of routeDeadLetter MUST record a
// dead-letter outcome — RecordDeadLetter on the success path, or
// RecordDeadLetterFailure on every drop path. No silent exit.
//
// This is the enforceable spine of the #1356 resolution: literal no-loss is
// unreachable at the MQTT transport layer (proven against Watermill / Spring
// Kafka / Kafka Connect / autopaho — all achieve no-loss via a durable substrate
// MQTT lacks), so the adapter contract is fail-closed-drop + alertable signal,
// and THAT signal is what we lock here. True no-loss is relocated to the
// consumer-cell transaction layer (deferred; see ADR-048 §threat-matrix amend).
//
// # The invariant (A1)
//
// In (*Subscriber).routeDeadLetter every *ast.ReturnStmt must be preceded —
// within the statement list of its directly-enclosing *ast.BlockStmt — by a call
// resolving to SubscriberCollector.RecordDeadLetterFailure OR
// SubscriberCollector.RecordDeadLetter. routeDeadLetter is a void method whose
// success path falls through the end (no ReturnStmt), so every explicit return is
// an early/drop exit; the success-metric alternative in the predicate only
// future-proofs an explicit success `return`. A return with neither preceding
// metric is a silent exit → diagnostic.
//
// Covered form: metric-call-then-return in the SAME block (the idiom in
// deadletter.go). A refactor that hoists the metric to an ancestor block before
// the if is reported (fail-closed: keep the signal adjacent to the return). That
// is a false-positive on a valid-but-unusual shape, NOT a missed bypass: the only
// way to PASS is to have a metric call before the return in its block, so a
// genuine silent drop (return with no metric anywhere) is always caught.
//
// # AI-robust grading (per .claude/rules/gocell/ai-robust.md)
//
// Medium. This is a single-axis structural invariant (a function-body
// control-shape guard), NOT a funnel — there is no caller-allowlist downstream /
// sealed-interface upstream split, so the §Funnel 双向锁评级 two-column format
// does not apply. The detector is type-aware: callee identity is resolved via
// go/types (ResolveMethodCall → *types.Func.FullName), so an import alias or a
// same-named method on a different type does not match. Enforcement is
// archtest-bound (Go cannot make "every exit records a metric" unrepresentable),
// hence Medium not Hard.
//
// Hard-upgrade path: collapse the drop decision into a single typed sink helper
// (e.g. routeDeadLetter returns a typed Outcome that the caller must record, or a
// `func dropPoison(ctx, reason)` that wraps log+metric+return so the metric is
// structurally inseparable from the drop). Then the metric call becomes
// unrepresentable to omit. Tracked at gh #1356 follow-up (recorded in this
// package godoc per ai-robust.md §Funnel 双向锁评级 "点名 issue 号").
//
// # Blind-spot inventory (per ai-robust.md §载体决策原则 强制盲区自检)
//
// The A1 scanner matches a metric call only as an *ast.ExprStmt wrapping a
// *ast.CallExpr with a *ast.SelectorExpr callee. AST forms outside that coverage,
// each with a reverse self-check below:
//
//   - B1 method-value: `f := s.collector.RecordDeadLetterFailure; f(ctx, r)` —
//     the call site `f(...)` has no SelectorExpr callee, so A1 would not credit
//     it. B1 asserts no method-value of RecordDeadLetter / RecordDeadLetterFailure
//     is taken (non-call SelectorExpr) inside routeDeadLetter.
//   - Non-vacuity: A1NonVacuous asserts ≥2 ReturnStmt are seen and
//     SignalsPresent asserts ≥1 RecordDeadLetter and ≥1 RecordDeadLetterFailure
//     callsite resolve inside routeDeadLetter — so an empty/broken scan (wrong
//     FullName, types resolution down) cannot vacuously pass.
//
// ref: gh #1356 — MQTT DLT no-loss (this invariant is the resolution's enforce spine)
// ref: gh #1334 — bounded $dead retry (deferred, data-driven; out of scope here)
// ref: ADR docs/architecture/202605281200-048-adr-mqtt-adapter.md §threat matrix ($dead publish failure row)
// ref: ADR docs/architecture/202605301200-050-adr-mqtt-requeue-semantics.md §6 (HoL / Option C)
// ref: ai-robust.md §AI-robust 三档分级 (Medium structural invariant)
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// routeDeadLetterFuncName is the method whose exit paths must each record a
// dead-letter outcome metric.
const routeDeadLetterFuncName = "routeDeadLetter"

// SubscriberCollector method FullName()s resolved via ResolveMethodCall. These
// are the two dead-letter outcome signals; RecordDeadLetterFailure is the
// alertable drop signal (mqtt_dlx_failed_total), RecordDeadLetter the success
// signal (mqtt_dlx_total).
const (
	recordDLXFailureFullName = "(github.com/ghbvf/gocell/adapters/mqtt.SubscriberCollector).RecordDeadLetterFailure"
	recordDLXSuccessFullName = "(github.com/ghbvf/gocell/adapters/mqtt.SubscriberCollector).RecordDeadLetter"
)

// dlxSignalMethodNames is the selector-name set used by the B1 method-value
// blind-spot scan (name-level reverse self-check; defense-in-depth).
var dlxSignalMethodNames = map[string]bool{
	"RecordDeadLetter":        true,
	"RecordDeadLetterFailure": true,
}

// findRouteDeadLetter returns the FuncDecl of (*Subscriber).routeDeadLetter in
// the given production file, or nil. Identity is confirmed via go/types: the
// resolved object's FullName must be the routeDeadLetter method on *Subscriber,
// so a same-named free function elsewhere does not match.
func findRouteDeadLetter(p *Pass, f *ast.File) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != routeDeadLetterFuncName || fd.Body == nil {
			continue
		}
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		obj := p.TypesInfo.Defs[fd.Name]
		fn, ok := obj.(*types.Func)
		if !ok {
			continue
		}
		if strings.HasSuffix(fn.FullName(), ".Subscriber).routeDeadLetter") {
			return fd
		}
	}
	return nil
}

// dlxStmtIsOutcomeMetric reports whether stmt is `<recv>.RecordDeadLetter(...)`
// or `<recv>.RecordDeadLetterFailure(...)` resolving (via go/types) to the
// SubscriberCollector method — i.e., a dead-letter outcome signal.
func dlxStmtIsOutcomeMetric(info *types.Info, stmt ast.Stmt) bool {
	es, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil {
		return false
	}
	full := fn.FullName()
	return full == recordDLXFailureFullName || full == recordDLXSuccessFullName
}

// scanRouteDeadLetterReturns walks every BlockStmt in routeDeadLetter and, for
// each ReturnStmt, checks that some earlier statement in the SAME block is a
// dead-letter outcome metric. Returns diagnostics for silent returns plus the
// total ReturnStmt count (for the non-vacuous companion).
func scanRouteDeadLetterReturns(p *Pass, f *ast.File, fd *ast.FuncDecl) ([]Diagnostic, int) {
	var diags []Diagnostic
	var returnCount int
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range block.List {
			if _, ok := stmt.(*ast.ReturnStmt); !ok {
				continue
			}
			returnCount++
			signaled := false
			for _, prev := range block.List[:i] {
				if dlxStmtIsOutcomeMetric(p.TypesInfo, prev) {
					signaled = true
					break
				}
			}
			if signaled {
				continue
			}
			pos := p.Fset.Position(stmt.Pos())
			rel := p.Rel(f)
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/A1: return in routeDeadLetter at %s:%d "+
						"is not preceded (same block) by RecordDeadLetterFailure/RecordDeadLetter — "+
						"a $dead drop path with no alertable recovery signal is a silent message loss",
					rel, pos.Line,
				),
			})
		}
		return true
	})
	return diags, returnCount
}

// countRouteDeadLetterSignals returns the number of resolved RecordDeadLetter
// and RecordDeadLetterFailure callsites inside routeDeadLetter.
func countRouteDeadLetterSignals(p *Pass, fd *ast.FuncDecl) (success, failure int) {
	EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok || fn == nil {
			return
		}
		switch fn.FullName() {
		case recordDLXSuccessFullName:
			success++
		case recordDLXFailureFullName:
			failure++
		}
	})
	return success, failure
}

// withRouteDeadLetter loads the adapters/mqtt production package and invokes fn
// with the routeDeadLetter FuncDecl (and its file/Pass). It fails the test if
// routeDeadLetter is not found, so a rename cannot silently disable the rule.
func withRouteDeadLetter(t *testing.T, fn func(p *Pass, f *ast.File, fd *ast.FuncDecl)) {
	t.Helper()
	found := false
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				fd := findRouteDeadLetter(p, f)
				if fd == nil {
					continue
				}
				found = true
				fn(p, f, fd)
			}
			return nil
		})
	assert.True(t, found,
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01: (*Subscriber).routeDeadLetter not found in adapters/mqtt "+
			"production AST — the rule target was renamed/removed; update this archtest")
}

// ─── A1: every routeDeadLetter return records a dead-letter outcome ───────────

func TestMQTTDLXFailureSignalFunnel_A1_NoSilentReturn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	withRouteDeadLetter(t, func(p *Pass, f *ast.File, fd *ast.FuncDecl) {
		diags, _ := scanRouteDeadLetterReturns(p, f, fd)
		assert.Empty(t, diags,
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/A1: routeDeadLetter has a return path with no "+
				"preceding dead-letter outcome metric (silent $dead drop)")
	})
}

func TestMQTTDLXFailureSignalFunnel_A1_NonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	withRouteDeadLetter(t, func(p *Pass, f *ast.File, fd *ast.FuncDecl) {
		_, returnCount := scanRouteDeadLetterReturns(p, f, fd)
		assert.GreaterOrEqual(t, returnCount, 2,
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/A1: scanner found <2 returns in routeDeadLetter — "+
				"there are two drop branches (mint failure + publish failure); the AST walk may be broken")
	})
}

// ─── A2: both outcome signals are wired (non-vacuous resolution) ──────────────

func TestMQTTDLXFailureSignalFunnel_A2_SignalsPresent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	withRouteDeadLetter(t, func(p *Pass, _ *ast.File, fd *ast.FuncDecl) {
		success, failure := countRouteDeadLetterSignals(p, fd)
		assert.GreaterOrEqual(t, failure, 1,
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/A2: no resolved RecordDeadLetterFailure callsite in "+
				"routeDeadLetter — the alertable drop signal is missing or the FullName drifted")
		assert.GreaterOrEqual(t, success, 1,
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/A2: no resolved RecordDeadLetter callsite in "+
				"routeDeadLetter — the success signal is missing or the FullName drifted")
	})
}

// ─── B1: method-value blind-spot reverse self-check ──────────────────────────

func TestMQTTDLXFailureSignalFunnel_B1_NoMethodValue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	withRouteDeadLetter(t, func(p *Pass, f *ast.File, fd *ast.FuncDecl) {
		// Collect SelectorExprs used as a call callee (allowed); any OTHER
		// SelectorExpr selecting a dlx signal method is a method-value escape.
		callFun := map[*ast.SelectorExpr]bool{}
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				callFun[sel] = true
			}
		})
		var diags []Diagnostic
		EachInSubtree[ast.SelectorExpr](fd.Body, func(sel *ast.SelectorExpr) {
			if sel.Sel == nil || !dlxSignalMethodNames[sel.Sel.Name] || callFun[sel] {
				return
			}
			pos := p.Fset.Position(sel.Pos())
			rel := p.Rel(f)
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/B1: dead-letter signal method %q taken as a "+
						"method-value at %s:%d — A1 cannot credit an indirect call; call it directly",
					sel.Sel.Name, rel, pos.Line,
				),
			})
		})
		assert.Empty(t, diags,
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/B1: dead-letter signal method taken as a method-value")
	})
}
