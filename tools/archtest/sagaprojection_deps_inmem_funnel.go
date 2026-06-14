package archtest

// sagaprojection_deps_inmem_funnel.go — importable SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01
// rule logic (#1391 / #2060).
//
// # SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01
//
// The in-process single-pod distributed-lock constructor
//
//	distlock.NewInProcessDriver (runtime/distlock)
//
// is constructed in the wiring layers — cmd/*, cellmodules/*, examples/* — ONLY
// inside the sanctioned resolver cellmodules/sagaprojectiondeps, whose demo /
// single-pod branch is its single home. A composition root that constructs it
// directly bypasses the topology gate and re-opens the silent-single-pod-in-
// multi-pod footgun: an in-process locker grants every pod the projection lock,
// so N replicas each believe they are leader and double-apply the projection.
//
// This is the call-granularity sibling of REPLAYDEPS-INMEM-FUNNEL-01 (the in-mem
// claimer/nonce funnel) and the 4th sealed single-pod primitive alongside the
// bus / claimer / nonce funnels (see .claude/rules/gocell/eventbus.md
// §"复用层选型"). A golangci depguard import-ban cannot express it: runtime/distlock
// is legitimately imported by the resolver (and others) for the Locker type, so a
// whole-package ban would false-red. Hence an AST callsite scan over the wiring
// roots, allowlisting the sanctioned resolver dir.
//
// # AI-robust grade
//
// Medium. The construction-bypass ban is a CI-time AST scan; the downstream
// fail-close decision (postgres-needs-pool, multi-pod-needs-Redis) is a runtime
// guard in sagaprojectiondeps.Resolve. A Hard form (sealing the constructor
// behind the resolver via an unexported type) is not pursued: NewInProcessDriver
// is a general runtime/distlock primitive that other single-pod callers may
// legitimately want, so over-sealing it would force a wider refactor than this
// PR's scope (no low-cost Hard path to register per ai-robust.md §"审查要求").
//
// # Blind-spot inventory (per ai-robust.md §archtest)
//
//   - Anti-vacuity: TestSAGA_PROJECTION_DEPS_INMEM_FUNNEL_01_ScanCore_SyntheticTree
//     drives the FULL scan core (scanSagaProjectionDepsInmemFunnel) over a
//     t.TempDir tree, proving DirsScope finds an offending file under a scanned
//     root, the sanctioned-dir allowlist suppresses an identical violation, and
//     the Diagnostic (Rel + Message + Line) is assembled — not just the selector
//     helper in isolation.
//   - Function-value reference `f := distlock.NewInProcessDriver; f(clk)`:
//     COVERED — firstQualifiedSelectorLine walks every <alias>.<sel> SelectorExpr.
//   - Dot-import `import . ".../distlock"; NewInProcessDriver(...)`: the symbol
//     becomes a bare *ast.Ident the SelectorExpr scan misses. Closed by the
//     reverse self-test TestSAGA_PROJECTION_DEPS_INMEM_FUNNEL_01_NoDotImportBlindSpot.
//   - Reflection-based construction: out of scope, treated as theoretical.
//   - runtime/distlock's OWN tests (inprocess_driver_test.go) are not under any
//     scanned wiring root, so they are naturally out of scope — no allowlist needed.
//
// Not registered in StandardCellRules: the wiring-root set is gocell-specific
// (no external-cell extension), so it would be vacuous-green for an external
// consumer — kept importable but gocell-only, mirroring REPLAYDEPS-INMEM-FUNNEL-01.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// distlockModule is the import path whose in-process single-pod driver
// constructor the funnel funnels through sagaprojectiondeps.
const distlockModule = PlatformFrameworkModulePath + "/runtime/distlock"

// sagaProjectionDepsScannedRoots are the wiring layers scanned for a direct
// distlock.NewInProcessDriver call. The sanctioned resolver lives under
// cellmodules/, so it is allowlisted by sagaProjectionDepsSanctionedDir below.
var sagaProjectionDepsScannedRoots = []string{"cmd", "cellmodules", "examples"}

// sagaProjectionDepsSanctionedDir is the one module-relative dir allowed to
// construct the in-process driver — the topology-gated resolver.
const sagaProjectionDepsSanctionedDir = "cellmodules/sagaprojectiondeps"

// sagaProjectionDepsFunnelMessage is the Diagnostic.Message emitted for every
// offending callsite. Shared between the real scan and the synthetic-tree test
// so the fixture asserts the exact production message.
const sagaProjectionDepsFunnelMessage = "distlock.NewInProcessDriver outside cellmodules/sagaprojectiondeps; route the " +
	"saga-projection leader locker through sagaprojectiondeps.Resolve " +
	"(SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01)"

// CheckSagaProjectionDepsInmemFunnel01 scans the running module's wiring roots
// for direct calls to distlock.NewInProcessDriver outside the sanctioned resolver
// dir and returns one Diagnostic per offending callsite. It is a thin wrapper
// over scanSagaProjectionDepsInmemFunnel rooted at findModuleRoot(t); the same
// scan core is exercised against a synthetic temp tree by the funnel tests so
// scope + allowlist + diagnostic assembly are proven (not just the selector
// helper). Dogfooded by the Test* via Report so the exact scan is the one enforced.
func CheckSagaProjectionDepsInmemFunnel01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	diags, err := scanSagaProjectionDepsInmemFunnel(findModuleRoot(t))
	if err != nil {
		t.Fatalf("SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01: %v", err)
	}
	return diags
}

// scanSagaProjectionDepsInmemFunnel is the testable scan core: given an explicit
// module root, it walks the scoped wiring roots, skips the sanctioned resolver
// dir, and returns one Diagnostic per direct distlock.NewInProcessDriver
// callsite. Taking root as a parameter (rather than discovering it) lets the
// funnel tests run the FULL rule — DirsScope + allowlist skip + Diagnostic
// assembly — against a synthetic t.TempDir tree, making the anti-vacuity genuine.
// It returns an error instead of calling t.Fatalf so it is reusable from any
// caller (the rule wrapper converts to t.Fatalf).
func scanSagaProjectionDepsInmemFunnel(root string) ([]Diagnostic, error) {
	files, err := scanner.DirsScope(root, sagaProjectionDepsScannedRoots).Files()
	if err != nil {
		return nil, fmt.Errorf("scanner.DirsScope: %w", err)
	}

	var diags []Diagnostic
	for _, path := range files {
		rel := funnelRelSlash(root, path)
		if strings.HasPrefix(rel, sagaProjectionDepsSanctionedDir+"/") {
			continue // sanctioned: the resolver is the single construction site
		}
		line, ok, perr := firstQualifiedSelectorLine(path, distlockModule, "distlock", "NewInProcessDriver")
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", rel, perr)
		}
		if ok {
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    line,
				Message: sagaProjectionDepsFunnelMessage,
			})
		}
	}
	return diags, nil
}
