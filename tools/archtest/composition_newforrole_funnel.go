package archtest

// composition_newforrole_funnel.go — importable COMPOSITION-NEWFORROLE-FUNNEL-01
// rule logic (#2278 PR-2). Non-test home so the detector core is shared by the
// production scan, the synthetic RED fixture, and the dot-import blind-spot
// guard — single source, no parallel rule body.

// # COMPOSITION-NEWFORROLE-FUNNEL-01
//
// composition.NewForRole is the sole sanctioned entry by which a production
// composition root (cmd/*, examples/*) mounts cells: it derives the per-process
// colocated subset from the deployment topology spec and filters the module set
// to it (the all-colocated monolith mounts everything; a split role mounts only
// its own cells). Wiring-layer packages MUST NOT call composition.New directly:
// New takes the full assembly cell-id set and mounts whatever modules it is
// given, so a root that calls New(allCells).With(allMods) in a split topology
// silently double-mounts a remote cell (the failure mode D4 describes).
//
// The funnel bans the bare composition.New selector in the wiring roots. The
// composition package itself (where NewForRole legitimately calls New) is the
// framework, not under a scanned root, so its New call is naturally out of
// scope. composition.NewForRole and composition.NewSharedDeps are distinct
// selectors and are not matched. Test files (*_test.go) are excluded by the
// scanner: integration tests legitimately exercise the lower-level New API.
//
// # AI-robust grade: Medium (AST callsite scan)
//
// NewForRole is an exported cross-package constructor; Go visibility cannot
// express "only the composition root may call it", so this is a CI-time AST
// scan — the same family as CELLTRANSPORT-SELECT-FUNNEL-01 and
// CROSSCELLOBS-MINTER-FUNNEL-01. The true fail-closed enforcement is the
// runtime MOUNTED-EQUALS-COLOCATED phase guard (upstream backstop), which
// catches any bypass regardless of how the build was assembled.
//
// # Blind spots
//
//   - Dot-import of runtime/composition: `import . ".../composition"; New(...)`
//     would make New a bare ident the SelectorExpr scan misses. Closed by the
//     reverse self-test TestCOMPOSITION_NEWFORROLE_FUNNEL_01_NoDotImportBlindSpot.
//   - Reflection-based construction: out of scope, treated as theoretical.

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// runtimeCompositionModule is the import path of the composition package whose
// bare New constructor the funnel bans in wiring-layer roots.
const runtimeCompositionModule = PlatformFrameworkModulePath + "/runtime/composition"

// compositionNewForRoleWiringRoots are the module-relative wiring-layer prefixes
// the funnel scans. These are the production composition roots that must mount
// cells via composition.NewForRole, never bare composition.New. cellmodules/ and
// corecells/ are NOT scanned: they provide modules, they do not assemble the app.
var compositionNewForRoleWiringRoots = []string{"cmd", "examples"}

// CheckCompositionNewForRoleFunnel01 scans the wiring-layer roots for direct
// calls to composition.New and returns one Diagnostic per offending callsite. It
// is the single rule body — the Test* dogfoods it via Report — so the exact scan
// is the one enforced.
func CheckCompositionNewForRoleFunnel01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, compositionNewForRoleWiringRoots).Files()
	if err != nil {
		t.Fatalf("COMPOSITION-NEWFORROLE-FUNNEL-01: scanner.DirsScope: %v", err)
	}

	var diags []Diagnostic
	for _, path := range files {
		rel := funnelRelSlash(root, path)

		line, ok, perr := firstQualifiedSelectorLine(path, runtimeCompositionModule, "composition", "New")
		if perr != nil {
			t.Fatalf("COMPOSITION-NEWFORROLE-FUNNEL-01: parse %s: %v", rel, perr)
		}
		if ok {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: "composition.New in a wiring-layer package; mount cells via " +
					"composition.NewForRole so the deployment role's colocated subset is selected " +
					"(COMPOSITION-NEWFORROLE-FUNNEL-01)",
			})
		}
	}
	return diags
}
