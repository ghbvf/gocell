package cell

import (
	"context"
	"errors"
	"testing"
)

type fakeRepoProber struct{ err error }

func (f fakeRepoProber) RepoReady(context.Context) error { return f.err }

func TestRegisterRepoReadiness_ForwardsToHealth(t *testing.T) {
	sentinel := errors.New("schema dropped")
	cases := []struct {
		name      string
		probeName string
		proberErr error
	}{
		{"healthy", "config_repo_ready", nil},
		{"unready", "session_store_ready", sentinel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := NewRegistryRecorder(map[string]any{}, DurabilityDemo)
			RegisterRepoReadiness(rec, tc.probeName, fakeRepoProber{err: tc.proberErr})

			snap := rec.Snapshot()
			fn, ok := snap.HealthCheckers[tc.probeName]
			if !ok {
				t.Fatalf("probe %q not registered via funnel", tc.probeName)
			}
			if got := fn(context.Background()); !errors.Is(got, tc.proberErr) {
				t.Fatalf("RepoReady passthrough = %v, want %v", got, tc.proberErr)
			}
		})
	}
}

// mustRegisterRepoReadiness calls RegisterRepoReadiness and returns the
// recovered panic value (nil if no panic occurred).
func mustRegisterRepoReadiness(name string) (panicked any) {
	defer func() { panicked = recover() }()
	rec := NewRegistryRecorder(map[string]any{}, DurabilityDemo)
	RegisterRepoReadiness(rec, name, fakeRepoProber{})
	return nil
}

func TestRegisterRepoReadiness_NameSuffixEnforcement(t *testing.T) {
	cases := []struct {
		name      string
		probeName string
		wantPanic bool
	}{
		{"valid suffix", "config_repo_ready", false},
		{"valid suffix session", "session_store_ready", false},
		{"missing suffix", "config_repo", true},
		{"empty name", "", true},
		{"wrong suffix", "config_ready_x", true},
		{"almost correct", "config_repo_readiness", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panicked := mustRegisterRepoReadiness(tc.probeName)
			if tc.wantPanic && panicked == nil {
				t.Fatalf("RegisterRepoReadiness(%q): expected panic, got none", tc.probeName)
			}
			if !tc.wantPanic && panicked != nil {
				t.Fatalf("RegisterRepoReadiness(%q): unexpected panic: %v", tc.probeName, panicked)
			}
		})
	}
}
