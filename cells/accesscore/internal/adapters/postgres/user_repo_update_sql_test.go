package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestUpdateLockStateSQL_ResetsLockoutColumnsOnActive verifies the CASE-based
// atomic reset: activating a user must zero failed_login_count / last_failed_at
// / locked_until in the same statement, closing the PR #585 P1#3 race that the
// old generic Update path exhibited (authzmutate.ActivateUser called
// domain.User.ResetFailedLogins in memory; the UPDATE then had to include those
// columns explicitly, which was fragile). The new SQL uses a CASE on the bound
// status value so "activate without lockout reset" is not expressible at the
// call site.
func TestUpdateLockStateSQL_ResetsLockoutColumnsOnActive(t *testing.T) {
	cases := []string{"failed_login_count", "last_failed_at", "locked_until"}
	for _, col := range cases {
		t.Run(col, func(t *testing.T) {
			assert.Contains(t, updateLockStateSQL, col,
				"updateLockStateSQL must CASE-reset %s when status='active' "+
					"(PR #585 review P1#3 — atomic lockout-reset invariant)", col)
		})
	}
	assert.Contains(t, updateLockStateSQL, "CASE WHEN $2 = 'active'",
		"updateLockStateSQL must use CASE WHEN $2 = 'active' for conditional reset")
}

// TestUpdateProfileSQL_ReturnsStar verifies that the RETURNING clause is
// present so UpdateProfile can reconstitute the post-write aggregate in one
// round-trip without a follow-up SELECT.
func TestUpdateProfileSQL_ReturnsStar(t *testing.T) {
	assert.Contains(t, updateProfileSQL, "RETURNING",
		"updateProfileSQL must include a RETURNING clause for single-round-trip reconstitution")
	assert.Contains(t, updateProfileSQL, "COALESCE($2, username)",
		"updateProfileSQL must use COALESCE for PATCH semantics on username")
	assert.Contains(t, updateProfileSQL, "COALESCE($3, email)",
		"updateProfileSQL must use COALESCE for PATCH semantics on email")
}
