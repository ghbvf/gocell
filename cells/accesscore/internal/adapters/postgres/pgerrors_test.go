package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	pgquery "github.com/ghbvf/gocell/pkg/pgquery"
)

// TestIsUniqueViolation covers nil, non-pgErr, wrong-code, match, and wrapped cases
// for the shared pgquery.IsUniqueViolation classifier (single source of truth).
func TestIsUniqueViolation(t *testing.T) {
	uniquePgErr := &pgconn.PgError{Code: pgquery.SQLStateUniqueViolation}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: pgquery.SQLStateForeignKeyViolation}, want: false},
		{name: "pgError match", err: uniquePgErr, want: true},
		{name: "wrapped pgError match", err: fmt.Errorf("repo: %w", uniquePgErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pgquery.IsUniqueViolation(tt.err))
		})
	}
}

// TestIsForeignKeyViolation covers nil, non-pgErr, wrong-code, match, and wrapped cases
// for the shared pgquery.IsForeignKeyViolation classifier.
func TestIsForeignKeyViolation(t *testing.T) {
	fkPgErr := &pgconn.PgError{Code: pgquery.SQLStateForeignKeyViolation}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: pgquery.SQLStateUniqueViolation}, want: false},
		{name: "pgError match", err: fkPgErr, want: true},
		{name: "wrapped pgError match", err: fmt.Errorf("repo: %w", fkPgErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pgquery.IsForeignKeyViolation(tt.err))
		})
	}
}

// TestIsLastAdminProtected covers nil, non-pgErr, wrong-code, P0001-no-sentinel,
// P0001-with-sentinel (match), and wrapped match cases for the shared
// pgquery.IsLastAdminProtected classifier.
func TestIsLastAdminProtected(t *testing.T) {
	matchErr := &pgconn.PgError{
		Code:    pgquery.SQLStateRaiseException,
		Message: "effective_admin_invariant: would leave the system with no effective admin",
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: pgquery.SQLStateUniqueViolation}, want: false},
		{name: "P0001 but no sentinel", err: &pgconn.PgError{Code: pgquery.SQLStateRaiseException, Message: "some other raise"}, want: false},
		{name: "P0001 with sentinel", err: matchErr, want: true},
		{name: "wrapped match", err: fmt.Errorf("repo: %w", matchErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pgquery.IsLastAdminProtected(tt.err))
		})
	}
}
