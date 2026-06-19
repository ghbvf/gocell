//go:build archtest

package archtest

// INVARIANT: ARCHTEST-SCOPE-CONFIG-UNIT-01
//
// scope_config_test.go is the defaults-lock unit-coverage anchor for the runtime
// scope-config single source ([RuntimeScopeConfig] / [DefaultScopeConfig], #2329):
// it pins each resolved field to its pre-#2329 path default so the helper-layer
// convergence is byte-for-byte behavior-preserving for GoCell's own dogfood.
// ARCHTEST-SCOPE-CONFIG-UNIT-01 is a unit-coverage anchor (same convention as
// ARCHTEST-EXTERNAL-SURFACE-01 / ARCHTEST-PASS-DRIVER-UNIT-01), not a production
// invariant gate.

import (
	"reflect"
	"testing"
)

// TestDefaultScopeConfig_ReproducesGoCellDefaults locks the default values of
// RuntimeScopeConfig to the legacy path defaults, satisfying #2329's
// requirement: "用单测锁定旧路径默认值".
//
// This is the regression anchor: if any default path drifts, this test turns
// red immediately, before the 370-rule suite has a chance to surface the gap.
func TestDefaultScopeConfig_ReproducesGoCellDefaults(t *testing.T) {
	cfg, err := DefaultScopeConfig()
	if err != nil {
		t.Fatalf("DefaultScopeConfig() error: %v", err)
	}

	root, err := lookupModuleRoot()
	if err != nil {
		t.Fatalf("lookupModuleRoot() error: %v", err)
	}
	if got := cfg.WorkspaceRoot(); got != root {
		t.Errorf("WorkspaceRoot() = %q, want %q (lookupModuleRoot)", got, root)
	}

	want, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("moduleImportPath() error: %v", err)
	}
	if got := cfg.TargetModulePath(); got != want {
		t.Errorf("TargetModulePath() = %q, want %q (moduleImportPath)", got, want)
	}

	// In GoCell's own dogfood the target module == the platform module.
	if got := cfg.TargetModulePath(); got != PlatformModulePath {
		t.Errorf("TargetModulePath() = %q, want PlatformModulePath %q", got, PlatformModulePath)
	}

	if got := cfg.FrameworkModulePath(); got != PlatformFrameworkModulePath {
		t.Errorf("FrameworkModulePath() = %q, want PlatformFrameworkModulePath %q", got, PlatformFrameworkModulePath)
	}

	if got := cfg.PlatformModulePath(); got != PlatformModulePath {
		t.Errorf("PlatformModulePath() = %q, want PlatformModulePath %q", got, PlatformModulePath)
	}

	if got := cfg.PlatformCellsModulePath(); got != PlatformCellsModulePath {
		t.Errorf("PlatformCellsModulePath() = %q, want PlatformCellsModulePath %q", got, PlatformCellsModulePath)
	}

	if got := cfg.PlatformCellScanDirs(); !reflect.DeepEqual(got, []string{PlatformCellsDir}) {
		t.Errorf("PlatformCellScanDirs() = %v, want %v", got, []string{PlatformCellsDir})
	}
}

// TestScopeConfig_ScanDirsSingleSource asserts that RuntimeScopeConfig.PlatformCellScanDirs()
// and the standalone platformCellScanDirs() helper return the same slice, proving
// they share a single source (platformCellScanDirsDefault).
func TestScopeConfig_ScanDirsSingleSource(t *testing.T) {
	cfg, err := DefaultScopeConfig()
	if err != nil {
		t.Fatalf("DefaultScopeConfig() error: %v", err)
	}
	if got, want := cfg.PlatformCellScanDirs(), platformCellScanDirs(); !reflect.DeepEqual(got, want) {
		t.Errorf("cfg.PlatformCellScanDirs() = %v, want %v (platformCellScanDirs)", got, want)
	}
}

// TestScopeConfig_ScanDirsDefensiveCopy proves PlatformCellScanDirs() returns a
// fresh copy each call: a caller that mutates the returned slice must not corrupt
// the config's internal state (the accessor does make+copy, not an alias).
func TestScopeConfig_ScanDirsDefensiveCopy(t *testing.T) {
	cfg, err := DefaultScopeConfig()
	if err != nil {
		t.Fatalf("DefaultScopeConfig() error: %v", err)
	}
	got := cfg.PlatformCellScanDirs()
	if len(got) == 0 {
		t.Fatal("PlatformCellScanDirs() returned an empty slice; cannot test isolation")
	}
	got[0] = "MUTATED-BY-CALLER"
	if again := cfg.PlatformCellScanDirs(); again[0] != PlatformCellsDir {
		t.Errorf("PlatformCellScanDirs() is not a defensive copy: caller mutation leaked, got %q want %q", again[0], PlatformCellsDir)
	}
}

// TestRuntimeScopeConfig_ZeroValueIsInert documents the construction guarantee
// honestly: the zero value RuntimeScopeConfig{} IS constructible anywhere (Go
// permits T{} for any struct, even with all-unexported fields), but it is inert —
// every accessor returns its empty zero, so it is never a usable config. Only
// DefaultScopeConfig mints a populated value. The forgery guarantee that DOES
// hold — external code cannot POPULATE a RuntimeScopeConfig with chosen field
// values — is a compile-time property of the unexported fields, not assertable
// here at runtime.
func TestRuntimeScopeConfig_ZeroValueIsInert(t *testing.T) {
	var zero RuntimeScopeConfig
	if got := zero.WorkspaceRoot(); got != "" {
		t.Errorf("zero.WorkspaceRoot() = %q, want empty", got)
	}
	if got := zero.TargetModulePath(); got != "" {
		t.Errorf("zero.TargetModulePath() = %q, want empty", got)
	}
	if got := zero.FrameworkModulePath(); got != "" {
		t.Errorf("zero.FrameworkModulePath() = %q, want empty", got)
	}
	if got := zero.PlatformModulePath(); got != "" {
		t.Errorf("zero.PlatformModulePath() = %q, want empty", got)
	}
	if got := zero.PlatformCellsModulePath(); got != "" {
		t.Errorf("zero.PlatformCellsModulePath() = %q, want empty", got)
	}
	if got := zero.PlatformCellScanDirs(); len(got) != 0 {
		t.Errorf("zero.PlatformCellScanDirs() = %v, want empty", got)
	}
}
