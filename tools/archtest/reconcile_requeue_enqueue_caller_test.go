// INVARIANT: RECONCILE-REQUEUE-ENQUEUE-CALLER-01
//
// This file owns ONE invariant: every channel-send statement (SendStmt) that
// writes to the Loop's internal work queue or delaying-queue input channel in
// kernel/reconcile must be enclosed in one of the SANCTIONED functions; no
// stray per-entity goroutine (`go func(){ queue <- req }()`) may send to those
// channels outside the approved dispatch path.
//
// # Background
//
// PR-A5 replaced the old scheduleRequeue (which spawned one goroutine per
// requeue) with a single shared delaying queue (F6) driven by ONE waitingLoop
// goroutine. The three sanctioned send sites are:
//
//   - drainReadyItems (package-level func): drains heap items into queue.
//   - (*Loop).feedFromSource: copies Source into queue (external feed goroutine).
//   - (*Loop).enqueueDelayed: sends a waitingItem to addCh (the delaying-queue
//     input). Called by dispatchResult and by process's dirty-re-run path;
//     it is the SOLE funnel from process/dispatchResult into the delaying queue.
//
// process() and dispatchResult() do NOT send directly — they call enqueueDelayed,
// which is the sanctioned send site.
//
// # What this invariant prevents
//
// A future edit that reintroduces `go func(){ addCh <- item }()` or
// `go func(){ queue <- req }()` directly inside process/dispatchResult would
// re-fragment the single-goroutine invariant and undo the goleak-clean F6
// property. This archtest catches that form.
//
// # AI-robust rating
//
// Medium. Mechanism: AST scan — for each FuncDecl in kernel/reconcile
// production files, walk its ENTIRE body (descending into nested FuncLit /
// goroutine-closure bodies) for SendStmt nodes; assert each enclosing FuncDecl
// identity is in the sanctioned allowlist. Descending into FuncLit is essential:
// the stray-goroutine form this invariant bans — `go func(){ addCh <- item }()`
// — lives inside a closure, so a scan that stopped at FuncLit boundaries would
// never see it (the false-negative this rule was hardened against).
//
// Honest ceiling: Go cannot express "exactly one goroutine drives the delaying
// queue" or "sends must traverse backoff" at compile time. The achievable
// ceiling is this Medium archtest + the goleak single-waitingLoop regression
// test in loop_test.go (TestLoop_SharedWaitingLoopNoLeak). Same permanent-ceiling shape as
// SPAN-SETATTR-HOLDER-SEAL-01 (#851) / HEALTHZ-HOLDER-SEAL-01 (#893).
//
// Hard upgrade path: privatize the `queue` and `addCh` channels behind an
// interface whose Send method is the only write surface, and seal construction
// so only waitingLoop can hold the send-end. That would make "calling Send
// from outside the sanctioned goroutine" a type-system violation (Hard). Tracked
// at gh #1418.
//
// # Blind spots (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - reflect-based channel send (via reflect.Value.Send): out of scope, accepted
//     as a theoretical gap — the kernel/reconcile package does not use reflect.
//     Reverse self-check: grep for "reflect.Value.Send" in kernel/reconcile is
//     vacuous (none found); confirmed by the package-scope production scan.
//
//   - Sending via a passed-in channel PARAMETER to a helper function: if a future
//     helper func takes `ch chan<- Request` and calls `ch <- req`, the channel
//     is structurally the queue but the SendStmt's Chan is a parameter Ident, not
//     one of the known channel names. This test does NOT resolve through parameter
//     aliases — it detects the enclosing FuncDecl, so as long as that helper is
//     named in the allowlist or is a new out-of-allowlist function it will be
//     caught/flagged respectively. Current production has no such helpers; documented
//     as a potential coverage gap for future helpers.
//
//   - SendStmt inside a FuncLit: the scan DESCENDS into FuncLit bodies and credits
//     each send to its enclosing FuncDecl, so `go func(){ addCh <- item }()` inside
//     an unsanctioned function (e.g. a reintroduced stray per-entity goroutine in
//     process/dispatchResult) IS flagged. The two Trigger producers
//     (tickerTrigger.Start / channelTrigger.Start) send to `queue` inside goroutine
//     closures and are LEGITIMATE external producers — they are explicitly listed
//     in reconcileRequeueSanctionedSet rather than excluded by a FuncLit-stop trick.
//     TestReconcileRequeueEnqueueCaller01_RedClosureSend proves a closure send in an
//     unsanctioned function is caught (the prior FuncLit-stop scan missed it).
//
// # Non-vacuous proof
//
// TestReconcileRequeueEnqueueCaller01_NonVacuousProof asserts that the production
// scan finds at least as many SendStmt callsites as there are sanctioned functions
// (currently 3), confirming the detector matched real production code.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// reconcileRequeueSanctionedSet is the allowlist of (receiverType, funcName)
// pairs permitted to contain a SendStmt that writes to the Loop's work queue or
// delaying-queue input channel. The receiver type is "" for package-level
// functions.
//
// Sanctioned set (PR-A5, loop.go + trigger.go). Because the scan descends into
// FuncLit bodies (so a goroutine-closure send is credited to its enclosing
// FuncDecl), the two Trigger producers — whose sends live inside `go func(){…}`
// — are explicitly listed here rather than being silently invisible:
//   - ("", "drainReadyItems"):          package-level helper — drains heap into queue.
//   - ("Loop", "feedFromSource"):       goroutine body — copies Source into queue.
//   - ("Loop", "enqueueDelayed"):       sole send funnel into addCh.
//   - ("Loop", "enqueueCancel"):        sole send funnel into cancelCh (permanent dead-letter).
//   - ("tickerTrigger", "Start"):       external producer — ticks resync into queue.
//   - ("channelTrigger", "Start"):      external producer — forwards a chan into queue.
var reconcileRequeueSanctionedSet = map[reconcileSendSite]bool{
	{recv: "", name: "drainReadyItems"}:     true,
	{recv: "Loop", name: "feedFromSource"}:  true,
	{recv: "Loop", name: "enqueueDelayed"}:  true,
	{recv: "Loop", name: "enqueueCancel"}:   true,
	{recv: "tickerTrigger", name: "Start"}:  true,
	{recv: "channelTrigger", name: "Start"}: true,
}

// reconcileSendSite identifies a function or method by its receiver type name
// (empty for package-level funcs) and its declared name.
type reconcileSendSite struct {
	recv string // base receiver type name, "" for package-level funcs
	name string
}

// reconcileSendSiteOf extracts the (recv, name) identity from a FuncDecl.
// Receiver type is derived from the first receiver's type expression using
// ReceiverTypeName (handles *T / T forms). Returns the zero value when fd is nil.
func reconcileSendSiteOf(fd *ast.FuncDecl) reconcileSendSite {
	if fd == nil {
		return reconcileSendSite{}
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return reconcileSendSite{name: fd.Name.Name}
	}
	recv := ReceiverTypeName(fd.Recv.List[0].Type)
	return reconcileSendSite{recv: recv, name: fd.Name.Name}
}

// scanReconcileRequeueEnqueueCallers walks all FuncDecl bodies in p's files
// (production files only, _test.go excluded), finds SendStmt nodes within each
// FuncDecl (descending into nested FuncLit bodies), and emits a diagnostic for
// any SendStmt whose enclosing FuncDecl is not in the sanctioned allowlist.
//
// Returns (diagnostics, totalSendStmtsFound) so the caller can assert non-vacuity.
func scanReconcileRequeueEnqueueCallers(p *Pass) ([]Diagnostic, int) {
	var diags []Diagnostic
	var totalFound int

	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		fileDiags, found := reconcileSendDiagsForFile(p.Fset, file, p.Rel(file))
		diags = append(diags, fileDiags...)
		totalFound += found
	}
	return diags, totalFound
}

// reconcileSendDiagsForFile is the pure-AST core of the scan (no types.Info
// needed): for every top-level FuncDecl it walks the WHOLE body — INCLUDING
// nested FuncLit bodies — for SendStmt nodes and credits each to the enclosing
// FuncDecl. A send inside `go func(){ addCh <- item }()` is therefore credited
// to the surrounding function: if that function is not sanctioned, the stray
// goroutine send is flagged (this is the form the invariant exists to ban).
// Go has no nested FuncDecls, so every SendStmt belongs to exactly one FuncDecl
// — no double counting. Returns (diagnostics, totalSendStmtsFound).
func reconcileSendDiagsForFile(fset *token.FileSet, file *ast.File, rel string) ([]Diagnostic, int) {
	var diags []Diagnostic
	var totalFound int
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return
		}
		site := reconcileSendSiteOf(fd)
		EachInSubtree[ast.SendStmt](fd.Body, func(send *ast.SendStmt) {
			totalFound++
			if !reconcileRequeueSanctionedSet[site] {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: fset.Position(send.Pos()).Line,
					Message: "channel send in unsanctioned function " +
						reconcileSendSiteName(site) +
						" — add to reconcileRequeueSanctionedSet or " +
						"route through enqueueDelayed (RECONCILE-REQUEUE-ENQUEUE-CALLER-01)",
				})
			}
		})
	})
	return diags, totalFound
}

// reconcileSendSiteName formats a reconcileSendSite for human-readable messages.
func reconcileSendSiteName(s reconcileSendSite) string {
	if s.recv == "" {
		return s.name
	}
	return "(*" + s.recv + ")." + s.name
}

// TestReconcileRequeueEnqueueCaller01 is the production GREEN baseline: every
// channel send in kernel/reconcile is inside one of the sanctioned functions.
func TestReconcileRequeueEnqueueCaller01(t *testing.T) {
	t.Parallel()

	const reconcilePkg = PlatformModulePath + "/kernel/reconcile"

	var allDiags []Diagnostic
	RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != reconcilePkg {
			return nil
		}
		diags, _ := scanReconcileRequeueEnqueueCallers(p)
		allDiags = append(allDiags, diags...)
		return nil
	})
	Report(t, "RECONCILE-REQUEUE-ENQUEUE-CALLER-01", allDiags)
}

// TestReconcileRequeueEnqueueCaller01_NonVacuousProof asserts the detector
// actually found SendStmt nodes in the production kernel/reconcile package — i.e.
// the scan is not vacuously passing because it matched nothing.
//
// We assert totalFound >= len(sanctioned set) because each sanctioned function
// contains at least one send, so a working detector must find ≥ 3 sends in the
// sanctioned functions. If this assertion fails, either the function names changed
// (update the allowlist) or the production code no longer sends on those channels
// (design change — reconsider the invariant).
func TestReconcileRequeueEnqueueCaller01_NonVacuousProof(t *testing.T) {
	t.Parallel()

	const reconcilePkg = PlatformModulePath + "/kernel/reconcile"

	var totalFound int
	var foundInSanctioned int
	RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != reconcilePkg {
			return nil
		}
		_, total := scanReconcileRequeueEnqueueCallers(p)
		totalFound += total

		// Count sends in each sanctioned function directly (to confirm matching).
		for _, file := range p.Files {
			if strings.HasSuffix(p.Rel(file), "_test.go") {
				continue
			}
			EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Body == nil {
					return
				}
				site := reconcileSendSiteOf(fd)
				if !reconcileRequeueSanctionedSet[site] {
					return
				}
				EachInSubtree[ast.SendStmt](fd.Body, func(_ *ast.SendStmt) {
					foundInSanctioned++
				})
			})
		}
		return nil
	})

	// One send each in the six sanctioned functions: drainReadyItems,
	// feedFromSource, enqueueDelayed, enqueueCancel, tickerTrigger.Start,
	// channelTrigger.Start (the last two are FuncLit-closure sends now credited
	// via the descend scan).
	const wantMinSends = 6
	if foundInSanctioned < wantMinSends {
		t.Errorf("RECONCILE-REQUEUE-ENQUEUE-CALLER-01 non-vacuous proof: "+
			"found %d SendStmt(s) in sanctioned functions, want ≥ %d. "+
			"Either the sanctioned function names changed (update reconcileRequeueSanctionedSet) "+
			"or the production sends were removed (reconsider the invariant). "+
			"Total SendStmts in package (including unsanctioned): %d",
			foundInSanctioned, wantMinSends, totalFound)
	}

	if totalFound == 0 {
		t.Errorf("RECONCILE-REQUEUE-ENQUEUE-CALLER-01 non-vacuous proof: " +
			"found 0 SendStmt nodes in kernel/reconcile — the scan is vacuous " +
			"(package path changed or all sends removed). " +
			"Check that the reconcilePkg constant matches the actual package path.")
	}
}

// reconcileSendDiagsForSrc parses src and runs the pure-AST send scan, returning
// the diagnostic count. Helper for the RED/GREEN closure-send controls.
func reconcileSendDiagsForSrc(t *testing.T, src string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err)
	diags, _ := reconcileSendDiagsForFile(fset, f, "fixture.go")
	return len(diags)
}

// TestReconcileRequeueEnqueueCaller01_RedClosureSend is the RED control: a stray
// `go func(){ addCh <- item }()` inside an UNSANCTIONED function must be flagged.
// The prior FuncLit-stop scan missed this exact form (the false negative F3
// hardened); this proves the descend-into-FuncLit scan catches it.
func TestReconcileRequeueEnqueueCaller01_RedClosureSend(t *testing.T) {
	t.Parallel()
	const red = `package reconcile
type Loop struct{}
func (l *Loop) strayDispatch(addCh chan int, item int) {
	go func() { addCh <- item }()
}`
	if got := reconcileSendDiagsForSrc(t, red); got != 1 {
		t.Fatalf("RED closure-send control: want 1 diagnostic for the stray goroutine send, got %d "+
			"(the descend-into-FuncLit scan must flag a closure send in an unsanctioned function)", got)
	}
}

// TestReconcileRequeueEnqueueCaller01_GreenClosureSend is the GREEN over-fire
// guard: a closure send inside a SANCTIONED function (Trigger producer) must NOT
// be flagged.
func TestReconcileRequeueEnqueueCaller01_GreenClosureSend(t *testing.T) {
	t.Parallel()
	const green = `package reconcile
type tickerTrigger struct{}
func (t *tickerTrigger) Start(queue chan int) {
	go func() { queue <- 1 }()
}`
	if got := reconcileSendDiagsForSrc(t, green); got != 0 {
		t.Fatalf("GREEN closure-send control: want 0 diagnostics for a sanctioned Trigger producer's "+
			"closure send, got %d", got)
	}
}
