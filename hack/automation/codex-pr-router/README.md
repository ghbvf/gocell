# codex-pr-router

launchd-resident bash poller that drives the automated PR review+check cycle
([#935](https://github.com/ghbvf/gocell/issues/935)) and the gated alternate
fix path ([#1662](https://github.com/ghbvf/gocell/issues/1662)) for the
`ghbvf/gocell` repository.

---

## Role × Engine Matrix

| Phase | Default engine | Alternate engine (engine-knob) |
|-------|---------------|-------------------------------|
| **review** (`pr-status/needs-review-again`) | `codex exec review --base develop` (read-only sandbox) | `claude -p "/pr-review <N>"` |
| **check** (`pr-status/needs-check-fix`) | `codex exec review --base develop` (read-only sandbox, check-variant prompt) | `claude -p "/pr-review <N>"` |
| **fix** (`pr-status/needs-fix` + `ai/local-fix`) | `codex exec` (workspace-write sandbox) | — (dormant by default) |

Switch the review/check engine via `GOCELL_ROUTER_REVIEW_ENGINE=claude`.
The fix path always uses codex workspace-write and is **dormant by default**
(see the "codex alternate fix" section below).

---

## Prerequisites

| Tool | Purpose |
|------|---------|
| `codex` ≥ 0.136.0 | Review + check + gated fix engine |
| `codex login` (ChatGPT account) | Auth — no API key required |
| `gh` (GitHub CLI) | PR queries, label edits, comment posting |
| `GH_TOKEN` or `gh auth login` | GitHub authentication for `gh` |
| `node` on `PATH` | Required by codex |
| `go` on `PATH` | Build guard in the fix path |
| `golangci-lint` on `PATH` (optional) | Lint guard in the fix path |
| `git` on `PATH` | Worktree management |
| `python3` on `PATH` | JSON rendering in the review comment builder |
| `jq` on `PATH` | JSON construction for `pr-meta.sh emit` |

---

## Environment Variables

All machine configuration is supplied exclusively via environment variables.
**Never commit real values to the repository.**

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `GOCELL_ROUTER_HOME` | **yes** | — | Base directory for worktrees, locks, state, and logs. Created automatically if it does not exist. Example: `${HOME}/.local/gocell-router` |
| `GOCELL_ROUTER_AUTHORS` | **yes** | — | Space-separated list of GitHub login names whose PRs the router will process. This is the primary security gate. Example: `"alice bot-login"` |
| `GOCELL_ROUTER_INTERVAL` | no | `120` | Poll interval in seconds. Tuning knob — safe default, not a security var. |
| `GOCELL_ROUTER_REVIEW_ENGINE` | no | `codex` | `codex` or `claude`. Selects the review/check engine. `claude` uses `claude -p "/pr-review <N>"` which is self-contained (posts comment + flips labels internally). |

---

## Install, Start, and Stop

### 1. Prepare the router home directory

```sh
export GOCELL_ROUTER_HOME="${HOME}/.local/gocell-router"
mkdir -p "${GOCELL_ROUTER_HOME}/logs"
```

### 2. Fill the plist template

```sh
REPO_ROOT="$(pwd -P)"   # must be the gocell repo root
sed \
  -e "s|@REPO_ROOT@|${REPO_ROOT}|g" \
  -e "s|@ROUTER_HOME@|${GOCELL_ROUTER_HOME}|g" \
  -e "s|@PATH@|$(dirname "$(command -v codex)"):$(dirname "$(command -v gh)"):/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin|g" \
  -e "s|@HOME@|${HOME}|g" \
  hack/automation/codex-pr-router/com.gocell.codex-pr-watch.plist.tmpl \
  > ~/Library/LaunchAgents/com.gocell.codex-pr-watch.plist
```

Then open the installed plist and set `GOCELL_ROUTER_AUTHORS` to your GitHub
login (replace `REPLACE_WITH_YOUR_GITHUB_LOGIN`).

### 3. Load (start)

```sh
launchctl load ~/Library/LaunchAgents/com.gocell.codex-pr-watch.plist
```

### 4. Verify the agent is running

```sh
launchctl print gui/$(id -u)/com.gocell.codex-pr-watch
```

The output should show `state = running` and a non-zero PID. If it shows
`state = waiting` or restarts rapidly, check the error log (step 5).

### 5. Watch logs

```sh
tail -f "${GOCELL_ROUTER_HOME}/logs/router.err"
tail -f "${GOCELL_ROUTER_HOME}/logs/router.out"
```

To view crash logs from the system crash reporter (useful when the agent
exits immediately before any log is written):

```sh
log show --predicate 'process == "router.sh"' --last 1h
```

### 6. Stop and uninstall

```sh
launchctl unload ~/Library/LaunchAgents/com.gocell.codex-pr-watch.plist
rm ~/Library/LaunchAgents/com.gocell.codex-pr-watch.plist
```

---

## Security Posture

### The 7 hard gates

Every candidate PR is evaluated against all 7 gates before any side-effecting
action is taken. Gates are re-evaluated against live GitHub API state (not cached
from the poll query) to prevent TOCTOU races.

| # | Gate | What it checks |
|---|------|---------------|
| 1 | **same-repo** | `isCrossRepository == false` — cross-fork PRs are never processed |
| 2 | **freshness** | Re-reads `headRefOid` from GitHub after acquiring the lock; skips if the head moved during the poll |
| 3 | **author allowlist** | `author.login ∈ GOCELL_ROUTER_AUTHORS` — only listed authors are processed |
| 4 | **command allowlist** | The command (`review`/`check`/`fix`) is **derived from the trigger label**, never read from a comment or machine block. Asserted to be in the fixed set `{review, check, fix}` |
| 5 | **idempotency** | Key `${N}@${OID}:${KIND}` recorded in `$GOCELL_ROUTER_HOME/state/seen`; duplicate delivery is skipped |
| 6 | **lock** | Atomic `mkdir` on `$GOCELL_ROUTER_HOME/locks/${N}.lock`; only one handler runs per PR at a time |
| 7 | **sandbox** | Every `codex exec` call passes explicit `-s read-only` (review/check) or `-s workspace-write` (fix). The `-s` flag is **never omitted** |

### ⚠️ Global codex config is `danger-full-access`

The user's `~/.codex/config.toml` sets `sandbox_mode=danger-full-access` +
`approval_policy=never`. **The router always passes explicit `-s` on every
`codex exec` call** and never inherits the global. This is enforced
unconditionally — there is no code path that omits `-s`.

- review / check → `-s read-only` (codex cannot modify the filesystem)
- fix → `-s workspace-write` (codex can write to the worktree, but not to
  arbitrary paths outside it)

### Author allowlist is mandatory

`GOCELL_ROUTER_HOME` is a required env var (fail-fast if absent). No PR is
processed unless its author's GitHub login is in the allowlist. This prevents
external contributors from triggering the automation.

---

## codex Alternate Fix (PR-4 / #1662) — DORMANT BY DEFAULT

The fix path (`handle_fix`) is **inactive unless the `ai/local-fix` label is
explicitly applied to the PR**. This label is not applied by default by any
automation in the pipeline.

### Why it stays off until the read-only chain is proven

The review + check chain (phases 1 and 2) is read-only and produces only
comments + label flips. It cannot break the codebase. The fix path writes to
the worktree and pushes commits — a qualitatively different risk. Until the
read-only chain has been observed to work correctly in production, the fix path
remains dormant.

### Additional fix-path guards

Beyond the 7 standard gates, the fix path requires:

- `ai/local-fix` label **and** `pr-status/needs-fix` label both present (live
  re-check after lock acquisition)
- `pr-meta.sh extract` succeeds (fresh machine block with matching `headSha`)
- `cycle.exhausted == false` (3-round circuit breaker not tripped)
- `next.agent != "human"` (not already escalated)
- **Cx1-only**: `byCx.cx2 == 0 && byCx.cx3 == 0 && byCx.cx4 == 0` — only
  Cx1 (single-file) findings are auto-fixed; anything more complex goes to
  human

After codex runs, a **build guard** validates:

1. `go build ./...` passes
2. `go test ./<changed-pkgs>/...` passes for all modified packages
3. `golangci-lint` passes (if installed)

If any guard fails, an escalation comment is posted and `ai/local-fix` is
removed to prevent re-triggering.

### Activation

```sh
# Create the label (once, on the repo)
gh label create "ai/local-fix" --color "#e4e669" \
  --description "Opt-in: codex alternate fix path (PR-4 #1662)" \
  --repo ghbvf/gocell

# Apply to a specific PR to activate auto-fix
gh pr edit <N> --add-label "ai/local-fix"
```

### Deactivation

```sh
# Remove from a PR (router will also remove it after escalation)
gh pr edit <N> --remove-label "ai/local-fix"
```

---

## Testing

### Dry-run (no side effects)

```sh
export GOCELL_ROUTER_HOME="/tmp/gocell-router-test"
export GOCELL_ROUTER_AUTHORS="your-github-login"
bash hack/automation/codex-pr-router/router.sh --dry-run
```

In dry-run mode, all 7 gates are evaluated and intended actions are printed,
but no `codex exec`, no `gh` label/comment edits, and no `git` writes are
performed.

### Single poll (one iteration, exits)

```sh
export GOCELL_ROUTER_HOME="/tmp/gocell-router-test"
export GOCELL_ROUTER_AUTHORS="your-github-login"
bash hack/automation/codex-pr-router/router.sh --once
```

### Shellcheck

```sh
bash hack/verify-shellcheck.sh
```

---

## State Directory Layout

```
$GOCELL_ROUTER_HOME/
  worktrees/
    pr-<N>/        git worktree pinned to headRefOid (removed after each run)
  locks/
    <N>.lock/      atomic mkdir lock (removed via trap RETURN)
  state/
    seen           newline-separated idempotency keys "${N}@${OID}:${KIND}"
    verdict-*.json temporary codex output (removed after processing)
    body-*.md      temporary comment body files (removed after posting)
  logs/
    router.out     stdout (launchd StandardOutPath)
    router.err     stderr (launchd StandardErrorPath)
```

### Ops: rotating the `state/seen` file

The `state/seen` file grows unboundedly. On long-running installations, rotate
it periodically:

```sh
# Safe online rotation: atomically replace with an empty file.
# The router re-processes any in-flight PR at most once after rotation —
# the idempotency block it reads from GitHub prevents duplicate comments.
> "${GOCELL_ROUTER_HOME}/state/seen"
```

For periodic automated rotation (e.g. monthly via cron):

```sh
# Truncate seen on the 1st of each month at 03:00 local time.
# Add to: crontab -e
0 3 1 * * > /path/to/gocell-router/state/seen
```

### Ops: CI status is NOT consumed by this router

The router drives the **review → fix → check** label machine based on
`pr-status/*` labels. It does **not** read or react to GitHub Actions CI
results (`pm:ci` comments or status checks).

To observe CI status:

- Check the PR's Checks tab in the GitHub UI, or
- `gh pr checks <N>` from the CLI.

If CI fails and you want the router to re-review after a fix, flip the label
manually:

```sh
gh pr edit <N> \
  --add-label "pr-status/needs-review-again" \
  --remove-label "pr-status/needs-check-fix"
```

### Ops: review engine vs fix engine are two independent axes

`GOCELL_ROUTER_REVIEW_ENGINE` controls **only the review/check phase**:

| `GOCELL_ROUTER_REVIEW_ENGINE` | Review / check engine |
|-------------------------------|----------------------|
| `codex` (default) | `codex exec review --base develop -s read-only` |
| `claude` | `claude -p "/pr-review <N>" --cwd <worktree>` |

The **fix phase** always uses `codex exec -s workspace-write --base develop`.
There is no `GOCELL_ROUTER_FIX_ENGINE` variable. The fix engine is not
configurable — it is a separate axis from the review engine and cannot be
unified with it.

---

## Related

- Issue #935: PR review loop automation (epic)
- Issue #1662: codex alternate fix path
- `hack/automation/pr-meta.sh`: machine block producer/consumer
- `hack/automation/schema/pr-meta.v1.json`: machine block schema
- `hack/automation/codex-pr-router/codex-review-verdict.schema.json`: codex output schema
- `.github/project-template/pr-comment.md`: comment format templates
- `.github/project-template/PROJECT.md` §2.5/§5: 5-state label machine
