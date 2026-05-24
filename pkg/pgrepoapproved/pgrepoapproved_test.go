package pgrepoapproved_test

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
)

// TestApprovedExecDirect_NoOp documents that the marker is a runtime no-op:
// the function accepts any string reason and returns nothing. All meaningful
// enforcement is performed statically by archtest PG-REPO-AMBIENT-TX-01 R3(b)
// via *types.Info resolution — the marker's only runtime responsibility is to
// be a valid call expression that compiles. This test exists to (1) document
// the contract, (2) keep the package non-empty for coverage tooling, and
// (3) catch a future regression where someone accidentally adds runtime
// behavior (logging, side effects, panic) that would change call-site cost
// expectations.
func TestApprovedExecDirect_NoOp(t *testing.T) {
	// At runtime ApprovedExecDirect is a pure no-op — any string is accepted
	// without inspection. The static reason constraints (BasicLit + kebab regex
	// + placeholder ban) live in the archtest layer (PG-REPO-AMBIENT-TX-01 R3(b)),
	// not here. The values below are all valid kebab-case literals so this test
	// documents only the runtime no-op contract.
	cases := []string{
		"revoke-session-cascade",
		"some-other-future-reason",
	}
	for _, reason := range cases {
		pgrepoapproved.ApprovedExecDirect(reason)
	}
}
