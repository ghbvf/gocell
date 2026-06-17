package healthz

import (
	"strings"
	"testing"
)

// TestRemoteCellReadyProbeName_Valid asserts the composed-name constructor for
// the cross-cell remote-peer readiness probe produces "<cellID>_remote_ready"
// and that the value satisfies the typed-name funnel (#2251 P2.7).
func TestRemoteCellReadyProbeName_Valid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		cellID string
		want   string
	}{
		{"configcore", "configcore_remote_ready"},
		{"auditcore", "auditcore_remote_ready"},
		{"a", "a_remote_ready"},
	}
	for _, tc := range cases {
		t.Run(tc.cellID, func(t *testing.T) {
			t.Parallel()
			got, err := RemoteCellReadyProbeName(tc.cellID)
			if err != nil {
				t.Fatalf("RemoteCellReadyProbeName(%q): unexpected error %v", tc.cellID, err)
			}
			if got.String() != tc.want {
				t.Errorf("RemoteCellReadyProbeName(%q) = %q, want %q", tc.cellID, got.String(), tc.want)
			}
			// anti-vacuity: the value must carry the dependency-availability
			// "_ready" suffix per observability.md, not just be non-empty.
			if !strings.HasSuffix(got.String(), "_ready") {
				t.Errorf("probe name %q must end in _ready (dependency-availability convention)", got.String())
			}
		})
	}
}

// TestRemoteCellReadyProbeName_Invalid asserts fail-fast on empty / malformed /
// over-budget cellID (it routes through NewProbeName, inheriting its validation).
func TestRemoteCellReadyProbeName_Invalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		cellID string
	}{
		{"empty", ""},
		{"uppercase", "ConfigCore"},
		{"hyphen", "config-core"},
		{"whitespace", "config core"},
		{"too_long", strings.Repeat("a", 60)}, // + "_remote_ready"(13) > 64
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := RemoteCellReadyProbeName(tc.cellID); err == nil {
				t.Errorf("RemoteCellReadyProbeName(%q): expected error, got nil", tc.cellID)
			}
		})
	}
}
