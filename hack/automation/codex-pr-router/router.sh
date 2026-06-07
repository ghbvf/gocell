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

# acquire_lock <pr> — atomic mkdir; prints lock path on success; returns 1 if locked
acquire_lock() {
    local lock_dir="${GOCELL_ROUTER_HOME}/locks/${1}.lock"
    if mkdir "${lock_dir}" 2>/dev/null; then
        echo "${lock_dir}"
        return 0
    fi
    return 1
}

release_lock() {
    local lock_dir="$1"
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
    git -C "${REPO_ROOT}" fetch origin "${branch}" --quiet 2>/dev/null || \
        git -C "${REPO_ROOT}" fetch origin --quiet 2>/dev/null || true

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

    local verdict total p0 p1 p2 p3 cx1 cx2 cx3 cx4
    verdict="$(python3 -c "import json,sys; d=json.load(open('${verdict_file}')); print(d['verdict'])")"
    total="$(python3 -c "import json,sys; d=json.load(open('${verdict_file}')); print(d['counts']['total'])")"
    p0="$(python3 -c "import json,sys; d=json.load(open('${verdict_file}')); print(d['counts']['byP']['p0'])")"
    p1="$(python3 -c "import json,sys; d=json.load(open('${verdict_file}')); print(d['counts']['byP']['p1'])")"
    p2="$(python3 -c "import json,sys; d=json.load(open('${verdict_file}')); print(d['counts']['byP']['p2'])")"
    p3="$(python3 -c "import json,sys; d=json.load(open('${verdict_file}')); print(d['counts']['byP']['p3'])")"
    cx1="$(python3 -c "import json,sys; d=json.load(open('${verdict_file}')); print(d['counts']['byCx']['cx1'])")"
    cx2="$(python3 -c "import json,sys; d=json.load(open('${verdict_file}')); print(d['counts']['byCx']['cx2'])")"
    cx3="$(python3 -c "import json,sys; d=json.load(open('${verdict_file}')); print(d['counts']['byCx']['cx3'])")"
    cx4="$(python3 -c "import json,sys; d=json.load(open('${verdict_file}')); print(d['counts']['byCx']['cx4'])")"

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

    # Re-check freshness after acquiring lock (TOCTOU guard)
    if ! check_freshness "${pr}" "${live_oid}"; then
        return 0
    fi

    if [[ "${DRY_RUN}" -eq 1 ]]; then
        log_dry "PR #${pr}: would run ${kind} via ${REVIEW_ENGINE} (oid=${live_oid:0:12} branch=${branch})"
        return 0
    fi

    # Engine knob: claude alternate
    if [[ "${REVIEW_ENGINE}" == "claude" ]]; then
        log "PR #${pr}: running via claude engine (claude -p '/pr-review ${pr}')"
        claude -p "/pr-review ${pr}"
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
        codex_prompt="Review this PR (base=develop). This is a CHECK phase: verify that findings from the previous review round have been fixed. For each prior finding, determine if it is fixed, not-fixed, regression, or partial. Emit structured JSON per the output schema."
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

    # Validate the output parses
    if ! python3 -c "import json; json.load(open('${out_file}'))" 2>/dev/null; then
        log "PR #${pr}: codex output is not valid JSON; skipping"
        rm -f "${out_file}"
        return 0
    fi

    local verdict
    verdict="$(python3 -c "import json; print(json.load(open('${out_file}'))['verdict'])")"
    log "PR #${pr}: codex verdict=${verdict} (kind=${kind})"

    local round
    round="$(pr_round "${pr}")"

    # Render comment body
    local body_file
    body_file="$(mktemp "${GOCELL_ROUTER_HOME}/state/body-${pr}-XXXXXX.md")"
    render_pr_review_body "${out_file}" "${pr}" "${kind}" "${branch}" "${live_oid}" "${wt}" "${round}" > "${body_file}"

    # Emit machine block and append to body
    local meta_block
    meta_block="$(jq -nc \
        --arg repo "${REPO_SLUG}" \
        --argjson pr "${pr}" \
        --arg baseRef "develop" \
        --arg headRef "${branch}" \
        --arg headSha "${live_oid}" \
        --arg phase "${kind}" \
        --arg verdict "${verdict}" \
        --arg wt "${wt}" \
        --argjson round "${round}" \
        --argjson total "$(python3 -c "import json; print(json.load(open('${out_file}'))['counts']['total'])")" \
        --argjson p0 "$(python3 -c "import json; print(json.load(open('${out_file}'))['counts']['byP']['p0'])")" \
        --argjson p1 "$(python3 -c "import json; print(json.load(open('${out_file}'))['counts']['byP']['p1'])")" \
        --argjson p2 "$(python3 -c "import json; print(json.load(open('${out_file}'))['counts']['byP']['p2'])")" \
        --argjson p3 "$(python3 -c "import json; print(json.load(open('${out_file}'))['counts']['byP']['p3'])")" \
        --argjson cx1 "$(python3 -c "import json; print(json.load(open('${out_file}'))['counts']['byCx']['cx1'])")" \
        --argjson cx2 "$(python3 -c "import json; print(json.load(open('${out_file}'))['counts']['byCx']['cx2'])")" \
        --argjson cx3 "$(python3 -c "import json; print(json.load(open('${out_file}'))['counts']['byCx']['cx3'])")" \
        --argjson cx4 "$(python3 -c "import json; print(json.load(open('${out_file}'))['counts']['byCx']['cx4'])")" \
        '{kind:"pr-review",phase:$phase,verdict:$verdict,repo:$repo,pr:$pr,
          baseRef:$baseRef,headRef:$headRef,headSha:$headSha,session:null,worktree:$wt,
          findings:{total:$total,byP:{p0:$p0,p1:$p1,p2:$p2,p3:$p3},
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

    # Label flips (issues B3, 5-state)
    flip_labels_review "${pr}" "${kind}" "${verdict}"

    # Mark seen (gate 5)
    mark_seen "${pr}" "${live_oid}" "${kind}"
}

# ---------------------------------------------------------------------------
# flip_labels_review <pr> <kind:review|check> <verdict>
# Implements the 5-state label machine (PROJECT.md §2.5 / §5).
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
                    --remove-label "pr-status/needs-review-again" 2>/dev/null || true
                log "PR #${pr}: labels → changes-requested + needs-fix"
                ;;
            approved)
                gh pr edit "${pr}" --repo "${REPO_SLUG}" \
                    --add-label "pr-review/approved" \
                    --remove-label "pr-review/changes-requested" \
                    --remove-label "pr-status/needs-review-again" 2>/dev/null || true
                log "PR #${pr}: labels → approved"
                ;;
        esac
    elif [[ "${kind}" == "check" ]]; then
        case "${verdict}" in
            ready)
                # B5 conflict precheck before flipping to ready
                local mergeable
                mergeable="$(gh pr view "${pr}" --repo "${REPO_SLUG}" \
                    --json mergeable --jq '.mergeable' 2>/dev/null || echo "UNKNOWN")"
                if [[ "${mergeable}" == "CONFLICTING" ]]; then
                    log "PR #${pr}: check ready but CONFLICTING — treating as changes-requested"
                    gh pr edit "${pr}" --repo "${REPO_SLUG}" \
                        --add-label "pr-review/changes-requested" \
                        --add-label "pr-status/needs-fix" \
                        --remove-label "pr-status/needs-check-fix" \
                        --remove-label "pr-review/approved" 2>/dev/null || true
                else
                    gh pr edit "${pr}" --repo "${REPO_SLUG}" \
                        --add-label "pr-status/ready" \
                        --add-label "pr-review/approved" \
                        --remove-label "pr-status/needs-check-fix" \
                        --remove-label "pr-review/changes-requested" 2>/dev/null || true
                    log "PR #${pr}: labels → ready + approved"
                fi
                ;;
            changes-requested)
                gh pr edit "${pr}" --repo "${REPO_SLUG}" \
                    --add-label "pr-review/changes-requested" \
                    --add-label "pr-status/needs-fix" \
                    --remove-label "pr-status/needs-check-fix" \
                    --remove-label "pr-review/approved" 2>/dev/null || true
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

    if [[ "${DRY_RUN}" -eq 1 ]]; then
        log_dry "PR #${pr}: would run codex fix (workspace-write) on branch ${branch} oid=${live_oid:0:12}"
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
        if ! golangci-lint run --new-from-rev=HEAD~1 "${wt}/..." 2>/dev/null; then
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
            git -C "${wt}" add "${f}"
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
🤖 PR #${pr} · Generated with Codex · branch ${branch} · worktree ${wt} · session —
FIXBODY

    local meta_block
    meta_block="$(jq -nc \
        --arg repo "${REPO_SLUG}" \
        --argjson pr "${pr}" \
        --arg headRef "${branch}" \
        --arg headSha "${live_oid}" \
        --arg wt "${wt}" \
        --argjson round "${new_round}" \
        --argjson total "${total}" \
        '{kind:"fix",phase:"fix",verdict:"needs-check-fix",repo:$repo,pr:$pr,
          baseRef:"develop",headRef:$headRef,headSha:$headSha,session:null,worktree:$wt,
          findings:{total:$total,byP:{p0:0,p1:0,p2:0,p3:0},
                    byCx:{cx1:0,cx2:0,cx3:0,cx4:0}},
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
post_fix_escalation() {
    local pr="$1" branch="$2" wt="$3" reason="$4"
    local body_file
    body_file="$(mktemp "${GOCELL_ROUTER_HOME}/state/escalate-${pr}-XXXXXX.md")"
    cat > "${body_file}" <<ESC
<!-- pm:fix -->
## 🔁 fix（codex 自动修复未完成，转人工）

codex 自动修复失败（原因：${reason}）。

**下一步**：请人工介入检查 worktree \`${wt}\`，修复后 push，再切 \`pr-status/needs-check-fix\`。

---
🤖 PR #${pr} · Generated with Codex · branch ${branch} · worktree ${wt} · session —
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
