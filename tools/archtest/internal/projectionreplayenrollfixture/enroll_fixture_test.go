//go:build archtest_fixture

package projectionreplayenrollfixture

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
)

// TestEnrolledReplaySourceConformance enrolls enrolledReplaySource in the shared
// conformance harness. The enroll archtest's RED fixture scans this call to credit
// enrolledReplaySource (positive direction). unenrolledReplaySource is intentionally
// absent from any RunReplaySourceConformance call.
func TestEnrolledReplaySourceConformance(t *testing.T) {
	clk := clockmock.New(time.Now())
	seedEntries := func(n int) []outbox.Entry {
		entries := make([]outbox.Entry, n)
		for i := range entries {
			e, err := outbox.NewEntry(clk, context.Background(), "topic.v1", []byte(`{}`))
			if err != nil {
				t.Fatalf("outbox.NewEntry: %v", err)
			}
			entries[i] = e
		}
		return entries
	}
	projectiontest.RunReplaySourceConformance(t, newEnrolledReplaySource(), seedEntries)
}
