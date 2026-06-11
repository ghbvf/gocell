//go:build archtest

// INVARIANT: MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01
//
// mqtt_dlx_failure_signal_funnel_test.go — the alertable dead-letter outcome
// signal in (*Subscriber).routeDeadLetter can never be silently dropped.
//
// # Why this exists (gh #1356, hardened gh #1440)
//
// MQTT has no broker-native dead-letter exchange, so adapters/mqtt routes a
// permanently-rejected / poison message to the app-level sink "$dead/<topic>".
// When that $dead publish ITSELF fails (topic unmintable / broker publish error)
// the message is acked-as-poison and dropped — a deliberate fail-closed-and-drop
// (Kafka Connect KIP-298 model). Because the message IS dropped on $dead failure,
// the ONLY operator-recovery hook is the alertable metric mqtt_dlx_failed_total
// (RecordDeadLetterFailure). If a future edit removed that metric call from a drop
// path, the drop would become silent and unrecoverable.
//
// # The mechanism (Hard, gh #1440)
//
// routeDeadLetter does NOT record the metric inline. It RETURNS a sealed
// dlxoutcome.Outcome whose only producers are dlxoutcome.Dropped (records the
// alertable RecordDeadLetterFailure) and dlxoutcome.Captured (records the success
// RecordDeadLetter). Because routeDeadLetter is typed to return Outcome, Go forces
// every exit path to `return` one — and the only non-forged way to obtain one runs
// a metric. A new drop branch that forgets the metric cannot produce an Outcome to
// return → COMPILE ERROR. "Every exit records a metric" is therefore type-enforced,
// not archtest-enforced. (This relocates the #1356 enforce spine from a same-block
// control-flow scan to the Go type system.)
//
// # AI-robust grading (honest, per .claude/rules/gocell/ai-robust.md)
//
//   - Hard (type system): no-silent-exit. A new routeDeadLetter exit cannot omit
//     the metric without failing to compile (no Outcome to return). This is the
//     highest-severity failure mode (an entire $dead drop going silent) and is what
//     #1440 hardens. H1 pins the return type so a refactor back to a void
//     routeDeadLetter (which removes the compile pressure) is caught.
//   - Medium residual ① (shape guard, H2): the empty composite literal
//     `dlxoutcome.Outcome{}` and a zero `var o dlxoutcome.Outcome` are still
//     constructible from package mqtt — irreducible in Go (no "no zero value"
//     modifier; same ceiling as internal/topicns sealed tokens). H2 bans both forms
//     in mqtt production files.
//   - Medium residual ② (F1 semantic, H3b): the type system forces SOME outcome
//     metric per exit but cannot force the CORRECT one (it does not know which
//     return is a drop). A drop branch wrongly using Captured would record a false
//     success (review F1). H3b pins the `Captured` callsite count == 1 (the single
//     success fall-through) and `Dropped` >= 1; a single-branch swap trips it. The
//     per-path failure/success correctness is also covered behaviorally by
//     deadletter_test.go.
//
// This is NOT a "drop -> failure metric is type-enforced" claim — that part stays
// Medium (H3b + behavioral tests). The ADR must not overclaim.
//
// # Blind-spot inventory (per ai-robust.md 强制盲区自检)
//
//   - empty-literal / zero-var forge → H2 (composite-lit + ValueSpec scan over mqtt
//     production files). Proven non-vacuous by the dlxoutcomeredfixture synthetic
//     red case, which uses a sealed-shape REPLICA (a real-type fixture is impossible:
//     dlxoutcome is an internal package the tools module cannot import — same replica
//     rationale as internal/mqttredfixture).
//   - new(T) allocation forge → `*new(dlxoutcome.Outcome)` / `var o = *new(...)` produce a
//     zero Outcome with neither a composite literal nor a typed zero-var, so H2 does not
//     see them. Irreducible in Go (same ceiling as the empty literal) and equally vacuous;
//     accepted residual, flagged so it is not mistaken for covered (not a closed hole).
//   - constructor gutted to a no-op → H3a (Dropped calls RecordDeadLetterFailure,
//     Captured calls RecordDeadLetter; a metric-less constructor would defeat the
//     seal while still compiling).
//   - H3a name-level match → H3a matches the recording call by selector NAME (rec is an
//     anonymous interface, so FullName is not a stable mqtt symbol). A future homonymous
//     method in the (tiny) dlxoutcome package would yield a false-POSITIVE (over-credit),
//     never a false-negative; H3b + behavioral tests stay valid regardless.
//   - drop credited as success (F1) → H3b + deadletter_test.go behavioral tests.
//
// ref: gh #1356 — MQTT DLT no-loss (this invariant is the resolution's enforce spine)
// ref: gh #1440 — Hard upgrade (drop decision收口为 sealed dlxoutcome.Outcome)
// ref: ADR docs/architecture/202605281200-048-adr-mqtt-adapter.md §threat matrix + Amendment 2026-06-11
// ref: ADR docs/architecture/202605301200-050-adr-mqtt-requeue-semantics.md §6 (HoL / Option C)
// ref: ai-robust.md §Hard 范本 sealed construction
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// mqttDLXOutcomePkgPath is the internal sub-package that owns the sealed
// dlxoutcome.Outcome proof token + its recording constructors (gh #1440).
const mqttDLXOutcomePkgPath = mqttPkgPath + "/internal/dlxoutcome"

// dlxOutcomeRedFixturePkgPath is the archtest_fixture-gated red fixture proving
// the H2 forbidden-construction scanner fires (sealed-shape replica).
const dlxOutcomeRedFixturePkgPath = PlatformModulePath + "/tools/archtest/internal/dlxoutcomeredfixture"

// routeDeadLetterFuncName / routeDeadLetterFullName identify the method whose
// signature (H1) and constructor balance (H3b) are pinned. FullName gives exact
// identity so a same-named method on a Subscriber in another package cannot match.
const (
	routeDeadLetterFuncName = "routeDeadLetter"
	routeDeadLetterFullName = "(*github.com/ghbvf/gocell/adapters/mqtt.Subscriber).routeDeadLetter"
)

// dlxoutcome symbol names. Dropped/Captured are the sole sanctioned Outcome
// producers; Outcome is the sealed return type. recordDLX* are the
// SubscriberCollector method names the constructors must call (H3a).
const (
	dlxOutcomeTypeName     = "Outcome"
	dlxDroppedFuncName     = "Dropped"
	dlxCapturedFuncName    = "Captured"
	recordDLXFailureMethod = "RecordDeadLetterFailure"
	recordDLXSuccessMethod = "RecordDeadLetter"
)

// findRouteDeadLetter returns the FuncDecl of (*Subscriber).routeDeadLetter in
// the given production file, or nil. Identity is confirmed via go/types so a
// same-named free function elsewhere does not match.
func findRouteDeadLetter(p *Pass, f *ast.File) *ast.FuncDecl {
	var result *ast.FuncDecl
	scanner.EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if result != nil {
			return
		}
		if fd.Name == nil || fd.Name.Name != routeDeadLetterFuncName || fd.Body == nil {
			return
		}
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return
		}
		fn, ok := p.TypesInfo.Defs[fd.Name].(*types.Func)
		if !ok {
			return
		}
		if fn.FullName() == routeDeadLetterFullName {
			result = fd
		}
	})
	return result
}

// withRouteDeadLetter loads the adapters/mqtt production package and invokes fn
// with the routeDeadLetter FuncDecl. It fails the test if routeDeadLetter is not
// found, so a rename cannot silently disable the rule.
func withRouteDeadLetter(t *testing.T, fn func(p *Pass, f *ast.File, fd *ast.FuncDecl)) {
	t.Helper()
	found := false
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath}),
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
			"production AST — the rule target was renamed/removed; update routeDeadLetterFuncName + "+
			"routeDeadLetterFullName in this file")
}

// ─── H1: routeDeadLetter returns the sealed dlxoutcome.Outcome ────────────────

func TestMQTTDLXFailureSignalFunnel_H1_ReturnsSealedOutcome(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	withRouteDeadLetter(t, func(p *Pass, _ *ast.File, fd *ast.FuncDecl) {
		fn, ok := p.TypesInfo.Defs[fd.Name].(*types.Func)
		if !assert.True(t, ok, "routeDeadLetter has no *types.Func definition") {
			return
		}
		sig, ok := fn.Type().(*types.Signature)
		if !assert.True(t, ok, "routeDeadLetter is not a *types.Signature") {
			return
		}
		if !assert.Equal(t, 1, sig.Results().Len(),
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H1: routeDeadLetter must return exactly one value "+
				"(dlxoutcome.Outcome) — a void routeDeadLetter removes the compile pressure that makes "+
				"the metric structurally inseparable from the drop (gh #1440)") {
			return
		}
		named, ok := sig.Results().At(0).Type().(*types.Named)
		if !assert.True(t, ok, "routeDeadLetter result is not a named type") {
			return
		}
		obj := named.Obj()
		assert.True(t,
			obj.Pkg() != nil && obj.Pkg().Path() == mqttDLXOutcomePkgPath && obj.Name() == dlxOutcomeTypeName,
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H1: routeDeadLetter must return %s.%s, got %s",
			mqttDLXOutcomePkgPath, dlxOutcomeTypeName, named.String())
	})
}

// ─── H2: no mqtt code forges a dlxoutcome.Outcome ─────────────────────────────

// dlxIsOutcomeType reports whether expr's resolved type is (pkgPath, typeName).
func dlxIsOutcomeType(info *types.Info, expr ast.Expr, pkgPath, typeName string) bool {
	if expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == pkgPath && obj.Name() == typeName
}

// scanForbiddenOutcomeConstruction flags every in-package construction of the
// sealed (pkgPath, typeName) Outcome token in file: a composite literal of ANY
// shape (INCLUDING the empty `Outcome{}` — unlike scanSealedCompositeLitConstruction
// which exempts empty literals) and a zero-value `var o Outcome` declaration. In
// the dead-letter funnel NO consumer may mint an Outcome; the only sanctioned
// producers are dlxoutcome.Dropped/Captured (which record a metric) and they live
// in the dlxoutcome package itself (not scanned here). This closes the irreducible
// empty-literal forge the type system cannot forbid (residual Medium per the godoc).
func scanForbiddenOutcomeConstruction(p *Pass, f *ast.File, rel, pkgPath, typeName string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CompositeLit](f, func(lit *ast.CompositeLit) {
		if !dlxIsOutcomeType(p.TypesInfo, lit.Type, pkgPath, typeName) {
			return
		}
		pos := p.Fset.Position(lit.Pos())
		out = append(out, Diagnostic{Rel: rel, Line: pos.Line, Message: fmt.Sprintf(
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H2: %s composite literal at %s:%d — no mqtt code may "+
				"mint a dlxoutcome.Outcome; obtain it only from dlxoutcome.Dropped/Captured (which "+
				"record the alertable metric)", typeName, rel, pos.Line)})
	})
	EachInSubtree[ast.ValueSpec](f, func(vs *ast.ValueSpec) {
		if vs.Type == nil || len(vs.Values) > 0 {
			return
		}
		if !dlxIsOutcomeType(p.TypesInfo, vs.Type, pkgPath, typeName) {
			return
		}
		pos := p.Fset.Position(vs.Pos())
		out = append(out, Diagnostic{Rel: rel, Line: pos.Line, Message: fmt.Sprintf(
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H2: zero-value `var %s` at %s:%d — a forged Outcome "+
				"bypasses the recording constructors; obtain it only from dlxoutcome.Dropped/Captured",
			typeName, rel, pos.Line)})
	})
	return out
}

func TestMQTTDLXFailureSignalFunnel_H2_NoOutcomeForgeInMQTT(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	seen := false
	diags := Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			seen = true
			var out []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				out = append(out, scanForbiddenOutcomeConstruction(p, f, rel, mqttDLXOutcomePkgPath, dlxOutcomeTypeName)...)
			}
			return out
		})
	assert.True(t, seen, "MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H2: adapters/mqtt production package not loaded")
	assert.Empty(t, diags,
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H2: mqtt forges a dlxoutcome.Outcome — see diagnostics")
}

// TestMQTTDLXFailureSignalFunnel_H2_ScannerFiresOnRedFixture proves the H2 scanner
// is non-vacuous: it MUST report the planted forge in the sealed-shape replica
// fixture. Without this, a silently-broken type-resolution path would let H2 pass
// vacuously (production has no forge).
func TestMQTTDLXFailureSignalFunnel_H2_ScannerFiresOnRedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{dlxOutcomeRedFixturePkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != dlxOutcomeRedFixturePkgPath {
				return nil
			}
			var out []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				out = append(out, scanForbiddenOutcomeConstruction(p, f, rel, dlxOutcomeRedFixturePkgPath, "FixtureOutcome")...)
			}
			return out
		})
	// The fixture plants BOTH forge forms (empty composite literal + zero-value var),
	// so require ≥2 diagnostics: if only one fires, one of the two scan paths
	// (CompositeLit vs ValueSpec) is silently broken.
	assert.GreaterOrEqual(t, len(diags), 2,
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H2: scanner must fire on BOTH dlxoutcomeredfixture forge forms "+
			"(empty composite literal + zero-value var); <2 means a composite-lit/var type-resolution path is silently broken")
}

// ─── H3a: the dlxoutcome constructors actually record (inseparability spine) ──

// dlxConstructorCallsMethod reports whether fd's body contains a call whose
// selector name is method. The receiver is the anonymous-interface `rec` param,
// so its FullName() is not a stable mqtt name — a name-level match is the
// deliberate (documented) choice, safe because the dlxoutcome package's only
// selector calls are these two collector methods.
func dlxConstructorCallsMethod(fd *ast.FuncDecl, method string) bool {
	found := false
	EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == method {
			found = true
		}
	})
	return found
}

func TestMQTTDLXFailureSignalFunnel_H3a_ConstructorsRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	seen := false
	var foundDropped, foundCaptured, droppedRecords, capturedRecords bool
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttDLXOutcomePkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttDLXOutcomePkgPath {
				return nil
			}
			seen = true
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				scanner.EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
					if fd.Body == nil || fd.Name == nil {
						return
					}
					switch fd.Name.Name {
					case dlxDroppedFuncName:
						foundDropped = true
						droppedRecords = dlxConstructorCallsMethod(fd, recordDLXFailureMethod)
					case dlxCapturedFuncName:
						foundCaptured = true
						capturedRecords = dlxConstructorCallsMethod(fd, recordDLXSuccessMethod)
					}
				})
			}
			return nil
		})
	assert.True(t, seen,
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H3a: %s package not loaded — gh #1440 mechanism missing", mqttDLXOutcomePkgPath)
	assert.True(t, foundDropped, "MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H3a: dlxoutcome.Dropped not found")
	assert.True(t, foundCaptured, "MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H3a: dlxoutcome.Captured not found")
	assert.True(t, droppedRecords,
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H3a: dlxoutcome.Dropped must call RecordDeadLetterFailure "+
			"(a no-op constructor would defeat the seal while still compiling)")
	assert.True(t, capturedRecords,
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H3a: dlxoutcome.Captured must call RecordDeadLetter")
}

// ─── H3b: F1 semantic balance — one success exit, ≥1 drop exit ────────────────

// dlxCountConstructorCalls counts dlxoutcome.Captured and dlxoutcome.Dropped
// callsites inside fd. They are package-level funcs, so the callee is resolved
// via Uses (not ResolveMethodCall, which is for value-receiver method calls).
func dlxCountConstructorCalls(p *Pass, fd *ast.FuncDecl) (captured, dropped int) {
	EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		obj, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
		if !ok || obj.Pkg() == nil || obj.Pkg().Path() != mqttDLXOutcomePkgPath {
			return
		}
		switch obj.Name() {
		case dlxCapturedFuncName:
			captured++
		case dlxDroppedFuncName:
			dropped++
		}
	})
	return captured, dropped
}

func TestMQTTDLXFailureSignalFunnel_H3b_OutcomeConstructorBalance(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	withRouteDeadLetter(t, func(p *Pass, _ *ast.File, fd *ast.FuncDecl) {
		captured, dropped := dlxCountConstructorCalls(p, fd)
		assert.Equal(t, 1, captured,
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H3b: routeDeadLetter must have exactly one "+
				"dlxoutcome.Captured callsite (the single success fall-through) — a drop branch wrongly "+
				"credited as success (review F1) makes this 2; a success path wrongly dropped makes it 0")
		assert.GreaterOrEqual(t, dropped, 1,
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H3b: routeDeadLetter must have ≥1 dlxoutcome.Dropped "+
				"callsite (the alertable failure exits)")
	})
}
