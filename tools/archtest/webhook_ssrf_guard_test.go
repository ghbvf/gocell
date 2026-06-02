// INVARIANT: WEBHOOK-SSRF-GUARD-01
//
// WEBHOOK-SSRF-GUARD-01 — outbound network funnel for kernel/webhook and
// runtime/webhook/dispatch (KERNEL-WEBHOOK-01).
//
// PR-4 shipped the pure SSRF building block: SafePolicy, the single-source
// policy whose DialContext / ValidateTargetURL / DenyRedirect methods share one
// config. PR-5 delivers the Dispatcher (kernel/webhook) and the dispatch
// consumer wiring (runtime/webhook/dispatch). A4 locks the Transport
// composite-literal form in the dispatcher — the dispatcher-specific
// single-sanctioned-holder Hard that complements the package-level bans.
//
// Scan scope (PR-5 extension): both kernel/webhook and runtime/webhook/dispatch
// production files are scanned. runtime/webhook/dispatch only wires kernel types
// (no raw dials, no global clients, no Transport literals) so A1–A4 are
// vacuously satisfied there today; the scan extension ensures that future
// additions to the package cannot silently bypass the SSRF funnel.
//
//   - A1 (downstream Hard): the net package dial-family FUNCTIONS
//     (net.Dial / DialTCP / DialUDP / DialIP / DialUnix / DialTimeout) are
//     banned in kernel/webhook and runtime/webhook/dispatch production code.
//     Detection: ResolvePackageRef(callee) == ("net", <dial-func>).
//   - A2 (downstream Hard): the net.Dialer.DialContext METHOD may be called
//     ONLY from inside (*SafePolicy).DialContext — the sanctioned vetted dialer
//     (which legitimately has two callsites: the IP-literal and the
//     resolved-hostname dial). The allowance binds to the go/types FullName
//     identity of the enclosing func, not its name or file, so a rogue func
//     that merely shares the name cannot inherit it and the method may move
//     files freely. A raw net.Dialer{}.DialContext anywhere else bypasses the
//     IP vet and fails. Detection: ResolveMethodCall(sel) == net.DialContext
//     AND enclosing-func FullName != (*SafePolicy).DialContext.
//   - A3 (downstream Hard): the global HTTP client/transport in net/http —
//     http.DefaultClient / http.DefaultTransport (package vars) and the
//     convenience callees http.Get / Post / PostForm / Head — are banned in
//     kernel/webhook and runtime/webhook/dispatch; they bypass the SSRF-wrapped
//     transport. Detection: ResolvePackageRef → ("net/http", <global|callee>).
//   - A4 (downstream Hard): any &http.Transport{} composite literal in
//     kernel/webhook or runtime/webhook/dispatch production code MUST set a
//     DialContext field (non-absent). A Transport without DialContext silently
//     falls back to the net default dialer, bypassing SafePolicy. Detection:
//     EachInSubtree[CompositeLit], resolve lit type via TypesInfo.Types[cl.Type],
//     check for "DialContext" key in the literal's Elts. Only fires on named
//     literals whose resolved type is net/http.Transport (not on un-typed /
//     other-package literals).
//
// AI-robust rating (Funnel 双向锁评级, per .claude/rules/gocell/ai-robust.md):
//
//	下游 Hard — A1/A2/A3/A4 are form-unique, type-resolved checks
//	  (ResolvePackageRef / ResolveMethodCall resolve import aliases and
//	  value-refs; A2 binds the allowance to a go/types FullName method identity,
//	  not a name or filename; A4 uses TypesInfo.Types to confirm the literal type
//	  is net/http.Transport — there is no "looks-like" grey zone).
//	上游 Medium = Go 永久天花板 — the holder axis ("only a *SafePolicy a
//	  dispatcher holds may produce egress") is inexpressible in Go's type
//	  system; package visibility only constrains implementers, not who may
//	  declare a field of a type. This is the same permanent ceiling as
//	  SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL (#893). Tracked
//	  won't-do: gh #1375.
//
// Blind spots (ai-robust 强制反向自检; reverse fixture below):
//
//	B-A1/A2/A3/A4 — rule-logic regression: testdata/webhook_ssrf_violate
//	  exercises net.Dial + net.DialTCP (A1), a raw net.Dialer{}.DialContext
//	  (A2), http.DefaultClient.Do + http.Get + http.DefaultTransport (A3), and
//	  &http.Transport{} with no DialContext (A4);
//	  TestWebhookSSRFGuard_ReverseFixture asserts each sub-rule fires.
//	B1 — http.Transport.RoundTrip direct call: a hand-rolled Transport whose
//	  DialContext is NOT the SafeDialContext, invoked via RoundTrip, bypasses
//	  the funnel. RoundTrip is a method with no resolvable package-callee form
//	  that distinguishes a safe vs unsafe transport; catching it needs
//	  type-level dataflow we do not have. Bounded response: the realistic
//	  egress path is http.Client.Do over the SSRF transport, locked by A4
//	  (the Transport must declare DialContext); documented here so a reviewer
//	  knows the AST scan does not cover raw Transport.RoundTrip.
//	B4-A4 — unnamed/inferred Transport type: if the composite literal appears
//	  in a context where its type is inferred (no explicit type annotation,
//	  e.g. assigned to a var of type http.RoundTripper), TypesInfo.Types[cl.Type]
//	  returns false (cl.Type is nil). The scan only fires when cl.Type is
//	  explicitly present. Bounded response: NewDispatcher always writes
//	  &http.Transport{...} with an explicit type in production (checked by the
//	  reverse fixture); a type-inferred literal assigned to an interface would
//	  still fail A3 (non-DefaultTransport) if it ever made an egress call.
//
// ref: docs/architecture/202605312300-1159-adr-webhook-ssrf-policy.md
// ref: tools/archtest/webhook_hmac_funnel_test.go (callsite-allowlist template)
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
	// dialContextFieldName is the struct field name in net/http.Transport that
	// wires a custom dialer. A Transport literal that omits this field falls back
	// to the net default dialer, silently bypassing SafePolicy (A4).
	dialContextFieldName = "DialContext"

	httpTransportTypeName = "Transport"
)

const (
	ssrfNetPkgPath  = "net"
	ssrfHTTPPkgPath = "net/http"
	// ssrfSanctionedDialFunc is the go/types canonical FullName of the ONE
	// method allowed to call net.Dialer.DialContext: (*SafePolicy).DialContext.
	// Binding the A2 allowance to this type-resolved identity (not a func name
	// or a filename) means a stray func that merely shares the name "dial" /
	// "DialContext" elsewhere cannot inherit the allowance, and the sanctioned
	// method can move files freely. A rename of the method flips every
	// DialContext callsite to a violation (fail-closed), forcing the author to
	// update this const in the same change.
	ssrfSanctionedDialFunc = "(*" + PlatformModulePath + "/kernel/webhook.SafePolicy).DialContext"
)

// ssrfBannedNetDialFuncs is the A1 banned set: net package functions that open
// a connection without the SSRF vet. The sanctioned path is the
// net.Dialer.DialContext METHOD inside ssrf.go (A2), not any of these.
var ssrfBannedNetDialFuncs = map[string]bool{
	"Dial":        true,
	"DialTCP":     true,
	"DialUDP":     true,
	"DialIP":      true,
	"DialUnix":    true,
	"DialTimeout": true,
}

// ssrfBannedHTTPGlobals is the A3 banned package-var set (global client /
// transport that skips the SSRF-wrapped transport).
var ssrfBannedHTTPGlobals = map[string]bool{
	"DefaultClient":    true,
	"DefaultTransport": true,
}

// ssrfBannedHTTPCallees is the A3 banned convenience-function set.
var ssrfBannedHTTPCallees = map[string]bool{
	"Get":      true,
	"Post":     true,
	"PostForm": true,
	"Head":     true,
}

// wantSSRF*Violations are INDEPENDENT expected floors for the reverse fixture
// (F12). Deriving the expected count from len(ssrfBanned*) was vacuous: removing
// a banned entry (weakening the rule) also shrank the expected count, so the
// self-test still passed. These hardcoded floors make a dropped banned form fail
// the GreaterOrEqual assertions; a paired Equal golden-lock (in the reverse-
// fixture test) forces a conscious bump here whenever the banned set changes.
const (
	wantSSRFNetDialViolations = 6 // Dial/DialTCP/DialUDP/DialIP/DialUnix/DialTimeout
	wantSSRFHTTPGlobalCallees = 6 // DefaultClient/DefaultTransport + Get/Post/PostForm/Head
)

// scanSSRFNetDial implements A1: net.Dial* package functions are banned.
func scanSSRFNetDial(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != ssrfNetPkgPath || !ssrfBannedNetDialFuncs[name] {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "net." + name + " called in kernel/webhook; outbound dial must funnel through " +
				"NewSafeDialer / SafeDialContext (WEBHOOK-SSRF-GUARD-01/A1)",
		})
	})
	return out
}

// scanSSRFDialerDialContext implements A2: net.Dialer.DialContext may be called
// only from inside the (*SafePolicy).DialContext method (callsite identity,
// resolved via go/types FullName — file- and name-independent).
func scanSSRFDialerDialContext(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		fn, ok := ResolveMethodCall(info, sel)
		if !ok || fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != ssrfNetPkgPath || fn.Name() != "DialContext" {
			return
		}
		if enc, ok := ResolveEnclosingFunc(info, file, call); ok && enc != nil && enc.FullName() == ssrfSanctionedDialFunc {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "net.Dialer.DialContext called outside (*SafePolicy).DialContext; the vetted dialer is " +
				"the sole sanctioned outbound callsite (WEBHOOK-SSRF-GUARD-01/A2)",
		})
	})
	return out
}

// scanSSRFHTTPGlobal implements A3: net/http global client/transport vars and
// convenience funcs are banned in kernel/webhook.
func scanSSRFHTTPGlobal(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, sel)
		if !ok || pkgPath != ssrfHTTPPkgPath || !ssrfBannedHTTPGlobals[name] {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(sel.Pos()).Line,
			Message: "http." + name + " referenced in kernel/webhook; use an SSRF-wrapped *http.Client " +
				"(WEBHOOK-SSRF-GUARD-01/A3)",
		})
	})
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != ssrfHTTPPkgPath || !ssrfBannedHTTPCallees[name] {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "http." + name + " called in kernel/webhook; use an SSRF-wrapped *http.Client " +
				"(WEBHOOK-SSRF-GUARD-01/A3)",
		})
	})
	return out
}

// scanSSRFTransportDialContext implements A4: any &http.Transport{} composite
// literal in kernel/webhook production code must include a DialContext field.
// A Transport without DialContext silently falls back to the net default dialer,
// bypassing the SafePolicy SSRF vet. Detection uses TypesInfo.Types[cl.Type] to
// resolve the literal's type to net/http.Transport (alias-safe), then checks
// whether any KeyValueExpr in the literal's Elts has the key "DialContext".
//
// Blind spot B4-A4: if the literal has no explicit type annotation (cl.Type is
// nil, type is inferred from context), TypesInfo.Types[cl.Type] is not
// available and the scan skips it. See file-header B4-A4 godoc.
func scanSSRFTransportDialContext(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		if cl.Type == nil {
			return // inferred-type literal — B4-A4 blind spot, skip
		}
		tv, ok := info.Types[cl.Type]
		if !ok {
			return
		}
		// Unwrap pointer: &http.Transport{} has type *http.Transport in context,
		// but the literal's own type expression is http.Transport (no pointer).
		t := tv.Type
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		named, ok := t.(*types.Named)
		if !ok {
			return
		}
		obj := named.Obj()
		if obj == nil || obj.Pkg() == nil {
			return
		}
		if obj.Pkg().Path() != ssrfHTTPPkgPath || obj.Name() != httpTransportTypeName {
			return
		}
		// Confirmed: literal is net/http.Transport. Check for DialContext key.
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if ok && key.Name == dialContextFieldName {
				return // DialContext is set — compliant
			}
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(cl.Pos()).Line,
			Message: "&http.Transport{} literal in kernel/webhook is missing a DialContext field; " +
				"omitting DialContext falls back to the net default dialer, bypassing SafePolicy " +
				"(WEBHOOK-SSRF-GUARD-01/A4)",
		})
	})
	return out
}

// ssrfScannedPkgPaths is the set of production package paths covered by the
// WEBHOOK-SSRF-GUARD-01 scan. Using a set (not a string prefix) keeps the scope
// explicit: only packages that actually handle outbound webhook egress or directly
// wrap kernel/webhook types are included.
//
//   - kernel/webhook: SafePolicy + Dispatcher (all four sub-rules apply)
//   - runtime/webhook/dispatch: wires kernel types only (no raw dials today;
//     extended so future additions cannot silently bypass the funnel)
var ssrfScannedPkgPaths = map[string]bool{
	PlatformModulePath + "/kernel/webhook":           true,
	PlatformModulePath + "/runtime/webhook/dispatch": true,
}

// ssrfPkgPatterns is the packages.Load pattern list for TestWebhookSSRFGuard.
// It covers kernel/webhook and runtime/webhook/dispatch (PR-5 extension).
var ssrfPkgPatterns = []string{
	"./kernel/webhook/...",
	"./runtime/webhook/dispatch",
}

func TestWebhookSSRFGuard(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var a1, a2, a3, a4 []Diagnostic
	_ = RunTyped(t, TypedOpts{Tests: false}, ssrfPkgPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || !ssrfScannedPkgPaths[p.Pkg.Path()] {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				a1 = append(a1, scanSSRFNetDial(p.Fset, f, rel, p.TypesInfo)...)
				a2 = append(a2, scanSSRFDialerDialContext(p.Fset, f, rel, p.TypesInfo)...)
				a3 = append(a3, scanSSRFHTTPGlobal(p.Fset, f, rel, p.TypesInfo)...)
				a4 = append(a4, scanSSRFTransportDialContext(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	Report(t, "WEBHOOK-SSRF-GUARD-01/A1", a1)
	Report(t, "WEBHOOK-SSRF-GUARD-01/A2", a2)
	Report(t, "WEBHOOK-SSRF-GUARD-01/A3", a3)
	Report(t, "WEBHOOK-SSRF-GUARD-01/A4", a4)
}

// TestWebhookSSRFGuard_ReverseFixture loads the synthetic violation fixture and
// asserts each sub-rule fires — guards against the rule logic silently
// regressing to a vacuous pass.
func TestWebhookSSRFGuard_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "webhook_ssrf_violate")

	var a1, a2, a3, a4 []Diagnostic
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
				a1 = append(a1, scanSSRFNetDial(p.Fset, f, rel, p.TypesInfo)...)
				a2 = append(a2, scanSSRFDialerDialContext(p.Fset, f, rel, p.TypesInfo)...)
				a3 = append(a3, scanSSRFHTTPGlobal(p.Fset, f, rel, p.TypesInfo)...)
				a4 = append(a4, scanSSRFTransportDialContext(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	// Golden-lock: the fixture-violation floors are independent consts (F12), not
	// len(ssrfBanned*). These equalities force a conscious const bump (and a paired
	// fixture update) whenever the banned set changes; the GreaterOrEqual checks
	// below then fail if the rule stops catching a banned form on the fixture.
	assert.Equal(t, wantSSRFNetDialViolations, len(ssrfBannedNetDialFuncs),
		"banned net.Dial* set changed — bump wantSSRFNetDialViolations and the reverse fixture")
	assert.Equal(t, wantSSRFHTTPGlobalCallees, len(ssrfBannedHTTPGlobals)+len(ssrfBannedHTTPCallees),
		"banned http global/callee set changed — bump wantSSRFHTTPGlobalCallees and the reverse fixture")

	assert.GreaterOrEqual(t, len(a1), wantSSRFNetDialViolations,
		"A1 reverse fixture: expected one diagnostic per banned net.Dial* func (Dial/DialTCP/DialUDP/DialIP/DialUnix/DialTimeout)")
	assert.GreaterOrEqual(t, len(a2), 1, "A2 reverse fixture: expected ≥1 raw net.Dialer.DialContext diagnostic")
	// Cover the FULL A3 banned set — both globals (DefaultClient/DefaultTransport)
	// AND every convenience callee (Get/Post/PostForm/Head) — so dropping any one
	// banned entry fails this self-test instead of passing on the others.
	assert.GreaterOrEqual(t, len(a3), wantSSRFHTTPGlobalCallees,
		"A3 reverse fixture: expected one diagnostic per banned http global + convenience func")
	assert.GreaterOrEqual(t, len(a4), 1,
		"A4 reverse fixture: expected ≥1 diagnostic for &http.Transport{} with no DialContext field")
}
