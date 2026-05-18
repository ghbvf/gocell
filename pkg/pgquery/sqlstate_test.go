package pgquery

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

// TestIsUniqueViolation covers nil, non-pg, wrong-code, match, and wrapped-chain cases.
func TestIsUniqueViolation(t *testing.T) {
	uniqueErr := &pgconn.PgError{Code: "23505"}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: "23503"}, want: false},
		{name: "pgError match 23505", err: uniqueErr, want: true},
		{name: "wrapped pgError match", err: fmt.Errorf("ctx: %w", uniqueErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsUniqueViolation(tt.err))
		})
	}
}

// TestIsForeignKeyViolation covers nil, non-pg, wrong-code, match, and wrapped-chain cases.
func TestIsForeignKeyViolation(t *testing.T) {
	fkErr := &pgconn.PgError{Code: "23503"}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: "23505"}, want: false},
		{name: "pgError match 23503", err: fkErr, want: true},
		{name: "wrapped pgError match", err: fmt.Errorf("ctx: %w", fkErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsForeignKeyViolation(tt.err))
		})
	}
}

// TestIsLastAdminProtected covers nil, non-pg, wrong-code, P0001 without
// sentinel, P0001 with sentinel as prefix (real trigger message), P0001 with
// sentinel appearing only as a substring (not at start — must return false),
// and wrapped-chain cases.
func TestIsLastAdminProtected(t *testing.T) {
	// Real trigger message from migrations/024_effective_admin_invariant.sql.
	matchErr := &pgconn.PgError{
		Code:    "P0001",
		Message: LastAdminTriggerSentinel + ": would leave the system with no effective admin",
	}
	noSentinelErr := &pgconn.PgError{
		Code:    "P0001",
		Message: "some other raise exception",
	}
	// Sentinel present as a substring but NOT at the start — must not match
	// (guards against the false-positive window of a substring scan).
	sentinelNotPrefixErr := &pgconn.PgError{
		Code:    "P0001",
		Message: "unrelated error mentioning " + LastAdminTriggerSentinel + " somewhere",
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code 23505", err: &pgconn.PgError{Code: "23505"}, want: false},
		{name: "P0001 without sentinel", err: noSentinelErr, want: false},
		{name: "P0001 sentinel substring not prefix", err: sentinelNotPrefixErr, want: false},
		{name: "P0001 with sentinel as prefix", err: matchErr, want: true},
		{name: "wrapped P0001 with sentinel", err: fmt.Errorf("ctx: %w", matchErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsLastAdminProtected(tt.err))
		})
	}
}
