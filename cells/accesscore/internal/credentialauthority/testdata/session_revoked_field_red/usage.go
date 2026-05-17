// Package session_revoked_field_red is a RED fixture for
// SESSION-REVOKED-FIELD-ACCESS-01. It reads session.{Session,ValidateView}.RevokedAt
// from a non-allowlisted file, which must be flagged by the upstream
// allowlist of the archtest (≥ 1 violation expected).
//
// LOCATION RATIONALE: the fixture imports runtime/auth/session and lives
// under cells/accesscore/internal/credentialauthority/testdata/ alongside
// the other credentialauthority RED fixtures (outside_caller_red,
// direct_canauth_skip_red, value_capture_red). The `testdata/` directory
// excludes the package from `go build ./...` while archtest loads it via
// an explicit packages.Load pattern.
package session_revoked_field_red

import (
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// badRevokedAtReaderView reads session.ValidateView.RevokedAt from a
// non-allowlisted file. The archtest must flag this read.
func badRevokedAtReaderView(view *session.ValidateView) bool {
	return view.RevokedAt != nil
}

// badRevokedAtReaderModel reads session.Session.RevokedAt (the on-record
// model field, distinct from ValidateView). The archtest must flag this
// read as well — both Session.RevokedAt and ValidateView.RevokedAt are
// in scope.
func badRevokedAtReaderModel(sess *session.Session) bool {
	return sess.RevokedAt != nil
}
