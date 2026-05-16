package credentialauthority

import (
	"strconv"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Assert is the read-side credential-authority Hard funnel.
//
// Assert always runs the baseline check (user.CanAuthenticate()) inline,
// then applies each Check in order. The three failure modes collapse to
// the same (KindPermissionDenied, ErrAuthUserNotActive) wire-error envelope
// so the slice can translate to its own uniform 401 (ErrAuthLoginFailed,
// ErrAuthRefreshFailed, ErrAuthInvalidToken) without leaking which check
// failed (防枚举). The specific reason is carried only in WithInternal
// for slog.
//
// Callers MUST NOT branch on the returned error to discover which check
// failed. If different side effects are required per failure class (see
// sessionrefresh cascade semantics), the slice should issue two separate
// Assert calls and route side effects by call site.
//
// No ctx parameter: Assert performs no I/O and has no tracing surface;
// adding ctx would be designing for hypothetical future requirements.
//
// See package doc for Hard funnel archtest invariant
// (CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01) and ADR §A11.
func Assert(user *domain.User, checks ...Check) error {
	if user == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"credentialauthority.Assert: user must not be nil")
	}
	if !user.CanAuthenticate() {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
			"credential not authoritative",
			errcode.WithInternal("credentialauthority: baseline CanAuthenticate=false"))
	}
	for i, c := range checks {
		if c == nil {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"credentialauthority.Assert: nil Check in variadic",
				errcode.WithInternal("credentialauthority: nil check at index "+strconv.Itoa(i)))
		}
		if err := c.apply(user); err != nil {
			return err
		}
	}
	return nil
}
