package domain

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// EffectiveAdminCounter is the sealed dependency required by LastAdminGuard.
// Effective admin = user.status='active' AND user holds the admin role
// (ADR-admin-invariant §3.2, S4.0). A user that holds the admin role but is
// locked/suspended is NOT counted (they cannot log in to administer).
//
// RoleRepository satisfies this interface via CountEffectiveAdmins. The narrow
// single-method shape prevents mis-wiring with the generic CountByRole, whose
// semantics include inactive holders (used by adminprovision bootstrap
// idempotency, not by the at-least-one invariant).
type EffectiveAdminCounter interface {
	CountEffectiveAdmins(ctx context.Context) (int, error)
}

// LastAdminGuard rejects operations that would leave the system with zero
// effective admins, enforcing the "at least one effective admin" invariant
// (ADR `docs/architecture/202605101400-adr-admin-invariant.md`, S4.0).
//
// Layered protection:
//
//  1. Application: CheckRemove is invoked from identitymanage.Delete /
//     Lock / Update(status mutation) and indirectly via
//     rbacassign.Revoke → RoleRepository.RemoveFromUserIfNotLast. Callers
//     receive ErrAuthLastAdminProtected (HTTP 403) with a precise errcode.
//
//  2. DB: migrations/024_effective_admin_invariant.sql installs BEFORE row
//     triggers on `users` (UPDATE/DELETE) and `role_assignments` (DELETE)
//     that raise 'effective_admin_invariant' when a mutation would leave
//     zero effective admins. The DB layer is the SQL-level safety net for
//     direct-SQL bypass, not the precision check.
//
// counter is required (NewLastAdminGuard fail-fasts on nil).
type LastAdminGuard struct {
	counter EffectiveAdminCounter
}

// NewLastAdminGuard constructs a LastAdminGuard. counter must be non-nil; a
// typed-nil or bare-nil EffectiveAdminCounter returns ErrValidationFailed so
// misconfigured assemblies fail at startup.
func NewLastAdminGuard(counter EffectiveAdminCounter) (*LastAdminGuard, error) {
	if validation.IsNilInterface(counter) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"domain.NewLastAdminGuard: EffectiveAdminCounter must not be nil")
	}
	return &LastAdminGuard{counter: counter}, nil
}

// CheckRemove reports whether mutating userID (delete, lock, status change
// away from 'active', or admin role revoke) may proceed without violating the
// at-least-one-effective-admin invariant.
//
// userIsActiveAdmin is the caller's evidence that userID is currently an
// effective admin (status='active' AND holds admin role). When false (target
// user is not an effective admin), the mutation cannot reduce the effective
// admin count, so CheckRemove returns nil without invoking the counter.
//
// When userIsActiveAdmin is true, the counter is queried; if the result is
// ≤ 1 (the target is the only effective admin), CheckRemove returns
// ErrAuthLastAdminProtected. The counter error is propagated unchanged so
// infrastructure faults do not get conflated with the fail-closed protection
// path. Caller's job to wrap with context (`fmt.Errorf("…: %w", err)`).
func (g *LastAdminGuard) CheckRemove(ctx context.Context, _ string, userIsActiveAdmin bool) error {
	if !userIsActiveAdmin {
		return nil
	}
	n, err := g.counter.CountEffectiveAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
			"cannot remove the last effective admin",
			errcode.WithCategory(errcode.CategoryAuth))
	}
	return nil
}
