//go:build archtest_fixture

package projectioncursorenrollfixture

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
)

// TestEnrolledCursorConformance enrolls enrolledCursor in the shared Cursor
// conformance harness. The enroll archtest's RED fixture scans this call to
// credit enrolledCursor (positive direction). unenrolledCursor is intentionally
// absent from any RunCursorConformance call.
func TestEnrolledCursorConformance(t *testing.T) {
	clk := clockmock.New(time.Now())
	cur := newEnrolledCursor()
	newEntry := func() projection.ProjectionEvent {
		e, err := outbox.NewEntry(clk, context.Background(), "topic.v1", []byte(`{}`))
		if err != nil {
			t.Fatalf("outbox.NewEntry: %v", err)
		}
		return e
	}
	seed := func(n int) []projection.ProjectionEvent {
		entries := make([]projection.ProjectionEvent, n)
		for i := range entries {
			e := newEntry()
			cur.add(e)
			entries[i] = e
		}
		return entries
	}
	projectiontest.RunCursorConformance(t, cur, seed, newEntry)
}
