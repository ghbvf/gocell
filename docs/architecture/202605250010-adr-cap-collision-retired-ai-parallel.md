# ADR: CAP COLLISION 退役 — AI-parallel 真实约束重塑

- 日期：2026-05-25
- 状态：Accepted
- 适用范围：`.claude/agents/daily-planner.md`（调度算法）；`.claude/skills/daily-planner/`（计划产物 schema）
- 关联：issue #934（本变更）、PR #926（daily-planner 落地）、`.claude/agents/daily-planner.md`（algorithm STEP 1-7）、`.claude/skills/daily-planner/SKILL.md`

## Context

PR #926 落地 daily-planner，调度算法含一条 **CAP COLLISION** heuristic：当同一 capability-tag（`cap-XX` label）下已有 ≥3 个 issue 排入当前 wave 时，触发 `[CAP COLLISION]` 标注并将后续同-cap issue 推迟（退出当前 wave 分配循环）。

这条规则的隐含前提是：

- 同一 cap 对应一位人类 reviewer / 实施者的认知负荷上限
- 3 件并行超出单人带宽，需压栈

在 GoCell AI-driven 多 worktree 并行场景下，这个前提不再成立：

1. **实施者是 AI**：不存在"认知负荷"这一人类心理模型概念；AI 并发执行多个 worktree 时容量由机器资源和 wave size 决定，不由 cap 分组决定。
2. **同 cap 不等于文件冲突**：`cap-14`（daily-planner）下的两个 issue 可能修改完全不同的文件，不会相互阻塞。CAP COLLISION 会错误地串行化本可并行的工作。
3. **文件冲突才是真实约束**：`conflict_group`（C2c）基于 `affected_paths` 前缀重合做 union-find，直接建模文件级冲突——同组串行，异组并行。这是 CAP COLLISION 试图近似但精度更低的同一底层约束。
4. **逐-cap 压栈无 AI 对应物**：多 worktree 场景中没有"cap 维度的 CI/review 带宽"这一共享瓶颈的运维等价物；reviewer 带宽不按 cap 分桶管理。

issue #934 C2a 要求重审 CAP COLLISION 语义，激进决议：彻底删除。

## Decision

**删除 CAP COLLISION heuristic**，不留任何降级路径：

- 删除 `daily-planner.md` 中的逐-cap 计数、`[CAP COLLISION]` 标注逻辑、per-cap wave 退场分支。
- 不留 `--cap-threshold` 等空头可配置参数（参数化一个无 AI 对应物的 heuristic 只是给未来维护者留坑）。
- 不留 `[CAP COLLISION]` 代码路径（包括注释掉的版本）——删除即删除。
- 算法重构为两正交轴（见下）：

**新两轴调度模型**：

| 轴 | 载体 | 说明 |
|----|------|------|
| 拓扑正确性序 | `blocked_by` DAG（C2b）→ STEP 4/5 | 依赖关系决定 wave placement；blocker.wave ≤ dependent.wave |
| 执行冲突分组 | `conflict_group`（C2c）→ STEP 6 | `affected_paths` 前缀重合做 union-find；同组冲突串行，异组可并行 |

容量由 `WAVE_COUNT × WAVE_SIZE` 单独 governing，与 cap label 无关。

## AI-robust 评级

本变更是**删除**一个 agent prompt heuristic（agent.md 内的 markdown 算法文本），不是新增 enforcement 机制。ai-robust 章程（`.claude/rules/gocell/ai-robust.md`）适用范围为"新增/修改约束 enforcement 机制（archtest / governance rule / codegen funnel / type marker）"——删除 heuristic 不在范围内，**不评级**。

新引入的 enforcement：CI smoke job（`pr-check.yml skill-daily-planner-smoke`）= 测试行为，ai-robust §适用范围外，同样不评级。

`apply-gate.sh` 新增的 `conflict_group`（必填 + int ≥ 1 校验）与 `wave_option_id`（已知 option ID 成员校验）runtime invariant guard：这两条是 **bootstrap 期 fail-fast 校验**（shell 脚本在执行前验证 plan.json schema，校验失败 exit 1 阻断后续操作），对应 ai-robust 章程"governance rule — bootstrap 期 fail-fast 校验"载体，评级 **Medium**（runtime guard，依赖运行时执行校验脚本；非 Go type system Hard，无 archtest 静态锁）。

## 威胁模型

删除 CAP COLLISION 后各风险的覆盖情况：

| 威胁 | 删除前覆盖 | 删除后覆盖 | 结论 |
|------|-----------|-----------|------|
| 同 cap 资源（CI/review 带宽）超载 | CAP COLLISION 压栈（≤3/cap/wave） | wave size 上限（`WAVE_COUNT × WAVE_SIZE`）全局容量兜底 | 接受代价：AI-parallel 吞吐优先，逐-cap 压栈无 AI 对应物；wave 容量是更直接的约束 |
| 文件冲突导致 worktree 互相阻塞 | 无（CAP COLLISION 是 cap 维度，不是文件维度） | `conflict_group` union-find（C2c）直接覆盖（同组冲突串行） | 替代机制更精确 |
| 依赖倒序（下游先 ship，上游还未 done） | 无 | `blocked_by` DAG（C2b）topo sort，blocker.wave ≤ dependent.wave | 新增覆盖 |
| AI 写入过多 issue 致 review 积压 | CAP COLLISION 间接限速 | wave size 参数（`WAVE_SIZE`）显式控制；用户可调低 | wave size 是更直接的旋钮，无需 cap 维度代理 |

删除后无威胁从"有覆盖"退化为"无覆盖"且无替代——表格中"接受代价"一行是有意识的权衡，不是疏漏。

## Out of scope

- C2b blocked-by DAG 的 GraphQL 字段确认（独立实施，同 PR）
- `conflict_group` 的 apply-gate.sh schema 校验（C2c，同 PR）
- wave size 参数的可配置 UI（已存在，不在本 ADR 范围）
- ship `--from-plan` 模式（C2d，同 PR）

## Related

- issue #934：AI-parallel 升级束全量实施（C1-C4）
- PR #926：daily-planner 初始落地（CAP COLLISION 首次引入）
- `.claude/agents/daily-planner.md`：STEP 1-7 算法（本 ADR Decision 的直接实施载体）
- `.claude/skills/daily-planner/SKILL.md`：skill 执行脚本
