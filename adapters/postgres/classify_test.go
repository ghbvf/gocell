package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestIsRetryablePGError covers all SQLSTATE branches for isRetryablePGError.
func TestIsRetryablePGError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "serialization_failure 40001",
			err:  &pgconn.PgError{Code: "40001"},
			want: true,
		},
		{
			name: "deadlock_detected 40P01",
			err:  &pgconn.PgError{Code: "40P01"},
			want: true,
		},
		{
			name: "connection_failure 08006",
			err:  &pgconn.PgError{Code: "08006"},
			want: true,
		},
		{
			name: "connection_does_not_exist 08003",
			err:  &pgconn.PgError{Code: "08003"},
			want: true,
		},
		{
			name: "connection_exception 08000",
			err:  &pgconn.PgError{Code: "08000"},
			want: true,
		},
		{
			name: "protocol_violation 08P01",
			err:  &pgconn.PgError{Code: "08P01"},
			want: true,
		},
		{
			name: "unique_violation 23505 — permanent",
			err:  &pgconn.PgError{Code: "23505"},
			want: false,
		},
		{
			name: "invalid_password 28P01 — permanent",
			err:  &pgconn.PgError{Code: "28P01"},
			want: false,
		},
		{
			name: "invalid_catalog_name 3D000 — permanent",
			err:  &pgconn.PgError{Code: "3D000"},
			want: false,
		},
		{
			name: "context.DeadlineExceeded",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "context.Canceled — NOT transient",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("unknown"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryablePGError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestClassifyPGError verifies that classifyPGError routes transient errors
// through errcode.WrapInfra (IsTransient == true) and permanent errors through
// errcode.Wrap (IsTransient == false).
func TestClassifyPGError(t *testing.T) {
	const opCode = errcode.Code("ERR_ADAPTER_PG_QUERY")
	const opMsg = "op"

	tests := []struct {
		name          string
		err           error
		wantTransient bool
	}{
		{
			name:          "serialization_failure 40001 → transient",
			err:           &pgconn.PgError{Code: "40001"},
			wantTransient: true,
		},
		{
			name:          "deadlock_detected 40P01 → transient",
			err:           &pgconn.PgError{Code: "40P01"},
			wantTransient: true,
		},
		{
			name:          "connection_failure 08006 → transient",
			err:           &pgconn.PgError{Code: "08006"},
			wantTransient: true,
		},
		{
			name:          "connection_does_not_exist 08003 → transient",
			err:           &pgconn.PgError{Code: "08003"},
			wantTransient: true,
		},
		{
			name:          "unique_violation 23505 → permanent",
			err:           &pgconn.PgError{Code: "23505"},
			wantTransient: false,
		},
		{
			name:          "invalid_password 28P01 → permanent",
			err:           &pgconn.PgError{Code: "28P01"},
			wantTransient: false,
		},
		{
			name:          "invalid_catalog_name 3D000 → permanent",
			err:           &pgconn.PgError{Code: "3D000"},
			wantTransient: false,
		},
		{
			name:          "context.DeadlineExceeded → transient",
			err:           context.DeadlineExceeded,
			wantTransient: true,
		},
		{
			name:          "context.Canceled → permanent",
			err:           context.Canceled,
			wantTransient: false,
		},
		{
			name:          "plain error → permanent",
			err:           errors.New("unknown error"),
			wantTransient: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPGError(tt.err, opCode, opMsg)
			assert.Equal(t, tt.wantTransient, errcode.IsTransient(got),
				"IsTransient mismatch for %q", tt.name)
		})
	}
}
