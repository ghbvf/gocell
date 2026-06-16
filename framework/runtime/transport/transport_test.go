package transport

import "testing"

// TestTransportMode_FrozenRegistry pins the closed transport-mode value set to
// exactly {in_proc, remote} and proves the registry is non-vacuous (anti-vacuity
// for the sealed-type value closure). Adding a new mode without registering it in
// allTransportModes — or renaming a wire value — trips this test.
func TestTransportMode_FrozenRegistry(t *testing.T) {
	t.Parallel()

	got := make(map[string]int, len(allTransportModes))
	for _, m := range allTransportModes {
		got[m.String()]++
	}

	want := map[string]int{"in_proc": 1, "remote": 1}
	if len(got) != len(want) {
		t.Fatalf("transport mode value set = %v, want %v", got, want)
	}
	for v, n := range want {
		if got[v] != n {
			t.Errorf("transport mode %q count = %d, want %d (registry: %v)", v, got[v], n, got)
		}
	}
}

// TestTransportMode_Accessors verifies the package-private singletons render the
// expected wire strings via their exported accessor functions (the sole way an
// external package can name a mode — reassigning a func is a compile error).
func TestTransportMode_Accessors(t *testing.T) {
	t.Parallel()

	if got := ModeInProc().String(); got != "in_proc" {
		t.Errorf("ModeInProc().String() = %q, want %q", got, "in_proc")
	}
	if got := ModeRemote().String(); got != "remote" {
		t.Errorf("ModeRemote().String() = %q, want %q", got, "remote")
	}
	if ModeInProc() == ModeRemote() {
		t.Error("ModeInProc() and ModeRemote() must be distinct values")
	}
}

// TestTransportMode_ZeroValueFailClosed asserts a zero-value (forged)
// TransportMode renders as the fail-closed sentinel, never the empty string, so
// it cannot write an empty/unattributed metric label — and the sentinel is NOT a
// producible mode in the frozen registry.
func TestTransportMode_ZeroValueFailClosed(t *testing.T) {
	t.Parallel()

	var zero TransportMode
	if got := zero.String(); got != TransportModeUnknown {
		t.Errorf("zero TransportMode.String() = %q, want %q (fail-closed sentinel)", got, TransportModeUnknown)
	}
	if zero.String() == "" {
		t.Error("zero TransportMode must never render as the empty label")
	}
	for _, m := range allTransportModes {
		if m.String() == TransportModeUnknown {
			t.Errorf("the fail-closed sentinel %q must not be a producible registered mode", TransportModeUnknown)
		}
	}
}

// TestTransportOutcome_FrozenRegistry pins the closed outcome value set and proves
// the registry is non-vacuous (anti-vacuity). Adding a new outcome without
// registering it in allTransportOutcomes — or renaming a wire value — trips this
// test (#1966 review P2.6).
func TestTransportOutcome_FrozenRegistry(t *testing.T) {
	t.Parallel()

	got := make(map[string]int, len(allTransportOutcomes))
	for _, o := range allTransportOutcomes {
		got[o.String()]++
	}

	want := map[string]int{
		"success": 1, "dial_error": 1, "timeout": 1,
		"canceled": 1, "resolver_error": 1, "rewrite_error": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("transport outcome value set = %v, want %v", got, want)
	}
	for v, n := range want {
		if got[v] != n {
			t.Errorf("transport outcome %q count = %d, want %d (registry: %v)", v, got[v], n, got)
		}
	}
}

// TestTransportOutcome_Accessors verifies the package-private singletons render
// the expected wire strings via their exported accessor functions.
func TestTransportOutcome_Accessors(t *testing.T) {
	t.Parallel()

	cases := map[string]TransportOutcome{
		"success":        OutcomeSuccess(),
		"dial_error":     OutcomeDialError(),
		"timeout":        OutcomeTimeout(),
		"canceled":       OutcomeCanceled(),
		"resolver_error": OutcomeResolverError(),
		"rewrite_error":  OutcomeRewriteError(),
	}
	for want, o := range cases {
		if got := o.String(); got != want {
			t.Errorf("outcome accessor String() = %q, want %q", got, want)
		}
	}
}

// TestTransportOutcome_ZeroValueFailClosed asserts a zero-value (forged)
// TransportOutcome renders as the fail-closed sentinel and is NOT a producible
// outcome in the frozen registry.
func TestTransportOutcome_ZeroValueFailClosed(t *testing.T) {
	t.Parallel()

	var zero TransportOutcome
	if got := zero.String(); got != TransportOutcomeUnknown {
		t.Errorf("zero TransportOutcome.String() = %q, want %q (fail-closed sentinel)", got, TransportOutcomeUnknown)
	}
	if zero.isRegistered() {
		t.Error("zero TransportOutcome must not be registered (fail-closed)")
	}
	for _, o := range allTransportOutcomes {
		if o.String() == TransportOutcomeUnknown {
			t.Errorf("the fail-closed sentinel %q must not be a producible registered outcome", TransportOutcomeUnknown)
		}
	}
}
