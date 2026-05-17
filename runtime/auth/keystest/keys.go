// Package keystest provides RSA / HMAC key fixtures for tests that exercise
// the auth subsystem (JWT issue/verify, HMAC service-token rings, OIDC stubs).
//
// All helpers in this package are STRICTLY test-only. Production callers must
// use runtime/auth.GenerateRSAKeyPair / NewKeySet / NewHMACKeyRing directly.
//
// The package was extracted from runtime/auth in PR #B2-K-02 to physically
// isolate test-only Must* helpers from the production auth namespace:
// production callers cannot accidentally pull in test key material because
// the symbol `auth.MustGenerateTestKeyPair` no longer exists — they must
// explicitly import `runtime/auth/keystest`, which any code review will flag.
//
// See ADR `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`
// (Hard 主防线 = symbol 物理迁包) and K8s `net/http/httptest` precedent.
//
// Naming: helpers drop the legacy `Test` infix (`MustGenerateKeyPair`,
// `MustNewKeySet`, `MustNewKeyProvider`) because the package name already
// conveys the test context. Following K8s `httptest.NewRecorder`, not
// `httptest.NewTestRecorder`.
//
// Coverage note: the panic blocks below are defensive against errors that
// are unreachable in practice (crypto/rand never fails, hardcoded HMAC secret
// is always ≥32 bytes, MustGenerateKeyPair returns non-nil keys). Per
// PANIC-REGISTERED-01 (panicregister.Approved reason must be a const string
// literal at the panic call site), these blocks cannot be funneled through a
// single helper without violating the literal-reason invariant — see the
// mustNoErr funnel design rejected at PR #553 review, ADR §"Coverage limits".
package keystest

import (
	"crypto/rand"
	"crypto/rsa"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/runtime/auth"
)

// MustGenerateKeyPair generates a 2048-bit RSA key pair for tests and
// examples. Panics on RNG failure (extremely rare; treated as a programmer
// error in the test caller's environment).
func MustGenerateKeyPair() (*rsa.PrivateKey, *rsa.PublicKey) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		e := errcode.Assertion("authtest: failed to generate test RSA key pair")
		e.InternalMessage = err.Error()
		panic(panicregister.Approved("authtest-rsa-keypair", e))
	}
	return priv, &priv.PublicKey
}

// MustNewKeySet creates a *auth.KeySet from a freshly generated 2048-bit RSA
// key pair. Panics on construction error. clk is required; pass clock.Real()
// from the test composition root or clockmock.New(...) for time-controlled
// tests.
func MustNewKeySet(clk clock.Clock) (*auth.KeySet, *rsa.PrivateKey, *rsa.PublicKey) {
	priv, pub := MustGenerateKeyPair()
	ks, err := auth.NewKeySet(priv, pub, clk)
	if err != nil {
		e := errcode.Assertion("authtest: failed to create test key set")
		e.InternalMessage = err.Error()
		panic(panicregister.Approved("authtest-keyset", e))
	}
	return ks, priv, pub
}

// MustNewKeyProvider creates an auth.KeyProvider with ephemeral RSA and HMAC
// keys for tests. Panics on construction error. clk is required.
//
// WARNING: hardcoded HMAC secret. DO NOT use in any production or staging path.
func MustNewKeyProvider(clk clock.Clock) auth.KeyProvider {
	ks, _, _ := MustNewKeySet(clk)
	ring, err := auth.NewHMACKeyRing([]byte("test-hmac-secret-at-least-32-bytes!!"), nil)
	if err != nil {
		e := errcode.Assertion("authtest: failed to create test HMAC key ring")
		e.InternalMessage = err.Error()
		panic(panicregister.Approved("authtest-hmac-keyring", e))
	}
	return auth.NewStaticKeyProvider(ks, ring)
}
