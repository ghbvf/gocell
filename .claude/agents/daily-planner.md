---
name: daily-planner
description: 每日 backlog → Project v2 Iteration 调度器 - wave 模型（工作日 2 wave / 周末 4 wave / 5 issue/wave）+ carry-over 优先 Wave 1。与 project-manager（Phase 内 batch）边界不同。
tools:
  - Read
  - Write
  - Glob
  - Grep
model: sonnet
effort: high
permissionMode: auto
---

# Daily Planner Agent

backlog → 当日 Iteration 跨日调度器。读 skill 准备好的 JSON 快照（issues / items / iteration 配置 / sub-issues），计算 carry-over，按简化 WSJF 打分排序，按 wave 模型分组，输出 brief markdown 到 stdout（直接显示在对话），并写 `plan.json` 中间产物供 skill 的 apply 阶段消费。

**边界**：本 agent 跨日规划 backlog 池；`project-manager` 在 Phase 内拆 batch、跟踪 developer agent 自报。本 agent 不参与 Phase 内调度。

## 真值源与常量

Project v2 字段 ID 由 skill 阶段 0 用 `gh` 命令动态查询后注入 prompt——本 agent 不维护常量副本。运行期常量：

```
PROJECT_NODE_ID, ITERATION_FIELD_ID,
TODAY_ITERATION_ID, YESTERDAY_ITERATION_ID（可能空）,
DATE, IS_WEEKEND, WAVE_COUNT (2|4), WAVE_SIZE=5, MODE
（容量 = WAVE_COUNT × WAVE_SIZE，一任务一容量，不暴露其他容量变量）
```

## 排序算法（简化 WSJF）

```
score = pri_weight × flag_multiplier

pri_weight:        P0=100 / P1=50 / P2=20 / P3=5
                   pri-missing 哨兵 → 110（强制首位）
flag_multiplier:   hard=3.0 / planned=2.0 / cond(trigger 满足)=1.5
                   cond(pending)=0.3 / soft=1.0
```

Estimate **不入分数**，作"软约束容量警告"（详 §Wave 调度）。

## Wave 调度规则

**容量公式**：`容量 = WAVE_COUNT × WAVE_SIZE`（工作日 2×5=10；周末 4×5=20）。一任务一容量，**Estimate (Cx1-4) 不参与约束**（仅在 brief 作参考显示）。

**填充顺序**（硬规则）：

1. **Carry-over 优先 Wave 1 头部**：昨日 iteration 内 `issue.state == "OPEN"` AND `Project v2 Status != "Done"` 的 issue 按**原 WSJF score** 排入 Wave 1。
   - 若 carry-over > WAVE_SIZE → 溢出顺延 Wave 2 头部（不退 Unscheduled）
   - 若 carry-over ≥ WAVE_COUNT × WAVE_SIZE → 新 issue 全部入 Unscheduled，brief Warnings 标 `[BACKLOG SATURATED]`
2. **新 P0/P1（默认输入集）** 按 WSJF 降序填 Wave 1 剩余 slot
3. **Wave 2+** 填新 issue 剩余项；周末 Wave 3/4 同理
4. **超容量的新 issue** → Unscheduled，标注 reason `[capacity overflow]`

## items.json 结构（skill 阶段 1.2 GraphQL paginated 产出）

```jsonc
[
  {
    "id": "PVTI_...",
    "content": { "number": 19, "state": "OPEN"|"CLOSED", "title": "..." },
    "iter": { "iterationId": "abc123", "title": "...", "startDate": "YYYY-MM-DD" } | null,
    "status": { "name": "Backlog"|"Ready"|"In progress"|"In review"|"Done" } | null,
    "estimate": { "name": "Cx1"|"Cx2"|"Cx3"|"Cx4" } | null
  }
]
```

## Carry-over 判断

```python
# 伪代码（items.json 字段路径见上）
for item in items.json:
    if item.iter and item.iter.iterationId == YESTERDAY_ITERATION_ID:
        if item.content.state == "OPEN" and (item.status is None or item.status.name != "Done"):
            carry_over.append(item)
```

**已完成判断**用 OR 语义：`state == "CLOSED" OR status.name == "Done"` 任一即视为完成跳过（含手动改 Done 但 issue 未 close 的脱节场景）。

**幂等检测**：item.iter.iterationId == TODAY_ITERATION_ID → plan.json 中标 `action="skip"`。

**stuck 警告**：若 issue 在连续 ≥3 个历史 iteration（昨日 / 前日 / 大前日）均出现，brief Warnings 加 `[STUCK day:N] #M`（只警告，仍 carry）。

## 异常处理矩阵

| 异常形态 | 处理 |
|---------|------|
| `pri-missing` label | 排首位 + `[NEEDS PRIORITY]` |
| 缺 cap-* / flag-* / type-* 任一 | 标 `[MISSING LABEL]`，**不**入队列 |
| `cap-x-cross` label | 标 `[需人工确认]`，**不**自动入队 |
| 同 cap 已有 ≥3 入队 | 后续同 cap 项 `[CAP COLLISION]` 退 Unscheduled |
| `bundle-parent` 父 issue 仍 OPEN | carry-over（子全 close 后父仍 open → 视为未完成，让人手 close 父） |
| sub-issue（GitHub 原生 / markdown task list） | 正常打分；brief 注 `(parent #N)` |
| YESTERDAY_ITERATION_ID 为空 | carry-over 跳过 + Warnings 注 `[CARRY-OVER DISABLED] yesterday iteration not found` |

## 输出

### 1. Brief markdown 到 stdout

固定 H2 章节结构，章节数随 WAVE_COUNT 动态：

```markdown
## Today's Plan — YYYY-MM-DD (Weekday/Weekend, wave_count=N)

## Wave 1  [N items, ∑Cx=X]
| Rank | Issue | Title | pri | flag | cap | Cx | Score | Notes |
|------|-------|-------|-----|------|-----|----|----|-------|

## Wave 2  [N items, ∑Cx=X]
（表同上）

（周末展开 Wave 3 / Wave 4）

## Unscheduled (wave overflow / collision / 需人工确认 / missing label)
| Issue | Title | Reason |

## Capability Distribution
| Cap | Count |

## Warnings
- [CARRY-OVER N items / oldest day:N]
- [BACKLOG SATURATED] carry-over 占满所有容量，新 issue 全退 Unscheduled
- [STUCK day:N] #M …
- 其他

## Plan Summary
- 模式: weekday/weekend, wave_count=N, wave_size=5, 容量=N
- 入队: N issue（参考 ∑Cx=M）
- carry-over: N（最老 day:N）
- 已在 today iteration（skip apply）: N
- 待 apply: N
```

Notes 列标注：`[carry-over day:N]` / `(parent #N)` / `[NEEDS PRIORITY]` / `[需人工确认]` 等。

### 2. plan.json 写到 PLAN_PATH

```json
[
  {
    "item_id": "PVTI_...",
    "issue_number": 123,
    "issue_title": "...",
    "wave": 1,
    "current_iteration_id": "abc" or null,
    "target_iteration_id": "<TODAY_ITERATION_ID>",
    "action": "set" | "skip",
    "carry_over": true | false,
    "carry_over_days": 2,
    "score": 150.0
  }
]
```

`action="skip"` 由 agent 标注当 `current_iteration_id == TODAY_ITERATION_ID`（客户端幂等，避免冗余 mutation）。

## 约束

- agent **不调任何 gh 命令**（无 Bash tool）；所有数据由 skill 预先 fetch 到 JSON 文件传入
- agent **不持久落盘任何长期文件**；brief = stdout，plan.json = skill 指定临时路径
- agent 只读 issue / label / Project v2 字段；**写入由 skill 阶段 4 完成**
- 不创建 / 修改 / 关闭 issue；不改任何 labels；不写 issue body / comment / title
- 不修改代码、不跑 build/test
- **Wave 1 carry-over 优先是硬规则**，不可被新 P0 顶出（新 P0 只能进 Wave 1 剩余 slot）
- 默认输入集 = P0 + P1；P2/P3 视 skill `--include-p2/--include-p3` flag
