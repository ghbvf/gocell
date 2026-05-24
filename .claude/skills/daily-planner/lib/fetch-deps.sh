#!/usr/bin/env bash
# fetch-deps.sh — pull the native blocked-by dependency DAG for input issues
# into $WORKDIR/deps.json. Single source for SKILL.md stage 1.5.
#
# Inputs (env):
#   WORKDIR — dir containing issues.json; deps.json written here
#
# Requires `gh` on PATH. GraphQL field `blockedBy` is a formal API (no preview
# header). schema: { "<issue_number_string>": { "blocked_by": [<int>,...] }, ... };
# only issues with in-edges appear; empty graph = {}.
#
# 降级策略：仅真·瞬态 API 错误才降级，loud WARN + [DEP DATA UNAVAILABLE] 标记，
# 禁 silent {}（区别于 sub-issues 的静默 fallback）。
set -euo pipefail

: "${WORKDIR:?WORKDIR required}"

DEPS_JSON="{}"
DEP_FETCH_FAILED=false
issue_count_for_deps=$(jq 'length' "$WORKDIR/issues.json")
if [[ "$issue_count_for_deps" -gt 0 ]]; then
  while IFS= read -r inum; do
    if ! blocked_raw=$(gh api graphql \
        -f query='query($num: Int!) {
          repository(owner:"ghbvf", name:"gocell") {
            issue(number: $num) {
              blockedBy(first: 20) { nodes { number } }
            }
          }
        }' -F num="$inum" 2>/tmp/deps-err-"$inum"); then
      echo "WARN: blocked-by query failed for issue #$inum (transient?); detail:" >&2
      sed 's/^/  /' /tmp/deps-err-"$inum" >&2
      DEP_FETCH_FAILED=true
      continue
    fi
    blocked_nums=$(echo "$blocked_raw" | jq '[.data.repository.issue.blockedBy.nodes[].number]')
    if [[ "$(echo "$blocked_nums" | jq 'length')" -gt 0 ]]; then
      DEPS_JSON=$(echo "$DEPS_JSON" | jq --arg n "$inum" --argjson b "$blocked_nums" \
        '. + {($n): {"blocked_by": $b}}')
    fi
  done < <(jq -r '.[].number' "$WORKDIR/issues.json")
fi
if $DEP_FETCH_FAILED; then
  echo "WARN: one or more blocked-by queries failed; deps.json may be incomplete [DEP DATA UNAVAILABLE]" >&2
fi
echo "$DEPS_JSON" > "$WORKDIR/deps.json"
if $DEP_FETCH_FAILED; then
  echo "INFO: [INCOMPLETE] deps.json: $(jq 'keys | length' "$WORKDIR/deps.json") issues with blockers (one or more queries failed; see WARN above)" >&2
else
  echo "INFO: deps.json: $(jq 'keys | length' "$WORKDIR/deps.json") issues with blockers" >&2
fi
