package credentialauthority

import (
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Check is the sealed option type for Assert. Implementations live in this
// package only; the unexported checkOK() marker method prevents external
// packages from satisfying the interface, so the variant set is closed by
// the Go type system.
type Check interface {
	apply(user *domain.User) error
	checkOK()
}

// WithPasswordVersionPin asserts the user's current PasswordVersion equals
// Expected. Used by the issue path (sessionlogin) under FOR UPDATE row lock
// to detect a concurrent ChangePassword that advanced the version between
// the pre-bcrypt snapshot and the locked re-read (P1.1 race defense).
type WithPasswordVersionPin struct{ Expected int64 }

func (w WithPasswordVersionPin) apply(u *domain.User) error {
	if u.PasswordVersion != w.Expected {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
			"credential not authoritative",
			errcode.WithInternal("credentialauthority: password version stale"))
	}
	return nil
}

func (WithPasswordVersionPin) checkOK() {}

// WithSessionNotRevoked asserts the backing session row is not revoked.
// The caller passes session.Session.RevokedAt or session.ValidateView.RevokedAt
// directly (a *time.Time) — this keeps the package free of any
// runtime/auth/session import and lets validate / refresh paths share one
// Check shape regardless of which session projection they hold.
type WithSessionNotRevoked struct{ RevokedAt *time.Time }

func (w WithSessionNotRevoked) apply(_ *domain.User) error {
	if w.RevokedAt != nil {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
			"credential not authoritative",
			errcode.WithInternal("credentialauthority: session revoked"))
	}
	return nil
}

func (WithSessionNotRevoked) checkOK() {}
