package sagajournaltest

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
)

func TestConformance_MemJournal(t *testing.T) {
	RunConformanceSuite(t, func(t *testing.T) (journal.Journal, *clockmock.FakeClock, func()) {
		clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		j, err := journal.NewMemJournal(clk)
		if err != nil {
			t.Fatalf("NewMemJournal: %v", err)
		}
		return j, clk, func() {}
	})
}
