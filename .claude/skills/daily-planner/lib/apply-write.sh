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

_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=_preflight.sh
source "$_LIB_DIR/_preflight.sh"
require_cmds gh jq

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
  # Use jq to construct JSON safely — item_id/prev_iter/target_iter come from
  # LLM-generated plan.json and may contain %, ", \, or other JSON-unsafe chars.
  local _ts _mode
  _ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  _mode=$([[ "$IS_WEEKEND" == "true" ]] && echo weekend || echo weekday)
  jq -cn \
    --arg ts "$_ts" \
    --arg date "$DATE" \
    --arg mode "$_mode" \
    --arg action "$1" \
    --arg item_id "$2" \
    --arg prev_iteration_id "$3" \
    --arg target_iteration_id "$4" \
    --arg result "$5" \
    '{ts:$ts,date:$date,mode:$mode,action:$action,item_id:$item_id,prev_iteration_id:$prev_iteration_id,target_iteration_id:$target_iteration_id,result:$result}' \
    >> "$AUDIT_NDJSON"
}

# ---------------------------------------------------------------------------
# Main write loop
# ---------------------------------------------------------------------------
APPLIED=0
SKIPPED=0
WAVE_APPLIED=0
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

  # Write Iteration field value; capture stderr to a temp file for diagnosis on failure.
  _iter_err_file=$(mktemp "/tmp/gh-iter-err-${item_id}.XXXXXX")
  if gh project item-edit \
       --project-id "$PROJECT_NODE_ID" \
       --id "$item_id" \
       --field-id "$ITERATION_FIELD_ID" \
       --iteration-id "$tgt" >/dev/null 2>"$_iter_err_file"; then
    rm -f "$_iter_err_file"
    APPLIED=$((APPLIED+1))
    APPLIED_ITEMS+=("$item_id|$cur|$tgt")
    audit_entry "$action" "$item_id" "$cur" "$tgt" "applied"

    # C3c: Write Wave field value (if WAVE_FIELD_ID set and wave_option_id non-empty)
    if [[ -n "${WAVE_FIELD_ID:-}" && -n "$woi" ]]; then
      _wave_err_file=$(mktemp "/tmp/gh-wave-err-${item_id}.XXXXXX")
      if gh project item-edit \
           --project-id "$PROJECT_NODE_ID" \
           --id "$item_id" \
           --field-id "$WAVE_FIELD_ID" \
           --single-select-option-id "$woi" >/dev/null 2>"$_wave_err_file"; then
        WAVE_APPLIED=$((WAVE_APPLIED+1))
        rm -f "$_wave_err_file"
      else
        _wave_err_summary=$(head -c 200 "$_wave_err_file" | tr '\n' ' ')
        rm -f "$_wave_err_file"
        echo "WARN: Wave field write failed for item $item_id (non-fatal): ${_wave_err_summary}" >&2
        audit_entry "$action" "$item_id" "$cur" "$tgt" "wave_failed"
      fi
    fi
  else
    _iter_err_summary=$(head -c 200 "$_iter_err_file" | tr '\n' ' ')
    rm -f "$_iter_err_file"
    echo "WARN: Iteration write failed for item $item_id: ${_iter_err_summary}" >&2
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
      # Shell-quote dynamic values so the copy-paste command is safe even if
      # IDs contain spaces or special characters.
      _q_proj=$(printf '%q' "$PROJECT_NODE_ID")
      _q_item=$(printf '%q' "$a_item")
      _q_field=$(printf '%q' "$ITERATION_FIELD_ID")
      if [[ -z "$a_prev" ]]; then
        echo "gh project item-edit --project-id ${_q_proj} --id ${_q_item} --field-id ${_q_field} --clear" >&2
      else
        _q_prev=$(printf '%q' "$a_prev")
        echo "gh project item-edit --project-id ${_q_proj} --id ${_q_item} --field-id ${_q_field} --iteration-id ${_q_prev}" >&2
      fi
    done
    echo "# --- end rollback ---" >&2
  fi

  echo "# --- failed items (NOT written; safe to retry) ---" >&2
  for entry in "${FAILED_ITEMS[@]}"; do
    IFS='|' read -r f_item _ f_tgt <<<"$entry"
    _q_proj=$(printf '%q' "$PROJECT_NODE_ID")
    _q_item=$(printf '%q' "$f_item")
    _q_field=$(printf '%q' "$ITERATION_FIELD_ID")
    _q_tgt=$(printf '%q' "$f_tgt")
    echo "gh project item-edit --project-id ${_q_proj} --id ${_q_item} --field-id ${_q_field} --iteration-id ${_q_tgt}" >&2
  done
  echo "# --- end failed ---" >&2
fi

# ---------------------------------------------------------------------------
# Audit summary line
# ---------------------------------------------------------------------------
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
MODE_STR=$([[ "$IS_WEEKEND" == "true" ]] && echo weekend || echo weekday)
echo "[audit] $TS date=$DATE mode=$MODE_STR applied=$APPLIED skipped=$SKIPPED failed=${#FAILED_ITEMS[@]} wave_applied=$WAVE_APPLIED" >&2
echo "[audit] per-item NDJSON: $AUDIT_NDJSON" >&2

# C2: any mutation failure (Iteration write or Wave write) → exit non-zero so
# automation can detect partial apply. Wave-only failures (wave_failed) also
# count as non-zero because the apply was not fully clean.
WAVE_FAILED_COUNT=$(grep -c '"result":"wave_failed"' "$AUDIT_NDJSON" 2>/dev/null || true)
if [[ ${#FAILED_ITEMS[@]} -gt 0 || "${WAVE_FAILED_COUNT:-0}" -gt 0 ]]; then
  exit 1
fi
