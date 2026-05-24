#!/usr/bin/env bash
# Resolve the Wave single-select field id + per-wave option ids from the JSON
# emitted by `gh project field-list <n> --owner <o> --format json`.
#
# Input:  FIELD_LIST_JSON env — the raw field-list JSON.
# Output: eval-able `export VAR=value` lines on stdout. Every value is a clean
#         single-line string (printf %q quoted), safe to pass to `gh ... --field-id`.
#
# WHY collect-then-first (not the obvious `select(...).id // ""`):
#   `jq -r '.fields[] | select(.name=="Wave").id // ""'` evaluates `// ""` PER
#   ELEMENT, so every non-Wave field emits an empty line. The captured shell var
#   then carries N-1 leading newlines and newline-poisons the id handed to
#   `gh project item-edit --field-id "$WAVE_FIELD_ID"` (mutation fails / malforms).
#   Collecting into an array first (`[ ... ] | first // ""`) emits exactly one
#   value. Same jq-precedence class as the PR #926 scoping bug; surfaced by the
#   SMOKE_LIVE Part B run and pinned by the offline resolve-wave-fields cases.
#
# This is the single source for Wave field resolution: SKILL.md stage 0 sources
# it via `eval "$(... resolve-wave-fields.sh)"` and test/smoke.sh exercises it
# against test/fixtures/field-list.json — neither re-implements the jq.
set -euo pipefail

fl="${FIELD_LIST_JSON:?FIELD_LIST_JSON required}"

wave_field_id=$(jq -r '[.fields[] | select(.name=="Wave").id] | first // ""' <<<"$fl")
wave_option_ids=$(jq -r '[.fields[] | select(.name=="Wave") | .options[]?.id] | join(",")' <<<"$fl")

printf 'export WAVE_FIELD_ID=%q\n' "$wave_field_id"
printf 'export WAVE_OPTION_IDS=%q\n' "$wave_option_ids"

# Per-wave option id by NAME match ("Wave 1".."Wave 4") — robust to UI option
# reordering (vs array index). Same collect-then-first guard.
for n in 1 2 3 4; do
  oid=$(jq -r --arg nm "Wave $n" \
    '[.fields[] | select(.name=="Wave") | .options[]? | select(.name==$nm).id] | first // ""' \
    <<<"$fl")
  printf 'export WAVE_OPTION_ID_WAVE%s=%q\n' "$n" "$oid"
done
