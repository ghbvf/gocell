package accesscoretest

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	accesscoremem "github.com/ghbvf/gocell/cells/accesscore/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// AccessFixture is the canonical in-memory test aggregate for accesscore.
// It wraps a single mem.Bundle so UserRepo(), RoleRepo(), TxRunner(), and
// SetupLock() all originate from the same underlying mem.Store — preventing
// the store-pairing footgun that caused PR #595.
//
// Use NewAccessFixture to construct; never embed the zero value.
type AccessFixture struct {
	bundle accesscoremem.Bundle
}

// NewAccessFixture constructs an AccessFixture backed by mem.NewBundle(clk).
// t is used to mark the function as a test helper for cleaner failure output.
func NewAccessFixture(t *testing.T, clk clock.Clock) *AccessFixture {
	t.Helper()
	return &AccessFixture{
		bundle: accesscoremem.NewBundle(clk),
	}
}

// UserRepository returns the bundle-paired UserRepository.
func (f *AccessFixture) UserRepository() ports.UserRepository { return f.bundle.UserRepository() }

// RoleRepository returns the bundle-paired RoleRepository.
func (f *AccessFixture) RoleRepository() ports.RoleRepository { return f.bundle.RoleRepository() }

// TxRunner returns the bundle-paired store-bound CellTxManager. Use this when
// wiring services that require a real atomic TxManager (e.g. identitymanage
// Create → lock-scoped read-modify-write).
func (f *AccessFixture) TxRunner() persistence.CellTxManager { return f.bundle.TxRunner() }

// SetupLock returns the bundle-paired SetupLockAcquirer (noop for mem).
func (f *AccessFixture) SetupLock() ports.SetupLockAcquirer { return f.bundle.SetupLock() }

// SeedUser persists u directly via UserRepository.Create. Call before
// exercising service methods that require the user to pre-exist.
func (f *AccessFixture) SeedUser(ctx context.Context, u *domain.User) error {
	return f.bundle.UserRepository().Create(ctx, u)
}

// SeedRole persists role directly via RoleRepository.Create.
func (f *AccessFixture) SeedRole(ctx context.Context, role *domain.Role) error {
	return f.bundle.RoleRepository().Create(ctx, role)
}

// SeedAssignment assigns the role to the user via RoleRepository.AssignToUser.
// Returns an error if either the user or role does not exist in the store.
func (f *AccessFixture) SeedAssignment(ctx context.Context, userID, roleID string) error {
	_, err := f.bundle.RoleRepository().AssignToUser(ctx, userID, roleID)
	return err
}
