//go:build archtest && !windows

// INVARIANT: HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01
package archtest

// INVARIANT: HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01 (Invariant E,
// runtime arm — exit-code propagation on cleanup failure)
//
// Static archtest (healthcheck_verify_script_test.go, Invariants A-D)
// can prove the script shape but cannot prove bash semantics. PR #1011
// round-2 review found this gap concretely: the cleanup function had
//
//	if ! docker compose down; then
//	  local _down_rc=$?    # always 0 — `!` already inverted to success
//	  ...
//	fi
//
// Static checks (shape, presence of `docker compose down`, presence of
// `if [ "$_rc" -eq 0 ]; then _rc=$_down_rc; fi`) all passed; the script
// still exited 0 on the "main success + cleanup failure" path because
// `$?` inside the `if ! cmd; then` branch is the result of the `!`
// expression, not of cmd. Caller saw "success + orphan containers".
//
// This file pins the *behavior* by running the real script with a
// fake `docker` on PATH that simulates the exact failure mode, then
// asserting the script's exit code matches the expected propagation
// semantics. AI-robust: Medium — bash semantics are not statically
// expressible from Go, but a black-box runtime test executed in CI is
// the closest practical guard.
//
// Build constraint: !windows. The test needs `bash` and a stub
// shell script on PATH; Windows runners get t.Skip via constraint.
// macOS / Linux runners (PR-time CI + nightly) cover it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestHealthcheckVerifyRuntime01 covers the two cleanup-failure
// propagation paths as a black-box runtime test against the real
// script. Subtests use a `docker` stub on PATH that simulates each
// scenario.
//
// Subtest matrix:
//
//	main flow / cleanup / expected script exit
//	  succeed  /  fail   /  non-zero (== cleanup exit code, currently 1)
//	  fail(7)  /  fail   /  7 (original main-flow failure preserved,
//	                        cleanup's exit code must NOT mask it)
func TestHealthcheckVerifyRuntime01(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	root := findModuleRoot(t)
	scriptPath := filepath.Clean(filepath.Join(root, "hack", "scripts", "healthcheck-verify.sh"))
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("script not found: %v", err)
	}

	cases := []struct {
		name string
		// stub returns this exit code for `docker compose up ...`;
		// `docker compose down` always exits 1; everything else 0.
		upExit int
		// wantExit==-1 means "non-zero, exact code does not matter"
		// (used for the cleanup-only-fails path where the cleanup
		// exit code is propagated but its exact value is bash's
		// concern — only "non-zero" matters for the contract).
		wantExit int
		why      string
	}{
		{
			name:     "cleanup-failure-propagates",
			upExit:   0,
			wantExit: -1,
			why: "main success + cleanup failure must surface as " +
				"non-zero exit so the caller doesn't see " +
				"`success + orphan containers`",
		},
		{
			name:     "original-failure-preserved",
			upExit:   7,
			wantExit: 7,
			why: "main-flow failure code must propagate verbatim; " +
				"the cleanup failure must NOT mask the original " +
				"signal the caller cares about",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stubDir := t.TempDir()
			stubPath := filepath.Join(stubDir, "docker")
			stub := buildDockerStub(tc.upExit)
			if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
				t.Fatalf("write stub: %v", err)
			}
			//nolint:gosec // bash from exec.LookPath; scriptPath from findModuleRoot; both trusted
			cmd := exec.Command(bash, scriptPath)
			cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, _ := cmd.CombinedOutput()
			got := cmd.ProcessState.ExitCode()

			var ok bool
			switch tc.wantExit {
			case -1:
				ok = got != 0
			default:
				ok = got == tc.wantExit
			}
			if !ok {
				wantDesc := "non-zero"
				if tc.wantExit != -1 {
					wantDesc = strconv.Itoa(tc.wantExit)
				}
				t.Fatalf("HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01 (E/%s): "+
					"got exit %d, want %s.\nWhy: %s\nScript output:\n%s",
					tc.name, got, wantDesc, tc.why, indent(string(out)))
			}
		})
	}
}

// buildDockerStub returns a bash docker-stub that:
//   - returns upExit for any `docker compose up ...` invocation
//   - returns 1 for `docker compose down`
//   - returns 0 for everything else (e.g. `docker compose ps`)
//
// Stub echoes its invocation to stderr so a test failure dump
// shows what the script actually attempted.
func buildDockerStub(upExit int) string {
	return `#!/usr/bin/env bash
# Test stub for docker — used by TestHealthcheckVerifyRuntime01.
echo "[docker-stub] $*" >&2
case "$2" in
  up)   exit ` + strconv.Itoa(upExit) + ` ;;
  down) exit 1 ;;
  *)    exit 0 ;;
esac
`
}

func indent(s string) string {
	if s == "" {
		return "  <empty>"
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}
