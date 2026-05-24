#!/usr/bin/env bash
# Smoke test for daily-planner lib/apply-gate.sh and lib/apply-write.sh.
#
# Mirrors .github/scripts/auto-label-priority-test.sh conventions:
#   - PATH-stub gh
#   - run_case helper with pass/fail counting
#   - exit 1 on any failure
#
# Part A (offline deterministic): gate + write cases, no live gh calls.
# Part B (live read-only, SMOKE_LIVE=1 opt-in): stage 0-2 + 1.5 dry-run, zero writes.
#
# Usage:
#   bash .claude/skills/daily-planner/test/smoke.sh         # Part A only
#   SMOKE_LIVE=1 bash .claude/skills/daily-planner/test/smoke.sh  # Part A + Part B

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
skill_dir="$(cd "${script_dir}/.." && pwd -P)"
fixtures_dir="${script_dir}/fixtures"

gate_script="${skill_dir}/lib/apply-gate.sh"
write_script="${skill_dir}/lib/apply-write.sh"

[[ -x "$gate_script" ]] || { echo "MISSING: $gate_script (expected RED before lib/ exists)" >&2; exit 1; }
[[ -x "$write_script" ]] || { echo "MISSING: $write_script (expected RED before lib/ exists)" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Stub directory for gh CLI (Part A)
# ---------------------------------------------------------------------------
stub_dir=$(mktemp -d)
trap 'rm -rf "$stub_dir"' EXIT

# gh stub: records item-edit calls to GH_STUB_LOG; returns exit 1 for
# item id matching GH_STUB_FAIL_ITEM (write-side failure simulation).
cat > "${stub_dir}/gh" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  project)
    case "${2:-}" in
      item-edit)
        # record all args after "item-edit"
        shift 2
        echo "item-edit $*" >> "${GH_STUB_LOG:?GH_STUB_LOG required}"
        # simulate failure for a specific item
        args=("$@")
        i=0
        while [[ $i -lt ${#args[@]} ]]; do
          if [[ "${args[$i]}" == "--id" ]]; then
            next=$((i+1))
            if [[ "${args[$next]:-}" == "${GH_STUB_FAIL_ITEM:-__none__}" ]]; then
              exit 1
            fi
          fi
          i=$((i+1))
        done
        ;;
      *) echo "stub: unexpected gh project $*" >&2; exit 1 ;;
    esac
    ;;
  *) echo "stub: unexpected gh $*" >&2; exit 1 ;;
esac
STUB
chmod +x "${stub_dir}/gh"

pass=0
fail=0

# ---------------------------------------------------------------------------
# Gate test helpers
# ---------------------------------------------------------------------------
make_gate_workdir() {
  local plan_fixture="$1"
  local wd
  wd=$(mktemp -d)
  cp "${fixtures_dir}/issues.json"      "$wd/issues.json"
  cp "${fixtures_dir}/items.json"       "$wd/items.json"
  cp "${fixtures_dir}/iter-config.json" "$wd/iter-config.json"
  cp "${fixtures_dir}/${plan_fixture}"  "$wd/plan.json"
  echo "$wd"
}

assert_gate_pass() {
  local name="$1"; shift
  if env "$@" bash "$gate_script" >/dev/null 2>&1; then
    echo "PASS [$name]"
    pass=$((pass+1))
  else
    echo "FAIL [$name] expected exit 0 but got non-zero"
    fail=$((fail+1))
  fi
}

assert_gate_fail() {
  local name="$1"
  local grep_pattern="$2"
  shift 2
  local stderr_out
  stderr_out=$(mktemp)
  local rc=0
  env "$@" bash "$gate_script" >/dev/null 2>"$stderr_out" || rc=$?
  local actual_stderr
  actual_stderr=$(cat "$stderr_out")
  rm -f "$stderr_out"

  if [[ $rc -eq 0 ]]; then
    echo "FAIL [$name] expected exit 1 but got 0"
    fail=$((fail+1))
    return
  fi
  if [[ "$actual_stderr" != *"$grep_pattern"* ]]; then
    echo "FAIL [$name] rc=$rc but pattern not found: '$grep_pattern'"
    echo "  stderr was:"
    printf '%s\n' "$actual_stderr" | sed 's/^/    /'
    fail=$((fail+1))
    return
  fi
  echo "PASS [$name]"
  pass=$((pass+1))
}

# ---------------------------------------------------------------------------
# Part A: Gate cases
# ---------------------------------------------------------------------------
echo "=== Part A: apply-gate.sh ==="

# A1: clean plan -> exit 0
wd=$(make_gate_workdir "plan-clean.json")
assert_gate_pass "A1: clean plan -> exit 0" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A2: rogue item_id -> "not in allowed set" + rc==1
wd=$(make_gate_workdir "plan-rogue-item.json")
assert_gate_fail "A2: rogue item_id -> not in allowed set" \
  "not in allowed set" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A3: wrong target -> "target_iteration_id !=" + rc==1
wd=$(make_gate_workdir "plan-wrong-target.json")
assert_gate_fail "A3: wrong target -> target_iteration_id !=" \
  "target_iteration_id !=" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A4: unknown current_iteration_id -> "current_iteration_id unknown" + rc==1
wd=$(make_gate_workdir "plan-unknown-current.json")
assert_gate_fail "A4: unknown current -> current_iteration_id unknown" \
  "current_iteration_id unknown" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A5: parallel_group not int -> "parallel_group must be int" + rc==1
wd=$(make_gate_workdir "plan-bad-parallel-group.json")
assert_gate_fail "A5: bad parallel_group -> parallel_group must be int" \
  "parallel_group must be int" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A6: WAVE_FIELD_ID="" -> "Wave field missing" + rc==1
wd=$(make_gate_workdir "plan-clean.json")
assert_gate_fail "A6: WAVE_FIELD_ID empty -> Wave field missing" \
  "Wave field missing" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A7: unknown wave_option_id for action==set -> "wave_option_id unknown" + rc==1
wd=$(make_gate_workdir "plan-unknown-wave-option.json")
assert_gate_fail "A7: unknown wave_option_id -> wave_option_id unknown" \
  "wave_option_id unknown" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A8: action==skip with empty wave_option_id -> gate pass (skip does not validate wave_option_id)
wd=$(make_gate_workdir "plan-skip-action.json")
assert_gate_pass "A8: skip action with empty wave_option_id -> exit 0" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# ---------------------------------------------------------------------------
# Part A: Write cases
# ---------------------------------------------------------------------------
echo ""
echo "=== Part A: apply-write.sh ==="

run_write() {
  local wd="$1" fail_item="$2" gh_log="$3" stderr_out="$4"
  env \
    "WORKDIR=$wd" \
    "TODAY_ITERATION_ID=ITER_TODAY" \
    "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
    "PROJECT_NODE_ID=PNI_TEST" \
    "ITERATION_FIELD_ID=IFIELD_123" \
    "WAVE_FIELD_ID=WFIELD_123" \
    "DATE=2026-05-24" \
    "IS_WEEKEND=false" \
    "GH_STUB_LOG=$gh_log" \
    "GH_STUB_FAIL_ITEM=${fail_item}" \
    "PATH=${stub_dir}:${PATH}" \
    bash "$write_script" >/dev/null 2>"$stderr_out" || true
}

validate_audit_fields() {
  local name="$1" audit_file="$2"
  local ok=true
  if [[ ! -f "$audit_file" ]]; then
    echo "FAIL [$name] audit.ndjson not found"
    fail=$((fail+1))
    return 1
  fi
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    for field in ts date mode action item_id prev_iteration_id target_iteration_id result; do
      if ! echo "$line" | python3 -c \
           "import sys,json; d=json.loads(sys.stdin.read()); assert '$field' in d" 2>/dev/null; then
        echo "FAIL [$name] audit line missing field '$field': $line"
        ok=false
      fi
    done
  done < "$audit_file"
  $ok
}

# W1: clean plan -> audit fields OK + iteration + wave calls made
wd_w1=$(mktemp -d)
cp "${fixtures_dir}/issues.json" "${fixtures_dir}/items.json" \
   "${fixtures_dir}/iter-config.json" "$wd_w1/"
cp "${fixtures_dir}/plan-clean.json" "$wd_w1/plan.json"
gh_log_w1=$(mktemp)
stderr_w1=$(mktemp)
run_write "$wd_w1" "" "$gh_log_w1" "$stderr_w1"
gh_calls_w1=$(cat "$gh_log_w1"); rm -f "$gh_log_w1" "$stderr_w1"
w1_ok=true
if ! validate_audit_fields "W1" "$wd_w1/audit.ndjson"; then w1_ok=false; fi
if [[ "$gh_calls_w1" != *"--field-id IFIELD_123 --iteration-id ITER_TODAY"* ]]; then
  echo "FAIL [W1] gh missing iteration call; got: $gh_calls_w1"
  w1_ok=false
fi
if [[ "$gh_calls_w1" != *"--field-id WFIELD_123 --single-select-option-id OPT_WAVE1"* ]]; then
  echo "FAIL [W1] gh missing wave call; got: $gh_calls_w1"
  w1_ok=false
fi
if $w1_ok; then echo "PASS [W1: clean plan writes iteration + wave]"; pass=$((pass+1))
else fail=$((fail+1)); fi
rm -rf "$wd_w1"

# W2: partial failure -> rollback applied item + retry failed item
# PVTI_ITEM_101 (prev=ITER_TODAY) succeeds -> rollback uses --iteration-id ITER_TODAY
# PVTI_ITEM_102 fails -> appears in retry (failed items) section
wd_w2=$(mktemp -d)
cp "${fixtures_dir}/issues.json" "${fixtures_dir}/items.json" \
   "${fixtures_dir}/iter-config.json" "$wd_w2/"
cp "${fixtures_dir}/plan-clean.json" "$wd_w2/plan.json"
gh_log_w2=$(mktemp)
stderr_w2=$(mktemp)
run_write "$wd_w2" "PVTI_ITEM_102" "$gh_log_w2" "$stderr_w2"
stderr_content_w2=$(cat "$stderr_w2"); rm -f "$gh_log_w2" "$stderr_w2"
w2_ok=true
if [[ "$stderr_content_w2" != *"--iteration-id ITER_TODAY"* ]]; then
  echo "FAIL [W2] rollback missing --iteration-id ITER_TODAY"
  echo "  stderr: $stderr_content_w2"
  w2_ok=false
fi
if [[ "$stderr_content_w2" != *"failed items"* ]]; then
  echo "FAIL [W2] missing 'failed items' section"
  w2_ok=false
fi
if $w2_ok; then echo "PASS [W2: partial fail -> rollback applied + retry failed]"; pass=$((pass+1))
else fail=$((fail+1)); fi
rm -rf "$wd_w2"

# W3: empty prev_iteration_id -> rollback uses --clear
# Two items: first has null current (succeeds), second fails -> rollback of first shows --clear
wd_w3=$(mktemp -d)
cp "${fixtures_dir}/issues.json" "${fixtures_dir}/items.json" \
   "${fixtures_dir}/iter-config.json" "$wd_w3/"
cat > "$wd_w3/plan.json" <<'PLANEOF'
[
  {
    "item_id": "PVTI_ITEM_101",
    "issue_number": 101,
    "issue_title": "Issue 101",
    "wave": 1,
    "current_iteration_id": null,
    "target_iteration_id": "ITER_TODAY",
    "action": "set",
    "carry_over": false,
    "score": 9.5,
    "parallel_group": 1,
    "wave_option_id": "OPT_WAVE1"
  },
  {
    "item_id": "PVTI_ITEM_102",
    "issue_number": 102,
    "issue_title": "Issue 102",
    "wave": 1,
    "current_iteration_id": "ITER_YESTERDAY",
    "target_iteration_id": "ITER_TODAY",
    "action": "set",
    "carry_over": true,
    "score": 8.0,
    "parallel_group": 2,
    "wave_option_id": "OPT_WAVE1"
  }
]
PLANEOF
gh_log_w3=$(mktemp)
stderr_w3=$(mktemp)
run_write "$wd_w3" "PVTI_ITEM_102" "$gh_log_w3" "$stderr_w3"
stderr_content_w3=$(cat "$stderr_w3"); rm -f "$gh_log_w3" "$stderr_w3"
w3_ok=true
if [[ "$stderr_content_w3" != *"--clear"* ]]; then
  echo "FAIL [W3] missing --clear in rollback for null prev"
  echo "  stderr: $stderr_content_w3"
  w3_ok=false
fi
if [[ "$stderr_content_w3" != *"PVTI_ITEM_102"* ]]; then
  echo "FAIL [W3] PVTI_ITEM_102 not in retry section"
  w3_ok=false
fi
if $w3_ok; then echo "PASS [W3: empty prev -> rollback --clear]"; pass=$((pass+1))
else fail=$((fail+1)); fi
rm -rf "$wd_w3"

# W4: action==skip -> no gh calls + audit result=skipped
wd_w4=$(mktemp -d)
cp "${fixtures_dir}/issues.json" "${fixtures_dir}/items.json" \
   "${fixtures_dir}/iter-config.json" "$wd_w4/"
cp "${fixtures_dir}/plan-skip-action.json" "$wd_w4/plan.json"
gh_log_w4=$(mktemp)
stderr_w4=$(mktemp)
run_write "$wd_w4" "" "$gh_log_w4" "$stderr_w4"
w4_gh=$(cat "$gh_log_w4"); rm -f "$gh_log_w4" "$stderr_w4"
w4_audit=$(cat "$wd_w4/audit.ndjson" 2>/dev/null || true)
w4_ok=true
if [[ -n "$w4_gh" ]]; then
  echo "FAIL [W4] gh was called for skip action: $w4_gh"
  w4_ok=false
fi
if [[ "$w4_audit" != *'"result":"skipped"'* ]]; then
  echo "FAIL [W4] audit result != skipped: $w4_audit"
  w4_ok=false
fi
if $w4_ok; then echo "PASS [W4: skip not written]"; pass=$((pass+1))
else fail=$((fail+1)); fi
rm -rf "$wd_w4"

# ---------------------------------------------------------------------------
# Part B: Live read-only (SMOKE_LIVE=1 opt-in)
# ---------------------------------------------------------------------------
echo ""
if [[ "${SMOKE_LIVE:-}" != "1" ]]; then
  echo "=== Part B: SKIP (set SMOKE_LIVE=1 to run live read-only stage 0-2 + 1.5) ==="
else
  echo "=== Part B: Live read-only (dry-run, zero mutation) ==="
  echo "INFO: Running SKILL.md stage 0-2 + 1.5 dry-run against real Project v2 #3 (ghbvf)..." >&2

  # Find repo root (4 levels up from test/)
  repo_root="$(cd "${skill_dir}/../../../.." && pwd -P)"
  echo "INFO: repo root: $repo_root" >&2

  live_wd=$(mktemp -d -t daily-planner-smoke.XXXXXX)
  chmod 700 "$live_wd"
  # shellcheck disable=SC2064
  trap "rm -rf '${live_wd}'" EXIT

  live_rc=0
  (
    cd "$repo_root"
    set -euo pipefail

    # Stage 0: token scope + project ID + iteration field ID
    if ! AUTH_STATUS=$(gh auth status 2>&1); then
      echo "ERROR: gh auth status failed" >&2; exit 1
    fi
    if ! echo "$AUTH_STATUS" | grep -qiE "scopes:.*\bproject\b"; then
      echo "ERROR: token missing 'project' scope" >&2; exit 1
    fi
    PROJECT_NODE_ID=$(gh project view 3 --owner ghbvf --format json | jq -r '.id')
    ITERATION_FIELD_ID=$(gh project field-list 3 --owner ghbvf --format json \
      | jq -r '.fields[] | select(.name=="Iteration").id')
    [[ -z "$PROJECT_NODE_ID" || -z "$ITERATION_FIELD_ID" ]] && {
      echo "ERROR: failed to resolve PROJECT_NODE_ID or ITERATION_FIELD_ID" >&2; exit 1
    }
    echo "INFO: PROJECT_NODE_ID=$PROJECT_NODE_ID ITERATION_FIELD_ID=$ITERATION_FIELD_ID" >&2

    # C3c: Wave field ID
    WAVE_FIELD_ID=$(gh project field-list 3 --owner ghbvf --format json \
      | jq -r '.fields[] | select(.name=="Wave").id // ""')
    echo "INFO: WAVE_FIELD_ID=${WAVE_FIELD_ID:-<not found>}" >&2

    # Stage 1: issues + items + iter-config
    DP_DATE="${DATE:-$(date +%Y-%m-%d)}"
    echo "[]" > "$live_wd/issues.json"
    gh issue list --repo ghbvf/gocell --label backlog --label pri-p0 \
      --state open --json number,title,labels,createdAt,body,url --limit 50 \
      > "$live_wd/issues-p0.json" 2>/dev/null || echo "[]" > "$live_wd/issues-p0.json"
    jq -s '(.[0] + .[1]) | unique_by(.number)' \
      "$live_wd/issues.json" "$live_wd/issues-p0.json" \
      > "$live_wd/issues.tmp" && mv "$live_wd/issues.tmp" "$live_wd/issues.json"
    echo "INFO: input issues: $(jq 'length' "$live_wd/issues.json")" >&2

    # shellcheck disable=SC2016
    gh api graphql --paginate -f query='
    query($endCursor: String) {
      user(login:"ghbvf"){ projectV2(number:3){
        items(first:100, after:$endCursor) {
          pageInfo { hasNextPage endCursor }
          nodes {
            id
            content { ... on Issue { number state title } }
            iter: fieldValueByName(name:"Iteration") {
              ... on ProjectV2ItemFieldIterationValue { iterationId title startDate }
            }
            status: fieldValueByName(name:"Status") {
              ... on ProjectV2ItemFieldSingleSelectValue { name }
            }
          }
        }
      }}
    }' | jq -s '[.[].data.user.projectV2.items.nodes[]]' > "$live_wd/items.json"
    echo "INFO: items loaded: $(jq 'length' "$live_wd/items.json")" >&2

    gh api graphql -f query='query {
      user(login:"ghbvf"){ projectV2(number:3){
        field(name:"Iteration"){ ... on ProjectV2IterationField {
          configuration { duration startDay iterations { id title startDate duration } }
        }}
      }}
    }' > "$live_wd/iter-config.json"
    echo "INFO: iter-config loaded (date=$DP_DATE)" >&2

    # Stage 1.5: deps.json (blocked-by DAG via native GraphQL)
    echo "INFO: fetching blocked-by DAG for input issues..." >&2
    echo "{}" > "$live_wd/deps.json"
    issue_count=$(jq 'length' "$live_wd/issues.json")
    if [[ "$issue_count" -gt 0 ]]; then
      deps_result="{}"
      while IFS= read -r num; do
        # shellcheck disable=SC2016
        blocked=$(gh api graphql \
          -f query='query($num: Int!) {
            repository(owner:"ghbvf", name:"gocell") {
              issue(number: $num) {
                blockedBy(first: 20) { nodes { number } }
              }
            }
          }' -F num="$num" 2>/dev/null \
          | jq '[.data.repository.issue.blockedBy.nodes[].number]' || echo "[]")
        if [[ "$(echo "$blocked" | jq 'length')" -gt 0 ]]; then
          deps_result=$(echo "$deps_result" | jq --arg n "$num" --argjson b "$blocked" \
            '. + {($n): {"blocked_by": $b}}')
        fi
      done < <(jq -r '.[].number' "$live_wd/issues.json")
      echo "$deps_result" > "$live_wd/deps.json"
    fi
    echo "INFO: deps.json = $(cat "$live_wd/deps.json")" >&2
    echo "INFO: Part B stage 0-2 + 1.5 dry-run complete (zero mutations)" >&2
  ) || live_rc=$?

  if [[ $live_rc -eq 0 ]]; then
    echo "PASS [Part B: live dry-run stage 0-2 + 1.5]"
    pass=$((pass+1))
  else
    echo "FAIL [Part B: live dry-run stage 0-2 + 1.5] exit $live_rc"
    fail=$((fail+1))
  fi

  rm -rf "$live_wd"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "Summary: ${pass} pass, ${fail} fail"
if [[ $fail -gt 0 ]]; then
  exit 1
fi
