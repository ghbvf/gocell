package authzmutate

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
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
//
// tx boundary: Mutator no longer owns a RunInTx boundary. Callers MUST invoke
// ApplyInTx from within their own outer RunInTx closure. This ensures that the
// domain mutation + event publish co-commit in the same transaction (L2
// OutboxFact guarantee).
type Mutator struct {
	inv  *credentialinvalidate.Invalidator
	repo ports.UserRepository
}

// New constructs a Mutator, fail-fasting on nil deps.
func New(
	inv *credentialinvalidate.Invalidator,
	repo ports.UserRepository,
) (*Mutator, error) {
	if validation.IsNilInterface(inv) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate: Invalidator required")
	}
	if validation.IsNilInterface(repo) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate: UserRepository required")
	}
	return &Mutator{inv: inv, repo: repo}, nil
}

// ApplyInTx executes the mutation within the caller-provided transaction
// context txCtx. The caller MUST invoke ApplyInTx from within their own outer
// RunInTx closure so that the domain mutation, credential invalidation, and
// event publish all co-commit in the same transaction (L2 OutboxFact).
//
// Steps:
//  1. GetByIDForUpdate(txCtx) — acquires a row lock on the user within the
//     caller's transaction.
//  2. m.apply(u, now) — mutates the in-memory domain aggregate.
//  3. repo.Update(txCtx, u) — persists the mutated aggregate.
//  4. If m.Invalidates(), inv.Apply(txCtx, userID, m.Event()) — bumps
//     authz_epoch + revokes sessions + revokes refresh chains.
//
// Preconditions: m must not be nil; userID must not be empty; txCtx must be
// an active transaction context obtained from the caller's RunInTx closure.
func (a *Mutator) ApplyInTx(ctx context.Context, txCtx context.Context, userID string, m Mutation, now time.Time) error {
	if m == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate.ApplyInTx: mutation must not be nil")
	}
	if userID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate.ApplyInTx: userID must not be empty")
	}
	_ = ctx // ctx is available for future use (e.g. tracing); txCtx carries the tx
	u, err := a.repo.GetByIDForUpdate(txCtx, userID)
	if err != nil {
		return fmt.Errorf("authzmutate.ApplyInTx: get user for update: %w", err)
	}
	m.apply(u, now)
	if err := a.repo.Update(txCtx, u); err != nil {
		return fmt.Errorf("authzmutate.ApplyInTx: update user: %w", err)
	}
	if m.Invalidates() {
		if err := a.inv.Apply(txCtx, userID, m.Event()); err != nil {
			return fmt.Errorf("authzmutate.ApplyInTx: invalidate credentials: %w", err)
		}
	}
	return nil
}
