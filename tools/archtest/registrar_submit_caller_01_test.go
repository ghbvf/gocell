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
// The allowlist is ENTRY-LEVEL, not package-level: the single sanctioned callsite
// is the one inside (*RegistrationGate).Submit in kernel/governance. Any OTHER
// callsite — including another function/method INSIDE kernel/governance — is
// flagged, because the invariant is "only the gate entry", and the governance
// package is large (a package-level allowlist would authorize far more than the
// one entry the invariant protects).
//
// # AI-robust rating
//
//   - MEDIUM (caller-allowlist, type-aware scan) — a GO-LANGUAGE CEILING, not a
//     deferred TODO. A genuinely-Hard sealed-construction funnel (Submit requires
//     a token only the gate can mint) is blocked by kernel layering: a token
//     sealed in registry cannot be minted by governance, and registry cannot
//     import governance (cycle). Go cannot express "only (*RegistrationGate).Submit
//     may call this exported method", so the caller-allowlist archtest is the
//     ceiling — same permanent posture documented for COMMAND-ASYNC-EMIT-CALLER-01 /
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
//  3. The scan walks top-level FuncDecls (and the func literals nested in them); a
//     Submit call in a package-level var initializer (outside any FuncDecl) is not
//     reached. Implausible — Submit needs a *ContractRegistrar receiver value and
//     returns two values — and consistent with the AST-scan surface of sibling
//     funnels.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	registrarPkgPath = PlatformFrameworkModulePath + "/kernel/registry"
	// registrationGatePkgPath is the SOLE sanctioned caller of
	// ContractRegistrar.Submit — the governance registration gate (gate.go).
	registrationGatePkgPath = PlatformFrameworkModulePath + "/kernel/governance"
	// registrarSubmitFixturePkg is the RED-fixture package. Its path is
	// deliberately NOT registrationGatePkgPath, so the main scan's allowlist does
	// not falsely exempt the fixture's bypass call. If the fixture is ever moved
	// under kernel/governance the RED self-check would silently false-pass — keep
	// it outside the gate package.
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

// recvTypeName returns the (de-pointered) receiver type name of a method
// declaration, or "" for a free function / unparsable receiver.
func recvTypeName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return ""
	}
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// isGateSubmitMethodDecl reports whether fd is the sanctioned gate entry method
// (*RegistrationGate).Submit — the ONE callsite allowed to reach
// ContractRegistrar.Submit. Callers must additionally confirm fd lives in the
// governance package (a same-named type elsewhere would not be the gate).
func isGateSubmitMethodDecl(fd *ast.FuncDecl) bool {
	return fd.Name != nil && fd.Name.Name == "Submit" && recvTypeName(fd) == "RegistrationGate"
}

// TestRegistrarSubmitCaller01 asserts that the ONLY production callsite of
// registry.ContractRegistrar.Submit is inside (*RegistrationGate).Submit — an
// entry-level allowlist, not a package-level one (any other callsite, even inside
// kernel/governance, is flagged).
func TestRegistrarSubmitCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var sanctionedCallsites int
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		gatePkg := p.Pkg.Path() == registrationGatePkgPath
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				sanctioned := gatePkg && isGateSubmitMethodDecl(fd)
				EachInSubtree[ast.CallExpr](fd, func(call *ast.CallExpr) {
					if !isRegistrarSubmitCall(p.TypesInfo, call) {
						return
					}
					if sanctioned {
						sanctionedCallsites++
						return // the single allowed callsite
					}
					pos := p.Fset.Position(call.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"REGISTRAR-SUBMIT-CALLER-01: %s calls registry.ContractRegistrar.Submit outside "+
								"(*RegistrationGate).Submit. A runtime contract MUST enter `submitted` only through "+
								"the governance registration gate, which validates the candidate and fail-closes on "+
								"error. Any other callsite — even inside kernel/governance — re-admits the ungated "+
								"\"submitted but invalid\" path the gate eliminates (303-US3 #2234).",
							rel),
					})
				})
			})
		}
		return d
	})

	// Anti-vacuity: EXACTLY ONE sanctioned callsite must exist — the single
	// store.Submit call inside (*RegistrationGate).Submit. 0 = the gate no longer
	// persists (the funnel guards nothing); >1 = the gate grew a second Submit
	// callsite that must be reviewed (the invariant is "the one gate entry", not
	// "the gate package").
	if sanctionedCallsites != 1 {
		diags = append(diags, Diagnostic{
			Message: fmt.Sprintf("REGISTRAR-SUBMIT-CALLER-01 anti-vacuity: expected EXACTLY 1 "+
				"registry.ContractRegistrar.Submit callsite inside (*RegistrationGate).Submit, found %d "+
				"(0 = gate no longer the registration entry → funnel vacuous; >1 = unreviewed second entry).",
				sanctionedCallsites),
		})
	}

	Report(t, "REGISTRAR-SUBMIT-CALLER-01", diags)
}

// TestIsGateSubmitMethodDecl unit-tests the entry-level discriminator directly: it
// must accept (*RegistrationGate).Submit and reject a same-package non-gate
// function / a Submit on a different receiver — the function-level refinement a
// same-package RED fixture cannot exercise (fixtures live in their own package).
func TestIsGateSubmitMethodDecl(t *testing.T) {
	t.Parallel()
	const src = `package governance
type RegistrationGate struct{}
type Other struct{}
func (g *RegistrationGate) Submit() {}
func (g *RegistrationGate) Check() {}
func (o *Other) Submit() {}
func Submit() {}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := map[string]bool{}
	EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		key := recvTypeName(fd) + "." + fd.Name.Name
		got[key] = isGateSubmitMethodDecl(fd)
	})
	assert.True(t, got["RegistrationGate.Submit"], "(*RegistrationGate).Submit must be sanctioned")
	assert.False(t, got["RegistrationGate.Check"], "non-Submit gate method must not be sanctioned")
	assert.False(t, got["Other.Submit"], "Submit on a different receiver must not be sanctioned")
	assert.False(t, got[".Submit"], "free function Submit must not be sanctioned")
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
