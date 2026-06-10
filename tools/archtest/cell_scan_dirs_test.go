// cell_scan_dirs_test.go — anti-vacuity backstop for the cell scan-dir single
// sources (platformCellScanDirs / businessCellScanDirs in
// reverse_coverage_invariants.go).
//
// These helpers are the scan root for several content-scanning invariants:
//   - INVARIANT: EVENT-DTO-CAMELCASE-01        (event_camelcase_invariants_test.go)
//   - INVARIANT: CELLS-NO-ROUTEMUX-WRAPPER-01  (auth_bootstrap_invariants_test.go)
//   - INVARIANT: ADAPTER-RETURNS-DECLARED-TYPES (adapter_returns_declared_types.go)
//   - INVARIANT: GRPC-SERVE-IN-CONTRACT-01     (grpc_service_in_contract_test.go)
//   - INVARIANT: SUBSCRIBE-MARKER-RETIRED-01   (contract_subscribers_funnel_test.go)
//   - INVARIANT: WEBHOOK-MARKER-RETIRED-01     (webhook_yaml_invariants_test.go)
//
// The platform cell module lives under corecells/ (#1560). scanner.DirsScope
// silently skips a missing directory and returns an empty file set, so if a
// future cell-layout rename drops corecells/ (or any cell root) from a helper,
// every consuming rule would scan zero platform-cell files and pass vacuously —
// the exact silent-coverage-loss regression that motivated converging the
// scan roots onto these single sources. This guard fails loudly the moment a
// helper stops resolving to real corecells/ files.

package archtest

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func TestCellScanDirs_CoverCorecells(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	helpers := map[string][]string{
		"platformCellScanDirs": platformCellScanDirs(),
		"businessCellScanDirs": businessCellScanDirs(),
	}
	for name, dirs := range helpers {
		files, err := scanner.DirsScope(root, dirs).Files()
		if err != nil {
			t.Fatalf("%s: DirsScope.Files: %v", name, err)
		}
		var corecells int
		for _, f := range files {
			if strings.Contains(filepath.ToSlash(f), "/corecells/") {
				corecells++
			}
		}
		if corecells == 0 {
			t.Errorf("%s() must resolve to a non-empty corecells/ file set "+
				"(got %d files total, 0 under corecells/): the platform cell module "+
				"was dropped from this scan root, so its consuming rule now passes vacuously",
				name, len(files))
		}
	}
}
