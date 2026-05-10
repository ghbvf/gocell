package bootstrap

// phase0_grace_period_test.go — PR-V1-030-G02 (b): phase0ValidateOptions must
// emit a slog.Warn (non-blocking) when the user-declared K8s
// terminationGracePeriodSeconds is less than the framework's own shutdown
// budget plus a 10s safety margin. Zero/unset value skips the check entirely.

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// File-local test-time constants (file-level package consts satisfy
// TEST-TIME-LITERAL-01 archtest; site-specific deadlines stay close to the
// test that owns them).
//
// Threshold = 2 × shutdownTimeout + 10s safety margin (see
// warnTerminationGracePeriodInsufficient godoc — drainCtx and tearCtx each
// own a shutdownTimeout-sized budget; preShutdownDelay is consumed inside
// drainCtx, not added on top of either).
//
// graceMinThreshold derives from terminationGraceSafetyMargin (same package
// const) so the formula stays single-sourced; bumping the safety margin
// updates the threshold here automatically.
const (
	graceShutdownTimeout   = testtime.D20s
	gracePreShutdownDelay  = testtime.D5s // recorded in warn payload but does not affect threshold
	graceMinThreshold      = 2*graceShutdownTimeout + terminationGraceSafetyMargin
	graceBelowThreshold    = graceMinThreshold - testtime.D5s  // 45s < 50s → must warn
	graceAboveThreshold    = graceMinThreshold + testtime.D10s // 60s > 50s → no warn
	graceFarBelowThreshold = testtime.D10s                     // for advisory-only assertion
	gracePersistPositive   = 45 * time.Second                  // setter round-trip
	gracePersistNegative   = -1 * time.Nanosecond
	graceLifecycleShutdown = testtime.D30s
	graceLifecyclePreDelay = testtime.D5s
)

// TestPhase0_TerminationGracePeriodWarn covers the three branches of the
// terminationGracePeriod sanity check in phase0ValidateOptions:
//   - unset (zero) → skip silently
//   - declared and < shutdownTimeout + 10s → warn (non-blocking)
//   - declared and ≥ threshold → no warn
//
// Common bootstrap scaffolding (shutdownTimeout=20s ⇒ minimum_required = 30s)
// is shared across cases so each row only varies the declared grace period.
//
// This test intentionally does NOT call t.Parallel(): it mutates the global
// slog.Default(), which would race with parallel tests in the same package
// that emit slog records. The convention is established by other tests in
// this package (bootstrap_test.go, dual_listener_test.go) that also drive
// slog.SetDefault sequentially.
func TestPhase0_TerminationGracePeriodWarn(t *testing.T) {
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
			terminationGrace: graceBelowThreshold, // 25 < 30
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
			terminationGrace:    graceMinThreshold, // exactly threshold ≥ 30 ok
			wantWarn:            false,
			forbidWarnSubstring: "terminationGracePeriodSeconds insufficient",
		},
		{
			name:                "above-threshold-no-warn",
			terminationGrace:    graceAboveThreshold,
			wantWarn:            false,
			forbidWarnSubstring: "terminationGracePeriodSeconds insufficient",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureGraceWarn(t, tc.terminationGrace)
			if tc.wantWarn {
				assertGraceWarnEmitted(t, out, tc.wantWarnSubstrings)
			} else {
				assertGraceWarnAbsent(t, out, tc.forbidWarnSubstring)
			}
		})
	}
}

// captureGraceWarn installs a JSON slog handler, invokes
// warnTerminationGracePeriodInsufficient with the shared shutdown/pre-delay
// scaffolding plus the supplied terminationGracePeriod, and returns the
// captured log output. Cleanup restores the previous default logger.
func captureGraceWarn(t *testing.T, terminationGrace time.Duration) string {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	oldDefault := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(oldDefault) })

	b := &Bootstrap{
		shutdownTimeout:        graceShutdownTimeout,
		preShutdownDelay:       gracePreShutdownDelay,
		terminationGracePeriod: terminationGrace,
	}
	b.warnTerminationGracePeriodInsufficient()
	return buf.String()
}

// assertGraceWarnEmitted asserts every required substring is present in out
// and that the captured slog record carries level=WARN. The handler is
// configured at LevelWarn so anything emitted IS warn-or-higher; the
// substring check on `"level":"WARN"` confirms the level explicitly without
// re-decoding the JSON record.
func assertGraceWarnEmitted(t *testing.T, out string, required []string) {
	t.Helper()
	require.NotEmpty(t, out, "expected warn record but log is empty")
	for _, sub := range required {
		assert.Contains(t, out, sub,
			"warn record missing required substring %q in output:\n%s", sub, out)
	}
	assert.Contains(t, out, `"level":"WARN"`,
		"expected slog level=WARN record:\n%s", out)
}

// assertGraceWarnAbsent asserts that no warn-related output mentioning the
// forbidden substring leaked into the captured log. Empty forbidden value
// (no substring asserted) is a no-op.
func assertGraceWarnAbsent(t *testing.T, out, forbidden string) {
	t.Helper()
	if forbidden == "" {
		return
	}
	assert.NotContains(t, out, forbidden,
		"unexpected warn-related output:\n%s", out)
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
		shutdownTimeout:        graceLifecycleShutdown,
		preShutdownDelay:       graceLifecyclePreDelay,
		terminationGracePeriod: graceFarBelowThreshold, // far below threshold
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
		{"positive", gracePersistPositive, gracePersistPositive},
		{"negative-stored-as-is", gracePersistNegative, gracePersistNegative}, // option does not normalise; phase0 treats <=0 as unset
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bootstrap{}
			WithTerminationGracePeriod(tc.in)(b)
			assert.Equal(t, tc.want, b.terminationGracePeriod)
		})
	}
}
