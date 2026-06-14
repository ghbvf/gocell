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

// TestIsRaiseException covers nil, non-pg, wrong-code, match, and wrapped-chain cases.
func TestIsRaiseException(t *testing.T) {
	raiseErr := &pgconn.PgError{Code: "P0001"}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: "23505"}, want: false},
		{name: "pgError match P0001", err: raiseErr, want: true},
		{name: "wrapped pgError match", err: fmt.Errorf("ctx: %w", raiseErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsRaiseException(tt.err))
		})
	}
}

// TestAsRaiseException covers the same cases as TestIsRaiseException plus
// asserts the returned *pgconn.PgError identity on match (callers rely on it
// to read Message without doing a second errors.As walk).
func TestAsRaiseException(t *testing.T) {
	raiseErr := &pgconn.PgError{Code: "P0001", Message: "trigger: rule"}

	tests := []struct {
		name    string
		err     error
		wantOK  bool
		wantPtr *pgconn.PgError
	}{
		{name: "nil error", err: nil, wantOK: false},
		{name: "plain error", err: errors.New("plain"), wantOK: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: "23505"}, wantOK: false},
		{name: "pgError match P0001", err: raiseErr, wantOK: true, wantPtr: raiseErr},
		{name: "wrapped pgError match", err: fmt.Errorf("ctx: %w", raiseErr), wantOK: true, wantPtr: raiseErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := AsRaiseException(tt.err)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Same(t, tt.wantPtr, got)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}
