#!/usr/bin/env bash
# fetch-data.sh — pull backlog issues + Project v2 items + iteration config +
# sub-issue relations into $WORKDIR JSON files. Single source for SKILL.md
# stage 1.
#
# Inputs (env):
#   WORKDIR     — destination dir (must exist)
#   INCLUDE_P2  — "true" adds pri-p2 to the input label set
#   INCLUDE_P3  — "true" adds pri-p3
#
# Requires `gh` on PATH. All gh calls go through $PATH so smoke can PATH-stub gh.
#
# Outputs (files in $WORKDIR): issues.json items.json iter-config.json sub-issues.json
set -euo pipefail

: "${WORKDIR:?WORKDIR required}"

# 1.1 输入集：pri-missing 哨兵 + P0 + P1 (默认) + 可选 P2/P3。
# pri-missing 是 backlog workflow 自动打的哨兵（漏标 priority 的 issue 不能被忽略）。
# GitHub label search 是 AND 语义，需逐 label 拉。
PRI_LABELS=(pri-missing pri-p0 pri-p1)
[[ "${INCLUDE_P2:-}" == "true" ]] && PRI_LABELS+=(pri-p2)
[[ "${INCLUDE_P3:-}" == "true" ]] && PRI_LABELS+=(pri-p3)

echo "[]" > "$WORKDIR/issues.json"
for pri_label in "${PRI_LABELS[@]}"; do
  gh issue list --repo ghbvf/gocell --label backlog --label "$pri_label" \
    --state open --json number,title,labels,createdAt,body,url --limit 200 \
    > "$WORKDIR/issues-$pri_label.json"
  jq -s '(.[0] + .[1]) | unique_by(.number)' \
    "$WORKDIR/issues.json" "$WORKDIR/issues-$pri_label.json" \
    > "$WORKDIR/issues.tmp" && mv "$WORKDIR/issues.tmp" "$WORKDIR/issues.json"
done
echo "INFO: input set = $(jq 'length' "$WORKDIR/issues.json") issues (priorities: ${PRI_LABELS[*]})" >&2

# 1.2 Project v2 items（GraphQL paginated；gh project item-list 不返回 iteration value / issue.state）
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
        estimate: fieldValueByName(name:"Estimate") {
          ... on ProjectV2ItemFieldSingleSelectValue { name }
        }
      }
    }
  }}
}' | jq -s '[.[].data.user.projectV2.items.nodes[]]' > "$WORKDIR/items.json"
echo "INFO: $(jq 'length' "$WORKDIR/items.json") Project v2 items loaded" >&2

# 1.3 Iteration 配置（当前所有 iteration option）
gh api graphql -f query='query {
  user(login:"ghbvf"){ projectV2(number:3){
    field(name:"Iteration"){ ... on ProjectV2IterationField {
      configuration { duration startDay iterations { id title startDate duration } }
    }}
  }}
}' > "$WORKDIR/iter-config.json"

# 1.4 Sub-issue 关系（GraphQL preview API）。账号/repo 未启用时返回 errors：
# 显式 WARN，不静默吞错（保留降级 `{}`，让 agent 当作"无 sub-issue 数据"运行）。
if ! gh api graphql -H "GraphQL-Features: sub_issues" -f query='query {
  repository(owner:"ghbvf",name:"gocell"){
    issues(first:100, labels:["bundle-parent"], states:OPEN){
      nodes {
        number title state
        subIssuesSummary { total completed percentCompleted }
        subIssues(first:50){ nodes { number title state } }
      }
    }
  }
}' > "$WORKDIR/sub-issues.json" 2>"$WORKDIR/sub-issues.err"; then
  echo "WARN: sub-issue GraphQL query failed; sub-issue data unavailable for this run" >&2
  echo "WARN: gh stderr:" >&2
  sed 's/^/  /' "$WORKDIR/sub-issues.err" >&2
  echo '{}' > "$WORKDIR/sub-issues.json"
fi
