#!/usr/bin/env bash
# apply-gate.sh — validate plan.json before any Project v2 mutation.
#
# Inputs (env):
#   WORKDIR               — directory containing plan.json, issues.json, items.json,
#                           iter-config.json
#   TODAY_ITERATION_ID    — the iteration ID for today (must be non-empty)
#   YESTERDAY_ITERATION_ID — iteration ID for yesterday (may be empty)
#   WAVE_FIELD_ID         — Project v2 Wave single-select field ID (must be non-empty for apply)
#   WAVE_OPTION_IDS       — comma-separated known wave option IDs (e.g. OPT_W1,OPT_W2,...)
#
# Exit:
#   0  — plan.json is valid, safe to apply
#   1  — validation failed; errors written to stderr as "ERROR: ..."

set -euo pipefail

: "${WORKDIR:?WORKDIR required}"
: "${TODAY_ITERATION_ID:?TODAY_ITERATION_ID required}"

# C3c: Wave field must be configured for apply mode.
# WAVE_FIELD_ID set but WAVE_OPTION_IDS empty = config corrupt (field exists with no options).
if [[ -z "${WAVE_FIELD_ID:-}" ]]; then
  echo "ERROR: Wave field missing; Wave single-select field not found in Project #3; see SKILL.md §C3a" >&2
  exit 1
fi
if [[ -z "${WAVE_OPTION_IDS:-}" ]]; then
  echo "ERROR: WAVE_OPTION_IDS empty but WAVE_FIELD_ID set; Wave field has no options (config corrupt)" >&2
  exit 1
fi

PLAN_FILE="$WORKDIR/plan.json"
ISSUES_FILE="$WORKDIR/issues.json"
ITEMS_FILE="$WORKDIR/items.json"
ITER_CONFIG_FILE="$WORKDIR/iter-config.json"

# ---------------------------------------------------------------------------
# Schema validation: type check + action enum + new fields
# ---------------------------------------------------------------------------
if ! jq -e '
  (type == "array") and
  all(.[];
    (.item_id | type == "string") and
    (.target_iteration_id | type == "string") and
    (.action | type == "string") and
    (.action == "set" or .action == "skip")
  )
' "$PLAN_FILE" > /dev/null 2>&1; then
  echo "ERROR: plan.json schema invalid (type / action enum)" >&2
  exit 1
fi

# Validate parallel_group: if present, must be an integer >= 1
SCHEMA_VIOLATIONS=0
while IFS= read -r row; do
  item_id=$(jq -r '.item_id' <<<"$row")
  pg=$(jq -r '.parallel_group // "null"' <<<"$row")
  if [[ "$pg" != "null" ]]; then
    # Must be a JSON number (integer) >= 1
    if ! jq -e '(.parallel_group | type == "number") and (.parallel_group >= 1) and (.parallel_group == (.parallel_group | floor))' <<<"$row" > /dev/null 2>&1; then
      echo "ERROR: plan.json schema invalid (parallel_group must be int >= 1)" >&2
      SCHEMA_VIOLATIONS=$((SCHEMA_VIOLATIONS+1))
    fi
  fi
done < <(jq -c '.[]' "$PLAN_FILE")

[[ $SCHEMA_VIOLATIONS -gt 0 ]] && exit 1

# ---------------------------------------------------------------------------
# Compute allowed item_id set (bash-side, independent of agent)
# ---------------------------------------------------------------------------
jq -r --slurpfile issues "$ISSUES_FILE" --arg yid "${YESTERDAY_ITERATION_ID:-}" '
  ($issues[0] | map(.number)) as $inputNums |
  .[] | .content.number as $cn | select(
    ($cn != null and ($inputNums | index($cn)) != null)
    or
    ($yid != "" and .iter.iterationId == $yid
     and .content.state == "OPEN"
     and (.status == null or .status.name != "Done"))
  ) | .id
' "$ITEMS_FILE" | sort -u > "$WORKDIR/valid-item-ids.txt"

# ---------------------------------------------------------------------------
# Compute valid iteration ID set
# ---------------------------------------------------------------------------
jq -r '.data.user.projectV2.field.configuration.iterations[].id' \
  "$ITER_CONFIG_FILE" | sort -u > "$WORKDIR/valid-iter-ids.txt"
# today's iteration (may have been just created) is always valid
echo "$TODAY_ITERATION_ID" >> "$WORKDIR/valid-iter-ids.txt"
sort -u "$WORKDIR/valid-iter-ids.txt" -o "$WORKDIR/valid-iter-ids.txt"

# ---------------------------------------------------------------------------
# Per-row membership + target + current + wave_option_id validation
# ---------------------------------------------------------------------------
VIOLATIONS=0

# Build WAVE_OPTION_IDS lookup: split comma-separated into sorted file
WAVE_OPT_FILE=$(mktemp)
trap 'rm -f "$WAVE_OPT_FILE"' EXIT
printf '%s\n' "${WAVE_OPTION_IDS:-}" | tr ',' '\n' | grep -v '^$' | sort -u > "$WAVE_OPT_FILE"

while IFS= read -r row; do
  item_id=$(jq -r '.item_id' <<<"$row")
  tgt=$(jq -r '.target_iteration_id' <<<"$row")
  cur=$(jq -r '.current_iteration_id // ""' <<<"$row")
  action=$(jq -r '.action' <<<"$row")
  woi=$(jq -r '.wave_option_id // ""' <<<"$row")

  # Format guard: item_id must be a safe identifier (alphanumeric, underscore, hyphen, equals sign).
  # Reject values containing newlines or other shell-unsafe characters before any grep usage.
  if [[ ! "$item_id" =~ ^[A-Za-z0-9_=/-]+$ ]]; then
    echo "ERROR: plan.json item_id contains unsafe characters: $item_id" >&2
    VIOLATIONS=$((VIOLATIONS+1))
    continue
  fi

  if ! grep -qxF "$item_id" "$WORKDIR/valid-item-ids.txt"; then
    echo "ERROR: plan.json item_id not in allowed set (input issues ∪ carry-over): $item_id" >&2
    VIOLATIONS=$((VIOLATIONS+1))
  fi

  if [[ "$tgt" != "$TODAY_ITERATION_ID" ]]; then
    echo "ERROR: plan.json target_iteration_id != TODAY_ITERATION_ID ($tgt vs $TODAY_ITERATION_ID) for item $item_id" >&2
    VIOLATIONS=$((VIOLATIONS+1))
  fi

  if [[ -n "$cur" ]] && ! grep -qxF "$cur" "$WORKDIR/valid-iter-ids.txt"; then
    echo "ERROR: plan.json current_iteration_id unknown: $cur (item $item_id)" >&2
    VIOLATIONS=$((VIOLATIONS+1))
  fi

  # wave_option_id validation: only required for action==set.
  # WAVE_OPTION_IDS is guaranteed non-empty at this point (fail-fast above).
  if [[ "$action" == "set" ]]; then
    # Format guard: wave_option_id must be a safe identifier before grep usage.
    if [[ -n "$woi" && ! "$woi" =~ ^[A-Za-z0-9_=/-]+$ ]]; then
      echo "ERROR: plan.json wave_option_id contains unsafe characters (item $item_id)" >&2
      VIOLATIONS=$((VIOLATIONS+1))
    elif ! grep -qxF "$woi" "$WAVE_OPT_FILE"; then
      echo "ERROR: plan.json wave_option_id unknown: $woi (item $item_id)" >&2
      VIOLATIONS=$((VIOLATIONS+1))
    fi
  fi

done < <(jq -c '.[]' "$PLAN_FILE")

[[ $VIOLATIONS -gt 0 ]] && {
  echo "ERROR: $VIOLATIONS plan.json membership violation(s); refusing to apply" >&2
  exit 1
}

exit 0
