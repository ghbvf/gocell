package postgres

import (
	"encoding/json"
	"testing"

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
