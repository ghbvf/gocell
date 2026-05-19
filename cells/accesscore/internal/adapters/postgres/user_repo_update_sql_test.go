package postgres

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestUpdateUserSQL_IncludesLockoutColumns is the PR #585 review P1#3 RED:
// authzmutate.Mutator.ApplyInTx → repo.Update is the production write path
// for the ActivateUser mutation, which calls domain.User.ResetFailedLogins()
// in its apply() (mutation.go:138). Without these columns in the UPDATE,
// the in-memory zeroing never reaches PG: an admin Unlock and the
// TryLazyUnlock lazy-unlock path both leave the stored counter and
// locked_until at their pre-unlock values, so the next failed login can
// re-trigger an immediate auto-lock or otherwise read stale state.
//
// Test seam: assert the constant SQL string literal lists each of the three
// columns mutated by ResetFailedLogins. Mirrors the existing pattern
// elsewhere where SQL invariants are asserted at the source level (e.g.
// FK lookups in schema_guard.go). A full PG behavioral test lives in
// tests/integration/l2atomicity/journey_accountlockout_e2e_test.go.
func TestUpdateUserSQL_IncludesLockoutColumns(t *testing.T) {
	cases := []string{"failed_login_count", "last_failed_at", "locked_until"}
	for _, col := range cases {
		t.Run(col, func(t *testing.T) {
			assert.Contains(t, updateUserSQL, col,
				"updateUserSQL must SET %s so that authzmutate.ActivateUser → "+
					"repo.Update persists the ResetFailedLogins() zeroing "+
					"(PR #585 review P1#3)", col)
		})
	}

	// Sanity: the placeholders count must match the columns. The SET clause
	// has 8 mutable columns + WHERE id=$1, so we expect $1..$8 minimum.
	// This is a smoke check; the integration test exercises the live path.
	assert.True(t, strings.Contains(updateUserSQL, "$8") ||
		strings.Contains(updateUserSQL, "$9") ||
		strings.Contains(updateUserSQL, "$10") ||
		strings.Contains(updateUserSQL, "$11"),
		"updateUserSQL must have positional params covering the new lockout columns")
}
