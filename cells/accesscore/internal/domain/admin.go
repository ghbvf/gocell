package domain

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// EffectiveAdminCounterImpl is the structural capability provided by the
// concrete RoleRepositories (PG, mem). PG/mem repos satisfy this interface
// directly via their CountEffectiveAdmins method. It is intentionally NOT
// the type accepted by NewLastAdminGuard — the sealed wrapper
// EffectiveAdminCounter is. Wiring callers (identitymanage.NewService,
// tests) pass an impl into WrapEffectiveAdminCounter to obtain the sealed
// wrapper.
//
// Effective admin = user.status='active' AND user holds the admin role
// (ADR-admin-invariant §3.2, S4.0). A user that holds the admin role but
// is locked/suspended is NOT counted (they cannot log in to administer).
//
// The narrow single-method shape prevents mis-wiring with the generic
// CountByRole, whose semantics include inactive holders (used by
// adminprovision bootstrap idempotency, not by the at-least-one
// invariant).
type EffectiveAdminCounterImpl interface {
	CountEffectiveAdmins(ctx context.Context) (int, error)
}

// EffectiveAdminCounter is the sealed dependency required by
// LastAdminGuard. The unexported sealedEffectiveAdminCounter() marker
// method makes it unimplementable outside the domain package — the only
// path to a value is WrapEffectiveAdminCounter.
//
// AI-rebust 评级 Hard (sealed interface, compile-time blocking): external
// code cannot declare a type satisfying this interface (cannot implement
// the unexported marker method); any attempt produces a compile error.
// The single construction path is WrapEffectiveAdminCounter, which
// validates the underlying impl is non-nil and rejects typed-nil. Same
// pattern as kernel/persistence.CellTxManager (ADR
// 202605101900-adr-cell-raw-infra-sealed-marker §D2).
type EffectiveAdminCounter interface {
	EffectiveAdminCounterImpl
	// MARKER: do not implement; this is the sealing marker — call
	// domain.WrapEffectiveAdminCounter(impl) to obtain a value of this
	// interface.
	sealedEffectiveAdminCounter()
}

// internalEffectiveAdminCounter is the only implementation of the sealed
// EffectiveAdminCounter. It composes the raw impl provided by the wiring
// caller and is constructed exclusively by WrapEffectiveAdminCounter.
type internalEffectiveAdminCounter struct {
	impl EffectiveAdminCounterImpl
}

func (i internalEffectiveAdminCounter) CountEffectiveAdmins(ctx context.Context) (int, error) {
	return i.impl.CountEffectiveAdmins(ctx)
}

func (internalEffectiveAdminCounter) sealedEffectiveAdminCounter() {}

// WrapEffectiveAdminCounter is the sole authorized path to construct an
// EffectiveAdminCounter. impl must be non-nil and not a typed-nil
// interface; the wrapper would otherwise hide the typed-nil from
// NewLastAdminGuard's IsNilInterface guard, silently bypassing the
// fail-fast.
func WrapEffectiveAdminCounter(impl EffectiveAdminCounterImpl) (EffectiveAdminCounter, error) {
	if validation.IsNilInterface(impl) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"domain.WrapEffectiveAdminCounter: EffectiveAdminCounterImpl must not be nil")
	}
	return internalEffectiveAdminCounter{impl: impl}, nil
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
// counter is the sealed EffectiveAdminCounter; NewLastAdminGuard
// fail-fasts on bare-nil or typed-nil sealed values.
type LastAdminGuard struct {
	counter EffectiveAdminCounter
}

// NewLastAdminGuard constructs a LastAdminGuard. counter must be the
// sealed EffectiveAdminCounter constructed via WrapEffectiveAdminCounter;
// a bare-nil or typed-nil sealed value returns ErrValidationFailed so
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
