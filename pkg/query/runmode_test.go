package query

import "testing"

func TestRunMode_IsDemo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode RunMode
		want bool
	}{
		{"zero value is prod (fail-closed default)", RunMode(0), false},
		{"RunModeProd is not demo", RunModeProd, false},
		{"RunModeDemo is demo", RunModeDemo, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.mode.IsDemo(); got != tc.want {
				t.Errorf("%v.IsDemo() = %v, want %v", tc.mode, got, tc.want)
			}
		})
	}
}

func TestRunMode_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode RunMode
		want string
	}{
		{RunModeProd, "prod"},
		{RunModeDemo, "demo"},
		{RunMode(99), "unknown"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.mode.String(); got != tc.want {
				t.Errorf("RunMode(%d).String() = %q, want %q", tc.mode, got, tc.want)
			}
		})
	}
}

// TestRunMode_ZeroValueIsProd guards the fail-closed contract:
// a zero-value RunMode (unset by caller) must never enable demo behavior.
// ref: go-zero core/service/serviceconf.go — default mode is ProMode ("pro")
func TestRunMode_ZeroValueIsProd(t *testing.T) {
	t.Parallel()
	var m RunMode
	if m.IsDemo() {
		t.Fatal("zero-value RunMode must NOT be demo (fail-closed default)")
	}
	if m != RunModeProd {
		t.Fatalf("zero-value RunMode = %v, want RunModeProd", m)
	}
}

// TestPublishFailureMode_ZeroValueIsFailClosed guards the fail-closed contract:
// a zero-value PublishFailureMode (unset by caller) must never enable fail-open behavior.
// ref: watermill publisher.go — nopPublisher only in _test.go; production defaults to real publishing
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

// TestPublishFailureMode_String verifies stable lowercase labels for logs.
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
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.mode.String(); got != tc.want {
				t.Errorf("PublishFailureMode(%d).String() = %q, want %q", tc.mode, got, tc.want)
			}
		})
	}
}

// TestPublishFailureModeForDemo verifies the boolean→mode translation helper.
// ref: RunModeForDemo — single wire-time decision point, not per-call sniffing.
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
