package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestErrorCodes_Prefix(t *testing.T) {
	codes := []errcode.Code{
		ErrAdapterPGConnect,
		ErrAdapterPGQuery,
		ErrAdapterPGMigrate,
		ErrAdapterPGNoTx,
		ErrAdapterPGMarshal,
		ErrAdapterPGPublish,
		ErrAdapterPGSchemaMismatch,
		ErrAdapterPGSchemaShape,
		ErrAdapterPGInvalidIndex,
	}

	for _, c := range codes {
		assert.Contains(t, string(c), "ERR_ADAPTER_PG_",
			"error code %s must use ERR_ADAPTER_PG_ prefix", c)
	}
}

func TestErrorCodes_Unique(t *testing.T) {
	codes := []errcode.Code{
		ErrAdapterPGConnect,
		ErrAdapterPGQuery,
		ErrAdapterPGMigrate,
		ErrAdapterPGNoTx,
		ErrAdapterPGMarshal,
		ErrAdapterPGPublish,
		ErrAdapterPGSchemaMismatch,
		ErrAdapterPGSchemaShape,
		ErrAdapterPGInvalidIndex,
	}

	seen := make(map[errcode.Code]bool, len(codes))
	for _, c := range codes {
		assert.False(t, seen[c], "duplicate error code: %s", c)
		seen[c] = true
	}
}

func TestErrorCodes_CanCreateErrors(t *testing.T) {
	err := errcode.New(errcode.KindInternal, ErrAdapterPGConnect, "connection failed")
	assert.Equal(t, ErrAdapterPGConnect, err.Code)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_PG_CONNECT")
}

// TestIsUniqueViolation covers nil, non-pgErr, wrong-code, match, and wrapped cases.
func TestIsUniqueViolation(t *testing.T) {
	uniquePgErr := &pgconn.PgError{Code: SQLStateUniqueViolation}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: "23503"}, want: false},
		{name: "pgError match", err: uniquePgErr, want: true},
		{name: "wrapped pgError match", err: fmt.Errorf("repo: %w", uniquePgErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsUniqueViolation(tt.err))
		})
	}
}

// TestIsForeignKeyViolation covers nil, non-pgErr, wrong-code, match, and wrapped cases.
func TestIsForeignKeyViolation(t *testing.T) {
	fkPgErr := &pgconn.PgError{Code: SQLStateForeignKeyViolation}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: "23505"}, want: false},
		{name: "pgError match", err: fkPgErr, want: true},
		{name: "wrapped pgError match", err: fmt.Errorf("repo: %w", fkPgErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsForeignKeyViolation(tt.err))
		})
	}
}

// TestIsLastAdminProtected covers nil, non-pgErr, wrong-code, P0001-no-sentinel,
// P0001-with-sentinel (match), and wrapped match cases.
func TestIsLastAdminProtected(t *testing.T) {
	matchErr := &pgconn.PgError{
		Code:    SQLStateRaiseException,
		Message: "last_admin_protected: cannot remove the last admin",
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "pgError wrong code", err: &pgconn.PgError{Code: "23505"}, want: false},
		{name: "P0001 but no sentinel", err: &pgconn.PgError{Code: SQLStateRaiseException, Message: "some other raise"}, want: false},
		{name: "P0001 with sentinel", err: matchErr, want: true},
		{name: "wrapped match", err: fmt.Errorf("repo: %w", matchErr), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsLastAdminProtected(tt.err))
		})
	}
}
