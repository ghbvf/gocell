//go:build archtest

// INVARIANT: PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01
//
// PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01 — projection subscriptions may only
// be carried by a transport that GUARANTEES serial in-order delivery.
//
// L3 CQRS projection exactly-once rests on a monotonic checkpoint (applyOne
// skips any event whose stream position ≤ the stored checkpoint), which is only
// sound under strictly serial, in-order delivery of a single subscription's
// stream. Under concurrent delivery (e.g. AMQP prefetch>1 dispatching a
// goroutine per delivery) a higher position can commit the checkpoint before a
// lower position is applied, silently dropping the lower event's apply (a
// projection gap). See ADR §6 threat row 4 + kernel/projection/doc.go.
//
// The enforcement is a marker + fail-closed-by-absence drain guard:
//
//   - kernel/outbox.SerialInOrderGuarantor is the capability marker
//     (GuaranteesSerialInOrderDelivery() bool). A Subscriber opts IN to carrying
//     projections by implementing it and returning true.
//   - The projection drain (runtime/bootstrap/phases_projection.go) type-asserts
//     the wired raw subscriber against the marker; absence or false → fail-fast.
//     A concurrent transport opts OUT simply by not implementing it, and any
//     future transport that forgets the method is auto-rejected for projections.
//
// This archtest has four sub-rules:
//
//   - A (marker freeze): outbox.SerialInOrderGuarantor has exactly one method,
//     GuaranteesSerialInOrderDelivery() bool. Adding/renaming a method (which
//     would change the capability contract) fails immediately.
//   - B (implementer set): the set of production types implementing the marker
//     is EXACTLY {runtime/eventbus.InMemoryEventBus}. A concurrent transport
//     (AMQP/MQTT) or the contractTracingSubscriber decorator must NOT implement
//     it — that is what makes fail-closed-by-absence load-bearing.
//   - C (guard callsite + drain-path binding): the only production type assertion
//     to the marker lives in runtime/bootstrap/phases_projection.go (locked by
//     …_GuardCallsite), AND drainCellProjections — the drain entrypoint — must
//     actually call the guard (locked by …_GuardOnDrainPath, #1369 F3 hardening).
//     The two together stop the guard from drifting off the projection wiring path
//     OR being left defined-but-uncalled (dead code that passes a file-presence
//     check yet wires projections onto a concurrent transport).
//   - D (decorator does not shadow): contractTracingSubscriber must NOT implement
//     the marker — it wraps (does not embed) the raw subscriber, so asserting the
//     raw s.sub bypasses it; an accidental decorator implementation would mask
//     the real transport's capability. (Subsumed by B's exact-set check; asserted
//     explicitly for the H2 blind spot.)
//
// # AI-robust grading (single-axis: runtime invariant guard + fail-closed marker)
//
//   - Medium. The guarded property is the runtime concurrent-delivery behavior of
//     an injected Subscriber (wired at the composition root as outbox.Subscriber).
//     Go cannot express "this injected interface value guarantees serial delivery"
//     at compile time; the marker is self-attestation. A runtime drain fail-fast,
//     fail-closed-by-absence, plus this archtest (marker freeze / exact implementer
//     set / single guard callsite) is the strongest achievable form — the same
//     permanent ceiling as SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL
//     (#893). Hard-ification (a sealed framework-owned projection-transport token
//     that AMQP cannot construct) requires reworking the WithSubscriber injection
//     surface; tracked as gh #1475 (see ADR §Amendment 2026-06-02).
//
// # Blind spots
//
//   - B1. Type-switch on the marker (`switch s.(type) { case outbox.SerialInOrderGuarantor: }`)
//     instead of a TypeAssertExpr would not be caught by sub-rule C's
//     TypeAssertExpr scan. Covered by
//     TestProjectionSerialDeliveryEnforcement01_ReverseBlindSpot_NoTypeSwitch.
//   - B2. reflect-based implicit implementation: no production projection/transport
//     code uses reflect to synthesize the marker. The marker has a behavioral
//     method, not a tag, so reflect-impl is not expressible.
//
// ref: tools/archtest/projection_checkpoint_conformance_enroll_test.go (impl-set scan pattern)
// ref: tools/archtest/projection_apply_hook_funnel_test.go (sibling projection funnel)
// ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §6 row 4 + §Amendment 2026-06-02
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

const (
	serialGuarantorPkgPath  = PlatformModulePath + "/kernel/outbox"
	serialGuarantorTypeName = "SerialInOrderGuarantor"
	serialGuarantorMethod   = "GuaranteesSerialInOrderDelivery"

	// serialGuardCallsiteRel is the ONLY production file allowed to type-assert a
	// subscriber against outbox.SerialInOrderGuarantor (the projection drain guard).
	serialGuardCallsiteRel = "runtime/bootstrap/phases_projection.go"

	// serialGuardDrainFunc is the drain entrypoint that MUST call the guard;
	// serialGuardCheckFunc is the guard function holding the marker type assertion.
	// Sub-rule C's drain-path binding asserts the former calls the latter so the
	// guard cannot be left defined-but-uncalled (#1369 F3 hardening).
	serialGuardDrainFunc = "drainCellProjections"
	serialGuardCheckFunc = "checkSubscriberGuaranteesSerialDelivery"

	// serialGuardBootstrapPkgPath is the import path of the package owning the
	// drain entrypoint, used to scope the drain-path binding scan.
	serialGuardBootstrapPkgPath = PlatformModulePath + "/runtime/bootstrap"

	// serialGuarantorSoleImpl is the ONLY production type permitted to implement
	// the marker — the serial in-memory bus. AMQP/MQTT and the
	// contractTracingSubscriber decorator stay absent (fail-closed); sub-rule D
	// (decorator must not shadow the marker) is subsumed by the exact-set check
	// in TestProjectionSerialDeliveryEnforcement01_ImplementerSet.
	//
	// Adding a second legitimate serial transport (e.g. an ordered NATS consumer)
	// is a DELIBERATE update: add it here AND it begins to qualify for projections.
	// The exact-set assertion fails CI until this golden is updated — intended.
	serialGuarantorSoleImpl = PlatformModulePath + "/runtime/eventbus.InMemoryEventBus"
)

// TestProjectionSerialDeliveryEnforcement01_MarkerFrozen (sub-rule A): the marker
// interface has exactly one method with the frozen name and signature func() bool.
func TestProjectionSerialDeliveryEnforcement01_MarkerFrozen(t *testing.T) {
	t.Parallel()

	rt := reflect.TypeOf((*outbox.SerialInOrderGuarantor)(nil)).Elem()
	require.Equal(t, reflect.Interface, rt.Kind(), "SerialInOrderGuarantor must be an interface")
	require.Equal(t, 1, rt.NumMethod(),
		"SerialInOrderGuarantor must have exactly one method (capability marker); "+
			"adding a method changes the contract — update the guard + ADR deliberately")

	m := rt.Method(0)
	assert.Equal(t, serialGuarantorMethod, m.Name, "marker method name is frozen")

	mt := m.Type
	assert.Equal(t, 0, mt.NumIn(), "marker method takes no arguments")
	require.Equal(t, 1, mt.NumOut(), "marker method returns exactly one value")
	assert.Equal(t, reflect.Bool, mt.Out(0).Kind(), "marker method returns bool")
}

// TestProjectionSerialDeliveryEnforcement01_ImplementerSet (sub-rules B + D): the
// set of production types implementing outbox.SerialInOrderGuarantor is exactly
// {runtime/eventbus.InMemoryEventBus}. Any concurrent transport or the
// contractTracingSubscriber decorator implementing it would break fail-closed.
func TestProjectionSerialDeliveryEnforcement01_ImplementerSet(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)

	// Resolve the marker interface and collect impls from the SAME load so
	// types.Implements uses pointer-identical *types.Named descriptors.
	var iface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == serialGuarantorPkgPath {
				if obj := p.Pkg.Scope().Lookup(serialGuarantorTypeName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if it, ok := named.Underlying().(*types.Interface); ok {
							iface = it.Complete()
						}
					}
				}
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, iface,
		"PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01: failed to resolve %s interface; check import path %s",
		serialGuarantorTypeName, serialGuarantorPkgPath)

	implSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg == nil {
			continue
		}
		for _, name := range pkg.Scope().Names() {
			obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			tt := obj.Type()
			if _, isIface := tt.Underlying().(*types.Interface); isIface {
				continue
			}
			if typesutil.ImplementsInterface(tt, iface) {
				implSet[pkg.Path()+"."+name] = true
			}
		}
	}

	got := make([]string, 0, len(implSet))
	for k := range implSet {
		got = append(got, k)
	}
	sort.Strings(got)

	want := []string{serialGuarantorSoleImpl}
	assert.Equal(t, want, got,
		"PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01: the marker implementer set must be exactly "+
			"{InMemoryEventBus}. A concurrent transport (AMQP/MQTT) or the contractTracingSubscriber "+
			"decorator implementing SerialInOrderGuarantor would defeat fail-closed-by-absence — "+
			"projections would be wired onto a non-serial transport. (sub-rules B + D)")
}

// TestProjectionSerialDeliveryEnforcement01_GuardCallsite (sub-rule C): the only
// production type assertion to outbox.SerialInOrderGuarantor lives in the
// projection drain file. The guard cannot drift off the projection wiring path.
func TestProjectionSerialDeliveryEnforcement01_GuardCallsite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)

	var foundInGuardFile bool
	diags := Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			var out []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.TypeAssertExpr](f, func(ta *ast.TypeAssertExpr) {
					if ta.Type == nil { // `x.(type)` inside a type switch — handled by B1 reverse test
						return
					}
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, ta.Type)
					if !ok || pkgPath != serialGuarantorPkgPath || name != serialGuarantorTypeName {
						return
					}
					if rel == serialGuardCallsiteRel {
						foundInGuardFile = true
						return
					}
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(ta.Pos()).Line,
						Message: fmt.Sprintf(
							"archtest: type assertion to outbox.%s outside the sanctioned projection drain "+
								"guard (%s). The serial-delivery guard must live only there so it runs on the "+
								"projection wiring path (PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01 sub-rule C).",
							serialGuarantorTypeName, serialGuardCallsiteRel),
					})
				})
			}
			return out
		})

	Report(t, "PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01", diags)
	assert.True(t, foundInGuardFile,
		"PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01: the projection drain guard "+
			"(type assertion to outbox.%s in %s) is missing — the serial-delivery enforcement is not wired",
		serialGuarantorTypeName, serialGuardCallsiteRel)
}

// TestProjectionSerialDeliveryEnforcement01_GuardOnDrainPath (sub-rule C,
// #1369 F3 hardening): the drain entrypoint drainCellProjections MUST call the
// guard checkSubscriberGuaranteesSerialDelivery. Sub-rule C's GuardCallsite test
// only proves the marker type assertion EXISTS in the drain file — a regression
// that left the guard defined but un-called (dead code) would pass GuardCallsite
// yet wire projections onto a concurrent transport. This binds the guard to the
// drain path: callee resolved via go/types (not a name-only match), so a
// shadowing local would not satisfy it.
func TestProjectionSerialDeliveryEnforcement01_GuardOnDrainPath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)

	var pkgSeen, drainSeen, guardCalled bool
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != serialGuardBootstrapPkgPath {
				return nil
			}
			pkgSeen = true
			for _, f := range p.Files {
				if p.Rel(f) != serialGuardCallsiteRel {
					continue
				}
				scanner.EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
					if fn.Name == nil || fn.Name.Name != serialGuardDrainFunc || fn.Body == nil {
						return
					}
					drainSeen = true
					EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
						id, ok := call.Fun.(*ast.Ident)
						if !ok {
							return
						}
						fnObj, ok := p.TypesInfo.ObjectOf(id).(*types.Func)
						if !ok || fnObj.Name() != serialGuardCheckFunc {
							return
						}
						if fnObj.Pkg() != nil && fnObj.Pkg().Path() == serialGuardBootstrapPkgPath {
							guardCalled = true
						}
					})
				})
			}
			return nil
		})

	require.True(t, pkgSeen,
		"PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01: package %s not loaded", serialGuardBootstrapPkgPath)
	require.True(t, drainSeen,
		"PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01: func %s not found in %s — the drain entrypoint "+
			"was renamed; update serialGuardDrainFunc", serialGuardDrainFunc, serialGuardCallsiteRel)
	assert.True(t, guardCalled,
		"PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01 (sub-rule C / #1369 F3): %s must call %s so the "+
			"serial-delivery guard runs on the projection wiring path. GuardCallsite only checks the marker "+
			"type assertion EXISTS in %s; this binds it to the drain entrypoint so a defined-but-uncalled "+
			"guard cannot pass.", serialGuardDrainFunc, serialGuardCheckFunc, serialGuardCallsiteRel)
}

// TestProjectionSerialDeliveryEnforcement01_ReverseBlindSpot_NoTypeSwitch (B1):
// no production file branches on the marker via a type switch (which sub-rule C's
// TypeAssertExpr scan would miss). Today the guard uses a comma-ok TypeAssertExpr;
// a type-switch form would be an undetected guard relocation.
func TestProjectionSerialDeliveryEnforcement01_ReverseBlindSpot_NoTypeSwitch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)

	diags := Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			var out []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.TypeSwitchStmt](f, func(sw *ast.TypeSwitchStmt) {
					scanner.EachInSubtree[ast.CaseClause](sw, func(cc *ast.CaseClause) {
						for _, te := range cc.List {
							pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, te)
							if ok && pkgPath == serialGuarantorPkgPath && name == serialGuarantorTypeName {
								out = append(out, Diagnostic{
									Rel:  rel,
									Line: p.Fset.Position(te.Pos()).Line,
									Message: fmt.Sprintf(
										"blind-spot B1: type switch on outbox.%s — sub-rule C's TypeAssertExpr "+
											"scan does not cover type-switch cases. Use the comma-ok type "+
											"assertion form in %s instead (PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01).",
										serialGuarantorTypeName, serialGuardCallsiteRel),
								})
							}
						}
					})
				})
			}
			return out
		})

	assert.Empty(t, diags,
		"B1 reverse: no production type switch may branch on outbox.%s", serialGuarantorTypeName)
}
