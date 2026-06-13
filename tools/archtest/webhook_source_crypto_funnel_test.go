//go:build archtest

// INVARIANT: WEBHOOK-SOURCE-CRYPTO-FUNNEL-01
//
// webhook_source_crypto_funnel_test.go — caller-allowlist for the two sealed
// webhook-secret crypto entry points in kernel/webhook:
//
//   - kwh.Source.Encrypt(ctx, vt) — seals a source secret into ciphertext.
//   - kwh.NewSourceFromCiphertext(ctx, vt, ...) — reconstructs a sealed Source.
//
// # What this guards (cluster C1, PR #1929 / #1540)
//
// Both entry points take a CALLER-SUPPLIED kcrypto.ValueTransformer and feed the
// raw secret through it (sourcecrypto.go: vt.Encrypt(ctx, s.secret, aad)). Any
// package that holds a webhook.Source (e.g. from SourceStore.Lookup) could pass a
// transformer that captures the plaintext at that hop. The plaintext therefore
// does NOT "never leave kernel/webhook" as a type-system property — it leaves
// through whatever ValueTransformer the caller supplies.
//
// This rule locks the WRITE/READ crypto sites to the single sanctioned holder of
// the trusted, composition-root-built transformer: the persistence repo
// adapters/postgres.WebhookSourceRepository (Upsert / LoadAll). A new caller that
// wires its own transformer trips CI. Combined with the repo constructor
// rejecting a passthrough NoopTransformer (F2), the cluster's "no plaintext
// observed / no plaintext at rest" promise is enforced at Medium.
//
// # AI-robust rating
//
//   - Rating: MEDIUM (caller-allowlist archtest, type-aware go/types scan).
//   - Downstream enforcement: walk every ident; match info.Uses *types.Func
//     FullName against the two sealed entry points; require the enclosing func
//     FullName ∈ webhookSourceCryptoCallerAllowlist. Covers direct call,
//     parenthesized call, and function/method-value capture (a direct-call-only
//     scan would miss `fn := kwh.NewSourceFromCiphertext`). The match is on
//     go/types FullName (package-path qualified), so a same-named symbol in
//     another package cannot inherit the allowance, and import aliases / dot-
//     imports cannot bypass it.
//   - Upstream: the secret field itself is sealed Hard — Source.secret is
//     unexported with no getter, and the ONLY bytes entry is the validated minter
//     NewSource (so the plaintext never materializes as a loose returnable slice
//     outside kernel/webhook). What is Medium, not Hard, is "no in-process code
//     observes the plaintext during encryption": that rests on the caller being
//     the trusted repo with a real KMS-backed transformer, which this rule (the
//     caller location) + F2 (the repo rejecting Noop) enforce.
//   - Permanent ceiling: Go visibility cannot express "only adapters/postgres may
//     call this EXPORTED method" at the type system — the method must be exported
//     for a different module to call it at all. A type-system Hard would require
//     the encrypt capability to be a sealed token minted only inside the trusted
//     path, but the transformer is necessarily reached via an open interface
//     (ValueTransformer / KeyHandle) that an in-process adversary can implement,
//     so the boundary only moves down a level — same permanent ceiling family as
//     WEBHOOK-HMAC-FUNNEL-01/A3-A4 (#851 / #893 / #1282 / #1375 / #1243). The
//     archtest is the downstream Medium backstop for the realistic regression
//     vector: a NEW caller wiring its own transformer.
//
// # register=no — gocell-internal-layout
//
// This rule references the sanctioned repo's FullName (adapters/postgres) which
// an external Cell repo lacks, so it is NOT in StandardCellRules() and not
// promised to run externally — dogfooded by TestWebhookSourceCryptoFunnel below.
//
// # Blind spots (ai-robust 强制反向自检; reverse self-tests below)
//
//	B1 — rule-logic regression / vacuous pass:
//	  TestWebhookSourceCryptoFunnel_AntiVacuity re-runs the scan over REAL
//	  production with an EMPTY allowlist and asserts it fires on the ≥2 real
//	  callers (repo Upsert + LoadAll) — proving the scan detects the symbols and
//	  the real allowlist is what makes production GREEN (non-vacuous).
//	B2 — use-shape coverage: the archtest_fixture RED module
//	  (internal/webhooksourcecryptofixture) calls both entry points via direct
//	  call AND value-capture from a non-repo context;
//	  TestWebhookSourceCryptoFunnel_ReverseFixture asserts every shape fires.
//	B3 — reflect/unsafe dynamic dispatch of Source.Encrypt bypasses the ident
//	  scan (no SelectorExpr). Not present in production today; a reflective call
//	  to an exported method on a value with an unexported secret field is
//	  conspicuous in review. Documented, not machine-checked (same class as the
//	  audit-trace reflect blind spot).
//
// ref: tools/archtest/audit_trace_id_write_caller_test.go (exported-symbol caller-allowlist over Production)
// ref: tools/archtest/webhook_hmac_funnel.go (A4 computeMAC caller-allowlist)
// ref: docs/architecture/202606122050-1540-adr-webhook-source-persistence.md
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// webhookSourceCryptoRuleID is the single source for this rule's ID, used in the
// Report call and every assertion message.
const webhookSourceCryptoRuleID = "WEBHOOK-SOURCE-CRYPTO-FUNNEL-01"

const (
	// webhookSourceEncryptFunc / webhookNewSourceFromCiphertextFunc are the
	// go/types FullNames of the two sealed webhook-secret crypto entry points.
	// Source.Encrypt is a value-receiver method → "(pkg.Source).Encrypt";
	// NewSourceFromCiphertext is a free func → "pkg.NewSourceFromCiphertext".
	// Anchored to PlatformModulePath so a module rename updates one place.
	webhookSourceEncryptFunc           = "(" + PlatformModulePath + "/kernel/webhook.Source).Encrypt"
	webhookNewSourceFromCiphertextFunc = PlatformModulePath + "/kernel/webhook.NewSourceFromCiphertext"

	// webhookSourceRepoUpsertFunc / webhookSourceRepoLoadAllFunc are the go/types
	// FullNames of the ONLY two functions permitted to call the sealed entry
	// points: the postgres persistence repo's pointer-receiver methods.
	webhookSourceRepoUpsertFunc  = "(*" + PlatformModulePath + "/adapters/postgres.WebhookSourceRepository).Upsert"
	webhookSourceRepoLoadAllFunc = "(*" + PlatformModulePath + "/adapters/postgres.WebhookSourceRepository).LoadAll"
)

// webhookSourceCryptoSealedFuncs is the closed set of crypto entry-point
// FullNames this rule guards.
var webhookSourceCryptoSealedFuncs = map[string]bool{
	webhookSourceEncryptFunc:           true,
	webhookNewSourceFromCiphertextFunc: true,
}

// webhookSourceCryptoCallerAllowlist is the set of go/types FullNames permitted
// to call the sealed crypto entry points. Only the sanctioned persistence repo
// holds the trusted composition-root transformer; any other caller could supply
// its own and capture the raw secret.
var webhookSourceCryptoCallerAllowlist = map[string]bool{
	webhookSourceRepoUpsertFunc:  true,
	webhookSourceRepoLoadAllFunc: true,
}

// scanWebhookSourceCryptoCallers reports every USE of a sealed webhook-secret
// crypto entry point (Source.Encrypt / NewSourceFromCiphertext) whose enclosing
// func is not in allowlist. It walks every identifier and matches info.Uses to
// the guarded FullNames, so ALL use forms are covered — direct call,
// parenthesized call, and function/method-value capture. The symbols' own
// declarations live in info.Defs (not info.Uses), so kernel/webhook never
// false-fires on its definitions. Passing allowlist as a parameter lets the
// anti-vacuity self-test re-run with an empty allowlist.
func scanWebhookSourceCryptoCallers(
	fset *token.FileSet, file *ast.File, rel string, info *types.Info, allowlist map[string]bool,
) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		fnObj, ok := info.Uses[id].(*types.Func)
		if !ok || !webhookSourceCryptoSealedFuncs[fnObj.FullName()] {
			return
		}
		if enc, ok := ResolveEnclosingFunc(info, file, id); ok && enc != nil && allowlist[enc.FullName()] {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: fset.Position(id.Pos()).Line,
			Message: "webhook source secret crypto (" + fnObj.Name() + ") used outside the sanctioned " +
				"persistence repo; Source.Encrypt / NewSourceFromCiphertext may be called only by " +
				"adapters/postgres.WebhookSourceRepository (Upsert/LoadAll), so a caller cannot supply " +
				"its own ValueTransformer and capture the raw secret (WEBHOOK-SOURCE-CRYPTO-FUNNEL-01)",
		})
	})
	return out
}

// scanWebhookSourceCryptoProduction runs scanWebhookSourceCryptoCallers over all
// non-test production packages with the given allowlist and returns the union of
// diagnostics. Shared by the GREEN dogfood (real allowlist) and the anti-vacuity
// self-check (empty allowlist).
func scanWebhookSourceCryptoProduction(t *testing.T, allowlist map[string]bool) []Diagnostic {
	t.Helper()
	var out []Diagnostic
	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, f := range p.Files {
			rel := p.Rel(f)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			out = append(out, scanWebhookSourceCryptoCallers(p.Fset, f, rel, p.TypesInfo, allowlist)...)
		}
		return nil
	})
	return out
}

// TestWebhookSourceCryptoFunnel is the GREEN dogfood: with the real caller-
// allowlist, no production code outside the sanctioned repo calls the sealed
// crypto entry points.
func TestWebhookSourceCryptoFunnel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	Report(t, webhookSourceCryptoRuleID,
		scanWebhookSourceCryptoProduction(t, webhookSourceCryptoCallerAllowlist))
}

// TestWebhookSourceCryptoFunnel_AntiVacuity is the B1 self-check: re-run the scan
// over REAL production with an EMPTY allowlist and assert it fires on the actual
// callers (≥2: repo Upsert calling Source.Encrypt + repo LoadAll calling
// NewSourceFromCiphertext). This proves the scan genuinely detects the symbols
// and that the real allowlist — not a vacuous scan — is what makes production
// GREEN.
func TestWebhookSourceCryptoFunnel_AntiVacuity(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	fired := scanWebhookSourceCryptoProduction(t, map[string]bool{})

	assert.GreaterOrEqual(t, len(fired), 2,
		webhookSourceCryptoRuleID+" anti-vacuity: empty allowlist must fire on ≥2 real callers "+
			"(repo Upsert→Source.Encrypt + repo LoadAll→NewSourceFromCiphertext); got %d — scan may be vacuous",
		len(fired))
	for i, d := range fired {
		assert.NotEmpty(t, d.Rel, webhookSourceCryptoRuleID+" anti-vacuity: diagnostic[%d] has empty Rel (not clickable)", i)
		assert.Greater(t, d.Line, 0, webhookSourceCryptoRuleID+" anti-vacuity: diagnostic[%d] Line = %d, want > 0", i, d.Line)
	}
}

// TestWebhookSourceCryptoFunnel_ReverseFixture is the B2 self-check: scan the
// archtest_fixture RED module with the REAL allowlist and assert the scanner
// fires on every prohibited use shape (direct call + value capture, for both
// entry points). The fixture lives behind the archtest_fixture build tag, so the
// production scan above never sees it; here it is loaded explicitly via Fixture.
func TestWebhookSourceCryptoFunnel_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var fired []Diagnostic
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/webhooksourcecryptofixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				fired = append(fired,
					scanWebhookSourceCryptoCallers(p.Fset, f, rel, p.TypesInfo, webhookSourceCryptoCallerAllowlist)...)
			}
			return nil
		})

	// Four prohibited uses: badEncrypt (call), badDecrypt (call),
	// badEncryptCapture (method value), badDecryptCapture (func value).
	assert.GreaterOrEqual(t, len(fired), 4,
		webhookSourceCryptoRuleID+" reverse fixture: expected ≥4 diagnostics from "+
			"webhooksourcecryptofixture (badEncrypt + badDecrypt + badEncryptCapture + "+
			"badDecryptCapture); got %d — a missing shape means the scan regressed", len(fired))
	for i, d := range fired {
		assert.NotEmpty(t, d.Rel, webhookSourceCryptoRuleID+" reverse fixture: diagnostic[%d] has empty Rel", i)
		assert.Greater(t, d.Line, 0, webhookSourceCryptoRuleID+" reverse fixture: diagnostic[%d] Line = %d, want > 0", i, d.Line)
	}
}
