// INVARIANT: CONTRACT-KINDS-CLOSED-SET-01
//
// # CONTRACT-KINDS-CLOSED-SET-01
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

// TestCONTRACT_KINDS_CLOSED_SET_01_NegativeFixture verifies that a project
// containing a contract with kind="workflow" (outside the closed set) would
// be caught by the closed-set checker. This is a unit test against the
// checker logic using a synthetic ProjectMeta — no filesystem contract.yaml.
func TestCONTRACT_KINDS_CLOSED_SET_01_NegativeFixture(t *testing.T) {
	t.Parallel()
	// Build a synthetic project with an unknown kind.
	type fakeContract struct {
		id   string
		kind string
	}
	contracts := []fakeContract{
		{"workflow.device.enroll.v1", "workflow"}, // unknown — must be caught
		{"http.device.list.v1", "http"},           // known — must pass
		{"event.device.enrolled.v1", "event"},     // known — must pass
	}

	var violations []string
	for _, c := range contracts {
		if !closedSetContractKinds[c.kind] {
			violations = append(violations,
				"contract "+c.id+": unknown kind \""+c.kind+"\" (allowed: http | event | command | projection)")
		}
	}

	if len(violations) == 0 {
		t.Errorf("expected at least 1 violation for kind=workflow, got 0")
	}
	// Only the workflow contract should violate.
	if len(violations) != 1 {
		t.Errorf("expected exactly 1 violation, got %d: %v", len(violations), violations)
	}
}
