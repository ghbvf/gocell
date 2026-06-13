package journal_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/saga/journal"
)

// TestParseEventKind_RoundTripAllKinds is the AI-HARD machine guard for the
// String() <-> ParseEventKind round-trip: every Valid() EventKind must parse
// back from its own String() label to the identical kind. This is the reverse
// of EventKind.String(); the saga-journal projection carrier serializes
// Kind.String() into its payload envelope and consumers ParseEventKind it back,
// so a kind added with a String() case but no ParseEventKind case (or vice
// versa) would silently mis-route — this test goes red instead.
//
// Anti-vacuity: the loop asserts it covered at least the full known set so a
// future narrowing of Valid() cannot make the test pass while covering nothing.
func TestParseEventKind_RoundTripAllKinds(t *testing.T) {
	t.Parallel()

	covered := 0
	// Enumerate every kind in the Valid() range (mirrors
	// runtime/saga.TestFoldEvents_AllKindsHandled).
	for k := journal.KindStepStarted; k <= journal.KindSagaCompensationFailed; k++ {
		if !k.Valid() {
			t.Fatalf("kind %d in [KindStepStarted, KindSagaCompensationFailed] is not Valid(); "+
				"Valid()'s upper bound must track the last declared kind", k)
		}
		label := k.String()
		got, ok := journal.ParseEventKind(label)
		if !ok {
			t.Errorf("ParseEventKind(%q) ok=false; every Valid() kind's String() label must round-trip", label)
			continue
		}
		if got != k {
			t.Errorf("ParseEventKind(%q) = %d, want %d (round-trip mismatch)", label, got, k)
		}
		covered++
	}

	const wantAtLeast = 11 // 11 declared kinds as of #1210 (KindSagaCompensationFailed)
	if covered < wantAtLeast {
		t.Fatalf("round-trip covered only %d kinds, want >= %d; anti-vacuity guard — "+
			"the Valid() range must enumerate every declared EventKind", covered, wantAtLeast)
	}
}

// TestParseEventKind_Unknown asserts ParseEventKind fail-closes (ok=false) for
// any label that is not a known kind: the empty string, free text, and the
// String() fallback form "eventkind(N)" for an out-of-range kind. A consumer
// MUST treat ok=false as an unrecoverable/permanent classification, never as a
// silent default.
func TestParseEventKind_Unknown(t *testing.T) {
	t.Parallel()

	for _, label := range []string{
		"",
		"not_a_kind",
		"SAGA_SUCCEEDED",      // case-sensitive: labels are lower snake_case
		"eventkind(0)",        // String() fallback for the zero value
		"eventkind(99)",       // String() fallback for an out-of-range kind
		"saga_succeeded ",     // trailing space must not match
	} {
		if got, ok := journal.ParseEventKind(label); ok {
			t.Errorf("ParseEventKind(%q) = (%d, true), want (_, false) — unknown label must fail-closed", label, got)
		}
	}
}
