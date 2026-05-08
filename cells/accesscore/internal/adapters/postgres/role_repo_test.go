package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Constructor validation
// ---------------------------------------------------------------------------

func TestNewPGRoleRepository_RequiresPool(t *testing.T) {
	_, err := NewPGRoleRepository(nil, clock.Real())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "pool must not be nil")
}

func TestNewPGRoleRepository_RequiresClock(t *testing.T) {
	_, err := NewPGRoleRepository(dummyPool(), nil)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "clock must not be nil")
}

func TestNewPGRoleRepository_TypedNilClock(t *testing.T) {
	var clk *typedNilClock
	_, err := NewPGRoleRepository(dummyPool(), clk)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "clock must not be nil")
}

func TestNewPGRoleRepository_HappyPath(t *testing.T) {
	repo, err := NewPGRoleRepository(dummyPool(), clock.Real())
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

func TestPGRoleRepository_LockAdminProvisionRequiresAmbientTx(t *testing.T) {
	repo, err := NewPGRoleRepository(dummyPool(), clock.Real())
	require.NoError(t, err)

	err = repo.LockAdminProvision(context.Background())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrInternal, ec.Code)
	assert.Contains(t, err.Error(), "requires ambient transaction")
}

// ---------------------------------------------------------------------------
// permissionsToJSON / permissionsFromJSON round-trip
// ---------------------------------------------------------------------------

func TestPermissionsRoundTrip_Empty(t *testing.T) {
	wire := permissionsToJSON(nil)
	data, err := json.Marshal(wire)
	require.NoError(t, err)

	result, err := permissionsFromJSON(data)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestPermissionsRoundTrip_NonEmpty(t *testing.T) {
	perms := []domain.Permission{
		{Resource: "users", Action: "read"},
		{Resource: "sessions", Action: "write"},
	}

	wire := permissionsToJSON(perms)
	data, err := json.Marshal(wire)
	require.NoError(t, err)

	result, err := permissionsFromJSON(data)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "users", result[0].Resource)
	assert.Equal(t, "read", result[0].Action)
	assert.Equal(t, "sessions", result[1].Resource)
	assert.Equal(t, "write", result[1].Action)
}

func TestPermissionsFromJSON_Null(t *testing.T) {
	result, err := permissionsFromJSON([]byte("null"))
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestPermissionsFromJSON_EmptyArray(t *testing.T) {
	result, err := permissionsFromJSON([]byte("[]"))
	require.NoError(t, err)
	assert.Empty(t, result)
}

// ---------------------------------------------------------------------------
// compareRoleField / roleFieldValue
// ---------------------------------------------------------------------------

func TestCompareRoleField_ByName(t *testing.T) {
	a := &domain.Role{ID: "1", Name: "admin"}
	b := &domain.Role{ID: "2", Name: "viewer"}

	assert.Negative(t, compareRoleField(a, b, "name"))
	assert.Positive(t, compareRoleField(b, a, "name"))
	assert.Zero(t, compareRoleField(a, a, "name"))
}

func TestCompareRoleField_ByID(t *testing.T) {
	a := &domain.Role{ID: "a", Name: "first"}
	b := &domain.Role{ID: "b", Name: "second"}

	assert.Negative(t, compareRoleField(a, b, "id"))
	assert.Positive(t, compareRoleField(b, a, "id"))
}

func TestCompareRoleField_UnknownField(t *testing.T) {
	a := &domain.Role{ID: "1", Name: "admin"}
	b := &domain.Role{ID: "2", Name: "viewer"}
	assert.Zero(t, compareRoleField(a, b, "unknown"))
}

func TestRoleFieldValue(t *testing.T) {
	r := &domain.Role{ID: "id-1", Name: "admin"}
	assert.Equal(t, "id-1", roleFieldValue(r, "id"))
	assert.Equal(t, "admin", roleFieldValue(r, "name"))
	assert.Equal(t, "", roleFieldValue(r, "unknown"))
}

// TestRemoveFromUserIfNotLast_RequiresAmbientTx covers the fail-fast guard
// added to satisfy the no-TOCTOU port contract: P1#3 advisory_xact_lock is
// only effective inside a TxRunner.RunInTx because the lock is released at
// transaction end. A pool-only ctx is rejected with ErrInternal rather than
// silently providing weaker isolation.
func TestRemoveFromUserIfNotLast_RequiresAmbientTx(t *testing.T) {
	r, err := NewPGRoleRepository(dummyPool(), clock.Real())
	require.NoError(t, err)

	_, err = r.RemoveFromUserIfNotLast(context.Background(), "user-1", "admin")
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrInternal, ec.Code,
		"P1#3 fail-fast: pool-only ctx must produce ErrInternal, not silent weaker isolation")
}

// TestMapAssignError_NonPgxError covers the non-pgx error fallback path of
// mapAssignError (anything that isn't a pgconn.PgError must wrap as KindInternal).
func TestMapAssignError_NonPgxError(t *testing.T) {
	r, err := NewPGRoleRepository(dummyPool(), clock.Real())
	require.NoError(t, err)

	plain := errors.New("network timeout")
	mapped := r.mapAssignError(plain, "user-1", "admin")
	require.Error(t, mapped)
	var ec *errcode.Error
	require.ErrorAs(t, mapped, &ec)
	assert.Equal(t, errAdapterPGQuery, ec.Code,
		"non-pgx error must wrap as ErrAdapterPGQuery (infra)")
}

// TestMapAssignError_UnknownPgUniqueConstraint covers the 23505 default
// branch for unexpected assignment unique constraints. PK collision is absorbed
// by ON CONFLICT DO NOTHING upstream and never reaches mapAssignError; here we
// exercise the "unknown constraint" fallback.
func TestMapAssignError_UnknownPgUniqueConstraint(t *testing.T) {
	r, err := NewPGRoleRepository(dummyPool(), clock.Real())
	require.NoError(t, err)

	pgErr := &pgconn.PgError{
		Code:           pgUniqueViolation,
		ConstraintName: "some_other_unique_idx",
		Message:        "duplicate key value violates unique constraint",
	}
	mapped := r.mapAssignError(pgErr, "user-1", "viewer")
	require.Error(t, mapped)
	var ec *errcode.Error
	require.ErrorAs(t, mapped, &ec)
	assert.Equal(t, errAdapterPGQuery, ec.Code,
		"unknown 23505 constraint must wrap as KindInternal, not silently mapped to admin-duplicate")
}

// TestMapAssignError_FKViolations covers both 23503 mappings introduced by
// migration 020: fk_role_assignments_user → ErrAuthUserNotFound and
// fk_role_assignments_role → ErrAuthRoleNotFound.
func TestMapAssignError_FKViolations(t *testing.T) {
	r, err := NewPGRoleRepository(dummyPool(), clock.Real())
	require.NoError(t, err)

	cases := []struct {
		name       string
		constraint string
		wantCode   errcode.Code
	}{
		{"user FK", "fk_role_assignments_user", errcode.ErrAuthUserNotFound},
		{"role FK", "fk_role_assignments_role", errcode.ErrAuthRoleNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pgErr := &pgconn.PgError{
				Code:           pgForeignKeyViolation,
				ConstraintName: tc.constraint,
			}
			mapped := r.mapAssignError(pgErr, "user-1", "admin")
			require.Error(t, mapped)
			var ec *errcode.Error
			require.ErrorAs(t, mapped, &ec)
			assert.Equal(t, tc.wantCode, ec.Code)
		})
	}
}
