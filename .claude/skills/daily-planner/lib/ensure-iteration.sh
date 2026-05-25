#!/usr/bin/env bash
# ensure-iteration.sh — resolve today's iteration option (creating it in apply
# mode if absent) + yesterday's iteration (carry-over source). Single source for
# SKILL.md stage 2.
#
# Inputs (env):
#   WORKDIR            — dir containing iter-config.json
#   DATE               — target date YYYY-MM-DD
#   APPLY              — "true" allows the field-configuration mutation; dry-run never mutates
#   ITERATION_FIELD_ID — required only on the apply-mode create path
#
# Requires `jq` + `python3` on PATH (preflight via _preflight.sh; always used).
# `gh` is additionally required only when creating an iteration (apply mode +
# missing option) — that path is not preflighted here (it fails at its callsite).
#
# Output: eval-able `export VAR=value` lines on stdout:
#   TODAY_ITERATION_ID YESTERDAY_ITERATION_ID CARRY_OVER_DISABLED
#   DP_ABORT_DRYRUN (="true" only when dry-run aborts because today's iteration is missing)
#
# **写入 Project v2 是 apply-only**：dry-run 缺 iteration 时给 would-create 摘要并
# 发 DP_ABORT_DRYRUN=true（调用方据此停止），绝不执行 updateProjectV2Field。
set -euo pipefail

_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=_preflight.sh
source "$_LIB_DIR/_preflight.sh"
# gh is apply-create-only (see header); jq + python3 run on every path.
require_cmds jq python3

: "${WORKDIR:?WORKDIR required}"
: "${DATE:?DATE required}"

ITER_CONFIG_FILE="$WORKDIR/iter-config.json"

# 2.1 推断 $DATE 对应的 iteration option。用 `as $it` 绑定当前 iteration，确保算术
# 子表达式仍能引用其 startDate / duration（select() 内嵌套 pipe 会覆盖 .）。
TODAY_ITERATION_ID=$(jq -r --arg d "$DATE" '
  .data.user.projectV2.field.configuration.iterations[]
  | . as $it
  | select($it.startDate <= $d
           and ($d < ((($it.startDate | strptime("%Y-%m-%d") | mktime) + ($it.duration * 86400))
                       | strftime("%Y-%m-%d")))).id
' "$ITER_CONFIG_FILE")

if [[ -z "$TODAY_ITERATION_ID" ]]; then
  if [[ "${APPLY:-}" != "true" ]]; then
    echo "WARN: [ACTION REQUIRED] today iteration for $DATE not found in Iteration field." >&2
    echo "WARN: [DRY-RUN] planning aborted — TODAY_ITERATION_ID is required to score wave layout." >&2
    echo "WARN: re-run with --apply to create today's iteration option and proceed." >&2
    # printf %q for consistency with the success-path emit below (single source of
    # quoting style); values are literal here but the form stays uniform.
    printf 'export DP_ABORT_DRYRUN=%q\n' "true"
    printf 'export TODAY_ITERATION_ID=%q\n' ""
    exit 0
  fi

  : "${ITERATION_FIELD_ID:?ITERATION_FIELD_ID required for apply-mode iteration create}"

  EXISTING=$(jq -c '.data.user.projectV2.field.configuration.iterations | map({title, startDate, duration})' "$ITER_CONFIG_FILE")
  NEW_TITLE="Iteration $(jq 'length + 1' <<<"$EXISTING")"  # 命名仅展示用，唯一 ID 由 GitHub 生成
  ALL_ITERS=$(jq -c --arg title "$NEW_TITLE" --arg d "$DATE" \
               '. + [{title: $title, startDate: $d, duration: 1}]' <<<"$EXISTING")

  # iterationConfiguration.startDate/.duration 是 field-level cycle 锚点；iterations
  # 数组每项 startDate/duration 决定 daily 边界。用 EXISTING 第一项作 cycle 锚点。
  # ProjectV2Iteration 是 INPUT_OBJECT；`-F` 才把 value 当 raw JSON/array 传入。
  gh api graphql -f query='
    mutation($fid: ID!, $start: Date!, $dur: Int!, $iters: [ProjectV2Iteration!]!) {
      updateProjectV2Field(input: {
        fieldId: $fid,
        iterationConfiguration: { startDate: $start, duration: $dur, iterations: $iters }
      }) { projectV2Field { ... on ProjectV2IterationField {
        configuration { iterations { id title startDate duration } }
      }}}
    }' \
    -f fid="$ITERATION_FIELD_ID" \
    -f start="$(jq -r '.[0].startDate' <<<"$ALL_ITERS")" \
    -F dur="$(jq -r '.[0].duration' <<<"$ALL_ITERS")" \
    -F iters="$ALL_ITERS" \
    > "$WORKDIR/iter-config-after.json"

  TODAY_ITERATION_ID=$(jq -r --arg d "$DATE" '
    .data.updateProjectV2Field.projectV2Field.configuration.iterations[]
    | . as $it | select($it.startDate == $d).id
  ' "$WORKDIR/iter-config-after.json")
  [[ -z "$TODAY_ITERATION_ID" ]] && {
    echo "ERROR: failed to create iteration for $DATE" >&2
    exit 1
  }
  echo "INFO: created new daily iteration for $DATE -> $TODAY_ITERATION_ID" >&2
fi

# 2.3 推断昨日 iteration ID（carry-over 源）。python3 用 sys.argv 传 $DATE 防注入
YESTERDAY=$(python3 -c 'import sys,datetime; print(datetime.date.fromisoformat(sys.argv[1]) - datetime.timedelta(days=1))' "$DATE")
YESTERDAY_ITERATION_ID=$(jq -r --arg d "$YESTERDAY" '
  .data.user.projectV2.field.configuration.iterations[]
  | . as $it
  | select($it.startDate == $d).id
' "$ITER_CONFIG_FILE")
CARRY_OVER_DISABLED=false
if [[ -z "$YESTERDAY_ITERATION_ID" ]]; then
  CARRY_OVER_DISABLED=true
  echo "WARN: yesterday iteration not found ($YESTERDAY); carry-over disabled" >&2
fi

printf 'export TODAY_ITERATION_ID=%q\n'     "$TODAY_ITERATION_ID"
printf 'export YESTERDAY_ITERATION_ID=%q\n' "$YESTERDAY_ITERATION_ID"
printf 'export CARRY_OVER_DISABLED=%q\n'    "$CARRY_OVER_DISABLED"
