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
