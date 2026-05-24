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
DATE, IS_WEEKEND, WAVE_COUNT (2|4), WAVE_SIZE=5,
CARRY_OVER_DISABLED (true 时 brief Warnings 必加 [CARRY-OVER DISABLED]),
MODE (apply|dry-run),
WAVE_FIELD_ID（可能空，C3c），wave→wave_option_id 映射（阶段 0 注入）
（容量 = WAVE_COUNT × WAVE_SIZE）
```

## 排序算法（简化 WSJF）

```
score = pri_weight × flag_multiplier

pri_weight:        P0=100 / P1=50 / P2=20 / P3=5
                   pri-missing 哨兵 → 110（强制首位）
flag_multiplier:   hard=3 / planned=2 / cond(trigger 满足)=1.5
                   cond(pending)=0.3 / soft=1
```

## 调度算法（STEP 1-7）

`容量 = WAVE_COUNT × WAVE_SIZE` = 工作日 10 / 周末 20。

**两正交轴**：拓扑（正确性序，STEP 4/5）约束先后顺序；conflict_group（文件冲突集，STEP 6）末端派生——同 conflict_group 表示共享文件、异 conflict_group 表示文件独立。conflict_group 是 **advisory 显示列**（ship --from-plan 移除后无机器消费者，仅供人工参考）。容量由 wave size 单独 governing，无 per-cap 阈值。

```
STEP 1  解析每 issue：score=pri_weight×flag_multiplier（WSJF 不变）；
        affected_paths[] 从 body "### Affected paths"（解析失败→brief Warnings [AFFECTED PATHS MALFORMED]）；
        blockers[] 从 deps.json 的 blocked_by。

STEP 2  carry-over 集（不变）：昨日 iter + OPEN + not Done → Wave 1 头部。

STEP 3  候选序 = carry-over(WSJF desc) ++ 新 issue(WSJF desc)。

STEP 4  拓扑排序（wave 填充前）：in-scope blocked_by 建 DAG。
        环 → 不阻塞 + Warnings [DEP CYCLE]（降级，不崩）；
        跨 iteration 不可解 → Warnings [DEP CROSS-ITERATION]，dependent 仍可排但标记。

STEP 5  wave 填充：Wave1 头=carry-over，后 WSJF desc，容量=WAVE_COUNT×WAVE_SIZE；
        placement 守拓扑：blocker.wave ≤ dependent.wave
        （dependent 顺延到其最晚 blocker 之后，最小化）；
        carry-over 不退 Unscheduled；
        新 issue 溢出 → Unscheduled [capacity overflow]；
        carry-over ≥ 全容量 → 新 issue 全 Unscheduled，Warnings [BACKLOG SATURATED]。

STEP 6  conflict_group（wave 内，**advisory 显示分组**）：每 wave 对 affected_paths
        前缀重合做 union-find，每连通分量=一个 conflict_group（共享文件的 item 集）；
        空 affected_paths（wildcard）：footprint 未知 → 与同 wave 内所有 item union →
        整 wave 落入同一 conflict_group；
        brief 对该 item 加 Warning [AFFECTED PATHS MISSING — shared footprint]。
        编号：全 plan 全局唯一 int，(wave 升序, 首次出现) 从 1 分配；
        同 conflict_group = 共享文件，异 conflict_group = 文件独立；跨 wave 不共组。
        **无机器消费者**——ship --from-plan 已移除（ADR 202605250010），此列仅供人工
        参考"哪些 issue 碰同一批文件"，不再驱动任何串/并行执行。

STEP 7  输出 brief + plan.json，每 entry 含 conflict_group(int) + wave_option_id(str)。
```

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

### 已完成判断 + 幂等

用 OR 语义：`state == "CLOSED" OR status.name == "Done"` 任一即视为完成跳过（含手动改 Done 但 issue 未 close 的脱节场景）。

**幂等检测**：item.iter.iterationId == TODAY_ITERATION_ID → plan.json 中标 `action="skip"`。

**CARRY_OVER_DISABLED**：skill 阶段 2.3 检测到 YESTERDAY_ITERATION_ID 为空（首日 / 假期跳过）时设 true；agent 跳过 carry-over 计算，brief Warnings 章节追加 `[CARRY-OVER DISABLED] yesterday iteration not found`。

## 异常处理矩阵

| 异常形态 | 处理 |
|---------|------|
| `pri-missing` label | 排首位 + `[NEEDS PRIORITY]` |
| 缺 cap-* / flag-* / type-* 任一 | 标 `[MISSING LABEL: cap]` / `[MISSING LABEL: flag]` / `[MISSING LABEL: type]`（多缺合并 `[MISSING LABEL: cap,flag]`），**不**入队列 |
| `cap-x-cross` label | 标 `[需人工确认]`，**不**自动入队 |
| `bundle-parent` 父 issue 仍 OPEN | carry-over（子全 close 后父仍 open → 视为未完成，让人手 close 父） |
| sub-issue（GitHub 原生 sub-issue API） | 正常打分；brief 注 `(parent #N)`。markdown body task list 形态**不**识别（升级到原生 sub-issue 才能被追踪） |
| YESTERDAY_ITERATION_ID 为空 | carry-over 跳过 + Warnings 注 `[CARRY-OVER DISABLED] yesterday iteration not found` |
| 输入集 + carry-over 全空 | Warnings 注 `[EMPTY INPUT SET]`；brief 显示空 Wave；plan.json = `[]` |
| blocked_by 成环 | Warnings 注 `[DEP CYCLE]`；涉及 issue 不阻塞，正常排入候选序 |
| blocked_by 跨 iteration 不可解 | Warnings 注 `[DEP CROSS-ITERATION]`；dependent 仍排入当前候选序并标记 |
| affected_paths 解析失败 | Warnings 注 `[AFFECTED PATHS MALFORMED]`；该 issue 走 wildcard 语义：与同 wave 所有 item union → 整 wave 归同一 conflict_group（advisory） |
| affected_paths 字段缺失 | Warnings 注 `[AFFECTED PATHS MISSING — shared footprint]`；同 wildcard 语义：整 wave 落入同一 conflict_group（advisory，无执行串行化） |

## 输出

### 1. Brief markdown 到 stdout

固定 H2 章节结构，章节数随 WAVE_COUNT 动态：

```markdown
## Today's Plan — YYYY-MM-DD (Weekday/Weekend, wave_count=N)

## Wave 1
| Rank | Issue | Title | pri | flag | cap | Cx | Score | Group | Notes |
|------|-------|-------|-----|------|-----|----|----|-------|-------|

## Wave 2
（表同上，含 Group 列）

（周末展开 Wave 3 / Wave 4）

## Unscheduled (wave overflow / 需人工确认 / missing label)
| Issue | Title | Reason |

## Capability Distribution
| Cap | Count |

## Warnings
- [CARRY-OVER N items]
- [BACKLOG SATURATED] carry-over 占满所有容量，新 issue 全退 Unscheduled
- [CARRY-OVER DISABLED] yesterday iteration not found（当 CARRY_OVER_DISABLED=true）
- [EMPTY INPUT SET] 输入池为空（当输入 + carry-over 全 0）
- [DEP CYCLE] #N → #M → #N（涉及 issue 编号）
- [DEP CROSS-ITERATION] #N blocked by #M（M 在其他 iteration）
- [AFFECTED PATHS MALFORMED] #N（解析失败，wildcard 语义 → 整 wave 同 conflict_group）
- [AFFECTED PATHS MISSING — shared footprint] #N（字段缺失，wildcard 语义 → 整 wave 同 conflict_group）
- [capacity overflow] N issue 超出容量退 Unscheduled
- 其他

## Plan Summary
- 模式: weekday/weekend, wave_count=N, wave_size=5, 容量=N
- 入队: N issue
- carry-over: N
- 已在 today iteration（skip apply）: N
- 待 apply: N
- conflict groups: N（全 plan 唯一 int 范围 1..M；advisory 显示分组，同组=共享文件、异组=文件独立；无机器消费者）

> Group 列说明：数字 = 受影响文件前缀重合分组（advisory）。同号 issue 触碰同一批文件，
> 异号互不重叠。仅供人工参考，**无自动串/并行约束**（ship --from-plan 已移除）。
```

Notes 列标注：`[carry-over]` / `(parent #N)` / `[NEEDS PRIORITY]` / `[需人工确认]` 等。Group 列标注 `conflict_group` int 值（advisory）：同 int 的 issue 共享文件、不同 int 的 issue 文件独立——仅供人工参考，无机器消费者驱动串/并行。

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
    "score": 150.0,
    "conflict_group": 1,
    "wave_option_id": "<wave_option_id_from_stage0>"
  }
]
```

字段说明：
- `action="skip"` 由 agent 标注当 `current_iteration_id == TODAY_ITERATION_ID`（客户端幂等，避免冗余 mutation）；`skip` 时 `wave_option_id` 可为 `""`。
- `conflict_group`：全 plan 全局唯一正整数（≥1），**advisory 显示列**。同值 issue 共享文件、不同值 issue 文件独立。跨 wave 不共组。由 STEP 6 union-find 在 wave 内按 affected_paths 前缀重合分组派生，(wave 升序, 首次出现) 从 1 起全局分配。空 affected_paths 走 wildcard 语义：与同 wave 所有 item union，整 wave 落入同一 conflict_group。**ship --from-plan 移除后无机器消费者**（ADR 202605250010）；`apply-gate.sh` 不再校验该字段，缺失或非整数不阻断 apply。续保留供人工读 brief Group 列；整链删除评估见 ADR 202605250010 §Amendment。
- `wave_option_id`：对应 Project v2 Wave 字段的 single-select option id，由 skill 阶段 0 查询后将 wave→option-id 映射注入 prompt，agent 按 entry 的 wave 值填写。`apply-gate.sh` 在 `WAVE_FIELD_ID` 非空时校验该值必须在已知 option id 集合内。

## 约束

- agent **不调任何 gh 命令**（无 Bash tool）；所有数据由 skill 预先 fetch 到 JSON 文件传入
- agent **不持久落盘任何长期文件**；brief = stdout，plan.json = skill 指定临时路径
- agent 只读 issue / label / Project v2 字段；**写入由 skill 阶段 4 完成**
- 不创建 / 修改 / 关闭 issue；不改任何 labels；不写 issue body / comment / title
- 不修改代码、不跑 build/test
- **Wave 1 carry-over 优先是硬规则**，不可被新 P0 顶出（新 P0 只能进 Wave 1 剩余 slot）
- 默认输入集 = P0 + P1；P2/P3 视 skill `--include-p2/--include-p3` flag
