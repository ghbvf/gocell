package healthz

import (
	"strings"
	"testing"
)

func TestNewProbeName_Valid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
	}{
		{"config_watcher", "config_watcher"},
		{"postgres_ready", "postgres_ready"},
		{"postgres_indexes_valid_ready", "postgres_indexes_valid_ready"},
		{"outbox_failopen_rate_accesscore", "outbox_failopen_rate_accesscore"},
		{"configcore_repo_ready", "configcore_repo_ready"},
		{"event_router", "event_router"},
		{"single_char", "a"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewProbeName(tc.input)
			if err != nil {
				t.Errorf("NewProbeName(%q) returned unexpected error: %v", tc.input, err)
			}
			if string(got) != tc.input {
				t.Errorf("NewProbeName(%q) = %q, want %q", tc.input, got, tc.input)
			}
		})
	}
}

func TestNewProbeName_Invalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"uppercase", "FOO"},
		{"hyphen", "foo-bar"},
		{"leading_digit", "1foo"},
		{"whitespace", "foo bar"},
		{"double_underscore", "foo__bar"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewProbeName(tc.input)
			if err == nil {
				t.Errorf("NewProbeName(%q) expected error, got nil", tc.input)
			}
		})
	}
}

func TestNewProbeName_TooLong(t *testing.T) {
	t.Parallel()

	// Build a snake_case name that exceeds 64 chars (probeNameMaxLen, K8s DNS-1123 + 1).
	long := "a" + strings.Repeat("_b", 33) // len = 1 + 66 = 67 > 64
	_, err := NewProbeName(long)
	if err == nil {
		t.Errorf("NewProbeName(%q) expected error for length %d, got nil", long, len(long))
	}
	// The error is an errcode.Error; the message contains "length budget".
	if !strings.Contains(err.Error(), "length budget") {
		t.Errorf("expected error to mention length budget, got: %v", err)
	}
}

// TestNewProbeName_AtBudget verifies the 64-char boundary: exactly 64 must
// accept, 65 must reject. Without an explicit boundary test, a future probe
// budget change might silently shift the cap.
func TestNewProbeName_AtBudget(t *testing.T) {
	t.Parallel()

	at := strings.Repeat("a", 64) // exactly 64 chars — accept
	if _, err := NewProbeName(at); err != nil {
		t.Errorf("NewProbeName(64-char) expected accept, got error: %v", err)
	}
	over := strings.Repeat("a", 65) // 65 chars — reject
	if _, err := NewProbeName(over); err == nil {
		t.Error("NewProbeName(65-char) expected reject, got nil")
	}
}

func TestEmitterFailOpenProbeName_Valid(t *testing.T) {
	t.Parallel()

	got, err := EmitterFailOpenProbeName("accesscore")
	if err != nil {
		t.Fatalf("EmitterFailOpenProbeName(%q) unexpected error: %v", "accesscore", err)
	}
	want := ProbeName("outbox_failopen_rate_accesscore")
	if got != want {
		t.Errorf("EmitterFailOpenProbeName(%q) = %q, want %q", "accesscore", got, want)
	}
}

// TestEmitterFailOpenProbeName_LongCellID verifies the composed-name budget
// stays safe at the scaffoldid 32-char cap: prefix(21) + cellID(32) = 53 < 64.
// This is the contract between pkg/scaffoldid.IdentifierPattern's upper bound
// and kernel/healthz.probeNameMaxLen — drift in either breaks the chain.
func TestEmitterFailOpenProbeName_LongCellID(t *testing.T) {
	t.Parallel()

	cellID := strings.Repeat("a", 32) // matches scaffoldid IdentifierPattern max
	got, err := EmitterFailOpenProbeName(cellID)
	if err != nil {
		t.Fatalf("EmitterFailOpenProbeName(32-char cellID) unexpected error: %v", err)
	}
	want := ProbeName("outbox_failopen_rate_" + cellID)
	if got != want {
		t.Errorf("EmitterFailOpenProbeName(32-char) = %q, want %q", got, want)
	}
}

func TestEmitterFailOpenProbeName_InvalidCellID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		cellID string
	}{
		{"empty", ""},
		{"hyphen", "Bad-Cell"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := EmitterFailOpenProbeName(tc.cellID)
			if err == nil {
				t.Errorf("EmitterFailOpenProbeName(%q) expected error, got nil", tc.cellID)
			}
		})
	}
}

func TestMustProbeName_Panic_RecoversWithRightReason(t *testing.T) {
	t.Parallel()

	// MustProbeName panics via panicregister.Approved("healthz-probe-name-invalid", ...)
	// The panic payload is *errcode.Error from errcode.Assertion — verify the
	// panic occurred and the payload contains the expected probe name.
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = MustProbeName("has-hyphen") // invalid — should panic
	}()

	if recovered == nil {
		t.Fatal("expected panic, got none")
	}
	// The payload is *errcode.Error from errcode.Assertion; it implements error.
	err, ok := recovered.(error)
	if !ok {
		t.Fatalf("recovered value is %T, want error (from errcode.Assertion)", recovered)
	}
	// The error message should contain the offending name.
	if !strings.Contains(err.Error(), "has-hyphen") {
		t.Errorf("panic error message %q does not contain %q", err.Error(), "has-hyphen")
	}
}

func TestConfigWatcherProbeName_Value(t *testing.T) {
	t.Parallel()
	if ConfigWatcherProbeName != "config_watcher" {
		t.Errorf("ConfigWatcherProbeName = %q, want %q", ConfigWatcherProbeName, "config_watcher")
	}
}

func TestConfigDriftProbeName_Value(t *testing.T) {
	t.Parallel()
	if ConfigDriftProbeName != "config_drift" {
		t.Errorf("ConfigDriftProbeName = %q, want %q", ConfigDriftProbeName, "config_drift")
	}
}

func TestEventRouterProbeName_Value(t *testing.T) {
	t.Parallel()
	if EventRouterProbeName != "event_router" {
		t.Errorf("EventRouterProbeName = %q, want %q", EventRouterProbeName, "event_router")
	}
}
