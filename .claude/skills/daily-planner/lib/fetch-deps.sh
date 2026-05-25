#!/usr/bin/env bash
# fetch-deps.sh — pull the native blocked-by dependency DAG for input issues
# into $WORKDIR/deps.json. Single source for SKILL.md stage 1.5.
#
# Inputs (env):
#   WORKDIR — dir containing issues.json; deps.json written here
#
# Requires `gh` + `jq` on PATH (preflight via _preflight.sh). GraphQL field
# `blockedBy` is a formal API (no preview header). schema:
# { "<issue_number_string>": { "blocked_by": [<int>,...] }, ... };
# only issues with in-edges appear; empty graph = {}.
#
# Fail-closed: blocked_by 是产出可执行计划的强依赖数据源。任何 blocked-by 查询失败
# (瞬态 OR 结构性) 都 exit 非零、不写 deps.json——绝不以缺边的不完整 DAG 继续，
# 否则下游 STEP 4 拓扑排序会把 dependent 误排到 blocker 之前。瞬态失败由调用方重跑
# (planner 全程只读、幂等，重跑成本极低)。无降级路径、无 [DEP DATA UNAVAILABLE]。
set -euo pipefail

_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=_preflight.sh
source "$_LIB_DIR/_preflight.sh"
require_cmds gh jq

: "${WORKDIR:?WORKDIR required}"

DEPS_JSON="{}"
issue_count_for_deps=$(jq 'length' "$WORKDIR/issues.json")
if [[ "$issue_count_for_deps" -gt 0 ]]; then
  while IFS= read -r inum; do
    # Per-issue stderr capture inside $WORKDIR (700-perm tmp), unique name —
    # avoids fixed /tmp/deps-err-$inum collisions across concurrent runs.
    _err_file=$(mktemp "$WORKDIR/deps-err-XXXXXX")
    if ! blocked_raw=$(gh api graphql \
        -f query='query($num: Int!) {
          repository(owner:"ghbvf", name:"gocell") {
            issue(number: $num) {
              blockedBy(first: 20) { nodes { number } }
            }
          }
        }' -F num="$inum" 2>"$_err_file"); then
      echo "ERROR: blocked-by query failed for issue #$inum — failing closed (will not emit a partial dependency graph). detail:" >&2
      sed 's/^/  /' "$_err_file" >&2
      rm -f "$_err_file"
      exit 1
    fi
    rm -f "$_err_file"
    blocked_nums=$(echo "$blocked_raw" | jq '[.data.repository.issue.blockedBy.nodes[].number]')
    if [[ "$(echo "$blocked_nums" | jq 'length')" -gt 0 ]]; then
      DEPS_JSON=$(echo "$DEPS_JSON" | jq --arg n "$inum" --argjson b "$blocked_nums" \
        '. + {($n): {"blocked_by": $b}}')
    fi
  done < <(jq -r '.[].number' "$WORKDIR/issues.json")
fi
echo "$DEPS_JSON" > "$WORKDIR/deps.json"
echo "INFO: deps.json: $(jq 'keys | length' "$WORKDIR/deps.json") issues with blockers" >&2
