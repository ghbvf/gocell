package credentialauthority

import (
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Check is the sealed option type for Assert. Implementations live in this
// package only; the unexported checkOK() marker method prevents external
// packages from satisfying the interface, so the variant set is closed by
// the Go type system.
//
// 适用域 (ADR §A11 重写后): user-bound credential checks only.
// session-state checks (RevokedAt 等) 由 SESSION-REVOKED-FIELD-ACCESS-01
// 独立 funnel 接管, 不再混进本接口 (消除 apply(_ *User) underscore 形态的
// 建模错位; 详见 ADR §A11/§A12 + commit a780fdb8 Wave 1 RED).
type Check interface {
	apply(user *domain.User) error
	checkOK()
}

// withPasswordVersionPin asserts the user's current PasswordVersion equals
// the expected value captured by SnapshotPasswordVersion. The expected and
// the concrete struct itself are unexported on purpose — external callers
// MUST obtain a Check through the SnapshotPasswordVersion factory so the
// only place that reads domain.User.PasswordVersion is inside this
// package, AND no external caller can zero-value construct a Check
// skipping the factory's fail-closed nil-user handling.
//
// (P1-B 上游 Hard: concrete sealed by unexported name +
// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 sealed-by-name detector守护;
// combined with the funnel's caller allowlist this closes the read-side
// loop for user-bound credential checks.)
type withPasswordVersionPin struct{ expected int64 }

// Compile-time assertion: withPasswordVersionPin satisfies Check.
// This is the file-local form of the sealed-interface invariant —
// SnapshotPasswordVersion's return type is Check (interface), so a
// signature mismatch surfaces at compile time at the call site; the
// assertion here is documentation for grep-able audit.
var _ Check = withPasswordVersionPin{}

func (w withPasswordVersionPin) apply(u *domain.User) error {
	if u.PasswordVersion != w.expected {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
			"credential not authoritative",
			errcode.WithInternal("credentialauthority: password version stale"))
	}
	return nil
}

func (withPasswordVersionPin) checkOK() {}

// SnapshotPasswordVersion captures the user's current PasswordVersion into
// an opaque Check for later validation under FOR UPDATE lock. This is the
// ONLY legal way for slice code to observe PasswordVersion: the field
// read happens inside the funnel package (allowlisted), and the slice
// holds an opaque Check value whose concrete type is unexported.
//
// Typical usage (sessionlogin pre-bcrypt snapshot → in-tx pin):
//
//	pin := credentialauthority.SnapshotPasswordVersion(preUser)
//	// ... run bcrypt outside tx ...
//	if err := credentialauthority.Assert(user, pin); err != nil { /* race */ }
//
// nil user returns a Check with sentinel expected=-1, which cannot match
// any real PasswordVersion that a real user can hold (NewUser 从 0 开始;
// -1 < 0 因此不匹配) — fail-closed. Callers should not pass nil; the
// upstream invariant is that *domain.User was successfully fetched.
func SnapshotPasswordVersion(u *domain.User) Check {
	if u == nil {
		return withPasswordVersionPin{expected: -1}
	}
	return withPasswordVersionPin{expected: u.PasswordVersion}
}
