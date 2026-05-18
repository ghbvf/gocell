// Package sessiontest provides test helpers for code that depends on
// runtime/auth/session. It mirrors the net/http/httptest convention of placing
// test helpers in a sub-package to avoid polluting the production API.
package sessiontest

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
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
// Panics on misconfiguration: this helper is meant for tests where a
// non-recoverable Protocol setup is a programmer error, not a runtime path.
// The panic block below is a defensive branch — session.NewProtocol with the
// hardcoded option triplet never errors in practice. Per PANIC-REGISTERED-01
// the reason must be a const literal at the panic call site, so this branch
// cannot be funneled through a shared helper without breaking the invariant.
func Protocol() *session.Protocol {
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if err != nil {
		e := errcode.Assertion("sessiontest: protocol construction failed")
		e.InternalMessage = err.Error()
		panic(panicregister.Approved("sessiontest-protocol-init", e))
	}
	return p
}
