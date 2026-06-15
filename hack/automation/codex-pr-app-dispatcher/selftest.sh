#!/usr/bin/env bash
# selftest.sh — offline regression tests for codex-pr-app-dispatcher/router.py.
#
# Stubs gh and codex. No network, no real app-server, no repository mutation.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROUTER="${SCRIPT_DIR}/router.py"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"

PASS_COUNT=0
FAIL_COUNT=0
CHECK_COUNT=0
# EXPECTED_CHECKS is the anti-vacuity anchor: the exact number of assert_* calls
# across all scenarios below. If a scenario returns early or is silently skipped
# (e.g. a handshake abort under load) CHECK_COUNT drifts from this and the run
# hard-fails instead of false-greening on a 0-fail partial run. Update this when
# adding/removing a check. (Same pattern as hack/automation/bucket-coverage-selftest.sh.)
EXPECTED_CHECKS=18

pass() { echo "PASS [$1]"; PASS_COUNT=$((PASS_COUNT + 1)); CHECK_COUNT=$((CHECK_COUNT + 1)); }
fail() { echo "FAIL [$1]: $2"; FAIL_COUNT=$((FAIL_COUNT + 1)); CHECK_COUNT=$((CHECK_COUNT + 1)); }

assert_contains() {
    local name="$1" file="$2" needle="$3"
    if grep -qF "${needle}" "${file}"; then
        pass "${name}"
    else
        fail "${name}" "expected ${file} to contain ${needle}"
    fi
}

assert_not_contains() {
    local name="$1" file="$2" needle="$3"
    if grep -qF "${needle}" "${file}"; then
        fail "${name}" "expected ${file} not to contain ${needle}"
    else
        pass "${name}"
    fi
}

assert_line_count() {
    local name="$1" file="$2" pattern="$3" want="$4"
    local got
    got="$(grep -cF "${pattern}" "${file}" || true)"
    if [[ "${got}" == "${want}" ]]; then
        pass "${name}"
    else
        fail "${name}" "want ${want} matches for ${pattern}, got ${got}"
    fi
}

WORKDIR="$(mktemp -d)"
STUB_BIN="${WORKDIR}/bin"
ROUTER_HOME="${WORKDIR}/router-home"
CALLS_LOG="${WORKDIR}/calls.log"
REVIEW_LIST="${WORKDIR}/review.json"
CHECK_LIST="${WORKDIR}/check.json"
OID_MAP="${WORKDIR}/oids.json"
CODEX_FAIL_FLAG="${WORKDIR}/codex-fail"

cleanup() { rm -rf "${WORKDIR}"; }
trap cleanup EXIT

mkdir -p "${STUB_BIN}" "${ROUTER_HOME}"
: > "${CALLS_LOG}"
echo '[]' > "${REVIEW_LIST}"
echo '[]' > "${CHECK_LIST}"
echo '{}' > "${OID_MAP}"

cat > "${STUB_BIN}/gh" <<'GHSTUB'
#!/usr/bin/env python3
import json
import os
import sys

args = sys.argv[1:]
review_file = os.environ["GH_REVIEW_LIST_FILE"]
check_file = os.environ["GH_CHECK_LIST_FILE"]
oid_file = os.environ["GH_OID_MAP_FILE"]

if args[:2] == ["pr", "list"]:
    label = args[args.index("--label") + 1]
    src = review_file if label == "pr-status/needs-review-again" else check_file
    with open(src, encoding="utf-8") as fh:
        sys.stdout.write(fh.read())
    sys.exit(0)

if args[:2] == ["pr", "view"]:
    pr = args[2]
    with open(oid_file, encoding="utf-8") as fh:
        oid_map = json.load(fh)
    if "--jq" in args:
        print(oid_map.get(pr, ""))
        sys.exit(0)
    labels = []
    is_draft = False
    for src, label in (
        (review_file, "pr-status/needs-review-again"),
        (check_file, "pr-status/needs-check-fix"),
    ):
        with open(src, encoding="utf-8") as fh:
            for item in json.load(fh):
                if str(item.get("number")) == pr:
                    labels.append({"name": label})
                    is_draft = bool(item.get("isDraft", False))
    print(json.dumps({
        "headRefOid": oid_map.get(pr, ""),
        "isDraft": is_draft,
        "labels": labels,
    }))
    sys.exit(0)

print("unexpected gh args: " + " ".join(args), file=sys.stderr)
sys.exit(2)
GHSTUB
chmod +x "${STUB_BIN}/gh"

cat > "${STUB_BIN}/codex" <<'CODEXSTUB'
#!/usr/bin/env python3
import json
import os
import sys

args = sys.argv[1:]
calls_log = os.environ["CALLS_LOG"]
fail_flag = os.environ["CODEX_FAIL_FLAG"]

def log(line):
    with open(calls_log, "a", encoding="utf-8") as fh:
        fh.write(line + "\n")

if args == ["app-server", "--stdio"]:
    log("app-server start")
    for line in sys.stdin:
        msg = json.loads(line)
        method = msg.get("method")
        if method == "thread/start" and os.path.exists(fail_flag):
            print(json.dumps({"id": msg["id"], "error": {"code": 1, "message": "forced failure"}}), flush=True)
            continue
        if method == "turn/start":
            text = ""
            for item in msg.get("params", {}).get("input", []):
                if item.get("type") == "text":
                    text = item.get("text", "")
            log("turn/start " + text)
            if "id" not in msg:
                continue
        if method == "thread/start":
            print(json.dumps({"id": msg["id"], "result": {"thread": {"id": "thread-stub"}}}), flush=True)
        elif method == "turn/start":
            print(json.dumps({"id": msg["id"], "result": {"turn": {"id": "turn-stub"}}}), flush=True)
        elif method == "initialize":
            print(json.dumps({"id": msg["id"], "result": {"codexHome": "/tmp/codex", "platformFamily": "unix", "platformOs": "macos", "userAgent": "stub"}}), flush=True)
        else:
            print(json.dumps({"id": msg["id"], "result": {}}), flush=True)
    sys.exit(0)

print("unexpected codex args: " + " ".join(args), file=sys.stderr)
sys.exit(2)
CODEXSTUB
chmod +x "${STUB_BIN}/codex"

run_router() {
    # APP_SERVER_REQUEST_TIMEOUT is generous (30s) so a stub JSON-RPC handshake that
    # is slow under heavy parallel CI load does not abort Scenario 1 (the ~8% flake
    # at 5s, #2130). The stub always responds in milliseconds on the happy path, so
    # this never adds wall-clock; no scenario waits for the timeout to fire (S4 returns
    # a forced error; S5 skips dispatch). It only raises the previously-flaky ceiling.
    env \
        PATH="${STUB_BIN}:${PATH}" \
        GOCELL_APP_ROUTER_HOME="${ROUTER_HOME}" \
        GOCELL_APP_ROUTER_REPO_ROOT="${REPO_ROOT}" \
        GOCELL_APP_ROUTER_REPO="ghbvf/gocell" \
        GOCELL_APP_ROUTER_AUTHORS="alice bot" \
        GOCELL_APP_ROUTER_PR_COOLDOWN_SECONDS="${ROUTER_COOLDOWN:-1800}" \
        GOCELL_APP_ROUTER_GH_TIMEOUT=5 \
        GOCELL_APP_ROUTER_APP_SERVER_REQUEST_TIMEOUT=30 \
        CODEX_BIN="${STUB_BIN}/codex" \
        GH_BIN="${STUB_BIN}/gh" \
        GH_REVIEW_LIST_FILE="${REVIEW_LIST}" \
        GH_CHECK_LIST_FILE="${CHECK_LIST}" \
        GH_OID_MAP_FILE="${OID_MAP}" \
        CALLS_LOG="${CALLS_LOG}" \
        CODEX_FAIL_FLAG="${CODEX_FAIL_FLAG}" \
        python3 "${ROUTER}" --once >/tmp/codex-pr-app-dispatcher-test.out 2>&1
}

write_json() {
    local file="$1" json="$2"
    printf '%s\n' "${json}" > "${file}"
}

OID1="aaaa1111bbbb2222cccc3333dddd4444eeee5555"
OID1B="bbbb1111bbbb2222cccc3333dddd4444eeee5555"
OID2="ffff0000aaaa1111bbbb2222cccc3333dddd4444"
OID3="cccc0000aaaa1111bbbb2222cccc3333dddd4444"
OID4="dddd0000aaaa1111bbbb2222cccc3333dddd4444"

echo ""
echo "=== Scenario 1: review/check dispatch and ledger ==="
write_json "${REVIEW_LIST}" '[{"number":1,"headRefName":"feat/a","headRefOid":"'"${OID1}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]'
write_json "${CHECK_LIST}" '[{"number":2,"headRefName":"feat/b","headRefOid":"'"${OID2}"'","author":{"login":"bot"},"isCrossRepository":false,"isDraft":false}]'
write_json "${OID_MAP}" '{"1":"'"${OID1}"'","2":"'"${OID2}"'"}'
run_router
assert_line_count "S1-two-turns" "${CALLS_LOG}" "turn/start" "2"
assert_contains "S1-review-command" "${CALLS_LOG}" "Execute \`pr-review 1\`"
assert_contains "S1-check-command" "${CALLS_LOG}" "Execute \`pr-review 2 --check\`"
assert_contains "S1-label-transition" "${CALLS_LOG}" "actually applying the label transition"
assert_contains "S1-ledger-review" "${ROUTER_HOME}/state/dispatched" "1@${OID1}:review"
assert_contains "S1-ledger-check" "${ROUTER_HOME}/state/dispatched" "2@${OID2}:check"
assert_contains "S1-events-review" "${ROUTER_HOME}/state/dispatch-events.jsonl" "\"key\":\"1@${OID1}:review\""
assert_contains "S1-events-check" "${ROUTER_HOME}/state/dispatch-events.jsonl" "\"key\":\"2@${OID2}:check\""

echo ""
echo "=== Scenario 2: ledger de-dupes repeated poll ==="
run_router
assert_line_count "S2-no-extra-turns" "${CALLS_LOG}" "turn/start" "2"

echo ""
echo "=== Scenario 3: changed head within cooldown skips re-dispatch ==="
write_json "${REVIEW_LIST}" '[{"number":1,"headRefName":"feat/a","headRefOid":"'"${OID1B}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]'
write_json "${CHECK_LIST}" '[]'
write_json "${OID_MAP}" '{"1":"'"${OID1B}"'"}'
run_router
assert_line_count "S3-no-extra-turn-cooldown" "${CALLS_LOG}" "turn/start" "2"
assert_not_contains "S3-no-ledger-new-head" "${ROUTER_HOME}/state/dispatched" "1@${OID1B}:review"

echo ""
echo "=== Scenario 3b: changed head can re-dispatch when cooldown disabled ==="
ROUTER_COOLDOWN=0 run_router
unset ROUTER_COOLDOWN
assert_line_count "S3b-one-extra-turn" "${CALLS_LOG}" "turn/start" "3"
assert_contains "S3b-ledger-new-head" "${ROUTER_HOME}/state/dispatched" "1@${OID1B}:review"

echo ""
echo "=== Scenario 4: app-server failure does not write ledger ==="
touch "${CODEX_FAIL_FLAG}"
write_json "${REVIEW_LIST}" '[{"number":3,"headRefName":"feat/c","headRefOid":"'"${OID3}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]'
write_json "${CHECK_LIST}" '[]'
write_json "${OID_MAP}" '{"3":"'"${OID3}"'"}'
run_router
rm -f "${CODEX_FAIL_FLAG}"
assert_not_contains "S4-no-ledger-on-failure" "${ROUTER_HOME}/state/dispatched" "3@${OID3}:review"

echo ""
echo "=== Scenario 4b: pidless lock is reclaimed ==="
mkdir -p "${ROUTER_HOME}/locks/3.lock"
run_router
assert_line_count "S4b-one-extra-turn" "${CALLS_LOG}" "turn/start" "4"
assert_contains "S4b-ledger-after-lock-reclaim" "${ROUTER_HOME}/state/dispatched" "3@${OID3}:review"

echo ""
echo "=== Scenario 5: conflicting labels skip dispatch ==="
write_json "${REVIEW_LIST}" '[{"number":4,"headRefName":"feat/d","headRefOid":"'"${OID4}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]'
write_json "${CHECK_LIST}" '[{"number":4,"headRefName":"feat/d","headRefOid":"'"${OID4}"'","author":{"login":"alice"},"isCrossRepository":false,"isDraft":false}]'
write_json "${OID_MAP}" '{"4":"'"${OID4}"'"}'
run_router
assert_not_contains "S5-no-ledger-conflict" "${ROUTER_HOME}/state/dispatched" "4@${OID4}:review"
assert_not_contains "S5-no-ledger-conflict-check" "${ROUTER_HOME}/state/dispatched" "4@${OID4}:check"

echo ""
echo "codex-pr-app-dispatcher selftest: ${PASS_COUNT} passed, ${FAIL_COUNT} failed (${CHECK_COUNT}/${EXPECTED_CHECKS} checks ran)"
if [[ "${CHECK_COUNT}" -ne "${EXPECTED_CHECKS}" ]]; then
    echo "ANTI-VACUITY FAIL: ran ${CHECK_COUNT} checks, expected ${EXPECTED_CHECKS} — a scenario was skipped (false-green risk)" >&2
    exit 1
fi
[[ "${FAIL_COUNT}" -eq 0 ]]
