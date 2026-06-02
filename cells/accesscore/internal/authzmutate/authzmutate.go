package authzmutate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
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
//  1. m.persist(txCtx, repo, userID, now) — writes the mutation directly via
//     the appropriate narrow port method (UpdateLockState / UpdatePasswordResetFlag).
//     RowsAffected==0 → ErrAuthUserNotFound (KindNotFound) from the port; this
//     replaces the prior GetByIDForUpdate round-trip with the same observable
//     error surface.
//  2. If m.Invalidates(), inv.Apply(txCtx, userID, m.Event()) — bumps
//     authz_epoch + revokes sessions + revokes refresh chains.
//
// Preconditions: m must not be nil; userID must not be empty; txCtx must be
// an active transaction context obtained from the caller's RunInTx closure.
// ApplyInTx executes the mutation within the caller-provided transaction
// context txCtx. tid is the tenant that scopes the mutation; callers derive it
// via tenant.FromContext(ctx) (post-auth) or carry it from the pre-auth login
// input (accountlockout, sessionlogin). Passing it as an explicit param avoids
// fragility on pre-auth paths where ctx may not carry ctxkeys.TenantID.
func (a *Mutator) ApplyInTx(
	ctx context.Context, txCtx context.Context, tid tenant.TenantID, userID string, m Mutation, now time.Time,
) error {
	if m == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate.ApplyInTx: mutation must not be nil")
	}
	if userID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authzmutate.ApplyInTx: userID must not be empty")
	}
	slog.DebugContext(ctx, "authzmutate.ApplyInTx", "userID", userID, "mutation", fmt.Sprintf("%T", m))
	if err := m.persist(txCtx, a.repo, tid, userID, now); err != nil {
		return fmt.Errorf("authzmutate.ApplyInTx: persist: %w", err)
	}
	if m.Invalidates() {
		if err := a.inv.Apply(txCtx, tid, userID, m.Event()); err != nil {
			return fmt.Errorf("authzmutate.ApplyInTx: invalidate credentials: %w", err)
		}
	}
	return nil
}
