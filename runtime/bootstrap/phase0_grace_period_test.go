package bootstrap

// phase0_grace_period_test.go — PR-V1-030-G02 (b): phase0ValidateOptions must
// emit a slog.Warn (non-blocking) when the user-declared K8s
// terminationGracePeriodSeconds is less than the framework's own shutdown
// budget plus a 10s safety margin. Zero/unset value skips the check entirely.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPhase0_TerminationGracePeriodWarn covers the three branches of the
// terminationGracePeriod sanity check in phase0ValidateOptions:
//   - unset (zero) → skip silently
//   - declared and < shutdownTimeout + preShutdownDelay + 10s → warn (non-blocking)
//   - declared and ≥ threshold → no warn
//
// Common bootstrap scaffolding (shutdownTimeout=20s, preShutdownDelay=5s ⇒
// minimum_required = 35s) is shared across cases so each row only varies the
// declared grace period.
func TestPhase0_TerminationGracePeriodWarn(t *testing.T) {
	const (
		shutdownTimeout  = 20 * time.Second
		preShutdownDelay = 5 * time.Second
		// minimum_required = shutdownTimeout + preShutdownDelay + 10s = 35s
	)

	cases := []struct {
		name                string
		terminationGrace    time.Duration
		wantWarn            bool
		wantWarnSubstrings  []string // substrings that MUST appear in the captured warn record
		forbidWarnSubstring string   // never appears (used when wantWarn is false)
	}{
		{
			name:                "unset-skips-check",
			terminationGrace:    0,
			wantWarn:            false,
			forbidWarnSubstring: "terminationGracePeriodSeconds",
		},
		{
			name:             "below-threshold-warns",
			terminationGrace: 30 * time.Second, // 30 < 35
			wantWarn:         true,
			wantWarnSubstrings: []string{
				"terminationGracePeriodSeconds insufficient",
				"termination_grace_period",
				"shutdown_timeout",
				"pre_shutdown_delay",
				"minimum_required",
				"hint",
			},
		},
		{
			name:                "at-threshold-no-warn",
			terminationGrace:    35 * time.Second, // exactly threshold ≥ 35 ok
			wantWarn:            false,
			forbidWarnSubstring: "terminationGracePeriodSeconds insufficient",
		},
		{
			name:                "above-threshold-no-warn",
			terminationGrace:    60 * time.Second,
			wantWarn:            false,
			forbidWarnSubstring: "terminationGracePeriodSeconds insufficient",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			oldDefault := slog.Default()
			slog.SetDefault(logger)
			t.Cleanup(func() { slog.SetDefault(oldDefault) })

			b := &Bootstrap{
				shutdownTimeout:       shutdownTimeout,
				preShutdownDelay:      preShutdownDelay,
				terminationGracePeriod: tc.terminationGrace,
			}

			b.warnTerminationGracePeriodInsufficient()

			out := buf.String()
			if tc.wantWarn {
				require.NotEmpty(t, out, "expected warn record but log is empty")
				for _, sub := range tc.wantWarnSubstrings {
					assert.Contains(t, out, sub,
						"warn record missing required substring %q in output:\n%s", sub, out)
				}
				// Cross-check structured field shape: at least one valid JSON record
				// containing the level=WARN and msg field.
				dec := json.NewDecoder(strings.NewReader(out))
				var sawWarn bool
				for dec.More() {
					var rec map[string]any
					require.NoError(t, dec.Decode(&rec))
					if rec["level"] == "WARN" {
						sawWarn = true
						break
					}
				}
				assert.True(t, sawWarn, "no slog level=WARN record found:\n%s", out)
			} else {
				if tc.forbidWarnSubstring != "" {
					assert.NotContains(t, out, tc.forbidWarnSubstring,
						"unexpected warn-related output for %q case:\n%s", tc.name, out)
				}
			}
		})
	}
}

// TestPhase0_TerminationGracePeriodWarn_DoesNotBlockStartup verifies the
// sanity check is advisory: even when the declared grace period is below
// threshold, phase0ValidateOptions does not return an error solely on this
// account. (It still surfaces unrelated phase0 errors; this test asserts the
// helper itself never returns/panics.)
func TestPhase0_TerminationGracePeriodWarn_DoesNotBlockStartup(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	oldDefault := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(oldDefault) })

	b := &Bootstrap{
		shutdownTimeout:       30 * time.Second,
		preShutdownDelay:      5 * time.Second,
		terminationGracePeriod: 10 * time.Second, // far below threshold
	}

	require.NotPanics(t, func() {
		b.warnTerminationGracePeriodInsufficient()
	})
	assert.Contains(t, buf.String(), "terminationGracePeriodSeconds insufficient",
		"warn must still be emitted even when the helper returns no error")
}

// TestWithTerminationGracePeriod_OptionPersists verifies the option setter
// stores the declared duration so phase0 can read it back.
func TestWithTerminationGracePeriod_OptionPersists(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero-unset", 0, 0},
		{"positive", 45 * time.Second, 45 * time.Second},
		{"negative-stored-as-is", -1, -1}, // option does not normalise; phase0 treats <=0 as unset
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bootstrap{}
			WithTerminationGracePeriod(tc.in)(b)
			assert.Equal(t, tc.want, b.terminationGracePeriod)
		})
	}
}
