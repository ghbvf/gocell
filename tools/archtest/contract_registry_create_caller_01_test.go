//go:build archtest

// contract_registry_create_caller_01_test.go — locks the caller-allowlist of the
// durable contract registration persistence entry point: every production call to
// ports.Registry.Create must originate from the registrywrite slice service
// (corecells/registrycore/slices/registrywrite), never hand-written elsewhere.
//
//   - INVARIANT: CONTRACT-REGISTRY-CREATE-CALLER-01
//
// This is the structural backing of the #2237 (303-US6) invariant "a contract
// registration persisted in the durable store must have passed the governance gate":
// the only way to durably persist a registration is ports.Registry.Create, and the
// only sanctioned caller is registrywrite.Service.Submit, which runs
// governance.RegistrationGate.Check before calling store.Create. A direct
// ports.Registry.Create call from any other package — a cell handler, a test helper
// compiled into production, or another slice — bypasses the gate and admits an
// unvalidated registration into the durable store (contract_registrations table),
// undermining the MDM/zero-trust governance invariant enforced by the gate.
//
// # Threat model
//
// The durable store (PG or in-mem) is opaque to the governance gate: if Create is
// reachable from any package that skips gate.Check, a caller can record an invalid
// or un-reviewed contract registration that then surfaces in the list/history APIs
// as if it had been gate-validated. The gate is the single chokepoint; this archtest
// is the static backing that makes "call Create → must be inside registrywrite" a CI
// invariant rather than a code-review convention.
//
// # AI-robust rating
//
//   - MEDIUM (caller-allowlist, type-aware scan) — a GO-LANGUAGE CEILING, not a
//     deferred TODO. ports.Registry is an exported-shaped interface in an internal
//     package of corecells/registrycore; Go cannot express "only
//     corecells/registrycore/slices/registrywrite may call this interface method".
//     The caller-allowlist archtest (type-aware AST scan via go/types info.Uses) is
//     the ceiling — same permanent posture documented for REGISTRAR-SUBMIT-CALLER-01
//     / COMMAND-ASYNC-EMIT-CALLER-01 / CROSSCELLOBS-MINTER-FUNNEL-01 / #851 / #893
//     / #1282. No fake Hard-upgrade issue is opened. The complementary HARD half lives
//     in the sealed kernel/governance.RegistrationGate: the gate itself is not
//     reachable without a valid tenant + ContractMeta, so the gateway logic cannot be
//     bypassed even when the gate is called; this archtest guards the "must call the
//     gate" requirement for the persistence seam.
//
// # Blind spots
//
//  1. Direct calls to a concrete implementation (mem.Registry.Create or
//     pg.Registry.Create) that do NOT go through the ports.Registry interface value
//     are not caught by this scan (the selector resolves to the concrete type's
//     method, not the interface method). In practice: mem.Registry is an internal
//     package; pg.Registry is also internal. A caller outside registrycore/internal
//     cannot reach either concrete type without importing an internal package
//     (compile error). Within the registrycore/internal packages themselves the
//     concrete methods are implementation detail (the test doubles, not gate bypass).
//     Documented as residual, not enforced.
//  2. _test.go files are EXCLUDED from the Production() scan (TypedOpts.Tests
//     defaults false). Test packages that call store.Create directly (e.g.
//     registrywrite/service_test.go's spy) are intentionally not flagged.
//  3. Build-tag-gated production files under a non-default tag are not scanned by
//     the default-tags Production scan (same posture as sibling caller funnels).
//  4. A call through a non-`ports.Registry` typed local variable (e.g. `var r
//     *mem.Registry = …; r.Create(…)`) resolves to the concrete method, not the
//     interface method, and is not caught (same as blind spot 1). The typed funnel
//     is robust against interface-typed variable calls, which is the primary threat
//     vector for any service injected with a ports.Registry value.
//  5. The RED fixture (registrycreateservicefixture) uses a LOCAL stand-in Registry
//     interface (Go's internal-package visibility prevents tools/archtest from
//     importing corecells/registrycore/internal/ports directly). The RedFixture test
//     therefore runs the detector against the fixture's OWN local interface, not
//     ports.Registry — proving the receiver-binding and method-name detection
//     mechanism works, not that it specifically resolves ports.Registry. Production
//     scan (TestContractRegistryCreateCaller01) does target ports.Registry.Create
//     precisely via its package path.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	// registryPortsPkgPath is the package that declares the ports.Registry interface.
	registryPortsPkgPath = PlatformCellsModulePath + "/registrycore/internal/ports"
	// registryWriteServicePkgPath is the SOLE sanctioned caller of ports.Registry.Create —
	// the registrywrite slice service (registrywrite.Service.Submit), which runs the
	// governance gate before persisting.
	registryWriteServicePkgPath = PlatformCellsModulePath + "/registrycore/slices/registrywrite"
	// registryCreateFixturePkg is the RED-fixture package. Its path is deliberately
	// NOT registryWriteServicePkgPath, so the main scan's allowlist does not falsely
	// exempt the fixture's bypass call. The fixture declares its own local Registry
	// stand-in interface (Go's internal-package restriction prevents it from importing
	// corecells/registrycore/internal/ports directly).
	registryCreateFixturePkg = "./tools/archtest/internal/registrycreateservicefixture"
	// registryCreateFixturePkgPath is the module-qualified path of the RED fixture,
	// used by the RedFixture test to identify the fixture package in the type graph.
	registryCreateFixturePkgPath = PlatformModulePath + "/tools/archtest/internal/registrycreateservicefixture"
)

// isRegistryCreateCall reports whether call resolves to ports.Registry.Create.
// Resolution is via go/types (info.Uses[sel.Sel]), so it is alias- and
// dot-import-proof; the receiver type is pinned to the Registry interface in the
// ports package so a same-named Create on any other interface or struct does not
// match.
func isRegistryCreateCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Create" {
		return false
	}
	if info == nil {
		return false // fail-closed: cannot confirm without type info
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != registryPortsPkgPath {
		return false
	}
	// Confirm the receiver is the Registry interface (not another type in ports).
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	return registryPortsReceiverName(sig.Recv().Type()) == "Registry"
}

// registryPortsReceiverName returns the named-type name of a (possibly pointer)
// receiver type, or "". For interface methods the receiver is the interface type
// itself (not a pointer).
func registryPortsReceiverName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name()
	}
	return ""
}

// isCreateCallOnInterface reports whether call is a `.Create(` selector call whose
// resolved *types.Func lives in pkgPath and whose receiver type has name "Registry".
// This is the generalized detector used by the RedFixture to target the fixture's
// own local Registry stand-in (same detection logic, different pkgPath).
func isCreateCallOnInterface(info *types.Info, call *ast.CallExpr, pkgPath string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Create" {
		return false
	}
	if info == nil {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != pkgPath {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	return registryPortsReceiverName(sig.Recv().Type()) == "Registry"
}

// TestContractRegistryCreateCaller01 asserts that the ONLY production callsite of
// ports.Registry.Create is inside the registrywrite slice package — a
// package-level allowlist (any other package calling Create is flagged, because the
// invariant is "only the registrywrite service", which is the sole entry point that
// runs the governance gate first).
func TestContractRegistryCreateCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var sanctionedCallsites int
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		inAllowlist := p.Pkg.Path() == registryWriteServicePkgPath
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if !isRegistryCreateCall(p.TypesInfo, call) {
					return
				}
				if inAllowlist {
					sanctionedCallsites++
					return // the sanctioned caller
				}
				pos := p.Fset.Position(call.Pos())
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"CONTRACT-REGISTRY-CREATE-CALLER-01: %s calls ports.Registry.Create outside "+
							"corecells/registrycore/slices/registrywrite. A durable contract registration "+
							"MUST be persisted only through the registrywrite.Service.Submit path, which "+
							"runs governance.RegistrationGate.Check before calling store.Create. Any "+
							"other caller bypasses the gate and admits an unvalidated registration "+
							"into the durable store (MDM/zero-trust governance bypass, #2237 303-US6).",
						rel),
				})
			})
		}
		return d
	})

	// Anti-vacuity: at least one sanctioned callsite must exist — the store.Create
	// call inside registrywrite.Service.Submit. 0 = the registrywrite service no
	// longer persists via the ports.Registry interface (the funnel guards nothing);
	// this catches accidental renames or refactors that move the Create call out of
	// the interface-typed path.
	if sanctionedCallsites == 0 {
		diags = append(diags, Diagnostic{
			Message: fmt.Sprintf("CONTRACT-REGISTRY-CREATE-CALLER-01 anti-vacuity: expected ≥ 1 "+
				"ports.Registry.Create callsite inside %s, found %d "+
				"(0 = registrywrite no longer persists via ports.Registry.Create → funnel vacuous; "+
				"check registrywrite/service.go store.Create call or service refactor).",
				registryWriteServicePkgPath, sanctionedCallsites),
		})
	}

	Report(t, "CONTRACT-REGISTRY-CREATE-CALLER-01", diags)
}

// TestContractRegistryCreateCaller01_RedFixture verifies the scanner's detection
// mechanism fires against a hand-written package that calls a Registry.Create
// interface method directly (bypassing the governance gate). The fixture
// (registrycreateservicefixture) cannot import corecells/registrycore/internal/ports
// due to Go's internal-package visibility rules, so it declares its OWN local
// Registry interface; the RedFixture test runs the same receiver-binding detector
// (isCreateCallOnInterface) against that fixture-local interface type. total==0
// means the receiver-binding detection regressed and the main Production scan is
// likely fail-open too.
func TestContractRegistryCreateCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{registryCreateFixturePkg}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			for _, file := range p.Files {
				EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
					// Use the fixture's own package path so the detector resolves the
					// fixture-local Registry stand-in, proving receiver-binding works.
					if isCreateCallOnInterface(p.TypesInfo, call, registryCreateFixturePkgPath) {
						found++
					}
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, found, 1,
		"CONTRACT-REGISTRY-CREATE-CALLER-01 RED fixture self-check FAILED: expected ≥ 1 "+
			"Registry.Create call detected in registrycreateservicefixture (using its local "+
			"Registry stand-in interface); got %d. found==0 means the receiver-binding "+
			"detection regressed — check isCreateCallOnInterface resolves the interface "+
			"method under the archtest_fixture tag.", found)
}
