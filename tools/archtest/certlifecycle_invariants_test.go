//go:build archtest

// INVARIANT: CERTLIFECYCLE-SIGN-VIA-FUNNEL-01
//
// The cert-lifecycle reconciler (framework/runtime/certlifecycle) obtains issued
// certificates EXCLUSIVELY via certsigning.Signer.Sign — it never mints an
// IssuedCert nor reaches into a concrete CA.
//
// # Rating: the SECURITY PROPERTY is Hard by inheritance; this archtest is the
// Medium reverse self-check + anti-vacuity (AI-robust §"内容扫描规则必须有
// synthetic red case 和 anti-vacuity"). No new Hard mechanism is introduced
// because three existing mechanisms already make Signer.Sign the only compilable
// way for certlifecycle to obtain an IssuedCert:
//
//   - certsigning.IssuedCert has only unexported fields, so certlifecycle cannot
//     forge one by composite literal (compile-time Hard,
//     CERT-VALUE-SEALED-CONSTRUCTION-01);
//   - the sole minter certsigning.NewIssuedCert is restricted by the existing
//     CERT-SIGN-FUNNEL-01 caller-allowlist (certIssuedMintAllowlist) to
//     {adapters/softca}, which excludes certlifecycle;
//   - certlifecycle cannot import adapters/softca: softca is a separate Go module
//     that requires the framework module, so the reverse import is a circular
//     module dependency the build refuses (module-topology Hard).
//
// This file pins those guarantees against drift with three legs:
//
//   - GreenProduction (anti-vacuity): certlifecycle is actually scanned, imports
//     the certsigning seam, calls certsigning.Signer.Sign at least once (the
//     funnel is USED, not stubbed), and calls certsigning.NewIssuedCert zero
//     times (it never self-mints).
//   - NotInMintAllowlist (cross-rule pin): certlifecycle is absent from
//     certIssuedMintAllowlist, so the existing CERT-SIGN-FUNNEL-01 genuinely
//     covers it — a future PR adding it (re-opening direct minting) fails here.
//   - RedFixture (synthetic red case): a lifecycle-shaped fixture that mints an
//     IssuedCert directly must fire the mint detector.
//
// Blind spot (per AI-robust §强制盲区自检): the "calls Signer.Sign" leg is an
// AST/types call-site scan; a Signer passed to an external func via closure that
// invokes it would be invisible. That residual is immaterial here because the
// security property does not rest on this positive leg — it rests on the three
// inherited Hard mechanisms above; this leg only guards against the funnel being
// silently removed from certlifecycle.

package archtest

import (
	"go/ast"
	"go/types"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// certlifecyclePkgPath is the import path of the cert-lifecycle reconciler.
func certlifecyclePkgPath() string { return PlatformFrameworkModulePath + "/runtime/certlifecycle" }

const certlifecycleMintMsg = "forbidden call certsigning.NewIssuedCert from certlifecycle — the lifecycle " +
	"reconciler must obtain certificates via certsigning.Signer.Sign, never mint them"

// isCertsigningSignerSign reports whether fn is the Sign method of the
// certsigning.Signer interface (resolved alias/pointer/value-proof).
func isCertsigningSignerSign(fn *types.Func, csPath string) bool {
	if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != csPath || fn.Name() != "Sign" {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	recv := sig.Recv().Type()
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	return ok && named.Obj() != nil && named.Obj().Name() == "Signer"
}

// TestCertlifecycleSignViaFunnel01_GreenProduction asserts the certlifecycle
// package is scanned, imports the certsigning seam, calls Signer.Sign (funnel
// use), and never calls NewIssuedCert (no self-mint).
func TestCertlifecycleSignViaFunnel01_GreenProduction(t *testing.T) {
	t.Parallel()
	csPath := certSigningPkgPath()
	clPath := certlifecyclePkgPath()
	var visited, importsCS, callsSign bool
	diags := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != clPath {
			return nil
		}
		visited = true
		importsCS = pkgImports(p.Pkg, csPath)
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if IsCallToPkgFunc(p.TypesInfo, call, csPath, "NewIssuedCert") {
					pos := p.Fset.Position(call.Pos())
					out = append(out, Diagnostic{Rel: rel, Line: pos.Line, Message: certlifecycleMintMsg})
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "Sign" {
					if fn, ok := ResolveMethodCall(p.TypesInfo, sel); ok && isCertsigningSignerSign(fn, csPath) {
						callsSign = true
					}
				}
			})
		}
		return out
	})
	if !visited {
		t.Fatal("CERTLIFECYCLE-SIGN-VIA-FUNNEL-01: certlifecycle package was never scanned — the check is vacuous")
	}
	if !importsCS {
		t.Error("CERTLIFECYCLE-SIGN-VIA-FUNNEL-01: certlifecycle does not import certsigning — it must depend on the seam")
	}
	if !callsSign {
		t.Error("CERTLIFECYCLE-SIGN-VIA-FUNNEL-01: certlifecycle never calls certsigning.Signer.Sign — the signing " +
			"funnel is not used (anti-vacuity failure)")
	}
	if len(diags) > 0 {
		t.Errorf("CERTLIFECYCLE-SIGN-VIA-FUNNEL-01: certlifecycle must not mint certificates: %v", diags)
	}
}

// TestCertlifecycleSignViaFunnel01_NotInMintAllowlist pins the load-bearing
// cross-rule assumption that the existing CERT-SIGN-FUNNEL-01 mint allowlist
// covers certlifecycle: certlifecycle must NOT be a member (it obtains certs via
// Signer.Sign, never mints). Adding it would re-open direct minting and fail here.
func TestCertlifecycleSignViaFunnel01_NotInMintAllowlist(t *testing.T) {
	t.Parallel()
	if _, ok := certIssuedMintAllowlist[certlifecyclePkgPath()]; ok {
		t.Fatal("CERTLIFECYCLE-SIGN-VIA-FUNNEL-01: certlifecycle is in certIssuedMintAllowlist — it must obtain " +
			"certificates via certsigning.Signer.Sign, never mint them directly")
	}
}

// TestCertlifecycleSignViaFunnel01_RedFixture loads the archtest_fixture-tagged
// lifecycle-bypass fixture (a lifecycle-shaped package calling NewIssuedCert) and
// asserts the mint detector fires — the reverse self-check that the GREEN mint
// baseline is meaningful.
func TestCertlifecycleSignViaFunnel01_RedFixture(t *testing.T) {
	t.Parallel()
	csPath := certSigningPkgPath()
	diags := Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/certlifecyclebypassfixture/..."}),
		func(p *Pass) []Diagnostic {
			return scanCertMintCallers(p, csPath)
		})
	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}
	if len(diags) == 0 {
		t.Error("CERTLIFECYCLE-SIGN-VIA-FUNNEL-01 RED fixture: scanner found 0 NewIssuedCert calls; expected ≥ 1 " +
			"from certlifecyclebypassfixture — the mint detector may be broken")
	}
}
