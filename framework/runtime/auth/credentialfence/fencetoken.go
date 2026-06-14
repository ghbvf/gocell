package credentialfence

import (
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// FenceToken is a sealed capability proof for the credential-invalidation
// trifecta. The unexported isCredentialFenceToken marker method makes this
// interface unimplementable outside the credentialfence package, so external
// packages cannot construct a non-nil FenceToken value.
//
// Allowed call site for [Mint] is enforced by archtest
// FENCE-TOKEN-MINT-FUNNEL-01 (see package godoc).
type FenceToken interface {
	// MARKER: do not implement; this is the sealing marker — call
	// credentialfence.Mint from corecells/accesscore/internal/credentialinvalidate
	// (or a *_test.go / conformance suite) instead.
	isCredentialFenceToken()
}

// fenceToken is the unexported concrete impl. Outside this package the type
// cannot be referenced, so fenceToken{} composite literals cannot be written.
type fenceToken struct{}

func (fenceToken) isCredentialFenceToken() {}

// Mint returns a fresh FenceToken. It is the sole entry point for
// constructing a non-nil FenceToken value.
//
// Allowed callers (enforced by archtest FENCE-TOKEN-MINT-FUNNEL-01):
//   - corecells/accesscore/internal/credentialinvalidate/ (the production funnel)
//   - runtime/auth/session/storetest/, runtime/auth/refresh/storetest/,
//     corecells/accesscore/internal/ports/conformance/ (conformance suites)
//   - any *_test.go file (unit / integration test bodies)
//
// Any other caller is rejected as a form-uniqueness violation.
func Mint() FenceToken {
	return fenceToken{}
}

// fenceTokenRequiredMessage is the const-literal format string passed to
// errcode.Assertion. The %s slot carries the calling method's identifier
// (e.g. "session.Store.RevokeForSubject"), supplied at the call site as a
// runtime argument. Assertion is allowed to format runtime context into
// Message per its documented exception (see pkg/errcode godoc).
//
//nolint:gosec // G101 false positive: format template, not a credential value
const fenceTokenRequiredMessage = "%s: FenceToken required; must route through credentialinvalidate.Invalidator"

// MustHave panics through the panic-taxonomy funnel
// (panicregister.Approved + errcode.Assertion, B-class) when tok is nil or
// a typed-nil interface, surfacing the programmer error as a 500 via the
// kernel Recovery middleware rather than letting the bug slip past as a
// silent failure deeper in the call chain.
//
// site identifies the offending method for the panic message — pass a
// const string such as "session.Store.RevokeForSubject" so the operator
// can pinpoint the bypass at a glance.
//
// The three credential-mutation methods (session.Store.RevokeForSubject,
// refresh.Store.RevokeUser, ports.UserRepository.BumpAuthzEpoch) and their
// implementations call MustHave as the first statement of their bodies.
// This is the defense-in-depth complement to the type-system seal — even if
// a future call form bypasses the upstream archtest blindspot checks
// (function-value capture, reflect.MethodByName), a nil token would
// still produce a hard failure here.
//
// MustHave is package-public because it is called from impl packages
// (runtime/auth/session, runtime/auth/refresh, corecells/accesscore/internal/mem,
// adapters/postgres, adapters/redis); it does not weaken the seal because
// it cannot construct a FenceToken — only Mint can.
func MustHave(tok FenceToken, site string) {
	if validation.IsNilInterface(tok) {
		panic(panicregister.Approved("credential-fence-nil-token",
			errcode.Assertion(fenceTokenRequiredMessage, site)))
	}
}
