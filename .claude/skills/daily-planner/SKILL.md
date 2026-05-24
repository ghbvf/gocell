---
name: daily-planner
description: "每日 backlog → Project v2 Iteration 调度（wave 模型 + carry-over + 周末自动识别）。默认 dry-run；--apply 才写入。仅用户显式 /daily-planner 触发，避免 AI 主动调用对真实 Project v2 写入。"
argument-hint: "[--apply] [--date=YYYY-MM-DD] [--weekend|--weekday] [--include-p2] [--include-p3]"
allowed-tools: [Bash, Read, Write, Agent]
disable-model-invocation: true
---

# Daily Planner Skill

调度 backlog → 当日 Project v2 Iteration（每日 1-day option）。**默认 dry-run**；`--apply` 才真写入。Backlog 真值源见 `docs/backlog.md`。

## 设计原则

- **Project v2 是真值源**：当日排期写入 Iteration field value；不在本地长期归档 brief / audit log
- **默认输入集 = P0 + P1**：单人项目 P3 大头不参与每日排期；`--include-p2` / `--include-p3` 可扩
- **Wave 模型**：工作日 2 wave × 5 issue = 10 容量 / 周末 4 wave × 5 issue = 20 容量
- **Carry-over 硬规则**：昨日 iteration 内 issue.state≠CLOSED 且 Status≠Done 的项**优先占 Wave 1 头部**
- **字段 ID 动态查询**：每次启动从 `gh` 拉真实 ID，不维护硬编副本

## 参数

| Flag | 默认 | 含义 |
|------|------|------|
| `--apply` | off | 写入 Project v2 Iteration 字段（含 carry-over move）；未传则纯 dry-run |
| `--date=YYYY-MM-DD` | today | 目标排期日期 |
| `--weekend` / `--weekday` | 自动 `date +%u` | 强制模式覆盖（影响 wave 数 → 容量）|
| `--include-p2` | off | 加入 P2 issue 作输入 |
| `--include-p3` | off | 加入 P3 issue（极少需要；P3 默认 nice-to-have） |

`disable-model-invocation: true`（frontmatter）= 阻止 AI 在日常对话里"看到 today / standup 关键词"就主动调本 skill；仅用户显式 `/daily-planner` 触发。

## Argument parsing convention

宿主 LLM 收到用户 `/daily-planner <args>` 后，将 flag 解析为环境变量再执行下列 bash：

| Flag | Env 设置 |
|------|---------|
| `--apply` | `APPLY=true` |
| `--date=YYYY-MM-DD` | `DATE=YYYY-MM-DD` |
| `--weekend` | `FORCE_WEEKEND=true` |
| `--weekday` | `FORCE_WEEKDAY=true` |
| `--include-p2` | `INCLUDE_P2=true` |
| `--include-p3` | `INCLUDE_P3=true` |

未设的 env 默认 unset（bash `${VAR:-}` 兜底）。

---

## 阶段 0：常量动态查询 + token scope fail-fast + 模式探测

```bash
set -euo pipefail

# 0.1 Token scope。先区分未登录 vs 缺 scope（gh auth status 失败 → 未登录；
# 成功但 grep 不到 → 缺 scope）。grep 用宽松正则，跨 gh 版本健壮。
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
PROJECT_NODE_ID=$(gh project view 3 --owner ghbvf --format json | jq -r '.id')
ITERATION_FIELD_ID=$(gh project field-list 3 --owner ghbvf --format json \
  | jq -r '.fields[] | select(.name=="Iteration").id')
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
```

## 阶段 1：拉取数据

```bash
WORKDIR="$(mktemp -d -t daily-planner.XXXXXX)"
chmod 700 "$WORKDIR"

# 1.1 输入集：pri-missing 哨兵 + P0 + P1 (默认) + 可选 P2/P3。
# pri-missing 是 backlog workflow 自动打的哨兵 (CLAUDE.md "新 backlog 条目")，
# 漏标 priority 的 issue 不能被排期忽略；按 score 公式它有最高权重 (110)。
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
echo "INFO: $(jq 'length' "$WORKDIR/items.json") Project v2 items loaded" >&2

# 1.3 Iteration 配置（拿当前所有 iteration option）
gh api graphql -f query='query {
  user(login:"ghbvf"){ projectV2(number:3){
    field(name:"Iteration"){ ... on ProjectV2IterationField {
      configuration { duration startDay iterations { id title startDate duration } }
    }}
  }}
}' > "$WORKDIR/iter-config.json"

# 1.4 Sub-issue 关系（GraphQL 原生）
# GraphQL-Features header 是 preview API；账号 / repo 未启用时会返回 errors。
# 失败显式 WARN，不静默吞错（保留降级 `{}`，让 agent 当作"无 sub-issue 数据"运行）。
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
```

## 阶段 2：确保 today iteration 存在

```bash
# 2.1 推断 $DATE 对应的 iteration option
# jq 在 select() 内嵌套 pipe 后 . 会被覆盖为子表达式中间值；用 `as $it` 把当前
# iteration 对象绑定为变量，确保算术子表达式仍能引用其 startDate / duration
TODAY_ITERATION_ID=$(jq -r --arg d "$DATE" '
  .data.user.projectV2.field.configuration.iterations[]
  | . as $it
  | select($it.startDate <= $d
           and ($d < ((($it.startDate | strptime("%Y-%m-%d") | mktime) + ($it.duration * 86400))
                       | strftime("%Y-%m-%d")))).id
' "$WORKDIR/iter-config.json")

# 2.2 缺失则用 GraphQL mutation append 一个 1-day iteration option 覆盖 $DATE。
# **写入 Project v2 是 apply-only**——dry-run 不得执行 updateProjectV2Field（mutation
# 会真实改 field configuration）；缺 iteration 时 dry-run 给出 would-create 摘要并退出。
if [[ -z "$TODAY_ITERATION_ID" ]]; then
  if [[ "${APPLY:-}" != "true" ]]; then
    echo "INFO: [DRY-RUN] today iteration for $DATE missing; --apply would append a 1-day option to Iteration field." >&2
    echo "INFO: [DRY-RUN] no Project v2 mutation executed; planning aborted (need TODAY_ITERATION_ID to score wave layout)." >&2
    echo "INFO: re-run with --apply to create today's iteration option and proceed." >&2
    exit 0
  fi

  EXISTING=$(jq -c '.data.user.projectV2.field.configuration.iterations | map({title, startDate, duration})' "$WORKDIR/iter-config.json")
  NEW_TITLE="Iteration $(jq 'length + 1' <<<"$EXISTING")"  # 命名仅展示用，实际唯一 ID 由 GitHub 生成
  ALL_ITERS=$(jq -c --arg title "$NEW_TITLE" --arg d "$DATE" \
               '. + [{title: $title, startDate: $d, duration: 1}]' <<<"$EXISTING")

  # iterationConfiguration.startDate / .duration 是 field-level "cycle 起点 / 周期" 配置，
  # iterations 数组每项 startDate / duration 才决定每个 option 的 daily 边界。
  # 用 EXISTING 第一项的 startDate / duration 作 cycle 锚点（保持已有配置不变；
  # 实际新增的 option 在 iterations 数组最后一项）。
  # 类型名 ProjectV2Iteration 是 INPUT_OBJECT（GraphQL 2026-05-24 introspection 实测）。
  # `-F` 大写才把 value 当 raw JSON/array 传入；`-f` 小写是 string 字面量（数组场景必 fail）。
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
' "$WORKDIR/iter-config.json")
CARRY_OVER_DISABLED=false
if [[ -z "$YESTERDAY_ITERATION_ID" ]]; then
  CARRY_OVER_DISABLED=true
  echo "WARN: yesterday iteration not found ($YESTERDAY); carry-over disabled" >&2
fi
```

## 阶段 3：派发 daily-planner agent 评分排序

下面 `Agent(...)` 块是**宿主 LLM 伪代码模板**（不是 bash），由宿主 LLM 把
`{VAR}` 占位符替换为上方 bash 阶段 0-2 设置的环境变量真实值后，调用 Agent
tool。每个 `{VAR}` 都必须能在 bash 上下文中找到对应变量。

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
      CARRY_OVER_DISABLED = {CARRY_OVER_DISABLED}        # true 时 brief Warnings 必加 [CARRY-OVER DISABLED]
      DATE = {DATE}                                       # YYYY-MM-DD
      IS_WEEKEND = {IS_WEEKEND}                           # true|false
      WAVE_COUNT = {WAVE_COUNT}                           # 2|4
      WAVE_SIZE = 5                                       # 容量 = WAVE_COUNT * WAVE_SIZE
      MODE = {"apply" if APPLY else "dry-run"}

    Data files (Read these):
      {WORKDIR}/issues.json        — backlog 池 (P0/P1 默认；P2/P3 视 flag)
      {WORKDIR}/items.json         — Project v2 items（含 iteration / status / state）
      {WORKDIR}/iter-config.json   — Iteration 配置
      {WORKDIR}/sub-issues.json    — sub-issue 关系（GraphQL 原生）

    Tasks:
      1. Read all 4 files
      2. Compute carry-over (if not CARRY_OVER_DISABLED): items.json 中
         iter.iterationId == YESTERDAY_ITERATION_ID AND content.state == "OPEN"
         AND (status==null OR status.name != "Done") 的 issue → 加入 carry-over 列表
      3. Score P0/P1 池 per WSJF 简化版（详 agent.md §排序算法）
      4. Wave 调度：
         - Wave 1 头部填 carry-over（按原 WSJF score 排序）
         - 剩余 Wave 1 / Wave 2+ 填 新 issue 按分数降序
         - 容量 = WAVE_COUNT × WAVE_SIZE；超出 → Unscheduled [capacity overflow]
         - 空输入集（input + carry-over 均 0）→ brief Warnings [EMPTY INPUT SET]，plan=[]
      5. Emit brief markdown to STDOUT（详 agent.md §输出格式）
      6. Write plan.json to {PLAN_PATH}（详 agent.md §输出 #2）
  """
)
```

## 阶段 4：Apply 分支（仅 `--apply`）

```bash
[[ "${APPLY:-}" != "true" ]] && {
  echo "INFO: dry-run 模式，跳过 Project v2 写入。加 --apply 真写。" >&2
  exit 0
}

# 4.1 plan.json 校验。两层 gate：
#   (a) schema：类型 + action 取值集
#   (b) membership：item_id ∈ allowed set（本轮 input 集对应 item ∪ carry-over item）；
#       target_iteration_id == TODAY_ITERATION_ID；
#       current_iteration_id（若非空）∈ iter-config.json 的 iteration IDs
# 校验失败 fail-closed：plan.json 是 agent (LLM) 生成的，必须当作未经信任的输入对待。
# allowed set 由 bash 独立从 items.json + issues.json + YESTERDAY_ITERATION_ID 算出，
# 不复用 agent 的判定——防止 agent 把任意 Project item（不在本轮排期范围）写入 today。
jq -e '
  (type == "array") and
  all(.[];
    (.item_id | type == "string") and
    (.target_iteration_id | type == "string") and
    (.action | type == "string") and
    (.action == "set" or .action == "skip")
  )
' "$WORKDIR/plan.json" > /dev/null || {
  echo "ERROR: plan.json schema invalid (type / action enum)" >&2
  exit 1
}

# 候选 item_id 集 = (本轮 input 集对应的 Project item) ∪ (carry-over item)，
# 由 bash 用 items.json + issues.json + YESTERDAY_ITERATION_ID 确定性算出，
# **不**信任 agent 的取舍——agent 偏离时由 bash 收口 fail-closed。
# 校验范围比"任意 Project item"窄，对齐 skill 的安全边界：
# "LLM 排计划，bash 守真实 mutation"。
jq -r --slurpfile issues "$WORKDIR/issues.json" --arg yid "${YESTERDAY_ITERATION_ID:-}" '
  ($issues[0] | map(.number)) as $inputNums |
  .[] | select(
    # (a) Project item linked to an input backlog issue
    (.content.number != null and ($inputNums | index(.content.number) != null))
    or
    # (b) carry-over: yesterday iter + OPEN + not Done
    ($yid != "" and .iter.iterationId == $yid
     and .content.state == "OPEN"
     and (.status == null or .status.name != "Done"))
  ) | .id
' "$WORKDIR/items.json" | sort -u > "$WORKDIR/valid-item-ids.txt"

# 候选 iteration_id 集 (field configuration)
jq -r '.data.user.projectV2.field.configuration.iterations[].id' \
  "$WORKDIR/iter-config.json" | sort -u > "$WORKDIR/valid-iter-ids.txt"
# 2.2 新建的 today iteration 也合法；merge 进去。
echo "$TODAY_ITERATION_ID" >> "$WORKDIR/valid-iter-ids.txt"
sort -u "$WORKDIR/valid-iter-ids.txt" -o "$WORKDIR/valid-iter-ids.txt"

VIOLATIONS=0
while read -r row; do
  item_id=$(jq -r '.item_id' <<<"$row")
  tgt=$(jq -r '.target_iteration_id' <<<"$row")
  cur=$(jq -r '.current_iteration_id // ""' <<<"$row")

  if ! grep -qxF "$item_id" "$WORKDIR/valid-item-ids.txt"; then
    echo "ERROR: plan.json item_id not in allowed set (input issues ∪ carry-over): $item_id" >&2
    VIOLATIONS=$((VIOLATIONS+1))
  fi
  if [[ "$tgt" != "$TODAY_ITERATION_ID" ]]; then
    echo "ERROR: plan.json target_iteration_id != TODAY_ITERATION_ID ($tgt vs $TODAY_ITERATION_ID) for item $item_id" >&2
    VIOLATIONS=$((VIOLATIONS+1))
  fi
  if [[ -n "$cur" ]] && ! grep -qxF "$cur" "$WORKDIR/valid-iter-ids.txt"; then
    echo "ERROR: plan.json current_iteration_id unknown: $cur (item $item_id)" >&2
    VIOLATIONS=$((VIOLATIONS+1))
  fi
done < <(jq -c '.[]' "$WORKDIR/plan.json")
[[ $VIOLATIONS -gt 0 ]] && {
  echo "ERROR: $VIOLATIONS plan.json membership violation(s); refusing to apply" >&2
  exit 1
}

# 4.2 逐项写入（agent 已用 action="skip" 标注幂等项）。
# APPLIED_ITEMS 跟踪"实际写入成功"的项（item_id|prev_iter|new_iter），供 4.3 回滚清单使用——
# partial apply 失败时，操作员真正需要回滚的是**已经写入**的项，而不是失败项（失败项未写）。
# 同步把每条 action 落地为 NDJSON 一行（4.4 持久化 per-item audit），用于失败追溯
# / 还原 old iteration / 重放 ——单行聚合 audit 信息不足以重建上下文。
APPLIED=0; SKIPPED=0; APPLIED_ITEMS=(); FAILED_ITEMS=()
AUDIT_NDJSON="$WORKDIR/audit.ndjson"
: > "$AUDIT_NDJSON"
audit_entry() {
  # $1=action $2=item_id $3=prev_iter $4=target_iter $5=result
  printf '{"ts":"%s","date":"%s","mode":"%s","action":"%s","item_id":"%s","prev_iteration_id":"%s","target_iteration_id":"%s","result":"%s"}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$DATE" "$([[ "$IS_WEEKEND" == "true" ]] && echo weekend || echo weekday)" \
    "$1" "$2" "$3" "$4" "$5" >> "$AUDIT_NDJSON"
}

while read -r row; do
  action=$(jq -r '.action' <<<"$row")
  item_id=$(jq -r '.item_id' <<<"$row")
  tgt=$(jq -r '.target_iteration_id' <<<"$row")
  cur=$(jq -r '.current_iteration_id // ""' <<<"$row")

  if [[ "$action" == "skip" ]]; then
    SKIPPED=$((SKIPPED+1))
    audit_entry "$action" "$item_id" "$cur" "$tgt" "skipped"
    continue
  fi

  if gh project item-edit \
       --project-id "$PROJECT_NODE_ID" --id "$item_id" \
       --field-id "$ITERATION_FIELD_ID" --iteration-id "$tgt" >/dev/null; then
    APPLIED=$((APPLIED+1))
    APPLIED_ITEMS+=("$item_id|$cur|$tgt")
    audit_entry "$action" "$item_id" "$cur" "$tgt" "applied"
  else
    FAILED_ITEMS+=("$item_id|$cur|$tgt")
    audit_entry "$action" "$item_id" "$cur" "$tgt" "failed"
  fi
done < <(jq -c '.[]' "$WORKDIR/plan.json")

# 4.3 生成回滚命令清单（实际值展开，避免占位符被 shell 误解析为重定向）。
# 范围 = APPLIED_ITEMS（partial apply 失败时已写入的项），不是 FAILED_ITEMS。
# 失败项未写入 Project v2，不需要回滚；失败项另列在 retry section 供操作员决策重试。
if [[ ${#FAILED_ITEMS[@]} -gt 0 ]]; then
  echo "" >&2
  echo "# --- partial apply detected: ${#APPLIED_ITEMS[@]} applied / ${#FAILED_ITEMS[@]} failed ---" >&2
  if [[ ${#APPLIED_ITEMS[@]} -gt 0 ]]; then
    echo "# --- rollback commands for ALREADY-APPLIED items (REVIEW EACH BEFORE EXEC; prev_iter==\"\" 表示原本无 iteration → --clear) ---" >&2
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

# 4.4 Audit：聚合行 + per-item NDJSON 路径
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
MODE_STR=$([[ "$IS_WEEKEND" == "true" ]] && echo weekend || echo weekday)
echo "[audit] $TS date=$DATE mode=$MODE_STR wave_count=$WAVE_COUNT applied=$APPLIED skipped=$SKIPPED failed=${#FAILED_ITEMS[@]}" >&2
echo "[audit] per-item NDJSON: $AUDIT_NDJSON (one line per plan row with prev/target iteration + result)" >&2
```

## 阶段 5：报告

宿主 LLM 把以下信息综合到对话回应：

1. **Brief**（阶段 3 agent stdout 全文，含 Wave N 章节）
2. **Audit 行**（仅 apply 模式，由 4.4 写到 stderr）
3. **Per-item NDJSON 路径**（仅 apply 模式，由 4.4 写到 stderr：`$WORKDIR/audit.ndjson`）——每行含 ts/date/mode/action/item_id/prev_iteration_id/target_iteration_id/result，是 partial apply / 还原 old iteration / 重放的真值源
4. **失败回滚命令清单**（仅 apply 模式有失败项时，由 4.3 写到 stderr，已是真实可执行命令；回滚目标是**已写入**的项，不是失败项）
5. **WORKDIR 清理提示**：`rm -rf $WORKDIR`（保留供 apply 失败后追溯 + per-item audit；用户审查后手动清）

## 约束

- 所有 `gh` 命令 `dangerouslyDisableSandbox: true`
- **默认 dry-run；`--apply` 必须显式 opt-in**——所有 Project v2 mutation（含 `updateProjectV2Field` field-configuration append、`gh project item-edit` field-value 写入）必须在 `APPLY=true` 分支内；dry-run 路径零 mutation
- 只写 Project v2 Iteration field value + Iteration field configuration（append-only 追加 daily option）；**不动 Status / Estimate / labels / issue body / comment / title**
- 不创建 / 修改 / 关闭 issue
- 不修改代码、不跑 build/test
- **Plan.json 来自 agent (LLM) → 当作未经信任的输入**：apply 前两层 gate（schema + membership：item_id ∈ Project items、target == TODAY_ITERATION_ID、action ∈ {set, skip}），任一违规整体 fail-closed
- Apply 失败不自动回滚（输出回滚命令清单交人决策）；**回滚目标 = 已写入项**（partial apply 残留），失败项另列在 retry section
- **不在本地长期落盘**：brief = stdout，plan.json / audit.ndjson = `$WORKDIR/` mktemp 临时；per-item NDJSON 提供 partial apply / 还原 / 重放所需的真值
- 字段 ID 每次启动从 `gh` 查询，不硬编（避免 Project v2 迁移后双源漂移）
- token 无 `project` scope → fail-fast 退出（无云沙箱降级）
