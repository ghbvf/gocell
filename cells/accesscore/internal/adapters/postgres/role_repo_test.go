package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	pgquery "github.com/ghbvf/gocell/pkg/pgquery"
)

// TestRoleCreateError_UniqueViolation asserts that a 23505 PG error is
// classified as KindConflict / ErrAuthRoleDuplicate, not ErrInternal.
// Mirrors session_store.go TestSessionCreateError pattern.
func TestRoleCreateError_UniqueViolation(t *testing.T) {
	uniqueErr := &pgconn.PgError{Code: pgquery.SQLStateUniqueViolation}

	err := roleCreateError(uniqueErr, "admin")
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "must be *errcode.Error, got %T: %v", err, err)
	assert.Equal(t, errcode.KindConflict, ec.Kind, "unique violation must map to KindConflict")
	assert.Equal(t, errcode.ErrAuthRoleDuplicate, ec.Code, "unique violation must use ErrAuthRoleDuplicate")
}

// TestRoleCreateError_InfraError asserts that a non-constraint error remains
// classified as KindInternal (infra errors must not be mis-classified).
func TestRoleCreateError_InfraError(t *testing.T) {
	infraErr := errors.New("connection reset by peer")

	err := roleCreateError(infraErr, "admin")
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind, "infra error must remain KindInternal")
	assert.Equal(t, errcode.ErrInternal, ec.Code)
}
