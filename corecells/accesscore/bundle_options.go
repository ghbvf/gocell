package accesscore

import (
	accessmem "github.com/ghbvf/gocell/corecells/accesscore/mem"
	accesspg "github.com/ghbvf/gocell/corecells/accesscore/postgres"
)

// WithMemBundle is the sole public entry point for mem-mode wiring of
// (UserRepository, RoleRepository, PolicyRepository, SetupLock, TxRunner). All
// primitives come from a single backing mem.Store via accessmem.NewBundle —
// mis-pairing (e.g. a non-store-paired TxRunner) is inexpressible at compile
// time outside the corecells/accesscore/mem package.
//
// See corecells/accesscore/mem.Bundle godoc for the Hard funnel rationale.
func WithMemBundle(b accessmem.Bundle) Option {
	return func(c *AccessCore) {
		withUserRepository(b.UserRepository())(c)
		withRoleRepository(b.RoleRepository())(c)
		withPolicyRepository(b.PolicyRepository())(c)
		withResourceAttributeProvider(b.ResourceAttributeProvider())(c)
		withSetupLock(b.SetupLock())(c)
		withTxManager(b.TxRunner())(c)
	}
}

// WithPGBundle is the sole public entry point for PG-mode wiring of
// (UserRepository, RoleRepository, PolicyRepository, SetupLock, TxRunner). All
// primitives come from the same (pool, txMgr, clk) triple via accesspg.NewBundle.
//
// See corecells/accesscore/postgres.Bundle godoc for the Hard funnel rationale.
func WithPGBundle(b accesspg.Bundle) Option {
	return func(c *AccessCore) {
		withUserRepository(b.UserRepository())(c)
		withRoleRepository(b.RoleRepository())(c)
		withPolicyRepository(b.PolicyRepository())(c)
		withResourceAttributeProvider(b.ResourceAttributeProvider())(c)
		withSetupLock(b.SetupLock())(c)
		withTxManager(b.TxRunner())(c)
	}
}
