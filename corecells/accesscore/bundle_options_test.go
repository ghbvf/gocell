package accesscore

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	accessmem "github.com/ghbvf/gocell/corecells/accesscore/mem"
	accesspg "github.com/ghbvf/gocell/corecells/accesscore/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestWithMemBundle_WiresFourPrimitives verifies that WithMemBundle copies the
// quadruple (UserRepository, RoleRepository, SetupLock, TxRunner) from the
// mem.Bundle into the AccessCore struct in a single option call. Mis-pairing
// is inexpressible — the four accessor outputs all come from the same Store.
func TestWithMemBundle_WiresFourPrimitives(t *testing.T) {
	b := accessmem.NewBundle(clock.Real())
	c := NewAccessCore(clock.Real(), WithMemBundle(b))

	assert.NotNil(t, c.userRepo, "WithMemBundle must wire userRepo")
	assert.NotNil(t, c.roleRepo, "WithMemBundle must wire roleRepo")
	assert.NotNil(t, c.policyRepo, "WithMemBundle must wire policyRepo")
	assert.NotNil(t, c.setupLock, "WithMemBundle must wire setupLock")
	assert.NotNil(t, c.txRunner, "WithMemBundle must wire txRunner")
	assert.False(t, c.setupLockNil, "non-nil bundle must not flip setupLockNil sentinel")

	// All primitives are pointer-equal to what the Bundle exposes via
	// its accessor methods — no double-wrap.
	assert.Equal(t, b.UserRepository(), c.userRepo)
	assert.Equal(t, b.RoleRepository(), c.roleRepo)
	assert.Equal(t, b.PolicyRepository(), c.policyRepo)
	assert.Equal(t, b.SetupLock(), c.setupLock)
	assert.Equal(t, b.TxRunner(), c.txRunner)
}

// TestWithPGBundle_WiresFourPrimitives mirrors the mem-bundle test with a PG
// bundle. The PG bundle uses zero-value pool/txMgr — no real PG connection is
// needed because inner constructors only store the references.
func TestWithPGBundle_WiresFourPrimitives(t *testing.T) {
	fakePool := new(pgxpool.Pool)
	fakeTxm := outbox.DemoTxRunner{}

	b, err := accesspg.NewBundle(fakePool, fakeTxm, clock.Real())
	require.NoError(t, err)

	c := NewAccessCore(clock.Real(), WithPGBundle(b))

	assert.NotNil(t, c.userRepo, "WithPGBundle must wire userRepo")
	assert.NotNil(t, c.roleRepo, "WithPGBundle must wire roleRepo")
	assert.NotNil(t, c.policyRepo, "WithPGBundle must wire policyRepo (PGPolicyRepo)")
	assert.NotNil(t, c.setupLock, "WithPGBundle must wire setupLock (PGSetupLock)")
	assert.NotNil(t, c.txRunner, "WithPGBundle must wire txRunner (WrapForCell-wrapped)")
	assert.False(t, c.setupLockNil)

	assert.Equal(t, b.UserRepository(), c.userRepo)
	assert.Equal(t, b.RoleRepository(), c.roleRepo)
	assert.Equal(t, b.PolicyRepository(), c.policyRepo)
	assert.Equal(t, b.SetupLock(), c.setupLock)
	assert.Equal(t, b.TxRunner(), c.txRunner)
}
