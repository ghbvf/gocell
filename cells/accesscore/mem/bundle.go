// Package mem exposes the typed funnel for mem-backed accesscore wiring. A
// single NewBundle call yields a UserRepository, RoleRepository, SetupLock and
// store-paired TxRunner all derived from the same underlying mem.Store —
// the Bundle struct is sealed (private fields, exported only via accessor
// methods consumed by cells/accesscore.WithMemBundle) so composition roots
// cannot accidentally pair primitives from different stores or substitute
// a non-store-paired TxRunner.
//
// AI-rebust: Hard. Go visibility makes mis-pairing inexpressible outside
// this package: zero-value Bundle{} has nil getters and is rejected by the
// existing phase0 validateRequiredDeps; valid bundles can only come from
// NewBundle. Reverse self-check: ACCESSCORE-BUNDLE-FUNNEL-01 archtest
// asserts cells/accesscore does not re-expose散装 WithUserRepository /
// WithRoleRepository / WithSetupLock / WithTxManager from its root.
//
// Backstory: PR #595 review found cmd/corebundle/access_test_helper_test.go
// wired UserRepository + RoleRepository + SetupLock but forgot the
// store-paired TxRunner — silently falling back to cell.DemoCellTxManager
// after Provisioner.mu was deleted. The Bundle funnel collapses that
// 4-option foot-gun into a single mandatory wire path.
package mem

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// noopSetupLock is the mem-mode SetupLock: a no-op, because the bundle's
// Store-paired TxRunner already serializes via store.mu inside the RunInTx
// closure. Equivalent to cells/accesscore.NoopSetupLock but unexported here
// to keep the bundle the sole construction path.
type noopSetupLock struct{}

func (noopSetupLock) Acquire(context.Context) error { return nil }

// Bundle is the mem-backed (UserRepository, RoleRepository, SetupLock,
// TxRunner) quadruple. Fields are unexported; cells/accesscore.WithMemBundle
// consumes the values via the exported accessor methods.
type Bundle struct {
	userRepo  ports.UserRepository
	roleRepo  ports.RoleRepository
	setupLock ports.SetupLock
	txRunner  persistence.CellTxManager
}

// NewBundle constructs a mem-backed accesscore bundle. All four wired
// primitives originate from the same backing mem.Store, guaranteeing the
// cross-repo effective-admin invariant (S4.0) and serializing concurrent
// first-admin provisioning via store.mu.
//
// clk is the clock used by the mem store (typically clock.Real() in
// production; tests inject deterministic clocks).
func NewBundle(clk clock.Clock) Bundle {
	store := mem.NewStore(clk)
	return Bundle{
		userRepo:  store.UserRepository(),
		roleRepo:  store.RoleRepository(),
		setupLock: noopSetupLock{},
		txRunner:  persistence.WrapForCell(store.TxRunner()),
	}
}

// UserRepository returns the bundle-paired UserRepository view.
func (b Bundle) UserRepository() ports.UserRepository { return b.userRepo }

// RoleRepository returns the bundle-paired RoleRepository view.
func (b Bundle) RoleRepository() ports.RoleRepository { return b.roleRepo }

// SetupLock returns the bundle-paired SetupLock (NoopSetupLock for mem;
// the store-paired TxRunner already serializes via store.mu).
func (b Bundle) SetupLock() ports.SetupLock { return b.setupLock }

// TxRunner returns the bundle-paired Store-bound CellTxManager.
func (b Bundle) TxRunner() persistence.CellTxManager { return b.txRunner }
