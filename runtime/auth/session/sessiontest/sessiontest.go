// Package sessiontest provides test helpers for code that depends on
// runtime/auth/session. It mirrors the net/http/httptest convention of placing
// test helpers in a sub-package to avoid polluting the production API.
package sessiontest

import (
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Protocol returns the canonical *session.Protocol used by cross-package test
// helpers (FingerprintJTIRef + OrderingAuthzEpoch + RevokeOnAll). It is the
// single composition-root-equivalent entry point for test code that needs a
// *session.Protocol without each test reaching into NewProtocol directly.
//
// Lives under runtime/auth/session/ so that the
// SESSION-PROTOCOL-COMPOSITION-ROOT-01 archtest allowlist covers this
// constructor; cells/* / adapters/* test helpers (e.g.
// cells/accesscore/internal/testutil) consume *session.Protocol via this
// function rather than re-implementing the option list and tripping the
// archtest.
//
// Panics on misconfiguration the same way MustNewProtocol does: this helper
// is meant for tests where a non-recoverable Protocol setup is a programmer
// error, not a runtime path.
func Protocol() *session.Protocol {
	return session.MustNewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
}
