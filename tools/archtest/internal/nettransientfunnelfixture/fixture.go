//go:build archtest_fixture

// Package nettransientfunnelfixture is the ADAPTER-NET-TRANSIENT-FUNNEL-01
// reverse self-check corpus. It models the two detectors implemented in
// tools/archtest/adapter_net_transient_funnel_test.go:
//
//   - allowlist detector: any `var x net.Error` declaration in a function
//     name NOT in the allowlist is RED.
//   - helper-form detector: any function body that declares `var x net.Error`
//     AND additionally references `.Timeout()` or a narrower *net.OpError
//     type assertion is RED (mimics a regressed IsTransientNet body).
//
// The fixture is loaded via RunTypedFixture with the archtest_fixture build
// tag; bypassing the reverse self-check requires editing this real source.
package nettransientfunnelfixture

import (
	"errors"
	"net"
)

// allowedSite is in the fixture-local allowlist used by the reverse self-check
// test; it must NOT be reported by the allowlist detector.
func allowedSite(err error) bool {
	var n net.Error
	return errors.As(err, &n)
}

// forbiddenSite is OUTSIDE the fixture-local allowlist — the allowlist
// detector must report it (RED).
func forbiddenSite(err error) bool {
	var n net.Error
	return errors.As(err, &n)
}

// regressedHelperTimeout mimics what a regressed IsTransientNet would look
// like: it re-adds the deprecated Timeout() filter. The helper-form detector
// must report it (RED).
func regressedHelperTimeout(err error) bool {
	var n net.Error
	if errors.As(err, &n) && n.Timeout() {
		return true
	}
	return false
}

// regressedHelperNarrow narrows to *net.OpError specifically — also a
// regression of the helper form. The helper-form detector must report it
// (RED).
//
// Narrowing to *net.OpError alone misses *net.DNSError, *net.AddrError, and
// other net.Error implementations — the helper must key on the net.Error
// interface, not on any specific subtype.
func regressedHelperNarrow(err error) bool {
	var n net.Error
	if !errors.As(err, &n) {
		return false
	}
	var op *net.OpError
	return errors.As(err, &op)
}

var (
	_ = allowedSite
	_ = forbiddenSite
	_ = regressedHelperTimeout
	_ = regressedHelperNarrow
)
