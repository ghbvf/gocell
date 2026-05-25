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
// testcontainers). For this 22-line script the upgrade cost outweighs the
// residual risk after the Medium guard below, so no tracking issue is
// opened — this is an explicit decision to remain at Medium, not an
// oversight. Per `.claude/rules/gocell/ai-robust.md` Review checklist,
// silent Medium carryover is forbidden; this paragraph is the explicit
// declaration.
//
// Parser blind spots the Medium guard deliberately addresses:
//
//   - Token-form: `--timeout=N` (equals form) — Invariant B detects both
//     space-separated `--timeout N` and equals-form `--timeout=N`.
//   - Line-continuation: a backslash-continued multi-line `docker compose
//     up` invocation would put `--wait` and `--timeout` on different lines
//     and defeat per-line token scan. Invariant C bans line-continuation
//     on the `docker compose up` logical command (the script is single-
//     line by convention; the ban makes that convention machine-checked).
//
// Self-check (meta-regression): TestHealthcheckVerifyScriptInvariantMeta01
// feeds known-broken script fixtures through the same checker the file-
// reading test uses, asserting each fixture trips the expected diagnostic.
// This closes the gap noted in `.claude/rules/gocell/ai-robust.md`
// §"工具选定后强制盲区自检".

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ruleHealthcheckVerify01 = "HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01"

func TestHealthcheckVerifyScriptInvariant01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scriptPath := filepath.Clean(filepath.Join(root, "scripts", "healthcheck-verify.sh"))
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", scriptPath, err)
	}
	for _, diag := range checkHealthcheckVerifyScript(data, scriptPath) {
		t.Errorf("%s", diag)
	}
}

// TestHealthcheckVerifyScriptInvariantMeta01 is the reverse self-check
// required by `.claude/rules/gocell/ai-robust.md` §"工具选定后强制盲区
// 自检". Each fixture below is a synthetic script that violates exactly
// one invariant; the test asserts the checker emits a diagnostic naming
// the expected sub-rule (A/B/C). Together with the prod test above this
// closes the TDD FAIL→PASS demonstration into a permanent regression
// guard.
func TestHealthcheckVerifyScriptInvariantMeta01(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		script    string
		wantSub   string
		wantClean bool
	}{
		{
			name: "A-no-trap",
			script: `#!/usr/bin/env bash
set -euo pipefail
docker compose up -d --wait --wait-timeout 30
docker compose down
`,
			wantSub: "(A)",
		},
		{
			name: "A-trap-after-docker",
			script: `#!/usr/bin/env bash
set -euo pipefail
docker compose up -d --wait --wait-timeout 30
trap 'docker compose down' EXIT
`,
			wantSub: "(A)",
		},
		{
			name: "B-space-form-timeout",
			script: `#!/usr/bin/env bash
set -euo pipefail
trap 'docker compose down' EXIT
docker compose up -d --wait --timeout 30
`,
			wantSub: "(B)",
		},
		{
			name: "B-equals-form-timeout",
			script: `#!/usr/bin/env bash
set -euo pipefail
trap 'docker compose down' EXIT
docker compose up -d --wait --timeout=30
`,
			wantSub: "(B)",
		},
		{
			name: "B-missing-wait-timeout-entirely",
			script: `#!/usr/bin/env bash
set -euo pipefail
trap 'docker compose down' EXIT
docker compose up -d --wait
`,
			wantSub: "(B)",
		},
		{
			name: "C-line-continuation",
			script: `#!/usr/bin/env bash
set -euo pipefail
trap 'docker compose down' EXIT
docker compose up -d \
  --wait \
  --wait-timeout 30
`,
			wantSub: "(C)",
		},
		{
			name: "clean-canonical",
			script: `#!/usr/bin/env bash
set -euo pipefail
trap 'docker compose down' EXIT
docker compose up -d --wait --wait-timeout 30
docker compose ps
`,
			wantClean: true,
		},
		{
			name: "clean-equals-form-wait-timeout",
			script: `#!/usr/bin/env bash
set -euo pipefail
trap 'docker compose down' EXIT
docker compose up -d --wait --wait-timeout=30
`,
			wantClean: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := checkHealthcheckVerifyScript([]byte(tc.script), "<fixture>")
			if tc.wantClean {
				if len(diags) != 0 {
					t.Errorf("expected clean fixture but got diagnostics: %v", diags)
				}
				return
			}
			if len(diags) == 0 {
				t.Fatalf("expected diagnostic with substring %q, got none", tc.wantSub)
			}
			matched := false
			for _, d := range diags {
				if strings.Contains(d, tc.wantSub) {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("expected diagnostic with substring %q, got %v", tc.wantSub, diags)
			}
		})
	}
}

// checkHealthcheckVerifyScript runs the three string-anchor invariants
// against script bytes and returns a slice of diagnostic messages. Empty
// slice = healthy. Pure function over bytes so meta self-checks can drive
// it with synthetic fixtures.
func checkHealthcheckVerifyScript(data []byte, scriptDesc string) []string {
	var diags []string
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

	// Invariant A: a `trap ... EXIT` must be installed before the first
	// `docker compose` invocation so cleanup runs on every exit path.
	switch {
	case trapEXITLine < 0:
		diags = append(diags, fmt.Sprintf(
			"%s (A): no `trap ... EXIT` line in %s; "+
				"failure / SIGINT paths will leak containers",
			ruleHealthcheckVerify01, scriptDesc))
	case firstDockerComposeLine >= 0 && trapEXITLine > firstDockerComposeLine:
		diags = append(diags, fmt.Sprintf(
			"%s (A): `trap ... EXIT` at line %d is AFTER "+
				"first `docker compose` at line %d; install trap first",
			ruleHealthcheckVerify01, trapEXITLine+1, firstDockerComposeLine+1))
	}

	// Invariant B / C: `docker compose up` must (B) bound `--wait` with
	// `--wait-timeout` (never `--timeout` — that's the stop-shutdown
	// flag), and (C) stay on a single logical line so per-line token
	// scanning is reliable.
	if upLineIdx >= 0 {
		if strings.HasSuffix(strings.TrimRight(upLine, " \t"), "\\") {
			diags = append(diags, fmt.Sprintf(
				"%s (C): line %d uses bash line-continuation; "+
					"the `docker compose up` command must stay on one "+
					"logical line so per-line token scanning is reliable",
				ruleHealthcheckVerify01, upLineIdx+1))
		} else {
			tokens := strings.Fields(upLine)
			var hasWait, hasBareTimeout, hasWaitTimeout bool
			for _, tok := range tokens {
				switch {
				case tok == "--wait":
					hasWait = true
				case tok == "--wait-timeout" ||
					strings.HasPrefix(tok, "--wait-timeout="):
					hasWaitTimeout = true
				case tok == "--timeout" ||
					strings.HasPrefix(tok, "--timeout="):
					hasBareTimeout = true
				}
			}
			// Branches are mutually exclusive: when `--timeout` is
			// present we report the precise diagnostic (stop-shutdown
			// flag confusion); only when it is absent do we fall through
			// to the generic missing-bound diagnostic. Avoids the
			// duplicate-error issue from the first revision.
			switch {
			case hasWait && hasBareTimeout:
				diags = append(diags, fmt.Sprintf(
					"%s (B): line %d combines `--wait` with `--timeout`; "+
						"`--timeout` is the stop-shutdown flag, "+
						"use `--wait-timeout` to bound `--wait` polling",
					ruleHealthcheckVerify01, upLineIdx+1))
			case hasWait && !hasWaitTimeout:
				diags = append(diags, fmt.Sprintf(
					"%s (B): line %d uses `--wait` without `--wait-timeout`; "+
						"health-check wait must be bounded explicitly",
					ruleHealthcheckVerify01, upLineIdx+1))
			}
		}
	}
	return diags
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
