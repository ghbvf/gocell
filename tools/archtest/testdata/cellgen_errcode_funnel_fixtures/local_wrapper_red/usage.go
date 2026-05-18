// Package local_wrapper_red verifies F7: a package-local error wrapper
// that internally calls errors.New is the most plausible AI-creative
// escape from a simple "ban fmt.Errorf" rule. The Hard guarantee is
// unavoidable inspection of the inner construction:
//
//   - Inner errors.New: STEP 2 fails at the wrapper definition body —
//     (errors, New) is in the cellgenErrConstructorBlacklist.
//   - Outer newErr caller: callee resolves to a same-package func; not in
//     blacklist → not flagged at the call site. The wrapper's body is
//     itself scanned, so the construction grounds out at the inner call.
//
// 1 violation expected (wrapper body errors.New). A chain of N
// same-package helpers all forward; the chain inevitably terminates in a
// call to a non-same / non-errcode constructor, which is the inspection
// point — no escape sequence hides an error construction.
package local_wrapper_red

import "errors"

func newErr(msg string) error {
	return errors.New(msg)
}

// foo exercises the same-package forwarding path; intentionally not flagged
// (newErr's body is the funnel inspection site).
func foo() error {
	return newErr("via local wrapper")
}
