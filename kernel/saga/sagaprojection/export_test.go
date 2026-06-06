package sagaprojection

import (
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/saga"
)

// NewUnseededEventForTest returns a *sagaProjectionEvent with globalSeq == 0
// (the unseeded sentinel). This value is never returned by a conforming
// GlobalReader (which guarantees GlobalSeq >= 1), so SagaJournalSource.Position
// will return a permanent error for it — exercising Cursor invariant #4 in
// RunCursorConformance.
//
// This function is compiled ONLY in test binaries (export_test.go lives in the
// production package but is excluded from production builds by the Go test
// toolchain). It is not part of the sagaprojection public API.
func NewUnseededEventForTest() cellvocab.ProjectionEvent {
	return &sagaProjectionEvent{globalSeq: 0}
}

// StatusSucceededForTest exposes saga.StatusSucceeded for the external test
// package (sagaprojection_test) which needs to call j.MarkTerminal with a
// terminal status. Compiled ONLY in test binaries.
const StatusSucceededForTest = saga.StatusSucceeded
