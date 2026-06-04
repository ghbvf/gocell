package packagesload

import (
	"testing"

	"golang.org/x/tools/go/packages"
)

func hasGoworkOff(env []string) bool {
	for _, e := range env {
		if e == "GOWORK=off" {
			return true
		}
	}
	return false
}

func TestApplyMode(t *testing.T) {
	t.Run("ModeModule appends GOWORK=off as last entry", func(t *testing.T) {
		cfg := &packages.Config{Env: []string{"FOO=1", "GOWORK=/some/where"}}
		if err := applyMode(ModeModule, cfg); err != nil {
			t.Fatalf("applyMode: %v", err)
		}
		if got := cfg.Env[len(cfg.Env)-1]; got != "GOWORK=off" {
			t.Errorf("last env entry = %q, want GOWORK=off (must win over inherited GOWORK)", got)
		}
		if cfg.Env[0] != "FOO=1" {
			t.Errorf("caller env not preserved: %v", cfg.Env)
		}
	})

	t.Run("ModeModule with nil Env falls back to process env", func(t *testing.T) {
		cfg := &packages.Config{}
		if err := applyMode(ModeModule, cfg); err != nil {
			t.Fatalf("applyMode: %v", err)
		}
		if !hasGoworkOff(cfg.Env) {
			t.Errorf("ModeModule did not set GOWORK=off: %v", cfg.Env)
		}
	})

	t.Run("ModeWorkspace does not set GOWORK=off", func(t *testing.T) {
		cfg := &packages.Config{Env: []string{"FOO=1"}}
		if err := applyMode(ModeWorkspace, cfg); err != nil {
			t.Fatalf("applyMode: %v", err)
		}
		if hasGoworkOff(cfg.Env) {
			t.Errorf("ModeWorkspace unexpectedly set GOWORK=off: %v", cfg.Env)
		}
	})

	t.Run("ModeWorkspace rejects ambient GOWORK=off", func(t *testing.T) {
		cfg := &packages.Config{Env: []string{"FOO=1", "GOWORK=off"}}
		if err := applyMode(ModeWorkspace, cfg); err == nil {
			t.Error("applyMode(ModeWorkspace) with GOWORK=off = nil error, want fail-closed")
		}
	})

	t.Run("ModeWorkspace accepts a later GOWORK override of off", func(t *testing.T) {
		// Last GOWORK= entry wins (exec semantics): off then a real path is fine.
		cfg := &packages.Config{Env: []string{"GOWORK=off", "GOWORK=/repo/go.work"}}
		if err := applyMode(ModeWorkspace, cfg); err != nil {
			t.Fatalf("applyMode(ModeWorkspace) with later GOWORK override: %v", err)
		}
	})

	t.Run("invalid mode errors", func(t *testing.T) {
		cfg := &packages.Config{}
		if err := applyMode(Mode(99), cfg); err == nil {
			t.Error("applyMode(invalid) = nil error, want error")
		}
	})

	t.Run("nil config errors", func(t *testing.T) {
		if err := applyMode(ModeModule, nil); err == nil {
			t.Error("applyMode(nil cfg) = nil error, want error")
		}
	})
}

func TestLoadRejectsNilConfig(t *testing.T) {
	if _, err := Load(ModeModule, nil); err == nil {
		t.Error("Load(nil cfg) = nil error, want error")
	}
}
