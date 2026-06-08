#!/usr/bin/env bash
# router-selftest.sh — offline regression selftest for router.sh decision logic.
# Stubs: gh, codex, claude, git via PATH shim dir.
# Exercises 7 scenarios (codex review F11). No network, no real external commands.
#
# Usage:  bash hack/automation/codex-pr-router/router-selftest.sh
# Exit:   0 = all PASS; non-zero = at least one FAIL
#
# ref: hack/automation/pr-meta.sh selftest — general shape.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROUTER="${SCRIPT_DIR}/router.sh"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"
PR_META="${REPO_ROOT}/hack/automation/pr-meta.sh"

# ---------------------------------------------------------------------------
# Selftest harness
# ---------------------------------------------------------------------------

PASS_COUNT=0
FAIL_COUNT=0
CHECK_COUNT=0

pass() { echo "PASS [$1]"; PASS_COUNT=$(( PASS_COUNT + 1 )); CHECK_COUNT=$(( CHECK_COUNT + 1 )); }
fail() { echo "FAIL [$1]: $2"; FAIL_COUNT=$(( FAIL_COUNT + 1 )); CHECK_COUNT=$(( CHECK_COUNT + 1 )); }

assert_contains() {
    local name="$1" haystack="$2" needle="$3"
    if echo "${haystack}" | grep -qF "${needle}"; then
        pass "${name}"
    else
        fail "${name}" "expected output to contain '${needle}'"
    fi
}

assert_not_contains() {
    local name="$1" haystack="$2" needle="$3"
    if echo "${haystack}" | grep -qF "${needle}"; then
        fail "${name}" "expected output NOT to contain '${needle}'"
    else
        pass "${name}"
    fi
}

# ---------------------------------------------------------------------------
# Temp workspace
# ---------------------------------------------------------------------------

WORKDIR="$(mktemp -d)"
STUB_BIN="${WORKDIR}/bin"
ROUTER_HOME="${WORKDIR}/router_home"

mkdir -p \
    "${STUB_BIN}" \
    "${ROUTER_HOME}/worktrees" \
    "${ROUTER_HOME}/locks" \
    "${ROUTER_HOME}/state" \
    "${ROUTER_HOME}/logs"

cleanup() { rm -rf "${WORKDIR}"; }
trap cleanup EXIT

CALLS_LOG="${WORKDIR}/calls.log"
: > "${CALLS_LOG}"

# ---------------------------------------------------------------------------
# Scenario data files that stubs read at runtime
# ---------------------------------------------------------------------------

GH_LIST_FILE="${WORKDIR}/gh_list.json"
GH_OID_FILE="${WORKDIR}/gh_oid.json"
GH_LABELS_FILE="${WORKDIR}/gh_labels.json"
GH_MERGE_FILE="${WORKDIR}/gh_merge.json"
# GH_API_BODIES_FILE: pre-filtered bodies returned by gh api (one body per line)
GH_API_BODIES_FILE="${WORKDIR}/gh_api_bodies.txt"
CODEX_VERDICT_FILE="${WORKDIR}/codex_verdict.json"
CODEX_FAIL_FLAG="${WORKDIR}/codex_fail"
GH_EDIT_FAIL_FLAG="${WORKDIR}/gh_edit_fail"
GH_COMMENT_FAIL_FLAG="${WORKDIR}/gh_comment_fail"

# Initialise all data files to safe defaults
echo '[]' > "${GH_LIST_FILE}"
echo '{"headRefOid":""}' > "${GH_OID_FILE}"
echo '{"labels":[]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
: > "${GH_API_BODIES_FILE}"
: > "${GH_COMMENT_FAIL_FLAG}"

# ---------------------------------------------------------------------------
# Python3 helper for gh --jq filtering (separate file to avoid quoting hell)
# ---------------------------------------------------------------------------
GH_JQ_HELPER="${WORKDIR}/gh_jq_helper.py"
cat > "${GH_JQ_HELPER}" << 'PYEOF'
#!/usr/bin/env python3
"""Minimal jq-like filter for gh stub. argv[1]=src_file argv[2]=filter_string"""
import json
import re
import sys

src_file = sys.argv[1]
filt = sys.argv[2].strip()

with open(src_file) as f:
    data = json.load(f)

if filt.startswith('.labels[].name'):
    parts = filt.split('|')
    labels = data.get('labels', [])
    names = [la['name'] for la in labels]
    if len(parts) > 1 and 'select' in parts[1]:
        m = re.search(r'== "([^"]+)"', parts[1])
        if m:
            target = m.group(1)
            for n in names:
                if n == target:
                    print(n)
    else:
        for n in names:
            print(n)
elif filt == '.headRefOid':
    print(data.get('headRefOid', ''))
elif filt == '.mergeable':
    print(data.get('mergeable', 'UNKNOWN'))
else:
    key = filt.lstrip('.')
    print(data.get(key, ''))
PYEOF
chmod +x "${GH_JQ_HELPER}"

# ---------------------------------------------------------------------------
# Stub: git — no-op for all calls
# ---------------------------------------------------------------------------
cat > "${STUB_BIN}/git" << 'GSTUB'
#!/usr/bin/env bash
exit 0
GSTUB
chmod +x "${STUB_BIN}/git"

# ---------------------------------------------------------------------------
# Stub: jq — use real jq if available; otherwise minimal python3 fallback
# ---------------------------------------------------------------------------
JQ_REAL="$(command -v jq 2>/dev/null || true)"
if [[ -n "${JQ_REAL}" ]]; then
    ln -sf "${JQ_REAL}" "${STUB_BIN}/jq"
else
    # Minimal python3 fallback: the router now calls `pr-meta.sh emit-block`,
    # which assembles its facts JSON with jq internally. A real jq is required for
    # the S5 emit path; this stub only keeps non-emit scenarios from crashing.
    cat > "${STUB_BIN}/jq" << 'JQSTUB'
#!/usr/bin/env bash
# Minimal jq stub: just print empty JSON object and exit 0
echo '{}'
JQSTUB
    chmod +x "${STUB_BIN}/jq"
fi

# ---------------------------------------------------------------------------
# Stub: claude — no-op
# ---------------------------------------------------------------------------
cat > "${STUB_BIN}/claude" << CSTUB
#!/usr/bin/env bash
echo "claude \$*" >> "${CALLS_LOG}"
exit 0
CSTUB
chmod +x "${STUB_BIN}/claude"

# ---------------------------------------------------------------------------
# Stub: gh — reads scenario data files; records write-side calls.
# For 'gh api': returns pre-filtered body text from GH_API_BODIES_FILE
# (simulating what real jq would output after filtering comment bodies).
# ---------------------------------------------------------------------------
cat > "${STUB_BIN}/gh" << GHSTUB
#!/usr/bin/env bash
GH_LIST_FILE="${GH_LIST_FILE}"
GH_OID_FILE="${GH_OID_FILE}"
GH_LABELS_FILE="${GH_LABELS_FILE}"
GH_MERGE_FILE="${GH_MERGE_FILE}"
GH_API_BODIES_FILE="${GH_API_BODIES_FILE}"
GH_EDIT_FAIL_FLAG="${GH_EDIT_FAIL_FLAG}"
GH_COMMENT_FAIL_FLAG="${GH_COMMENT_FAIL_FLAG}"
CALLS_LOG="${CALLS_LOG}"
GH_JQ_HELPER="${GH_JQ_HELPER}"

cmd="\${1:-}"
sub="\${2:-}"

case "\${cmd}" in
    pr)
        case "\${sub}" in
            list)
                cat "\${GH_LIST_FILE}"
                ;;
            view)
                all_args="\$*"
                jq_filter=""
                prev=""
                for a in "\$@"; do
                    if [[ "\${prev}" == "--jq" ]]; then
                        jq_filter="\${a}"
                    fi
                    prev="\${a}"
                done
                if echo "\${all_args}" | grep -q 'headRefOid'; then
                    src="\${GH_OID_FILE}"
                elif echo "\${all_args}" | grep -q 'labels'; then
                    src="\${GH_LABELS_FILE}"
                elif echo "\${all_args}" | grep -q 'mergeable'; then
                    src="\${GH_MERGE_FILE}"
                else
                    src="\${GH_LABELS_FILE}"
                fi
                if [[ -n "\${jq_filter}" ]]; then
                    python3 "\${GH_JQ_HELPER}" "\${src}" "\${jq_filter}"
                else
                    cat "\${src}"
                fi
                ;;
            edit)
                echo "gh pr edit \$*" >> "\${CALLS_LOG}"
                if [[ -f "\${GH_EDIT_FAIL_FLAG}" ]]; then
                    exit 1
                fi
                exit 0
                ;;
            comment)
                echo "gh pr comment \$*" >> "\${CALLS_LOG}"
                if [[ -f "\${GH_COMMENT_FAIL_FLAG}" ]]; then
                    exit 1
                fi
                echo "https://github.com/ghbvf/gocell/pull/42#issuecomment-stub"
                ;;
        esac
        ;;
    api)
        # Return pre-filtered body text (what real jq would output after the
        # fetch_trusted_bodies filter). Each line is one comment body.
        cat "\${GH_API_BODIES_FILE}"
        ;;
    *)
        exit 0
        ;;
esac
GHSTUB
chmod +x "${STUB_BIN}/gh"

# ---------------------------------------------------------------------------
# Stub: codex — reads CODEX_VERDICT_FILE; fails if CODEX_FAIL_FLAG exists
# ---------------------------------------------------------------------------
cat > "${STUB_BIN}/codex" << CSTUB
#!/usr/bin/env bash
CODEX_VERDICT_FILE="${CODEX_VERDICT_FILE}"
CODEX_FAIL_FLAG="${CODEX_FAIL_FLAG}"
CALLS_LOG="${CALLS_LOG}"

echo "codex exec \$*" >> "\${CALLS_LOG}"

if [[ -f "\${CODEX_FAIL_FLAG}" ]]; then
    exit 1
fi

out_file=""
prev=""
for a in "\$@"; do
    if [[ "\${prev}" == "-o" ]]; then
        out_file="\${a}"
    fi
    prev="\${a}"
done

if [[ -n "\${out_file}" && -f "\${CODEX_VERDICT_FILE}" ]]; then
    cp "\${CODEX_VERDICT_FILE}" "\${out_file}"
fi
exit 0
CSTUB
chmod +x "${STUB_BIN}/codex"

# ---------------------------------------------------------------------------
# Shared canned verdict JSONs
# ---------------------------------------------------------------------------

VERDICT_CHANGES_REQUESTED="${WORKDIR}/verdict_cr.json"
cat > "${VERDICT_CHANGES_REQUESTED}" << 'VJSON'
{
  "verdict": "changes-requested",
  "findings": [
    {
      "fileLine": "foo/bar.go:10",
      "p": "P2",
      "cx": "Cx1",
      "dimension": "correctness",
      "summary": "missing nil check"
    }
  ],
  "counts": {"total":1,"byP":{"p0":0,"p1":0,"p2":1,"p3":0},"byCx":{"cx1":1,"cx2":0,"cx3":0,"cx4":0}}
}
VJSON

VERDICT_APPROVED="${WORKDIR}/verdict_approved.json"
cat > "${VERDICT_APPROVED}" << 'VJSON'
{
  "verdict": "approved",
  "findings": [],
  "counts": {"total":0,"byP":{"p0":0,"p1":0,"p2":0,"p3":0},"byCx":{"cx1":0,"cx2":0,"cx3":0,"cx4":0}}
}
VJSON

VERDICT_MALFORMED="${WORKDIR}/verdict_bad.json"
cat > "${VERDICT_MALFORMED}" << 'VJSON'
{
  "verdict": "unknown-bad-value",
  "findings": [],
  "counts": {"total":0,"byP":{"p0":0,"p1":0,"p2":0,"p3":0},"byCx":{"cx1":0,"cx2":0,"cx3":0,"cx4":0}}
}
VJSON

VERDICT_BAD_FINDING="${WORKDIR}/verdict_badfinding.json"
cat > "${VERDICT_BAD_FINDING}" << 'VJSON'
{
  "verdict": "changes-requested",
  "findings": [
    {
      "fileLine": "BADNOCOION",
      "p": "P2",
      "cx": "Cx1",
      "dimension": "correctness",
      "summary": "bad fileLine no colon separator"
    }
  ],
  "counts": {"total":1,"byP":{"p0":0,"p1":0,"p2":1,"p3":0},"byCx":{"cx1":1,"cx2":0,"cx3":0,"cx4":0}}
}
VJSON

# ---------------------------------------------------------------------------
# 40-char hex OIDs (pr-meta emit-block requires headSha to match ^[0-9a-f]{40}$)
# ---------------------------------------------------------------------------
OID_REVIEW="aaaa1111bbbb2222cccc3333dddd4444eeee5555"  # S1,S2,S4,S6 standard review OID
OID_S2="ffff0000aaaa1111bbbb2222cccc3333dddd4444"      # S2 uses different PR#50 OID
OID_S3_LISTED="aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000"  # S3 listed OID
OID_S3_LIVE="bbbb1111bbbb1111bbbb1111bbbb1111bbbb1111"    # S3 live OID (different → gate fires)
OID_S5="dddd4444eeee5555ffff6666aaaa7777bbbb8888"      # S5 fix path OID
OID_S7="1111aaaa2222bbbb3333cccc4444dddd5555eeee"      # S7 label-gone OID

# ---------------------------------------------------------------------------
# Pre-generate machine block bodies for S5 (handle_fix needs pr-meta extract)
# The block must have headSha matching the live OID the stub returns.
# We emit two blocks: S5a (cx2=1, router must skip) and S5b (cx2=0, proceed).
#
# fetch_trusted_bodies calls: gh api ... --jq '.[] | select(...) | .body'
# Our stub returns the pre-filtered result — just the raw body text.
# The body must contain:
#   1. <!-- pm:pr-review --> marker (for the trust filter)
#   2. <!-- gocell-pr-meta:v1 <base64> --> line (for extract to find)
# ---------------------------------------------------------------------------

# S5a block: cx2=1, cx1=1, total=2 (round-base=1 -> pr-review carry round=1).
# Built via the emit-block funnel with full overrides (offline: refs + round-base
# supplied, so no gh/git/env access).
S5A_BLOCK="$(bash "${PR_META}" emit-block \
    --kind=pr-review --phase=review --verdict=changes-requested \
    --pr=43 --tool=codex \
    --head-sha="${OID_S5}" --base-ref=develop --head-ref=feat/fix \
    --round-base=1 --session= --worktree= \
    --findings='{"total":2,"fixed":0,"unresolved":2,"blocking":0,"byP":{"p0":0,"p1":0,"p2":2,"p3":0},"byCx":{"cx1":1,"cx2":1,"cx3":0,"cx4":0}}' \
    2>/dev/null)" || {
    echo "FATAL: pr-meta emit-block failed for S5a — cannot continue selftest" >&2
    exit 1
}

# S5a body: contains pm:pr-review marker so fetch_trusted_bodies selects it
S5A_BODY="<!-- pm:pr-review -->
## pr-review stub comment
${S5A_BLOCK}"

# S5b block: cx2=0, cx1=1, total=1 (round-base=1 -> pr-review carry round=1)
S5B_BLOCK="$(bash "${PR_META}" emit-block \
    --kind=pr-review --phase=review --verdict=changes-requested \
    --pr=43 --tool=codex \
    --head-sha="${OID_S5}" --base-ref=develop --head-ref=feat/fix \
    --round-base=1 --session= --worktree= \
    --findings='{"total":1,"fixed":0,"unresolved":1,"blocking":0,"byP":{"p0":0,"p1":0,"p2":1,"p3":0},"byCx":{"cx1":1,"cx2":0,"cx3":0,"cx4":0}}' \
    2>/dev/null)" || {
    echo "FATAL: pr-meta emit-block failed for S5b — cannot continue selftest" >&2
    exit 1
}

S5B_BODY="<!-- pm:pr-review -->
## pr-review stub comment
${S5B_BLOCK}"

# ---------------------------------------------------------------------------
# run_router: run router.sh --once (optionally with --dry-run)
# Returns stdout+stderr; always exits 0 from router perspective.
# ---------------------------------------------------------------------------
run_router() {
    local dry_run_flag="${1:-}"
    # shellcheck disable=SC2086
    env \
        PATH="${STUB_BIN}:${PATH}" \
        GOCELL_ROUTER_HOME="${ROUTER_HOME}" \
        GOCELL_ROUTER_AUTHORS="alice" \
        GOCELL_ROUTER_INTERVAL="120" \
        GOCELL_ROUTER_REVIEW_ENGINE="codex" \
        bash "${ROUTER}" --once ${dry_run_flag} 2>&1 || true
}

# reset_scenario: clear state between scenarios
reset_scenario() {
    : > "${CALLS_LOG}"
    rm -f "${GH_EDIT_FAIL_FLAG}" "${CODEX_FAIL_FLAG}" "${GH_COMMENT_FAIL_FLAG}"
    : > "${GH_API_BODIES_FILE}"
    # Clear seen file so idempotency gate doesn't fire
    : > "${ROUTER_HOME}/state/seen"
    # Remove any stale locks
    rm -rf "${ROUTER_HOME}/locks/"*.lock 2>/dev/null || true
    # Reset codex verdict to changes-requested (safe default)
    cp "${VERDICT_CHANGES_REQUESTED}" "${CODEX_VERDICT_FILE}"
}

# ---------------------------------------------------------------------------
# Scenario 1: label XOR — 5-state label machine
# Case 1a: review/changes-requested → add needs-fix, remove needs-review-again
# Case 1b: review/approved (no findings) → add pr-status/ready (F4)
# ---------------------------------------------------------------------------
echo ""
echo "=== Scenario 1: label XOR / 5-state machine ==="

# 1a: changes-requested verdict
reset_scenario
echo '[{"number":42,"headRefName":"feat/t","headRefOid":"'"${OID_REVIEW}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_REVIEW}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-review-again"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
cp "${VERDICT_CHANGES_REQUESTED}" "${CODEX_VERDICT_FILE}"

out_1a="$(run_router)"

if grep -qF "gh pr edit" "${CALLS_LOG}" && \
   grep -qF "pr-status/needs-fix" "${CALLS_LOG}" && \
   grep -qF "pr-status/needs-review-again" "${CALLS_LOG}"; then
    pass "S1a: changes-requested → gh pr edit adds needs-fix removes needs-review-again"
else
    fail "S1a: changes-requested" "calls=$(cat "${CALLS_LOG}") out=${out_1a}"
fi
if grep -qF "needs-fix" "${CALLS_LOG}"; then
    pass "S1a-add-needs-fix"
else
    fail "S1a-add-needs-fix" "calls=$(cat "${CALLS_LOG}")"
fi
if grep -qF "needs-review-again" "${CALLS_LOG}"; then
    pass "S1a-remove-nra"
else
    fail "S1a-remove-nra" "calls=$(cat "${CALLS_LOG}")"
fi

# 1b: approved → ready (F4)
reset_scenario
echo '[{"number":42,"headRefName":"feat/t","headRefOid":"'"${OID_REVIEW}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_REVIEW}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-review-again"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
cp "${VERDICT_APPROVED}" "${CODEX_VERDICT_FILE}"

out_1b="$(run_router)"

if grep -qF "pr-status/ready" "${CALLS_LOG}"; then
    pass "S1b-add-ready"
else
    fail "S1b-add-ready" "calls=$(cat "${CALLS_LOG}") out=${out_1b}"
fi
if ! grep -qF "pr-status/needs-fix" "${CALLS_LOG}"; then
    pass "S1b-no-needs-fix"
else
    fail "S1b-no-needs-fix" "calls=$(cat "${CALLS_LOG}")"
fi

# ---------------------------------------------------------------------------
# Scenario 2: mark_seen only on success (F3)
# Case 2a: gh pr edit FAILS → NOT recorded in seen
# Case 2b: gh pr edit SUCCEEDS → IS recorded in seen
# ---------------------------------------------------------------------------
echo ""
echo "=== Scenario 2: mark_seen only on success (F3) ==="

SEEN_FILE="${ROUTER_HOME}/state/seen"

# 2a: PR already in seen file → idempotency gate skips (not processed again)
# gate 5 (is_seen) prevents codex from being called a second time for the same OID.
reset_scenario
echo '[{"number":50,"headRefName":"feat/s2","headRefOid":"'"${OID_S2}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_S2}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-review-again"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
# Pre-seed the seen file so the router treats this PR as already processed
echo "50@${OID_S2}:review" >> "${SEEN_FILE}"

out_2a="$(run_router)"

if ! grep -qF "codex exec" "${CALLS_LOG}"; then
    pass "S2a: already-seen → codex not called again (idempotency)"
else
    fail "S2a: already-seen should skip codex" "codex was called despite PR in seen"
fi
assert_contains "S2a-seen-log" "${out_2a}" "already processed"

# 2b: edit succeeds
reset_scenario
echo '[{"number":50,"headRefName":"feat/s2","headRefOid":"'"${OID_S2}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_S2}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-review-again"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
cp "${VERDICT_CHANGES_REQUESTED}" "${CODEX_VERDICT_FILE}"
# No GH_EDIT_FAIL_FLAG → edit succeeds

out_2b="$(run_router)"

if grep -qF "50@${OID_S2}:review" "${SEEN_FILE}" 2>/dev/null; then
    pass "S2b: edit-success → recorded in seen"
else
    fail "S2b: edit-success should record seen" "key NOT found; seen=$(cat "${SEEN_FILE}" 2>/dev/null) out=${out_2b}"
fi

# ---------------------------------------------------------------------------
# Scenario 3: head moved (gate 2 / TOCTOU)
# ---------------------------------------------------------------------------
echo ""
echo "=== Scenario 3: head moved gate ==="

reset_scenario
echo '[{"number":60,"headRefName":"feat/s3","headRefOid":"'"${OID_S3_LISTED}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
# Live OID is DIFFERENT from list OID → gate 2 fires
echo '{"headRefOid":"'"${OID_S3_LIVE}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-review-again"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"

out_3="$(run_router)"

if ! grep -qF "codex exec" "${CALLS_LOG}"; then
    pass "S3: head moved → codex not called"
else
    fail "S3: head moved" "codex was called despite head moving"
fi
assert_contains "S3-log-head-moved" "${out_3}" "head moved"

# ---------------------------------------------------------------------------
# Scenario 4: dry-run no side effects
# ---------------------------------------------------------------------------
echo ""
echo "=== Scenario 4: dry-run no side effects ==="

reset_scenario
echo '[{"number":42,"headRefName":"feat/t","headRefOid":"'"${OID_REVIEW}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_REVIEW}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-review-again"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"

out_4="$(run_router "--dry-run")"

if ! grep -qF "gh pr edit" "${CALLS_LOG}"; then
    pass "S4: dry-run → no gh pr edit"
else
    fail "S4: dry-run" "gh pr edit called in dry-run"
fi
if ! grep -qF "gh pr comment" "${CALLS_LOG}"; then
    pass "S4: dry-run → no gh pr comment"
else
    fail "S4: dry-run" "gh pr comment called in dry-run"
fi
if ! grep -qF "codex exec" "${CALLS_LOG}"; then
    pass "S4: dry-run → no codex exec"
else
    fail "S4: dry-run" "codex exec called in dry-run"
fi
assert_contains "S4-dry-run-log" "${out_4}" "DRY-RUN"

# ---------------------------------------------------------------------------
# Scenario 5: fix Cx1-only gate (F7)
# Case 5a: cx2 > 0 → handle_fix SKIPS (no codex exec workspace-write)
# Case 5b: cx1-only → handle_fix proceeds (codex workspace-write called)
#
# handle_fix calls: bash "${PR_META}" extract "${pr}"
# pr-meta extract calls:
#   1. fetch_trusted_bodies → gh api ... --jq '...' (returns body text via stub)
#   2. gh pr view --json headRefOid --jq .headRefOid (returns OID_S5 via stub)
# The machine block headSha must match the live OID (OID_S5).
#
# The GH_LIST_FILE must list the PR under ALL three label queries that poll_once
# makes (needs-review-again / needs-check-fix / needs-fix+ai/local-fix). Since
# handle_review and handle_check will skip (trigger label re-confirm fails after
# lock — the PR has needs-fix + ai/local-fix labels, not needs-review-again),
# handle_fix will be the path that actually proceeds.
# ---------------------------------------------------------------------------
echo ""
echo "=== Scenario 5: fix Cx1-only gate (F7) ==="

# 5a: cx2 > 0 → must skip
reset_scenario
echo '[{"number":43,"headRefName":"feat/fix","headRefOid":"'"${OID_S5}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_S5}"'"}' > "${GH_OID_FILE}"
# Labels must include BOTH ai/local-fix and pr-status/needs-fix for handle_fix
# to pass the live label re-confirm inside handle_fix.
# handle_review/check will get past gates but fail the trigger-label re-confirm.
echo '{"labels":[{"name":"pr-status/needs-fix"},{"name":"ai/local-fix"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
# Set up the pre-filtered body for pr-meta extract (cx2=1 block)
printf '%s\n' "${S5A_BODY}" > "${GH_API_BODIES_FILE}"
cp "${VERDICT_CHANGES_REQUESTED}" "${CODEX_VERDICT_FILE}"

out_5a="$(run_router)"

if ! grep -qF "workspace-write" "${CALLS_LOG}"; then
    pass "S5a: cx2>0 → skip codex workspace-write"
else
    fail "S5a: cx2>0 gate" "codex workspace-write called despite cx2>0"
fi
# Router logs "Cx2/Cx3/Cx4" when skipping due to cx2>0
assert_contains "S5a-skip-cx2-log" "${out_5a}" "Cx2"

# 5b: cx1-only → must proceed to codex workspace-write
reset_scenario
echo '[{"number":43,"headRefName":"feat/fix","headRefOid":"'"${OID_S5}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_S5}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-fix"},{"name":"ai/local-fix"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
# Set up the pre-filtered body for pr-meta extract (cx2=0 block)
printf '%s\n' "${S5B_BODY}" > "${GH_API_BODIES_FILE}"
cp "${VERDICT_CHANGES_REQUESTED}" "${CODEX_VERDICT_FILE}"

out_5b="$(run_router)"

if grep -qF "workspace-write" "${CALLS_LOG}"; then
    pass "S5b: cx1-only → codex workspace-write called"
else
    fail "S5b: cx1-only proceed" "codex workspace-write NOT called; calls=$(cat "${CALLS_LOG}") out=${out_5b}"
fi

# ---------------------------------------------------------------------------
# Scenario 6: schema malformed (F10)
# Case 6a: bad verdict enum → no comment, no label flip
# Case 6b: bad fileLine (no colon) → no comment
# ---------------------------------------------------------------------------
echo ""
echo "=== Scenario 6: schema malformed (F10) ==="

# 6a: bad verdict enum
reset_scenario
echo '[{"number":42,"headRefName":"feat/t","headRefOid":"'"${OID_REVIEW}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_REVIEW}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-review-again"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
cp "${VERDICT_MALFORMED}" "${CODEX_VERDICT_FILE}"

out_6a="$(run_router)"

if ! grep -qF "gh pr comment" "${CALLS_LOG}"; then
    pass "S6a: malformed verdict → no comment"
else
    fail "S6a: malformed verdict" "gh pr comment was called"
fi
if ! grep -qF "gh pr edit" "${CALLS_LOG}"; then
    pass "S6a: malformed verdict → no label edit"
else
    fail "S6a: malformed verdict" "gh pr edit was called"
fi
assert_contains "S6a-skip-log" "${out_6a}" "skipping"

# 6b: bad fileLine
reset_scenario
echo '[{"number":42,"headRefName":"feat/t","headRefOid":"'"${OID_REVIEW}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_REVIEW}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-review-again"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
cp "${VERDICT_BAD_FINDING}" "${CODEX_VERDICT_FILE}"

out_6b="$(run_router)"

if ! grep -qF "gh pr comment" "${CALLS_LOG}"; then
    pass "S6b: bad fileLine → no comment"
else
    fail "S6b: bad fileLine" "gh pr comment called; out=${out_6b}"
fi

# ---------------------------------------------------------------------------
# Scenario 7: post-lock label re-confirm (F8)
# After lock, live labels show trigger label GONE → must skip
# The PR appears in the needs-review-again list but live labels no longer
# have pr-status/needs-review-again → router skips after acquiring lock.
# ---------------------------------------------------------------------------
echo ""
echo "=== Scenario 7: post-lock label re-confirm (F8) ==="

reset_scenario
echo '[{"number":70,"headRefName":"feat/s7","headRefOid":"'"${OID_S7}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_S7}"'"}' > "${GH_OID_FILE}"
# trigger label pr-status/needs-review-again is ABSENT from live labels
echo '{"labels":[{"name":"pr-status/needs-fix"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"

out_7="$(run_router)"

if ! grep -qF "codex exec" "${CALLS_LOG}"; then
    pass "S7: trigger label gone → codex not called"
else
    fail "S7: trigger label gone" "codex was called despite trigger label absent"
fi
# Router should log about trigger label gone after lock
assert_contains "S7-skip-log" "${out_7}" "trigger label"

# ---------------------------------------------------------------------------
# Scenario 8: check-path prior-findings extractor (#1762 F1)
# The check phase must extract prior-round findings from the latest pm:pr-review
# comment (in the actual claude six-dimension format: **Finding 详表** list +
# <details> lossless table, finding ids "**F<n>**") and feed them to codex.
# Case 8a: finding-bearing body → check proceeds, codex called read-only, and
#          the extracted findings (file:line) reach the codex prompt.
# Case 8b: finding-less body → fail-closed skip, codex NOT called.
#
# Live labels = ONLY pr-status/needs-check-fix so handle_review("review") and
# handle_fix skip their trigger re-confirm; handle_review("check") proceeds.
# The gh-api stub returns GH_API_BODIES_FILE verbatim (it simulates the
# post-filter body), so this exercises the python extractor directly.
# ---------------------------------------------------------------------------
echo ""
echo "=== Scenario 8: check-path prior-findings extractor (#1762 F1) ==="

OID_S8="cccc3333dddd4444eeee5555ffff6666aaaa7777"

# 8a: realistic finding-bearing pm:pr-review body → extractor succeeds
reset_scenario
echo '[{"number":80,"headRefName":"feat/s8","headRefOid":"'"${OID_S8}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_S8}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-check-fix"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
cp "${VERDICT_CHANGES_REQUESTED}" "${CODEX_VERDICT_FILE}"
cat > "${GH_API_BODIES_FILE}" <<'S8ABODY'
<!-- pm:pr-review -->
## pr-review（六维度分级审查）

**Finding 详表**

- **F1** [P1·Cx2·correctness] hack/probe.go:99 → 簇 C1
  concise finding summary

<details><summary>完整详表</summary>

**F1** [P1·Cx2·correctness] `hack/probe.go:99`
- 证据：`prior extractor keyed on a nonexistent title`
- 建议：anchor on the bold finding id

</details>
<!-- gocell-pr-meta:v1 eyJraW5kIjoicHItcmV2aWV3In0= -->
S8ABODY

# S8a asserts on the codex call recorded in CALLS_LOG (a file side-effect),
# not on router stdout — discard stdout to keep shellcheck happy.
run_router >/dev/null
calls_8a="$(cat "${CALLS_LOG}")"

assert_contains "S8a-codex-readonly-called" "${calls_8a}" "read-only"
assert_contains "S8a-findings-in-prompt"   "${calls_8a}" "Prior-round findings to verify"
assert_contains "S8a-finding-reached-prompt" "${calls_8a}" "hack/probe.go:99"

# 8b: finding-less body → fail-closed skip (no codex)
reset_scenario
echo '[{"number":80,"headRefName":"feat/s8","headRefOid":"'"${OID_S8}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]' \
    > "${GH_LIST_FILE}"
echo '{"headRefOid":"'"${OID_S8}"'"}' > "${GH_OID_FILE}"
echo '{"labels":[{"name":"pr-status/needs-check-fix"}]}' > "${GH_LABELS_FILE}"
echo '{"mergeable":"MERGEABLE"}' > "${GH_MERGE_FILE}"
cat > "${GH_API_BODIES_FILE}" <<'S8BBODY'
<!-- pm:pr-review -->
## pr-review（六维度分级审查）

**总体结论**：通过

_无 findings_
S8BBODY

out_8b="$(run_router)"

assert_not_contains "S8b-no-codex" "$(cat "${CALLS_LOG}")" "codex exec"
assert_contains "S8b-skip-log" "${out_8b}" "no prior pm:pr-review findings"

# ---------------------------------------------------------------------------
# Scenario 9: emit-block auto-derive path (skill path — no ref/round overrides)
# The router always passes --head-sha/--base-ref/--head-ref/--round-base, so its
# tests never exercise cmd_emit_block's auto-derive glue (gh pr view -> refs,
# cmd_round -> roundBase, env -> session/worktree). The ship/fix/pr-review skills
# rely on exactly that glue, so cover it here via the existing gh stub.
# ---------------------------------------------------------------------------
echo ""
echo "=== Scenario 9: emit-block auto-derive (skill path) ==="
reset_scenario
# gh pr view --json baseRefName,headRefName,headRefOid -> full ref object
echo '{"baseRefName":"develop","headRefName":"feat/auto","headRefOid":"'"${OID_REVIEW}"'"}' > "${GH_OID_FILE}"
# no prior pm:* blocks -> cmd_round returns 0
: > "${GH_API_BODIES_FILE}"
S9_BLOCK="$(env PATH="${STUB_BIN}:${PATH}" bash "${PR_META}" emit-block \
    --kind=ship --pr=42 \
    --findings='{"total":1,"fixed":1,"unresolved":0,"blocking":0,"byP":{"p0":0,"p1":0,"p2":1,"p3":0},"byCx":{"cx1":1,"cx2":0,"cx3":0,"cx4":0}}' \
    2>/dev/null)"
S9_JSON="$(printf '%s\n' "${S9_BLOCK}" | bash "${PR_META}" decode 2>/dev/null)"
if [[ -n "${S9_JSON}" ]] \
   && [[ "$(echo "${S9_JSON}" | jq -r '.kind')"          == "ship" ]] \
   && [[ "$(echo "${S9_JSON}" | jq -r '.baseRef')"       == "develop" ]] \
   && [[ "$(echo "${S9_JSON}" | jq -r '.headRef')"       == "feat/auto" ]] \
   && [[ "$(echo "${S9_JSON}" | jq -r '.headSha')"       == "${OID_REVIEW}" ]] \
   && [[ "$(echo "${S9_JSON}" | jq -r '.cycle.round')"   == "0" ]]; then
    pass "S9: emit-block auto-derive (refs from gh pr view, round from cmd_round)"
else
    fail "S9: emit-block auto-derive" "block=${S9_JSON}"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=== Selftest summary ==="
echo "PASS: ${PASS_COUNT}"
echo "FAIL: ${FAIL_COUNT}"

EXPECTED_CHECKS=29
if [[ "${CHECK_COUNT}" -ne "${EXPECTED_CHECKS}" ]]; then
    echo "FAIL [check-count]: expected ${EXPECTED_CHECKS} checks, ran ${CHECK_COUNT}"
    FAIL_COUNT=$(( FAIL_COUNT + 1 ))
fi

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
    echo "router-selftest: FAILED (${FAIL_COUNT} failures)"
    exit 1
fi
echo "router-selftest: OK (${PASS_COUNT} checks)"
