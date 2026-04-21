package configpublish

import "testing"

// TestPublishFailureMode_ZeroValueIsFailClosed guards the fail-closed contract:
// a zero-value PublishFailureMode (unset by caller) must never enable fail-open
// behavior, ensuring safe-by-default production semantics.
//
// ref: watermill publisher.go — nopPublisher only in _test.go; production
// defaults to real publishing.
func TestPublishFailureMode_ZeroValueIsFailClosed(t *testing.T) {
	t.Parallel()
	var m PublishFailureMode
	if m.IsFailOpen() {
		t.Fatal("zero-value PublishFailureMode must NOT be fail-open (fail-closed default)")
	}
	if m != PublishFailureModeFailClosed {
		t.Fatalf("zero-value PublishFailureMode = %v, want PublishFailureModeFailClosed", m)
	}
}

// TestPublishFailureMode_String verifies stable lowercase labels for structured
// logs and metrics. Changing these strings is a breaking change for log parsers.
func TestPublishFailureMode_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode PublishFailureMode
		want string
	}{
		{PublishFailureModeFailClosed, "fail-closed"},
		{PublishFailureModeFailOpen, "fail-open"},
		{PublishFailureMode(99), "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.mode.String(); got != tc.want {
				t.Errorf("PublishFailureMode(%d).String() = %q, want %q", tc.mode, got, tc.want)
			}
		})
	}
}

// TestPublishFailureModeForDemo verifies the boolean→mode translation helper.
// This is the single wire-time decision point, not per-call sniffing.
//
// ref: query.RunModeForDemo — same pattern applied to read-path mode.
func TestPublishFailureModeForDemo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		demo bool
		want PublishFailureMode
	}{
		{"demo=false → fail-closed", false, PublishFailureModeFailClosed},
		{"demo=true → fail-open", true, PublishFailureModeFailOpen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := PublishFailureModeForDemo(tc.demo); got != tc.want {
				t.Errorf("PublishFailureModeForDemo(%v) = %v, want %v", tc.demo, got, tc.want)
			}
		})
	}
}
