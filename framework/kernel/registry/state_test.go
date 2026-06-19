package registry

import "testing"

// TestRegistrationState_FrozenRegistry pins the closed RegistrationState value
// set to exactly the 8 documented states and proves the registry is non-vacuous
// (anti-vacuity for the sealed-type value closure). Adding a new state without
// registering it in allRegistrationStates — or renaming a wire value — trips
// this test. Mirrors transport.TestTransportMode_FrozenRegistry.
func TestRegistrationState_FrozenRegistry(t *testing.T) {
	t.Parallel()

	got := make(map[string]int, len(allRegistrationStates))
	for _, s := range allRegistrationStates {
		got[s.String()]++
	}

	want := map[string]int{
		"submitted":        1,
		"probing":          1,
		"conformant":       1,
		"pending-approval": 1,
		"approved":         1,
		"rejected":         1,
		"active":           1,
		"retired":          1,
	}
	if len(got) != len(want) {
		t.Fatalf("registration state value set = %v, want %v", got, want)
	}
	for v, n := range want {
		if got[v] != n {
			t.Errorf("registration state %q count = %d, want %d (registry: %v)", v, got[v], n, got)
		}
	}
}

// TestRegistrationState_Accessors verifies each package-private singleton renders
// the expected wire string via its exported accessor (the sole way an external
// package can name a state — reassigning a func is a compile error).
func TestRegistrationState_Accessors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got  RegistrationState
		want string
	}{
		{StateSubmitted(), "submitted"},
		{StateProbing(), "probing"},
		{StateConformant(), "conformant"},
		{StatePendingApproval(), "pending-approval"},
		{StateApproved(), "approved"},
		{StateRejected(), "rejected"},
		{StateActive(), "active"},
		{StateRetired(), "retired"},
	}
	for _, tc := range cases {
		if got := tc.got.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
		if tc.got.IsZero() {
			t.Errorf("%q reported IsZero", tc.want)
		}
		if !tc.got.isRegistered() {
			t.Errorf("%q not registered", tc.want)
		}
	}
}

// TestRegistrationState_ZeroValueFailClosed verifies a forged zero value renders
// fail-closed ("unknown"), reports IsZero, and is NOT registered.
func TestRegistrationState_ZeroValueFailClosed(t *testing.T) {
	t.Parallel()
	var zero RegistrationState
	if got := zero.String(); got != RegistrationStateUnknown {
		t.Errorf("zero String() = %q, want %q", got, RegistrationStateUnknown)
	}
	if !zero.IsZero() {
		t.Error("zero value should report IsZero")
	}
	if zero.isRegistered() {
		t.Error("zero value must not be registered")
	}
	if zero.IsTerminal() {
		t.Error("zero value must not be terminal")
	}
}

// TestRegistrationState_IsTerminal verifies only rejected and retired are
// terminal (no outgoing transitions).
func TestRegistrationState_IsTerminal(t *testing.T) {
	t.Parallel()
	terminal := map[RegistrationState]bool{
		StateRejected(): true,
		StateRetired():  true,
	}
	for _, s := range allRegistrationStates {
		if got, want := s.IsTerminal(), terminal[s]; got != want {
			t.Errorf("%q IsTerminal() = %v, want %v", s, got, want)
		}
	}
}

// TestParseState_RoundTrips verifies ParseState is the exact inverse of String()
// for every producible state — the round-trip a durable store relies on to
// reconstruct a sealed state from its `state TEXT` column.
func TestParseState_RoundTrips(t *testing.T) {
	t.Parallel()
	for _, want := range allRegistrationStates {
		got, ok := ParseState(want.String())
		if !ok {
			t.Errorf("ParseState(%q) ok=false, want true", want.String())
			continue
		}
		if got != want {
			t.Errorf("ParseState(%q) = %q, want %q", want.String(), got, want)
		}
	}
}

// TestParseState_EmptyIsZeroSentinel verifies the empty string maps to the zero
// sentinel (ok=true) — the persisted From of an initial submit event.
func TestParseState_EmptyIsZeroSentinel(t *testing.T) {
	t.Parallel()
	got, ok := ParseState("")
	if !ok {
		t.Fatal("ParseState(\"\") ok=false, want true (zero sentinel)")
	}
	if !got.IsZero() {
		t.Errorf("ParseState(\"\") = %q, want zero value", got)
	}
}

// TestParseState_UnknownRejected verifies an unrecognized label — including the
// fail-closed "unknown" render — is rejected, never silently folded to a state.
func TestParseState_UnknownRejected(t *testing.T) {
	t.Parallel()
	for _, s := range []string{RegistrationStateUnknown, "garbage", "Submitted", "SUBMITTED"} {
		if got, ok := ParseState(s); ok {
			t.Errorf("ParseState(%q) ok=true (=%q), want false", s, got)
		}
	}
}

// TestStateNames verifies StateNames returns a non-empty slice whose elements
// match the closed allRegistrationStates registry exactly (anti-vacuity: same
// count, same values), each of which round-trips through ParseState.
func TestStateNames(t *testing.T) {
	t.Parallel()
	got := StateNames()
	if len(got) != len(allRegistrationStates) {
		t.Fatalf("StateNames() len=%d, want %d", len(got), len(allRegistrationStates))
	}
	seen := make(map[string]int, len(got))
	for _, name := range got {
		seen[name]++
		if _, ok := ParseState(name); !ok {
			t.Errorf("StateNames() returned %q which ParseState rejects", name)
		}
	}
	for _, s := range allRegistrationStates {
		if seen[s.v] != 1 {
			t.Errorf("StateNames() missing or duplicate %q", s.v)
		}
	}
}

// TestStateNames_IsCopy verifies mutation of the returned slice does not affect
// subsequent calls (fresh copy invariant).
func TestStateNames_IsCopy(t *testing.T) {
	t.Parallel()
	first := StateNames()
	first[0] = "mutated"
	second := StateNames()
	if second[0] == "mutated" {
		t.Error("StateNames() returned same backing array; mutation affected subsequent call")
	}
}
