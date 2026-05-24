---
name: daily-planner
description: "每日 backlog → Project v2 Iteration 调度（wave 模型 + carry-over + 周末自动识别）。默认 dry-run；--apply 才写入。仅用户显式 /daily-planner 触发，避免 AI 主动调用对真实 Project v2 写入。"
argument-hint: "[--apply] [--date=YYYY-MM-DD] [--capacity=N] [--weekend|--weekday] [--include-p2] [--include-p3]"
allowed-tools: [Bash, Read, Write, Agent]
disable-model-invocation: true
---

# Daily Planner Skill

调度 backlog → 当日 Project v2 Iteration（每日 1-day option）。**默认 dry-run**；`--apply` 才真写入。Backlog 真值源见 `docs/backlog.md`。

## 设计原则

- **Project v2 是真值源**：当日排期写入 Iteration field value；不在本地长期归档 brief / audit log
- **默认输入集 = P0 + P1**：单人项目 P3 大头不参与每日排期；`--include-p2` / `--include-p3` 可扩
- **Wave 模型**：工作日 2 wave × 5 issue / 周末 4 wave × 5 issue；wave_count × 5 是硬上限，∑Cx ≤ capacity 是软警告
- **Carry-over 硬规则**：昨日 iteration 内 issue.state≠CLOSED 且 Status≠Done 的项**优先占 Wave 1 头部**
- **字段 ID 动态查询**：每次启动从 `gh` 拉真实 ID，不维护硬编副本

## 参数

| Flag | 默认 | 含义 |
|------|------|------|
| `--apply` | off | 写入 Project v2 Iteration 字段（含 carry-over move）；未传则纯 dry-run |
| `--date=YYYY-MM-DD` | today | 目标排期日期 |
| `--capacity=N` | 30 工作日 / 60 周末 | ∑Cx 软警告阈值（Cx1=1/Cx2=2/Cx3=3/Cx4=4） |
| `--weekend` / `--weekday` | 自动 `date +%u` | 强制模式覆盖 |
| `--include-p2` | off | 加入 P2 issue 作输入 |
| `--include-p3` | off | 加入 P3 issue（极少需要；P3 默认 nice-to-have） |

---

## 阶段 0：常量动态查询 + token scope fail-fast + 模式探测

```bash
# 0.1 Token scope（无 project scope → 直接退出；删除原降级模式）
if ! gh auth status 2>&1 | grep -qiE "scopes:.*\bproject\b"; then
  echo "ERROR: token missing 'project' scope; run: gh auth refresh -s project" >&2
  exit 1
fi

# 0.2 Project node + Iteration field ID
PROJECT_NODE_ID=$(gh project view 3 --owner ghbvf --format json | jq -r '.id')
ITERATION_FIELD_ID=$(gh project field-list 3 --owner ghbvf --format json \
  | jq -r '.fields[] | select(.name=="Iteration").id')
[[ -z "$PROJECT_NODE_ID" || -z "$ITERATION_FIELD_ID" ]] && {
  echo "ERROR: failed to resolve PROJECT_NODE_ID or ITERATION_FIELD_ID" >&2
  exit 1
}

# 0.3 日期 + 模式（weekday/weekend）。用 python3 跨 macOS BSD / Linux GNU date 兼容
DATE="${DATE:-$(date +%Y-%m-%d)}"
DOW=$(python3 -c "from datetime import date; print(date.fromisoformat('$DATE').isoweekday())")
# 自动判定，flag 覆盖
if [[ "$FORCE_WEEKEND" == "true" ]]; then IS_WEEKEND=true
elif [[ "$FORCE_WEEKDAY" == "true" ]]; then IS_WEEKEND=false
elif [[ "$DOW" -ge 6 ]]; then IS_WEEKEND=true
else IS_WEEKEND=false
fi

if [[ "$IS_WEEKEND" == "true" ]]; then
  WAVE_COUNT=4
  CAPACITY_DEFAULT=60
else
  WAVE_COUNT=2
  CAPACITY_DEFAULT=30
fi
CAPACITY="${CAPACITY:-$CAPACITY_DEFAULT}"
WAVE_SIZE=5  # 固定，Miller's Law
ISSUE_CAP=$(( WAVE_COUNT * WAVE_SIZE ))
```

## 阶段 1：拉取数据

```bash
WORKDIR="$(mktemp -d -t daily-planner.XXXXXX)"
chmod 700 "$WORKDIR"

# 1.1 输入集：P0 + P1 (默认) + 可选 P2/P3
PRI_TERMS='label:pri-p0,label:pri-p1'
[[ "$INCLUDE_P2" == "true" ]] && PRI_TERMS="$PRI_TERMS,label:pri-p2"
[[ "$INCLUDE_P3" == "true" ]] && PRI_TERMS="$PRI_TERMS,label:pri-p3"

# 用多次 gh issue list 联合（GitHub label search 是 AND 语义，需逐 label 拉再合并）
echo "[]" > "$WORKDIR/issues.json"
for term in ${PRI_TERMS//,/ }; do
  pri_label="${term#label:}"
  gh issue list --repo ghbvf/gocell --label backlog --label "$pri_label" \
    --state open --json number,title,labels,createdAt,body,url --limit 200 \
    > "$WORKDIR/issues-$pri_label.json"
  jq -s '.[0] + .[1]' "$WORKDIR/issues.json" "$WORKDIR/issues-$pri_label.json" \
    > "$WORKDIR/issues.tmp" && mv "$WORKDIR/issues.tmp" "$WORKDIR/issues.json"
done
echo "INFO: input set = $(jq 'length' "$WORKDIR/issues.json") issues (priority labels: $PRI_TERMS)"

# 1.2 Project v2 items（用 GraphQL paginated，因为 gh project item-list 不返回 iteration value 和 issue.state）
# 输出 NDJSON 多页，jq -s 合并所有页的 nodes
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
echo "INFO: $(jq 'length' "$WORKDIR/items.json") Project v2 items loaded"

# 1.3 Iteration 配置（拿当前所有 iteration option）
gh api graphql -f query='query {
  user(login:"ghbvf"){ projectV2(number:3){
    field(name:"Iteration"){ ... on ProjectV2IterationField {
      configuration { duration startDay iterations { id title startDate duration } }
    }}
  }}
}' > "$WORKDIR/iter-config.json"

# 1.4 Sub-issue 关系（GraphQL 原生）
gh api graphql -H "GraphQL-Features: sub_issues" -f query='query {
  repository(owner:"ghbvf",name:"gocell"){
    issues(first:100, labels:["bundle-parent"], states:OPEN){
      nodes {
        number title state
        subIssuesSummary { total completed percentCompleted }
        subIssues(first:50){ nodes { number title state } }
      }
    }
  }
}' > "$WORKDIR/sub-issues.json" 2>/dev/null || echo '{}' > "$WORKDIR/sub-issues.json"
```

## 阶段 2：确保 today iteration 存在

```bash
# 2.1 推断 $DATE 对应的 iteration option（注意 pipe 后 root context 丢失，用 `as $it` 绑定保留）
TODAY_ITERATION_ID=$(jq -r --arg d "$DATE" '
  .data.user.projectV2.field.configuration.iterations[]
  | . as $it
  | select($it.startDate <= $d
           and ($d < ((($it.startDate | strptime("%Y-%m-%d") | mktime) + ($it.duration * 86400))
                       | strftime("%Y-%m-%d")))).id
' "$WORKDIR/iter-config.json")

# 2.2 缺失则用 GraphQL mutation append 一个 1-day iteration option 覆盖 $DATE
if [[ -z "$TODAY_ITERATION_ID" ]]; then
  EXISTING=$(jq -c '.data.user.projectV2.field.configuration.iterations | map({title, startDate, duration})' "$WORKDIR/iter-config.json")
  NEW_TITLE="Iteration $(jq 'length + 1' <<<"$EXISTING")"
  ALL_ITERS=$(jq -c --arg title "$NEW_TITLE" --arg d "$DATE" \
               '. + [{title: $title, startDate: $d, duration: 1}]' <<<"$EXISTING")

  # 类型名 ProjectV2Iteration 是 INPUT_OBJECT（GraphQL 2026-05-24 introspection 实测）
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
    -f iters="$ALL_ITERS" \
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

# 2.3 推断昨日 iteration ID（carry-over 源）
YESTERDAY=$(python3 -c "from datetime import date,timedelta; print(date.fromisoformat('$DATE') - timedelta(days=1))")
YESTERDAY_ITERATION_ID=$(jq -r --arg d "$YESTERDAY" '
  .data.user.projectV2.field.configuration.iterations[]
  | . as $it
  | select($it.startDate == $d).id
' "$WORKDIR/iter-config.json")
[[ -z "$YESTERDAY_ITERATION_ID" ]] && echo "WARN: yesterday iteration not found ($YESTERDAY); carry-over disabled" >&2
```

## 阶段 3：派发 daily-planner agent 评分排序

```
PLAN_PATH="$WORKDIR/plan.json"

Agent(
  description: "Score backlog + carry-over + wave grouping",
  subagent_type: "daily-planner",
  prompt: f"""
    Constants:
      PROJECT_NODE_ID = {PROJECT_NODE_ID}
      ITERATION_FIELD_ID = {ITERATION_FIELD_ID}
      TODAY_ITERATION_ID = {TODAY_ITERATION_ID}
      YESTERDAY_ITERATION_ID = {YESTERDAY_ITERATION_ID}  # 可能空（首日 / 假期跳过后）
      DATE = {DATE}                                       # YYYY-MM-DD
      IS_WEEKEND = {IS_WEEKEND}                           # true|false
      WAVE_COUNT = {WAVE_COUNT}                           # 2|4
      WAVE_SIZE = 5
      ISSUE_CAP = {ISSUE_CAP}                             # WAVE_COUNT * 5
      CAPACITY = {CAPACITY}                               # ∑Cx 软警告阈值
      MODE = {"apply" if APPLY else "dry-run"}

    Data files (Read these):
      {WORKDIR}/issues.json        — backlog 池 (P0/P1 默认；P2/P3 视 flag)
      {WORKDIR}/items.json         — Project v2 items（含 iteration / status / state）
      {WORKDIR}/iter-config.json   — Iteration 配置
      {WORKDIR}/sub-issues.json    — sub-issue 关系（GraphQL 原生）

    Tasks:
      1. Read all 4 files
      2. Compute carry-over: items.json 中 iteration == YESTERDAY_ITERATION_ID 且
         (issue.state == "OPEN") AND (status != "Done") 的 issue → 加入 carry-over 列表
      3. Score P0/P1 池 per WSJF 简化版（详 agent.md §排序算法）
      4. Wave 调度：
         - Wave 1 头部填 carry-over（按原 WSJF score 排序）
         - 剩余 Wave 1 / Wave 2+ 填 新 issue 按分数降序
         - 硬约束: 总 issue 数 ≤ ISSUE_CAP
         - 软约束: ∑Cx 超 CAPACITY 时在 Warnings 标注，不阻塞
      5. Emit brief markdown to STDOUT（详 agent.md §输出格式）
      6. Write plan.json to {PLAN_PATH}（详 agent.md §输出 #2）
  """
)
```

## 阶段 4：Apply 分支（仅 `--apply`）

```bash
[[ "$APPLY" != "true" ]] && {
  echo "INFO: dry-run 模式，跳过 Project v2 写入。加 --apply 真写。" >&2
  exit 0
}

# 4.1 plan.json schema 校验
jq -e '
  (type == "array") and
  all(.[]; (.item_id | type == "string") and (.target_iteration_id | type == "string") and (.action | type == "string"))
' "$WORKDIR/plan.json" > /dev/null || {
  echo "ERROR: plan.json schema invalid" >&2
  exit 1
}

# 4.2 逐项写入（agent 已用 action="skip" 标注幂等项）
APPLIED=0; SKIPPED=0; FAILED_ITEMS=()
while read -r row; do
  action=$(jq -r '.action' <<<"$row")
  item_id=$(jq -r '.item_id' <<<"$row")
  tgt=$(jq -r '.target_iteration_id' <<<"$row")
  cur=$(jq -r '.current_iteration_id // ""' <<<"$row")

  if [[ "$action" == "skip" ]]; then
    SKIPPED=$((SKIPPED+1))
    continue
  fi

  if gh project item-edit \
       --project-id "$PROJECT_NODE_ID" --id "$item_id" \
       --field-id "$ITERATION_FIELD_ID" --iteration-id "$tgt" >/dev/null; then
    APPLIED=$((APPLIED+1))
  else
    FAILED_ITEMS+=("$item_id|$cur|$tgt")
  fi
done < <(jq -c '.[]' "$WORKDIR/plan.json")
```

## 阶段 5：报告

主 LLM 把以下信息综合到对话回应：

1. **Brief**（阶段 3 agent stdout 全文，含 Wave N 章节）
2. **Audit 行**（仅 apply 模式）：`[audit] $(date -u +%FT%TZ) date=$DATE mode=$([[ $IS_WEEKEND == true ]] && echo weekend || echo weekday) wave_count=$WAVE_COUNT capacity=$CAPACITY applied=$APPLIED skipped=$SKIPPED failed=${#FAILED_ITEMS[@]}`
3. **失败回滚命令**（仅有失败项时；**逐条审查后再执行**，不要批量复制粘贴）：
   ```
   # 以下命令仅对失败项有效，逐条确认对应项确需回滚再执行
   gh project item-edit --project-id <PID> --id <ITEM> --field-id <FID> --iteration-id <OLD_ITER_ID>
   ...
   ```
4. **WORKDIR 清理提示**：`rm -rf $WORKDIR`

## 约束

- 所有 `gh` 命令 `dangerouslyDisableSandbox: true`
- **默认 dry-run；`--apply` 必须显式 opt-in**
- 只写 Project v2 Iteration field value + Iteration field configuration（append-only 追加 daily option）；**不动 Status / Estimate / labels / issue body / comment / title**
- 不创建 / 修改 / 关闭 issue
- 不修改代码、不跑 build/test
- Apply 失败不自动回滚（输出回滚命令清单交人决策）
- **不在本地长期落盘**：brief = stdout，plan.json = `$WORKDIR/` mktemp 临时
- 字段 ID 每次启动从 `gh` 查询，不硬编（避免 Project v2 迁移后双源漂移）
- token 无 `project` scope → fail-fast 退出（无云沙箱降级）
