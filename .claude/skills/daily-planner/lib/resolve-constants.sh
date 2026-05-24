#!/usr/bin/env bash
# resolve-constants.sh — token-scope fail-fast + Project node / Iteration field
# resolution + date/weekend mode. Single source for SKILL.md stage 0 (token,
# project IDs, date/mode); the Wave field ids come from resolve-wave-fields.sh
# and the apply-only wave fail-closed guard stays inline in SKILL.md (it reads
# WAVE_* env that only exists after resolve-wave-fields runs).
#
# Inputs (env):
#   DATE          — target date YYYY-MM-DD (optional; default today)
#   FORCE_WEEKEND — "true" forces weekend mode (mutually exclusive with FORCE_WEEKDAY)
#   FORCE_WEEKDAY — "true" forces weekday mode
#
# Requires `gh` on PATH (project view + field-list + auth status).
#
# Output: eval-able `export VAR=value` lines on stdout (printf %q quoted):
#   PROJECT_NODE_ID ITERATION_FIELD_ID FIELD_LIST_JSON
#   DATE DOW IS_WEEKEND WAVE_COUNT WAVE_SIZE
# FIELD_LIST_JSON carries arbitrary GitHub-API JSON (field names are owner-controlled);
# `printf %q` fully bash-quotes it so the caller's `eval "$(...)"` is injection-safe.
#
# Exit non-zero (errors to stderr) on: not logged in / missing project scope /
#   unresolved project|iteration id / --weekend + --weekday both set.
set -euo pipefail

# 0.1 Token scope. Distinguish not-logged-in (gh auth status fails) vs missing
# scope (succeeds but grep misses). Loose regex stays robust across gh versions.
if ! AUTH_STATUS=$(gh auth status 2>&1); then
  echo "ERROR: gh auth status failed; run: gh auth login" >&2
  echo "$AUTH_STATUS" >&2
  exit 1
fi
if ! grep -qiE "scopes:.*\bproject\b" <<<"$AUTH_STATUS"; then
  echo "ERROR: token missing 'project' scope; run: gh auth refresh -s project" >&2
  exit 1
fi

# 0.2 Project node + Iteration field ID
# `// empty` so a missing key yields "" (caught by -z below) rather than the
# literal string "null" that `jq -r .id` prints for absent keys (which passes -z).
PROJECT_NODE_ID=$(gh project view 3 --owner ghbvf --format json | jq -r '.id // empty')
FIELD_LIST_JSON=$(gh project field-list 3 --owner ghbvf --format json)
ITERATION_FIELD_ID=$(jq -r '[.fields[] | select(.name=="Iteration").id] | first // empty' <<<"$FIELD_LIST_JSON")
[[ -z "$PROJECT_NODE_ID" || -z "$ITERATION_FIELD_ID" ]] && {
  echo "ERROR: failed to resolve PROJECT_NODE_ID or ITERATION_FIELD_ID" >&2
  exit 1
}

# 0.3 日期 + 模式（weekday/weekend）。python3 用 sys.argv 传 $DATE 防 shell 注入
DATE="${DATE:-$(date +%Y-%m-%d)}"
DOW=$(python3 -c 'import sys,datetime; print(datetime.date.fromisoformat(sys.argv[1]).isoweekday())' "$DATE")

# --weekend / --weekday 互斥（同传 fail-fast 不静默吞错）
if [[ "${FORCE_WEEKEND:-}" == "true" && "${FORCE_WEEKDAY:-}" == "true" ]]; then
  echo "ERROR: --weekend and --weekday are mutually exclusive" >&2
  exit 1
fi
if [[ "${FORCE_WEEKEND:-}" == "true" ]]; then IS_WEEKEND=true
elif [[ "${FORCE_WEEKDAY:-}" == "true" ]]; then IS_WEEKEND=false
elif [[ "$DOW" -ge 6 ]]; then IS_WEEKEND=true
else IS_WEEKEND=false
fi

WAVE_SIZE=5  # 固定，Miller's Law
WAVE_COUNT=$([[ "$IS_WEEKEND" == "true" ]] && echo 4 || echo 2)

printf 'export PROJECT_NODE_ID=%q\n'    "$PROJECT_NODE_ID"
printf 'export ITERATION_FIELD_ID=%q\n' "$ITERATION_FIELD_ID"
printf 'export FIELD_LIST_JSON=%q\n'    "$FIELD_LIST_JSON"
printf 'export DATE=%q\n'               "$DATE"
printf 'export DOW=%q\n'                "$DOW"
printf 'export IS_WEEKEND=%q\n'         "$IS_WEEKEND"
printf 'export WAVE_COUNT=%q\n'         "$WAVE_COUNT"
printf 'export WAVE_SIZE=%q\n'          "$WAVE_SIZE"
