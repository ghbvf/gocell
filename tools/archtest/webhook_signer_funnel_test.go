// INVARIANT: WEBHOOK-SIGNER-FUNNEL-01
//
// WEBHOOK-SIGNER-FUNNEL-01 — kernel/webhook signature-header write funnel.
//
// The three outbound webhook signature headers (webhook-id / webhook-timestamp
// / webhook-signature) define GoCell's outbound identity protocol: only a value
// produced by a sealed [Signer.Sign] (which returns a [Headers]) may reach the
// wire. That guarantee is worthless if any code can bypass it by calling
// http.Header.Set with one of the three header-name strings directly.
//
// This rule locks every http.Header.Set call whose key argument evaluates to
// one of the three header-name constant values to the sole sanctioned body:
// [(Headers).Apply] in kernel/webhook/webhook.go.
//
//   - A1 (downstream Hard): scan production (non-_test.go) files in
//     kernel/webhook/... and runtime/webhook/... for any
//     (net/http.Header).Set call whose key argument evaluates (via
//     EvaluateConstString) to one of the three header-name strings
//     ("webhook-id" / "webhook-timestamp" / "webhook-signature"). Any such
//     call whose enclosing function is NOT [signerSanctionedApplyFunc] is a
//     violation. Detection uses ResolveMethodCall to confirm the callee is
//     net/http.Header.Set (alias-safe), EvaluateConstString to fold the key
//     argument across const refs and raw literals, and ResolveEnclosingFunc to
//     bind the allowance to a go/types FullName identity (file- and
//     name-independent).
//
// AI-robust rating (Funnel 双向锁评级, per .claude/rules/gocell/ai-robust.md):
//
//	下游 Hard — A1 is form-unique, type-resolved callsite-allowlist.
//	  ResolveMethodCall resolves import aliases and value-refs so alias bypass
//	  is ineffective. EvaluateConstString folds across const definitions.
//	  ResolveEnclosingFunc binds the allowance to a go/types FullName — a
//	  stray func that merely shares the name "Apply" cannot inherit it.
//	上游 Hard (external) — [Headers] is produced only by a sealed
//	  [Signer.Sign]: Signer carries an unexported sealed() marker method
//	  (WEBHOOK-HMAC-FUNNEL-01/A3) so package-external Signer implementations
//	  are a compile error; Sign() is the only path to a Headers value; and
//	  Headers has no exported constructor outside of Sign. Thus an
//	  externally-created Headers value (and hence a call to Apply) is
//	  inexpressible at the type-system level.
//	上游 Medium (package-internal) — Go package visibility cannot express
//	  "only one struct within the same package may declare a Headers field".
//	  A new package-internal struct that holds a Headers value and calls
//	  Apply is syntactically expressible; it is caught by the downstream A1
//	  scan at CI time, not at compile time. This is the same permanent ceiling
//	  as SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL (#893) /
//	  WEBHOOK-SSRF-GUARD-01 (#1375). Explicit Hard-ization of the
//	  package-internal axis is tracked in gh #1243 (inherited from the Signer
//	  sealing issue, which already covers this axis).
//
// Note: WEBHOOK-SIGNED-STRING-FORM-01 (signed-string "{deliveryID}.{timestamp}
// .{body}" form) is NOT a new archtest here. It is already locked by:
//   - TestSign_MatchesSvixGoldenVector in kernel/webhook/signer_test.go
//     (byte-exact Svix golden vector: any change to the signed-string format
//     breaks this test immediately)
//   - WEBHOOK-HMAC-FUNNEL-01/A1 (sole computeMAC callsite — the MAC covers
//     the signed string, so the format cannot change without also changing the
//     unique computeMAC callsite, which is in the archtest allowlist)
//
// A duplicate AST literal-lock would create parallel structure without
// additional safety; both existing locks are already behavioral. (Per
// ai-robust.md: avoid parallel structure.)
//
// Blind spots (ai-robust 强制反向自检; each has a reverse self-test below):
//
//	B-A1 — rule-logic regression: testdata/webhook_signer_violate exercises
//	  Header.Set with a raw string literal key and with a const key that
//	  evaluates to each of the three header names;
//	  TestWebhookSignerFunnel_ReverseFixture asserts A1 fires on each form.
//	B3 — fmt.Fprintf raw-write bypass: code that writes the header via
//	  fmt.Fprintf(w, "webhook-signature: %s\r\n", val) on a raw net.Conn or
//	  bufio.Writer cannot be caught by the Set-callee scan (no CallExpr with a
//	  resolvable net/http.Header.Set callee). Bounded response: the realistic
//	  egress path for outbound webhooks is http.Client.Do over an *http.Request
//	  (which uses header.Set internally); raw TCP writes that happen to embed
//	  the header name would be caught by WEBHOOK-SSRF-GUARD-01/A1 (net.Dial*
//	  ban) before they could produce a valid delivery.
//
// ref: docs/architecture/202605291200-adr-webhook-signing-algorithm.md
// ref: tools/archtest/webhook_hmac_funnel_test.go (A3 sealed-interface template)
// ref: tools/archtest/webhook_ssrf_guard_test.go (FullName-bound allowance template)
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	// signerSanctionedApplyFunc is the go/types canonical FullName of the ONE
	// function allowed to call Header.Set with a webhook signature-header key.
	// Headers has a value receiver, so FullName does NOT have a leading "*".
	// Binding the A1 allowance to this type-resolved identity means a stray func
	// that merely shares the name "Apply" cannot inherit it, and the method can
	// move files freely without updating this const. A rename of the method flips
	// every violation to caught (fail-closed), forcing the author to update here.
	signerSanctionedApplyFunc = "(" + PlatformModulePath + "/kernel/webhook.Headers).Apply"

	httpPkgPath = "net/http"
)

// webhookSignatureHeaders is the set of header-name string VALUES that are
// locked to (Headers).Apply. The scan evaluates the key argument to a string
// constant (EvaluateConstString) and checks membership here — covering both
// raw string literals and const references from any package.
var webhookSignatureHeaders = map[string]bool{
	"webhook-id":        true,
	"webhook-timestamp": true,
	"webhook-signature": true,
}

// scanSignerHeaderSet implements A1: http.Header.Set calls whose key evaluates
// to one of the three signature-header names must be enclosed by
// signerSanctionedApplyFunc. Any other enclosing function (including the
// package-level init or a FuncLit) is a violation.
func scanSignerHeaderSet(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		// Confirm the callee is (net/http.Header).Set — type-resolved, so import
		// aliases cannot bypass.
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		fn, ok := ResolveMethodCall(info, sel)
		if !ok || fn == nil || fn.Pkg() == nil ||
			fn.Pkg().Path() != httpPkgPath || fn.Name() != "Set" {
			return
		}
		// Confirm the receiver type is net/http.Header (not some other type
		// that also has a Set method).
		recvType := info.TypeOf(sel.X)
		if recvType == nil {
			return
		}
		// Unwrap pointer if needed.
		if ptr, ok := recvType.(*types.Pointer); ok {
			recvType = ptr.Elem()
		}
		named, ok := recvType.(*types.Named)
		if !ok {
			return
		}
		if named.Obj() == nil || named.Obj().Pkg() == nil ||
			named.Obj().Pkg().Path() != httpPkgPath || named.Obj().Name() != "Header" {
			return
		}

		// Call is confirmed to be (net/http.Header).Set. Evaluate the key arg.
		if len(call.Args) < 1 {
			return
		}
		keyVal, ok := EvaluateConstString(info, call.Args[0])
		if !ok || !webhookSignatureHeaders[keyVal] {
			return
		}

		// Key matches a signature header. Check the enclosing function.
		enc, ok := ResolveEnclosingFunc(info, file, call)
		if ok && enc != nil && enc.FullName() == signerSanctionedApplyFunc {
			return // sanctioned
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "http.Header.Set with key \"" + keyVal + "\" called outside " +
				"(Headers).Apply; signature headers must only be written by " +
				"Headers.Apply (WEBHOOK-SIGNER-FUNNEL-01/A1)",
		})
	})
	return out
}

// webhookSignerPkgPatterns are the package patterns scanned by
// TestWebhookSignerFunnel. Covers both the kernel types and the runtime
// consumer layer.
var webhookSignerPkgPatterns = []string{
	"./kernel/webhook/...",
	"./runtime/webhook/...",
}

func TestWebhookSignerFunnel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var a1 []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false}, webhookSignerPkgPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			// Scope: the kernel/webhook and runtime/webhook production trees,
			// INCLUDING subpackages. Exact-match previously excluded
			// runtime/webhook/dispatch — the ./runtime/webhook/... pattern loads
			// it, but the filter dropped it — so a bare Header.Set of a signature
			// header in the dispatcher consumer layer (where Headers.Apply is
			// actually called) would have slipped past the funnel. Subtree-match
			// (exact base or base+"/") also auto-covers any future subpackage.
			pkgPath := p.Pkg.Path()
			inTree := func(base string) bool {
				return pkgPath == base || strings.HasPrefix(pkgPath, base+"/")
			}
			if !inTree(PlatformModulePath+"/kernel/webhook") &&
				!inTree(PlatformModulePath+"/runtime/webhook") {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				a1 = append(a1, scanSignerHeaderSet(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	Report(t, "WEBHOOK-SIGNER-FUNNEL-01/A1", a1)
}

// TestWebhookSignerFunnel_ReverseFixture loads the synthetic violation fixture
// and asserts A1 fires on every violation form — guards against the rule logic
// silently regressing to a vacuous pass (B-A1 blind-spot self-test).
//
// The fixture covers:
//
//   - raw string literal key "webhook-signature"
//   - const key whose value is "webhook-signature"
//   - const key whose value is "webhook-id"
//   - const key whose value is "webhook-timestamp"
//
// so that dropping any one of the three header names from webhookSignatureHeaders
// causes this test to fail.
func TestWebhookSignerFunnel_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "webhook_signer_violate")

	var a1 []Diagnostic
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
				a1 = append(a1, scanSignerHeaderSet(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	// The fixture has exactly 4 violations:
	//   - 1 raw string literal key "webhook-signature"
	//   - 3 const-keyed calls, one per header name (webhook-id / webhook-timestamp
	//     / webhook-signature)
	// Asserting ≥4 ensures that dropping ANY single fixture violation (e.g. removing
	// one const form or one header name from webhookSignatureHeaders) causes this
	// self-test to fail immediately, rather than passing on the remaining 3.
	const webhookSignerFixtureMinViolations = 4
	assert.GreaterOrEqual(t, len(a1), webhookSignerFixtureMinViolations,
		"A1 reverse fixture: expected ≥4 diagnostics (1 raw literal + 3 const refs, "+
			"one per header name webhook-id / webhook-timestamp / webhook-signature)")
}
