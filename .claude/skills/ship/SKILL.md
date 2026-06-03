---
name: ship
description: "全流程实施：探索→计划→worktree→TDD→实施→PR→review→/fix Cx1/Cx2→人工确认。L1(跳过探索,1 reviewer)/L2(单agent探索,1 reviewer)/L3(默认,三agent探索,按diff行数1/2/3/6 reviewer自动)"
argument-hint: "[--level=L1|L2|L3] <#issue-number 或任务描述>"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent, AskUserQuestion]
---

# GoCell Ship — 全流程实施

默认 L3（三 agent 探索 + 详细计划 + 与用户确认 + 按 diff 行数自动 1/2/3/6 reviewer，见阶段 7）。

> 所有 `gh` / `git push` 命令用 `dangerouslyDisableSandbox: true`（CLAUDE.md 全局总则）。

> **多沟通原则（默认多问、有歧义即停）**：L2/L3 在创建 worktree（阶段 3）**之前**必须完整呈现「方案方向
> （阶段 1）+ 改动计划（阶段 2）」并经 AskUserQuestion 确认——不在未对齐时就开工。实施中（阶段 5）surface
> 阶段性进度与 blocker；阶段 7→8 之间呈现内置 review findings 表再决定修哪些。任何方案歧义 / 范围不清 / 取舍
> 没把握 → 停下问，不默默假设。

剥离 `--level=` flag 后，剩余参数匹配 `^#?[0-9]+$` 时视为 issue 号，先 `gh issue view <N> --json title,body,labels,state` 拉取作为任务上下文；后续阶段以 issue title/body 替代自由文本任务描述，阶段 6 PR body 追加 `Closes #<N>`。`state != "OPEN"`（CLOSED / MERGED 等）或 `gh issue view` 失败均用 AskUserQuestion 让用户裁定是否继续。

## 等级

| 等级 | 探索 | 计划确认 | 实施 agent | review |
|------|------|---------|-----------|--------|
| L1 | 不探索 | 不需要 | 1-2 并行 | 1 reviewer |
| L2 | 1 explorer | 展示给用户 | 1-2 并行 | 1 reviewer |
| L3（默认） | 3 并行 explorer | AskUserQuestion 确认 | ≤ 4 并行 | 1/2/3/6 reviewer（按 diff 行数自动，见阶段 7） |

---

## 阶段 1：探索（L1 跳过）

**L2**：启动 1 个 `explorer` agent，研究对标开源项目实现方案，查 `docs/references/framework-comparison.md` 找 primary 对标框架，用 WebFetch 拉取源码（`raw.githubusercontent.com`），提取接口签名、生命周期、错误处理关键设计，输出采纳建议和偏离理由。

**L3（默认）**：并行启动 3 个 `explorer` agent：
1. **对标开源项目实现方案**
2. 测试策略（table-driven / 集成 / benchmark 覆盖模式）
3. 边界条件与安全处理

全部完成后按「方案与计划原则（含自检）」汇总并逐条自查，再**用 AskUserQuestion 与用户确认方案方向**后继续。

---

### 方案与计划原则（含进入下一步前的自检）

阶段 1 汇总 / 阶段 2 计划必须满足下列原则；L3 在 AskUserQuestion 前逐条自查，任一不通过 → 在确认问题中**显式列出取舍及理由**，不默认放行：

- **彻底**：根因 + 完整解法，范围内紧密相关的小工作一并纳入。自查「是否还藏 TODO/FIXME/follow-up、兼容代码、未列入范围的关联工作？」→ 合并进当前 PR 或写明 blocker 理由。
- **不向后兼容**：删字段/改签名/换实现直接做。自查「是否留了 deprecation 别名、旧字段、兼容 shim、双路径？」→ 删掉或写明保留理由。
- **优雅简洁**：最少代码改动达成目标，不引入新抽象层、不预设未来需求。自查「能否用更少的代码/抽象/新文件达成同样目标？」→ 简化或写明保留理由。
- **开源对标**：做了嘛，方向正确吗。

---

## 阶段 2：计划

按「方案与计划原则（含自检）」生成改动文件清单（按依赖顺序）、任务分组（串行/并行批次）、TDD 测试先写清单、对标参考（`ref: framework file`）。生成后逐条自查，L3 用 AskUserQuestion 与用户确认计划后继续。

**并行批次分析**（改动文件 ≥ 4 时必须在计划中明确）：
- 标注各任务的文件归属和批次编号
- 标注批次间依赖关系（有依赖 → 串行；无依赖 → 可并行）
- 解决同文件冲突：同一文件必须归入同一批次/agent

---

## 阶段 3：Worktree

基于 `origin/develop` 创建（依照 `git-worktree` skill 约定）：

```bash
git fetch origin
git worktree add worktrees/<Type>/<issue#-short-name> -b <Type>/<issue#-short-name> origin/develop
```

命名依 `git-worktree` skill：**编号 = 关联 issue#**（无 issue 不编号），path 与分支首段含 **Type**（Feature/Fix/Refactor/Docs/Experiment）。下文 `worktrees/<wt>` 简写指该 worktree 目录。

---

## 阶段 4：TDD — 先写测试

在 worktree 中先写 `*_test.go`，覆盖正常/边界/错误路径（kernel/ ≥ 90%，其余 ≥ 80%）。运行 `go -C worktrees/<wt> test ./...` 确认测试先 **FAIL**，再进入实施。

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
- worktree 路径（`worktrees/<wt>`）
- 分配的任务列表（文件路径 + 改动描述）
- go 命令格式：`go -C worktrees/<wt> test ./...`
- CLAUDE.md 关键约束（分层规则、覆盖率要求）
- commit 格式：`<type>(<scope>): <描述>`

每个 sub-agent 在自己负责的任务上**串行**执行 Edit-Test Loop，完成后跑 `golangci-lint run ./...`（0 issues 才 commit）。

### 5.2 主 agent 汇总（所有并行 agent 完成后）

```bash
go -C worktrees/<wt> build ./...
go -C worktrees/<wt> test ./...
golangci-lint run ./...   # 0 issues 才进阶段 6
```

---

## 阶段 6：PR

```bash
git -C worktrees/<wt> push -u origin <branch>
gh pr create --title "..." --body-file <填好的 pull_request_template.md>
gh pr edit <PR#> --add-label pr-status/in-progress   # 进入 ship→codex→fix 流程（见 .github/project-template/PROJECT.md §5）
```

PR body 结构单源 = `.github/project-template/pull_request_template.md`（Summary / Refs / Test plan）；读模版填占位（`Refs: Closes #<ID>` + `ref: framework file`），不在技能内重述结构。本仓 PR 全程 CLI 创建，必须 `--body-file` 读填好的模版。

---

## 阶段 7：Review（内置 reviewer）

> ship 的 review 是 ship→codex→fix 流程里的**内置首审**（6 维 reviewer）；codex 外部 review 由你在外部跑，
> 续修走 `/fix <PR#>`（见 `.github/project-template/PROJECT.md` §5）。ship 单独使用只做内置审。

**L1/L2**：1 个 `reviewer` agent（GoCell 六维度）。

**L3**：按 PR diff 净增删行数确定 `reviewer` agent 数量。用 `--shortstat` 直接读增删行（避开 `--stat` 末行格式坑，也无需 awk 求和）：

```bash
git -C worktrees/<wt> diff --shortstat origin/develop
```

输出形如 `N files changed, X insertions(+), Y deletions(-)`；diff 行数 = X + Y（某项为 0 时该子句省略，按 0 计）。

> 阶段 7 被单独调用（非 ship 完整流程、无 worktree）时，回退到仓库根执行 `git diff --shortstat origin/develop`。

GoCell 六维度 = 架构合规 / 安全 / 测试 / 运维可观测 / DX / 产品。**reviewer 数 + 维度切分单源 = `.claude/agents/reviewer.md` §派发分档**（按上面算出的 diff 行数定档，区间左闭右开，边界归更高档）。

多 agent 时并行启动，每个 agent prompt 自包含其负责维度 + 必读 `.github/project-template/PROJECT.md` §3（P/Cx 评级单源）；全部完成后由主 agent 汇总去重 findings 表（含 Cx 分级）。

---

## 阶段 8：Fix（内置审 findings）+ 收尾

1. **呈现内置 review findings 表**（含 P/Cx 分级 + IN_SCOPE/OUT 归属）。L3 用 AskUserQuestion 与用户确认
   修哪些（默认修 Cx1/Cx2 IN_SCOPE）。
2. 对 Cx1/Cx2 IN_SCOPE findings 派发 `developer` agent 执行 `/fix <finding>`；Cx3/Cx4 和 OUT_OF_SCOPE 收集到阶段 9。
3. **收尾**（命令形态见 `issues` Part B；评论用 `.github/project-template/pr-comment.md` 的 `<!-- pm:ship -->` 模板，含 footer，**评论必留**）：
   - `gh pr comment <PR#>` 贴 ship 评论（reviewer 数 / findings 表 / 已修 Cx1-Cx2 / 遗留 / OUT_OF_SCOPE / 下一步=待 codex）→ 把 `gh pr comment` 返回的评论 URL **回显给用户**。
   - `gh pr edit` 切 `pr-status/needs-codex`（移除 `pr-status/in-progress`）。

> ship 到此结束（内置审 + 修）。codex 外部 review 后，续修走 `/fix <PR#>`。

---

## 阶段 9：人工确认

```
PR: #<编号> <URL>
已完成：TDD / 实施 / PR / review（实跑 reviewer 数：按 diff 1/2/3/6 自动） / Cx1-Cx2 fix / CI

未处理问题（需人工确认）：
| # | Finding | Cx | 建议方案 | 原因 |
|---|---------|----|---------|----|
```

---

## 约束

- lint 0 issues 才 push；不 `--no-verify`；不 amend 已 push commit
- worktree merge 后提示用户手动 `git worktree remove`，不自动删除
