---
name: daily-planner
description: "每日 backlog → Project v2 Iteration 调度（wave 模型 + carry-over + 周末自动识别）。默认 dry-run；--apply 才写入。仅用户显式 /daily-planner 触发，避免 AI 主动调用对真实 Project v2 写入。"
argument-hint: "[--apply] [--date=YYYY-MM-DD] [--weekend|--weekday] [--include-p2] [--include-p3]"
allowed-tools: [Bash, Read, Write, Agent, AskUserQuestion]
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
FIELD_LIST_JSON=$(gh project field-list 3 --owner ghbvf --format json)
ITERATION_FIELD_ID=$(jq -r '.fields[] | select(.name=="Iteration").id' <<<"$FIELD_LIST_JSON")
[[ -z "$PROJECT_NODE_ID" || -z "$ITERATION_FIELD_ID" ]] && {
  echo "ERROR: failed to resolve PROJECT_NODE_ID or ITERATION_FIELD_ID" >&2
  exit 1
}

# 0.4 Wave single-select field ID + option IDs（C3c）
# 提取走 lib/resolve-wave-fields.sh（单源；collect-then-first 防 per-element `// ""`
# 换行污染——裸 `select(.name=="Wave").id // ""` 会为每个非 Wave 字段吐一空行，
# 污染 WAVE_FIELD_ID 换行后传给 `gh ... --field-id`。同 PR #926 jq-scoping bug，
# 由 SMOKE_LIVE Part B 暴露；resolve-wave-fields 离线 case 钉死回归。
# dry-run 模式下 WAVE_FIELD_ID 为空时 agent 仍可出 plan（wave_option_id 字段设 ""）。
eval "$(FIELD_LIST_JSON="$FIELD_LIST_JSON" bash .claude/skills/daily-planner/lib/resolve-wave-fields.sh)"
# fail-CLOSED（仅 apply 模式）：
#   - WAVE_FIELD_ID 空 → 阶段 4 apply-gate.sh fail-fast
#   - WAVE_FIELD_ID 非空但 WAVE_OPTION_IDS 空 = 配置损坏（字段无选项）
#   - 任一 Wave N 名称缺失（option id 空）→ fail-fast（防 UI 重排/缺名静默错位）
if [[ "${APPLY:-}" == "true" && -n "$WAVE_FIELD_ID" ]]; then
  if [[ -z "$WAVE_OPTION_IDS" ]]; then
    echo "ERROR: WAVE_OPTION_IDS empty but WAVE_FIELD_ID set; Wave field has no options (config corrupt)" >&2
    exit 1
  fi
  for _wn in 1 2 3 4; do
    _wvar="WAVE_OPTION_ID_WAVE${_wn}"
    if [[ -z "${!_wvar:-}" ]]; then
      echo "ERROR: Wave option \"Wave ${_wn}\" not found in Project #3 Wave field options" >&2
      exit 1
    fi
  done
fi

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

## 阶段 1.5：拉取 blocked-by 依赖 DAG

```bash
# 1.5 Native blocked-by 边（C2b）→ $WORKDIR/deps.json
# GraphQL 字段 `blockedBy(first: N)` 是正式 API（无 preview header 需求，
# 2026-05-25 live introspection 实测于 ghbvf/gocell 账号确认）。
# schema：{ "<issue_number_string>": { "blocked_by": [<int>, ...] }, ... }
# 仅含有入边的 issue；空图 = {}。
# 降级策略：仅真·瞬态 API 错误才降级，loud WARN + [DEP DATA UNAVAILABLE] 标记，
# **禁止 silent {}**（区别于 sub-issues 的静默 fallback）。
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
    echo "WARN: [ACTION REQUIRED] today iteration for $DATE not found in Iteration field." >&2
    echo "WARN: [DRY-RUN] planning aborted — TODAY_ITERATION_ID is required to score wave layout." >&2
    echo "WARN: re-run with --apply to create today's iteration option and proceed." >&2
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
  description: "Score backlog + carry-over + wave grouping + conflict_group + wave_option_id",
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
      WAVE_FIELD_ID = {WAVE_FIELD_ID}                    # Wave single-select field ID（空=字段未配置）
      WAVE_OPTION_IDS = {WAVE_OPTION_IDS}                # 逗号分隔已知 wave option id
      WAVE_OPTION_ID_WAVE1 = {WAVE_OPTION_ID_WAVE1}      # Wave 1 option id（按名 "Wave 1" 匹配）
      WAVE_OPTION_ID_WAVE2 = {WAVE_OPTION_ID_WAVE2}      # Wave 2 option id（按名 "Wave 2" 匹配）
      WAVE_OPTION_ID_WAVE3 = {WAVE_OPTION_ID_WAVE3}      # Wave 3 option id（按名 "Wave 3" 匹配）
      WAVE_OPTION_ID_WAVE4 = {WAVE_OPTION_ID_WAVE4}      # Wave 4 option id（按名 "Wave 4" 匹配）

    Data files (Read these):
      {WORKDIR}/issues.json        — backlog 池 (P0/P1 默认；P2/P3 视 flag)
      {WORKDIR}/items.json         — Project v2 items（含 iteration / status / state）
      {WORKDIR}/iter-config.json   — Iteration 配置
      {WORKDIR}/sub-issues.json    — sub-issue 关系（GraphQL 原生）
      {WORKDIR}/deps.json          — blocked-by DAG（C2b）：{ "<issue_num>": { "blocked_by": [<int>,...] } }

    Tasks:
      # 两正交轴说明（避免概念混淆）：
      # - 拓扑序（wave 编号）= 正确性序：blocker.wave ≤ dependent.wave，确保依赖关系不倒置
      # - conflict_group = 冲突并行度：同 conflict_group = 共享文件冲突 → 必须串行；
      #   异 conflict_group = 独立 → 可并行
      # 两者独立：同 wave 内可有多 conflict_group（并行），同 conflict_group 内也应拓扑安全
      1. Read all 5 files
      2. Compute carry-over (if not CARRY_OVER_DISABLED): items.json 中
         iter.iterationId == YESTERDAY_ITERATION_ID AND content.state == "OPEN"
         AND (status==null OR status.name != "Done") 的 issue → 加入 carry-over 列表
      3. Score P0/P1 池 per WSJF 简化版（详 agent.md §排序算法）；
         解析每个 issue body 的 "### Affected paths" 段（C2c）作为 affected_paths[]；
         解析失败 / 缺失 → 按 #6 wildcard 处理（不可当作独立可并行）
      4. 拓扑排序（C2b）：消费 deps.json blocked_by 边建 DAG；
         环 → 不阻塞 + brief Warnings [DEP CYCLE]；
         跨 iteration 不可解 → Warnings [DEP CROSS-ITERATION]，dependent 仍可排但标记
      5. Wave 调度：
         - Wave 1 头部填 carry-over（按原 WSJF score 排序）
         - 剩余 Wave 1 / Wave 2+ 填 新 issue 按分数降序
         - 容量 = WAVE_COUNT × WAVE_SIZE；超出 → Unscheduled [capacity overflow]
         - 空输入集（input + carry-over 均 0）→ brief Warnings [EMPTY INPUT SET]，plan=[]
         - placement 守拓扑：blocker.wave ≤ dependent.wave（dependent 顺延到其最晚 blocker 之后）
      6. conflict_group（C2c）：每 wave 对 affected_paths 前缀重合做 union-find，
         每连通分量=一组（同组=共享文件冲突 → 串行；跨组=独立 → 可并行）；
         **无 / 解析失败 affected_paths → wildcard fail-closed**：footprint 未知，与同 wave
         所有 item union（保守视为冲突于一切）→ 整 wave 落同一 conflict_group（串行），
         brief 标 [AFFECTED PATHS MISSING — wave serialized]。**绝不视为 singleton 独立组**
         （未知文件范围当可并行会重造并发文件冲突，与 agent.md STEP 6 一致）。
         全局唯一 int ≥ 1，(wave 升序, 首次出现) 从 1 分配；set 和 skip 都必须有值。
      7. wave_option_id（C3c）：action=="set" 时根据 wave 编号从 WAVE_OPTION_ID_WAVE* 常量取值；
         WAVE_FIELD_ID 为空时置 ""。
      8. Emit brief markdown to STDOUT（详 agent.md §输出格式）
      9. Write plan.json to {PLAN_PATH}（详 agent.md §输出 #2）
         新增字段：conflict_group (int>=1，set 和 skip 均必填) + wave_option_id (str，action==skip 可为 "")
  """
)
```

## 阶段 3.5：Apply 前确认 gate（仅 `--apply` + plan 非空）

apply 模式下，agent 出 plan.json 后、执行写入前，宿主 LLM 用 `AskUserQuestion` 向用户确认。
dry-run 不执行此阶段（无 mutation，无需确认）。

**设计原则：default-deny**（对标 Terraform `apply` 仅 explicit approve 才写入）。
proceed 默认 false；**唯一进入阶段 4 的路径是用户显式回答 Yes**。
No / Show 后非 Yes / 任何歧义 → 零写入，exit 0。

```
# 伪代码：宿主 LLM 逻辑（非 bash）
if APPLY == "true":
  plan = read_json(PLAN_PATH)
  if len(plan) == 0:
    # 空 plan，无需确认，直接跳阶段 4（无实际 mutation）
    pass
  else:
    proceed = false  # default-deny
    while true:
      answer = AskUserQuestion(
        question="plan.json 已生成（共 {len(plan)} 条）。是否立即写入 Project v2？",
        options=[
          "Yes — 立即写入（执行阶段 4，无法撤销；失败时输出回滚命令清单）",
          "No — 退出，不写入（零 mutation，可稍后重新 --apply）",
          "Show plan.json — 先查看完整计划再决策"
        ]
      )
      if answer == "Yes":
        proceed = true
        break
      elif answer == "No":
        # fail-closed：不写入，零 mutation
        echo "INFO: 用户取消，退出，零 mutation。" >&2
        exit 0
      elif answer starts with "Show":
        # 展示完整 plan.json，然后继续循环重新问
        Read(PLAN_PATH)
        # 循环后再次展示三选项，不自动进入阶段 4
        continue
      else:
        # 任何歧义回答 → 零写入，fail-closed
        echo "INFO: 回答不明确，退出，零 mutation。" >&2
        exit 0
    # 循环结束后：只有 proceed=true 才进入阶段 4
    if not proceed:
      exit 0
```

## 阶段 4：Apply 分支（仅 `--apply`）

```bash
[[ "${APPLY:-}" != "true" ]] && {
  echo "INFO: dry-run 模式，跳过 Project v2 写入。加 --apply 真写。" >&2
  echo "INFO: plan.json 路径: $PLAN_PATH（可用 Read 或 cat 查看 agent 计划）" >&2
  exit 0
}
# 所有校验与写入逻辑在 lib/ 脚本中（单源，零平行副本）。
# 校验失败 fail-closed：plan.json 是 agent (LLM) 生成的，必须当作未经信任的输入对待。
# WAVE_FIELD_ID/WAVE_OPTION_IDS 由阶段 0 动态查出，通过 env 传给子脚本。
export WAVE_FIELD_ID WAVE_OPTION_IDS
SKILL_DIR=".claude/skills/daily-planner"
bash "$SKILL_DIR/lib/apply-gate.sh"  || exit 1
bash "$SKILL_DIR/lib/apply-write.sh"
```

## 阶段 5：报告

宿主 LLM 把以下信息综合到对话回应：

1. **Brief**（阶段 3 agent stdout 全文，含 Wave N 章节 + conflict_group 分组说明）
2. **Plan 路径**：`$PLAN_PATH`（dry-run 和 apply 模式均输出；`/ship --from-plan=` 消费此路径；**在清理 WORKDIR 前保存**）
3. **Audit 行**（仅 apply 模式，由 apply-write.sh 写到 stderr；含 `applied / skipped / failed / wave_applied` 计数）
4. **Per-item NDJSON 路径**（仅 apply 模式：`$WORKDIR/audit.ndjson`）——每行含 ts/date/mode/action/item_id/prev_iteration_id/target_iteration_id/result，是 partial apply / 还原 old iteration / 重放的真值源
5. **失败回滚命令清单**（仅 apply 模式有失败项时，由 apply-write.sh 写到 stderr；回滚目标是**已写入**的项，不是失败项）
6. **Wave 写入情况**（仅 apply 模式）：Audit 汇总行的 `wave_applied=N` 字段（Wave 写失败时 audit.ndjson 含 `result=wave_failed` 行 + stderr WARN）
7. **WORKDIR 清理提示**：`rm -rf $WORKDIR`（保留供 apply 失败后追溯 + per-item audit；用户审查后手动清）

## 约束

- 所有 `gh` 命令 `dangerouslyDisableSandbox: true`
- **默认 dry-run；`--apply` 必须显式 opt-in**——所有 Project v2 mutation（含 `updateProjectV2Field` field-configuration append、`gh project item-edit` field-value 写入）必须在 `APPLY=true` 分支内；dry-run 路径零 mutation
- 可写字段：Project v2 **Iteration** field value（主写）+ **Wave** single-select field value（C3c，apply-write.sh 在 Iteration 写入成功后追写）+ Iteration field configuration（append-only 追加 daily option）
- **不动 Status / Estimate / labels / issue body / comment / title**
- 不创建 / 修改 / 关闭 issue
- 不修改代码、不跑 build/test
- **Plan.json 来自 agent (LLM) → 当作未经信任的输入**：apply 前两层 gate（schema + membership：item_id ∈ Project items、target == TODAY_ITERATION_ID、action ∈ {set, skip}，conflict_group int≥1（必填），wave_option_id ∈ WAVE_OPTION_IDS），任一违规整体 fail-closed
- **Wave field fail-closed**：apply 模式下 WAVE_FIELD_ID 为空（字段未配置 / 字段名拼错） → apply-gate.sh fail-fast，零 mutation；详见 `## C3a 前置` 段
- Apply 失败不自动回滚（输出回滚命令清单交人决策）；**回滚目标 = 已写入项**（partial apply 残留），失败项另列在 retry section
- **不在本地长期落盘**：brief = stdout，plan.json / audit.ndjson / deps.json = `$WORKDIR/` mktemp 临时；per-item NDJSON 提供 partial apply / 还原 / 重放所需的真值
- 字段 ID 每次启动从 `gh` 查询，不硬编（避免 Project v2 迁移后双源漂移）
- token 无 `project` scope → fail-fast 退出（无云沙箱降级）
- **blocked-by 降级策略**：仅真·瞬态 API 错误才降级，loud WARN + `[DEP DATA UNAVAILABLE]`；结构性不支持 = `{}`（需 ADR carve-out + backlog 追踪）

## C3a 前置：Project v2 配置（已完成）

Project v2 #3（owner `ghbvf`）Wave 字段配置状态：

| 配置项 | 状态 | 说明 |
|--------|------|------|
| **Wave 字段**（single-select） | ✅ 已建 | options：Wave 1 / Wave 2 / Wave 3 / Wave 4 |
| `Item added to project` workflow | ✅ 已开 | target Status = Backlog |
| `Pull request merged` workflow | ✅ 已开 | target Status = Done |
| `Pull request linked to issue` workflow | ✅ 已开 | target Status = In review（/ship 不写 In review，交此 workflow）|

字段 ID 由阶段 0 动态查询（`gh project field-list`），不硬编。`WAVE_FIELD_ID` 为空时 apply-gate.sh fail-fast，提示 `ERROR: Wave field missing; Wave single-select field not found in Project #3; see SKILL.md §C3a`。

**若 Wave 字段尚未配置，在 Project v2 UI 建字段步骤：**

1. 打开 `https://github.com/users/ghbvf/projects/3`
2. 点右上角 `+`（Add field）→ 选 **Single select**
3. 字段名填 `Wave`（大小写必须完全一致）
4. 依次添加 4 个选项：`Wave 1`、`Wave 2`、`Wave 3`、`Wave 4`（顺序即 wave_number 的映射顺序）
5. 保存后重新运行 `/daily-planner --apply`，阶段 0 会自动拉取新字段 ID
