package archtest

// replaydeps_inmem_funnel.go — importable REPLAYDEPS-INMEM-FUNNEL-01 rule logic
// (#825 / #2017).
//
// # REPLAYDEPS-INMEM-FUNNEL-01
//
// The in-memory distributed-replay constructors
//
//   - idempotency.NewInMemClaimer    (kernel/idempotency)
//   - auth.NewInMemoryNonceStore     (runtime/auth)
//
// are reachable in the hardened composition roots — cmd/corebundle (the
// production binary) and examples/ssobff (the demo composition root) — ONLY
// through cellmodules/replaydeps.Resolve's demo/single-pod branch. A
// composition root that calls either constructor directly re-opens the
// #2017-class gap: a durable-storage (postgres) deployment that silently runs
// an in-memory replay primitive in a multi-pod topology, where it cannot
// coordinate at-most-once / replay defense across replicas.
//
// This is the call-granularity sibling of the bus funnel
// COREBUNDLE-EVENTBUS-FUNNEL-01 (a golangci depguard import-ban). depguard
// cannot express THIS rule: both kernel/idempotency and runtime/auth are
// legitimately imported by the roots for their types (idempotency.Claimer,
// kauth.NonceStore), so a whole-package import ban would false-red. Hence an
// AST callsite scan, scoped to the two hardened roots.
//
// The sanctioned holder cellmodules/replaydeps is not under either scanned root,
// so its own NewInMemClaimer / NewInMemoryNonceStore calls are naturally out of
// scope — no allowlist entry needed.
//
// # AI-robust grade
//
// Medium. Upstream (the construction-bypass ban) is a CI-time AST scan; the
// downstream fail-close decision is a runtime guard in replaydeps.Resolve. A
// Hard form (sealing the in-mem constructors behind the resolver) is high cost —
// both constructors have legitimate callers across other examples and runtime
// packages outside this PR's scope — so it is deliberately not pursued (no
// low-cost Hard path to register per ai-robust.md §"审查要求").
//
// # Why pure-AST (not the typed façade)
//
// Each callsite is resolved by import-path + alias + selector name — no
// receiver-type / interface-implementation / const-evaluation is needed. Per
// ai-robust.md §载体选择, that is the pure-AST route. Reuses the generic
// firstQualifiedSelectorLine / fileDotImportsModule scanners from
// bcrypt_cost_funnel.go (same package, parameterised by module + selector).
//
// # Blind-spot inventory (per ai-robust.md §archtest)
//
//   - Function-value reference `f := idempotency.NewInMemClaimer; f(clk)`:
//     COVERED — firstQualifiedSelectorLine walks every <alias>.<sel>
//     SelectorExpr, not just CallExpr.Fun.
//   - Dot-import `import . ".../idempotency"; NewInMemClaimer(...)`: the symbol
//     becomes a bare *ast.Ident the SelectorExpr scan misses. Closed by the
//     reverse self-test TestREPLAYDEPS_INMEM_FUNNEL_01_NoDotImportBlindSpot.
//   - Reflection-based construction: out of scope, treated as theoretical.
//
// Not registered in StandardCellRules: the hardened-root set is gocell-specific
// (no external-cell extension), so it would be vacuous-green for an external
// consumer — kept importable but gocell-only, mirroring BCRYPT-COST-FUNNEL-01.

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	// idempotencyInMemModule / runtimeAuthModule are the import paths whose
	// in-memory replay constructors the funnel bans in the hardened roots.
	idempotencyInMemModule = PlatformFrameworkModulePath + "/kernel/idempotency"
	runtimeAuthModule      = PlatformFrameworkModulePath + "/runtime/auth"
)

// replaydepsHardenedRoots are the module-relative composition-root directories
// the funnel scans. These two roots route all replay primitives through
// replaydeps.Resolve; any direct in-mem constructor call here is a regression.
// Other examples (todoorder / iotdevice / …) are intentionally NOT scanned —
// whether their in-memory primitives are correct depends on whether they use a
// durable outbox, which is out of this PR's scope (see ADR 202606131500-1940).
var replaydepsHardenedRoots = []string{"cmd/corebundle", "examples/ssobff"}

// replaydepsInmemBannedCall describes one forbidden in-memory constructor call.
type replaydepsInmemBannedCall struct {
	module  string
	defName string
	selName string
	message string
}

func replaydepsInmemBannedCalls() []replaydepsInmemBannedCall {
	return []replaydepsInmemBannedCall{
		{
			module:  idempotencyInMemModule,
			defName: "idempotency",
			selName: "NewInMemClaimer",
			message: "idempotency.NewInMemClaimer in a hardened composition root; route through replaydeps.Resolve (REPLAYDEPS-INMEM-FUNNEL-01)",
		},
		{
			module:  runtimeAuthModule,
			defName: "auth",
			selName: "NewInMemoryNonceStore",
			message: "auth.NewInMemoryNonceStore in a hardened composition root; route through replaydeps.Resolve (REPLAYDEPS-INMEM-FUNNEL-01)",
		},
	}
}

// CheckReplaydepsInmemFunnel01 scans the hardened composition roots for direct
// calls to the in-memory replay constructors and returns one Diagnostic per
// offending callsite. It is the single rule body — the Test* dogfoods it via
// Report — so the exact scan is the one enforced.
func CheckReplaydepsInmemFunnel01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, replaydepsHardenedRoots).Files()
	if err != nil {
		t.Fatalf("REPLAYDEPS-INMEM-FUNNEL-01: scanner.DirsScope: %v", err)
	}

	var diags []Diagnostic
	for _, path := range files {
		rel := funnelRelSlash(root, path)
		for _, banned := range replaydepsInmemBannedCalls() {
			line, ok, perr := firstQualifiedSelectorLine(path, banned.module, banned.defName, banned.selName)
			if perr != nil {
				t.Fatalf("REPLAYDEPS-INMEM-FUNNEL-01: parse %s: %v", rel, perr)
			}
			if ok {
				diags = append(diags, Diagnostic{Rel: rel, Line: line, Message: banned.message})
			}
		}
	}
	return diags
}
