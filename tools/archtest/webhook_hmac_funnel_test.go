// INVARIANT: WEBHOOK-HMAC-FUNNEL-01
//
// WEBHOOK-HMAC-FUNNEL-01 — kernel/webhook HMAC signing funnel (KERNEL-WEBHOOK-01).
//
//   - A1 (downstream Hard): crypto/hmac.New has exactly one callsite in the
//     kernel/webhook package — computeMAC in signer.go. Any other hmac.New call
//     in the package fails. This also closes the package-internal upstream blind
//     spot: any new struct that wants to sign MUST call hmac.New, which is
//     allowlisted to signer.go, so an unsealed internal holder cannot produce a
//     signature undetected. Detection: ResolvePackageRef(callee) == crypto/hmac.New
//     AND enclosing file != signer.go.
//   - A2 (downstream Hard): signature comparison must use crypto/hmac.Equal or
//     crypto/subtle.ConstantTimeCompare. bytes.Equal is banned in the package
//     (it is not constant-time). This makes the constant-time invariant an AST
//     lock rather than a flaky timing test. Detection:
//     ResolvePackageRef(callee) == bytes.Equal in the package.
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
//	  (testdata/webhook_hmac_violate) calls hmac.New outside signer.go and
//	  bytes.Equal, and TestWebhookHMACFunnel_ReverseFixture asserts A1/A2 fire.
//	B6 — Source.Secret leak: slog of the raw unexported secret field would leak
//	  it (Source.LogValue + slog.LogValuer covers slog.Any of a whole Source, but
//	  not slog of src.secret directly). TestWebhookFunnel_NoRawSecretSlog asserts
//	  no slog.* call in the package references a `.secret` selector.
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
	webhookPkgPattern   = "./kernel/webhook/..."
	webhookSignerSuffix = "kernel/webhook/signer.go"
	hmacPkgPath         = "crypto/hmac"
	slogPkgPath         = "log/slog"
)

// webhookSealedInterfaces are the interface type names in kernel/webhook that
// must carry an unexported sealed() marker (A3).
var webhookSealedInterfaces = map[string]bool{"Signer": true, "Verifier": true}

// scanWebhookHMACNew implements A1: crypto/hmac.New callsites must live in
// signer.go.
func scanWebhookHMACNew(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	allowed := strings.HasSuffix(filepath.ToSlash(rel), webhookSignerSuffix)
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != hmacPkgPath || name != "New" {
			return
		}
		if allowed {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "crypto/hmac.New called outside kernel/webhook/signer.go; " +
				"all HMAC computation must funnel through computeMAC (WEBHOOK-HMAC-FUNNEL-01/A1)",
		})
	})
	return out
}

// scanWebhookBytesEqual implements A2: bytes.Equal is banned in the package
// (signature comparison must use hmac.Equal / subtle.ConstantTimeCompare).
func scanWebhookBytesEqual(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != "bytes" || name != "Equal" {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "bytes.Equal called in kernel/webhook; signature comparison must use " +
				"crypto/hmac.Equal or crypto/subtle.ConstantTimeCompare (WEBHOOK-HMAC-FUNNEL-01/A2)",
		})
	})
	return out
}

// scanWebhookSecretSlog implements B6: no slog.* call may pass a `.secret`
// selector (the raw unexported secret field).
func scanWebhookSecretSlog(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, _, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != slogPkgPath {
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

	assert.NotEmpty(t, a1, "A1 reverse fixture: expected ≥1 diagnostic for hmac.New outside signer.go")
	assert.NotEmpty(t, a2, "A2 reverse fixture: expected ≥1 diagnostic for bytes.Equal")
	assert.NotEmpty(t, b6, "B6 reverse fixture: expected ≥1 diagnostic for slog of .secret")
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
