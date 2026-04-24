package initialadmin

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCredentialPath_EmptyArg_NoEnv_ReturnsAbsolutePath(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv.
	// Clear any GOCELL_STATE_DIR that might be set in the test environment.
	t.Setenv("GOCELL_STATE_DIR", "")

	p, err := ResolveCredentialPath("")
	if err != nil {
		// On unsupported platforms defaultStateDir returns an error — acceptable.
		t.Skipf("platform has no default state dir: %v", err)
	}
	if !filepath.IsAbs(p) {
		t.Errorf("expected absolute path, got %q", p)
	}
	if !strings.HasSuffix(p, "initial_admin_password") {
		t.Errorf("expected path ending in 'initial_admin_password', got %q", p)
	}
}

func TestResolveCredentialPath_ExplicitAbsoluteStatedDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p, err := ResolveCredentialPath(dir)
	if err != nil {
		t.Fatalf("ResolveCredentialPath: %v", err)
	}
	want := filepath.Join(dir, "initial_admin_password")
	if p != want {
		t.Errorf("got %q, want %q", p, want)
	}
}

func TestResolveCredentialPath_EnvOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOCELL_STATE_DIR", dir)

	p, err := ResolveCredentialPath("")
	if err != nil {
		t.Fatalf("ResolveCredentialPath: %v", err)
	}
	if !strings.HasPrefix(p, dir) {
		t.Errorf("expected path under %q, got %q", dir, p)
	}
	if !strings.HasSuffix(p, "initial_admin_password") {
		t.Errorf("expected path ending in 'initial_admin_password', got %q", p)
	}
}

func TestResolveCredentialPath_ExplicitRelativeStatedDir_Errors(t *testing.T) {
	t.Parallel()

	_, err := ResolveCredentialPath("relative/path")
	if err == nil {
		t.Fatal("expected error for relative stateDir, got nil")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error should mention 'absolute', got: %v", err)
	}
}

func TestResolveCredentialPath_RelativeEnvVar_Errors(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", "relative/path")

	_, err := ResolveCredentialPath("")
	if err == nil {
		t.Fatal("expected error for relative GOCELL_STATE_DIR, got nil")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error should mention 'absolute', got: %v", err)
	}
}

func TestResolveCredentialPath_ResultContainsNoDotDot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p, err := ResolveCredentialPath(dir)
	if err != nil {
		t.Fatalf("ResolveCredentialPath: %v", err)
	}
	if strings.Contains(p, "..") {
		t.Errorf("result contains '..': %q", p)
	}
}

func TestResolveCredentialPath_ExplicitArgTakesPrecedenceOverEnv(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	t.Setenv("GOCELL_STATE_DIR", dir2)

	p, err := ResolveCredentialPath(dir1)
	if err != nil {
		t.Fatalf("ResolveCredentialPath: %v", err)
	}
	if !strings.HasPrefix(p, dir1) {
		t.Errorf("explicit arg should take precedence over env; got %q (expected prefix %q)", p, dir1)
	}
}
