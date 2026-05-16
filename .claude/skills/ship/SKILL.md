---
name: ship
description: "全流程实施：探索→计划→worktree→TDD→实施→PR→review→/fix Cx1/Cx2→人工确认。L1(跳过探索,1 reviewer)/L2(单agent探索,1 reviewer)/L3(默认,三agent探索,1 reviewer)/L4(三agent探索+6角色review)"
argument-hint: "[--level=L1|L2|L3|L4] <backlog-id 或任务描述>"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent, AskUserQuestion]
---

# GoCell Ship — 全流程实施

默认 L3（三 agent 探索 + 详细计划 + 与用户确认 + 1 reviewer）。L4 在 L3 基础上升级为 6 角色 review。

## 等级

| 等级 | 探索 | 计划确认 | 实施 agent | review |
|------|------|---------|-----------|--------|
| L1 | 不探索 | 不需要 | 1-2 并行 | 1 reviewer |
| L2 | 1 explorer | 展示给用户 | 1-2 并行 | 1 reviewer |
| L3（默认） | 3 并行 explorer | AskUserQuestion 确认 | ≤ 4 并行 | 1–3 reviewer（按 diff 行数，见阶段 7） |
| L4 | 3 并行 explorer | AskUserQuestion 确认 | ≤ 4 并行 | 6角色6个并行 |

---

## 阶段 1：探索（L1 跳过）

**L2**：启动 1 个 `explorer` agent，研究对标开源项目实现方案，查 `docs/references/framework-comparison.md` 找 primary 对标框架，用 WebFetch 拉取源码（`raw.githubusercontent.com`），提取接口签名、生命周期、错误处理关键设计，输出采纳建议和偏离理由。

**L3（默认）**：并行启动 3 个 `explorer` agent：
1. **对标开源项目实现方案**
2. 测试策略（table-driven / 集成 / benchmark 覆盖模式）
3. 边界条件与安全处理

全部完成后按"方案与计划原则"汇总，执行下方"反思自检"，再**用 AskUserQuestion 与用户确认方案方向**后继续。

---

## 方案与计划原则（阶段 1 汇总 / 阶段 2 计划必须满足）

- **彻底**：根因 + 完整解法，不留 TODO/FIXME/follow-up；范围内紧密相关的小工作一并纳入，不拆 P2/后续 PR
- **不向后兼容**：删字段/改签名/换实现直接做，不留 deprecation 别名、不留兼容 shim、不留旧路径
- **优雅简洁**：用最少的代码改动达成目标，不引入新抽象层、不预设未来需求
- **开源对标**：做了嘛，方向正确吗

### 反思自检（AskUserQuestion 前强制执行）

呈现给用户前，逐条自查并在确认问题中如实回答：

1. **彻底**：方案/计划里是否还藏着 TODO、兼容代码、未列入范围的关联工作？→ 合并进当前 PR 或写明 blocker 理由
2. **不向后兼容**：是否引入了 deprecation 别名、旧字段保留、双路径并存？→ 删掉或写明保留理由
3. **优雅简洁**：能否用更少的代码、更少的抽象、更少的新文件达成同样目标？→ 简化或写明保留理由

任一条不通过 → AskUserQuestion 中**显式列出取舍及理由**，不得默认放行。

---

## 阶段 2：计划（L1 跳过）

按"方案与计划原则"生成改动文件清单（按依赖顺序）、任务分组（串行/并行批次）、TDD 测试先写清单、对标参考（`ref: framework file`）。生成后执行"反思自检"，L3 用 AskUserQuestion 与用户确认计划后继续。

**并行批次分析**（改动文件 ≥ 4 时必须在计划中明确）：
- 标注各任务的文件归属和批次编号
- 标注批次间依赖关系（有依赖 → 串行；无依赖 → 可并行）
- 解决同文件冲突：同一文件必须归入同一批次/agent

---

## 阶段 3：Worktree

基于 `origin/develop` 创建（依照 `git-worktree` skill 约定）：

```bash
git fetch origin
git worktree add worktrees/<NNN-short-name> -b <branch-name> origin/develop
```

编号：Fix 200-299 / Feature 001-199 / Refactor 500-599，扫描 `worktrees/` + `git branch -a` 取最大 +1。

---

## 阶段 4：TDD — 先写测试

在 worktree 中先写 `*_test.go`，覆盖正常/边界/错误路径（kernel/ ≥ 90%，其余 ≥ 80%）。运行 `go -C worktrees/<NNN> test ./...` 确认测试先 **FAIL**，再进入实施。

---

## 阶段 5：实施

### 5.0 分组与并行度决策（实施前必须执行）

主 agent 根据阶段 2 的改动文件清单和批次依赖关系，**自主决定**：
- 哪些任务无文件交叉且无逻辑依赖 → 可并行启动 developer agent
- 哪些任务有依赖或改同一文件 → 串行或归入同一 agent

**硬约束**：
- 同一文件只能分给同一 agent（防写冲突）
- 有前置依赖的批次必须等上一批全部完成后再启动
- 并行 developer agent 上限 **4 个**

### 5.1 Sub-agent prompt 自包含要求

每个 developer sub-agent prompt 必须包含：
- worktree 路径（`worktrees/<NNN>`）
- 分配的任务列表（文件路径 + 改动描述）
- go 命令格式：`go -C worktrees/<NNN> test ./...`
- CLAUDE.md 关键约束（分层规则、覆盖率要求）
- commit 格式：`<type>(<scope>): <描述>`

每个 sub-agent 在自己负责的任务上**串行**执行 Edit-Test Loop，完成后跑 `golangci-lint run ./...`（0 issues 才 commit）。

### 5.2 主 agent 汇总（所有并行 agent 完成后）

```bash
go -C worktrees/<NNN> build ./...
go -C worktrees/<NNN> test ./...
golangci-lint run ./...   # 0 issues 才进阶段 6
```

---

## 阶段 6：PR

```bash
git -C worktrees/<NNN> push -u origin <branch>   # dangerouslyDisableSandbox: true
gh pr create --title "..." --body "..."
```

PR body 包含：Summary、`Refs: <ID>`、`ref: framework file`、Test plan checklist。

---

## 阶段 7：Review

**L1/L2**：1 个 `reviewer` agent（GoCell 六维度）。

**L3**：按 PR diff 净增删行数（`git -C worktrees/<NNN> diff --stat origin/develop` 末行 insertions+deletions）确定 `reviewer` agent 数量：

| diff 行数 | reviewer 数 | 维度切分 |
|-----------|------------|---------|
| < 200 | 1 | 单 agent 跑 GoCell 全六维度 |
| 200–600 | 2 | A：正确性 + 测试；B：安全 + 运维可观测 + 架构合规 + DX |
| 600–1500 | 3 | A：正确性 + 测试；B：安全 + 架构合规；C：运维可观测 + DX |
| > 1500 | — | 不在 L3 内强跑：提示用户拆 PR 或升 L4（6 角色并行） |

多 agent 时并行启动，每个 agent prompt 自包含负责维度；全部完成后由主 agent 汇总去重 findings 表（含 Cx 分级）。维度不重不漏，覆盖 GoCell 六维度全集。

**L4**：并行 6 个 `reviewer` agent（正确性 / 安全 / 测试 / 运维可观测 / 开发者体验 / 架构合规）。全部完成后汇总 findings 表（含 Cx 分级）。

Review结束后查看（`gh pr checks <编号>`），禁止自动循环等待CI结束
---

## 阶段 8：Fix

对 Cx1/Cx2 IN_SCOPE findings 派发 `developer` agent 执行 `/fix <finding>`；Cx3/Cx4 和 OUT_OF_SCOPE 收集到阶段 9。

---

## 阶段 9：人工确认

```
PR: #<编号> <URL>
已完成：TDD / 实施 / PR / 6角色review / Cx1-Cx2 fix / CI

未处理问题（需人工确认）：
| # | Finding | Cx | 建议方案 | 原因 |
|---|---------|----|---------|----|
```

---

## 约束

- lint 0 issues 才 push；不 `--no-verify`；不 amend 已 push commit
- `git push` 用 `dangerouslyDisableSandbox: true`
- worktree merge 后提示用户手动 `git worktree remove`，不自动删除
