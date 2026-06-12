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
// # The mechanism (sealed-construction funnel + type floor, gh #1440)
//
// routeDeadLetter does NOT record the metric inline. It RETURNS a sealed
// dlxoutcome.Outcome whose only sanctioned producers are dlxoutcome.Dropped
// (records the alertable RecordDeadLetterFailure) and dlxoutcome.Captured (records
// the success RecordDeadLetter). The type system contributes a FLOOR: because
// routeDeadLetter is typed to return Outcome, every exit must `return` SOME Outcome
// value — a bare `return` is a compile error. But the type system stops there: it
// does NOT prove the returned Outcome came from a recording path. A stateless proof
// token has a zero value that is always constructible (Outcome{}, var o, *new(T), …),
// so a drop branch CAN forget the metric and still compile by forging a zero value.
// What closes that gap is the archtest funnel: H2 forbids mqtt forging an Outcome
// (composite-lit / zero-var / new), H4 forbids dlxoutcome gaining a non-recording
// producer, H3a checks the two constructors actually record. Floor (H1) + funnel
// (H2/H4/H3a) ⇒ every Outcome routeDeadLetter returns came from a recording
// constructor. This is a stronger, sealed-construction form of the #1356 enforce
// spine — but the closure is archtest, hence Medium (§grading), NOT the literal
// "metric omission is a compile error" the type system cannot deliver.
//
// # AI-robust grading (honest, per .claude/rules/gocell/ai-robust.md)
//
// Medium — a sealed-construction funnel with a type-system floor. Per the
// ai-robust carrier table, archtest typed scan = Medium; the closure of this
// invariant is archtest (H2/H4/H3a), so the whole no-silent-exit axis is Medium,
// NOT Hard. Genuine type-system Hard is unreachable here: it would require proving
// a SIDE EFFECT (the metric was recorded) happened on every exit, but Go can only
// prove a VALUE of type Outcome was produced — and a stateless token's zero value
// is always constructible (no "no zero value" modifier), so the value carries no
// proof of the side effect. Contrast genuinely-Hard sealed construction
// (mqtt.ClientID, internal/topicns): those seal a MEANINGFUL unexported field, so a
// contentful value is package-external compile-gated. Outcome has no state to seal.
//
//   - Type floor (Hard sub-property, H1): routeDeadLetter must return an Outcome —
//     a bare `return` / a refactor back to a void routeDeadLetter is a compile error
//     caught by H1. This raises the bar (a silent drop must actively FORGE a zero
//     value) but does not by itself enforce the metric.
//   - Completion (Medium, H2 + H4 + H3a): H2 forbids mqtt forging an Outcome
//     (composite-lit / zero-var / new); H4 forbids dlxoutcome gaining a non-recording
//     producer; H3a checks Dropped/Captured actually record. Together these close the
//     "the Outcome came from a recording path" gap the type floor leaves open.
//   - F1 semantic (Medium, H3b + behavioral): the floor forces SOME outcome per exit
//     but not the CORRECT one. A drop branch wrongly using Captured records a false
//     success (review F1). H3b pins the Captured callsite count == 1 (single success
//     fall-through) and Dropped >= 1; per-path correctness is also covered by
//     deadletter_test.go.
//
// The ADR / ops / dlxoutcome godoc must NOT claim "metric omission is a compile
// error" or "type-system Hard" — that is the overclaim corrected in gh #1873 F1.
//
// # Blind-spot inventory (per ai-robust.md 强制盲区自检)
//
//   - composite-lit / zero-var / new(T) forge in mqtt → H2 (CompositeLit + ValueSpec +
//     new-builtin scan over mqtt production files). All enumerable AST forms, and each
//     is alias-aware: H2 resolves the type via dlxNamedUnaliased (types.Unalias), so a
//     `type A = dlxoutcome.Outcome; A{}` / `new(A)` forge is caught too (gh #1873 r2).
//     Proven non-vacuous by the dlxoutcomeredfixture synthetic red case (four planted
//     forges incl. an alias literal), which uses a sealed-shape REPLICA (a real-type
//     fixture is impossible: dlxoutcome is an internal package the tools module cannot
//     import — same replica rationale as internal/mqttredfixture).
//   - non-recording third producer in dlxoutcome → H4 (sole-producer allowlist =
//     {Dropped, Captured}), alias-aware via dlxNamedUnaliased so `func Silent() A`
//     (A = Outcome alias) is recognized as a producer. Proven non-vacuous by the same
//     red fixture, whose four planted functions return the replica Outcome (one via the
//     alias) but are not Dropped/Captured.
//   - constructor gutted to a no-op → H3a (Dropped calls RecordDeadLetterFailure,
//     Captured calls RecordDeadLetter; a metric-less constructor would defeat the
//     seal while still compiling).
//   - H3a name-level match → H3a matches the recording call by selector NAME (rec is an
//     anonymous interface, so FullName is not a stable mqtt symbol). A future homonymous
//     method in the (tiny) dlxoutcome package would yield a false-POSITIVE (over-credit),
//     never a false-negative; H3b + behavioral tests stay valid regardless.
//   - drop credited as success (F1) → H3b + deadletter_test.go behavioral tests.
//   - ACCEPTED RESIDUAL (the Medium ceiling, gh #1873 F1): open-set zero-value
//     extraction — a zero Outcome pulled from an array/map element, reflect.Zero, or an
//     IIFE — is not an enumerable AST forge form, so neither H2 nor any archtest can
//     bound it. This is precisely why no-silent-exit is Medium, not Hard: the type
//     system cannot forbid zero values and the forge surface is open-ended. Flagged so
//     it is not mistaken for a closed hole.
//
// ref: gh #1356 — MQTT DLT no-loss (this invariant is the resolution's enforce spine)
// ref: gh #1440 — sealed dlxoutcome.Outcome funnel (drop decision 收口为 sealed token)
// ref: gh #1873 — F1: no-silent-exit is Medium (funnel), not Hard; new(T) + sole-producer closed
// ref: ADR docs/architecture/202605281200-048-adr-mqtt-adapter.md §threat matrix + Amendment 2026-06-11
// ref: ADR docs/architecture/202605301200-050-adr-mqtt-requeue-semantics.md §6 (HoL / Option C)
// ref: ai-robust.md §Hard 范本 sealed construction (pattern followed; graded Medium — stateless token)
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
		named, ok := dlxNamedUnaliased(sig.Results().At(0).Type())
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

// dlxNamedUnaliased unwraps a type alias and returns the underlying *types.Named.
// types.Unalias is REQUIRED because under Go 1.23+ (gotypesalias=1, the default) a
// `type A = dlxoutcome.Outcome` denotes a *types.Alias, not a *types.Named — so a
// bare .(*types.Named) assertion would let an alias-typed forge/producer
// (`A{}` / `new(A)` / `func() A`) slip past H1/H2/H4 (gh #1873 review F1 r2; same
// requirement as reconcile_invariants isReconcileLoopType). No-op when t is already
// a *types.Named.
func dlxNamedUnaliased(t types.Type) (*types.Named, bool) {
	named, ok := types.Unalias(t).(*types.Named)
	return named, ok
}

// dlxIsOutcomeType reports whether expr's resolved type (alias-unwrapped) is
// (pkgPath, typeName).
func dlxIsOutcomeType(info *types.Info, expr ast.Expr, pkgPath, typeName string) bool {
	if expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok {
		return false
	}
	named, ok := dlxNamedUnaliased(tv.Type)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == pkgPath && obj.Name() == typeName
}

// scanForbiddenOutcomeConstruction flags every in-package construction of the
// sealed (pkgPath, typeName) Outcome token in file via the three enumerable
// zero-value forge forms: a composite literal of ANY shape (INCLUDING the empty
// `Outcome{}` — unlike scanSealedCompositeLitConstruction which exempts empty
// literals), a zero-value `var o Outcome` declaration, and a `*new(Outcome)`
// allocation. All three resolve the type through dlxNamedUnaliased (types.Unalias),
// so an alias-typed forge (`type A = Outcome; A{}` / `new(A)`) is caught as well
// (gh #1873 review F1 r2). In the dead-letter funnel NO consumer may mint an Outcome;
// the only sanctioned producers are dlxoutcome.Dropped/Captured (which record a
// metric) and they live in the dlxoutcome package itself (locked by H4, not scanned
// here).
//
// These three are the enumerable forge forms; the genuinely-irreducible residual
// (open-set zero-value extraction: array/map element, reflect.Zero, an IIFE) is
// what keeps the no-silent-exit axis Medium, not Hard (see the package godoc
// §grading). The type floor only forces routeDeadLetter to return SOME Outcome;
// that it came from a recording path is closed by this scan (H2) + the
// sole-producer guard (H4), both archtest.
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
	// new(Outcome) / *new(Outcome): the builtin new has no package object in
	// TypesInfo.Uses, so identify it by name — no user-defined func may be named
	// `new` in Go (same pattern as reconcile.Loop's new(T) guard,
	// reconcile_invariants_test.go). Closes the new(T) forge (gh #1873 review F1):
	// it is enumerable, not an irreducible residual.
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "new" || len(call.Args) != 1 {
			return
		}
		if !dlxIsOutcomeType(p.TypesInfo, call.Args[0], pkgPath, typeName) {
			return
		}
		pos := p.Fset.Position(call.Pos())
		out = append(out, Diagnostic{Rel: rel, Line: pos.Line, Message: fmt.Sprintf(
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H2: new(%s) at %s:%d — a forged zero Outcome "+
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
	// The fixture plants ALL FOUR forge forms (empty composite literal + zero-value
	// var + *new(T) + an alias-typed composite literal), so require ≥4 diagnostics:
	// if fewer fire, one of the four scan paths (CompositeLit / ValueSpec / new-builtin
	// / types.Unalias) is silently broken. The 4th specifically requires types.Unalias
	// in dlxIsOutcomeType (gh #1873 review F1 r2): without it the alias forge is missed.
	assert.GreaterOrEqual(t, len(diags), 4,
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H2: scanner must fire on ALL FOUR dlxoutcomeredfixture forge forms "+
			"(empty composite literal + zero-value var + *new(T) + alias literal); <4 means a "+
			"composite-lit/var/new/unalias type-resolution path is silently broken")
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

// ─── H4: dlxoutcome's ONLY Outcome producers are Dropped/Captured ─────────────
//
// H2 forbids mqtt from FORGING an Outcome; H4 closes the upstream side of the
// funnel — it forbids the dlxoutcome package from gaining a THIRD producer (e.g.
// `func Silent() Outcome { return Outcome{} }`) that returns an Outcome without
// recording a metric. Without H4, H2 (downstream) + H3b (aggregate count) would
// pass while a silent drop routed through such a producer (gh #1873 review F1,
// "没闭合上游唯一生产者"). Together H1+H2+H4+H3a mean every Outcome mqtt returns came
// from a recording constructor — the closed funnel that makes no-silent-exit a
// Medium machine-guarded property (the type system only forces returning SOME
// Outcome; see the package godoc §grading).

// dlxSanctionedProducers is the allowlist of dlxoutcome functions permitted to
// return an Outcome. Both record a metric (H3a), so confining production to this
// set is what makes "any Outcome was minted by a recording path" hold.
func dlxSanctionedProducers() map[string]bool {
	return map[string]bool{dlxDroppedFuncName: true, dlxCapturedFuncName: true}
}

// funcResultIsOutcome reports whether fd's signature returns the sealed
// (pkgPath, typeName) Outcome in any result position. Generic constructors
// (Dropped[R]/Captured[R]) still have a concrete Outcome result, so the type-param
// does not affect the match.
func funcResultIsOutcome(info *types.Info, fd *ast.FuncDecl, pkgPath, typeName string) bool {
	if fd.Name == nil {
		return false
	}
	fn, ok := info.Defs[fd.Name].(*types.Func)
	if !ok {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	res := sig.Results()
	for i := 0; i < res.Len(); i++ {
		named, ok := dlxNamedUnaliased(res.At(i).Type())
		if !ok {
			continue
		}
		obj := named.Obj()
		if obj.Pkg() != nil && obj.Pkg().Path() == pkgPath && obj.Name() == typeName {
			return true
		}
	}
	return false
}

// scanOutcomeProducers walks the top-level functions of p's loaded package and
// returns (diags, found): a diagnostic for every function whose result type is the
// sealed (pkgPath, typeName) Outcome but whose name is NOT in allowed, and found =
// the names of ALL Outcome producers (for the non-vacuity assertion).
func scanOutcomeProducers(p *Pass, pkgPath, typeName string, allowed map[string]bool) (diags []Diagnostic, found []string) {
	for _, f := range p.Files {
		rel := p.Rel(f)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		scanner.EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if !funcResultIsOutcome(p.TypesInfo, fd, pkgPath, typeName) {
				return
			}
			found = append(found, fd.Name.Name)
			if allowed[fd.Name.Name] {
				return
			}
			pos := p.Fset.Position(fd.Pos())
			diags = append(diags, Diagnostic{Rel: rel, Line: pos.Line, Message: fmt.Sprintf(
				"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H4: %s returns %s but is not a sanctioned producer "+
					"(Dropped/Captured) at %s:%d — a non-recording producer mints an Outcome without the "+
					"alertable metric; the sole producers of %s must record a dead-letter outcome",
				fd.Name.Name, typeName, rel, pos.Line, typeName)})
		})
	}
	return diags, found
}

func TestMQTTDLXFailureSignalFunnel_H4_SoleProducers(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	seen := false
	var diags []Diagnostic
	var found []string
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttDLXOutcomePkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttDLXOutcomePkgPath {
				return nil
			}
			seen = true
			diags, found = scanOutcomeProducers(p, mqttDLXOutcomePkgPath, dlxOutcomeTypeName, dlxSanctionedProducers())
			return nil
		})
	assert.True(t, seen,
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H4: %s package not loaded — gh #1440 mechanism missing", mqttDLXOutcomePkgPath)
	assert.Empty(t, diags,
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H4: dlxoutcome has a non-sanctioned Outcome producer — only "+
			"Dropped/Captured (which record a metric) may return Outcome; a third producer reopens silent drop")
	// Non-vacuity: both sanctioned producers must be present, else the result-type
	// resolution is silently broken and H4 would pass on an empty producer set.
	assert.Subset(t, found, []string{dlxDroppedFuncName, dlxCapturedFuncName},
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H4: expected Dropped+Captured among Outcome producers; "+
			"missing one means the producer scan is broken")
}

// TestMQTTDLXFailureSignalFunnel_H4_ScannerFiresOnRedFixture proves the H4 producer
// scan is non-vacuous: the replica fixture declares three Outcome producers
// (archtestForgedOutcome{Literal,Var,New}), none named Dropped/Captured, so the
// allowlist scan MUST flag all three.
func TestMQTTDLXFailureSignalFunnel_H4_ScannerFiresOnRedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	var diags []Diagnostic
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{dlxOutcomeRedFixturePkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != dlxOutcomeRedFixturePkgPath {
				return nil
			}
			diags, _ = scanOutcomeProducers(p, dlxOutcomeRedFixturePkgPath, "FixtureOutcome", dlxSanctionedProducers())
			return nil
		})
	assert.GreaterOrEqual(t, len(diags), 4,
		"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H4: scanner must flag all four non-sanctioned producers in "+
			"dlxoutcomeredfixture (incl. the alias-return producer); <4 means the result-type producer "+
			"scan is silently broken — the alias one needs types.Unalias (gh #1873 review F1 r2)")
	for _, d := range diags {
		assert.True(t, strings.HasSuffix(d.Rel, "fixture.go"),
			"MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01/H4: diagnostic not from the red fixture file: %s", d.Rel)
	}
}
