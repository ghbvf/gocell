package wrapper

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// recoverAndFinish is the shared span-teardown helper for WrapConsumer's
// panic path. It must be called from within a deferred closure so that
// recover() captures the in-flight panic.
//
// Behavior:
//   - rec == nil  → no-op (normal path; caller is responsible for span.End).
//   - rec != nil  → SetStatus(Error, "panic") + RecordError(redaction.RedactError(err))
//   - span.End + re-panic.
//
// Re-panicking preserves the original stack for outer Recovery middleware /
// runSubscribe goroutine supervisors. Redaction is hardcoded — see
// pkg/redaction for the fail-closed rationale.
func recoverAndFinish(span Span, rec any) {
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
	span.RecordError(redaction.RedactError(err))
	span.End()
	panic(panicregister.Approved("lifecycle-recover-rethrow-to-recovery-middleware", rec))
}
