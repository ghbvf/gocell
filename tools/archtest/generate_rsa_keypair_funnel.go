package archtest

// generate_rsa_keypair_funnel.go — importable GENERATE-RSA-KEYPAIR-FUNNEL-01 rule
// logic (#2052; #825 / #2017 family).
//
// # GENERATE-RSA-KEYPAIR-FUNNEL-01
//
// The ephemeral RSA key-pair generator
//
//   - auth.GenerateRSAKeyPair    (runtime/auth)
//
// is reachable in the hardened composition roots — cmd/corebundle (the production
// binary) and examples/ssobff (the demo composition root) — ONLY through
// cellmodules/cellsecrets.LoadKeySet's demo branch. A composition root that calls
// GenerateRSAKeyPair directly re-opens the #2052 gap: a multi-pod deployment whose
// each replica mints its own per-process signing key, so a token signed by replica
// A verifies as 401 against replica B. cellsecrets.LoadKeySet topology-gates the
// key source — ephemeral in demo, shared env keys (GOCELL_JWT_PRIVATE_KEY /
// GOCELL_JWT_PUBLIC_KEY) in real mode, fail-closed when missing.
//
// This is the 4th sibling of the #825/#2017 single-pod-primitive family, joining
// COREBUNDLE-EVENTBUS-FUNNEL-01 (bus, golangci depguard import-ban) and
// REPLAYDEPS-INMEM-FUNNEL-01 (claimer + nonce, AST callsite scan). Like the latter
// — and unlike the bus funnel — depguard cannot express THIS rule: runtime/auth is
// legitimately imported by the roots for many types (JWTIssuer, JWTVerifier,
// ListenerAuth, KeySet), so a whole-package import ban would false-red. Hence an
// AST callsite scan, scoped to the two hardened roots.
//
// The sanctioned holder cellmodules/cellsecrets.LoadKeySet is not under either
// scanned root, so its own GenerateRSAKeyPair call (the demo branch) is naturally
// out of scope — no allowlist entry needed.
//
// # Scanned-root scope (why examples/corebundlestarter is excluded)
//
// examples/corebundlestarter calls auth.GenerateRSAKeyPair directly (run.go) but
// is deliberately NOT scanned: its topology is hard-coded bootstrap.NewTopology("",
// "memory", false) — always dev/memory, never real, so it cannot run multi-pod and
// its ephemeral key is safe. Only cmd/corebundle and examples/ssobff are
// topology-driven composition roots that can reach real multi-pod mode. This
// mirrors REPLAYDEPS-INMEM-FUNNEL-01, which scans the same two roots and likewise
// excludes the other examples (their correctness depends on their fixed topology).
// If corebundlestarter ever gains a real/topology-driven path, add it here.
//
// # AI-robust grade
//
// Medium. The funnel (AST scan) is the CI-time code-regression guard; the actual
// runtime correctness guarantee is the Hard-equivalent pairing of a sealed
// bootstrap.Topology (AdapterMode() cannot be forged — it is derived from validated
// env) plus the startup fail-fast in cellsecrets.LoadKeySet — in real adapter mode
// a process with no shared JWT key env vars cannot boot, so it can never silently
// run ephemeral per-pod keys. No
// low-cost Hard form exists for the funnel itself: a depguard import-ban false-reds
// (see above); unexporting / sealing GenerateRSAKeyPair breaks legitimate callers
// (cellsecrets itself, other examples, tests building fixture keys); a sealed
// KeySet-token type that only LoadKeySet can mint is a true Hard form but touches
// runtime/auth's key types + cellsecrets + both roots — high cost, out of #2052
// scope. Per ai-robust.md §审查要求 (Hard only when a low-cost path exists), Medium
// is the terminal grade; the sealed-token option is recorded as a high-cost backlog
// candidate, not pursued here.
//
// # Scope precision
//
// The funnel targets GenerateRSAKeyPair = the per-pod EPHEMERAL key class (the 401
// root cause). A hard-coded static key pair is a different anti-pattern (it is the
// SAME across pods, so it does not 401) already guarded by cellsecrets'
// WellKnownDemoKeys / ResolveAndRejectDemoKey — not this funnel's concern.
//
// # Why pure-AST (not the typed façade)
//
// The callsite is resolved by import-path + alias + selector name — no
// receiver-type / interface-implementation / const-evaluation is needed. Per
// ai-robust.md §载体选择 that is the pure-AST route. Reuses the generic
// firstQualifiedSelectorLine / fileDotImportsModule scanners (same package) and the
// shared runtimeAuthModule const.
//
// # Blind-spot inventory (per ai-robust.md §archtest)
//
//   - Function-value reference `f := auth.GenerateRSAKeyPair; f()`: COVERED —
//     firstQualifiedSelectorLine walks every <alias>.<sel> SelectorExpr, not just
//     CallExpr.Fun.
//   - Dot-import `import . ".../runtime/auth"; GenerateRSAKeyPair()`: the symbol
//     becomes a bare *ast.Ident the SelectorExpr scan misses. Closed by the reverse
//     self-test TestGENERATE_RSA_KEYPAIR_FUNNEL_01_NoDotImportBlindSpot.
//   - Reflection-based construction: out of scope, treated as theoretical.
//
// Not registered in StandardCellRules: the hardened-root set is gocell-specific (no
// external-cell extension), so it would be vacuous-green for an external consumer —
// kept importable but gocell-only, mirroring REPLAYDEPS-INMEM-FUNNEL-01.

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// generateRSAKeypairHardenedRoots are the module-relative composition-root
// directories the funnel scans — the same two roots REPLAYDEPS-INMEM-FUNNEL-01
// guards. Both route JWT signing keys through cellsecrets.LoadKeySet; any direct
// auth.GenerateRSAKeyPair call here is a #2052 regression.
var generateRSAKeypairHardenedRoots = []string{"cmd/corebundle", "examples/ssobff"}

// generateRSAKeypairBanMessage is the Diagnostic message for an offending callsite.
const generateRSAKeypairBanMessage = "auth.GenerateRSAKeyPair in a hardened composition root; " +
	"route JWT keys through cellsecrets.LoadKeySet (GENERATE-RSA-KEYPAIR-FUNNEL-01)"

// CheckGenerateRSAKeypairFunnel01 scans the hardened composition roots for direct
// calls to auth.GenerateRSAKeyPair and returns one Diagnostic per offending
// callsite. It is the single rule body — the Test* dogfoods it via Report — so the
// exact scan is the one enforced.
func CheckGenerateRSAKeypairFunnel01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, generateRSAKeypairHardenedRoots).Files()
	if err != nil {
		t.Fatalf("GENERATE-RSA-KEYPAIR-FUNNEL-01: scanner.DirsScope: %v", err)
	}

	var diags []Diagnostic
	for _, path := range files {
		rel := funnelRelSlash(root, path)
		line, ok, perr := firstQualifiedSelectorLine(path, runtimeAuthModule, "auth", "GenerateRSAKeyPair")
		if perr != nil {
			t.Fatalf("GENERATE-RSA-KEYPAIR-FUNNEL-01: parse %s: %v", rel, perr)
		}
		if ok {
			diags = append(diags, Diagnostic{Rel: rel, Line: line, Message: generateRSAKeypairBanMessage})
		}
	}
	return diags
}
