#!/usr/bin/env bash
# router.sh — codex-pr-router: launchd-resident poller that drives the
# automated PR review+check cycle (#935) and the gated alternate fix path
# (#1662) for the ghbvf/gocell repository.
#
# Role×Engine matrix:
#   review / check  → codex exec review (read-only sandbox)
#                     OR claude -p "/pr-review <N>" (engine-knob alternate)
#   fix             → codex exec (workspace-write, DORMANT unless ai/local-fix
#                     label is explicitly applied to the PR)
#
# Machine config is supplied exclusively via environment variables (see README).
# Required vars are fail-fast validated at startup; GOCELL_ROUTER_INTERVAL and
# GOCELL_ROUTER_REVIEW_ENGINE have safe defaults (tuning knobs, not
# security/correctness vars).
#
# ⚠️  GLOBAL codex config (~/.codex/config.toml) is sandbox_mode=danger-full-access
# + approval_policy=never.  This router ALWAYS passes explicit -s on every
# codex exec call.  Never omit -s.  Never inherit the global.
#
# 7 hard gates (evaluated before any side-effecting action):
#   1. same-repo      isCrossRepository==false
#   2. freshness      live headRefOid must match throughout; skip if it moved
#   3. author         author.login ∈ GOCELL_ROUTER_AUTHORS
#   4. command        derived from label (not from comment/block); ∈ fixed set
#   5. idempotency    ${N}@${OID}:${KIND} recorded in $GOCELL_ROUTER_HOME/state/seen
#   6. lock           atomic mkdir .../locks/${N}.lock
#   7. sandbox        explicit -s on every codex exec
#
# ref: hack/automation/pr-meta.sh — script shape (set -euo pipefail,
#      REPO_ROOT resolution, temp-engine pattern, main "$@" dispatch).
#      kubernetes/kubernetes hack/verify-shellcheck.sh — shape and exclude list.

set -euo pipefail

# REPO_ROOT: codex-pr-router/ is three levels below the repo root
# (hack/automation/codex-pr-router/), so we need ../../..
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
REPO_SLUG="ghbvf/gocell"

PR_META="${REPO_ROOT}/hack/automation/pr-meta.sh"
VERDICT_SCHEMA="${REPO_ROOT}/hack/automation/codex-pr-router/codex-review-verdict.schema.json"

# Allowed codex command set (gate 4 — derived from label, never from block)
ALLOWED_COMMANDS="review check fix"

# ---------------------------------------------------------------------------
# usage
# ---------------------------------------------------------------------------

usage() {
    cat >&2 <<'EOF'
usage: router.sh [--once] [--dry-run]
  (no flags)   loop: poll_once every $GOCELL_ROUTER_INTERVAL seconds
  --once       single poll then exit (useful for testing)
  --dry-run    evaluate gates + print intended actions; take NO real action
               (no codex exec, no gh edit/comment, no git write)

Required env:
  GOCELL_ROUTER_HOME     worktrees/locks/state/logs base directory
  GOCELL_ROUTER_AUTHORS  space-separated GitHub login allowlist (e.g. "alice bob")

Optional env (tuning knobs with safe defaults):
  GOCELL_ROUTER_INTERVAL      poll interval in seconds (default: 120)
  GOCELL_ROUTER_REVIEW_ENGINE codex|claude (default: codex)
EOF
}

# ---------------------------------------------------------------------------
# env validation (required vars — fail-fast; optional vars get defaults)
# ---------------------------------------------------------------------------

validate_env() {
    local missing=0
    for v in GOCELL_ROUTER_HOME GOCELL_ROUTER_AUTHORS; do
        if [[ -z "${!v:-}" ]]; then
            echo "router: REQUIRED env var ${v} is not set" >&2
            missing=1
        fi
    done
    if [[ "${missing}" -eq 1 ]]; then
        echo "router: set required env vars before running (see README)" >&2
        exit 1
    fi
    # Optional with safe defaults (tuning knobs, not security vars)
    INTERVAL="${GOCELL_ROUTER_INTERVAL:-120}"
    REVIEW_ENGINE="${GOCELL_ROUTER_REVIEW_ENGINE:-codex}"
    if [[ "${REVIEW_ENGINE}" != "codex" && "${REVIEW_ENGINE}" != "claude" ]]; then
        echo "router: GOCELL_ROUTER_REVIEW_ENGINE must be 'codex' or 'claude' (got '${REVIEW_ENGINE}')" >&2
        exit 1
    fi
    # Ensure runtime dirs exist
    mkdir -p \
        "${GOCELL_ROUTER_HOME}/worktrees" \
        "${GOCELL_ROUTER_HOME}/locks" \
        "${GOCELL_ROUTER_HOME}/state" \
        "${GOCELL_ROUTER_HOME}/logs"
}

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

DRY_RUN=0
ONCE=0

log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $*"; }
log_dry() { echo "[DRY-RUN] $*"; }

# gate_check <condition_desc> — in dry-run, print; in live, used inline
dry_action() {
    if [[ "${DRY_RUN}" -eq 1 ]]; then
        log_dry "$*"
        return 0
    fi
    return 1
}

# author_allowed <login> — checks against space-separated GOCELL_ROUTER_AUTHORS
author_allowed() {
    local login="$1"
    local a
    for a in ${GOCELL_ROUTER_AUTHORS}; do
        if [[ "${a}" == "${login}" ]]; then
            return 0
        fi
    done
    return 1
}

# seen_key <pr> <oid> <kind> — returns the idempotency key string
seen_key() {
    printf '%s@%s:%s' "$1" "$2" "$3"
}

# mark_seen <pr> <oid> <kind>
mark_seen() {
    local key
    key="$(seen_key "$1" "$2" "$3")"
    echo "${key}" >> "${GOCELL_ROUTER_HOME}/state/seen"
}

# is_seen <pr> <oid> <kind>
is_seen() {
    local key
    key="$(seen_key "$1" "$2" "$3")"
    local seen_file="${GOCELL_ROUTER_HOME}/state/seen"
    [[ -f "${seen_file}" ]] && grep -qxF "${key}" "${seen_file}"
}

# acquire_lock <pr> — atomic mkdir; writes PID into lock dir; prints lock path on success; returns 1 if locked
# If an existing lock's PID is dead (SIGKILL stale), the lock is reclaimed.
acquire_lock() {
    local lock_dir="${GOCELL_ROUTER_HOME}/locks/${1}.lock"
    local pid_file="${lock_dir}/pid"
    if mkdir "${lock_dir}" 2>/dev/null; then
        echo $$ > "${pid_file}"
        echo "${lock_dir}"
        return 0
    fi
    # Check for stale lock: if holding PID is dead, reclaim
    if [[ -f "${pid_file}" ]]; then
        local held_pid
        held_pid="$(cat "${pid_file}" 2>/dev/null || true)"
        if [[ -n "${held_pid}" ]] && ! kill -0 "${held_pid}" 2>/dev/null; then
            log "PR #${1}: reclaiming stale lock (held by dead PID ${held_pid})"
            rm -f "${pid_file}"
            rmdir "${lock_dir}" 2>/dev/null || true
            if mkdir "${lock_dir}" 2>/dev/null; then
                echo $$ > "${pid_file}"
                echo "${lock_dir}"
                return 0
            fi
        fi
    fi
    return 1
}

release_lock() {
    local lock_dir="$1"
    rm -f "${lock_dir}/pid" 2>/dev/null || true
    rmdir "${lock_dir}" 2>/dev/null || true
}

# get_live_oid <pr> — fetch the current headRefOid from GitHub
get_live_oid() {
    gh pr view "$1" --repo "${REPO_SLUG}" --json headRefOid --jq '.headRefOid'
}

# pr_round <pr> — current max cycle.round via pr-meta.sh
pr_round() {
    bash "${PR_META}" round "$1" 2>/dev/null || echo "0"
}

# prepare_worktree <pr> <branch> <oid>
# Creates or resets a worktree at $GOCELL_ROUTER_HOME/worktrees/pr-<N>
# pinned to <oid> (detached HEAD).
prepare_worktree() {
    local pr="$1" branch="$2" oid="$3"
    local wt="${GOCELL_ROUTER_HOME}/worktrees/pr-${pr}"

    # Fetch the remote branch so we have the OID locally
    if ! git -C "${REPO_ROOT}" fetch origin "${branch}" --quiet 2>/dev/null; then
        if ! git -C "${REPO_ROOT}" fetch origin --quiet 2>/dev/null; then
            log "WARN: both fetch attempts failed for branch=${branch}; proceeding with cached refs (later OID lookups may fail)"
        fi
    fi

    if [[ -d "${wt}" ]]; then
        # Worktree exists — reset it to the pinned OID
        git -C "${wt}" checkout --detach --quiet "${oid}" 2>/dev/null || {
            # Worktree may be stale/corrupt — remove and re-add
            git -C "${REPO_ROOT}" worktree remove --force "${wt}" 2>/dev/null || true
            git -C "${REPO_ROOT}" worktree add --detach --quiet "${wt}" "${oid}"
        }
    else
        git -C "${REPO_ROOT}" worktree add --detach --quiet "${wt}" "${oid}"
    fi
    echo "${wt}"
}

# remove_worktree <path>
remove_worktree() {
    local wt="$1"
    git -C "${REPO_ROOT}" worktree remove --force "${wt}" 2>/dev/null || true
}

# pr_has_label <pr> <label>
pr_has_label() {
    local pr="$1" label="$2"
    gh pr view "${pr}" --repo "${REPO_SLUG}" --json labels \
        --jq ".labels[].name | select(. == \"${label}\")" \
        2>/dev/null | grep -q .
}

# ---------------------------------------------------------------------------
# gate 2 freshness check: re-read live OID after worktree prep; skip if moved
# ---------------------------------------------------------------------------
check_freshness() {
    local pr="$1" original_oid="$2"
    local live_oid
    live_oid="$(get_live_oid "${pr}")"
    if [[ "${live_oid}" != "${original_oid}" ]]; then
        log "PR #${pr}: head moved during poll (${original_oid} → ${live_oid}); skipping"
        return 1
    fi
    return 0
}

# ---------------------------------------------------------------------------
# render_pr_review_body <verdict_json_file> <pr> <phase> <branch> <oid> <wt> <round>
# Writes a pm:pr-review comment body to stdout.
# ---------------------------------------------------------------------------
render_pr_review_body() {
    local verdict_file="$1" pr="$2" phase="$3" branch="$4" oid="$5" wt="$6" round="$7"

    # F16: consolidate 10 per-field python3 calls into ONE invocation (quoting-safe: file path via argv[1])
    local verdict total p0 p1 p2 p3 cx1 cx2 cx3 cx4
    local _fields_out
    _fields_out="$(python3 - "$verdict_file" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    d = json.load(f)
c = d['counts']
fields = [
    d['verdict'],
    str(c['total']),
    str(c['byP']['p0']),
    str(c['byP']['p1']),
    str(c['byP']['p2']),
    str(c['byP']['p3']),
    str(c['byCx']['cx1']),
    str(c['byCx']['cx2']),
    str(c['byCx']['cx3']),
    str(c['byCx']['cx4']),
]
print('\n'.join(fields))
PY
)"
    verdict="$(echo "${_fields_out}" | sed -n '1p')"
    total="$(echo "${_fields_out}"   | sed -n '2p')"
    p0="$(echo "${_fields_out}"      | sed -n '3p')"
    p1="$(echo "${_fields_out}"      | sed -n '4p')"
    p2="$(echo "${_fields_out}"      | sed -n '5p')"
    p3="$(echo "${_fields_out}"      | sed -n '6p')"
    cx1="$(echo "${_fields_out}"     | sed -n '7p')"
    cx2="$(echo "${_fields_out}"     | sed -n '8p')"
    cx3="$(echo "${_fields_out}"     | sed -n '9p')"
    cx4="$(echo "${_fields_out}"     | sed -n '10p')"

    # Build finding list (check-phase uses ✅/❌/⚠️/🔧 markers)
    local findings_list
    findings_list="$(python3 - "${verdict_file}" "${phase}" <<'PY'
import json, sys

verdict_file = sys.argv[1]
phase = sys.argv[2]
check_status_map = {"fixed": "✅已修复", "not-fixed": "❌未修复",
                    "regression": "⚠️回归", "partial": "🔧部分"}
with open(verdict_file) as f:
    data = json.load(f)

lines = []
for i, finding in enumerate(data.get("findings", []), 1):
    fl = finding.get("fileLine", "?")
    p = finding.get("p", "?")
    cx = finding.get("cx", "?")
    dim = finding.get("dimension", "?")
    summary = finding.get("summary", "?")
    if phase == "check":
        cs = finding.get("checkStatus", "not-fixed")
        marker = check_status_map.get(cs, cs)
        lines.append(f"- **F{i}** [{p}·{cx}·{dim}] `{fl}` — {summary} → {marker}")
    else:
        lines.append(f"- **F{i}** [{p}·{cx}·{dim}] `{fl}` — {summary}")

print("\n".join(lines) if lines else "_No findings._")
PY
)"

    # Build details table
    local details_table
    details_table="$(python3 - "${verdict_file}" "${phase}" <<'PY'
import json, sys

verdict_file = sys.argv[1]
phase = sys.argv[2]
check_status_map = {"fixed": "✅已修复", "not-fixed": "❌未修复",
                    "regression": "⚠️回归", "partial": "🔧部分"}
with open(verdict_file) as f:
    data = json.load(f)

lines = []
for i, finding in enumerate(data.get("findings", []), 1):
    fl = finding.get("fileLine", "?")
    p = finding.get("p", "?")
    cx = finding.get("cx", "?")
    dim = finding.get("dimension", "?")
    summary = finding.get("summary", "?")
    evidence = finding.get("evidence", "")
    suggestion = finding.get("suggestion", "")

    lines.append(f"**F{i}** [{p}·{cx}·{dim}] `{fl}`")
    lines.append(f"- {summary}")
    if evidence:
        lines.append(f"- 证据：`{evidence}`")
    if suggestion:
        lines.append(f"- 建议：{suggestion}")
    if phase == "check":
        cs = finding.get("checkStatus", "not-fixed")
        marker = check_status_map.get(cs, cs)
        lines.append(f"- 验证结果：{marker}")
    lines.append("")

print("\n".join(lines) if lines else "_无_")
PY
)"

    # Determine conclusion string
    local conclusion
    if [[ "${phase}" == "check" ]]; then
        case "${verdict}" in
            ready)             conclusion="全部修复 → 可合并" ;;
            changes-requested) conclusion="存在未修复/回归项 → 回 /fix" ;;
            *)                 conclusion="${verdict}" ;;
        esac
    else
        case "${verdict}" in
            approved)          conclusion="通过" ;;
            changes-requested) conclusion="需修复" ;;
            *)                 conclusion="${verdict}" ;;
        esac
    fi

    # Emit the body
    cat <<BODY
<!-- pm:pr-review -->
## 🔍 pr-review（六维度分级审查）

**根因簇** — · **Findings** ${total}（P0 ${p0}·P1 ${p1}·P2 ${p2}·P3 ${p3} ｜ Cx1 ${cx1}·Cx2 ${cx2}·Cx3 ${cx3}·Cx4 ${cx4}）· **结论** ${conclusion}

**Findings**（每条带 file:line，/fix 无损提取）
${findings_list}

<details><summary>完整详表（证据 + 建议 + 根因 + 方案种子，/fix 读此）</summary>

${details_table}
</details>

**修复分流**：Cx1/Cx2 → \`/fix\`；Cx3/Cx4 → 需人工决策（方案种子见详表）。
**结论**：${conclusion}

---
🤖 PR #${pr} · Generated with Codex · branch ${branch} · worktree ${wt} · session —
BODY
}

# ---------------------------------------------------------------------------
# handle_review <review|check> <pr_json_object>
# ---------------------------------------------------------------------------
handle_review() {
    local kind="$1"
    local pr_json="$2"

    local pr branch oid author_login is_cross draft
    pr="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['number'])")"
    branch="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['headRefName'])")"
    oid="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['headRefOid'])")"
    author_login="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['author']['login'])")"
    is_cross="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['isCrossRepository'])")"
    draft="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['isDraft'])")"

    log "PR #${pr}: considering ${kind} (branch=${branch} oid=${oid:0:12}... author=${author_login})"

    # Gate 1: same-repo
    if [[ "${is_cross}" == "True" ]]; then
        log "PR #${pr}: skip — cross-repository (gate 1)"
        return 0
    fi

    # Gate 2: freshness — re-read live OID
    local live_oid
    live_oid="$(get_live_oid "${pr}")"
    if [[ "${live_oid}" != "${oid}" ]]; then
        log "PR #${pr}: skip — head moved (gate 2: listed=${oid:0:12} live=${live_oid:0:12})"
        return 0
    fi

    # Gate 3: author allowlist
    if ! author_allowed "${author_login}"; then
        log "PR #${pr}: skip — author '${author_login}' not in allowlist (gate 3)"
        return 0
    fi

    # Gate 4: command allowlist (derived from label, never from comment)
    if ! echo "${ALLOWED_COMMANDS}" | grep -qw "${kind}"; then
        log "PR #${pr}: skip — derived command '${kind}' not in allowed set (gate 4)"
        return 0
    fi

    # Gate 5: idempotency
    if is_seen "${pr}" "${live_oid}" "${kind}"; then
        log "PR #${pr}: skip — already processed ${kind} at ${live_oid:0:12} (gate 5)"
        return 0
    fi

    # Gate 6: lock
    local lock_dir
    if ! lock_dir="$(acquire_lock "${pr}")"; then
        log "PR #${pr}: skip — locked by another handler (gate 6)"
        return 0
    fi
    # shellcheck disable=SC2064
    trap "release_lock '${lock_dir}'" RETURN

    # Draft guard (review/check skip draft PRs)
    if [[ "${draft}" == "True" ]]; then
        log "PR #${pr}: skip — draft PR"
        return 0
    fi

    # F8: Re-confirm trigger label still present after acquiring lock (TOCTOU: human may have moved label during poll→lock window)
    if [[ "${kind}" == "review" ]]; then
        if ! pr_has_label "${pr}" "pr-status/needs-review-again"; then
            log "PR #${pr}: skip — trigger label pr-status/needs-review-again gone after lock (state changed under us)"
            return 0
        fi
    elif [[ "${kind}" == "check" ]]; then
        if ! pr_has_label "${pr}" "pr-status/needs-check-fix"; then
            log "PR #${pr}: skip — trigger label pr-status/needs-check-fix gone after lock (state changed under us)"
            return 0
        fi
    fi

    # Re-check freshness after acquiring lock (TOCTOU guard)
    if ! check_freshness "${pr}" "${live_oid}"; then
        return 0
    fi

    if dry_action "PR #${pr}: would run ${kind} via ${REVIEW_ENGINE} (oid=${live_oid:0:12} branch=${branch})"; then
        return 0
    fi

    # Engine knob: claude alternate (F2: check phase uses --check flag)
    if [[ "${REVIEW_ENGINE}" == "claude" ]]; then
        local wt_claude
        wt_claude="$(prepare_worktree "${pr}" "${branch}" "${live_oid}")"
        # shellcheck disable=SC2064
        trap "remove_worktree '${wt_claude}'; release_lock '${lock_dir}'" RETURN
        if [[ "${kind}" == "check" ]]; then
            log "PR #${pr}: running via claude engine (claude -p '/pr-review ${pr} --check' --cwd ${wt_claude})"
            if ! claude -p "/pr-review ${pr} --check" --cwd "${wt_claude}"; then
                log "PR #${pr}: claude engine failed; not marking seen"
                return 0
            fi
        else
            log "PR #${pr}: running via claude engine (claude -p '/pr-review ${pr}' --cwd ${wt_claude})"
            if ! claude -p "/pr-review ${pr}" --cwd "${wt_claude}"; then
                log "PR #${pr}: claude engine failed; not marking seen"
                return 0
            fi
        fi
        mark_seen "${pr}" "${live_oid}" "${kind}"
        return 0
    fi

    # codex path ---
    # Gate 7: sandbox — explicit -s read-only on every codex exec
    local wt
    wt="$(prepare_worktree "${pr}" "${branch}" "${live_oid}")"
    # shellcheck disable=SC2064
    trap "remove_worktree '${wt}'; release_lock '${lock_dir}'" RETURN

    local out_file
    out_file="$(mktemp "${GOCELL_ROUTER_HOME}/state/verdict-${pr}-XXXXXX.json")"

    local codex_prompt
    if [[ "${kind}" == "check" ]]; then
        # F1: fetch prior-round findings from the PR comments before the check call.
        # Without this, codex re-reviews without knowing what was previously found → may emit 'ready' vacuously.
        # Fail-closed: if no prior findings are found, do NOT proceed (a check with nothing to verify is a bug).
        local prior_findings_json
        prior_findings_json="$(gh api "repos/${REPO_SLUG}/issues/${pr}/comments" \
            --jq '[.[] | select(.body | contains("<!-- pm:pr-review -->")) | .body] | last' \
            2>/dev/null || echo "null")"

        local prior_findings_text
        prior_findings_text="$(python3 - "${prior_findings_json:-null}" <<'PY'
import json, sys, re

raw = sys.argv[1]
if not raw or raw == "null":
    print("")
    sys.exit(0)

# Extract the findings list section from the comment body
# Look for the Findings section between header and details
lines = raw.split("\n")
findings = []
in_findings = False
for line in lines:
    if "**Findings**" in line and "/fix" in line:
        in_findings = True
        continue
    if in_findings:
        if line.startswith("<details>") or line.startswith("**修复分流") or line.startswith("**结论"):
            break
        if line.strip():
            findings.append(line.strip())

print("\n".join(findings))
PY
)"

        if [[ -z "${prior_findings_text}" ]]; then
            log "PR #${pr}: check phase: no prior pm:pr-review findings found — skipping (check with nothing to verify is a bug)"
            return 0
        fi

        codex_prompt="Review this PR (base=develop). This is a CHECK phase: verify that the following findings from the previous review round have been fixed. For EACH finding listed below, determine its checkStatus: 'fixed', 'not-fixed', 'regression', or 'partial'. Every finding in the list MUST appear in the output findings[] with a checkStatus field. Do NOT emit 'ready' unless you have verified every prior finding.

Prior-round findings to verify:
${prior_findings_text}

Emit structured JSON per the output schema with verdict (ready if all fixed, changes-requested if any unfixed/regression) and per-finding details including checkStatus for each prior finding."
    else
        codex_prompt="Review this PR (base=develop). Perform a thorough six-dimension review (security, correctness, DX, ops, arch, tests). Emit structured JSON per the output schema with verdict and per-finding details."
    fi

    log "PR #${pr}: running codex exec review (kind=${kind} sandbox=read-only worktree=${wt})"
    # Gate 7: explicit -s read-only (NEVER inherit global danger-full-access)
    if ! codex exec review \
            --base develop \
            --output-schema "${VERDICT_SCHEMA}" \
            -o "${out_file}" \
            -s read-only \
            -C "${wt}" \
            --message "${codex_prompt}"; then
        log "PR #${pr}: codex exec failed; skipping comment+label flip"
        rm -f "${out_file}"
        return 0
    fi

    # Validate codex output + recompute counts from findings[] (F7+F10)
    # F10: fail-closed when jsonschema unavailable — manual validator covers key fields
    # F7: recompute total/byP/byCx from actual findings[] (not self-reported counts)
    local verdict
    if ! verdict="$(python3 - "${out_file}" "${VERDICT_SCHEMA}" "${kind}" <<'PY'
import json, sys

out_file = sys.argv[1]
schema_file = sys.argv[2]
phase = sys.argv[3]

try:
    with open(out_file) as f:
        data = json.load(f)
except (json.JSONDecodeError, OSError) as e:
    print(f"INVALID:not valid JSON: {e}", file=sys.stderr)
    sys.exit(1)

# Try jsonschema if available; if not, run full manual validator (fail-closed, not pass)
jsonschema_available = False
try:
    import jsonschema
    jsonschema_available = True
    with open(schema_file) as f:
        schema = json.load(f)
    jsonschema.validate(data, schema)
except ImportError:
    pass  # will run manual checks below
except jsonschema.ValidationError as e:
    print(f"INVALID:schema validation failed: {e.message}", file=sys.stderr)
    sys.exit(1)

# Manual checks: verdict must be in allowed set (always run)
allowed_verdicts = {"approved", "changes-requested", "ready"}
v = data.get("verdict", "")
if v not in allowed_verdicts:
    print(f"INVALID:verdict '{v}' not in {sorted(allowed_verdicts)}", file=sys.stderr)
    sys.exit(1)

# F10: when jsonschema not available, validate findings[] element shape manually (fail-closed)
if not jsonschema_available:
    valid_p = {"P0", "P1", "P2", "P3"}
    valid_cx = {"Cx1", "Cx2", "Cx3", "Cx4"}
    valid_check_status = {"fixed", "not-fixed", "regression", "partial"}
    findings = data.get("findings", [])
    if not isinstance(findings, list):
        print("INVALID:findings must be an array", file=sys.stderr)
        sys.exit(1)
    for i, f in enumerate(findings):
        if not isinstance(f, dict):
            print(f"INVALID:findings[{i}] must be an object", file=sys.stderr)
            sys.exit(1)
        fl = f.get("fileLine", "")
        if not fl or not isinstance(fl, str) or ":" not in fl:
            print(f"INVALID:findings[{i}].fileLine missing or malformed (expected 'path:line')", file=sys.stderr)
            sys.exit(1)
        p_val = f.get("p", "")
        if p_val not in valid_p:
            print(f"INVALID:findings[{i}].p='{p_val}' not in {sorted(valid_p)}", file=sys.stderr)
            sys.exit(1)
        cx_val = f.get("cx", "")
        if cx_val not in valid_cx:
            print(f"INVALID:findings[{i}].cx='{cx_val}' not in {sorted(valid_cx)}", file=sys.stderr)
            sys.exit(1)
        if phase == "check":
            cs = f.get("checkStatus", "")
            if cs and cs not in valid_check_status:
                print(f"INVALID:findings[{i}].checkStatus='{cs}' not in {sorted(valid_check_status)}", file=sys.stderr)
                sys.exit(1)

# F7: recompute counts from actual findings[] (ignore self-reported counts — source of truth)
findings = data.get("findings", [])
recomputed = {"total": len(findings), "byP": {"p0":0,"p1":0,"p2":0,"p3":0}, "byCx": {"cx1":0,"cx2":0,"cx3":0,"cx4":0}}
for f in findings:
    p_key = f.get("p","").lower()
    cx_key = f.get("cx","").lower()
    if p_key in recomputed["byP"]:
        recomputed["byP"][p_key] += 1
    if cx_key in recomputed["byCx"]:
        recomputed["byCx"][cx_key] += 1

# Detect and log discrepancy between self-reported and recomputed counts
reported = data.get("counts", {})
reported_total = reported.get("total", -1)
if reported_total != recomputed["total"]:
    print(f"WARN:findings[] count discrepancy: self-reported total={reported_total} recomputed={recomputed['total']}; using recomputed", file=sys.stderr)

# Overwrite data counts with recomputed values so downstream uses correct numbers
data["counts"] = recomputed

# Write patched data back to out_file so bash reads recomputed values
with open(out_file, "w") as f:
    json.dump(data, f)

print(v)
PY
    )"; then
        log "PR #${pr}: codex output failed validation; skipping"
        rm -f "${out_file}"
        return 0
    fi
    log "PR #${pr}: codex verdict=${verdict} (kind=${kind})"

    local round
    round="$(pr_round "${pr}")"

    # Read recomputed counts from out_file (patched by validator above)
    local _counts_out total p0 p1 p2 p3 cx1 cx2 cx3 cx4
    _counts_out="$(python3 - "${out_file}" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    d = json.load(f)
c = d['counts']
print(c['total'])
print(c['byP']['p0'])
print(c['byP']['p1'])
print(c['byP']['p2'])
print(c['byP']['p3'])
print(c['byCx']['cx1'])
print(c['byCx']['cx2'])
print(c['byCx']['cx3'])
print(c['byCx']['cx4'])
PY
)"
    total="$(echo "${_counts_out}" | sed -n '1p')"
    p0="$(echo "${_counts_out}"    | sed -n '2p')"
    p1="$(echo "${_counts_out}"    | sed -n '3p')"
    p2="$(echo "${_counts_out}"    | sed -n '4p')"
    p3="$(echo "${_counts_out}"    | sed -n '5p')"
    cx1="$(echo "${_counts_out}"   | sed -n '6p')"
    cx2="$(echo "${_counts_out}"   | sed -n '7p')"
    cx3="$(echo "${_counts_out}"   | sed -n '8p')"
    cx4="$(echo "${_counts_out}"   | sed -n '9p')"

    # F5: mergeability precheck BEFORE rendering/posting the ready comment.
    # Never post a "ready" comment or flip to ready on unknown/conflicting mergeability.
    # If not MERGEABLE, override verdict to changes-requested before rendering body.
    if [[ "${kind}" == "check" && "${verdict}" == "ready" ]]; then
        local mergeable
        mergeable="$(gh pr view "${pr}" --repo "${REPO_SLUG}" \
            --json mergeable --jq '.mergeable' 2>/dev/null || echo "UNKNOWN")"
        # UNKNOWN means GitHub is still computing; poll once more after a short wait
        if [[ "${mergeable}" == "UNKNOWN" ]]; then
            sleep 5
            mergeable="$(gh pr view "${pr}" --repo "${REPO_SLUG}" \
                --json mergeable --jq '.mergeable' 2>/dev/null || echo "UNKNOWN")"
        fi
        if [[ "${mergeable}" != "MERGEABLE" ]]; then
            log "PR #${pr}: check ready but mergeable=${mergeable} — overriding verdict to changes-requested"
            verdict="changes-requested"
        fi
    fi

    # Render comment body
    local body_file
    body_file="$(mktemp "${GOCELL_ROUTER_HOME}/state/body-${pr}-XXXXXX.md")"
    # F6: pass basename of worktree (not absolute path) to avoid leaking it into public PR comment
    local wt_display
    wt_display="$(basename "${wt}")"
    render_pr_review_body "${out_file}" "${pr}" "${kind}" "${branch}" "${live_oid}" "${wt_display}" "${round}" > "${body_file}"

    # F1: emit machine block — includes required schema fields: tool, findings.{fixed,unresolved,blocking}
    # handle_review: fixed=0 (review phase hasn't fixed anything), unresolved=$total, blocking=$((p0+p1))
    local blocking
    blocking=$(( p0 + p1 ))
    local meta_block
    meta_block="$(jq -nc \
        --arg repo "${REPO_SLUG}" \
        --argjson pr "${pr}" \
        --arg baseRef "develop" \
        --arg headRef "${branch}" \
        --arg headSha "${live_oid}" \
        --arg phase "${kind}" \
        --arg verdict "${verdict}" \
        --arg tool "codex" \
        --argjson round "${round}" \
        --argjson total "${total}" \
        --argjson p0 "${p0}" \
        --argjson p1 "${p1}" \
        --argjson p2 "${p2}" \
        --argjson p3 "${p3}" \
        --argjson cx1 "${cx1}" \
        --argjson cx2 "${cx2}" \
        --argjson cx3 "${cx3}" \
        --argjson cx4 "${cx4}" \
        --argjson blocking "${blocking}" \
        '{kind:"pr-review",phase:$phase,verdict:$verdict,repo:$repo,pr:$pr,
          tool:$tool,
          baseRef:$baseRef,headRef:$headRef,headSha:$headSha,session:null,worktree:null,
          findings:{total:$total,fixed:0,unresolved:$total,blocking:$blocking,
                    byP:{p0:$p0,p1:$p1,p2:$p2,p3:$p3},
                    byCx:{cx1:$cx1,cx2:$cx2,cx3:$cx3,cx4:$cx4}},
          cycle:{round:$round}}' \
        | bash "${PR_META}" emit)" || {
        log "PR #${pr}: pr-meta emit failed; skipping"
        rm -f "${out_file}" "${body_file}"
        return 0
    }
    echo "${meta_block}" >> "${body_file}"

    # Post comment (issues B4)
    local comment_url
    comment_url="$(gh pr comment "${pr}" --repo "${REPO_SLUG}" --body-file "${body_file}")"
    log "PR #${pr}: comment posted → ${comment_url}"
    rm -f "${out_file}" "${body_file}"

    # Label flips (issues B3, 5-state) — F3: only mark_seen if flip succeeds
    if flip_labels_review "${pr}" "${kind}" "${verdict}"; then
        # Mark seen (gate 5) — only after BOTH comment post AND label flip succeed
        mark_seen "${pr}" "${live_oid}" "${kind}"
    else
        log "PR #${pr}: label flip failed — not marking seen so next poll can retry flip"
    fi
}

# ---------------------------------------------------------------------------
# flip_labels_review <pr> <kind:review|check> <verdict>
# Implements the 5-state label machine (PROJECT.md §2.5 / §5).
# F3: no || true — returns real gh exit status so caller can gate mark_seen.
# F4: review/approved sets pr-status/ready + pr-review/approved (no-findings terminal state).
# F5: mergeability precheck is done in handle_review BEFORE comment post; by the time
#     flip_labels_review is called the verdict already reflects the precheck outcome.
# ---------------------------------------------------------------------------
flip_labels_review() {
    local pr="$1" kind="$2" verdict="$3"

    if [[ "${kind}" == "review" ]]; then
        case "${verdict}" in
            changes-requested)
                gh pr edit "${pr}" --repo "${REPO_SLUG}" \
                    --add-label "pr-review/changes-requested" \
                    --add-label "pr-status/needs-fix" \
                    --remove-label "pr-review/approved" \
                    --remove-label "pr-status/needs-review-again"
                log "PR #${pr}: labels → changes-requested + needs-fix"
                ;;
            approved)
                # F4: no findings → terminal ready state (approved + pr-status/ready)
                gh pr edit "${pr}" --repo "${REPO_SLUG}" \
                    --add-label "pr-review/approved" \
                    --add-label "pr-status/ready" \
                    --remove-label "pr-review/changes-requested" \
                    --remove-label "pr-status/needs-review-again"
                log "PR #${pr}: labels → approved + pr-status/ready (no findings)"
                ;;
        esac
    elif [[ "${kind}" == "check" ]]; then
        case "${verdict}" in
            ready)
                # Mergeability precheck already performed in handle_review before this call
                gh pr edit "${pr}" --repo "${REPO_SLUG}" \
                    --add-label "pr-status/ready" \
                    --add-label "pr-review/approved" \
                    --remove-label "pr-status/needs-check-fix" \
                    --remove-label "pr-review/changes-requested"
                log "PR #${pr}: labels → ready + approved"
                ;;
            changes-requested)
                gh pr edit "${pr}" --repo "${REPO_SLUG}" \
                    --add-label "pr-review/changes-requested" \
                    --add-label "pr-status/needs-fix" \
                    --remove-label "pr-status/needs-check-fix" \
                    --remove-label "pr-review/approved"
                log "PR #${pr}: labels → changes-requested + needs-fix"
                ;;
        esac
    fi
}

# ---------------------------------------------------------------------------
# handle_fix <pr_json_object>
# Gated alternate fix path (#1662). DORMANT by default — requires ai/local-fix
# label to be explicitly applied. Cx1-only guard.
# ---------------------------------------------------------------------------
handle_fix() {
    local pr_json="$1"

    local pr branch oid author_login is_cross draft
    pr="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['number'])")"
    branch="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['headRefName'])")"
    oid="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['headRefOid'])")"
    author_login="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['author']['login'])")"
    is_cross="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['isCrossRepository'])")"
    draft="$(echo "${pr_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['isDraft'])")"

    log "PR #${pr}: considering fix (ai/local-fix gate active)"

    # Gate 1: same-repo
    if [[ "${is_cross}" == "True" ]]; then
        log "PR #${pr}: skip fix — cross-repository (gate 1)"
        return 0
    fi

    # Draft guard (no auto-fix on draft PRs)
    if [[ "${draft}" == "True" ]]; then
        log "PR #${pr}: skip fix — draft PR"
        return 0
    fi

    # Gate 2: freshness
    local live_oid
    live_oid="$(get_live_oid "${pr}")"
    if [[ "${live_oid}" != "${oid}" ]]; then
        log "PR #${pr}: skip fix — head moved (gate 2)"
        return 0
    fi

    # Gate 3: author allowlist
    if ! author_allowed "${author_login}"; then
        log "PR #${pr}: skip fix — author not in allowlist (gate 3)"
        return 0
    fi

    # Gate 4: command allowlist
    if ! echo "${ALLOWED_COMMANDS}" | grep -qw "fix"; then
        log "PR #${pr}: skip fix — 'fix' not in allowed commands (gate 4)"
        return 0
    fi

    # Gate 5: idempotency
    if is_seen "${pr}" "${live_oid}" "fix"; then
        log "PR #${pr}: skip fix — already processed at ${live_oid:0:12} (gate 5)"
        return 0
    fi

    # Gate 6: lock
    local lock_dir
    if ! lock_dir="$(acquire_lock "${pr}")"; then
        log "PR #${pr}: skip fix — locked (gate 6)"
        return 0
    fi
    # shellcheck disable=SC2064
    trap "release_lock '${lock_dir}'" RETURN

    # Re-confirm live labels: BOTH ai/local-fix AND pr-status/needs-fix must be present
    if ! pr_has_label "${pr}" "ai/local-fix" || ! pr_has_label "${pr}" "pr-status/needs-fix"; then
        log "PR #${pr}: skip fix — live labels no longer have both ai/local-fix + pr-status/needs-fix"
        return 0
    fi

    # Re-check freshness after lock
    if ! check_freshness "${pr}" "${live_oid}"; then
        return 0
    fi

    # pr-meta extract: must be exit 0 / fresh
    local meta_json
    if ! meta_json="$(bash "${PR_META}" extract "${pr}" 2>/dev/null)"; then
        log "PR #${pr}: skip fix — pr-meta extract failed (no fresh block or stale headSha)"
        return 0
    fi

    # Check cycle.exhausted
    local exhausted next_agent
    exhausted="$(echo "${meta_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['cycle']['exhausted'])")"
    next_agent="$(echo "${meta_json}" | python3 -c "import json,sys; print(json.load(sys.stdin)['next']['agent'])")"

    if [[ "${exhausted}" == "True" ]]; then
        log "PR #${pr}: skip fix — cycle exhausted (circuit breaker, next.agent=human)"
        return 0
    fi
    if [[ "${next_agent}" == "human" ]]; then
        log "PR #${pr}: skip fix — next.agent=human (escalated to human)"
        return 0
    fi

    # Cx1-only gate: cx2/cx3/cx4 must all be 0
    local cx2 cx3 cx4
    cx2="$(echo "${meta_json}" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('findings',{}).get('byCx',{}).get('cx2',0))")"
    cx3="$(echo "${meta_json}" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('findings',{}).get('byCx',{}).get('cx3',0))")"
    cx4="$(echo "${meta_json}" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('findings',{}).get('byCx',{}).get('cx4',0))")"

    if [[ "${cx2}" != "0" || "${cx3}" != "0" || "${cx4}" != "0" ]]; then
        log "PR #${pr}: skip fix — findings include Cx2/Cx3/Cx4 (cx2=${cx2} cx3=${cx3} cx4=${cx4}); human required"
        return 0
    fi

    if dry_action "PR #${pr}: would run codex fix (workspace-write) on branch ${branch} oid=${live_oid:0:12}"; then
        return 0
    fi

    # Prepare worktree
    local wt
    wt="$(prepare_worktree "${pr}" "${branch}" "${live_oid}")"
    # shellcheck disable=SC2064
    trap "remove_worktree '${wt}'; release_lock '${lock_dir}'" RETURN

    # Gate 7: explicit -s workspace-write (never inherit global danger-full-access)
    log "PR #${pr}: running codex fix (sandbox=workspace-write worktree=${wt})"
    local fix_prompt
    fix_prompt="Fix the Cx1 findings listed in the most recent pm:pr-review comment on PR #${pr}. Abide by the fix protocol (§3.4 [AUTO-FIX]). Do NOT touch kernel interfaces, migrations, or concurrency primitives. If you encounter any of those, self-abort and leave a note in a comment instead of modifying the code."

    if ! codex exec \
            -s workspace-write \
            -C "${wt}" \
            --base develop \
            "${fix_prompt}"; then
        log "PR #${pr}: codex fix exec failed; posting human-escalation comment"
        post_fix_escalation "${pr}" "${branch}" "${wt}" "codex exec failed"
        return 0
    fi

    # Build guard: compile + test changed packages + lint
    log "PR #${pr}: running build guard (go build + go test + golangci-lint)"
    if ! go -C "${wt}" build ./... 2>/dev/null; then
        log "PR #${pr}: build failed after codex fix; posting human-escalation comment"
        post_fix_escalation "${pr}" "${branch}" "${wt}" "go build ./... failed after codex fix"
        return 0
    fi

    # Find changed packages and test them
    local changed_pkgs
    changed_pkgs="$(git -C "${wt}" diff --name-only HEAD | \
        python3 -c "
import sys, os
files = sys.stdin.read().strip().split('\n')
pkgs = set()
for f in files:
    if f.endswith('.go'):
        d = os.path.dirname(f)
        pkgs.add('./' + d if d else './.')
print('\n'.join(sorted(pkgs)))
" 2>/dev/null || true)"

    if [[ -n "${changed_pkgs}" ]]; then
        while IFS= read -r pkg; do
            if [[ -n "${pkg}" ]]; then
                if ! go -C "${wt}" test "${pkg}" 2>/dev/null; then
                    log "PR #${pr}: tests failed for ${pkg}; posting human-escalation comment"
                    post_fix_escalation "${pr}" "${branch}" "${wt}" "go test ${pkg} failed"
                    return 0
                fi
            fi
        done <<< "${changed_pkgs}"
    fi

    # Run golangci-lint if available
    if command -v golangci-lint >/dev/null 2>&1; then
        if ! golangci-lint run -C "${wt}" --new-from-rev=HEAD~1 ./... 2>/dev/null; then
            log "PR #${pr}: golangci-lint failed; posting human-escalation comment"
            post_fix_escalation "${pr}" "${branch}" "${wt}" "golangci-lint reported issues"
            return 0
        fi
    fi

    # Commit changed files (NEVER git add -A)
    local changed_files
    changed_files="$(git -C "${wt}" diff --name-only HEAD 2>/dev/null || true)"
    if [[ -z "${changed_files}" ]]; then
        log "PR #${pr}: codex fix made no changes; skipping commit+push"
        return 0
    fi

    while IFS= read -r f; do
        if [[ -n "${f}" ]]; then
            git -C "${wt}" add -- "${f}"
        fi
    done <<< "${changed_files}"

    local round
    round="$(pr_round "${pr}")"
    local new_round=$(( round + 1 ))

    git -C "${wt}" commit -m "fix(pr-${pr}): codex auto-fix round ${new_round} [#935][#1662]
Co-Authored-By: codex <noreply@codex.ai>"

    # B5 conflict precheck before push
    local mergeable
    mergeable="$(gh pr view "${pr}" --repo "${REPO_SLUG}" \
        --json mergeable --jq '.mergeable' 2>/dev/null || echo "UNKNOWN")"
    if [[ "${mergeable}" == "CONFLICTING" ]]; then
        log "PR #${pr}: conflict detected before push; merging origin/develop"
        git -C "${wt}" fetch origin develop --quiet
        if ! git -C "${wt}" merge origin/develop --no-edit --quiet; then
            log "PR #${pr}: merge conflict resolution failed; posting human-escalation"
            post_fix_escalation "${pr}" "${branch}" "${wt}" "merge conflict with develop"
            return 0
        fi
        # Re-run build guard after merge
        if ! go -C "${wt}" build ./... 2>/dev/null; then
            log "PR #${pr}: build failed after merge; escalating"
            post_fix_escalation "${pr}" "${branch}" "${wt}" "go build failed after merge"
            return 0
        fi
    fi

    git -C "${wt}" push origin "HEAD:${branch}"
    log "PR #${pr}: pushed fix commit to ${branch}"

    # F6: re-read new head sha after push — live_oid is pre-push and must not be used in machine block
    local pushed_oid
    pushed_oid="$(git -C "${wt}" rev-parse HEAD 2>/dev/null || echo "${live_oid}")"
    log "PR #${pr}: post-push head sha=${pushed_oid:0:12}"

    # Post pm:fix comment with machine block
    local body_file
    body_file="$(mktemp "${GOCELL_ROUTER_HOME}/state/fixbody-${pr}-XXXXXX.md")"

    local cx1
    cx1="$(echo "${meta_json}" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('findings',{}).get('byCx',{}).get('cx1',0))")"
    local total
    total="$(echo "${meta_json}" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('findings',{}).get('total',0))")"

    cat > "${body_file}" <<FIXBODY
<!-- pm:fix -->
## 🔁 fix（findings triage + fix）

**Findings** ${total}（已修 Cx1 ${cx1} · 遗留 Cx3/Cx4 0 · OUT_OF_SCOPE 0）

codex 自动修复了 ${cx1} 个 Cx1 finding。

**下一步**：切 \`pr-status/needs-check-fix\`（待 \`/pr-review --check\` 验证）。

---
🤖 PR #${pr} · Generated with Codex · branch ${branch} · session —
FIXBODY

    local meta_block
    meta_block="$(jq -nc \
        --arg tool "codex" \
        --arg repo "${REPO_SLUG}" \
        --argjson pr "${pr}" \
        --arg headRef "${branch}" \
        --arg headSha "${pushed_oid}" \
        --argjson round "${new_round}" \
        --argjson total "${total}" \
        --argjson cx1_count "${cx1}" \
        '{kind:"fix",phase:"fix",verdict:"needs-check-fix",repo:$repo,pr:$pr,
          tool:$tool,
          baseRef:"develop",headRef:$headRef,headSha:$headSha,session:null,worktree:null,
          findings:{total:$total,fixed:$cx1_count,unresolved:0,blocking:0,
                    byP:{p0:0,p1:0,p2:0,p3:0},
                    byCx:{cx1:$cx1_count,cx2:0,cx3:0,cx4:0}},
          cycle:{round:$round}}' \
        | bash "${PR_META}" emit)" || {
        log "PR #${pr}: pr-meta emit for fix comment failed"
        rm -f "${body_file}"
        return 0
    }
    echo "${meta_block}" >> "${body_file}"

    local comment_url
    comment_url="$(gh pr comment "${pr}" --repo "${REPO_SLUG}" --body-file "${body_file}")"
    log "PR #${pr}: fix comment posted → ${comment_url}"
    rm -f "${body_file}"

    # Flip to needs-check-fix
    gh pr edit "${pr}" --repo "${REPO_SLUG}" \
        --add-label "pr-status/needs-check-fix" \
        --remove-label "pr-status/needs-fix" 2>/dev/null || true
    log "PR #${pr}: labels → needs-check-fix"

    mark_seen "${pr}" "${live_oid}" "fix"
}

# post_fix_escalation <pr> <branch> <wt> <reason>
# F9: do NOT embed ${wt} in the comment body — wt is deleted by the RETURN trap before (or
# simultaneously with) this call, so the path is already invalid when a human reads the comment.
# Instead, give a reproducible checkout command so the human can reconstruct the state.
post_fix_escalation() {
    local pr="$1" branch="$2" wt="$3" reason="$4"
    local body_file
    body_file="$(mktemp "${GOCELL_ROUTER_HOME}/state/escalate-${pr}-XXXXXX.md")"
    cat > "${body_file}" <<ESC
<!-- pm:fix -->
## 🔁 fix（codex 自动修复未完成，转人工）

codex 自动修复失败（原因：${reason}）。

**下一步**：请人工介入 —— 检出分支 \`${branch}\`，查阅 router 日志（\`${GOCELL_ROUTER_HOME}/logs/\`），修复后 push，再切 \`pr-status/needs-check-fix\`：

\`\`\`
git checkout ${branch}
# 查阅 router 日志了解失败详情
# 修复后：
git add <files> && git commit -m "fix: ..." && git push
gh pr edit ${pr} --add-label pr-status/needs-check-fix --remove-label pr-status/needs-fix
\`\`\`

---
🤖 PR #${pr} · Generated with Codex · branch ${branch} · session —
ESC
    local comment_url
    comment_url="$(gh pr comment "${pr}" --repo "${REPO_SLUG}" --body-file "${body_file}" 2>/dev/null || echo "(comment failed)")"
    log "PR #${pr}: escalation comment posted → ${comment_url}"
    rm -f "${body_file}"

    # Remove ai/local-fix to prevent re-triggering
    gh pr edit "${pr}" --repo "${REPO_SLUG}" \
        --remove-label "ai/local-fix" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# poll_once — query each trigger label; dispatch handlers
# ---------------------------------------------------------------------------

poll_once() {
    log "poll_once: querying ${REPO_SLUG} for candidate PRs"

    local pr_json_list

    # Trigger: pr-status/needs-review-again → handle_review review
    pr_json_list="$(gh pr list --repo "${REPO_SLUG}" \
        --state open \
        --label "pr-status/needs-review-again" \
        --json "number,headRefName,headRefOid,isCrossRepository,author,isDraft" \
        2>/dev/null || echo "[]")"
    echo "${pr_json_list}" | python3 -c "
import json, sys
prs = json.load(sys.stdin)
for pr in prs:
    print(json.dumps(pr))
" | while IFS= read -r pr_obj; do
        handle_review "review" "${pr_obj}" || true
    done

    # Trigger: pr-status/needs-check-fix → handle_review check
    pr_json_list="$(gh pr list --repo "${REPO_SLUG}" \
        --state open \
        --label "pr-status/needs-check-fix" \
        --json "number,headRefName,headRefOid,isCrossRepository,author,isDraft" \
        2>/dev/null || echo "[]")"
    echo "${pr_json_list}" | python3 -c "
import json, sys
prs = json.load(sys.stdin)
for pr in prs:
    print(json.dumps(pr))
" | while IFS= read -r pr_obj; do
        handle_review "check" "${pr_obj}" || true
    done

    # Trigger: BOTH pr-status/needs-fix AND ai/local-fix → handle_fix
    # We query needs-fix and filter for ai/local-fix presence live in handle_fix
    pr_json_list="$(gh pr list --repo "${REPO_SLUG}" \
        --state open \
        --label "pr-status/needs-fix" \
        --label "ai/local-fix" \
        --json "number,headRefName,headRefOid,isCrossRepository,author,isDraft" \
        2>/dev/null || echo "[]")"
    echo "${pr_json_list}" | python3 -c "
import json, sys
prs = json.load(sys.stdin)
for pr in prs:
    print(json.dumps(pr))
" | while IFS= read -r pr_obj; do
        handle_fix "${pr_obj}" || true
    done

    log "poll_once: done"
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

main() {
    local once=0 dry_run=0

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --once)    once=1 ;;
            --dry-run) dry_run=1 ;;
            -h|--help) usage; exit 0 ;;
            *) echo "router: unknown flag '$1'" >&2; usage; exit 64 ;;
        esac
        shift
    done

    DRY_RUN="${dry_run}"
    ONCE="${once}"

    validate_env

    if [[ "${DRY_RUN}" -eq 1 ]]; then
        log "router: DRY-RUN mode — evaluating gates, printing intended actions, NO side effects"
    fi

    if [[ "${ONCE}" -eq 1 ]]; then
        poll_once
        return 0
    fi

    log "router: starting loop (interval=${INTERVAL}s engine=${REVIEW_ENGINE})"
    while true; do
        poll_once || log "router: poll_once returned non-zero; continuing loop"
        sleep "${INTERVAL}"
    done
}

main "$@"
