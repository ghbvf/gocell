// Package authzmutate is the single entry point for all authz-field mutations
// on a User aggregate. It enforces the Hard funnel invariant:
//
//	"mutate authz state without epoch-bump+revoke" is unrepresentable.
//
// Every caller that needs to change a user's status, passwordResetRequired, or
// trigger a role-revoke credential event MUST go through Mutator.Apply.
//
// # Archtest enforcement (Wave 2)
//
// DOMAIN-AUTHZ-FIELD-PRIVATE-01 — domain.User authz fields are private;
// no exported field or new public setter exists.
//
// AUTHZ-MUTATION-APPLY-FUNNEL-01 — callers of domain.User.SetStatus /
// SetPasswordResetRequired ⊆ {authzmutate/, domain _test.go};
// callers of credentialinvalidate.Invalidator.Apply ⊆
// {credentialinvalidate/, authzmutate/, identitymanage/, sessionrefresh/, rbacassign/}.
//
// Rule (b) note: the broader caller set (vs the originally intended
// {authzmutate/, sessionrefresh/}) is the §A10-documented co-tx-atomicity
// deviation — identitymanage/ calls inv.Apply directly for Delete and
// changePasswordInTx (user-row-delete + revoke must be one transaction;
// routing through authzmutate would split those transactions); rbacassign/
// similarly calls inv.Apply co-tx with the role-row write. The write-side
// Hard guarantee comes from Rule (a) field privatization
// (DOMAIN-AUTHZ-FIELD-PRIVATE-01), NOT from Rule (b) caller-set closure.
//
// Backlog: AUTHZ-MUTATION-FUNNEL-UPGRADE-01 (S4e, landed in PR #494).
// ADR §A10 → §A10 status now "landed".
package authzmutate

import (
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Mutation is the sealed interface for all authz-field change operations.
// Implementations are in this package only; the unexported mutationOK()
// method prevents external packages from satisfying the interface.
//
// Invalidates() distinguishes additive operations (activate, clear-reset)
// from credential-weakening operations (lock, suspend, require-reset, role-revoke).
// Apply skips inv.Apply when Invalidates() == false — additive changes do not
// need an epoch-bump+revoke because no existing grants become too broad.
//
// ActivateUser semantics (ADR §A6 / OAuth Security BCP §4.13.2):
// re-activating a user is an additive operation (scope-expanding, not
// scope-narrowing). It persists the status change but must NOT bump the
// authz_epoch or revoke sessions — existing sessions become MORE valid,
// not LESS. Invalidates() == false for ActivateUser.
//
// ClearPasswordReset semantics: clearing the reset-required flag means the
// user has changed their password; the password-change path itself calls
// inv.Apply via changePasswordInTx, so clearing the flag here is a no-op
// on the invalidation side. Invalidates() == false for ClearPasswordReset.
type Mutation interface {
	// Event returns the CredentialEvent label carried by this mutation.
	//
	// Contract: Event() is consumed ONLY when Invalidates()==true. The value
	// is passed to inv.Apply → audit/credential-event routing and must be a
	// meaningful CredentialEvent for those callers.
	//
	// For additive mutations (Invalidates()==false: ActivateUser,
	// ClearPasswordReset) the return value is NEVER READ by any code path —
	// Mutator.Apply skips inv.Apply entirely when Invalidates()==false.
	// These implementations return the nearest-domain event purely to satisfy
	// the total interface; the value is a documented don't-care and will never
	// reach an audit row or session-revocation path.
	Event() session.CredentialEvent

	// Invalidates returns true when this mutation is a credential-weakening
	// event that requires an epoch-bump + session/refresh revocation.
	Invalidates() bool

	// apply executes the domain mutation on u. Called exclusively from
	// Mutator.Apply inside a RunInTx closure.
	apply(u *domain.User, now time.Time)

	// mutationOK seals the interface to this package.
	mutationOK()
}

// LockUser locks the user account. Credential-weakening — Invalidates() == true.
type LockUser struct{}

func (LockUser) Event() session.CredentialEvent { return session.CredentialEventLock }
func (LockUser) Invalidates() bool              { return true }
func (LockUser) apply(u *domain.User, now time.Time) {
	u.SetStatus(domain.StatusLocked, now)
}
func (LockUser) mutationOK() {}

// SuspendUser suspends the user account. Credential-weakening — Invalidates() == true.
//
// suspend≡lock for credential revocation (intentional, ADR-consistent):
// Event() returns CredentialEventLock rather than a hypothetical
// CredentialEventSuspend. This is not a bug — session.CredentialEvent is a
// sealed, completeness-checked set (WithRevokeOnAll / NewProtocol /
// ValidateCredentialEvent / String / all Store.RevokeForSubject impls) with
// no Suspend member by design. The project canonically treats suspend as
// equivalent to Lock for the purpose of session/refresh revocation: both
// states make the user non-authenticable and must revoke all active tokens.
// Precedent: identitymanage.cascadeInvalidateOnDemotion godoc explicitly
// states "suspended semantics are equivalent to Lock".
type SuspendUser struct{}

func (SuspendUser) Event() session.CredentialEvent { return session.CredentialEventLock }
func (SuspendUser) Invalidates() bool              { return true }
func (SuspendUser) apply(u *domain.User, now time.Time) {
	u.SetStatus(domain.StatusSuspended, now)
}
func (SuspendUser) mutationOK() {}

// ActivateUser re-activates a user. Additive — Invalidates() == false.
// Existing sessions remain valid; no epoch-bump needed.
// ref: ADR §A6 / OAuth Security BCP §4.13.2 (scope-expanding ops don't revoke).
type ActivateUser struct{}

func (ActivateUser) Event() session.CredentialEvent { return session.CredentialEventLock }
func (ActivateUser) Invalidates() bool              { return false }
func (ActivateUser) apply(u *domain.User, now time.Time) {
	u.SetStatus(domain.StatusActive, now)
}
func (ActivateUser) mutationOK() {}

// RequirePasswordReset marks the user as requiring a password reset.
// Credential-weakening — Invalidates() == true (existing sessions should
// be revoked so the user is forced through the reset flow immediately).
type RequirePasswordReset struct{}

func (RequirePasswordReset) Event() session.CredentialEvent {
	return session.CredentialEventPasswordReset
}
func (RequirePasswordReset) Invalidates() bool { return true }
func (RequirePasswordReset) apply(u *domain.User, now time.Time) {
	u.SetPasswordResetRequired(true, now)
}
func (RequirePasswordReset) mutationOK() {}

// ClearPasswordReset clears the password-reset-required flag. Additive —
// Invalidates() == false. The credential change (password rotation) was
// already handled by changePasswordInTx which called inv.Apply directly;
// this mutation only updates the domain flag.
type ClearPasswordReset struct{}

func (ClearPasswordReset) Event() session.CredentialEvent {
	return session.CredentialEventPasswordReset
}
func (ClearPasswordReset) Invalidates() bool { return false }
func (ClearPasswordReset) apply(u *domain.User, now time.Time) {
	u.SetPasswordResetRequired(false, now)
}
func (ClearPasswordReset) mutationOK() {}

// RoleRevoked signals that a role was revoked from the user. The apply method
// is a no-op on user fields — the role-row write is handled by rbacassign's
// own transaction logic. The mutation carries CredentialEventRoleRevoke so the
// trifecta (epoch-bump + session-revoke + refresh-revoke) is triggered.
// Credential-weakening — Invalidates() == true.
type RoleRevoked struct{}

func (RoleRevoked) Event() session.CredentialEvent { return session.CredentialEventRoleRevoke }
func (RoleRevoked) Invalidates() bool              { return true }
func (RoleRevoked) apply(_ *domain.User, _ time.Time) {
	// no-op: role-row write handled by rbacassign; epoch-bump done via inv.Apply.
}
func (RoleRevoked) mutationOK() {}
