//go:build archtest

package archtest

// INVARIANT: HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01
//
// scripts/healthcheck-verify.sh — regression guard for bugs surfaced in
// PR #9 / PR #1011 post-merge review (issue #19, backlog
// DEVOPS-INTEGRATION-CLEANUP-WAIT-TIMEOUT-01):
//
//  1. Bare `docker compose down` at the script tail does not run when
//     `docker compose up --wait` fails, when `docker compose ps` fails, or
//     on SIGINT (`set -euo pipefail` exits early). Must install a `trap`
//     on EXIT before any `docker compose` invocation so cleanup is
//     unconditional across success / failure / signal paths (Invariant A).
//
//  2. `docker compose up --wait --timeout N` uses the stop-shutdown flag,
//     not the wait bound. The flag that bounds `--wait` polling is
//     `--wait-timeout`. The original bug was silent: the documented
//     "wait up to TIMEOUT seconds" envelope was inert; `--wait` ran with
//     compose's internal default. Applies to every `docker compose up`
//     in the script, not just the first (Invariant B; F3 round-2 fix).
//
//  3. EXIT-trap shape alone does not prove cleanup actually runs `down`:
//     `trap 'echo noop' EXIT` would satisfy Invariant A but leak
//     containers. The script must contain a literal `docker compose
//     down` reachable from the trap (Invariant D; F2 round-2 fix).
//
// AI-robust: Medium (string-anchor regression). Bash has no typed call
// sites; the Hard upgrade path is to port the verifier to Go (os/exec +
// testcontainers). For this <50-line script the upgrade cost outweighs
// the residual risk after the Medium guard below, so no tracking issue
// is opened — this is an explicit decision to remain at Medium, not an
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
//   - Multi-up: a script may call `docker compose up` more than once
//     (e.g. once for healthchecks, once for a real boot). Invariant B/C
//     iterate every `docker compose up` line, not only the first.
//   - Trap-noop: an EXIT trap with a body that does not call `docker
//     compose down` (e.g. `trap 'echo bye' EXIT`) would pass A. Invariant
//     D asserts the script contains a literal `docker compose down`
//     command, ensuring the trap has a real cleanup target to reach.
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
// 自检". Each fixture below is a synthetic script that either violates
// exactly one invariant (the test asserts the checker emits the expected
// sub-rule diagnostic) or is canonical-clean (the test asserts zero
// diagnostics). Together with the prod test above this closes the TDD
// FAIL→PASS demonstration into a permanent regression guard.
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
			name: "B-second-up-broken",
			script: `#!/usr/bin/env bash
set -euo pipefail
trap 'docker compose down' EXIT
docker compose up -d --wait --wait-timeout 30
docker compose up -d --wait --timeout 60
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
			name: "D-trap-noop",
			script: `#!/usr/bin/env bash
set -euo pipefail
trap 'echo noop' EXIT
docker compose up -d --wait --wait-timeout 30
docker compose ps
`,
			wantSub: "(D)",
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
		{
			name: "clean-function-trap",
			script: `#!/usr/bin/env bash
set -euo pipefail
_cleanup() {
  docker compose down || true
}
trap _cleanup EXIT
docker compose up -d --wait --wait-timeout 30
`,
			wantClean: true,
		},
		{
			name: "clean-multi-up-both-correct",
			script: `#!/usr/bin/env bash
set -euo pipefail
trap 'docker compose down' EXIT
docker compose up -d --wait --wait-timeout 30
docker compose up -d --wait --wait-timeout=60
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

// checkHealthcheckVerifyScript runs the four string-anchor invariants
// (A trap-before-docker, B/C per-up flag form, D cleanup-actually-runs-
// down) against script bytes and returns a slice of diagnostic messages.
// Empty slice = healthy. Pure function over bytes so meta self-checks
// can drive it with synthetic fixtures.
//
// Function-body scoping: `docker compose` inside a bash function body
// (e.g. inside `_cleanup() { ... }`) is a *definition*, not a *call*,
// and must not count toward Invariant A's "first docker compose call".
// Per-line scanning tracks a simple function-nesting depth: an opening
// `name() {` increments depth, a closing `}` decrements it. Only lines
// at depth 0 are considered for A/B/C; D scans every depth (the
// `docker compose down` may live inside the trap's cleanup function,
// which is precisely the recommended idiom).
func checkHealthcheckVerifyScript(data []byte, scriptDesc string) []string {
	var diags []string
	lines := strings.Split(string(data), "\n")

	firstDockerComposeLine := -1
	trapEXITLine := -1
	hasDockerComposeDown := false
	var upLines []int // line indices of every top-level `docker compose up`

	funcDepth := 0
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Invariant D (any nesting depth): the script must contain at
		// least one literal `docker compose down`. Comments stripped
		// above, so a `# docker compose down` doc line never matches.
		if strings.Contains(trimmed, "docker compose down") {
			hasDockerComposeDown = true
		}

		// Function-body nesting: lines inside `name() { ... }` are
		// definition, not execution. Increment on opening, decrement
		// on closing brace; skip A/B/C accounting while nested.
		if isBashFuncOpen(trimmed) {
			funcDepth++
			continue
		}
		if trimmed == "}" && funcDepth > 0 {
			funcDepth--
			continue
		}
		if funcDepth > 0 {
			continue
		}

		// At top level from here on.
		if strings.HasPrefix(trimmed, "trap ") && hasToken(trimmed, "EXIT") {
			if trapEXITLine < 0 {
				trapEXITLine = i
			}
		}
		if strings.Contains(trimmed, "docker compose") && firstDockerComposeLine < 0 {
			firstDockerComposeLine = i
		}
		if strings.Contains(trimmed, "docker compose up") {
			upLines = append(upLines, i)
		}
	}

	// Invariant A: a `trap ... EXIT` must be installed before the first
	// `docker compose` invocation so cleanup runs on every exit path.
	switch {
	case trapEXITLine < 0:
		diags = append(diags, fmt.Sprintf(
			"%s (A): no `trap ... EXIT` line in %s; "+
				"failure / SIGINT paths will leak containers",
			ruleHealthcheckVerify01, scriptDesc,
		))
	case firstDockerComposeLine >= 0 && trapEXITLine > firstDockerComposeLine:
		diags = append(diags, fmt.Sprintf(
			"%s (A): `trap ... EXIT` at line %d is AFTER "+
				"first `docker compose` at line %d; install trap first",
			ruleHealthcheckVerify01, trapEXITLine+1, firstDockerComposeLine+1,
		))
	}

	// Invariant D: the script must contain at least one literal
	// `docker compose down` outside comments. Without this an EXIT
	// trap whose body is `echo noop` (or similar) would silently
	// satisfy A while leaking every container. Comments were stripped
	// during the scan, so a `# docker compose down` doc string does
	// not count.
	if !hasDockerComposeDown {
		diags = append(diags, fmt.Sprintf(
			"%s (D): %s contains no `docker compose down` outside "+
				"comments; the EXIT trap has no real cleanup target",
			ruleHealthcheckVerify01, scriptDesc,
		))
	}

	// Invariant B / C: each `docker compose up` must (B) bound `--wait`
	// with `--wait-timeout` (never `--timeout` — that's the stop-
	// shutdown flag), and (C) stay on a single logical line so per-
	// line token scanning is reliable. Applied to every up line, not
	// only the first, so a regression on a secondary up cannot hide.
	for _, idx := range upLines {
		upLine := strings.TrimSpace(lines[idx])
		if strings.HasSuffix(strings.TrimRight(upLine, " \t"), "\\") {
			diags = append(diags, fmt.Sprintf(
				"%s (C): line %d uses bash line-continuation; "+
					"the `docker compose up` command must stay on one "+
					"logical line so per-line token scanning is reliable",
				ruleHealthcheckVerify01, idx+1,
			))
			continue
		}
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
		// flag confusion); only when it is absent do we fall
		// through to the generic missing-bound diagnostic.
		switch {
		case hasWait && hasBareTimeout:
			diags = append(diags, fmt.Sprintf(
				"%s (B): line %d combines `--wait` with `--timeout`; "+
					"`--timeout` is the stop-shutdown flag, "+
					"use `--wait-timeout` to bound `--wait` polling",
				ruleHealthcheckVerify01, idx+1,
			))
		case hasWait && !hasWaitTimeout:
			diags = append(diags, fmt.Sprintf(
				"%s (B): line %d uses `--wait` without `--wait-timeout`; "+
					"health-check wait must be bounded explicitly",
				ruleHealthcheckVerify01, idx+1,
			))
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

// isBashFuncOpen reports whether trimmed is the opening line of a bash
// function definition. Accepts both common forms:
//
//	name() {
//	name () {
//	function name {
//	function name() {
//
// The brace may sit on the next line in some styles; this helper only
// matches the same-line form, which is the GoCell convention for
// scripts/*.sh. A future style change would need to extend this
// matcher in tandem.
func isBashFuncOpen(trimmed string) bool {
	if !strings.HasSuffix(trimmed, "{") {
		return false
	}
	head := strings.TrimSpace(strings.TrimSuffix(trimmed, "{"))
	// `function name` or `function name()`
	if strings.HasPrefix(head, "function ") {
		head = strings.TrimSpace(strings.TrimPrefix(head, "function "))
	}
	// Strip trailing `()` (with or without internal whitespace).
	if idx := strings.Index(head, "("); idx >= 0 {
		rest := strings.TrimSpace(head[idx:])
		if rest != "()" && rest != "( )" {
			return false
		}
		head = strings.TrimSpace(head[:idx])
	}
	if head == "" {
		return false
	}
	for _, r := range head {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') {
			return false
		}
	}
	return true
}
