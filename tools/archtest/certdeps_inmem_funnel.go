package archtest

// certdeps_inmem_funnel.go — importable CERTDEPS-INMEM-FUNNEL-01 rule logic (#2302).
//
// # CERTDEPS-INMEM-FUNNEL-01
//
// The softca construction primitives
//
//   - softca.NewDevCA / softca.NewFileCA              (adapters/softca CA material)
//   - softca.NewSoftCA                                  (signer + revocation bundle)
//   - softca.NewSigner / softca.NewRevocationStore
//   - softca.NewMemLedger                               (in-memory issuance ledger)
//
// are reachable in the wiring layer (cmd/*, cellmodules/*, examples/*) ONLY
// through cellmodules/certdeps.Resolve's demo branch. A composition root that
// constructs any of them directly bypasses the topology gate and re-opens the
// cert footgun: a postgres/production deployment silently served by the dev
// soft-CA — whose trust anchor rotates on every restart and whose in-memory
// MemLedger loses issuance and revocation state across restart — instead of
// failing closed the way certdeps.Resolve's postgres branch does.
//
// This is the cert sibling of REPLAYDEPS-INMEM-FUNNEL-01 and
// SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01 (same call-granularity AST shape). A
// golangci depguard import-ban cannot express it: adapters/softca is
// legitimately imported by certdeps itself and by leaf tests for the
// certsigning.Signer / RevocationStore types, so a whole-package import ban
// would false-red. Hence an AST callsite scan with an allowlist for the sole
// sanctioned holder, cellmodules/certdeps.
//
// Theme placement: this is a composition WIRING funnel, so it lives in its own
// file beside its true sibling replaydeps_inmem_funnel.go — NOT in
// cert_invariants_test.go, which holds the orthogonal cert SIGNING-domain
// invariants (CERT-SIGN-FUNNEL-01 mint funnel, CERT-PRIVATE-KEY-CUSTODY-01 key
// custody). Signing correctness and composition-root wiring are different
// concerns; folding them would fragment the theme.
//
// # AI-robust grade
//
// Medium. Upstream (the construction-bypass ban) is a CI-time AST scan over the
// wiring roots; the downstream fail-close decision is a runtime guard in
// certdeps.Resolve (postgres → fail-fast), because bootstrap.Topology is a
// runtime value the type system cannot express — per ai-robust.md "runtime guard
// 只用于 type system 不可表达的边界". A Hard form (sealing softca's constructors
// so the bypass is unexpressible) is high cost and wrong: adapters/softca is a
// standalone, independently-reusable adapter with its own test surface, and Go
// has no friend-package mechanism, so unexporting its constructors to route them
// only through certdeps would break the adapter's layering. Same conclusion as
// REPLAYDEPS-INMEM-FUNNEL-01 — deliberately not pursued, no low-cost Hard path to
// register per ai-robust.md §审查要求.
//
// # Why pure-AST (not the typed façade)
//
// Each callsite resolves by import-path + alias + selector name — no
// receiver-type / interface-implementation / const-evaluation needed. Per
// ai-robust.md §载体选择 that is the pure-AST route; reuses the generic
// firstQualifiedSelectorLine scanner (parameterised by module + selector).
//
// # Blind-spot inventory (per ai-robust.md §archtest)
//
//   - Function-value reference `f := softca.NewDevCA; f(clk)`: COVERED —
//     firstQualifiedSelectorLine walks every <alias>.<sel> SelectorExpr, not just
//     CallExpr.Fun.
//   - Dot-import `import . ".../softca"; NewDevCA(...)`: the symbol becomes a bare
//     *ast.Ident the SelectorExpr scan misses. Closed by the reverse self-test
//     TestCERTDEPS_INMEM_FUNNEL_01_NoDotImportBlindSpot.
//   - corecells/ and external cell modules (the epic's externalcells/mdm has its
//     own module) are NOT under the scanned roots — vacuous-green for an external
//     consumer, mirroring REPLAYDEPS-INMEM-FUNNEL-01. External-module wiring
//     governance is the #1090 / M9 track.
//   - Reflection-based construction: out of scope, treated as theoretical.
//
// Not registered in StandardCellRules: the scanned-root set is gocell-specific,
// so it would be vacuous-green for an external cell — kept importable but
// gocell-only, mirroring REPLAYDEPS-INMEM-FUNNEL-01.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// certdepsSoftcaModule is the import path whose CA / signer / ledger constructors
// the funnel bans outside the sanctioned resolver.
const certdepsSoftcaModule = PlatformModulePath + "/adapters/softca"

// certdepsInmemSanctionedDir is the sole construction site: certdeps.Resolve's
// demo branch. It is under a scanned root (cellmodules/) so it must be allowlisted.
const certdepsInmemSanctionedDir = "cellmodules/certdeps"

// certdepsInmemScannedRoots are the module-relative wiring-layer directories the
// funnel scans. certdeps is meant to be THE single seam for all cert wiring, so
// the scan is broad (cf. SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01).
var certdepsInmemScannedRoots = []string{"cmd", "cellmodules", "examples"}

// certdepsInmemBannedSelectors are the softca constructors a wiring root must not
// call directly — all routed through certdeps.Resolve instead.
var certdepsInmemBannedSelectors = []string{
	"NewDevCA", "NewFileCA", "NewSoftCA", "NewSigner", "NewRevocationStore", "NewMemLedger",
}

// scanCertdepsInmemFunnel is the testable rule core: it scans the wiring roots
// under root for direct softca constructor calls, skipping the sanctioned
// certdeps dir, and returns one Diagnostic per offending callsite. Taking root as
// a parameter lets the synthetic-tree anti-vacuity test drive it over a temp tree.
func scanCertdepsInmemFunnel(root string) ([]Diagnostic, error) {
	files, err := scanner.DirsScope(root, certdepsInmemScannedRoots).Files()
	if err != nil {
		return nil, err
	}

	var diags []Diagnostic
	for _, path := range files {
		rel := funnelRelSlash(root, path)
		if strings.HasPrefix(rel, certdepsInmemSanctionedDir+"/") {
			continue // sanctioned: certdeps.Resolve is the sole construction site
		}
		for _, selName := range certdepsInmemBannedSelectors {
			line, ok, perr := firstQualifiedSelectorLine(path, certdepsSoftcaModule, "softca", selName)
			if perr != nil {
				return nil, perr
			}
			if ok {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: fmt.Sprintf("softca.%s in a wiring root; route cert wiring through "+
						"certdeps.Resolve (CERTDEPS-INMEM-FUNNEL-01)", selName),
				})
			}
		}
	}
	return diags, nil
}

// CheckCertdepsInmemFunnel01 scans the real module tree and returns one Diagnostic
// per offending callsite. It is the single rule body — the Test* dogfoods it via
// Report — so the exact scan is the one enforced.
func CheckCertdepsInmemFunnel01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	diags, err := scanCertdepsInmemFunnel(findModuleRoot(t))
	if err != nil {
		t.Fatalf("CERTDEPS-INMEM-FUNNEL-01: %v", err)
	}
	return diags
}
