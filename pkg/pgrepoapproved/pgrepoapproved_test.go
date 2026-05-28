package pgrepoapproved_test

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
)

// TestApprove_MintsApproval documents that Approve is a runtime no-op token
// minter: it accepts an ApprovalReason from the package catalog and returns a
// non-nil sealed Approval. All meaningful enforcement is performed statically
// by archtest PG-REPO-AMBIENT-TX-01 R3 (callsite gate) and R4 (orphan ban).
// This test exists to (1) document the runtime contract, (2) keep the package
// non-empty for coverage tooling, and (3) catch a future regression where
// someone accidentally adds runtime behavior (logging, side effects, panic)
// that would change call-site cost expectations.
func TestApprove_MintsApproval(t *testing.T) {
	cases := []pgrepoapproved.ApprovalReason{
		pgrepoapproved.RevokeSessionCascade,
		pgrepoapproved.IntegrationTestDeleteRoleAssignment,
		pgrepoapproved.IntegrationTestLockUser,
		pgrepoapproved.IntegrationTestDeleteUser,
	}
	for _, reason := range cases {
		if got := pgrepoapproved.Approve(reason); got == nil {
			t.Fatalf("Approve(%q) returned nil Approval", reason)
		}
	}
}
