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

所有确定性 shell 收口 `lib/*.sh`（单源 + smoke 覆盖）；本文件只保留一行调用 + Agent/AskUserQuestion 伪代码。

```bash
set -euo pipefail

# 0.1-0.3 token scope fail-fast + Project node / Iteration field ID + date/weekend
# 模式 → lib/resolve-constants.sh（单源；emit eval-able exports，含 FIELD_LIST_JSON）。
# FORCE_*/DATE 经 env 透传（host LLM 按 argument parsing convention 设置）。
eval "$(DATE="${DATE:-}" FORCE_WEEKEND="${FORCE_WEEKEND:-}" FORCE_WEEKDAY="${FORCE_WEEKDAY:-}" \
  bash .claude/skills/daily-planner/lib/resolve-constants.sh)"

# 0.4 Wave field ID + per-wave option IDs（C3c）→ lib/resolve-wave-fields.sh
# （单源；collect-then-first 防 per-element `// ""` 换行污染 WAVE_FIELD_ID，同 PR #926
# jq-scoping bug；resolve-wave-fields 离线 case 钉死回归）。
eval "$(FIELD_LIST_JSON="$FIELD_LIST_JSON" bash .claude/skills/daily-planner/lib/resolve-wave-fields.sh)"

# 0.5 Wave fail-CLOSED（仅 apply 模式；读上方已 resolve 的 WAVE_* env）：
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
```

## 阶段 1：拉取数据

```bash
WORKDIR="$(mktemp -d -t daily-planner.XXXXXX)"
chmod 700 "$WORKDIR"

# 1.1-1.4 backlog issues（pri-missing + P0 + P1 默认；P2/P3 视 flag）+ Project v2
# items + iteration 配置 + sub-issue 关系 → lib/fetch-data.sh（单源；issues 逐 label
# 拉后 unique_by(.number) 合并；sub-issues preview API 失败 loud WARN + 降级 {}）。
INCLUDE_P2="${INCLUDE_P2:-}" INCLUDE_P3="${INCLUDE_P3:-}" WORKDIR="$WORKDIR" \
  bash .claude/skills/daily-planner/lib/fetch-data.sh
```

## 阶段 1.5：拉取 blocked-by 依赖 DAG

```bash
# 1.5 Native blocked-by 边（C2b）→ lib/fetch-deps.sh（单源；写 $WORKDIR/deps.json，
# schema { "<issue_num>": { "blocked_by": [<int>,...] } }，仅含有入边的 issue）。
# 降级策略：仅真·瞬态 API 错误才降级，loud WARN + [DEP DATA UNAVAILABLE]，禁 silent {}。
WORKDIR="$WORKDIR" bash .claude/skills/daily-planner/lib/fetch-deps.sh
```

## 阶段 2：确保 today iteration 存在

```bash
# 2.1-2.3 推断 today iteration（缺失：dry-run abort / apply-only append 1-day option）
# + 推断昨日 iteration（carry-over 源）→ lib/ensure-iteration.sh（单源；emit eval-able
# exports TODAY_ITERATION_ID / YESTERDAY_ITERATION_ID / CARRY_OVER_DISABLED）。
# **写入 Project v2 是 apply-only**：dry-run 路径零 mutation；缺 iteration 时 dry-run
# 打印 would-create 摘要 + emit DP_ABORT_DRYRUN=true 让调用方停止。
eval "$(WORKDIR="$WORKDIR" DATE="$DATE" APPLY="${APPLY:-}" ITERATION_FIELD_ID="$ITERATION_FIELD_ID" \
  bash .claude/skills/daily-planner/lib/ensure-iteration.sh)"
[[ "${DP_ABORT_DRYRUN:-}" == "true" ]] && exit 0
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
      # 两轴说明（避免概念混淆）：
      # - 拓扑序（wave 编号）= 正确性序：blocker.wave ≤ dependent.wave，确保依赖关系不倒置
      # - conflict_group = advisory 显示分组：同 conflict_group = 共享文件、异 = 文件独立；
      #   ship --from-plan 移除后无机器消费者，仅供人工读 brief Group 列（ADR 202605250010）
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
      6. conflict_group（C2c，**advisory 显示列**）：每 wave 对 affected_paths 前缀重合做
         union-find，每连通分量=一组（同组=共享文件；异组=文件独立）；
         **无 / 解析失败 affected_paths → wildcard**：footprint 未知，与同 wave 所有 item
         union → 整 wave 落同一 conflict_group，brief 标 [AFFECTED PATHS MISSING — shared
         footprint]。全局唯一 int ≥ 1，(wave 升序, 首次出现) 从 1 分配。
         **无机器消费者**——ship --from-plan 移除后该列仅供人工参考（ADR 202605250010），
         apply-gate 不再校验；缺失也不阻断 apply（续保留与否见 ADR 202605250010 §Amendment）。
      7. wave_option_id（C3c）：action=="set" 时根据 wave 编号从 WAVE_OPTION_ID_WAVE* 常量取值；
         WAVE_FIELD_ID 为空时置 ""。
      8. Emit brief markdown to STDOUT（详 agent.md §输出格式）
      9. Write plan.json to {PLAN_PATH}（详 agent.md §输出 #2）
         字段：conflict_group (int>=1，advisory 显示列，不再校验) + wave_option_id (str，action==skip 可为 "")
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
2. **Plan 路径**：`$PLAN_PATH`（dry-run 和 apply 模式均输出，是 apply-gate.sh 的 schema/membership 校验输入；**在清理 WORKDIR 前保存**）
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
- **Plan.json 来自 agent (LLM) → 当作未经信任的输入**：apply 前两层 gate（schema + membership：item_id ∈ Project items、target == TODAY_ITERATION_ID、action ∈ {set, skip}，wave_option_id ∈ WAVE_OPTION_IDS），任一违规整体 fail-closed。conflict_group 是 advisory 显示列（无机器消费者），不在 gate 范围
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
| `Pull request linked to issue` workflow | ✅ 已开 | target Status = In review（由 PR body `Closes #N` link 自动触发）|

字段 ID 由阶段 0 动态查询（`gh project field-list`），不硬编。`WAVE_FIELD_ID` 为空时 apply-gate.sh fail-fast，提示 `ERROR: Wave field missing; Wave single-select field not found in Project #3; see SKILL.md §C3a`。

**若 Wave 字段尚未配置，在 Project v2 UI 建字段步骤：**

1. 打开 `https://github.com/users/ghbvf/projects/3`
2. 点右上角 `+`（Add field）→ 选 **Single select**
3. 字段名填 `Wave`（大小写必须完全一致）
4. 依次添加 4 个选项：`Wave 1`、`Wave 2`、`Wave 3`、`Wave 4`（顺序即 wave_number 的映射顺序）
5. 保存后重新运行 `/daily-planner --apply`，阶段 0 会自动拉取新字段 ID
