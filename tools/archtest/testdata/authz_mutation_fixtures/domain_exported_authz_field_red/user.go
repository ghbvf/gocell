// Package domain_exported_authz_field_red is a RED fixture for the
// DOMAIN-AUTHZ-FIELD-PRIVATE-01 archtest: it defines a User struct with
// exported authz-sensitive fields (Status, PasswordResetRequired, AuthzEpoch)
// and an exported setter beyond the two sanctioned ones. The archtest must
// detect these as violations.
//
// This package is intentionally standalone (does not import the real domain
// package) — it simulates a regression where someone re-exports the fields.
// The archtest loads this fixture via explicit pattern and verifies the
// field/method scanner flags at least one violation.
package domain

// UserStatus is a regression stub.
type UserStatus string

// User is the RED fixture: it exports authz-sensitive fields that must stay
// private in production code.
type User struct {
	// OK fields (non-authz — present for realism)
	ID       string
	Username string

	// VIOLATION: these three must be private in production domain.User
	Status                UserStatus
	PasswordResetRequired bool
	AuthzEpoch            int64
}

// SetStatusPublic is a VIOLATION: an exported setter beyond SetStatus /
// SetPasswordResetRequired that writes an authz-sensitive concept.
func (u *User) SetStatusPublic(s UserStatus) {
	u.Status = s
}
