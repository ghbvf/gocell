package wrapper

import "fmt"

// recoverAndFinish is the shared span-teardown helper for HTTPHandler and
// WrapConsumer. It must be called from within a deferred closure so that
// recover() captures the in-flight panic.
//
// Behaviour:
//   - rec == nil  → no-op (normal path; caller is responsible for span.End).
//   - rec != nil  → SetStatus(Error, "panic") + RecordError + span.End + re-panic.
//
// Re-panicking preserves the original stack for outer Recovery middleware /
// runSubscribe goroutine supervisors.
func recoverAndFinish(span Span, rec any) {
	if rec == nil {
		return
	}
	span.SetStatus(StatusError, "panic")
	if err, ok := rec.(error); ok {
		span.RecordError(err)
	} else {
		span.RecordError(fmt.Errorf("panic: %v", rec))
	}
	span.End()
	panic(rec)
}
