package credentialauthority

import (
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
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
// the expected value captured by SnapshotPasswordVersion. The expected field
// is unexported on purpose — slice code MUST construct this Check through
// SnapshotPasswordVersion(user) so the only place that reads
// domain.User.PasswordVersion is inside this package. Combined with the
// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 upstream prong (which forbids reads
// under the slice prefixes), this makes "slice reasoning about password
// version without funnel routing" unrepresentable.
type WithPasswordVersionPin struct{ expected int64 }

func (w WithPasswordVersionPin) apply(u *domain.User) error {
	if u.PasswordVersion != w.expected {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
			"credential not authoritative",
			errcode.WithInternal("credentialauthority: password version stale"))
	}
	return nil
}

func (WithPasswordVersionPin) checkOK() {}

// SnapshotPasswordVersion captures the user's current PasswordVersion into a
// WithPasswordVersionPin Check for later validation under FOR UPDATE lock.
// This is the ONLY legal way for slice code to observe PasswordVersion: the
// field read happens inside the funnel package (allowlisted), and the slice
// holds an opaque Check value.
//
// Typical usage (sessionlogin pre-bcrypt snapshot → in-tx pin):
//
//	pin := credentialauthority.SnapshotPasswordVersion(preUser)
//	// ... run bcrypt outside tx ...
//	if err := credentialauthority.Assert(user, pin); err != nil { /* race */ }
//
// nil user returns a pin with sentinel expected=-1, which cannot match any
// real (>=0) PasswordVersion — fail-closed. Callers should not pass nil; the
// upstream invariant is that *domain.User was successfully fetched.
func SnapshotPasswordVersion(u *domain.User) WithPasswordVersionPin {
	if u == nil {
		return WithPasswordVersionPin{expected: -1}
	}
	return WithPasswordVersionPin{expected: u.PasswordVersion}
}

// WithSessionNotRevoked asserts the backing session row is not revoked.
// The revokedAt field is unexported; slice code MUST construct this Check
// via SessionNotRevoked(view) so the only place that reads
// session.ValidateView.RevokedAt is inside this package.
type WithSessionNotRevoked struct{ revokedAt *time.Time }

func (w WithSessionNotRevoked) apply(_ *domain.User) error {
	if w.revokedAt != nil {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
			"credential not authoritative",
			errcode.WithInternal("credentialauthority: session revoked"))
	}
	return nil
}

func (WithSessionNotRevoked) checkOK() {}

// SessionNotRevoked captures a ValidateView's RevokedAt timestamp into a
// Check. The field read happens inside this funnel package only; slice
// code holds the opaque Check value.
//
// nil view returns a Check that fails closed (treated as a revoked session)
// — callers should pre-fetch the view, but the funnel does not trust caller
// invariants and refuses to leak "no session ≡ live session" semantics.
func SessionNotRevoked(v *session.ValidateView) WithSessionNotRevoked {
	if v == nil {
		now := time.Unix(0, 0).UTC()
		return WithSessionNotRevoked{revokedAt: &now}
	}
	return WithSessionNotRevoked{revokedAt: v.RevokedAt}
}
