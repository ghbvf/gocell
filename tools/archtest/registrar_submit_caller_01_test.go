//go:build archtest

// registrar_submit_caller_01_test.go — locks the caller-allowlist of the runtime
// contract-registration state machine's entry point: every production call to
// registry.ContractRegistrar.Submit must originate from the governance
// registration gate (kernel/governance), never hand-written elsewhere.
//
//   - INVARIANT: REGISTRAR-SUBMIT-CALLER-01
//
// This is the structural backing of the 303-US3 (#2234) invariant "an invalid
// contract never enters the sealed `submitted` state": the only way to create a
// `submitted` registration is ContractRegistrar.Submit, and the only sanctioned
// caller is governance.RegistrationGate, which validates the candidate first and
// fail-closes on any error. A direct ContractRegistrar.Submit call from a cell
// handler or anywhere else would re-admit the ungated "submitted but invalid"
// path the gate exists to eliminate.
//
// # AI-robust rating
//
//   - MEDIUM (caller-allowlist, type-aware scan) — a GO-LANGUAGE CEILING, not a
//     deferred TODO. A genuinely-Hard sealed-construction funnel (Submit requires
//     a token only the gate can mint) is blocked by kernel layering: a token
//     sealed in registry cannot be minted by governance, and registry cannot
//     import governance (cycle). Go cannot express "only kernel/governance may
//     call this exported method", so the caller-allowlist archtest is the ceiling
//     — same permanent posture documented for COMMAND-ASYNC-EMIT-CALLER-01 /
//     CROSSCELLOBS-MINTER-FUNNEL-01 / #851 / #893 / #1282. No fake Hard-upgrade
//     issue is opened. The complementary HARD half is the sealed RegistrationState
//     (registry.state.go): `submitted` cannot be forged, only reached via the
//     transition table from Submit.
//
// # Tool blind spots
//
//  1. _test.go files are EXCLUDED from the Production() scan (TypedOpts.Tests
//     defaults false) — registry's own registrar_test.go calls Submit directly and
//     is intentionally not flagged. Test callers are a funnel-consistency note, not
//     a production bypass.
//  2. Build-tag-gated production files under a non-default tag are not scanned by
//     the default-tags Production scan (same posture as sibling caller funnels).
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	registrarPkgPath = PlatformFrameworkModulePath + "/kernel/registry"
	// registrationGatePkgPath is the SOLE sanctioned caller of
	// ContractRegistrar.Submit — the governance registration gate (gate.go).
	registrationGatePkgPath   = PlatformFrameworkModulePath + "/kernel/governance"
	registrarSubmitFixturePkg = "./tools/archtest/internal/registrarsubmitcallerfixture"
)

// isRegistrarSubmitCall reports whether call resolves to
// (*registry.ContractRegistrar).Submit. Resolution is via go/types
// (info.Uses[sel.Sel]), so it is alias- and dot-import-proof; the receiver type
// is pinned to ContractRegistrar so a same-named Submit on a different registry
// type would not match.
func isRegistrarSubmitCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Submit" {
		return false
	}
	if info == nil {
		return false // fail-closed: cannot confirm without type info
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != registrarPkgPath {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	return registrarReceiverName(sig.Recv().Type()) == "ContractRegistrar"
}

// registrarReceiverName returns the named-type name of a (possibly pointer)
// receiver type, or "".
func registrarReceiverName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name()
	}
	return ""
}

// TestRegistrarSubmitCaller01 asserts that no production package other than the
// governance registration gate calls registry.ContractRegistrar.Submit.
func TestRegistrarSubmitCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		if p.Pkg.Path() == registrationGatePkgPath {
			return nil // sanctioned caller: the governance registration gate
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if !isRegistrarSubmitCall(p.TypesInfo, call) {
					return
				}
				pos := p.Fset.Position(call.Pos())
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"REGISTRAR-SUBMIT-CALLER-01: %s calls registry.ContractRegistrar.Submit directly. "+
							"A runtime contract MUST enter the `submitted` state only through the governance "+
							"registration gate (kernel/governance.RegistrationGate.Submit), which validates the "+
							"candidate and fail-closes on error. A direct Submit re-admits the ungated "+
							"\"submitted but invalid\" path the gate eliminates (303-US3 #2234).",
						rel),
				})
			})
		}
		return d
	})

	// Anti-vacuity: the governance gate must actually call ContractRegistrar.Submit,
	// else this funnel guards nothing (the Production scan would never observe a
	// sanctioned caller and any future bypass would still be the ONLY caller).
	if sanctioned := countGateRegistrarSubmitCalls(t); sanctioned == 0 {
		diags = append(diags, Diagnostic{
			Message: "REGISTRAR-SUBMIT-CALLER-01 anti-vacuity: kernel/governance does not call " +
				"registry.ContractRegistrar.Submit — the gate is no longer the registration entry, " +
				"so the caller funnel guards nothing. Either the gate was removed/renamed or the " +
				"scanner regressed.",
		})
	}

	Report(t, "REGISTRAR-SUBMIT-CALLER-01", diags)
}

// countGateRegistrarSubmitCalls returns the number of ContractRegistrar.Submit
// calls in the governance package (the sanctioned-caller anti-vacuity anchor).
func countGateRegistrarSubmitCalls(t *testing.T) int {
	t.Helper()
	var found int
	_ = Run(t, Typed(TypedOpts{}, []string{"./framework/kernel/governance"}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if isRegistrarSubmitCall(p.TypesInfo, call) {
					found++
				}
			})
		}
		return nil
	})
	return found
}

// TestRegistrarSubmitCaller01_RedFixture verifies the scanner fires against a
// hand-written package that calls ContractRegistrar.Submit directly (bypassing the
// gate). total==0 means the scanner is fail-open.
func TestRegistrarSubmitCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{registrarSubmitFixturePkg}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			for _, file := range p.Files {
				EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
					if isRegistrarSubmitCall(p.TypesInfo, call) {
						found++
					}
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, found, 1,
		"REGISTRAR-SUBMIT-CALLER-01 RED fixture self-check FAILED: expected ≥ 1 violation from "+
			"registrarsubmitcallerfixture (direct ContractRegistrar.Submit); got %d. found==0 means the "+
			"scanner is fail-open — check info.Uses resolves under the archtest_fixture tag.", found)
}
