package initialadmin

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveCredentialPath_ExplicitStateDir(t *testing.T) {
	dir := t.TempDir()

	got, err := ResolveCredentialPath(dir)
	if err != nil {
		t.Fatalf("ResolveCredentialPath: %v", err)
	}

	want := filepath.Join(dir, "initial_admin_password")
	if got != want {
		t.Fatalf("ResolveCredentialPath(%q) = %q, want %q", dir, got, want)
	}
}

func TestResolveCredentialPath_RelativeStateDirFails(t *testing.T) {
	_, err := ResolveCredentialPath("relative-state")
	if err == nil {
		t.Fatal("expected relative state dir to fail")
	}
	if !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("error = %v, want absolute path hint", err)
	}
}

func TestResolveCredentialPath_DefaultIsPlatformAppropriate(t *testing.T) {
	t.Setenv("GOCELL_STATE_DIR", "")

	got, err := ResolveCredentialPath("")
	if err != nil {
		t.Fatalf("ResolveCredentialPath default: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("default credential path is not absolute: %q", got)
	}
	if filepath.Base(got) != "initial_admin_password" {
		t.Fatalf("default credential path = %q, want initial_admin_password basename", got)
	}

	if runtime.GOOS == "linux" {
		if got != filepath.Join(string(filepath.Separator), "run", "gocell", "initial_admin_password") {
			t.Fatalf("linux default path = %q, want /run/gocell/initial_admin_password", got)
		}
		return
	}

	if strings.Contains(filepath.ToSlash(got), "/run/gocell/") {
		t.Fatalf("%s default path must not use Linux /run/gocell: %q", runtime.GOOS, got)
	}
}
