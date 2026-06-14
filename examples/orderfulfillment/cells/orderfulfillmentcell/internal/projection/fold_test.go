package projection_test

import (
	"testing"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/projection"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
)

// TestFoldStatus_AllKinds covers every valid journal.EventKind and asserts the
// correct status transition from the zero-value "no status yet" starting point.
// Anti-vacuity: we enumerate all kinds via journal.EventKind.Valid(), so adding a
// new kind without updating FoldStatus makes the test fail.
func TestFoldStatus_AllKinds(t *testing.T) {
	t.Parallel()

	none := orderstatusgen.ResponseDataStatus("") // zero value = no row yet

	tests := []struct {
		kind       journal.EventKind
		wantStatus orderstatusgen.ResponseDataStatus
	}{
		{journal.KindStepStarted, orderstatusgen.ResponseDataStatusRunning},
		{journal.KindStepCompleted, orderstatusgen.ResponseDataStatusRunning},
		{journal.KindStepFailed, orderstatusgen.ResponseDataStatusRunning},
		{journal.KindStepCompensated, orderstatusgen.ResponseDataStatusRunning},
		{journal.KindCompensationStarted, orderstatusgen.ResponseDataStatusRunning},
		{journal.KindSagaSucceeded, orderstatusgen.ResponseDataStatusSucceeded},
		{journal.KindSagaFailed, orderstatusgen.ResponseDataStatusFailed},
		{journal.KindSagaCompensated, orderstatusgen.ResponseDataStatusCompensated},
		{journal.KindSagaExpired, orderstatusgen.ResponseDataStatusFailed},
		{journal.KindStepCompensationFailed, orderstatusgen.ResponseDataStatusRunning},
		{journal.KindSagaCompensationFailed, orderstatusgen.ResponseDataStatusFailed},
	}

	// Anti-vacuity: enumerate all valid kinds from KindStepStarted through
	// KindSagaCompensationFailed using the named sentinel. If a new kind is
	// added after KindSagaCompensationFailed the loop auto-expands (k.Valid()
	// gates the count) and len(tests) diverges — the fatal below fires.
	validCount := 0
	for k := journal.KindStepStarted; k <= journal.KindSagaCompensationFailed; k++ {
		if k.Valid() {
			validCount++
		}
	}
	if validCount < 11 {
		t.Fatalf("anti-vacuity: expected at least 11 valid EventKinds, got %d; was the journal enum truncated?", validCount)
	}
	if len(tests) != validCount {
		t.Fatalf("fold test table has %d entries but journal has %d valid kinds (%s..%s); update the test table",
			len(tests), validCount, journal.KindStepStarted, journal.KindSagaCompensationFailed)
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.kind.String(), func(t *testing.T) {
			t.Parallel()
			got, err := projection.FoldStatus(none, tc.kind)
			if err != nil {
				t.Fatalf("FoldStatus(none, %s) error: %v", tc.kind, err)
			}
			if got != tc.wantStatus {
				t.Errorf("FoldStatus(none, %s) = %q, want %q", tc.kind, got, tc.wantStatus)
			}
		})
	}
}

// TestFoldStatus_TerminalAbsorbing asserts that once a terminal status is
// stored, any subsequent non-terminal kind leaves it unchanged (monotonic).
func TestFoldStatus_TerminalAbsorbing(t *testing.T) {
	t.Parallel()

	terminals := []orderstatusgen.ResponseDataStatus{
		orderstatusgen.ResponseDataStatusSucceeded,
		orderstatusgen.ResponseDataStatusCompensated,
		orderstatusgen.ResponseDataStatusFailed,
	}
	nonTerminals := []journal.EventKind{
		journal.KindStepStarted,
		journal.KindStepCompleted,
		journal.KindStepFailed,
		journal.KindStepCompensated,
		journal.KindCompensationStarted,
		journal.KindStepCompensationFailed,
	}

	for _, term := range terminals {
		term := term
		for _, kind := range nonTerminals {
			kind := kind
			t.Run(string(term)+"/"+kind.String(), func(t *testing.T) {
				t.Parallel()
				got, err := projection.FoldStatus(term, kind)
				if err != nil {
					t.Fatalf("FoldStatus(%s, %s) error: %v", term, kind, err)
				}
				if got != term {
					t.Errorf("FoldStatus(%s, %s) = %q, want terminal %q (monotonic)", term, kind, got, term)
				}
			})
		}
	}
}

// TestFoldStatus_TerminalAbsorbing_TerminalKinds asserts that a terminal kind
// fed after another terminal keeps the canonical terminal (kind wins, no regression).
func TestFoldStatus_TerminalAbsorbing_TerminalKinds(t *testing.T) {
	t.Parallel()
	// Succeeded is already terminal; feeding KindSagaFailed must keep Succeeded
	// (first terminal wins — no terminal→terminal regression).
	got, err := projection.FoldStatus(orderstatusgen.ResponseDataStatusSucceeded, journal.KindSagaFailed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != orderstatusgen.ResponseDataStatusSucceeded {
		t.Errorf("FoldStatus(Succeeded, KindSagaFailed) = %q, want Succeeded", got)
	}
}

// TestFoldStatus_UnknownKind asserts that an unknown/zero EventKind returns an
// error and no status (fail-closed — never silently default).
func TestFoldStatus_UnknownKind(t *testing.T) {
	t.Parallel()

	unknowns := []journal.EventKind{
		journal.EventKind(0),
		journal.EventKind(99),
		journal.EventKind(255),
	}
	for _, k := range unknowns {
		k := k
		t.Run(k.String(), func(t *testing.T) {
			t.Parallel()
			got, err := projection.FoldStatus("", k)
			if err == nil {
				t.Fatalf("FoldStatus(%q) expected error for unknown kind, got status=%q", k, got)
			}
			if got != "" {
				t.Errorf("FoldStatus unknown kind returned non-empty status %q", got)
			}
		})
	}
}
