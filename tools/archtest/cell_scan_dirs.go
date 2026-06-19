package archtest

// cell_scan_dirs.go — single source for the repo-relative on-disk scan roots
// that hold cell source, consumed by every archtest DirsScope that walks cell
// files. Before #1560 these were the bare literal []string{"cells"} /
// []string{"cells", "examples"} scattered across rule files; after the go.work
// P5 split the platform cells moved to the dedicated corecells module, so a
// literal "cells" root silently scans an empty/absent dir (fail-open). Routing
// every callsite through these helpers — derived from [PlatformCellsDir] — means
// a relayout touches one place, and PLATFORM-CELL-SCAN-COVERAGE-01 proves no
// production platform-cell package escapes the scan set.

// platformCellScanDirsDefault is the package-private single source for the
// platform-cell scan directories, consumed by both the helper functions below
// and by [RuntimeScopeConfig.PlatformCellScanDirs] (#2329).
func platformCellScanDirsDefault() []string { return []string{PlatformCellsDir} }

// platformCellScanDirs returns the on-disk scan root(s) holding GoCell's own
// platform cells. Since #1560 that is the corecells module dir ([PlatformCellsDir]);
// the repo root no longer has a cells/ directory.
func platformCellScanDirs() []string {
	return platformCellScanDirsDefault()
}

// platformAndExampleCellScanDirs returns the platform-cell scan root plus the
// examples/ subtree, for rules that also walk example cells (kept under
// examples/<id>/cells/ on disk). Single source for the recurring
// DirsScope(root, []string{"cells", "examples"}) pattern.
func platformAndExampleCellScanDirs() []string {
	return append(platformCellScanDirsDefault(), "examples")
}
