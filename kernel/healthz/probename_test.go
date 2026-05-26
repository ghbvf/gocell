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

	// Build a snake_case name that exceeds 48 chars.
	long := "a" + strings.Repeat("_b", 25) // len > 48
	_, err := NewProbeName(long)
	if err == nil {
		t.Errorf("NewProbeName(%q) expected error for length %d, got nil", long, len(long))
	}
	// The error is an errcode.Error; the message contains "length budget".
	if !strings.Contains(err.Error(), "length budget") {
		t.Errorf("expected error to mention length budget, got: %v", err)
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
