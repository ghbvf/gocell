package certlifecycle

import "testing"

func TestStateClosedValueSet(t *testing.T) {
	t.Parallel()
	// AllStates is the completeness anchor: a state added to the type but not to
	// AllStates (or vice versa) fails here.
	if got := len(AllStates()); got != 8 {
		t.Fatalf("AllStates length = %d, want 8 (closed lifecycle vocabulary)", got)
	}
	// Every AllStates entry must round-trip through its canonical string.
	for _, s := range AllStates() {
		if s.IsZero() {
			t.Errorf("AllStates contains the zero State (string=%q)", s.String())
		}
		got, ok := ParseState(s.String())
		if !ok {
			t.Errorf("ParseState(%q) ok=false, want true", s.String())
			continue
		}
		if got != s {
			t.Errorf("ParseState(%q) = %+v, want %+v (round-trip)", s.String(), got, s)
		}
	}
}

func TestParseStateRejectsUnknown(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "bogus", "Active", "ACTIVE", "renew", "near_expiry"} {
		if got, ok := ParseState(in); ok {
			t.Errorf("ParseState(%q) ok=true (got %+v), want fail-closed false", in, got)
		}
	}
}

func TestStateZeroValue(t *testing.T) {
	t.Parallel()
	var z State
	if !z.IsZero() {
		t.Error("zero State.IsZero() = false, want true")
	}
	if z.String() != "" {
		t.Errorf("zero State.String() = %q, want empty", z.String())
	}
	if z.renewable() {
		t.Error("zero State.renewable() = true, want false (fail-closed)")
	}
}

func TestStateRenewableTruthTable(t *testing.T) {
	t.Parallel()
	// Only active is renewable; every other state (incl. revoked terminal and the
	// pre-active / derived states) must be skipped by the renewal sweep.
	cases := []struct {
		state State
		want  bool
	}{
		{StateRequested(), false},
		{StateIssued(), false},
		{StateActive(), true},
		{StateNearExpiry(), false},
		{StateRenewing(), false},
		{StateRotated(), false},
		{StateRevoked(), false},
		{StateExpired(), false},
	}
	for _, tc := range cases {
		if got := tc.state.renewable(); got != tc.want {
			t.Errorf("State(%q).renewable() = %v, want %v", tc.state.String(), got, tc.want)
		}
	}
}

func TestStateAccessorsDistinct(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, s := range AllStates() {
		if seen[s.String()] {
			t.Errorf("duplicate state string %q across accessors", s.String())
		}
		seen[s.String()] = true
	}
}
