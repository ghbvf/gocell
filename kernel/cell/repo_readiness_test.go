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
