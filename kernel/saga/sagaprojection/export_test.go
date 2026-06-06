package sagaprojection

import "github.com/ghbvf/gocell/kernel/cellvocab"

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
