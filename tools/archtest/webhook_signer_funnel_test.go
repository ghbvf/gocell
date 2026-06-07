// INVARIANT: WEBHOOK-SIGNER-FUNNEL-01
//
// WEBHOOK-SIGNER-FUNNEL-01 — kernel/webhook signature-header write funnel.
//
// The three outbound webhook signature headers (webhook-id / webhook-timestamp
// / webhook-signature) define GoCell's outbound identity protocol. This rule is
// a CLOSED double-lock funnel (#1492 + #1733 F1): [(SignedHeaders).Apply] is the
// SOLE write site for these header names (A1, downstream), and [SignedHeaders] —
// the value Apply writes — is sealed-construction + a `valid` provenance flag so
// only [Signer.Sign] can produce a wire-writable one; Apply fail-closes on a
// zero value (A2, upstream Hard external).
//
//   - A1 (downstream Hard): scan production (non-_test.go) files in
//     kernel/webhook/... and runtime/webhook/... for any
//     (net/http.Header).Set call whose key argument evaluates (via
//     EvaluateConstString) to one of the three header-name strings
//     ("webhook-id" / "webhook-timestamp" / "webhook-signature"). Any such
//     call whose enclosing function is NOT [signerSanctionedApplyFunc]
//     ((SignedHeaders).Apply) is a violation. Detection uses ResolveMethodCall
//     to confirm the callee is net/http.Header.Set (alias-safe),
//     EvaluateConstString to fold the key argument across const refs and raw
//     literals, and ResolveEnclosingFunc to bind the allowance to a go/types
//     FullName identity (file- and name-independent). Plus (review #1733 F2):
//     a (net/http.Header).Set captured as a METHOD VALUE — `set := h.Set;
//     set("webhook-signature", v)` — is banned outright in the scanned tree
//     (scanSignerHeaderSetMethodValue), because the eventual key is not visible at
//     the capture site; the only sanctioned signature-header write is a direct
//     (SignedHeaders).Apply call.
//   - A2 (upstream Hard external, sealed construction + valid-token + reflect
//     freeze): [SignedHeaders] has UNEXPORTED fields, so an outside-package
//     POPULATED literal forge (webhook.SignedHeaders{signature: …}) is a Go
//     compile error. Unexported fields alone do NOT stop a ZERO-value construction
//     (`var h webhook.SignedHeaders`; review #1733 F1), so the type carries an
//     unexported `valid` provenance flag that ONLY [Signer.Sign] sets, and
//     [SignedHeaders.Apply] fail-closes (writes nothing) on a zero value — thus
//     only a Sign-produced SignedHeaders can write to the wire. The reflect schema
//     freeze (NumField + per-field name/type/unexported, INCL. `valid`) locks the
//     field set against in-package drift — exporting any field (esp. `valid`)
//     re-opens the forge vector. Detection: reflect.TypeOf(webhook.SignedHeaders{})
//     vs the frozen tuple (TestWebhookSignerFunnel_SignedHeadersSealedFields).
//
// AI-robust rating (Funnel 双向锁评级, per .claude/rules/gocell/ai-robust.md):
//
//	下游 Hard — A1 is form-unique, type-resolved callsite-allowlist.
//	  ResolveMethodCall resolves import aliases and value-refs so alias bypass
//	  is ineffective. EvaluateConstString folds across const definitions.
//	  ResolveEnclosingFunc binds the allowance to a go/types FullName — a
//	  stray func that merely shares the name "Apply" cannot inherit it.
//	上游 Hard (external) — CLOSED by #1492 + valid-token (#1733 F1). Before #1492,
//	  Apply lived on the public [Headers] struct with EXPORTED fields, so any caller
//	  could hand-build a `webhook.Headers{Signature: …}` literal and Apply it. #1492
//	  split the type: the inbound parse DTO stays [Headers] (untrusted by design — a
//	  forged inbound Headers just fails Verify), while the OUTBOUND value Apply
//	  writes is the sealed [SignedHeaders]. Unexported fields stop a POPULATED
//	  outside-package literal (compile error) but NOT a ZERO-value construction
//	  (`var h webhook.SignedHeaders`) — the overclaim review #1733 F1 caught. The
//	  closure is the unexported `valid` provenance flag: only [Signer.Sign] sets it,
//	  and Apply fail-closes (writes nothing) when valid==false, so an external
//	  zero-value Apply writes no signature headers. External code can set neither a
//	  populated literal nor `valid`, so only a Sign-produced SignedHeaders reaches
//	  the wire. A2's reflect freeze locks the field set (incl. `valid`) against
//	  in-package drift. (Read-only accessors don't weaken this — the seal is on
//	  construction; values travel on the wire in plaintext.)
//	上游 (package-internal holder axis) — permanent Go ceiling. A same-package
//	  SignedHeaders{…} literal (Go allows in-package access to unexported fields)
//	  is not compile-prevented; the A1 scan catches a stray in-package Header.Set
//	  at CI, not compile time. Same permanent ceiling as SPAN-SETATTR-HOLDER-SEAL
//	  (#851) / HEALTHZ-HOLDER-SEAL (#893) / outbox principal-write (#1282) /
//	  WEBHOOK-SSRF-GUARD-01 (#1375); Go cannot express "only Signer.Sign may build
//	  it in-package", so no separate Hard-upgrade issue.
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
//	  Header.Set with a raw string literal key, a const key per header name, AND
//	  a method-value capture (`set := h.Set`, #1733 F2);
//	  TestWebhookSignerFunnel_ReverseFixture asserts A1 fires on each form.
//	B-methodvalue — CLOSED (#1733 F2): a (net/http.Header).Set method-value capture
//	  is now caught by scanSignerHeaderSetMethodValue. The remaining uncovered form
//	  is reflection (reflect.Value.Call on Header.Set) — the permanent AST-scan
//	  ceiling, same as the B3 raw-write below; not realistically reachable.
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
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/webhook"
)

const (
	// signerSanctionedApplyFunc is the go/types canonical FullName of the ONE
	// function allowed to call Header.Set with a webhook signature-header key.
	// SignedHeaders has a value receiver, so FullName does NOT have a leading "*".
	// Binding the A1 allowance to this type-resolved identity means a stray func
	// that merely shares the name "Apply" cannot inherit it, and the method can
	// move files freely without updating this const. A rename of the method flips
	// every violation to caught (fail-closed), forcing the author to update here.
	signerSanctionedApplyFunc = "(" + PlatformModulePath + "/kernel/webhook.SignedHeaders).Apply"

	httpPkgPath = "net/http"
)

// webhookSignatureHeaders is the set of header-name string VALUES that are
// locked to (SignedHeaders).Apply. The scan evaluates the key argument to a
// string constant (EvaluateConstString) and checks membership here — covering
// both raw string literals and const references from any package.
var webhookSignatureHeaders = map[string]bool{
	"webhook-id":        true,
	"webhook-timestamp": true,
	"webhook-signature": true,
}

// isNetHTTPHeaderSetSelector reports whether sel resolves to the
// (net/http.Header).Set method — type-resolved (alias-safe), confirming both the
// method (Set in net/http) and the receiver type (net/http.Header). Shared by the
// direct-call scan (A1) and the method-value-capture scan (A1, review #1733 F2).
func isNetHTTPHeaderSetSelector(info *types.Info, sel *ast.SelectorExpr) bool {
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil || fn.Pkg() == nil ||
		fn.Pkg().Path() != httpPkgPath || fn.Name() != "Set" {
		return false
	}
	recvType := info.TypeOf(sel.X)
	if recvType == nil {
		return false
	}
	if ptr, ok := recvType.(*types.Pointer); ok {
		recvType = ptr.Elem()
	}
	named, ok := recvType.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj() != nil && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == httpPkgPath && named.Obj().Name() == "Header"
}

// scanSignerHeaderSet implements A1 (direct call): (net/http.Header).Set calls
// whose key evaluates to one of the three signature-header names must be enclosed
// by signerSanctionedApplyFunc. Any other enclosing function (including the
// package-level init or a FuncLit) is a violation.
func scanSignerHeaderSet(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isNetHTTPHeaderSetSelector(info, sel) {
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
				"(SignedHeaders).Apply; signature headers must only be written by " +
				"SignedHeaders.Apply (WEBHOOK-SIGNER-FUNNEL-01/A1)",
		})
	})
	return out
}

// scanSignerHeaderSetMethodValue implements A1's method-value closure (review
// #1733 F2): (net/http.Header).Set captured as a METHOD VALUE rather than called
// directly — e.g. `set := req.Header.Set; set("webhook-signature", v)` — bypasses
// the direct-call key check, because the eventual key is not visible at the capture
// site. There is no legitimate reason to capture Header.Set in kernel/webhook or
// runtime/webhook (the sole signature-header writer is the direct calls inside
// (SignedHeaders).Apply), so ANY such capture is a violation. Detection: a
// SelectorExpr resolving to (net/http.Header).Set that is NOT a direct call callee.
func scanSignerHeaderSetMethodValue(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	// SelectorExprs that ARE direct call callees are handled by scanSignerHeaderSet.
	directCallee := map[*ast.SelectorExpr]bool{}
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			directCallee[sel] = true
		}
	})
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if directCallee[sel] || !isNetHTTPHeaderSetSelector(info, sel) {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(sel.Pos()).Line,
			Message: "net/http.Header.Set captured as a method value (not a direct call); " +
				"signature-header writes must be direct (SignedHeaders).Apply calls so the key " +
				"is verifiable (WEBHOOK-SIGNER-FUNNEL-01/A1)",
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

	_ = Run(t, Typed(TypedOpts{Tests: false}, webhookSignerPkgPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			// Scope: the kernel/webhook and runtime/webhook production trees,
			// INCLUDING subpackages. Exact-match previously excluded
			// runtime/webhook/dispatch — the ./runtime/webhook/... pattern loads
			// it, but the filter dropped it — so a bare Header.Set of a signature
			// header in the dispatcher consumer layer (where SignedHeaders.Apply is
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
				a1 = append(a1, scanSignerHeaderSetMethodValue(p.Fset, f, rel, p.TypesInfo)...)
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
//   - raw string literal key "webhook-signature" (direct call)
//   - const key whose value is "webhook-signature" (direct call)
//   - const key whose value is "webhook-id" (direct call)
//   - const key whose value is "webhook-timestamp" (direct call)
//   - a (net/http.Header).Set method-value capture `set := h.Set` (#1733 F2)
//
// so that dropping any one of the three header names from webhookSignatureHeaders,
// or the method-value branch, causes this test to fail.
func TestWebhookSignerFunnel_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "webhook_signer_violate")

	var a1 []Diagnostic
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
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
				a1 = append(a1, scanSignerHeaderSetMethodValue(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	// The fixture has exactly 5 violations:
	//   - 1 raw string literal key "webhook-signature" (direct call, scanSignerHeaderSet)
	//   - 3 const-keyed direct calls, one per header name (webhook-id / webhook-timestamp
	//     / webhook-signature)
	//   - 1 method-value capture `set := h.Set` (scanSignerHeaderSetMethodValue, #1733 F2)
	// Asserting ≥5 ensures that dropping ANY single fixture violation (e.g. removing
	// one const form, one header name, or the method-value branch) causes this
	// self-test to fail immediately, rather than passing on the remaining ones.
	const webhookSignerFixtureMinViolations = 5
	assert.GreaterOrEqual(t, len(a1), webhookSignerFixtureMinViolations,
		"A1 reverse fixture: expected ≥5 diagnostics (1 raw literal + 3 const refs + "+
			"1 method-value capture)")
}

// ---- A2: SignedHeaders sealed-construction reflect freeze (#1492) ----
//
// "reflect schema freeze" Hard 范本 (ai-robust.md): reflect.Type.NumField /
// StructField.PkgPath / StructField.Type are objective structural facts, so any
// drift (export a field / rename / reorder / add / remove) trips a tuple mismatch
// and forces the change onto this explicit checkpoint. The upstream-Hard-external
// guarantee of WEBHOOK-SIGNER-FUNNEL-01 rests on SignedHeaders' fields staying
// UNEXPORTED — an exported field would re-open `webhook.SignedHeaders{signature:…}`
// outside-package literal forge (the very gap #1492 closed).

// frozenSignedHeadersField is the expected frozen shape of one SignedHeaders field.
type frozenSignedHeadersField struct {
	name     string
	typeName string // reflect.Type.String()
	exported bool   // PkgPath == "" → exported
}

// frozenSignedHeadersFields is the expected exact field tuple for
// webhook.SignedHeaders (in Go struct declaration order). All MUST be unexported.
// `valid` is the provenance flag (#1733 F1): only Signer.Sign sets it, and Apply
// fail-closes on a zero value, so an external zero-value SignedHeaders cannot
// write to the wire. It MUST stay unexported (an exported `Valid` would let an
// external caller forge a valid value).
var frozenSignedHeadersFields = []frozenSignedHeadersField{
	{name: "deliveryID", typeName: "webhook.DeliveryID", exported: false},
	{name: "timestamp", typeName: "string", exported: false},
	{name: "signature", typeName: "string", exported: false},
	{name: "valid", typeName: "bool", exported: false},
}

// TestWebhookSignerFunnel_SignedHeadersSealedFields is the A2 primary guard: it
// freezes the field set of webhook.SignedHeaders and asserts every field is
// unexported (sealed construction; outside-package literal forge = compile error).
func TestWebhookSignerFunnel_SignedHeadersSealedFields(t *testing.T) {
	t.Parallel()

	st := reflect.TypeOf(webhook.SignedHeaders{})
	require.Equal(t, reflect.Struct, st.Kind(), "webhook.SignedHeaders must be a struct")

	// Axis 1: exact field count.
	require.Equal(t, len(frozenSignedHeadersFields), st.NumField(),
		"WEBHOOK-SIGNER-FUNNEL-01/A2: SignedHeaders NumField = %d, want %d "+
			"(adding a field may re-open the sealed-construction forge vector; removing one "+
			"breaks the Apply contract; update frozenSignedHeadersFields + webhook.go together)",
		st.NumField(), len(frozenSignedHeadersFields))

	for i, want := range frozenSignedHeadersFields {
		sf := st.Field(i)
		assert.Equal(t, want.name, sf.Name,
			"WEBHOOK-SIGNER-FUNNEL-01/A2: SignedHeaders field[%d] name = %q, want %q", i, sf.Name, want.name)
		assert.Equal(t, want.typeName, sf.Type.String(),
			"WEBHOOK-SIGNER-FUNNEL-01/A2: SignedHeaders.%s type = %q, want %q", sf.Name, sf.Type.String(), want.typeName)
		exported := sf.PkgPath == ""
		assert.Equal(t, want.exported, exported,
			"WEBHOOK-SIGNER-FUNNEL-01/A2: SignedHeaders.%s exported = %v, want %v "+
				"(an exported field allows `webhook.SignedHeaders{%s:…}` outside kernel/webhook — "+
				"re-opening the forge vector #1492 closed; re-seal by lowercasing)",
			sf.Name, exported, want.exported, sf.Name)
	}
}

// TestWebhookSignerFunnel_SignedHeadersAntiVacuity guards against the freeze
// trivially passing on a wrong/empty type load.
func TestWebhookSignerFunnel_SignedHeadersAntiVacuity(t *testing.T) {
	t.Parallel()

	st := reflect.TypeOf(webhook.SignedHeaders{})
	assert.Equal(t, "SignedHeaders", st.Name(),
		"A2 anti-vacuity: loaded type name = %q, want SignedHeaders", st.Name())
	assert.True(t, strings.HasSuffix(st.PkgPath(), "kernel/webhook"),
		"A2 anti-vacuity: package path %q must end with kernel/webhook", st.PkgPath())
	assert.Greater(t, st.NumField(), 0, "A2 anti-vacuity: SignedHeaders has no fields — wrong type loaded?")
}

// TestWebhookSignerFunnel_SignedHeadersRedFixture is the reverse self-check: a
// look-alike struct with an EXPORTED field must be detected by the same
// PkgPath=="" visibility check, proving the freeze is not vacuous.
func TestWebhookSignerFunnel_SignedHeadersRedFixture(t *testing.T) {
	t.Parallel()

	type brokenSignedHeaders struct {
		Signature  string             // EXPORTED — must be detected as a violation
		deliveryID webhook.DeliveryID //nolint:unused // mimics the unexported shape
		timestamp  string             //nolint:unused // mimics the unexported shape
	}
	bt := reflect.TypeOf(brokenSignedHeaders{})
	exportedDetected := bt.Field(0).PkgPath == ""
	assert.True(t, exportedDetected,
		"RED fixture self-check: exported Signature field must be detected (PkgPath empty); "+
			"if this fails the A2 visibility check would miss real violations")
}
