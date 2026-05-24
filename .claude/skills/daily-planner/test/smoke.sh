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

# gh stub: records item-edit calls to GH_STUB_LOG; returns exit 1 for:
#   - item id matching GH_STUB_FAIL_ITEM (write-side item failure simulation)
#   - field id matching GH_STUB_FAIL_FIELD_ID (per-field failure, e.g. Wave field)
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
        # simulate failure for a specific item id
        args=("$@")
        _item_id=""
        _field_id=""
        i=0
        while [[ $i -lt ${#args[@]} ]]; do
          if [[ "${args[$i]}" == "--id" ]]; then
            next=$((i+1))
            _item_id="${args[$next]:-}"
          fi
          if [[ "${args[$i]}" == "--field-id" ]]; then
            next=$((i+1))
            _field_id="${args[$next]:-}"
          fi
          i=$((i+1))
        done
        if [[ -n "${GH_STUB_FAIL_ITEM:-}" && "$_item_id" == "${GH_STUB_FAIL_ITEM}" ]]; then
          exit 1
        fi
        if [[ -n "${GH_STUB_FAIL_FIELD_ID:-}" && "$_field_id" == "${GH_STUB_FAIL_FIELD_ID}" ]]; then
          exit 1
        fi
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
  # make_gate_workdir <plan_fixture> [items_fixture]
  local plan_fixture="$1"
  local items_fixture="${2:-items.json}"
  local wd
  wd=$(mktemp -d)
  cp "${fixtures_dir}/issues.json"           "$wd/issues.json"
  cp "${fixtures_dir}/${items_fixture}"      "$wd/items.json"
  cp "${fixtures_dir}/iter-config.json"      "$wd/iter-config.json"
  cp "${fixtures_dir}/${plan_fixture}"       "$wd/plan.json"
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

# A9: WAVE_FIELD_ID set but WAVE_OPTION_IDS empty -> config corrupt fail-fast
wd=$(make_gate_workdir "plan-clean.json")
assert_gate_fail "A9: WAVE_FIELD_ID set but WAVE_OPTION_IDS empty -> config corrupt" \
  "WAVE_OPTION_IDS empty but WAVE_FIELD_ID set" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS="
rm -rf "$wd"

# A10: CLOSED/Done carry-over items are NOT in the allowed set -> "not in allowed set" + rc==1
# Uses items-with-closed.json which has PVTI_ITEM_CLOSED (state=CLOSED) and
# PVTI_ITEM_DONE (status=Done) both with iter.iterationId=ITER_YESTERDAY.
# The plan tries to carry these over; gate must reject them.
wd=$(make_gate_workdir "plan-closed-carryover.json" "items-with-closed.json")
assert_gate_fail "A10: CLOSED/Done carry-over not in allowed set -> rc==1" \
  "not in allowed set" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A11: conflict_group is advisory (brief display-only, no machine consumer after
# ship --from-plan removal) -> apply-gate no longer validates it; a plan WITHOUT
# conflict_group must PASS the gate (all other fields valid).
wd=$(make_gate_workdir "plan-missing-conflict-group.json")
assert_gate_pass "A11: missing conflict_group -> exit 0 (advisory, not validated)" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# A12: conflict_group present but NON-INTEGER ("not-an-int") -> still PASSES gate.
# Reverse of the deleted A5 (which asserted the old int-validation FAILED on this
# fixture). Proves the guard removal is complete: a value that previously hard-failed
# the gate now passes (apply-write never reads conflict_group). All other fields valid.
wd=$(make_gate_workdir "plan-bad-parallel-group.json")
assert_gate_pass "A12: non-int conflict_group -> exit 0 (guard removed, advisory)" \
  "WORKDIR=$wd" "TODAY_ITERATION_ID=ITER_TODAY" "YESTERDAY_ITERATION_ID=ITER_YESTERDAY" \
  "WAVE_FIELD_ID=WFIELD_123" "WAVE_OPTION_IDS=OPT_WAVE1,OPT_WAVE2,OPT_WAVE3,OPT_WAVE4"
rm -rf "$wd"

# ---------------------------------------------------------------------------
# Part A: resolve-wave-fields cases (single-line extraction regression)
# Pins the per-element `// ""` newline-poison bug (same class as PR #926 jq
# scoping). field-list.json has Wave as the 5th field with 4 named options, so
# a buggy extraction would prepend empty lines to WAVE_FIELD_ID / option ids.
# ---------------------------------------------------------------------------
echo ""
echo "=== Part A: resolve-wave-fields.sh ==="

resolve_script="${skill_dir}/lib/resolve-wave-fields.sh"
[[ -x "$resolve_script" ]] || { echo "MISSING: $resolve_script" >&2; exit 1; }

rwf_out=$(FIELD_LIST_JSON="$(cat "${fixtures_dir}/field-list.json")" bash "$resolve_script")
# shellcheck disable=SC1090
eval "$rwf_out"

rwf_ok=1
check_rwf() {
  # check_rwf <name> <actual> <expected>; also asserts single clean line (no embedded newline)
  local name="$1" actual="$2" expected="$3"
  local nlines
  nlines=$(printf '%s' "$actual" | grep -c '' || true)   # 1 for single line, >1 if newline-poisoned
  if [[ "$actual" != "$expected" ]]; then
    echo "  FAIL [$name]: got [$actual] want [$expected]"; rwf_ok=0
  elif [[ "$nlines" != "1" ]]; then
    echo "  FAIL [$name]: value spans $nlines lines (newline-poisoned)"; rwf_ok=0
  fi
}
check_rwf "WAVE_FIELD_ID"   "${WAVE_FIELD_ID:-}"   "PVTSSF_wave"
check_rwf "WAVE_OPTION_IDS" "${WAVE_OPTION_IDS:-}" "opt_w1,opt_w2,opt_w3,opt_w4"
for _n in 1 2 3 4; do
  _vn="WAVE_OPTION_ID_WAVE${_n}"   # indirect ref: vars assigned via eval, avoids SC2153
  check_rwf "$_vn" "${!_vn:-}" "opt_w${_n}"
done
if [[ "$rwf_ok" == "1" ]]; then
  echo "PASS [RWF: Wave field/option ids resolve to clean single-line values]"
  pass=$((pass+1))
else
  echo "FAIL [RWF: resolve-wave-fields]"
  fail=$((fail+1))
fi

# RWF2: no Wave field -> all empty, no error
rwf2_out=$(FIELD_LIST_JSON='{"fields":[{"id":"PVTF_iter","name":"Iteration"}]}' bash "$resolve_script")
# shellcheck disable=SC1090
eval "$rwf2_out"
if [[ -z "$WAVE_FIELD_ID" && -z "$WAVE_OPTION_IDS" && -z "$WAVE_OPTION_ID_WAVE1" ]]; then
  echo "PASS [RWF2: no Wave field -> empty vars, no crash]"
  pass=$((pass+1))
else
  echo "FAIL [RWF2: no Wave field -> expected empty, got WAVE_FIELD_ID=[$WAVE_FIELD_ID]]"
  fail=$((fail+1))
fi
# Reset for downstream cases (RWF2 cleared them).
unset WAVE_FIELD_ID WAVE_OPTION_IDS WAVE_OPTION_ID_WAVE1 WAVE_OPTION_ID_WAVE2 WAVE_OPTION_ID_WAVE3 WAVE_OPTION_ID_WAVE4

# ---------------------------------------------------------------------------
# Part A: stage 0-2 fetch/setup lib extraction (resolve-constants / fetch-data /
# fetch-deps / ensure-iteration). These cover the deterministic jq/date logic
# that used to live inline in SKILL.md stage 0-2 (untested). gh is PATH-stubbed;
# the pure-inference cases (ensure-iteration when the iteration exists) need no
# gh at all.
# ---------------------------------------------------------------------------
echo ""
echo "=== Part A: stage 0-2 lib (resolve-constants / fetch-data / fetch-deps / ensure-iteration) ==="

resolve_constants_script="${skill_dir}/lib/resolve-constants.sh"
fetch_data_script="${skill_dir}/lib/fetch-data.sh"
fetch_deps_script="${skill_dir}/lib/fetch-deps.sh"
ensure_iteration_script="${skill_dir}/lib/ensure-iteration.sh"

# Stage-0-2 gh stub: routes auth/project/issue/graphql by args. Responses are
# env-configurable so cases inject fixtures. graphql queries are routed by a
# distinctive substring of the query body.
stage_stub_dir=$(mktemp -d)
cat > "${stage_stub_dir}/gh" <<'SSTUB'
#!/usr/bin/env bash
set -euo pipefail
all="$*"
case "${1:-} ${2:-}" in
  "auth status")
    echo "github.com" >&2
    echo "  - Token scopes: 'project', 'read:org', 'repo'" >&2
    exit 0 ;;
  "project view")
    echo "{\"id\":\"${GH_STUB_PROJECT_ID:-PNI_STUB}\"}" ;;
  "project field-list")
    cat "${GH_STUB_FIELD_LIST:?GH_STUB_FIELD_LIST required}" ;;
  "issue list")
    # route by pri label present in args. Real gh always emits a JSON array
    # (at least []); emit [] when the configured fixture is unset/empty.
    _il=""
    if [[ "$all" == *"pri-p0"* ]]; then _il="${GH_STUB_ISSUES_P0:-}"
    elif [[ "$all" == *"pri-p1"* ]]; then _il="${GH_STUB_ISSUES_P1:-}"
    elif [[ "$all" == *"pri-missing"* ]]; then _il="${GH_STUB_ISSUES_MISSING:-}"
    fi
    if [[ -s "$_il" ]]; then cat "$_il"; else echo "[]"; fi ;;
  "api graphql")
    # Routing is first-match-wins. Order matters: the iteration-create MUTATION
    # body contains "configuration", so it MUST be matched (updateProjectV2Field)
    # before the iter-config READ branch (ProjectV2IterationField/configuration),
    # otherwise the mutation would be mis-routed to the read response.
    if [[ "$all" == *"updateProjectV2Field"* ]]; then cat "${GH_STUB_GRAPHQL_ITERUPDATE:-/dev/null}"
    elif [[ "$all" == *"blockedBy"* ]]; then cat "${GH_STUB_GRAPHQL_DEPS:-/dev/null}"
    elif [[ "$all" == *"subIssuesSummary"* ]]; then echo "{}"
    elif [[ "$all" == *"items(first"* ]]; then cat "${GH_STUB_GRAPHQL_ITEMS:-/dev/null}"
    elif [[ "$all" == *"ProjectV2IterationField"* || "$all" == *"configuration"* ]]; then cat "${GH_STUB_GRAPHQL_ITERCFG:-/dev/null}"
    else echo "{}"; fi ;;
  *) echo "stage-stub: unexpected gh $all" >&2; exit 1 ;;
esac
SSTUB
chmod +x "${stage_stub_dir}/gh"
# extend EXIT trap to also clean stage_stub_dir
trap 'rm -rf "$stub_dir" "$stage_stub_dir"' EXIT

# --- resolve-constants.sh: date/mode derivation ---
# PC-RC1: Saturday -> IS_WEEKEND=true, WAVE_COUNT=4
if [[ ! -x "$resolve_constants_script" ]]; then
  echo "FAIL [PC-RC1: resolve-constants.sh missing (expected RED before Wave 2)]"; fail=$((fail+1))
else
  out=$(env "DATE=2026-05-23" "GH_STUB_FIELD_LIST=${fixtures_dir}/field-list.json" \
        "PATH=${stage_stub_dir}:${PATH}" bash "$resolve_constants_script" 2>/dev/null) || out=""
  if [[ "$out" == *"IS_WEEKEND=true"* && "$out" == *"WAVE_COUNT=4"* \
        && "$out" == *"ITERATION_FIELD_ID=PVTF_iteration"* \
        && "$out" == *"PROJECT_NODE_ID=PNI_STUB"* ]]; then
    echo "PASS [PC-RC1: Saturday -> weekend, 4 waves, project node + iteration field resolved]"; pass=$((pass+1))
  else
    echo "FAIL [PC-RC1: expected IS_WEEKEND=true/WAVE_COUNT=4/ITERATION_FIELD_ID=PVTF_iteration/PROJECT_NODE_ID=PNI_STUB; got: $out]"; fail=$((fail+1))
  fi
fi

# PC-RC2: Monday -> IS_WEEKEND=false, WAVE_COUNT=2
if [[ ! -x "$resolve_constants_script" ]]; then
  echo "FAIL [PC-RC2: resolve-constants.sh missing (expected RED before Wave 2)]"; fail=$((fail+1))
else
  out=$(env "DATE=2026-05-25" "GH_STUB_FIELD_LIST=${fixtures_dir}/field-list.json" \
        "PATH=${stage_stub_dir}:${PATH}" bash "$resolve_constants_script" 2>/dev/null) || out=""
  if [[ "$out" == *"IS_WEEKEND=false"* && "$out" == *"WAVE_COUNT=2"* ]]; then
    echo "PASS [PC-RC2: Monday -> weekday, 2 waves]"; pass=$((pass+1))
  else
    echo "FAIL [PC-RC2: expected IS_WEEKEND=false/WAVE_COUNT=2; got: $out]"; fail=$((fail+1))
  fi
fi

# PC-RC3: --weekend + --weekday both -> fail-fast "mutually exclusive"
if [[ ! -x "$resolve_constants_script" ]]; then
  echo "FAIL [PC-RC3: resolve-constants.sh missing (expected RED before Wave 2)]"; fail=$((fail+1))
else
  rc3_err=$(env "DATE=2026-05-25" "FORCE_WEEKEND=true" "FORCE_WEEKDAY=true" \
        "GH_STUB_FIELD_LIST=${fixtures_dir}/field-list.json" \
        "PATH=${stage_stub_dir}:${PATH}" bash "$resolve_constants_script" 2>&1 >/dev/null) && rc3_rc=0 || rc3_rc=$?
  if [[ "${rc3_rc:-0}" -ne 0 && "$rc3_err" == *"mutually exclusive"* ]]; then
    echo "PASS [PC-RC3: --weekend + --weekday -> fail-fast]"; pass=$((pass+1))
  else
    echo "FAIL [PC-RC3: expected non-zero + 'mutually exclusive'; rc=${rc3_rc:-0} err=$rc3_err]"; fail=$((fail+1))
  fi
fi

# --- ensure-iteration.sh: iteration inference (pure jq, no gh) ---
# PC-EI1: DATE matches an existing iteration -> TODAY/YESTERDAY ids + carry-over enabled
if [[ ! -x "$ensure_iteration_script" ]]; then
  echo "FAIL [PC-EI1: ensure-iteration.sh missing (expected RED before Wave 2)]"; fail=$((fail+1))
else
  ei_wd=$(mktemp -d); cp "${fixtures_dir}/iter-config.json" "$ei_wd/iter-config.json"
  out=$(env "WORKDIR=$ei_wd" "DATE=2026-05-24" bash "$ensure_iteration_script" 2>/dev/null) || out=""
  if [[ "$out" == *"TODAY_ITERATION_ID=ITER_TODAY"* \
        && "$out" == *"YESTERDAY_ITERATION_ID=ITER_YESTERDAY"* \
        && "$out" == *"CARRY_OVER_DISABLED=false"* ]]; then
    echo "PASS [PC-EI1: today/yesterday iteration inferred, carry-over enabled]"; pass=$((pass+1))
  else
    echo "FAIL [PC-EI1: expected ITER_TODAY/ITER_YESTERDAY/CARRY_OVER_DISABLED=false; got: $out]"; fail=$((fail+1))
  fi
  rm -rf "$ei_wd"
fi

# PC-EI2: DATE has no matching iteration + dry-run (APPLY unset) -> abort exit 0, no mutation
if [[ ! -x "$ensure_iteration_script" ]]; then
  echo "FAIL [PC-EI2: ensure-iteration.sh missing (expected RED before Wave 2)]"; fail=$((fail+1))
else
  ei_wd=$(mktemp -d); cp "${fixtures_dir}/iter-config.json" "$ei_wd/iter-config.json"
  ei2_err=$(env "WORKDIR=$ei_wd" "DATE=2026-05-22" bash "$ensure_iteration_script" 2>&1 >/dev/null) && ei2_rc=0 || ei2_rc=$?
  if [[ "${ei2_rc:-0}" -eq 0 && "$ei2_err" == *"DRY-RUN"* ]]; then
    echo "PASS [PC-EI2: missing iteration + dry-run -> abort exit 0]"; pass=$((pass+1))
  else
    echo "FAIL [PC-EI2: expected exit 0 + DRY-RUN abort; rc=${ei2_rc:-0} err=$ei2_err]"; fail=$((fail+1))
  fi
  rm -rf "$ei_wd"
fi

# PC-EI3: yesterday iteration absent -> CARRY_OVER_DISABLED=true
if [[ ! -x "$ensure_iteration_script" ]]; then
  echo "FAIL [PC-EI3: ensure-iteration.sh missing (expected RED before Wave 2)]"; fail=$((fail+1))
else
  ei_wd=$(mktemp -d)
  # iter-config with only today (no 05-23 yesterday)
  jq '.data.user.projectV2.field.configuration.iterations |= map(select(.id=="ITER_TODAY"))' \
    "${fixtures_dir}/iter-config.json" > "$ei_wd/iter-config.json"
  out=$(env "WORKDIR=$ei_wd" "DATE=2026-05-24" bash "$ensure_iteration_script" 2>/dev/null) || out=""
  if [[ "$out" == *"CARRY_OVER_DISABLED=true"* ]]; then
    echo "PASS [PC-EI3: no yesterday iteration -> carry-over disabled]"; pass=$((pass+1))
  else
    echo "FAIL [PC-EI3: expected CARRY_OVER_DISABLED=true; got: $out]"; fail=$((fail+1))
  fi
  rm -rf "$ei_wd"
fi

# PC-EI4: APPLY=true + DATE absent from iter-config -> ensure-iteration creates the
# iteration via updateProjectV2Field mutation (gh-stubbed) and re-resolves the new id.
# Guards that the apply-only mutation path emits TODAY_ITERATION_ID from the mutation
# response. (The stub routes updateProjectV2Field BEFORE the configuration read branch.)
if [[ ! -x "$ensure_iteration_script" ]]; then
  echo "FAIL [PC-EI4: ensure-iteration.sh missing (expected RED before Wave 2)]"; fail=$((fail+1))
else
  ei_wd=$(mktemp -d); cp "${fixtures_dir}/iter-config.json" "$ei_wd/iter-config.json"
  # mutation response carrying the newly-created iteration for 2026-05-26
  cat > "$ei_wd/iter-update.json" <<'EIUPD'
{"data":{"updateProjectV2Field":{"projectV2Field":{"configuration":{"iterations":[
  {"id":"ITER_YESTERDAY","title":"2026-05-23","startDate":"2026-05-23","duration":1},
  {"id":"ITER_TODAY","title":"2026-05-24","startDate":"2026-05-24","duration":1},
  {"id":"ITER_CREATED","title":"Iteration 3","startDate":"2026-05-26","duration":1}
]}}}}}
EIUPD
  out=$(env "WORKDIR=$ei_wd" "DATE=2026-05-26" "APPLY=true" "ITERATION_FIELD_ID=PVTF_iteration" \
        "GH_STUB_GRAPHQL_ITERUPDATE=$ei_wd/iter-update.json" \
        "PATH=${stage_stub_dir}:${PATH}" bash "$ensure_iteration_script" 2>/dev/null) || out=""
  if [[ "$out" == *"TODAY_ITERATION_ID=ITER_CREATED"* ]]; then
    echo "PASS [PC-EI4: apply mode creates missing iteration via mutation]"; pass=$((pass+1))
  else
    echo "FAIL [PC-EI4: expected TODAY_ITERATION_ID=ITER_CREATED; got: $out]"; fail=$((fail+1))
  fi
  rm -rf "$ei_wd"
fi

# --- fetch-data.sh: issue dedup merge + items paginate unpack (gh-stubbed) ---
# PC-FD1: P0 and P1 lists share issue #101 -> issues.json deduped by number;
# items graphql page (paginate-wrapped) -> items.json unpacked via jq -s '[...nodes[]]'.
# NOTE: GH_STUB_GRAPHQL_ITEMS uses items-graphql-page.json (raw paginate response
# shape {data.user.projectV2.items.nodes[]}), NOT the flat items.json fixture — the
# latter is the POST-jq shape that apply-gate consumes, not the gh-response shape.
if [[ ! -x "$fetch_data_script" ]]; then
  echo "FAIL [PC-FD1: fetch-data.sh missing (expected RED before Wave 2)]"; fail=$((fail+1))
else
  fd_wd=$(mktemp -d)
  echo '[{"number":101,"title":"a"},{"number":102,"title":"b"}]' > "$fd_wd/p0.json"
  echo '[{"number":101,"title":"a"},{"number":103,"title":"c"}]' > "$fd_wd/p1.json"
  env "WORKDIR=$fd_wd" \
      "GH_STUB_FIELD_LIST=${fixtures_dir}/field-list.json" \
      "GH_STUB_ISSUES_P0=$fd_wd/p0.json" "GH_STUB_ISSUES_P1=$fd_wd/p1.json" \
      "GH_STUB_ISSUES_MISSING=/dev/null" \
      "GH_STUB_GRAPHQL_ITEMS=${fixtures_dir}/items-graphql-page.json" \
      "GH_STUB_GRAPHQL_ITERCFG=${fixtures_dir}/iter-config.json" \
      "PATH=${stage_stub_dir}:${PATH}" bash "$fetch_data_script" >/dev/null 2>&1 || true
  fd_issues=$(jq 'length' "$fd_wd/issues.json" 2>/dev/null || echo missing)
  fd_items=$(jq 'length' "$fd_wd/items.json" 2>/dev/null || echo missing)
  if [[ "$fd_issues" == "3" && "$fd_items" == "4" ]]; then
    echo "PASS [PC-FD1: issues deduped (3 unique) + items paginate-unpacked (4 nodes)]"; pass=$((pass+1))
  else
    echo "FAIL [PC-FD1: expected issues=3 items=4; got issues=$fd_issues items=$fd_items]"; fail=$((fail+1))
  fi
  rm -rf "$fd_wd"
fi

# --- fetch-deps.sh: blocked-by DAG assembly (gh-stubbed) ---
# PC-FDEP1: issue #102 blocked by #101 -> deps.json schema { "102": { "blocked_by": [101] } }
if [[ ! -x "$fetch_deps_script" ]]; then
  echo "FAIL [PC-FDEP1: fetch-deps.sh missing (expected RED before Wave 2)]"; fail=$((fail+1))
else
  fdep_wd=$(mktemp -d)
  echo '[{"number":102,"title":"b"}]' > "$fdep_wd/issues.json"
  echo '{"data":{"repository":{"issue":{"blockedBy":{"nodes":[{"number":101}]}}}}}' > "$fdep_wd/dep-resp.json"
  env "WORKDIR=$fdep_wd" "GH_STUB_GRAPHQL_DEPS=$fdep_wd/dep-resp.json" \
      "PATH=${stage_stub_dir}:${PATH}" bash "$fetch_deps_script" >/dev/null 2>&1 || true
  if [[ -f "$fdep_wd/deps.json" ]] \
     && [[ "$(jq -r '."102".blocked_by[0]' "$fdep_wd/deps.json" 2>/dev/null)" == "101" ]]; then
    echo "PASS [PC-FDEP1: deps.json blocked-by edge assembled]"; pass=$((pass+1))
  else
    echo "FAIL [PC-FDEP1: expected deps.json {102:{blocked_by:[101]}}; got $(cat "$fdep_wd/deps.json" 2>/dev/null || echo missing)]"; fail=$((fail+1))
  fi
  rm -rf "$fdep_wd"
fi

# PC-FDEP2: blocked-by query fails (transient) -> loud WARN + deps.json={} (NOT silent);
# script still exits 0. Points GH_STUB_GRAPHQL_DEPS at a missing file so the stub's
# `cat` fails -> gh stub exits non-zero -> fetch-deps takes the DEP_FETCH_FAILED path.
if [[ ! -x "$fetch_deps_script" ]]; then
  echo "FAIL [PC-FDEP2: fetch-deps.sh missing (expected RED before Wave 2)]"; fail=$((fail+1))
else
  fdep2_wd=$(mktemp -d)
  echo '[{"number":102,"title":"b"}]' > "$fdep2_wd/issues.json"
  fdep2_err=$(env "WORKDIR=$fdep2_wd" "GH_STUB_GRAPHQL_DEPS=$fdep2_wd/nonexistent.json" \
      "PATH=${stage_stub_dir}:${PATH}" bash "$fetch_deps_script" 2>&1 >/dev/null) && fdep2_rc=0 || fdep2_rc=$?
  fdep2_deps=$(cat "$fdep2_wd/deps.json" 2>/dev/null || echo missing)
  if [[ "${fdep2_rc:-0}" -eq 0 && "$fdep2_deps" == "{}" \
        && "$fdep2_err" == *"[DEP DATA UNAVAILABLE]"* ]]; then
    echo "PASS [PC-FDEP2: transient failure -> loud WARN + deps.json={} (no silent)]"; pass=$((pass+1))
  else
    echo "FAIL [PC-FDEP2: expected rc0 + deps={} + [DEP DATA UNAVAILABLE]; rc=${fdep2_rc:-0} deps=$fdep2_deps]"; fail=$((fail+1))
  fi
  rm -rf "$fdep2_wd"
fi

# ---------------------------------------------------------------------------
# Part A: Write cases
# ---------------------------------------------------------------------------
echo ""
echo "=== Part A: apply-write.sh ==="

run_write() {
  # run_write <wd> <fail_item> <gh_log> <stderr_out> [fail_field_id]
  # Returns exit code via $RUN_WRITE_RC (caller must reset to 0 before calling).
  local wd="$1" fail_item="$2" gh_log="$3" stderr_out="$4"
  local _fail_field_id="${5:-}"
  RUN_WRITE_RC=0
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
    "GH_STUB_FAIL_FIELD_ID=${_fail_field_id}" \
    "PATH=${stub_dir}:${PATH}" \
    bash "$write_script" >/dev/null 2>"$stderr_out" || RUN_WRITE_RC=$?
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

# W1: clean plan -> rc==0 + audit fields OK + iteration + wave calls made
wd_w1=$(mktemp -d)
cp "${fixtures_dir}/issues.json" "${fixtures_dir}/items.json" \
   "${fixtures_dir}/iter-config.json" "$wd_w1/"
cp "${fixtures_dir}/plan-clean.json" "$wd_w1/plan.json"
gh_log_w1=$(mktemp)
stderr_w1=$(mktemp)
RUN_WRITE_RC=0; run_write "$wd_w1" "" "$gh_log_w1" "$stderr_w1"
rc_w1=$RUN_WRITE_RC
gh_calls_w1=$(cat "$gh_log_w1"); rm -f "$gh_log_w1" "$stderr_w1"
w1_ok=true
if [[ $rc_w1 -ne 0 ]]; then
  echo "FAIL [W1] expected rc==0, got rc=$rc_w1"
  w1_ok=false
fi
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

# W2: partial failure -> rc==1 (C2: any mutation failure exits non-zero) + rollback applies
# to already-written item (PVTI_ITEM_101) with specific --id and --iteration-id + retry
# section for PVTI_ITEM_102.
# PVTI_ITEM_101 (prev=ITER_TODAY) succeeds -> rollback section contains:
#   "# --- rollback commands" marker
#   "--id PVTI_ITEM_101 ... --iteration-id ITER_TODAY"
# PVTI_ITEM_102 fails -> "failed items" section contains PVTI_ITEM_102
wd_w2=$(mktemp -d)
cp "${fixtures_dir}/issues.json" "${fixtures_dir}/items.json" \
   "${fixtures_dir}/iter-config.json" "$wd_w2/"
cp "${fixtures_dir}/plan-clean.json" "$wd_w2/plan.json"
gh_log_w2=$(mktemp)
stderr_w2=$(mktemp)
RUN_WRITE_RC=0; run_write "$wd_w2" "PVTI_ITEM_102" "$gh_log_w2" "$stderr_w2"
rc_w2=$RUN_WRITE_RC
stderr_content_w2=$(cat "$stderr_w2"); rm -f "$gh_log_w2" "$stderr_w2"
w2_ok=true
if [[ $rc_w2 -ne 1 ]]; then
  echo "FAIL [W2] expected rc==1 (partial failure), got rc=$rc_w2"
  w2_ok=false
fi
if [[ "$stderr_content_w2" != *"# --- rollback commands"* ]]; then
  echo "FAIL [W2] rollback marker '# --- rollback commands' not found"
  echo "  stderr: $stderr_content_w2"
  w2_ok=false
fi
if [[ "$stderr_content_w2" != *"--id PVTI_ITEM_101"*"--iteration-id ITER_TODAY"* ]]; then
  echo "FAIL [W2] rollback missing '--id PVTI_ITEM_101 ... --iteration-id ITER_TODAY'"
  echo "  stderr: $stderr_content_w2"
  w2_ok=false
fi
if [[ "$stderr_content_w2" != *"failed items"* ]]; then
  echo "FAIL [W2] missing 'failed items' section"
  w2_ok=false
fi
if [[ "$stderr_content_w2" != *"PVTI_ITEM_102"* ]]; then
  echo "FAIL [W2] PVTI_ITEM_102 not in failed/retry section"
  w2_ok=false
fi
if $w2_ok; then echo "PASS [W2: partial fail -> rc==1 + rollback applied + retry failed]"; pass=$((pass+1))
else fail=$((fail+1)); fi
rm -rf "$wd_w2"

# W3: empty prev_iteration_id -> rc==1 (partial fail) + rollback uses --clear
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
    "conflict_group": 1,
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
    "conflict_group": 2,
    "wave_option_id": "OPT_WAVE1"
  }
]
PLANEOF
gh_log_w3=$(mktemp)
stderr_w3=$(mktemp)
RUN_WRITE_RC=0; run_write "$wd_w3" "PVTI_ITEM_102" "$gh_log_w3" "$stderr_w3"
rc_w3=$RUN_WRITE_RC
stderr_content_w3=$(cat "$stderr_w3"); rm -f "$gh_log_w3" "$stderr_w3"
w3_ok=true
if [[ $rc_w3 -ne 1 ]]; then
  echo "FAIL [W3] expected rc==1 (partial failure), got rc=$rc_w3"
  w3_ok=false
fi
if [[ "$stderr_content_w3" != *"--clear"* ]]; then
  echo "FAIL [W3] missing --clear in rollback for null prev"
  echo "  stderr: $stderr_content_w3"
  w3_ok=false
fi
if [[ "$stderr_content_w3" != *"PVTI_ITEM_102"* ]]; then
  echo "FAIL [W3] PVTI_ITEM_102 not in retry section"
  w3_ok=false
fi
if $w3_ok; then echo "PASS [W3: empty prev -> rc==1 + rollback --clear]"; pass=$((pass+1))
else fail=$((fail+1)); fi
rm -rf "$wd_w3"

# W4: action==skip -> rc==0 + no gh calls + audit result=skipped
wd_w4=$(mktemp -d)
cp "${fixtures_dir}/issues.json" "${fixtures_dir}/items.json" \
   "${fixtures_dir}/iter-config.json" "$wd_w4/"
cp "${fixtures_dir}/plan-skip-action.json" "$wd_w4/plan.json"
gh_log_w4=$(mktemp)
stderr_w4=$(mktemp)
RUN_WRITE_RC=0; run_write "$wd_w4" "" "$gh_log_w4" "$stderr_w4"
rc_w4=$RUN_WRITE_RC
w4_gh=$(cat "$gh_log_w4"); rm -f "$gh_log_w4" "$stderr_w4"
w4_audit=$(cat "$wd_w4/audit.ndjson" 2>/dev/null || true)
w4_ok=true
if [[ $rc_w4 -ne 0 ]]; then
  echo "FAIL [W4] expected rc==0, got rc=$rc_w4"
  w4_ok=false
fi
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

# W5: Wave write fails but Iteration write succeeds -> rc==1 (C2: wave_failed counts as
#     non-zero) + iteration audit=applied + stderr contains Wave WARN + wave_failed audit
#     line present.
# GH_STUB_FAIL_FIELD_ID=WFIELD_123 causes Wave item-edit to fail.
wd_w5=$(mktemp -d)
cp "${fixtures_dir}/issues.json" "${fixtures_dir}/items.json" \
   "${fixtures_dir}/iter-config.json" "$wd_w5/"
cp "${fixtures_dir}/plan-clean.json" "$wd_w5/plan.json"
gh_log_w5=$(mktemp)
stderr_w5=$(mktemp)
RUN_WRITE_RC=0; run_write "$wd_w5" "" "$gh_log_w5" "$stderr_w5" "WFIELD_123"
rc_w5=$RUN_WRITE_RC
gh_calls_w5=$(cat "$gh_log_w5")
stderr_content_w5=$(cat "$stderr_w5"); rm -f "$gh_log_w5" "$stderr_w5"
w5_audit=$(cat "$wd_w5/audit.ndjson" 2>/dev/null || true)
w5_ok=true
if [[ $rc_w5 -ne 1 ]]; then
  echo "FAIL [W5] expected rc==1 (wave_failed exits non-zero per C2), got rc=$rc_w5"
  w5_ok=false
fi
if [[ "$gh_calls_w5" != *"--field-id IFIELD_123 --iteration-id ITER_TODAY"* ]]; then
  echo "FAIL [W5] gh missing iteration call; got: $gh_calls_w5"
  w5_ok=false
fi
if [[ "$stderr_content_w5" != *"WARN: Wave field write failed"* ]]; then
  echo "FAIL [W5] missing Wave WARN in stderr"
  echo "  stderr: $stderr_content_w5"
  w5_ok=false
fi
if [[ "$w5_audit" != *'"result":"applied"'* ]]; then
  echo "FAIL [W5] no 'applied' audit line for iteration write"
  w5_ok=false
fi
if [[ "$w5_audit" != *'"result":"wave_failed"'* ]]; then
  echo "FAIL [W5] no 'wave_failed' audit line"
  w5_ok=false
fi
if $w5_ok; then echo "PASS [W5: Wave fail -> rc==1 + iteration applied + wave_failed audited]"; pass=$((pass+1))
else fail=$((fail+1)); fi
rm -rf "$wd_w5"

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
  # Cumulative trap: keep cleaning the Part A stub dirs (registered earlier) AND
  # live_wd. A bare `trap "rm -rf '$live_wd'"` would OVERWRITE the earlier trap and
  # leak stub_dir / stage_stub_dir if the script dies during/after Part B.
  # shellcheck disable=SC2064
  trap "rm -rf '${stub_dir}' '${stage_stub_dir}' '${live_wd}'" EXIT

  live_rc=0
  (
    cd "$repo_root"
    set -euo pipefail

    # Part B now drives the SAME lib/ scripts SKILL.md stage 0-1.5 uses (single
    # source — no re-implementation). Read-only: resolve-constants + fetch-data +
    # fetch-deps make zero mutations (ensure-iteration is apply-only and skipped).
    lib="${skill_dir}/lib"

    # Stage 0: token scope + project/iteration IDs + date/mode
    # shellcheck disable=SC1090
    eval "$(DATE="${DATE:-}" bash "$lib/resolve-constants.sh")"
    echo "INFO: PROJECT_NODE_ID=$PROJECT_NODE_ID ITERATION_FIELD_ID=$ITERATION_FIELD_ID IS_WEEKEND=$IS_WEEKEND" >&2
    # shellcheck disable=SC1090
    eval "$(FIELD_LIST_JSON="$FIELD_LIST_JSON" bash "$lib/resolve-wave-fields.sh")"
    echo "INFO: WAVE_FIELD_ID=${WAVE_FIELD_ID:-<not found>}" >&2

    # Stage 1: issues + items + iter-config + sub-issues
    WORKDIR="$live_wd" bash "$lib/fetch-data.sh"
    echo "INFO: input issues: $(jq 'length' "$live_wd/issues.json") items: $(jq 'length' "$live_wd/items.json")" >&2

    # Stage 1.5: blocked-by DAG (loud-WARN degrade path lives in the lib script)
    WORKDIR="$live_wd" bash "$lib/fetch-deps.sh"
    echo "INFO: deps.json = $(cat "$live_wd/deps.json")" >&2
    echo "INFO: Part B stage 0-1.5 dry-run complete via lib/ scripts (zero mutations)" >&2
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
echo "# Summary: ${pass} pass, ${fail} fail"
if [[ $fail -gt 0 ]]; then
  exit 1
fi
