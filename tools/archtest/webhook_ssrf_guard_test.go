// INVARIANT: WEBHOOK-SSRF-GUARD-01
//
// WEBHOOK-SSRF-GUARD-01 — kernel/webhook outbound network funnel (KERNEL-WEBHOOK-01).
//
// PR-4 ships the pure SSRF building block (SafeDialContext). The dispatcher
// that holds the SSRF-wrapped *http.Client is PR-5; the Dispatcher.client
// typed-field upstream lock is therefore deferred to PR-5 (the struct does not
// exist yet — a field scan now would pass vacuously). This invariant locks the
// package-level callsite bans that ARE meaningful for a pure building block:
// any outbound network primitive in kernel/webhook must funnel through the
// vetted dialer, so PR-5 cannot wire an un-vetted egress path.
//
//   - A1 (downstream Hard): the net package dial-family FUNCTIONS
//     (net.Dial / DialTCP / DialUDP / DialIP / DialUnix / DialTimeout) are
//     banned anywhere in kernel/webhook production code. Detection:
//     ResolvePackageRef(callee) == ("net", <dial-func>).
//   - A2 (downstream Hard): the net.Dialer.DialContext METHOD has exactly one
//     callsite — the dial function in ssrf.go (the sanctioned vetted dialer).
//     A DialContext on any net.Dialer anywhere else fails. This is the
//     callsite-uniqueness lock analogous to computeMAC/hmac.New: a rogue raw
//     net.Dialer{}.DialContext would bypass the IP vet. Detection:
//     ResolveMethodCall(sel) == net.DialContext AND (basename != ssrf.go OR
//     enclosing func != dial).
//   - A3 (downstream Hard): the global HTTP client/transport in net/http —
//     http.DefaultClient / http.DefaultTransport (package vars) and the
//     convenience callees http.Get / Post / PostForm / Head — are banned in
//     kernel/webhook; they bypass the SSRF-wrapped transport. Detection:
//     ResolvePackageRef → ("net/http", <global|callee>).
//
// AI-robust rating (Funnel 双向锁评级, per .claude/rules/gocell/ai-robust.md):
//
//	下游 Hard — A1/A2/A3 are form-unique, type-resolved callsite bans
//	  (ResolvePackageRef / ResolveMethodCall resolve import aliases and
//	  value-refs; there is no "looks-like" grey zone).
//	上游 Medium = Go 永久天花板 — the holder axis ("only NewSafeDialer may
//	  produce a dialer that a struct holds") is inexpressible in Go's type
//	  system; package visibility only constrains implementers, not who may
//	  declare a field of a type. This is the same permanent ceiling as
//	  SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL (#893). Tracked
//	  won't-do: gh #1431. The PR-5 Dispatcher.client typed-field lock is the
//	  dispatcher-specific single-sanctioned-holder Hard that complements this
//	  package-level ban.
//
// Blind spots (ai-robust 强制反向自检; reverse fixture below):
//
//	B-A1/A2/A3 — rule-logic regression: testdata/webhook_ssrf_violate exercises
//	  net.Dial + net.DialTCP (A1), a raw net.Dialer{}.DialContext (A2), and
//	  http.DefaultClient.Do + http.Get + http.DefaultTransport (A3);
//	  TestWebhookSSRFGuard_ReverseFixture asserts each sub-rule fires.
//	B1 — http.Transport.RoundTrip direct call: a hand-rolled Transport whose
//	  DialContext is NOT the SafeDialContext, invoked via RoundTrip, bypasses
//	  the funnel. RoundTrip is a method with no resolvable package-callee form
//	  that distinguishes a safe vs unsafe transport; catching it needs
//	  type-level dataflow we do not have. Bounded response: the realistic
//	  egress path is http.Client.Do over the SSRF transport, locked when the
//	  dispatcher lands in PR-5; documented here so a reviewer knows the AST scan
//	  does not cover raw Transport.RoundTrip.
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
	"github.com/stretchr/testify/require"
)

const (
	ssrfFileBasename = "ssrf.go"
	ssrfDialFuncName = "dial"
	ssrfNetPkgPath   = "net"
	ssrfHTTPPkgPath  = "net/http"
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
// only inside the dial function of ssrf.go (callsite uniqueness).
func scanSSRFDialerDialContext(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	inSSRFFile := filepath.Base(filepath.ToSlash(rel)) == ssrfFileBasename
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		fn, ok := ResolveMethodCall(info, sel)
		if !ok || fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != ssrfNetPkgPath || fn.Name() != "DialContext" {
			return
		}
		if inSSRFFile {
			if enc, ok := ResolveEnclosingFunc(info, file, call); ok && enc != nil && enc.Name() == ssrfDialFuncName {
				return
			}
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "net.Dialer.DialContext called outside ssrf.go dial(); the vetted dialer is the " +
				"sole sanctioned outbound callsite (WEBHOOK-SSRF-GUARD-01/A2)",
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

func TestWebhookSSRFGuard(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var a1, a2, a3 []Diagnostic
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
				a1 = append(a1, scanSSRFNetDial(p.Fset, f, rel, p.TypesInfo)...)
				a2 = append(a2, scanSSRFDialerDialContext(p.Fset, f, rel, p.TypesInfo)...)
				a3 = append(a3, scanSSRFHTTPGlobal(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	Report(t, "WEBHOOK-SSRF-GUARD-01/A1", a1)
	Report(t, "WEBHOOK-SSRF-GUARD-01/A2", a2)
	Report(t, "WEBHOOK-SSRF-GUARD-01/A3", a3)
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

	var a1, a2, a3 []Diagnostic
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
			}
			return nil
		})

	assert.GreaterOrEqual(t, len(a1), 2, "A1 reverse fixture: expected ≥2 net.Dial* diagnostics (net.Dial + net.DialTCP)")
	assert.GreaterOrEqual(t, len(a2), 1, "A2 reverse fixture: expected ≥1 raw net.Dialer.DialContext diagnostic")
	assert.GreaterOrEqual(t, len(a3), 2, "A3 reverse fixture: expected ≥2 http global diagnostics")
}
