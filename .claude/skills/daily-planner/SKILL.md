---
name: daily-planner
description: "每日迭代调度（默认 dry-run；--apply 才写入 Project v2 Iteration 字段）。触发：'今日计划' / 'daily plan' / 'standup' / '排今天的活' / 'iteration 排期'。"
argument-hint: "[--apply] [--date=YYYY-MM-DD] [--capacity=N]"
allowed-tools: [Bash, Read, Write, Agent]
---

# Daily Planner Skill

调度 backlog → 当日 Project v2 Iteration。**默认 dry-run**（只输出 brief 到对话窗），`--apply` 才真写入。Backlog 真值源见 `docs/backlog.md`。

## 设计原则

- **Project v2 是唯一真值源**：本 skill 不在本地长期归档 brief / audit log。当日排期写入 Project v2 Iteration 字段（每天一个 1-day iteration），brief markdown 直接显示在对话窗，运维与历史回溯走 Project v2 UI + `git log`。
- **临时中间产物落 `/tmp/`**：仅当次 run 用，run 完丢弃；非持久化。
- **字段 ID 动态查询**：skill 启动时用 `gh` 拉取真实 ID 注入 agent prompt，不维护硬编常量副本。

## 参数

| Flag | 默认 | 含义 |
|------|------|------|
| `--apply` | off | 写入 Project v2 Iteration 字段；未传则纯 dry-run |
| `--date=YYYY-MM-DD` | today（系统 TZ）| 目标排期日期 |
| `--capacity=N` | 4 | 当日 ∑Cx 上限（Cx1=1/Cx2=2/Cx3=3/Cx4=4）|

---

## 阶段 0：常量动态查询 + Token scope check

```bash
# 0.1 Token scope 检测（用宽松正则，跨 gh 版本健壮）
if gh auth status 2>&1 | grep -qiE "scopes:.*\bproject\b"; then
  HAS_PROJECT=true
else
  HAS_PROJECT=false
fi

# 0.2 Project node + Iteration field ID（每次启动查，不硬编）
if [[ $HAS_PROJECT == true ]]; then
  PROJECT_NODE_ID=$(gh project view 3 --owner ghbvf --format json | jq -r '.id')
  ITERATION_FIELD_ID=$(gh project field-list 3 --owner ghbvf --format json \
    | jq -r '.fields[] | select(.name=="Iteration").id')
  [[ -z "$PROJECT_NODE_ID" || -z "$ITERATION_FIELD_ID" ]] && {
    echo "ERROR: failed to resolve PROJECT_NODE_ID or ITERATION_FIELD_ID" >&2
    exit 1
  }
fi
```

| 状态 | 模式 |
|------|------|
| `HAS_PROJECT=true` + `--apply` | 完整模式：读 + 写 |
| `HAS_PROJECT=true` + dry-run | 完整模式但不写 |
| `HAS_PROJECT=false` + `--apply` | **报错退出**，提示 `gh auth refresh -s project` |
| `HAS_PROJECT=false` + dry-run | 降级"只读 brief"模式：跳过所有 `gh project` 命令，仅基于 `gh issue list` label 维度排序 |

云沙箱 token 默认无 `project` scope（详见 `docs/backlog.md` §云沙箱查询），降级模式必须可用。

## 阶段 1：拉取数据

```bash
WORKDIR="$(mktemp -d -t daily-planner.XXXXXX)"
chmod 700 "$WORKDIR"          # 限制其他用户可读（issue body 可能含未脱敏字段）
DATE="${DATE:-$(date +%Y-%m-%d)}"
CAPACITY="${CAPACITY:-4}"

# 1.1 Backlog 池
gh issue list --repo ghbvf/gocell --label backlog --state open \
  --json number,title,labels,createdAt,body,url --limit 300 \
  > "$WORKDIR/issues.json"

# 1.2 Project v2 items（仅完整模式）
if [[ $HAS_PROJECT == true ]]; then
  gh project item-list 3 --owner ghbvf --format json --limit 300 \
    > "$WORKDIR/items.json"
fi

# 1.3 Sub-issue 关系（GraphQL 原生 + body grep 双源回落）
gh api graphql -H "GraphQL-Features: sub_issues" -f query='query {
  repository(owner:"ghbvf",name:"gocell"){
    issues(first:100, labels:["bundle-parent","backlog"], states:OPEN){
      nodes {
        number title
        subIssuesSummary { total completed }
        subIssues(first:50){ nodes { number title state } }
      }
    }
  }
}' > "$WORKDIR/sub-issues.json" 2>/dev/null || echo '{}' > "$WORKDIR/sub-issues.json"
```

## 阶段 2：确保 today iteration 存在（仅完整模式）

GoCell Project v2 已配 Iteration 为 1-day duration（详见 `docs/backlog.md` §Daily planning）。skill 检测目标日期是否落在已有 iteration option 上；若无则用 GraphQL 追加一个 1-day iteration 覆盖目标日期，agent 始终拿到有效 `TODAY_ITERATION_ID`。

```bash
if [[ $HAS_PROJECT == true ]]; then
  # 2.1 拉当前 iteration 配置
  gh api graphql -f query='query {
    user(login:"ghbvf"){ projectV2(number:3){
      field(name:"Iteration"){ ... on ProjectV2IterationField {
        configuration { duration startDay iterations { id title startDate duration } }
      }}
    }}
  }' > "$WORKDIR/iter-config.json"

  # 2.2 推断目标日期 iteration（注意 pipe 后 root context 丢失，用 `as $it` 绑定保留访问）
  TODAY_ITERATION_ID=$(jq -r --arg d "$DATE" '
    .data.user.projectV2.field.configuration.iterations[]
    | . as $it
    | select($it.startDate <= $d
             and ($d < ((($it.startDate | strptime("%Y-%m-%d") | mktime) + ($it.duration * 86400))
                         | strftime("%Y-%m-%d")))).id
  ' "$WORKDIR/iter-config.json")

  # 2.3 缺失则新建（appendOnly：保留所有现有 iteration，追加一个 1-day 覆盖 $DATE）
  if [[ -z "$TODAY_ITERATION_ID" ]]; then
    NEW_TITLE="Iteration $(jq '.data.user.projectV2.field.configuration.iterations | length + 1' "$WORKDIR/iter-config.json")"
    EXISTING=$(jq -c '.data.user.projectV2.field.configuration.iterations
                       | map({title, startDate, duration})' "$WORKDIR/iter-config.json")
    ALL_ITERS=$(jq -c --arg title "$NEW_TITLE" --arg d "$DATE" \
                 '. + [{title: $title, startDate: $d, duration: 1}]' <<<"$EXISTING")

    # 类型名 `ProjectV2Iteration` 是 INPUT_OBJECT（GraphQL 2026-05-24 introspection 实测，
    # 非 ProjectV2IterationInput）；字段集见 ProjectV2IterationFieldConfigurationInput
    gh api graphql -f query='
      mutation($fid: ID!, $start: Date!, $dur: Int!, $iters: [ProjectV2Iteration!]!) {
        updateProjectV2Field(input: {
          fieldId: $fid,
          iterationConfiguration: {
            startDate: $start, duration: $dur, iterations: $iters
          }
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
      echo "ERROR: failed to create iteration for $DATE; check rate limit + field permissions" >&2
      exit 1
    }
    echo "INFO: created new daily iteration for $DATE -> $TODAY_ITERATION_ID" >&2
  fi
fi
```

## 阶段 3：派发 daily-planner agent 评分排序

```
PLAN_PATH="$WORKDIR/plan.json"

Agent(
  description: "Score backlog and emit daily brief",
  subagent_type: "daily-planner",
  prompt: f"""
    Constants (injected by skill):
      PROJECT_NODE_ID = {PROJECT_NODE_ID}
      ITERATION_FIELD_ID = {ITERATION_FIELD_ID}
      TODAY_ITERATION_ID = {TODAY_ITERATION_ID}
      DATE = {DATE}
      CAPACITY = {CAPACITY}
      HAS_PROJECT = {HAS_PROJECT}
      MODE = {"apply" if APPLY else "dry-run"}

    Data files (Read these directly):
      {WORKDIR}/issues.json        — backlog gh issue list output
      {WORKDIR}/items.json         — Project v2 items (only if HAS_PROJECT)
      {WORKDIR}/iter-config.json   — Iteration field configuration
      {WORKDIR}/sub-issues.json    — GraphQL sub-issue relations

    Tasks:
      1. Read all 4 files
      2. Score & sort per WSJF 简化版 (see your agent.md §排序算法)
      3. Apply capacity filter ∑Cx <= CAPACITY
      4. Emit brief markdown to STDOUT (use the §输出 章节 fixed format)
      5. Write plan.json to {PLAN_PATH} (see schema in your agent.md §输出 #2)
      6. dry-run mode: write plan.json but with all action="dry-run" (skill skips阶段 4)
  """
)
```

> 主 LLM 派发此 Agent 后会拿到 agent 的 stdout（brief markdown）+ plan.json 已落盘。brief 直接复述给用户在对话窗显示。

## 阶段 4：Apply 分支（仅 `--apply` + `HAS_PROJECT=true`）

```bash
# 4.1 plan.json schema 校验（防 agent 漂移字段名）
jq -e '
  (type == "array") and
  all(.[]; (.item_id | type == "string") and (.target_iteration_id | type == "string") and (.action | type == "string"))
' "$WORKDIR/plan.json" > /dev/null || {
  echo "ERROR: plan.json schema invalid (expect [{item_id,target_iteration_id,action,...}])" >&2
  exit 1
}

# 4.2 逐项写入（agent 已用 action="skip" 标注幂等项）
APPLIED=0
SKIPPED=0
FAILED_ITEMS=()

while read -r row; do
  action=$(jq -r '.action' <<<"$row")
  item_id=$(jq -r '.item_id' <<<"$row")
  tgt=$(jq -r '.target_iteration_id' <<<"$row")
  cur=$(jq -r '.current_iteration_id // ""' <<<"$row")

  if [[ "$action" == "skip" || "$action" == "dry-run" ]]; then
    SKIPPED=$((SKIPPED+1))
    continue
  fi

  if gh project item-edit \
       --project-id "$PROJECT_NODE_ID" \
       --id "$item_id" \
       --field-id "$ITERATION_FIELD_ID" \
       --iteration-id "$tgt" >/dev/null; then
    APPLIED=$((APPLIED+1))
  else
    FAILED_ITEMS+=("$item_id|$cur|$tgt")
  fi
done < <(jq -c '.[]' "$WORKDIR/plan.json")
```

## 阶段 5：报告

主 LLM 把以下信息综合到对话回应：

1. **Brief**（阶段 3 agent stdout 全文，固定 H2 章节）
2. **Apply 摘要**（仅 apply 模式）：
   - 写入 `$APPLIED` / 跳过 `$SKIPPED` / 失败 `${#FAILED_ITEMS[@]}`
3. **失败回滚命令**（仅有失败项时；**逐条审查后再执行**，不要批量复制粘贴）：
   ```
   # 以下命令仅对失败项有效，请确认对应项确需回滚再逐条执行
   gh project item-edit --project-id <PID> --id <ITEM> --field-id <FID> --iteration-id <OLD_ITER_ID>
   ...
   ```
4. **WORKDIR 清理提示**：`rm -rf $WORKDIR`（含 issues.json 等含 body 副本的临时文件）

## 约束

- 所有 `gh` 命令 `dangerouslyDisableSandbox: true`
- **默认 dry-run**；`--apply` 必须显式 opt-in
- 只写 Project v2 Iteration 字段 value + Iteration field configuration（追加 daily iteration option，append-only）；**不动 Status / Estimate / labels**
- 不创建 / 修改 / 关闭 issue
- 不修改代码、不跑 build/test
- Apply 失败不自动回滚（输出回滚命令清单交人决策，避免二次破坏）
- **不在本地长期落盘**：brief = stdout，plan.json = `$WORKDIR/`（mktemp，run 完丢弃）
- 字段 ID 每次启动从 `gh` 查询，不硬编（避免 Project v2 迁移后双源漂移）
