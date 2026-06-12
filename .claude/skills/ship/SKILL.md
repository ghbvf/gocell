---
name: ship
description: "全流程实施：探索→计划→worktree→TDD→实施→PR→review→/fix Cx1/Cx2→人工确认。L1(跳过探索,1 reviewer)/L2(单agent探索,1 reviewer)/L3(默认,三agent探索,按diff行数1/2/3/6 reviewer自动)"
argument-hint: "[--level=L1|L2|L3] <#issue-number 或任务描述>"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent, AskUserQuestion]
---

# GoCell Ship — 全流程实施

> **多沟通原则（默认多问、有歧义即停）**：L2/L3 在创建 worktree（阶段 3）**之前**必须完整呈现「方案方向
> （阶段 1）+ 改动计划（阶段 2）」并经 AskUserQuestion 确认——不在未对齐时就开工。实施中（阶段 5）surface
> 阶段性进度与 blocker；阶段 7→8 呈现内置 review findings 表，**Cx1/Cx2 IN_SCOPE 自动修**，仅 Cx3/Cx4 / 归属-取舍
> 不清才停下问。任何方案歧义 / 范围不清 / 取舍没把握 → 停下问，不默默假设。

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
gh pr edit <PR#> --add-label pr-status/in-progress   # 进入 ship→review→fix→check 流程（见 .github/project-template/PROJECT.md §5）
```

PR body 结构单源 = `.github/project-template/pull_request_template.md`；读模版填占位（`Refs: Closes #<ID>` + `ref: framework file`），不在技能内重述结构。本仓 PR 全程 CLI 创建，必须 `--body-file` 读填好的模版。

---

## 阶段 7：Review（内置 reviewer）

> ship 的 review 是 ship→review→fix→check 流程里的**内置首审**（6 维 reviewer）；外部再审（codex / `/pr-review`）由你在外部跑，
> 续修走 `/fix <PR#>`（见 `.github/project-template/PROJECT.md` §5）。ship 单独使用只做内置审。

**L1/L2**：1 个 `reviewer` agent（GoCell 六维度）。

**L3**：按 PR diff 净增删行数确定 `reviewer` agent 数量：

```bash
git -C worktrees/<wt> diff --shortstat origin/develop   # N files changed, X insertions(+), Y deletions(-)
```

diff 行数 = X + Y（缺项按 0 计）。阶段 7 被单独调用（无 worktree）时回退仓库根 `git diff --shortstat origin/develop`。

GoCell 六维度 = 架构合规 / 安全 / 测试 / 运维可观测 / DX / 产品。**reviewer 数 + 维度切分单源 = `.claude/agents/reviewer.md` §派发分档**（按上面算出的 diff 行数定档，区间左闭右开，边界归更高档）。

多 agent 时并行启动，每个 agent prompt 自包含其负责维度 + 必读 `.github/project-template/PROJECT.md` §3（P/Cx 评级单源）；全部完成后由主 agent 汇总去重 findings 表（含 Cx 分级）。

---

## 阶段 8：Fix（内置审 findings）+ 收尾

1. **先在对话窗口完整打印内置 review findings 表**（主输出：含 P/Cx 分级 + IN_SCOPE/OUT 归属 + 每条 `file:line`），再贴 pm:ship 评论留痕——窗口=主输出、评论=无损留痕，两者都做（输出纪律单源见 `PROJECT.md` §5）。
2. **Cx1/Cx2 IN_SCOPE findings 自动修**（派 `developer` agent 按 `fix` 的 [AUTO-FIX] 流程直接 Edit-Test 修——developer 无 Skill 工具，不真调 `/fix`，是复用其判定 + 修复循环；**不逐条问**）；Cx3/Cx4 遗留与 OUT_OF_SCOPE 不进主评论详表（移至下面步骤 4 的独立 pm:oos 评论）。**仅当**归属不清 / 取舍没把握 / 有 Cx3+ 需现在做时才 AskUserQuestion。
3. **推送 + 冲突预检（阻塞）**：`git -C worktrees/<wt> push` 推送内置修复 commits；按 `issues` B5 ① 先验无文件冲突（冲突则 merge origin/develop --no-edit 解冲突再 push）。冲突预检通过后**立即**执行步骤 4（不等 CI）。
4. **立即收尾（评论 + 状态，不等 CI）**（命令形态见 `issues` Part B；评论用 `.github/project-template/pr-comment.md` 的 `<!-- pm:ship -->` 模板，含 footer，**评论必留**）：
   - 贴 ship 评论（命令 + **回显 comment URL/id** 见 `issues` B4）：IN_SCOPE findings 无损写入（无损约定见 `pr-comment.md`：每条带 `file:line`、详表入 `<details>`，供再审 / `/fix` 直接读取）；含 reviewer 数 / 已修 Cx1-Cx2 / 遗留 Cx3/Cx4 / OUT_OF_SCOPE 数量（OOS 仅一行指针：`🚦 OUT_OF_SCOPE（详见本 PR 的 pm:oos 评论）`，全量无损记录由步骤 5 独立 pm:oos 承载）。
   - **追加机器块**（贴评论前，接口见 `pr-comment.md` §机器块）：`bash hack/automation/pr-meta.sh emit-block --kind=ship --pr=<PR#> --findings='<计数 json>'`（phase/verdict/round/refs/session/worktree 全派生），输出单行追加到填好的 `pm:ship` body 末尾（footer 之后），再走 `issues` B4 贴评论。
   - `gh pr edit` 切 `pr-status/needs-review-again`（移除 `pr-status/in-progress`）——review-side 执行器从此刻可立即开始，无需等待 CI。
5. **CI 异步收敛（非阻塞收尾，步骤 4 完成后执行）**：按 `issues` B5 ② 等 CI 收敛 + 失败回 `fix` 修复循环再推再等（时限 / 3 轮熔断单源在 B5 ②）；CI 收敛后**贴独立 pm:ci 评论**（用 `.github/project-template/pr-comment.md` 的 `<!-- pm:ci -->` 模板）：
   - 全绿 → `verdict=ci-green`；B5 ② 熔断仍红 → `verdict=ci-failed`（含失败 check 摘要 + run 链接）。
   - **追加机器块**（贴评论前）：`bash hack/automation/pr-meta.sh emit-block --kind=ci --pr=<PR#> --ci='{"failedChecks":[{"name":"…","url":"…"},…],"passedChecks":<n>,"totalChecks":<m>}'`（verdict 由 failedChecks 派生、round carry），输出追加到 pm:ci body 末尾，再走 `issues` B4 贴评论。
6. **OOS findings → 自动建 issue + 独立 pm:oos 评论**（仅当有 OUT_OF_SCOPE findings 时）：
   - **逐条自动建 backlog issue**（建单单源命令见 `issues` B1）：从 finding 字段无损填 `.github/project-template/backlog.md` body（现状←证据+三维根因+影响 / 修复方向←三级方案种子 / Files←file:line 全集 / Source←PR #<PR#> F<k> + `Discovered via /ship`）；四轴标签派生：`cx`←finding `[Cx…]` tag、`area`←finding 文件路径（PROJECT.md §2.1 path-glob）、`type`←finding 性质、`pri`←finding `[P…]`（无则默认 `pri-p2`）；先 `bash hack/automation/issue-labels.sh validate --labels "backlog,pri-pX,area-XX,type-XX,cx-X"` 过四轴门，再 `gh issue create …`，回显 issue #N/URL。
   - **安全闸门（不自动建 → 标 deferred）**：`pri-p0`（incident：线上故障/数据完整性/CVE）→ 停下 AskUserQuestion 确认后再决定；`issue-labels.sh validate` 失败（area/type 判不定）→ 该条标 `deferred=labels-underivable`（**机器块 item 仍写 `deferred`，XOR funnel 照常满足**），pm:oos 正文回退打印草稿命令待人工。
   - **追加机器块**（贴评论前，用 `<!-- pm:oos -->` 模板）：`bash hack/automation/pr-meta.sh emit-block --kind=oos --pr=<PR#> --oos='{"items":[{"fileLine":"…","rootCause":{"code":"…","arch":"…","history":"…"},"solutionSeeds":{"minimal":"…","thorough":"…","refactor":"…"},"issue":"#<N>"},…]}'`——**每个 item 必须带 `issue`（已建号/URL）或 `deferred`（`pri-p0-incident`｜`labels-underivable`）之一**，否则 emit-block 拒绝（Hard 闸门：没建 issue 也没显式 defer 就发不出 pm:oos 评论）。输出追加到 pm:oos body 末尾，再走 `issues` B4 贴评论；评论正文每条回填 `✅ 已建 #N <url>` 或 `🟡 deferred:<原因>`。
7. **监控建议（用户驱动，可选）**：所有评论 + label 操作完成后，窗口打印一行建议供用户启动 review-side 监控——`运行 /loop 30m /pr-monitor <PR#>`（report 模式；`/loop` 简单 loop 每 30min 调一次**无状态**的 `/pr-monitor` 检查 review 进展，human-in-loop 可随时停）。**ship 不自己启动 loop**（一次性会话结束后 in-session loop 即消亡）——循环交给内建 `/loop` 原语。

> ship 到此结束（内置审 + 修；评论 + 状态已先行；CI 异步收敛 + OOS 独立贴）。再审（codex / `/pr-review`）后，续修走 `/fix <PR#>`。

---

## 阶段 9：人工确认

```
PR: #<编号> <URL>
评论: <pm:ship 评论 URL，含 #issuecomment-<id>（来自 issues B4 回显）>
已完成：TDD / 实施 / PR / review（实跑 reviewer 数：按 diff 1/2/3/6 自动） / Cx1-Cx2 fix / CI 绿

未处理问题（需人工确认）——本表仅摘要 + 指针；完整无损详表（证据/三维根因/三级方案种子）见 pm:ship
评论的 `<details>`；OUT_OF_SCOPE findings **已自动建 issue**（#N / deferred 原因见本 PR 的 pm:oos 评论）：
| # | Finding (file:line) | Cx | 归属 | 建议方案 | 原因 |
|---|---------------------|----|------|---------|----|
```

---

## 约束

- lint 0 issues 才 push；不 `--no-verify`；不 amend 已 push commit
- worktree merge 后提示用户手动 `git worktree remove`，不自动删除
