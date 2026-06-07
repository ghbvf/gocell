// INVARIANT: WEBHOOK-HMAC-FUNNEL-01
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
//     crypto/hmac.New, not reuse of the package-level computeMAC helper. That
//     internal axis is covered by A4 (computeMAC caller-allowlist, Medium). True
//     type-system Hard for the internal axis is a PERMANENT Go ceiling (in-package
//     code can always declare sealed(), read Source.secret, and call computeMAC) —
//     same family as #851 / #893 / #1282 / #1375; #1243 is relabeled won't-do and
//     named here as that ceiling's tracker. Detection: the Signer/Verifier
//     interface type decls must contain an unexported method.
//   - A4 (Medium, internal axis; #1243): every USE of the package-internal
//     computeMAC symbol — direct call, parenthesized call, OR function-value
//     capture (`macFn := computeMAC`; bypass review #1733 F4) — must have an
//     enclosing func whose go/types FullName ∈ {hmacSigner.Sign,
//     hmacVerifier.Verify}. This is the actual enforcement of "only the sanctioned
//     signer/verifier may compute a MAC", closing the A3 internal axis at Medium.
//     Detection: walk every ident, match info.Uses FullName == computeMAC's,
//     require enclosing FullName ∈ allowlist (definition is in info.Defs).
//
// Blind spots (ai-robust 强制反向自检; each has a reverse self-test below):
//
//	B-A1/A2 — rule-logic regression: a reverse fixture module
//	  (testdata/webhook_hmac_violate) calls hmac.New outside computeMAC (both
//	  outside signer.go and inside signer.go in a non-computeMAC func) and the
//	  banned comparison callees; TestWebhookHMACFunnel_ReverseFixture asserts A1/A2
//	  fire on each form.
//	B-A4 — rule-logic regression (computeMAC is unexported, so no standalone-module
//	  RED fixture can call it): TestWebhookHMACFunnel_ComputeMACCallerAntiVacuity
//	  re-runs the A4 scan over real production with an EMPTY allowlist and asserts it
//	  fires on the ≥2 real computeMAC callers (Sign + Verify).
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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookHMACFunnel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	Report(t, "WEBHOOK-HMAC-FUNNEL-01", CheckWebhookHMACFunnel(t, ConfigForExternalCell{}))
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
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{webhookPkgPattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != webhookPkgPath {
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

// TestWebhookHMACFunnel_SealedMarkerDiagnosticsLocated is the F1 reverse
// self-check for the A3 sealed-marker branches. The GREEN dogfood never reaches
// them (production declares both sealed interfaces with markers), so a
// regression dropping Diagnostic.Rel/Line — degrading Report to ":0:" — would
// pass CI undetected. It drives checkWebhookSealedMarkers with both violating
// inputs and asserts every emitted Diagnostic is clickable.
func TestWebhookHMACFunnel_SealedMarkerDiagnosticsLocated(t *testing.T) {
	t.Parallel()

	assertLocated := func(name string, diags []Diagnostic) {
		if len(diags) == 0 {
			t.Errorf("%s: expected ≥1 diagnostic, got none (vacuous)", name)
		}
		for i, d := range diags {
			if d.Rel == "" {
				t.Errorf("%s[%d]: empty Rel (Diagnostic must be clickable, not \":0:\")", name, i)
			}
			if d.Line <= 0 {
				t.Errorf("%s[%d]: Line = %d, want > 0", name, i, d.Line)
			}
		}
	}

	// "interface not found" → anchored to the package-anchor rel.
	assertLocated("notFound",
		checkWebhookSealedMarkers(map[string]webhookSealInfo{}, "kernel/webhook/signer.go"))

	// "interface present but missing the sealed() marker" → anchored to its decl.
	assertLocated("missingMarker", checkWebhookSealedMarkers(map[string]webhookSealInfo{
		"Signer":   {hasUnexported: false, rel: "kernel/webhook/signer.go", line: 12},
		"Verifier": {hasUnexported: false, rel: "kernel/webhook/verifier.go", line: 8},
	}, "kernel/webhook/signer.go"))
}

// TestWebhookHMACFunnel_ComputeMACCallerAntiVacuity is the B-A4 self-check.
// computeMAC is unexported, so a standalone-module RED fixture cannot call it;
// instead this re-runs the A4 scan over REAL production with an EMPTY allowlist
// and asserts it fires on the actual computeMAC callers (≥2: hmacSigner.Sign +
// hmacVerifier.Verify). That proves the scan genuinely detects computeMAC calls
// and that the real allowlist (not a vacuous scan) is what makes production GREEN.
func TestWebhookHMACFunnel_ComputeMACCallerAntiVacuity(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var fired []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{webhookPkgPattern}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != webhookPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				// Empty allowlist → every computeMAC caller is a violation.
				fired = append(fired, scanWebhookComputeMACCallers(p.Fset, f, rel, p.TypesInfo, map[string]bool{})...)
			}
			return nil
		})

	assert.GreaterOrEqual(t, len(fired), 2,
		"A4 anti-vacuity: empty allowlist must fire on ≥2 real computeMAC callers "+
			"(hmacSigner.Sign + hmacVerifier.Verify); got %d — scan may be vacuous", len(fired))
	for i, d := range fired {
		assert.NotEmpty(t, d.Rel, "A4 anti-vacuity: diagnostic[%d] has empty Rel (not clickable)", i)
		assert.Greater(t, d.Line, 0, "A4 anti-vacuity: diagnostic[%d] Line = %d, want > 0", i, d.Line)
	}
}
