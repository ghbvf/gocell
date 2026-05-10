package domain

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// AdminCounter returns the number of users currently holding the admin role.
// Implementations defer to the underlying RoleRepository (CountByRole("admin"))
// — the type alias keeps LastAdminGuard's signature dependency narrow so unit
// tests can supply a closure without standing up a full RoleRepository fake.
type AdminCounter func(ctx context.Context) (int, error)

// LastAdminGuard rejects operations that would remove the only remaining
// admin from the system, enforcing the "at least one admin" invariant
// (ADR `docs/architecture/202605101400-adr-admin-invariant.md`).
//
// Layered protection:
//
//  1. Application: CheckRemove is invoked from identitymanage.DeleteUser /
//     ChangeUserStatus(Locked) / rbacassign.RevokeRole("admin") so callers
//     receive ErrAuthLastAdminProtected (HTTP 403) with a precise errcode.
//     Composition-root wiring lands in S4 (this PR ships the rule + its
//     unit tests; service-level wiring is the cell-injection PR).
//
//  2. DB: migrations/019_roles.sql installs a BEFORE DELETE trigger on
//     role_assignments that raises 'last_admin_protected' when a direct
//     DELETE would remove the sole admin holder. The DB layer is the
//     SQL-level safety net, not the precision check.
//
// AdminCounter is required (NewLastAdminGuard fail-fasts on nil).
type LastAdminGuard struct {
	count AdminCounter
}

// NewLastAdminGuard constructs a LastAdminGuard. count must be non-nil; nil
// returns ErrValidationFailed so misconfigured assemblies fail at startup.
func NewLastAdminGuard(count AdminCounter) (*LastAdminGuard, error) {
	if count == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"domain.NewLastAdminGuard: AdminCounter must not be nil")
	}
	return &LastAdminGuard{count: count}, nil
}

// CheckRemove reports whether userID may be removed (deleted, locked, or
// have its admin role revoked) without violating the at-least-one invariant.
//
// hasAdminRole is the caller's evidence that userID currently holds the admin
// role; non-admin users are unconditionally allowed (returns nil). When the
// user does hold admin, CheckRemove queries the live admin count: if exactly
// one admin remains, the operation is rejected with ErrAuthLastAdminProtected.
//
// The counter error is propagated unchanged so infrastructure faults (DB
// outage, query error) do not get conflated with the fail-closed protection
// path. Caller's job to wrap with context (`fmt.Errorf("…: %w", err)`).
func (g *LastAdminGuard) CheckRemove(ctx context.Context, _ string, hasAdminRole bool) error {
	if !hasAdminRole {
		return nil
	}
	n, err := g.count(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
			"cannot remove the last admin",
			errcode.WithCategory(errcode.CategoryAuth))
	}
	return nil
}
