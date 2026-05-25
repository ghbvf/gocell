package archtest

// INVARIANT: HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01
//
// scripts/healthcheck-verify.sh — regression guard for two bugs surfaced in
// PR #9 post-merge review (issue #19, backlog
// DEVOPS-INTEGRATION-CLEANUP-WAIT-TIMEOUT-01):
//
//  1. Bare `docker compose down` at the script tail does not run when
//     `docker compose up --wait` fails, when `docker compose ps` fails, or
//     on SIGINT (`set -euo pipefail` exits early). Must install a `trap`
//     on EXIT before any `docker compose` invocation so cleanup is
//     unconditional across success / failure / signal paths.
//
//  2. `docker compose up --wait --timeout N` uses the stop-shutdown flag,
//     not the wait bound. The flag that bounds `--wait` polling is
//     `--wait-timeout`. The original bug was silent: the documented
//     "wait up to TIMEOUT seconds" envelope was inert; `--wait` ran with
//     compose's internal default.
//
// AI-robust: Medium (string-anchor regression). Bash has no typed call
// sites; the Hard upgrade path is to port the verifier to Go (os/exec +
// testcontainers), which is unjustified by this single 22-line script's
// footprint. The Medium guard locks the exact regression class — line
// ordering of trap-vs-first-docker-compose and token-level inspection of
// the `up` invocation — both of which are independently necessary and
// jointly sufficient to prevent silent reintroduction.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthcheckVerifyScriptInvariant01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scriptPath := filepath.Clean(filepath.Join(root, "scripts", "healthcheck-verify.sh"))
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", scriptPath, err)
	}
	lines := strings.Split(string(data), "\n")

	firstDockerComposeLine := -1
	trapEXITLine := -1
	upLineIdx := -1
	var upLine string

	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "trap ") && hasToken(trimmed, "EXIT") {
			if trapEXITLine < 0 {
				trapEXITLine = i
			}
		}
		if strings.Contains(trimmed, "docker compose") && firstDockerComposeLine < 0 {
			firstDockerComposeLine = i
		}
		if strings.Contains(trimmed, "docker compose up") && upLineIdx < 0 {
			upLineIdx = i
			upLine = trimmed
		}
	}

	const ruleID = "HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01"

	// Invariant A: a `trap ... EXIT` must be installed before the first
	// `docker compose` invocation so cleanup runs on every exit path.
	switch {
	case trapEXITLine < 0:
		t.Errorf("%s (A): no `trap ... EXIT` line in %s; "+
			"failure / SIGINT paths will leak containers",
			ruleID, scriptPath)
	case firstDockerComposeLine >= 0 && trapEXITLine > firstDockerComposeLine:
		t.Errorf("%s (A): `trap ... EXIT` at line %d is AFTER "+
			"first `docker compose` at line %d; install trap first",
			ruleID, trapEXITLine+1, firstDockerComposeLine+1)
	}

	// Invariant B: `docker compose up` using `--wait` must bound it with
	// `--wait-timeout`, never with bare `--timeout` (which is the
	// stop-shutdown flag and is silently ignored as a wait bound).
	if upLineIdx >= 0 {
		tokens := strings.Fields(upLine)
		var hasWait, hasBareTimeout, hasWaitTimeout bool
		for _, tok := range tokens {
			switch tok {
			case "--wait":
				hasWait = true
			case "--timeout":
				hasBareTimeout = true
			case "--wait-timeout":
				hasWaitTimeout = true
			}
		}
		if hasWait && hasBareTimeout {
			t.Errorf("%s (B): line %d combines `--wait` with `--timeout`; "+
				"`--timeout` is the stop-shutdown flag, "+
				"use `--wait-timeout` to bound `--wait` polling",
				ruleID, upLineIdx+1)
		}
		if hasWait && !hasWaitTimeout {
			t.Errorf("%s (B): line %d uses `--wait` without `--wait-timeout`; "+
				"health-check wait must be bounded explicitly",
				ruleID, upLineIdx+1)
		}
	}
}

// hasToken reports whether word appears as a whitespace-separated token in s.
// Used to distinguish `EXIT` (a real signal name) from substrings inside
// quoted commands or comments.
func hasToken(s, word string) bool {
	for _, tok := range strings.Fields(s) {
		if tok == word {
			return true
		}
	}
	return false
}
