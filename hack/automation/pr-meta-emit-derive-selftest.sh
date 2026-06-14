#!/usr/bin/env bash
# pr-meta-emit-derive-selftest.sh — offline selftest for cmd_emit_block's
# auto-derive glue: refs from `gh pr view` + roundBase from `cmd_round`/`gh api`
# + session/worktree from env, exercised WITHOUT --head-sha/--base-ref/--head-ref/
# --round-base overrides.
#
# Why this exists: `pr-meta.sh selftest` only covers the offline Python engine
# (decode / derive_facts). The shell auto-derive path (the glue the ship/fix/
# pr-review skills actually rely on when they call emit-block with only --kind/
# --pr/--findings) was covered by the now-removed codex-pr-router Scenario 9; this
# selftest migrates that coverage via a minimal gh stub so the skill path stays
# guarded after the router retirement (PR #2129 / #2124).
#
# Usage:  bash hack/automation/pr-meta-emit-derive-selftest.sh
# Exit:   0 = all checks PASS; non-zero = at least one FAIL (or anti-vacuity)
#
# ref: hack/automation/codex-pr-router/router-selftest.sh Scenario 9 (deleted) — covered path.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
PR_META="${SCRIPT_DIR}/pr-meta.sh"

WORKDIR="$(mktemp -d)"
STUB_BIN="${WORKDIR}/bin"
GH_VIEW_FILE="${WORKDIR}/gh_view.json"
mkdir -p "${STUB_BIN}"
cleanup() { rm -rf "${WORKDIR}"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Harness (pass/fail + EXPECTED_CHECKS anti-vacuity anchor — same false-green
# defence the sibling automation selftests use).
# ---------------------------------------------------------------------------
PASS_COUNT=0
FAIL_COUNT=0
CHECK_COUNT=0
EXPECTED_CHECKS=5
pass() { echo "PASS [$1]"; PASS_COUNT=$(( PASS_COUNT + 1 )); CHECK_COUNT=$(( CHECK_COUNT + 1 )); }
fail() { echo "FAIL [$1]: $2"; FAIL_COUNT=$(( FAIL_COUNT + 1 )); CHECK_COUNT=$(( CHECK_COUNT + 1 )); }
assert_eq() {
    local name="$1" want="$2" got="$3"
    if [[ "${got}" == "${want}" ]]; then pass "${name}"; else fail "${name}" "want '${want}', got '${got}'"; fi
}

# ---------------------------------------------------------------------------
# gh stub: emit-block's auto-derive path makes exactly two gh calls —
#   `gh pr view <pr> --repo <slug> --json baseRefName,headRefName,headRefOid`  (refs)
#   `gh api .../comments`  (via cmd_round/fetch_trusted_bodies; empty => round 0)
# ---------------------------------------------------------------------------
cat > "${STUB_BIN}/gh" <<GHSTUB
#!/usr/bin/env bash
GH_VIEW_FILE="${GH_VIEW_FILE}"
case "\${1:-}" in
    pr)
        case "\${2:-}" in
            view) cat "\${GH_VIEW_FILE}" ;;
            *) exit 0 ;;
        esac
        ;;
    api) : ;;   # no trusted comment bodies => cmd_round derives 0
    *) exit 0 ;;
esac
GHSTUB
chmod +x "${STUB_BIN}/gh"

OID="aaaa1111bbbb2222cccc3333dddd4444eeee5555"
printf '{"baseRefName":"develop","headRefName":"feat/auto","headRefOid":"%s"}\n' "${OID}" > "${GH_VIEW_FILE}"

# ---------------------------------------------------------------------------
# Run emit-block WITHOUT ref/round overrides — the skill path — then decode.
# ---------------------------------------------------------------------------
BLOCK="$(env PATH="${STUB_BIN}:${PATH}" CLAUDE_CODE_SESSION_ID="" \
    bash "${PR_META}" emit-block --kind=ship --pr=42 \
    --findings='{"total":1,"fixed":1,"unresolved":0,"blocking":0,"byP":{"p0":0,"p1":0,"p2":1,"p3":0},"byCx":{"cx1":1,"cx2":0,"cx3":0,"cx4":0}}' \
    2>/dev/null)"
JSON="$(printf '%s\n' "${BLOCK}" | bash "${PR_META}" decode 2>/dev/null)"

if [[ -z "${JSON}" ]]; then
    fail "emit-derive/decode" "emit-block produced no decodable block"
else
    assert_eq "emit-derive/kind"      "ship"        "$(echo "${JSON}" | jq -r '.kind')"
    assert_eq "emit-derive/baseRef"   "develop"     "$(echo "${JSON}" | jq -r '.baseRef')"       # <- gh pr view
    assert_eq "emit-derive/headRef"   "feat/auto"   "$(echo "${JSON}" | jq -r '.headRef')"       # <- gh pr view
    assert_eq "emit-derive/headSha"   "${OID}"      "$(echo "${JSON}" | jq -r '.headSha')"       # <- gh pr view
    assert_eq "emit-derive/round"     "0"           "$(echo "${JSON}" | jq -r '.cycle.round')"   # <- cmd_round
fi

# ---------------------------------------------------------------------------
# Summary + anti-vacuity: a silently-skipped assertion (e.g. early decode fail
# returning before the 5 field checks) trips the count mismatch and fails red.
# ---------------------------------------------------------------------------
echo ""
echo "pr-meta-emit-derive-selftest: ${PASS_COUNT} passed, ${FAIL_COUNT} failed"
if [[ "${CHECK_COUNT}" -ne "${EXPECTED_CHECKS}" ]]; then
    echo "FAIL [check-count]: expected ${EXPECTED_CHECKS} checks, ran ${CHECK_COUNT} (anti-vacuity)" >&2
    exit 1
fi
[[ "${FAIL_COUNT}" -eq 0 ]]
