// Package pgrepoapproved provides the proof-of-authorization token that
// pgexec.ExecDirect requires as its first argument. ExecDirect bypasses the
// ambient transaction (independent-commit cascade/compensation paths), so
// every callsite must be explicitly ADR-approved.
//
// The approval is bound to the call argument, and the reason is a typed
// constant from the catalog declared in this package — not an arbitrary
// kebab-case string. This is the Hard 范本 #3 (string-typed concept funnel)
// upgrade on top of #2 (typed marker funnel for unbounded ops): the
// ApprovalReason newtype + closed set of constants makes the approved-reason
// set semantically inspectable, not merely formally well-shaped.
//
//	pgexec.ExecDirect(pgrepoapproved.Approve(pgrepoapproved.RevokeSessionCascade),
//	    s.db, cascadeCtx, revokeSessionSQL, revokedAt, sessionID)
//
// "Call without approval" = compile error (the first ExecDirect parameter is
// typed Approval). "Approval without a call" = orphan dead code, statically
// detected by archtest PG-REPO-AMBIENT-TX-01 R4. "Approval from a forged
// reason" = compile error if the caller cannot reach an ApprovalReason value:
// the constants are the only stable instances of ApprovalReason and the
// archtest R3 gate rejects identifiers / selectors that do not resolve to a
// *types.Const declared inside this package with type ApprovalReason.
//
// To add a new approved ExecDirect callsite:
//
//  1. Open the ADR that motivates the bypass (typically a session, audit, or
//     compensation flow) and add a section explaining why the ambient
//     transaction must not envelop the write.
//  2. Add a new constant to ApprovalReason below — kebab-case identifier in
//     the constant value documents the rationale. The Go identifier name
//     captures the high-level intent (e.g. RevokeSessionCascade).
//  3. Pass the constant inline at the callsite: pgexec.ExecDirect(
//     pgrepoapproved.Approve(pgrepoapproved.MyNewReason), ...).
//  4. Run archtest TestPGRepoAmbientTx + TestPGRepoApprovedSealed to confirm
//     the funnel still holds.
//
// archtest PG-REPO-AMBIENT-TX-01 R3 statically enforces that arg[0] of every
// pgexec.ExecDirect callsite is an inline CallExpr to pgrepoapproved.Approve
// whose own arg[0] is an identifier or selector resolving to a *types.Const
// declared in this package with type ApprovalReason. R4 statically enforces
// that every pgrepoapproved.Approve callsite appears exactly as arg[0] of a
// pgexec.ExecDirect call (no orphan approvals).
package pgrepoapproved

// Approval is the sealed proof token for a single pgexec.ExecDirect callsite.
// It is a sealed interface: only the unexported approval type (minted by
// Approve) implements it, so callers cannot forge one via a composite literal
// or a parallel implementation. The value carries no runtime data — ExecDirect
// ignores it; it exists solely to make the bypass authorization an inseparable
// part of the call expression that archtest can statically verify.
type Approval interface{ approvedExecDirect() }

type approval struct{}

func (approval) approvedExecDirect() {}

// ApprovalReason is the sealed-by-convention catalog type for the set of
// ADR-approved ExecDirect bypass reasons. Although the underlying type is
// string and Go allows external packages to convert untyped strings to
// ApprovalReason via type conversion (ApprovalReason("...")), archtest
// PG-REPO-AMBIENT-TX-01 R3 statically rejects any Approve(arg) where arg does
// not resolve to a *types.Const declared in THIS package with type
// ApprovalReason. The combined effect is: the only reachable ApprovalReason
// values are the constants below.
type ApprovalReason string

// Approved-reason catalog. Each constant maps 1:1 to an ADR-documented
// ExecDirect callsite. To add a new reason, see the package godoc workflow.
const (
	// RevokeSessionCascade authorizes the independent-commit cascade revoke
	// of a session row after a refresh-token reuse / replay detection. See
	// adapters/postgres/refresh_store.go::revokeSessionDetachedAt and ADR
	// docs/architecture/202605051800-adr-refresh-store-ambient-tx-and-idle-grace.md.
	RevokeSessionCascade ApprovalReason = "revoke-session-cascade"

	// IntegrationTestDeleteRoleAssignment authorizes raw DELETE against the
	// role_assignments table from integration tests that exercise the
	// DB-level last-admin trigger. The ambient-tx-aware repo path is
	// intentionally bypassed so the test drives the trigger directly. See
	// cells/accesscore/internal/adapters/postgres/role_repo_integration_test.go.
	IntegrationTestDeleteRoleAssignment ApprovalReason = "integration-test-delete-role-assignment"

	// IntegrationTestLockUser authorizes raw UPDATE users SET status='locked'
	// from integration tests that exercise the effective_admin BEFORE UPDATE
	// trigger and related cascade paths. See
	// cells/accesscore/internal/adapters/postgres/role_repo_integration_test.go.
	IntegrationTestLockUser ApprovalReason = "integration-test-lock-user"

	// IntegrationTestDeleteUser authorizes raw DELETE from the users table
	// from integration tests that need to remove fixture rows outside the
	// repo's normal flow. See
	// cells/accesscore/internal/adapters/postgres/role_repo_integration_test.go.
	IntegrationTestDeleteUser ApprovalReason = "integration-test-delete-user"
)

// Approve mints an Approval for the pgexec.ExecDirect callsite it is passed
// to. reason MUST be a constant declared in this package (see the catalog
// above) — archtest PG-REPO-AMBIENT-TX-01 R3 statically rejects any other
// form (local consts, type conversions of untyped strings, runtime
// expressions). The reason value itself is ignored at runtime; it serves as
// the source-level binding to the ADR rationale.
func Approve(reason ApprovalReason) Approval {
	_ = reason
	return approval{}
}
