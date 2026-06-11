package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

// TestIsLastAdminProtected_UnitCoverage covers the P2-3 colon-delimited match
// precision requirement: a sibling prefix "effective_admin_invariant_v2: ..."
// must return false (regression case that would have been true under the old
// bare-prefix match).
func TestIsLastAdminProtected_UnitCoverage(t *testing.T) {
	// Real trigger message from migrations/024_effective_admin_invariant.sql.
	realTriggerMsg := lastAdminTriggerSentinel + ": would leave the system with no effective admin"

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "non-pg plain error",
			err:  errors.New("some plain error"),
			want: false,
		},
		{
			name: "PG 23505 unique violation (not P0001)",
			err:  &pgconn.PgError{Code: "23505", Message: "duplicate key"},
			want: false,
		},
		{
			name: "P0001 with unrelated message",
			err:  &pgconn.PgError{Code: "P0001", Message: "some other raise exception"},
			want: false,
		},
		{
			name: "P0001 with sentinel but no colon (P2-3: must not match bare prefix)",
			// "effective_admin_invariant" with NO colon — the trigger always
			// emits a colon; this guards against a hypothetical bare-sentinel
			// message that would have matched the old strings.HasPrefix(msg, sentinel).
			err:  &pgconn.PgError{Code: "P0001", Message: lastAdminTriggerSentinel},
			want: false,
		},
		{
			name: "P0001 with sibling-prefix effective_admin_invariant_v2 (P2-3 regression case)",
			// This is the key P2-3 negative case: the old bare-prefix
			// strings.HasPrefix(msg, "effective_admin_invariant") would return
			// true for a hypothetical v2 trigger. The colon-delimited match
			// must return false.
			err:  &pgconn.PgError{Code: "P0001", Message: "effective_admin_invariant_v2: some future trigger"},
			want: false,
		},
		{
			name: "P0001 with real trigger message (exact match)",
			err:  &pgconn.PgError{Code: "P0001", Message: realTriggerMsg},
			want: true,
		},
		{
			name: "P0001 wrapped in fmt.Errorf chain",
			err:  fmt.Errorf("ctx: %w", &pgconn.PgError{Code: "P0001", Message: realTriggerMsg}),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isLastAdminProtected(tt.err))
		})
	}
}
