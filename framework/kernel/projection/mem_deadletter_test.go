package projection_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/projection"
)

func TestMemDeadLetterStore_RecordAndList(t *testing.T) {
	t.Parallel()
	s := projection.NewMemDeadLetterStore()
	ctx := context.Background()
	occurred := time.Unix(1700000000, 0).UTC()

	dl := projection.DeadLetter{
		CellID:       "orderfulfillmentcell",
		ProjectionID: "orderstatus",
		GlobalSeq:    42,
		EventID:      "saga-journal:42@inst-1",
		Stream:       "saga-journal",
		ErrorType:    "ERR_VALIDATION_FAILED",
		ErrorMessage: "permanent: unknown event kind",
		OccurredAt:   occurred,
	}
	if err := s.Record(ctx, dl); err != nil {
		t.Fatalf("Record: unexpected error: %v", err)
	}

	got := s.Records()
	if len(got) != 1 {
		t.Fatalf("Records: want 1, got %d", len(got))
	}
	if got[0] != dl {
		t.Fatalf("Records: want %+v, got %+v", dl, got[0])
	}
}

func TestMemDeadLetterStore_RecordIdempotentOnSeq(t *testing.T) {
	t.Parallel()
	s := projection.NewMemDeadLetterStore()
	ctx := context.Background()
	dl := projection.DeadLetter{CellID: "c", ProjectionID: "p", GlobalSeq: 7, EventID: "e7"}

	// Re-recording the same (cell, projection, globalSeq) is a no-op (mirrors PG
	// ON CONFLICT DO NOTHING — a re-driven skip must not duplicate).
	for i := 0; i < 3; i++ {
		if err := s.Record(ctx, dl); err != nil {
			t.Fatalf("Record #%d: unexpected error: %v", i, err)
		}
	}
	if got := s.Records(); len(got) != 1 {
		t.Fatalf("Records: want 1 after 3 identical records, got %d", len(got))
	}

	// A different globalSeq is a distinct entry.
	if err := s.Record(ctx, projection.DeadLetter{CellID: "c", ProjectionID: "p", GlobalSeq: 8}); err != nil {
		t.Fatalf("Record seq 8: %v", err)
	}
	if got := s.Records(); len(got) != 2 {
		t.Fatalf("Records: want 2 distinct seqs, got %d", len(got))
	}
}

func TestMemDeadLetterStore_RecordsReturnsCopy(t *testing.T) {
	t.Parallel()
	s := projection.NewMemDeadLetterStore()
	_ = s.Record(context.Background(), projection.DeadLetter{CellID: "c", ProjectionID: "p", GlobalSeq: 1})
	got := s.Records()
	got[0].EventID = "mutated"
	if s.Records()[0].EventID == "mutated" {
		t.Fatal("Records must return a copy; caller mutation leaked into the store")
	}
}
