package accesscore

import (
	accessmem "github.com/ghbvf/gocell/cells/accesscore/mem"
	accesspg "github.com/ghbvf/gocell/cells/accesscore/postgres"
)

// WithMemBundle is the sole public entry point for mem-mode wiring of
// (UserRepository, RoleRepository, SetupLock, TxRunner). All four primitives
// come from a single backing mem.Store via accessmem.NewBundle — mis-pairing
// (e.g. a non-store-paired TxRunner) is inexpressible at compile time outside
// the cells/accesscore/mem package.
//
// See cells/accesscore/mem.Bundle godoc for the Hard funnel rationale.
func WithMemBundle(b accessmem.Bundle) Option {
	return func(c *AccessCore) {
		withUserRepository(b.UserRepository())(c)
		withRoleRepository(b.RoleRepository())(c)
		withSetupLock(b.SetupLock())(c)
		withTxManager(b.TxRunner())(c)
	}
}

// WithPGBundle is the sole public entry point for PG-mode wiring of
// (UserRepository, RoleRepository, SetupLock, TxRunner). All four primitives
// come from the same (pool, txMgr, clk) triple via accesspg.NewBundle.
//
// See cells/accesscore/postgres.Bundle godoc for the Hard funnel rationale.
func WithPGBundle(b accesspg.Bundle) Option {
	return func(c *AccessCore) {
		withUserRepository(b.UserRepository())(c)
		withRoleRepository(b.RoleRepository())(c)
		withSetupLock(b.SetupLock())(c)
		withTxManager(b.TxRunner())(c)
	}
}
