#!/usr/bin/env bash
# apply-write.sh — write plan.json to Project v2 (assumes apply-gate.sh already passed).
#
# Inputs (env):
#   WORKDIR               — directory containing plan.json and audit.ndjson output
#   TODAY_ITERATION_ID    — the iteration ID for today
#   YESTERDAY_ITERATION_ID — iteration ID for yesterday (may be empty)
#   PROJECT_NODE_ID       — Project v2 node ID
#   ITERATION_FIELD_ID    — Iteration field ID
#   WAVE_FIELD_ID         — Wave single-select field ID (required for Wave writes)
#   DATE                  — YYYY-MM-DD target date
#   IS_WEEKEND            — true|false (for audit mode field)
#
# All gh calls go through $PATH so smoke tests can PATH-stub gh.

set -euo pipefail

: "${WORKDIR:?WORKDIR required}"
: "${TODAY_ITERATION_ID:?TODAY_ITERATION_ID required}"
: "${PROJECT_NODE_ID:?PROJECT_NODE_ID required}"
: "${ITERATION_FIELD_ID:?ITERATION_FIELD_ID required}"
: "${DATE:?DATE required}"
: "${IS_WEEKEND:?IS_WEEKEND required}"

PLAN_FILE="$WORKDIR/plan.json"
AUDIT_NDJSON="$WORKDIR/audit.ndjson"
: > "$AUDIT_NDJSON"

# ---------------------------------------------------------------------------
# audit_entry helper: append one NDJSON line per plan row
# Fields: ts / date / mode / action / item_id / prev_iteration_id /
#         target_iteration_id / result
# ---------------------------------------------------------------------------
audit_entry() {
  # $1=action $2=item_id $3=prev_iter $4=target_iter $5=result
  printf '{"ts":"%s","date":"%s","mode":"%s","action":"%s","item_id":"%s","prev_iteration_id":"%s","target_iteration_id":"%s","result":"%s"}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    "$DATE" \
    "$([[ "$IS_WEEKEND" == "true" ]] && echo weekend || echo weekday)" \
    "$1" "$2" "$3" "$4" "$5" \
    >> "$AUDIT_NDJSON"
}

# ---------------------------------------------------------------------------
# Main write loop
# ---------------------------------------------------------------------------
APPLIED=0
SKIPPED=0
APPLIED_ITEMS=()
FAILED_ITEMS=()

while IFS= read -r row; do
  action=$(jq -r '.action' <<<"$row")
  item_id=$(jq -r '.item_id' <<<"$row")
  tgt=$(jq -r '.target_iteration_id' <<<"$row")
  cur=$(jq -r '.current_iteration_id // ""' <<<"$row")
  woi=$(jq -r '.wave_option_id // ""' <<<"$row")

  if [[ "$action" == "skip" ]]; then
    SKIPPED=$((SKIPPED+1))
    audit_entry "$action" "$item_id" "$cur" "$tgt" "skipped"
    continue
  fi

  # Write Iteration field value
  if gh project item-edit \
       --project-id "$PROJECT_NODE_ID" \
       --id "$item_id" \
       --field-id "$ITERATION_FIELD_ID" \
       --iteration-id "$tgt" >/dev/null; then
    APPLIED=$((APPLIED+1))
    APPLIED_ITEMS+=("$item_id|$cur|$tgt")
    audit_entry "$action" "$item_id" "$cur" "$tgt" "applied"

    # C3c: Write Wave field value (if WAVE_FIELD_ID set and wave_option_id non-empty)
    if [[ -n "${WAVE_FIELD_ID:-}" && -n "$woi" ]]; then
      gh project item-edit \
        --project-id "$PROJECT_NODE_ID" \
        --id "$item_id" \
        --field-id "$WAVE_FIELD_ID" \
        --single-select-option-id "$woi" >/dev/null || {
          echo "WARN: Wave field write failed for item $item_id (non-fatal)" >&2
        }
    fi
  else
    FAILED_ITEMS+=("$item_id|$cur|$tgt")
    audit_entry "$action" "$item_id" "$cur" "$tgt" "failed"
  fi
done < <(jq -c '.[]' "$PLAN_FILE")

# ---------------------------------------------------------------------------
# Partial apply: emit rollback + retry sections to stderr
# ---------------------------------------------------------------------------
if [[ ${#FAILED_ITEMS[@]} -gt 0 ]]; then
  echo "" >&2
  echo "# --- partial apply detected: ${#APPLIED_ITEMS[@]} applied / ${#FAILED_ITEMS[@]} failed ---" >&2

  if [[ ${#APPLIED_ITEMS[@]} -gt 0 ]]; then
    echo "# --- rollback commands for ALREADY-APPLIED items (REVIEW EACH BEFORE EXEC; prev_iter==\"\" means original had no iteration → --clear) ---" >&2
    for entry in "${APPLIED_ITEMS[@]}"; do
      IFS='|' read -r a_item a_prev _ <<<"$entry"
      if [[ -z "$a_prev" ]]; then
        echo "gh project item-edit --project-id $PROJECT_NODE_ID --id $a_item --field-id $ITERATION_FIELD_ID --clear" >&2
      else
        echo "gh project item-edit --project-id $PROJECT_NODE_ID --id $a_item --field-id $ITERATION_FIELD_ID --iteration-id $a_prev" >&2
      fi
    done
    echo "# --- end rollback ---" >&2
  fi

  echo "# --- failed items (NOT written; safe to retry) ---" >&2
  for entry in "${FAILED_ITEMS[@]}"; do
    IFS='|' read -r f_item _ f_tgt <<<"$entry"
    echo "gh project item-edit --project-id $PROJECT_NODE_ID --id $f_item --field-id $ITERATION_FIELD_ID --iteration-id $f_tgt" >&2
  done
  echo "# --- end failed ---" >&2
fi

# ---------------------------------------------------------------------------
# Audit summary line
# ---------------------------------------------------------------------------
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
MODE_STR=$([[ "$IS_WEEKEND" == "true" ]] && echo weekend || echo weekday)
echo "[audit] $TS date=$DATE mode=$MODE_STR applied=$APPLIED skipped=$SKIPPED failed=${#FAILED_ITEMS[@]}" >&2
echo "[audit] per-item NDJSON: $AUDIT_NDJSON" >&2
