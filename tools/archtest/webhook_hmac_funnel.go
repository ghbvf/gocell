// INVARIANT: WEBHOOK-HMAC-FUNNEL-01
//
// webhook_hmac_funnel.go — importable webhook HMAC signing funnel rule logic.
//
// WEBHOOK-HMAC-FUNNEL-01 — kernel/webhook HMAC signing funnel (KERNEL-WEBHOOK-01).
//
//   - A1 (downstream Hard): crypto/hmac.New has exactly one callsite in the
//     kernel/webhook package — the computeMAC function. The check is
//     FUNCTION-IDENTITY level via go/types FullName (file-independent; #1493): an
//     hmac.New whose enclosing func FullName != computeMAC's fails, and a
//     same-named function in another package cannot inherit the allowance
//     (mirrors WEBHOOK-SSRF-GUARD-01/A2). Detection: ResolvePackageRef(callee) ==
//     crypto/hmac.New AND ResolveEnclosingFunc(call).FullName() !=
//     hmacSanctionedComputeMACFunc.
//   - A2 (downstream Hard): signature comparison must use crypto/hmac.Equal or
//     crypto/subtle.ConstantTimeCompare. The non-constant-time comparison callees
//     bytes.Equal / bytes.Compare / slices.Equal / reflect.DeepEqual are banned in
//     the package. This makes the constant-time invariant an AST lock rather than
//     a flaky timing test. Detection: ResolvePackageRef(callee) ∈ the banned set.
//   - A3 (upstream Hard external / Medium internal): the Signer and Verifier
//     interfaces each carry an unexported sealed() marker method, so
//     package-external implementations are a compile error (Hard external).
//     Package-internal new holders are NOT blocked by sealing — and contrary to a
//     prior overclaim, A1 does NOT transitively cover them: A1 locks only
//     crypto/hmac.New, not reuse of the package-level computeMAC helper, so a
//     rogue in-package struct could call computeMAC and produce a valid signature
//     without its own hmac.New. That internal axis is instead covered by A4
//     (computeMAC caller-allowlist, Medium). True type-system Hard for the
//     internal axis is a PERMANENT Go ceiling (in-package code can always declare
//     sealed(), read Source.secret, and call computeMAC) — same family as #851 /
//     #893 / #1282 / #1375; #1243 is relabeled won't-do and named here as that
//     ceiling's tracker. Detection: the Signer/Verifier interface type decls must
//     contain an unexported method.
//   - A4 (Medium, internal axis; #1243): every USE of the package-internal
//     computeMAC symbol — direct call, parenthesized call, OR function-value
//     capture (`macFn := computeMAC`; the bypass review #1733 F4 flagged) — must
//     have an enclosing func whose go/types FullName ∈ {hmacSigner.Sign,
//     hmacVerifier.Verify}. This is the actual enforcement of "only the sanctioned
//     signer/verifier may compute a MAC", closing the A3 internal axis at Medium
//     (Hard is the permanent ceiling above). Detection: walk every ident, match
//     info.Uses FullName == computeMAC's, require enclosing FullName ∈ allowlist
//     (the computeMAC definition is in info.Defs, not info.Uses, so it never fires).
//
// Blind spots (ai-robust 强制反向自检; each has a reverse self-test in the _test.go):
//
//	B-A1/A2 — rule-logic regression: a reverse fixture module
//	  (testdata/webhook_hmac_violate) calls hmac.New outside computeMAC (both
//	  outside signer.go and inside signer.go in a non-computeMAC func) and the
//	  banned comparison callees; TestWebhookHMACFunnel_ReverseFixture asserts A1/A2
//	  fire on each form.
//	B-A4 — rule-logic regression (computeMAC is unexported, so no standalone-module
//	  RED fixture can call it): TestWebhookHMACFunnel_ComputeMACCallerAntiVacuity
//	  re-runs the A4 scan over real production with an EMPTY allowlist and asserts it
//	  fires on the ≥2 real computeMAC callers (Sign + Verify) — proving the scan
//	  detects computeMAC calls and the allowlist is what suppresses them (non-vacuous).
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
// This is the non-test home of the WEBHOOK-HMAC-FUNNEL-01 scanner helpers so
// they can be compiled by external Cell repositories through the CellRule
// pattern (Go never compiles a dependency's _test.go, so rule logic that
// external repos must run cannot live in a _test.go file). GoCell's own
// TestWebhookHMACFunnel function (webhook_hmac_funnel_test.go) calls the same
// shared helpers — single source, no parallel rule body.
//
// register=no — gocell-internal-layout (scans kernel/webhook), NOT in
// StandardCellRules() and NOT promised to run externally — this dogfood-only
// rule targets a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via TestWebhookHMACFunnel).
//
// Platform-symbol paths are anchored to [PlatformModulePath] so a module
// rename updates exactly one place and no bare literal appears here.
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
)

// webhookPkgPath is the canonical import path of the kernel/webhook package —
// derived from PlatformModulePath so no bare literal appears here.
const webhookPkgPath = PlatformModulePath + "/kernel/webhook"

const (
	webhookPkgPattern  = "./kernel/webhook/..."
	signerFileBasename = "signer.go"
	hmacPkgPath        = "crypto/hmac"
	slogPkgPath        = "log/slog"

	// hmacSanctionedComputeMACFunc is the go/types FullName of the ONE function
	// allowed to call crypto/hmac.New: the free func computeMAC in signer.go.
	// FullName encodes the package path, so a same-named function in another
	// package cannot inherit the A1 allowance (mirrors WEBHOOK-SSRF-GUARD-01/A2's
	// ssrfSanctionedDialFunc; #1493). computeMAC is package-unique, so the check is
	// file-independent — computeMAC may move files freely. It also doubles as the
	// callee identity for the A4 caller-allowlist scan (computeMAC's own FullName).
	hmacSanctionedComputeMACFunc = PlatformModulePath + "/kernel/webhook.computeMAC"

	// hmacSanctionedSignFunc / hmacSanctionedVerifyFunc are the go/types FullNames
	// of the ONLY two functions permitted to call computeMAC (A4 caller-allowlist).
	hmacSanctionedSignFunc   = "(*" + PlatformModulePath + "/kernel/webhook.hmacSigner).Sign"
	hmacSanctionedVerifyFunc = "(*" + PlatformModulePath + "/kernel/webhook.hmacVerifier).Verify"
)

// webhookSealedInterfaces are the interface type names in kernel/webhook that
// must carry an unexported sealed() marker (A3).
var webhookSealedInterfaces = map[string]bool{"Signer": true, "Verifier": true}

// computeMACCallerAllowlist is the set of go/types FullNames permitted to call
// the package-internal computeMAC helper (A4). Any other in-package caller could
// compute a valid MAC and thereby forge a signature WITHOUT an hmac.New callsite
// of its own — A1 only locks hmac.New, not computeMAC reuse, so A4 is the actual
// enforcement of "only the sanctioned signer/verifier may produce a MAC".
var computeMACCallerAllowlist = map[string]bool{
	hmacSanctionedSignFunc:   true,
	hmacSanctionedVerifyFunc: true,
}

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
// computeMAC function (function-identity level via go/types FullName, file-
// independent; #1493). The allowance is bound to computeMAC's FullName, not its
// bare name, so a same-named function in another package cannot inherit it.
func scanWebhookHMACNew(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != hmacPkgPath || name != "New" {
			return
		}
		if fn, ok := ResolveEnclosingFunc(info, file, call); ok && fn != nil &&
			fn.FullName() == hmacSanctionedComputeMACFunc {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "crypto/hmac.New called outside computeMAC; " +
				"all HMAC computation must funnel through computeMAC (WEBHOOK-HMAC-FUNNEL-01/A1)",
		})
	})
	return out
}

// scanWebhookComputeMACCallers implements A4: every USE of the package-internal
// computeMAC symbol must have an enclosing func whose go/types FullName is in
// allowlist. It walks every identifier and matches info.Uses to computeMAC's
// FullName, so ALL use forms are covered — direct call `computeMAC(...)`,
// parenthesized `(computeMAC)(...)`, and function-value capture
// `macFn := computeMAC` (the bypass review #1733 F4 flagged); the older
// call.Fun.(*ast.Ident) form only caught direct calls. The computeMAC *definition*
// ident lives in info.Defs (not info.Uses), so signer.go's declaration never
// false-fires. Passing allowlist as a parameter lets the anti-vacuity self-test
// re-run with an empty allowlist to prove the scan detects the real uses.
func scanWebhookComputeMACCallers(
	fset *token.FileSet, file *ast.File, rel string, info *types.Info, allowlist map[string]bool,
) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		fnObj, ok := info.Uses[id].(*types.Func)
		if !ok || fnObj.FullName() != hmacSanctionedComputeMACFunc {
			return
		}
		if enc, ok := ResolveEnclosingFunc(info, file, id); ok && enc != nil && allowlist[enc.FullName()] {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(id.Pos()).Line,
			Message: "computeMAC used outside the sanctioned signer/verifier (direct call, " +
				"parenthesized call, or function-value capture); MAC computation must funnel " +
				"through hmacSigner.Sign / hmacVerifier.Verify (WEBHOOK-HMAC-FUNNEL-01/A4)",
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
		collectSecretSlogArgs(fset, call, rel, &out)
	})
	return out
}

// collectSecretSlogArgs checks each argument of a slog call for a `.secret`
// selector and appends a Diagnostic for each match. Extracted to keep
// scanWebhookSecretSlog within gocognit ≤15.
func collectSecretSlogArgs(fset *token.FileSet, call *ast.CallExpr, rel string, out *[]Diagnostic) {
	for _, arg := range call.Args {
		EachInSubtree[ast.SelectorExpr](arg, func(sel *ast.SelectorExpr) {
			if sel.Sel.Name == "secret" {
				*out = append(*out, Diagnostic{
					Rel:  rel,
					Line: fset.Position(call.Pos()).Line,
					Message: "slog call references a raw .secret field in kernel/webhook; " +
						"log a redacted Source instead (WEBHOOK-HMAC-FUNNEL-01 B6)",
				})
			}
		})
	}
}

// webhookSealInfo records, for a sealed interface decl found during the scan,
// whether it carries an unexported sealed() marker plus its source location, so
// the A3 diagnostic can anchor to the interface declaration (rel:line) instead
// of degrading to ":0:".
type webhookSealInfo struct {
	hasUnexported bool
	rel           string
	line          int
}

// collectWebhookSealedMarkers reports the unexported-marker status of the named
// sealed interfaces declared in file. Returns a map name→webhookSealInfo for any
// of webhookSealedInterfaces found in this file.
func collectWebhookSealedMarkers(fset *token.FileSet, file *ast.File, rel string) map[string]webhookSealInfo {
	found := map[string]webhookSealInfo{}
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
		found[ts.Name.Name] = webhookSealInfo{
			hasUnexported: hasUnexported,
			rel:           rel,
			line:          fset.Position(ts.Pos()).Line,
		}
	})
	return found
}

// webhookScanAccum collects the per-rule diagnostic slices threaded through
// scanWebhookPkg, keeping that helper's signature within lint limits while it
// fans the file scans (A1/A2/A4/B6) plus sealed-marker collection.
type webhookScanAccum struct {
	a1, a2, a4, b6 *[]Diagnostic
	sealed         map[string]webhookSealInfo
}

// scanWebhookPkg runs the A1/A2/A4/B6 file-level scans and sealed-marker
// collection over all non-test production files in the webhook package pass.
// Extracted from CheckWebhookHMACFunnel to keep the Check func within
// gocognit ≤15. It returns a package-anchor rel (preferring signer.go) used to
// locate the A3 "interface not found" diagnostic, which has no decl to point at.
func scanWebhookPkg(p *Pass, acc webhookScanAccum) string {
	var pkgRel string
	for _, f := range p.Files {
		rel := p.Rel(f)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if pkgRel == "" || filepath.Base(filepath.ToSlash(rel)) == signerFileBasename {
			pkgRel = rel
		}
		*acc.a1 = append(*acc.a1, scanWebhookHMACNew(p.Fset, f, rel, p.TypesInfo)...)
		*acc.a2 = append(*acc.a2, scanWebhookBytesEqual(p.Fset, f, rel, p.TypesInfo)...)
		*acc.a4 = append(*acc.a4, scanWebhookComputeMACCallers(p.Fset, f, rel, p.TypesInfo, computeMACCallerAllowlist)...)
		*acc.b6 = append(*acc.b6, scanWebhookSecretSlog(p.Fset, f, rel, p.TypesInfo)...)
		for name, info := range collectWebhookSealedMarkers(p.Fset, f, rel) {
			acc.sealed[name] = info
		}
	}
	return pkgRel
}

// CheckWebhookHMACFunnel runs the WEBHOOK-HMAC-FUNNEL-01 production scan
// (A1/A2/A3/A4/B6) and returns all diagnostics.
//
// cfg is unused: kernel/webhook has no build-tagged production files, so a
// single default-config scan is complete.
//
// register=no — gocell-internal-layout (scans kernel/webhook), NOT in
// StandardCellRules() and NOT promised to run externally — this dogfood-only
// rule targets a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via TestWebhookHMACFunnel).
func CheckWebhookHMACFunnel(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	var a1, a2, a4, b6 []Diagnostic
	sealed := map[string]webhookSealInfo{}
	var pkgRel string

	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{webhookPkgPattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != webhookPkgPath {
				return nil
			}
			pkgRel = scanWebhookPkg(p, webhookScanAccum{a1: &a1, a2: &a2, a4: &a4, b6: &b6, sealed: sealed})
			return nil
		})

	var all []Diagnostic
	all = append(all, a1...)
	all = append(all, a2...)
	all = append(all, a4...)
	all = append(all, b6...)
	all = append(all, checkWebhookSealedMarkers(sealed, pkgRel)...)
	return all
}

// checkWebhookSealedMarkers validates A3 and converts violations to
// Diagnostics. Extracted to keep CheckWebhookHMACFunnel within gocognit ≤15.
// A "missing marker" diagnostic anchors to the interface decl (info.rel:line);
// an "interface not found" diagnostic anchors to pkgRel (the package's signer.go
// or first production file) since there is no decl to point at.
func checkWebhookSealedMarkers(sealed map[string]webhookSealInfo, pkgRel string) []Diagnostic {
	var out []Diagnostic
	for name := range webhookSealedInterfaces {
		info, found := sealed[name]
		if !found {
			out = append(out, diagFile(pkgRel,
				"WEBHOOK-HMAC-FUNNEL-01/A3: interface "+name+" not found in kernel/webhook"))
			continue
		}
		if !info.hasUnexported {
			out = append(out, diagAt(info.rel, info.line,
				"WEBHOOK-HMAC-FUNNEL-01/A3: interface "+name+
					" must carry an unexported sealed() marker so package-external implementations are a compile error"))
		}
	}
	return out
}
