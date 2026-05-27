package pgrepoapproved_test

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
)

// TestApprove_MintsApproval documents that Approve is a runtime no-op token
// minter: it accepts any string reason and returns a non-nil sealed Approval.
// All meaningful enforcement is performed statically by archtest
// PG-REPO-AMBIENT-TX-01 R3 via *types.Info resolution — Approve's only runtime
// responsibility is to be a valid call expression that compiles and yields the
// token pgexec.ExecDirect consumes. This test exists to (1) document the
// contract, (2) keep the package non-empty for coverage tooling, and (3) catch
// a future regression where someone accidentally adds runtime behavior
// (logging, side effects, panic) that would change call-site cost expectations.
func TestApprove_MintsApproval(t *testing.T) {
	// At runtime Approve is a pure token minter — any string is accepted without
	// inspection. The static reason constraints (BasicLit + kebab regex +
	// placeholder ban) live in the archtest layer (PG-REPO-AMBIENT-TX-01 R3),
	// not here. The values below are all valid kebab-case literals so this test
	// documents only the runtime contract.
	cases := []string{
		"revoke-session-cascade",
		"some-other-future-reason",
	}
	for _, reason := range cases {
		if got := pgrepoapproved.Approve(reason); got == nil {
			t.Fatalf("Approve(%q) returned nil Approval", reason)
		}
	}
}
