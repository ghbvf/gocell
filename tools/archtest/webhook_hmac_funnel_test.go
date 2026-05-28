// INVARIANT: WEBHOOK-HMAC-FUNNEL-01
//
// WEBHOOK-HMAC-FUNNEL-01 — kernel/webhook HMAC signing funnel (KERNEL-WEBHOOK-01).
//
//   - A1 (downstream Hard): crypto/hmac.New has exactly one callsite in the
//     kernel/webhook package — the computeMAC function in signer.go. The check is
//     FUNCTION-level, not file-level: an hmac.New in any other function (even
//     inside signer.go) fails. This also closes the package-internal upstream
//     blind spot: any new struct that wants to sign MUST call hmac.New, which is
//     allowlisted to computeMAC, so an unsealed internal holder cannot produce a
//     signature undetected. Detection: ResolvePackageRef(callee) == crypto/hmac.New
//     AND (file basename != signer.go OR enclosing func != computeMAC).
//   - A2 (downstream Hard): signature comparison must use crypto/hmac.Equal or
//     crypto/subtle.ConstantTimeCompare. The non-constant-time comparison callees
//     bytes.Equal / bytes.Compare / slices.Equal / reflect.DeepEqual are banned in
//     the package. This makes the constant-time invariant an AST lock rather than
//     a flaky timing test. Detection: ResolvePackageRef(callee) ∈ the banned set.
//   - A3 (upstream Hard external / Medium internal): the Signer and Verifier
//     interfaces each carry an unexported sealed() marker method, so
//     package-external implementations are a compile error (Hard). Package-internal
//     new holders are not blocked by sealing (Medium) — covered transitively by
//     A1. Explicit Hard-ization of the internal axis (unexported method-set
//     interface + private construction, per the SPAN-SETATTR-HOLDER-SEAL #851
//     precedent) is tracked in gh #1243. Detection: the Signer/Verifier interface
//     type decls must contain an unexported method.
//
// Blind spots (ai-robust 强制反向自检; each has a reverse self-test below):
//
//	B-A1/A2 — rule-logic regression: a reverse fixture module
//	  (testdata/webhook_hmac_violate) calls hmac.New outside computeMAC (both
//	  outside signer.go and inside signer.go in a non-computeMAC func) and the
//	  banned comparison callees; TestWebhookHMACFunnel_ReverseFixture asserts A1/A2
//	  fire on each form.
//	B6 — Source.Secret leak: slog of the raw unexported secret field would leak
//	  it (Source.LogValue + slog.LogValuer covers slog.Any of a whole Source, but
//	  not slog of src.secret directly). scanWebhookSecretSlog covers BOTH the
//	  package-function form (slog.Info(...)) and the method form
//	  (logger.Info(...)). TestWebhookFunnel_NoRawSecretSlog asserts no such call in
//	  the package references a `.secret` selector.
//	B7 — non-AST-detectable constant-time bypass: comparing the base64 signature
//	  STRINGS with `==` (e.g. expectedB64 == presentedB64) is non-constant-time but
//	  is a plain *ast.BinaryExpr with no resolvable callee, so the A2 callee scan
//	  cannot see it. Detecting it would need type-level data-flow analysis. Bounded
//	  response: production matchAnySignature compares raw MAC bytes via hmac.Equal
//	  (A2-clean), and this blind spot is documented here so a future reviewer knows
//	  the AST scan does not cover string-`==`.
//
// ref: docs/architecture/202605291200-adr-webhook-signing-algorithm.md
// ref: tools/archtest/healthz_invariants_test.go (callsite-allowlist template)
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	webhookPkgPattern  = "./kernel/webhook/..."
	signerFileBasename = "signer.go"
	computeMACFuncName = "computeMAC"
	hmacPkgPath        = "crypto/hmac"
	slogPkgPath        = "log/slog"
)

// webhookSealedInterfaces are the interface type names in kernel/webhook that
// must carry an unexported sealed() marker (A3).
var webhookSealedInterfaces = map[string]bool{"Signer": true, "Verifier": true}

// nonConstTimeCompareCallees is the A2 banned set: package-qualified callees
// that compare bytes/values in non-constant time. Signature comparison must use
// crypto/hmac.Equal or crypto/subtle.ConstantTimeCompare instead.
var nonConstTimeCompareCallees = map[[2]string]bool{
	{"bytes", "Equal"}:       true,
	{"bytes", "Compare"}:     true,
	{"slices", "Equal"}:      true,
	{"reflect", "DeepEqual"}: true,
}

// scanWebhookHMACNew implements A1: crypto/hmac.New may be called only inside the
// computeMAC function of signer.go (function-level, not merely file-level).
func scanWebhookHMACNew(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	inSignerFile := filepath.Base(filepath.ToSlash(rel)) == signerFileBasename
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != hmacPkgPath || name != "New" {
			return
		}
		if inSignerFile {
			if fn, ok := ResolveEnclosingFunc(info, file, call); ok && fn != nil && fn.Name() == computeMACFuncName {
				return
			}
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "crypto/hmac.New called outside computeMAC (signer.go); " +
				"all HMAC computation must funnel through computeMAC (WEBHOOK-HMAC-FUNNEL-01/A1)",
		})
	})
	return out
}

// scanWebhookBytesEqual implements A2: non-constant-time comparison callees are
// banned in the package (signature comparison must use hmac.Equal /
// subtle.ConstantTimeCompare). See nonConstTimeCompareCallees for the set; the
// string-`==` form is the documented blind spot B7.
func scanWebhookBytesEqual(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || !nonConstTimeCompareCallees[[2]string{pkgPath, name}] {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: pkgPath + "." + name + " called in kernel/webhook; signature comparison must use " +
				"crypto/hmac.Equal or crypto/subtle.ConstantTimeCompare (WEBHOOK-HMAC-FUNNEL-01/A2)",
		})
	})
	return out
}

// isSlogCall reports whether call is a log/slog call in either form: a
// package-function call (slog.Info(...)) or a method call on a *slog.Logger
// (logger.Info(...)).
func isSlogCall(call *ast.CallExpr, info *types.Info) bool {
	if pkgPath, _, ok := ResolvePackageRef(info, call.Fun); ok && pkgPath == slogPkgPath {
		return true
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	return ok && fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == slogPkgPath
}

// scanWebhookSecretSlog implements B6: no slog call (package-function OR
// *slog.Logger method form) may pass a `.secret` selector (the raw unexported
// secret field).
func scanWebhookSecretSlog(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isSlogCall(call, info) {
			return
		}
		for _, arg := range call.Args {
			EachInSubtree[ast.SelectorExpr](arg, func(sel *ast.SelectorExpr) {
				if sel.Sel.Name == "secret" {
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: fset.Position(call.Pos()).Line,
						Message: "slog call references a raw .secret field in kernel/webhook; " +
							"log a redacted Source instead (WEBHOOK-HMAC-FUNNEL-01 B6)",
					})
				}
			})
		}
	})
	return out
}

// interfaceSealed reports the unexported-marker status of the named sealed
// interfaces declared in file. Returns a map name→hasUnexportedMethod for any
// of webhookSealedInterfaces found.
func collectWebhookSealedMarkers(file *ast.File) map[string]bool {
	found := map[string]bool{}
	EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if !webhookSealedInterfaces[ts.Name.Name] {
			return
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			return
		}
		hasUnexported := false
		for _, m := range iface.Methods.List {
			for _, n := range m.Names {
				if !n.IsExported() {
					hasUnexported = true
				}
			}
		}
		found[ts.Name.Name] = hasUnexported
	})
	return found
}

func TestWebhookHMACFunnel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var a1, a2, b6 []Diagnostic
	sealed := map[string]bool{}

	_ = RunTyped(t, TypedOpts{Tests: false}, []string{webhookPkgPattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != "github.com/ghbvf/gocell/kernel/webhook" {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				a1 = append(a1, scanWebhookHMACNew(p.Fset, f, rel, p.TypesInfo)...)
				a2 = append(a2, scanWebhookBytesEqual(p.Fset, f, rel, p.TypesInfo)...)
				b6 = append(b6, scanWebhookSecretSlog(p.Fset, f, rel, p.TypesInfo)...)
				for name, ok := range collectWebhookSealedMarkers(f) {
					sealed[name] = ok
				}
			}
			return nil
		})

	Report(t, "WEBHOOK-HMAC-FUNNEL-01/A1", a1)
	Report(t, "WEBHOOK-HMAC-FUNNEL-01/A2", a2)
	Report(t, "WEBHOOK-HMAC-FUNNEL-01/B6", b6)

	// A3: both interfaces must exist and carry an unexported sealed() marker.
	for name := range webhookSealedInterfaces {
		hasUnexported, found := sealed[name]
		assert.True(t, found, "WEBHOOK-HMAC-FUNNEL-01/A3: interface %s not found in kernel/webhook", name)
		assert.True(t, hasUnexported,
			"WEBHOOK-HMAC-FUNNEL-01/A3: interface %s must carry an unexported sealed() marker "+
				"so package-external implementations are a compile error", name)
	}
}

// TestWebhookHMACFunnel_ReverseFixture loads the synthetic violation fixture and
// asserts A1 (hmac.New outside signer.go) and A2 (bytes.Equal) fire — guards
// against the rule logic silently regressing to a vacuous pass.
func TestWebhookHMACFunnel_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "webhook_hmac_violate")

	var a1, a2, b6 []Diagnostic
	_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				a1 = append(a1, scanWebhookHMACNew(p.Fset, f, rel, p.TypesInfo)...)
				a2 = append(a2, scanWebhookBytesEqual(p.Fset, f, rel, p.TypesInfo)...)
				b6 = append(b6, scanWebhookSecretSlog(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	// A1: both forms must fire — hmac.New outside signer.go (violations.go) AND
	// hmac.New inside signer.go but outside computeMAC (signer.go fixture file).
	assert.GreaterOrEqual(t, len(a1), 2,
		"A1 reverse fixture: expected ≥2 diagnostics (hmac.New outside signer.go + inside signer.go non-computeMAC)")
	// A2: every banned comparison callee must fire (Equal/Compare/slices.Equal/reflect.DeepEqual).
	assert.GreaterOrEqual(t, len(a2), 4,
		"A2 reverse fixture: expected ≥4 diagnostics (bytes.Equal/bytes.Compare/slices.Equal/reflect.DeepEqual)")
	// B6: both the package-function form and the *slog.Logger method form must fire.
	assert.GreaterOrEqual(t, len(b6), 2,
		"B6 reverse fixture: expected ≥2 diagnostics (slog.Info package form + logger.Info method form)")
}

// TestWebhookFunnel_NoRawSecretSlog is the B6 blind-spot self-check: production
// kernel/webhook must contain no slog call referencing a raw .secret field.
func TestWebhookFunnel_NoRawSecretSlog(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var b6 []Diagnostic
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{webhookPkgPattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != "github.com/ghbvf/gocell/kernel/webhook" {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				b6 = append(b6, scanWebhookSecretSlog(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})
	require.Empty(t, b6, "B6 self-check: no slog call may reference a raw .secret field")
}
