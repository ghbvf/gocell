package sagaprojection_test

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/saga/sagaprojection"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestParseSagaJournalEventID covers the validation contract of
// ParseSagaJournalEventID: prefix, positive globalSeq, '@' separator, and a
// non-empty instanceID. Every malformation returns a non-nil *errcode.Error of
// KindInvalid (NOT wrapped permanent — the caller decides disposition).
func TestParseSagaJournalEventID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		eventID string
		wantSeq int64
		wantID  string
		wantErr bool
	}{
		{
			name:    "valid",
			eventID: "saga-journal:42@ord-abc",
			wantSeq: 42,
			wantID:  "ord-abc",
		},
		{
			// LastIndex splits on the FINAL '@', so the seq portion becomes
			// "7@ns" which is not a base-10 int64 → error. The split-on-last-'@'
			// contract (mirroring EventID's documented consumer guidance) only
			// keeps the instanceID suffix intact; a seq that itself ends up with
			// an embedded '@' is rejected. EventID() never produces such a string
			// for a numeric globalSeq, so this is a defensive malformation case.
			name:    "embedded @ makes seq unparseable",
			eventID: "saga-journal:7@ns@inst",
			wantErr: true,
		},
		{
			name:    "missing prefix",
			eventID: "wrong-prefix:1@ord-x",
			wantErr: true,
		},
		{
			name:    "seq zero",
			eventID: "saga-journal:0@ord-x",
			wantErr: true,
		},
		{
			name:    "seq negative",
			eventID: "saga-journal:-3@ord-x",
			wantErr: true,
		},
		{
			name:    "seq non-numeric",
			eventID: "saga-journal:abc@ord-x",
			wantErr: true,
		},
		{
			name:    "no at separator",
			eventID: "saga-journal:1-no-at-sign",
			wantErr: true,
		},
		{
			name:    "empty instanceID",
			eventID: "saga-journal:5@",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seq, id, err := sagaprojection.ParseSagaJournalEventID(tc.eventID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSagaJournalEventID(%q) = (%d, %q, nil), want error", tc.eventID, seq, id)
				}
				var ec *errcode.Error
				if !errors.As(err, &ec) {
					t.Fatalf("error = %T, want *errcode.Error", err)
				}
				if ec.Kind != errcode.KindInvalid {
					t.Errorf("error kind = %v, want KindInvalid", ec.Kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSagaJournalEventID(%q): unexpected error: %v", tc.eventID, err)
			}
			if seq != tc.wantSeq {
				t.Errorf("globalSeq = %d, want %d", seq, tc.wantSeq)
			}
			if id != tc.wantID {
				t.Errorf("instanceID = %q, want %q", id, tc.wantID)
			}
		})
	}
}

// TestParseSagaJournalEventID_RoundTrip asserts that the wire string produced by
// the production carrier's EventID() always parses back to the same
// (globalSeq, instanceID) — the parser is the exact inverse of the formatter, so
// a format change on either side breaks this test.
func TestParseSagaJournalEventID_RoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		globalSeq  int64
		instanceID string
	}{
		{1, "ord-1001"},
		{256, "ord-with-dashes-and-numbers-42"},
		{9999999, "ord-seed-inst-7"},
	}

	for _, c := range cases {
		eventID := sagaprojection.EventIDForTest(c.globalSeq, c.instanceID)
		seq, id, err := sagaprojection.ParseSagaJournalEventID(eventID)
		if err != nil {
			t.Fatalf("round-trip parse of %q: %v", eventID, err)
		}
		if seq != c.globalSeq || id != c.instanceID {
			t.Errorf("round-trip(%q) = (%d, %q), want (%d, %q)",
				eventID, seq, id, c.globalSeq, c.instanceID)
		}
	}
}
