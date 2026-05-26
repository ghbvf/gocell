// Package funcvalueindirectionred is the teeth fixture for
// TestGovernanceEmitterConstructorsNeverFuncValue. It takes a stand-in emitter
// constructor (named to match governanceEmitterName's name-based match) as a
// func VALUE rather than calling it directly — the func-value indirection that
// would evade GOVERNANCE-RULE-CODE-DETECT-BINDING-01 / -ERROR-FIX-FIELD-01.
// The reverse self-check MUST flag this; if it returns zero, the scan is vacuous.
package funcvalueindirectionred

type result struct{}

func newError() result { return result{} }

// violate assigns newError to a variable instead of calling it directly. The
// `newError` on the RHS is a func-value use, not a call callee — exactly the
// blind-spot form the reverse self-check must detect.
func violate() result {
	emit := newError // func-value indirection — must be detected
	return emit()
}
