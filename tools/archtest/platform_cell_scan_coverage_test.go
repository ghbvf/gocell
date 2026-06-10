//go:build archtest

// INVARIANT: PLATFORM-CELL-SCAN-COVERAGE-01
//
// Keystone anti-vacuity guard for the #1560 go.work P5 split. When the platform
// cells moved out of the repo-root cells/ into the dedicated corecells module,
// every discovery mechanism anchored on the literal "cells/" path that fail-opens
// on a missing/empty target silently stopped covering corecells WITHOUT erroring.
// This rule makes that fail-open class a loud CI failure instead of a review miss:
//
//	① the corecells module is a go.work workspace member (the production-scan /
//	   classification module set [findWorkspaceModules] feeds every workspace-aware
//	   cell rule, so a missing `use ./corecells` would drop corecells from all of
//	   them — proven present here);
//	② the on-disk platform-cell scan-root set [platformCellScanDirs] is non-empty
//	   (a relayout that points it at an empty/absent dir turns CI red HERE, not
//	   silently in each downstream DirsScope rule);
//	③ every metadata-discovered platform-cell production file lives under that
//	   scan-root set (proves the DirsScope cell rules actually reach corecells).
//
// AI-robust rating: Medium (runtime set-comparison; Hard is unreachable for a
// scan-coverage property). The TestPlatformCellScanCoverage01_AntiVacuity
// sub-test proves assertion ② is dir-sensitive (a bogus root scans zero files),
// so a future always-nonempty global scan cannot vacuously satisfy it.
package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPlatformCellScanCoverage01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// ① corecells is a workspace member.
	var hasCorecells bool
	for _, m := range findWorkspaceModules(t, root) {
		if m.ImportPath == PlatformCellsModulePath {
			hasCorecells = true
			break
		}
	}
	if !hasCorecells {
		t.Fatalf("workspace module set missing %q — go.work `use ./%s` and the "+
			".gocell/manifest.yaml corecells module entry must land in the same change",
			PlatformCellsModulePath, PlatformCellsDir)
	}

	// ② anti-vacuity: the on-disk scan-root set resolves to real files.
	scanRootFiles, err := DirsScope(root, platformCellScanDirs()).Files()
	if err != nil {
		t.Fatalf("scan platformCellScanDirs()=%v: %v", platformCellScanDirs(), err)
	}
	if len(scanRootFiles) == 0 {
		t.Fatalf("platformCellScanDirs()=%v scanned zero .go files — the platform-cell "+
			"scan root drifted from the on-disk %s module", platformCellScanDirs(), PlatformCellsDir)
	}
	scanSet := make(map[string]struct{}, len(scanRootFiles))
	for _, f := range scanRootFiles {
		scanSet[f] = struct{}{}
	}

	// ③ every platform-cell production file (metadata-discovered) is in the scan set.
	cellFiles, err := findCellProductionGoFiles(root)
	if err != nil {
		t.Fatalf("findCellProductionGoFiles: %v", err)
	}
	platformPrefix := PlatformCellsDir + "/"
	var platformCellFiles int
	for _, f := range cellFiles {
		rel, relErr := filepath.Rel(root, f)
		if relErr != nil {
			t.Fatalf("filepath.Rel(%s, %s): %v", root, f, relErr)
		}
		if !strings.HasPrefix(filepath.ToSlash(rel), platformPrefix) {
			continue // example-local cell file; covered via the examples/ scan root elsewhere
		}
		platformCellFiles++
		if _, ok := scanSet[f]; !ok {
			t.Errorf("platform-cell production file not covered by platformCellScanDirs(): %s", rel)
		}
	}
	if platformCellFiles == 0 {
		t.Fatalf("no platform-cell production files discovered under %s/ — assertion ③ is "+
			"vacuous (metadata locator no longer reaches the corecells module)", PlatformCellsDir)
	}
}

// TestPlatformCellScanCoverage01_AntiVacuity proves the assertion-② non-empty
// check is genuinely dir-sensitive: a scan root that does not exist matches zero
// files (or errors). If a bogus root ever returned files, ②'s non-emptiness would
// be vacuous and corecells drift would pass silently.
func TestPlatformCellScanCoverage01_AntiVacuity(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// Case A: non-existent dir — DirsScope must return zero files (or an error).
	files, err := DirsScope(root, []string{"corecells-does-not-exist-xyz"}).Files()
	if err != nil {
		// Erroring on a missing root also proves dir-sensitivity.
	} else if len(files) != 0 {
		t.Fatalf("non-existent scan root matched %d files; assertion ② would be vacuous", len(files))
	}

	// Case B: existing but EMPTY dir (no .go files) — DirsScope must return zero
	// files. This rules out a global-scan fallback that would satisfy the
	// non-emptiness check regardless of which dir is targeted.
	emptyDir := t.TempDir() // guaranteed to exist and have no .go files
	// DirsScope takes a parent root + relative dir names; use the temp dir as
	// root with "." as the only dir so it scans only the (empty) temp dir.
	files2, err2 := DirsScope(emptyDir, []string{"."}).Files()
	if err2 != nil {
		// An error is acceptable — it also proves the scan is not global.
		return
	}
	if len(files2) != 0 {
		t.Fatalf("empty existing dir matched %d files; DirsScope must not fall back to a global scan", len(files2))
	}
}
