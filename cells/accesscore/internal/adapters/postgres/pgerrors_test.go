package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

// TestIsUniqueViolation covers nil, non-pgErr, wrong-code, match, and wrapped cases
// for the cell-private isUniqueViolation helper.
func TestIsUniqueViolation(t *testing.T) {
	uniquePgErr := &pgconn.PgError{Code: sqlStateUniqueViolation}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: sqlStateForeignKeyViolation}, want: false},
		{name: "pgError match", err: uniquePgErr, want: true},
		{name: "wrapped pgError match", err: fmt.Errorf("repo: %w", uniquePgErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isUniqueViolation(tt.err))
		})
	}
}

// TestIsForeignKeyViolation covers nil, non-pgErr, wrong-code, match, and wrapped cases
// for the cell-private isForeignKeyViolation helper.
func TestIsForeignKeyViolation(t *testing.T) {
	fkPgErr := &pgconn.PgError{Code: sqlStateForeignKeyViolation}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: sqlStateUniqueViolation}, want: false},
		{name: "pgError match", err: fkPgErr, want: true},
		{name: "wrapped pgError match", err: fmt.Errorf("repo: %w", fkPgErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isForeignKeyViolation(tt.err))
		})
	}
}

// TestIsLastAdminProtected covers nil, non-pgErr, wrong-code, P0001-no-sentinel,
// P0001-with-sentinel (match), and wrapped match cases for the cell-private helper.
func TestIsLastAdminProtected(t *testing.T) {
	matchErr := &pgconn.PgError{
		Code:    sqlStateRaiseException,
		Message: "last_admin_protected: cannot remove the last admin",
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: sqlStateUniqueViolation}, want: false},
		{name: "P0001 but no sentinel", err: &pgconn.PgError{Code: sqlStateRaiseException, Message: "some other raise"}, want: false},
		{name: "P0001 with sentinel", err: matchErr, want: true},
		{name: "wrapped match", err: fmt.Errorf("repo: %w", matchErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isLastAdminProtected(tt.err))
		})
	}
}
