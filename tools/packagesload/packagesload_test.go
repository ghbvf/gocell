package packagesload

import (
	"os"
	"path/filepath"
	"strings"
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

func hasGoworkPath(env []string, path string) bool {
	want := "GOWORK=" + path
	for _, e := range env {
		if e == want {
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

	t.Run("ModeWorkspace pins cfg.Dir go.work when ambient GOWORK=off", func(t *testing.T) {
		root := t.TempDir()
		goWork := filepath.Join(root, "go.work")
		if err := os.WriteFile(goWork, []byte("go 1.25\n\nuse .\n"), 0o600); err != nil {
			t.Fatalf("write go.work: %v", err)
		}
		cfg := &packages.Config{Dir: root, Env: []string{"FOO=1", "GOWORK=off"}}
		if err := applyMode(ModeWorkspace, cfg); err != nil {
			t.Fatalf("applyMode(ModeWorkspace) with cfg.Dir go.work: %v", err)
		}
		if hasGoworkOff(cfg.Env) {
			t.Errorf("ModeWorkspace left GOWORK=off in env: %v", cfg.Env)
		}
		if !hasGoworkPath(cfg.Env, goWork) {
			t.Errorf("ModeWorkspace did not pin cfg.Dir go.work %q: %v", goWork, cfg.Env)
		}
	})

	t.Run("ModeWorkspace rejects GOWORK=off without cfg.Dir go.work", func(t *testing.T) {
		cfg := &packages.Config{Dir: t.TempDir(), Env: []string{"FOO=1", "GOWORK=off"}}
		if err := applyMode(ModeWorkspace, cfg); err == nil {
			t.Error("applyMode(ModeWorkspace) without cfg.Dir go.work = nil error, want fail-closed")
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

func TestWithoutGOWORK(t *testing.T) {
	got := withoutGOWORK([]string{"A=1", "GOWORK=off", "B=2", "GOWORK=/repo/go.work"})
	joined := strings.Join(got, ",")
	if strings.Contains(joined, "GOWORK=") {
		t.Fatalf("withoutGOWORK kept GOWORK entry: %v", got)
	}
	if joined != "A=1,B=2" {
		t.Fatalf("withoutGOWORK = %v, want A=1,B=2", got)
	}
}
