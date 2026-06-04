// INVARIANT: MODULES-PATH-VALIDATION-01
//go:build !windows

package archtest

// INVARIANT: MODULES-PATH-VALIDATION-01 (runtime arm)
//
// hack/lib/modules.sh::gocell::modules::dirs is the single source for workspace
// module enumeration (sourced from go.work via `go work edit -json`). It
// fail-closed validates every `use` DiskPath before handing it to `go -C`, so a
// malformed go.work (`use ../outside`, `use /etc`) cannot drive a build outside
// the repo. Static shellcheck cannot prove that bash semantics actually reject
// these paths, so this black-box runtime test crafts go.work files with hostile
// DiskPaths and asserts the helper exits non-zero with a path-boundary error —
// the regression guard for the F4 review finding on PR #1571.
//
// AI-robust: Medium — bash path semantics are not statically expressible from
// Go; a black-box runtime test executed in CI is the closest practical guard
// (same approach as HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01).
//
// Build constraint: !windows (needs bash + `go work edit`; Windows runners skip).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestModulesPathValidation01(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	root := findModuleRoot(t)
	utilSh := filepath.Join(root, "hack", "lib", "util.sh")
	modulesSh := filepath.Join(root, "hack", "lib", "modules.sh")
	for _, p := range []string{utilSh, modulesSh} {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Fatalf("required script missing: %v", statErr)
		}
	}

	cases := []struct {
		name string
		// useLine is the `use ...` directive written into the crafted go.work.
		useLine string
		// extraSetup runs against the temp dir before invoking the helper.
		extraSetup func(t *testing.T, dir string)
		wantOK     bool   // true => helper exits 0
		wantStdout string // required substring of stdout when wantOK
		wantStderr string // required substring of stderr when !wantOK
	}{
		{
			name:       "valid root use .",
			useLine:    "use .",
			wantOK:     true,
			wantStdout: ".",
		},
		{
			name:       "parent escape rejected",
			useLine:    "use ../outside",
			wantOK:     false,
			wantStderr: "'..' segment",
		},
		{
			name:       "absolute path rejected",
			useLine:    "use /etc",
			wantOK:     false,
			wantStderr: "is absolute",
		},
		{
			name:    "member without go.mod rejected",
			useLine: "use ./sub",
			extraSetup: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
					t.Fatalf("mkdir sub: %v", err)
				}
			},
			wantOK:     false,
			wantStderr: "has no go.mod",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/wsmodtest\n\ngo 1.25.11\n")
			writeFile(t, filepath.Join(dir, "go.work"), "go 1.25.11\n\n"+tc.useLine+"\n")
			if tc.extraSetup != nil {
				tc.extraSetup(t, dir)
			}

			script := fmt.Sprintf("source %q; source %q; gocell::modules::dirs", utilSh, modulesSh)
			cmd := exec.Command(bash, "-c", script) //nolint:gosec // G204: bash + script paths are findModuleRoot-derived, not user input
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOWORK="+filepath.Join(dir, "go.work"))
			var stdout, stderr strings.Builder
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr := cmd.Run()

			if tc.wantOK {
				if runErr != nil {
					t.Fatalf("expected success, got error %v\nstderr: %s", runErr, stderr.String())
				}
				if !strings.Contains(strings.TrimSpace(stdout.String()), tc.wantStdout) {
					t.Errorf("stdout %q does not contain %q", stdout.String(), tc.wantStdout)
				}
				return
			}
			if runErr == nil {
				t.Fatalf("expected non-zero exit for %q, got success\nstdout: %s", tc.useLine, stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr %q does not contain %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
