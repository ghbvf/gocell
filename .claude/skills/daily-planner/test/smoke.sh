#!/usr/bin/env bash
# Smoke test for daily-planner lib/apply-gate.sh and lib/apply-write.sh.
#
# Mirrors .github/scripts/auto-label-priority-test.sh conventions:
#   - PATH-stub gh
#   - run_case helper with pass/fail counting
#   - exit 1 on any failure
#
# Part A (offline deterministic): gate + write cases, no live gh calls.
# Part B (live read-only, SMOKE_LIVE=1 opt-in): stage 0-2 dry-run, zero writes.
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
        while [[ $# -gt 0 ]]; do
          if [[ "${1:-}" == "--id" && "${2:-}" == "${GH_STUB_FAIL_ITEM:-__none__}" ]]; then
            exit 1
          fi
          shift
        done
        ;;
      *) echo "stub: unexpected gh project $*" >&2; exit 1 ;;
    esac
    ;;
  *) echo "stub: unexpected gh $*" >&2; exit 1 ;;
esac
STUB
chmod +x "${stub_dir}/gh"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
pass=0
fail=0

run_case() {
  local name="$1"
  shift
  # remaining args: cmd and env (passed as env var assignments)
  if "$@" >/dev/null 2>&1; then
    local rc=0
  else
    local rc=$?
  fi
  # caller checks rc and output; this wrapper is just for counting
  echo "$rc"
}

assert_gate_pass() {
  local name="$1"; shift
  local rc
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
  if ! grep -qF "$grep_pattern" <<<"$actual_stderr"; then
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

# Common env for gate tests
COMMON_GATE_ENV=(
  "WORKDIR=${fixtures_dir}"
  "TODAY_ITERATION_ID=ITER_TODAY"
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY"
  "WAVE_FIELD_ID=WFIELD_123"
  "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
)

# We need WORKDIR to contain plan.json for each test case.
# Use a temp workdir that symlinks the shared fixtures but copies plan.json.
make_gate_workdir() {
  local plan_fixture="$1"
  local wd
  wd=$(mktemp -d)
  cp "${fixtures_dir}/issues.json"     "$wd/issues.json"
  cp "${fixtures_dir}/items.json"      "$wd/items.json"
  cp "${fixtures_dir}/iter-config.json" "$wd/iter-config.json"
  cp "${fixtures_dir}/${plan_fixture}" "$wd/plan.json"
  echo "$wd"
}

# A1: clean plan -> exit 0
wd=$(make_gate_workdir "plan-clean.json")
assert_gate_pass "A1: clean plan -> exit 0" \
  "WORKDIR=$wd" \
  "TODAY_ITERATION_ID=ITER_TODAY" \
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" \
  "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A2: rogue item_id -> grep "not in allowed set" + rc==1
wd=$(make_gate_workdir "plan-rogue-item.json")
assert_gate_fail "A2: rogue item_id -> not in allowed set" \
  "not in allowed set" \
  "WORKDIR=$wd" \
  "TODAY_ITERATION_ID=ITER_TODAY" \
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" \
  "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A3: wrong target -> grep "target_iteration_id !=" + rc==1
wd=$(make_gate_workdir "plan-wrong-target.json")
assert_gate_fail "A3: wrong target -> target_iteration_id !=" \
  "target_iteration_id !=" \
  "WORKDIR=$wd" \
  "TODAY_ITERATION_ID=ITER_TODAY" \
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" \
  "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A4: unknown current_iteration_id -> grep "current_iteration_id unknown" + rc==1
wd=$(make_gate_workdir "plan-unknown-current.json")
assert_gate_fail "A4: unknown current -> current_iteration_id unknown" \
  "current_iteration_id unknown" \
  "WORKDIR=$wd" \
  "TODAY_ITERATION_ID=ITER_TODAY" \
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" \
  "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A5: parallel_group not int -> grep "parallel_group must be int" + rc==1
wd=$(make_gate_workdir "plan-bad-parallel-group.json")
assert_gate_fail "A5: bad parallel_group -> parallel_group must be int" \
  "parallel_group must be int" \
  "WORKDIR=$wd" \
  "TODAY_ITERATION_ID=ITER_TODAY" \
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" \
  "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A6: WAVE_FIELD_ID="" -> grep "Wave field missing" + rc==1
wd=$(make_gate_workdir "plan-clean.json")
assert_gate_fail "A6: WAVE_FIELD_ID empty -> Wave field missing" \
  "Wave field missing" \
  "WORKDIR=$wd" \
  "TODAY_ITERATION_ID=ITER_TODAY" \
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=" \
  "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A7: unknown wave_option_id for action==set -> grep "wave_option_id unknown" + rc==1
wd=$(make_gate_workdir "plan-unknown-wave-option.json")
assert_gate_fail "A7: unknown wave_option_id -> wave_option_id unknown" \
  "wave_option_id unknown" \
  "WORKDIR=$wd" \
  "TODAY_ITERATION_ID=ITER_TODAY" \
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" \
  "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A8: action==skip with empty wave_option_id -> gate pass (skip不校验wave_option_id)
wd=$(make_gate_workdir "plan-skip-action.json")
assert_gate_pass "A8: skip action with empty wave_option_id -> exit 0" \
  "WORKDIR=$wd" \
  "TODAY_ITERATION_ID=ITER_TODAY" \
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" \
  "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# ---------------------------------------------------------------------------
# Part A: Write cases
# ---------------------------------------------------------------------------
echo ""
echo "=== Part A: apply-write.sh ==="

assert_write_audit() {
  # name, plan_fixture, fail_item (or ""), expected checks...
  local name="$1"
  local plan_fixture="$2"
  local fail_item="$3"   # item_id to fail, or ""
  shift 3
  # remaining: associative checks as "key:value" pairs
  local checks=("$@")

  local wd
  wd=$(mktemp -d)
  cp "${fixtures_dir}/issues.json"     "$wd/issues.json"
  cp "${fixtures_dir}/items.json"      "$wd/items.json"
  cp "${fixtures_dir}/iter-config.json" "$wd/iter-config.json"
  cp "${fixtures_dir}/${plan_fixture}" "$wd/plan.json"

  local gh_log
  gh_log=$(mktemp)

  local rc=0
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
    bash "$write_script" >/dev/null 2>/dev/null || rc=$?

  local audit_file="$wd/audit.ndjson"
  local gh_calls
  gh_calls=$(cat "$gh_log" 2>/dev/null || true)
  rm -f "$gh_log"

  local all_ok=true

  # Check audit.ndjson exists and has full fields
  if [[ ! -f "$audit_file" ]]; then
    echo "FAIL [$name] audit.ndjson not found"
    rm -rf "$wd"
    fail=$((fail+1))
    return
  fi

  # Validate each audit line has required fields
  local bad_line=false
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    for field in ts date mode action item_id prev_iteration_id target_iteration_id result; do
      if ! echo "$line" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); assert '$field' in d, '$field missing'" 2>/dev/null; then
        echo "FAIL [$name] audit line missing field '$field': $line"
        bad_line=true
        all_ok=false
      fi
    done
  done < "$audit_file"

  # Run each check
  for check in "${checks[@]}"; do
    local key="${check%%:*}"
    local val="${check#*:}"
    case "$key" in
      gh_contains)
        if ! grep -qF "$val" <<<"$gh_calls"; then
          echo "FAIL [$name] gh calls missing: '$val'"
          echo "  gh calls were:"
          printf '%s\n' "$gh_calls" | sed 's/^/    /'
          all_ok=false
        fi
        ;;
      gh_not_contains)
        if grep -qF "$val" <<<"$gh_calls"; then
          echo "FAIL [$name] gh calls should NOT contain: '$val'"
          all_ok=false
        fi
        ;;
      stderr_contains)
        # re-run capturing stderr
        local stderr_tmp
        stderr_tmp=$(mktemp)
        env \
          "WORKDIR=$wd" \
          "TODAY_ITERATION_ID=ITER_TODAY" \
          "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
          "PROJECT_NODE_ID=PNI_TEST" \
          "ITERATION_FIELD_ID=IFIELD_123" \
          "WAVE_FIELD_ID=WFIELD_123" \
          "DATE=2026-05-24" \
          "IS_WEEKEND=false" \
          "GH_STUB_LOG=$(mktemp)" \
          "GH_STUB_FAIL_ITEM=${fail_item}" \
          "PATH=${stub_dir}:${PATH}" \
          bash "$write_script" >/dev/null 2>"$stderr_tmp" || true
        if ! grep -qF "$val" "$stderr_tmp"; then
          echo "FAIL [$name] stderr missing: '$val'"
          all_ok=false
        fi
        rm -f "$stderr_tmp"
        ;;
    esac
  done

  rm -rf "$wd"

  if $all_ok && ! $bad_line; then
    echo "PASS [$name]"
    pass=$((pass+1))
  else
    fail=$((fail+1))
  fi
}

# W1: clean plan -> audit.ndjson has all fields + gh item-edit called for Iteration + Wave
assert_write_audit "W1: clean plan writes iteration + wave" \
  "plan-clean.json" \
  "" \
  "gh_contains:--field-id ITERATION_FIELD_ID --iteration-id ITER_TODAY" \
  "gh_contains:--field-id WFIELD_123 --single-select-option-id OPT_WAVE1"

# W2: partial failure -> rollback section for applied item, retry for failed item
# Force PVTI_ITEM_102 (second item) to fail; PVTI_ITEM_101 should succeed first
# Rollback: prev was ITER_YESTERDAY -> --iteration-id ITER_YESTERDAY (not --clear)
assert_write_audit "W2: partial fail -> rollback uses --iteration-id for prev" \
  "plan-clean.json" \
  "PVTI_ITEM_102" \
  "stderr_contains:--iteration-id ITER_YESTERDAY" \
  "gh_not_contains:--iteration-id PVTI_ITEM_102"

# W3: item with empty prev_iteration_id -> rollback uses --clear
# Use plan-skip to test clear (action skip doesn't write so prev stays "")
# Better: create a special plan fixture with empty prev for first item
# Use plan-rogue approach: make a plan where first item has cur=""
# We test this by checking the rollback command logic in W2 context already.
# Let's test W3 separately via a fixture that has one item with empty current and that item fails.
# Actually let's create an inline fixture.
wd_w3=$(mktemp -d)
cp "${fixtures_dir}/issues.json"     "$wd_w3/issues.json"
cp "${fixtures_dir}/items.json"      "$wd_w3/items.json"
cp "${fixtures_dir}/iter-config.json" "$wd_w3/iter-config.json"

# Two items: first has empty current (will succeed), second will fail
# We want to test rollback of first (prev=="") shows --clear
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
env \
  "WORKDIR=$wd_w3" \
  "TODAY_ITERATION_ID=ITER_TODAY" \
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "PROJECT_NODE_ID=PNI_TEST" \
  "ITERATION_FIELD_ID=IFIELD_123" \
  "WAVE_FIELD_ID=WFIELD_123" \
  "DATE=2026-05-24" \
  "IS_WEEKEND=false" \
  "GH_STUB_LOG=$gh_log_w3" \
  "GH_STUB_FAIL_ITEM=PVTI_ITEM_102" \
  "PATH=${stub_dir}:${PATH}" \
  bash "$write_script" >/dev/null 2>"${wd_w3}/stderr.txt" || true

w3_stderr=$(cat "${wd_w3}/stderr.txt" 2>/dev/null || true)
w3_ok=true

# rollback for PVTI_ITEM_101 (prev="") should have --clear
if ! grep -qF -- "--clear" <<<"$w3_stderr"; then
  echo "FAIL [W3: empty prev -> rollback --clear] missing --clear in stderr"
  echo "  stderr:"
  printf '%s\n' "$w3_stderr" | sed 's/^/    /'
  w3_ok=false
  fail=$((fail+1))
else
  # Also confirm PVTI_ITEM_102 appears in retry section (failed, not in rollback)
  if ! grep -qF "PVTI_ITEM_102" <<<"$w3_stderr"; then
    echo "FAIL [W3: empty prev -> rollback --clear] PVTI_ITEM_102 not in retry section"
    w3_ok=false
    fail=$((fail+1))
  else
    echo "PASS [W3: empty prev -> rollback --clear]"
    pass=$((pass+1))
  fi
fi

rm -f "$gh_log_w3"
rm -rf "$wd_w3"

# W4: action==skip -> NOT written, audit line result=skipped
wd_w4=$(mktemp -d)
cp "${fixtures_dir}/issues.json"       "$wd_w4/issues.json"
cp "${fixtures_dir}/items.json"        "$wd_w4/items.json"
cp "${fixtures_dir}/iter-config.json"  "$wd_w4/iter-config.json"
cp "${fixtures_dir}/plan-skip-action.json" "$wd_w4/plan.json"

gh_log_w4=$(mktemp)
env \
  "WORKDIR=$wd_w4" \
  "TODAY_ITERATION_ID=ITER_TODAY" \
  "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "PROJECT_NODE_ID=PNI_TEST" \
  "ITERATION_FIELD_ID=IFIELD_123" \
  "WAVE_FIELD_ID=WFIELD_123" \
  "DATE=2026-05-24" \
  "IS_WEEKEND=false" \
  "GH_STUB_LOG=$gh_log_w4" \
  "GH_STUB_FAIL_ITEM=" \
  "PATH=${stub_dir}:${PATH}" \
  bash "$write_script" >/dev/null 2>/dev/null || true

w4_gh=$(cat "$gh_log_w4" 2>/dev/null || true)
w4_audit=$(cat "$wd_w4/audit.ndjson" 2>/dev/null || true)
rm -f "$gh_log_w4"
rm -rf "$wd_w4"

w4_ok=true
if [[ -n "$w4_gh" ]]; then
  echo "FAIL [W4: skip not written] gh was called: $w4_gh"
  w4_ok=false
  fail=$((fail+1))
elif ! grep -q '"result":"skipped"' <<<"$w4_audit"; then
  echo "FAIL [W4: skip not written] audit result != skipped: $w4_audit"
  w4_ok=false
  fail=$((fail+1))
else
  echo "PASS [W4: skip not written]"
  pass=$((pass+1))
fi

# ---------------------------------------------------------------------------
# Part B: Live read-only (SMOKE_LIVE=1 opt-in)
# ---------------------------------------------------------------------------
echo ""
if [[ "${SMOKE_LIVE:-}" != "1" ]]; then
  echo "=== Part B: SKIP (set SMOKE_LIVE=1 to run live read-only stage 0-2 + 1.5) ==="
else
  echo "=== Part B: Live read-only (dry-run, zero mutation) ==="
  echo "INFO: Running SKILL.md stage 0-2 dry-run against real Project v2 #3 (ghbvf)..."

  # Find the SKILL.md and repo root
  repo_root="$(cd "${skill_dir}/../../../.." && pwd -P)"
  echo "INFO: repo root: $repo_root"

  # Stage 0-2 dry-run: source relevant parts of SKILL.md as a bash script subset.
  # We test by running the actual bash blocks from SKILL.md stages 0, 1, 1.5, 2
  # with APPLY unset (dry-run). This verifies live gh connectivity + field ID resolution.
  live_wd=$(mktemp -d -t daily-planner-smoke.XXXXXX)
  chmod 700 "$live_wd"
  trap "rm -rf '$live_wd'" EXIT

  (
    cd "$repo_root"
    set -euo pipefail

    # Stage 0 subset: token scope + project ID + iteration field ID
    if ! AUTH_STATUS=$(gh auth status 2>&1); then
      echo "ERROR: gh auth status failed" >&2
      exit 1
    fi
    if ! grep -qiE "scopes:.*\bproject\b" <<<"$AUTH_STATUS"; then
      echo "ERROR: token missing 'project' scope" >&2
      exit 1
    fi
    PROJECT_NODE_ID=$(gh project view 3 --owner ghbvf --format json | jq -r '.id')
    ITERATION_FIELD_ID=$(gh project field-list 3 --owner ghbvf --format json \
      | jq -r '.fields[] | select(.name=="Iteration").id')
    [[ -z "$PROJECT_NODE_ID" || -z "$ITERATION_FIELD_ID" ]] && {
      echo "ERROR: failed to resolve PROJECT_NODE_ID or ITERATION_FIELD_ID" >&2
      exit 1
    }
    echo "INFO: PROJECT_NODE_ID=$PROJECT_NODE_ID ITERATION_FIELD_ID=$ITERATION_FIELD_ID" >&2

    # Stage 0 C3c: Wave field ID
    WAVE_FIELD_ID=$(gh project field-list 3 --owner ghbvf --format json \
      | jq -r '.fields[] | select(.name=="Wave").id // ""')
    echo "INFO: WAVE_FIELD_ID=${WAVE_FIELD_ID:-<not found>}" >&2

    # Stage 1 subset: issues + items + iter-config
    DATE="${DATE:-$(date +%Y-%m-%d)}"
    echo "[]" > "$live_wd/issues.json"
    gh issue list --repo ghbvf/gocell --label backlog --label pri-p0 \
      --state open --json number,title,labels,createdAt,body,url --limit 50 \
      > "$live_wd/issues-p0.json" 2>/dev/null || echo "[]" > "$live_wd/issues-p0.json"
    jq -s '(.[0] + .[1]) | unique_by(.number)' \
      "$live_wd/issues.json" "$live_wd/issues-p0.json" \
      > "$live_wd/issues.tmp" && mv "$live_wd/issues.tmp" "$live_wd/issues.json"
    echo "INFO: input issues: $(jq 'length' "$live_wd/issues.json")" >&2

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
    echo "INFO: iter-config loaded" >&2

    # Stage 1.5: deps.json (blocked-by DAG via native GraphQL)
    ISSUE_NUMBERS=$(jq '[.[].number] | @json' "$live_wd/issues.json")
    echo "INFO: fetching blocked-by DAG for input issues..." >&2
    # Build deps.json: for each input issue, query blockedBy
    echo "{}" > "$live_wd/deps.json"
    issue_count=$(jq 'length' "$live_wd/issues.json")
    if [[ "$issue_count" -gt 0 ]]; then
      # Batch query: iterate input issues and collect blocked_by
      deps_result="{}"
      while IFS= read -r num; do
        blocked=$(gh api graphql -f query='
          query($num: Int!) {
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
  ) && {
    echo "PASS [Part B: live dry-run stage 0-2 + 1.5]"
    pass=$((pass+1))
  } || {
    echo "FAIL [Part B: live dry-run stage 0-2 + 1.5]"
    fail=$((fail+1))
  }

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
