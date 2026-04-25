package wrapper

import "fmt"

// recoverAndFinishWithRedactor is the shared span-teardown helper for
// WrapConsumer's panic path. It must be called from within a deferred
// closure so that recover() captures the in-flight panic.
//
// Behaviour:
//   - rec == nil  → no-op (normal path; caller is responsible for span.End).
//   - rec != nil  → SetStatus(Error, "panic") + RecordError(redact(err))
//   - span.End + re-panic.
//
// Re-panicking preserves the original stack for outer Recovery middleware /
// runSubscribe goroutine supervisors.
//
// redact applies the caller's ErrorRedactor to whatever error surface the
// panic produced (err-typed panic → err; any other value → wrapped via
// fmt.Errorf). Pass identityRedactor when redaction is disabled.
func recoverAndFinishWithRedactor(span Span, rec any, redact ErrorRedactor) {
	if rec == nil {
		return
	}
	span.SetStatus(StatusError, "panic")
	var err error
	if e, ok := rec.(error); ok {
		err = e
	} else {
		err = fmt.Errorf("panic: %v", rec)
	}
	if redact == nil {
		redact = identityRedactor
	}
	span.RecordError(redact(err))
	span.End()
	panic(rec)
}
