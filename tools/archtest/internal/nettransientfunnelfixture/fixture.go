//go:build archtest_fixture

// Package nettransientfunnelfixture is the ADAPTER-NET-TRANSIENT-FUNNEL-01 +
// TRANSIENT-NET-HELPER-FORM-01 reverse self-check corpus. It models four
// detector paths implemented in
// tools/archtest/adapter_net_transient_funnel_test.go:
//
//   - allowlist detector (scanNetErrorDeclarations): any `var x net.Error`
//     declaration whose enclosing function is OUTSIDE the allowlist is RED.
//     Alias declarations (`type myNetErr = net.Error; var n myNetErr`) are
//     equally detected via types.Unalias.
//   - narrow-form detector (scanErrorsAsNetSubtypeNarrow): any
//     `errors.As(err, &x)` where x is typed `*net.X` (X != "Error") and the
//     enclosing function is outside the allowlist is RED — locks the
//     concrete-subtype-as-narrowing bypass form.
//   - helper-form negative scan (scanHelperFormViolations): `.Timeout()`
//     SelectorExpr or `*net.X` StarExpr inside the named helper body is RED.
//   - helper-form positive scan (scanHelperPositiveErrorsAs): the named
//     helper body MUST contain at least one canonical
//     `errors.As(err, &netErrVar)` call where netErrVar is typed net.Error;
//     empty / stub bodies (return false / return true) are RED.
//
// The fixture is loaded via Run(t, Fixture(...)) with the archtest_fixture
// build tag; bypassing the reverse self-check requires editing this real source.
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

// regressedHelperEmptyFalse mimics a stubbed helper that always returns
// false — would silently disable transient classification. The positive
// shape check (scanHelperPositiveErrorsAs) must report it (RED) because the
// body contains no `errors.As(err, &netErrVar)` call.
func regressedHelperEmptyFalse(_ error) bool {
	return false
}

// regressedHelperEmptyTrue mimics a stubbed helper that always returns
// true — would silently force every error to transient (retry-burn DoS).
// The positive shape check must report it (RED) for the same reason.
func regressedHelperEmptyTrue(_ error) bool {
	return true
}

// myNetErr is a type alias for net.Error — the Go 1.22+ type-alias
// declaration form. The allowlist detector must report the
// `var n myNetErr` site after types.Unalias resolution; without Unalias
// it would silently slip past.
type myNetErr = net.Error

// forbiddenAliasNetError uses the type-alias form to declare a net.Error
// variable outside the fixture allowlist. The allowlist detector
// (scanNetErrorDeclarations) must report it (RED) — alias transparency is
// only available through types.Unalias.
func forbiddenAliasNetError(err error) bool {
	var n myNetErr
	return errors.As(err, &n)
}

// forbiddenOpErrorNarrow uses the concrete-subtype narrow form
// (`var op *net.OpError; errors.As(err, &op)`) to bypass the
// `var x net.Error` declaration scan. The narrow-form detector
// (scanErrorsAsNetSubtypeNarrow) must report it (RED) — semantically
// equivalent to `var n net.Error` but with a concrete subtype anchor.
func forbiddenOpErrorNarrow(err error) bool {
	var op *net.OpError
	return errors.As(err, &op)
}

var (
	_ = allowedSite
	_ = forbiddenSite
	_ = regressedHelperTimeout
	_ = regressedHelperNarrow
	_ = regressedHelperEmptyFalse
	_ = regressedHelperEmptyTrue
	_ = forbiddenAliasNetError
	_ = forbiddenOpErrorNarrow
)
