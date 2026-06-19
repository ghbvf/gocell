package main

import (
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock"
)

// TestOperatorAuthFromEnv_Absent: with no operator credentials the admin plane is
// left unconfigured (ok=false). This is the #1810-no-regression guard: the
// AdminListener + #1755 verify endpoint are gated on operatorEnabled, so a
// deployment that provisions only the audit admin pool (super-admin reads) but no
// operator credentials never stands up the admin plane.
func TestOperatorAuthFromEnv_Absent(t *testing.T) {
	t.Setenv(operatorAdminUsernameEnv, "")
	t.Setenv(operatorAdminPasswordEnv, "")
	_, ok, err := operatorAuthFromEnv(clock.Real())
	if err != nil {
		t.Fatalf("operatorAuthFromEnv: %v", err)
	}
	if ok {
		t.Fatal("operator admin plane must be disabled when credentials are absent")
	}
}

// TestOperatorAuthFromEnv_PartialAbsent: a username without a password (or vice
// versa) is treated as unconfigured (ok=false), not an error — the admin plane is
// opt-in via BOTH credentials.
func TestOperatorAuthFromEnv_PartialAbsent(t *testing.T) {
	t.Setenv(operatorAdminUsernameEnv, "ops")
	t.Setenv(operatorAdminPasswordEnv, "")
	_, ok, err := operatorAuthFromEnv(clock.Real())
	if err != nil {
		t.Fatalf("operatorAuthFromEnv: %v", err)
	}
	if ok {
		t.Fatal("operator admin plane must be disabled when only the username is set")
	}
}

// TestOperatorAuthFromEnv_Present: both credentials present → a valid AuthOperator
// plan is returned and the admin plane is enabled.
func TestOperatorAuthFromEnv_Present(t *testing.T) {
	t.Setenv(operatorAdminUsernameEnv, "ops")
	t.Setenv(operatorAdminPasswordEnv, "s3cret-operator-pw")
	_, ok, err := operatorAuthFromEnv(clock.Real())
	if err != nil {
		t.Fatalf("operatorAuthFromEnv: %v", err)
	}
	if !ok {
		t.Fatal("operator admin plane must be enabled when both credentials are set")
	}
}

// TestOperatorAuthFromEnv_WeakPassword: a too-short password is a hard error
// (NewAuthOperator enforces a minimum length).
func TestOperatorAuthFromEnv_WeakPassword(t *testing.T) {
	t.Setenv(operatorAdminUsernameEnv, "ops")
	t.Setenv(operatorAdminPasswordEnv, "short")
	if _, _, err := operatorAuthFromEnv(clock.Real()); err == nil {
		t.Fatal("expected error for a too-short operator password")
	}
}

func TestAdminHTTPAddr_DefaultAndOverride(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv(adminHTTPAddrEnv, "")
		if got := adminHTTPAddr(); got != defaultAdminHTTPAddr {
			t.Errorf("adminHTTPAddr default = %q, want %q", got, defaultAdminHTTPAddr)
		}
	})

	t.Run("override", func(t *testing.T) {
		t.Setenv(adminHTTPAddrEnv, "127.0.0.1:19999")
		if got := adminHTTPAddr(); got != "127.0.0.1:19999" {
			t.Errorf("adminHTTPAddr override = %q, want 127.0.0.1:19999", got)
		}
	})
}

// TestAdminHTTPAddr_NonLoopbackWarns verifies that a non-loopback override still
// returns the override address (operator may front with reverse proxy). The Warn
// side-effect is best-effort and not easily captured in a unit test without
// injecting a slog handler, so we just assert the return value is correct.
func TestAdminHTTPAddr_NonLoopbackWarns(t *testing.T) {
	t.Setenv(adminHTTPAddrEnv, "0.0.0.0:9092")
	got := adminHTTPAddr()
	if got != "0.0.0.0:9092" {
		t.Errorf("adminHTTPAddr non-loopback = %q, want 0.0.0.0:9092", got)
	}
}
