package authzmutate

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Mutator is the single entry point for all authz-field mutations on a User
// aggregate. It guarantees that every credential-weakening mutation (status
// change to locked/suspended, requirePasswordReset, role-revoke) atomically
// bumps authz_epoch + revokes sessions + revokes refresh chains via the
// credentialinvalidate funnel.
//
// Additive mutations (activate, clear-reset) persist the domain-field change
// but skip the invalidation trifecta (ADR §A6).
type Mutator struct {
	inv     *credentialinvalidate.Invalidator
	repo    ports.UserRepository
	txMgr   persistence.CellTxManager
}

// New constructs a Mutator, fail-fasting on nil deps.
func New(
	inv *credentialinvalidate.Invalidator,
	repo ports.UserRepository,
	txMgr persistence.CellTxManager,
) (*Mutator, error) {
	if validation.IsNilInterface(inv) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate: Invalidator required")
	}
	if validation.IsNilInterface(repo) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate: UserRepository required")
	}
	if validation.IsNilInterface(txMgr) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate: CellTxManager required")
	}
	return &Mutator{inv: inv, repo: repo, txMgr: txMgr}, nil
}

// MustNew is the composition-root fail-fast wrapper around New. It panics on
// validation failure to surface misconfiguration at process startup. Use only
// from cmd/* (composition root) or test helpers.
func MustNew(
	inv *credentialinvalidate.Invalidator,
	repo ports.UserRepository,
	txMgr persistence.CellTxManager,
) *Mutator {
	m, err := New(inv, repo, txMgr)
	if err != nil {
		panic(panicregister.Approved("authzmutate-mustnew",
			errcode.Assertion("authzmutate: construction failed: %v", err)))
	}
	return m
}

// Apply executes the mutation inside a transaction:
//  1. GetByIDForUpdate — acquires a row lock on the user.
//  2. m.apply(u, now) — mutates the in-memory domain aggregate.
//  3. repo.Update(txCtx, u) — persists the mutated aggregate.
//  4. If m.Invalidates(), inv.Apply(txCtx, userID, m.Event()) — bumps
//     authz_epoch + revokes sessions + revokes refresh chains.
//
// Preconditions: m must not be nil; userID must not be empty.
func (a *Mutator) Apply(ctx context.Context, userID string, m Mutation, now time.Time) error {
	if m == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate.Apply: mutation must not be nil")
	}
	if userID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate.Apply: userID must not be empty")
	}
	return a.txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		u, err := a.repo.GetByIDForUpdate(txCtx, userID)
		if err != nil {
			return fmt.Errorf("authzmutate.Apply: get user for update: %w", err)
		}
		m.apply(u, now)
		if err := a.repo.Update(txCtx, u); err != nil {
			return fmt.Errorf("authzmutate.Apply: update user: %w", err)
		}
		if m.Invalidates() {
			if err := a.inv.Apply(txCtx, userID, m.Event()); err != nil {
				return fmt.Errorf("authzmutate.Apply: invalidate credentials: %w", err)
			}
		}
		return nil
	})
}
