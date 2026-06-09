package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeServiceGo writes a minimal service.go with a required field into
// sliceDir so requireddepsgen.Generate can parse it.
func writeServiceGo(t *testing.T, sliceDir, pkgName string) {
	t.Helper()
	body := "package " + pkgName + "\n\ntype Service struct {\n" +
		"\ttxRunner interface{} `gocell:\"required\"`\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(sliceDir, "service.go"), []byte(body), 0o644); err != nil {
		t.Fatalf("write service.go: %v", err)
	}
}

// makeMinimalProject creates a temp dir with a go.mod and returns the root.
// It cd's into root and registers cleanup to restore cwd.
// Not parallel-safe (uses os.Chdir).
func makeMinimalProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/testproject\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return root
}

// TestGenerateRequiredDeps_AllMode_RegeneratesAllFiles verifies that --all
// produces service_required_gen.go for every slice directory that has a
// service.go.
func TestGenerateRequiredDeps_AllMode_RegeneratesAllFiles(t *testing.T) {
	// Not parallel: uses os.Chdir.
	root := makeMinimalProject(t)

	// cells/myapp/slices/login/service.go and
	// corecells/accesscore/slices/setup/service.go with required fields.
	sliceDir := filepath.Join(root, "cells", "myapp", "slices", "login")
	if err := os.MkdirAll(sliceDir, 0o755); err != nil {
		t.Fatalf("mkdir slice: %v", err)
	}
	writeServiceGo(t, sliceDir, "login")
	coreSliceDir := filepath.Join(root, "corecells", "accesscore", "slices", "setup")
	if err := os.MkdirAll(coreSliceDir, 0o755); err != nil {
		t.Fatalf("mkdir core slice: %v", err)
	}
	writeServiceGo(t, coreSliceDir, "setup")

	if err := generateRequiredDeps([]string{"--all"}); err != nil {
		t.Fatalf("generateRequiredDeps --all: %v", err)
	}

	for _, dir := range []string{sliceDir, coreSliceDir} {
		genFile := filepath.Join(dir, "service_required_gen.go")
		//nolint:gosec // genFile is a controlled test path built from t.TempDir()
		content, err := os.ReadFile(genFile)
		if err != nil {
			t.Fatalf("expected service_required_gen.go at %s: %v", genFile, err)
		}
		if !strings.Contains(string(content), "validateRequired") {
			t.Errorf("generated file missing validateRequired; content:\n%s", content)
		}
	}
}

// TestGenerateRequiredDeps_IdMode_TargetsSingleSlice verifies that --id=<path>
// only writes to the specified slice directory.
func TestGenerateRequiredDeps_IdMode_TargetsSingleSlice(t *testing.T) {
	// Not parallel: uses os.Chdir.
	root := makeMinimalProject(t)

	loginDir := filepath.Join(root, "cells", "myapp", "slices", "login")
	logoutDir := filepath.Join(root, "cells", "myapp", "slices", "logout")
	for _, d := range []string{loginDir, logoutDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	writeServiceGo(t, loginDir, "login")
	writeServiceGo(t, logoutDir, "logout")

	// Use relative path (relative to cwd = root) as the slice path argument.
	relLogin, err := filepath.Rel(root, loginDir)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}

	if err := generateRequiredDeps([]string{relLogin}); err != nil {
		t.Fatalf("generateRequiredDeps with id: %v", err)
	}

	// Login should have the generated file.
	if _, err := os.Stat(filepath.Join(loginDir, "service_required_gen.go")); err != nil {
		t.Errorf("expected service_required_gen.go for login: %v", err)
	}
	// Logout should NOT have it.
	if _, err := os.Stat(filepath.Join(logoutDir, "service_required_gen.go")); err == nil {
		t.Errorf("did not expect service_required_gen.go for logout")
	}
}

// TestGenerateRequiredDeps_VerifyMode_NoDriftReturnsZero verifies that when
// the committed file matches the generator output, verify mode exits without
// error and prints nothing to stderr.
func TestGenerateRequiredDeps_VerifyMode_NoDriftReturnsZero(t *testing.T) {
	// Not parallel: uses os.Chdir.
	root := makeMinimalProject(t)

	sliceDir := filepath.Join(root, "cells", "myapp", "slices", "login")
	if err := os.MkdirAll(sliceDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeServiceGo(t, sliceDir, "login")

	// First generate to produce the committed file.
	if err := generateRequiredDeps([]string{"--all"}); err != nil {
		t.Fatalf("initial generate: %v", err)
	}

	// Now verify — should be clean.
	if err := generateRequiredDeps([]string{"--all", "--verify"}); err != nil {
		t.Fatalf("verify on clean tree: %v", err)
	}
}

// TestGenerateRequiredDeps_VerifyMode_DriftReturnsError verifies that when
// the committed file differs from what the generator would produce, verify
// mode returns an error containing "drift".
func TestGenerateRequiredDeps_VerifyMode_DriftReturnsError(t *testing.T) {
	// Not parallel: uses os.Chdir.
	root := makeMinimalProject(t)

	sliceDir := filepath.Join(root, "cells", "myapp", "slices", "login")
	if err := os.MkdirAll(sliceDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeServiceGo(t, sliceDir, "login")

	// First generate to produce the committed file.
	if err := generateRequiredDeps([]string{"--all"}); err != nil {
		t.Fatalf("initial generate: %v", err)
	}

	// Corrupt the generated file.
	genFile := filepath.Join(sliceDir, "service_required_gen.go")
	corrupt := "// Code generated by gocell generate required-deps. DO NOT EDIT.\n\n" +
		"package login\n\n// stale\nfunc (s *Service) validateRequired() error { return nil }\n"
	if err := os.WriteFile(genFile, []byte(corrupt), 0o644); err != nil {
		t.Fatalf("corrupt gen file: %v", err)
	}

	err := generateRequiredDeps([]string{"--all", "--verify"})
	if err == nil {
		t.Fatal("expected verify to detect drift, got nil")
	}
	if !strings.Contains(err.Error(), "drift") {
		t.Errorf("expected error to mention drift, got: %v", err)
	}
}

// TestGenerateRequiredDeps_MissingService_FailsClean verifies that specifying
// a slice path without a service.go returns a clear error.
func TestGenerateRequiredDeps_MissingService_FailsClean(t *testing.T) {
	// Not parallel: uses os.Chdir.
	root := makeMinimalProject(t)

	emptyDir := filepath.Join(root, "cells", "myapp", "slices", "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No service.go written.

	relEmpty, err := filepath.Rel(root, emptyDir)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}

	err = generateRequiredDeps([]string{relEmpty})
	if err == nil {
		t.Fatal("expected error for missing service.go, got nil")
	}
}

// TestGenerateRequiredDeps_DryRunVerifyMutex ensures mutually exclusive flags
// produce a clear error.
func TestGenerateRequiredDeps_DryRunVerifyMutex(t *testing.T) {
	t.Parallel()
	err := generateRequiredDeps([]string{"--dry-run", "--verify"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually exclusive error, got: %v", err)
	}
}

// TestGenerateRequiredDeps_DryRun_NoWrite verifies that --dry-run reports
// paths but does not write files.
func TestGenerateRequiredDeps_DryRun_NoWrite(t *testing.T) {
	// Not parallel: uses os.Chdir.
	root := makeMinimalProject(t)

	sliceDir := filepath.Join(root, "cells", "myapp", "slices", "login")
	if err := os.MkdirAll(sliceDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeServiceGo(t, sliceDir, "login")

	if err := generateRequiredDeps([]string{"--all", "--dry-run"}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	// File must NOT have been written.
	if _, err := os.Stat(filepath.Join(sliceDir, "service_required_gen.go")); err == nil {
		t.Error("dry-run must not write the file")
	}
}
