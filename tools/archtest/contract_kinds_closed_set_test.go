// CONTRACT-KINDS-CLOSED-SET-01
//
// Invariant: all contract kinds in the project must be members of the closed
// set {"http", "event", "command", "projection"}. Any new kind introduced
// without updating this gate signals an unreviewed extension and requires
// architecture approval.
//
// No allowlist needed: this gate is always-on and should never fail for
// known-good kinds. It exists to fail loudly when a future maintainer
// accidentally introduces a typo or an uncategorised kind.
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#05a K#PR4 W3
package archtest

import (
	"sort"
	"testing"
)

// closedSetContractKinds is the exhaustive list of allowed contract kinds.
// Additions require architecture approval and a corresponding generator/template.
var closedSetContractKinds = map[string]bool{
	"http":       true,
	"event":      true,
	"command":    true,
	"projection": true,
}

// TestCONTRACT_KINDS_CLOSED_SET_01 verifies that every contract.yaml in the
// project uses a kind from closedSetContractKinds.
func TestCONTRACT_KINDS_CLOSED_SET_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	seen := make(map[string]bool)
	var violations []string
	for _, contract := range project.Contracts {
		kind := contract.Kind
		if closedSetContractKinds[kind] {
			seen[kind] = true
			continue
		}
		violations = append(violations, "contract "+contract.ID+": unknown kind \""+kind+
			"\" (allowed: http | event | command | projection)")
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("CONTRACT-KINDS-CLOSED-SET-01: %s", v)
	}

	if t.Failed() {
		return
	}

	// Report which known kinds are actually in use (informational, not a failure).
	var kinds []string
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	t.Logf("CONTRACT-KINDS-CLOSED-SET-01: active kinds in project: %v", kinds)
}
