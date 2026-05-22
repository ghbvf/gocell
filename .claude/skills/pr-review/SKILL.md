---
name: pr-review
description: "对指定 PR 跑一次自动分级六维度 review。按 diff 净增删行数自动分配 1/2/3/6 reviewer agent 并行；主 agent 做根因聚类 + Cx 分级 + 修复分流建议，不自动 fix。"
argument-hint: "<PR 编号>"
allowed-tools: [Read, Glob, Grep, Bash, Agent]
---

# GoCell PR Review — 自动分级六维度审查

按 PR diff 净增删行数自动分配 1/2/3/6 个 `reviewer` agent 并行做六维度审查，主 agent 做根因聚类与修复分流建议。**只 review，不自动 fix**——Cx1/Cx2 簇仅给出 `/fix` 建议命令，由用户决定。

---

## 阶段 1：输入解析

参数必须是 PR 编号：纯数字（`838`）或带井号（`#838`）。

- 缺参 → 立即输出 `错误：缺少 PR 编号；用法：/pr-review <PR 编号>`，不执行后续阶段
- 非法格式（含字母、空格、其他符号）→ 立即输出 `错误：参数 "<原值>" 不是合法 PR 编号`，不执行后续阶段
- **不进入交互**确认，保持调用简单

---

## 阶段 2：取 diff 行数

主路径（GitHub 已计算好，无需本地 checkout）：

```bash
gh pr view <N> --json additions,deletions --jq '.additions + .deletions'
```

`gh` 命令用 `dangerouslyDisableSandbox: true`（沿用 CLAUDE.md 约定）。

取不到 PR（404 / 权限） → 报错退出，提示用户检查 PR 编号与 `gh auth status`。

---

## 阶段 2.5：定位或自动创建 review worktree

reviewer agent 需要在某个 worktree 内做 Read/Grep（拿到 PR 改动后的全文件 + 追溯调用链）。流程：

```bash
BRANCH=$(gh pr view <N> --json headRefName --jq .headRefName)
WORKTREE=$(git worktree list --porcelain | awk -v b="refs/heads/$BRANCH" '/^worktree /{w=$2} $0=="branch "b{print w; exit}')
```

### 情况 A：找到既有 worktree（`$WORKTREE` 非空）

- 直接使用 `$WORKTREE` 绝对路径
- **不动用户的 worktree 状态**（可能有 work in progress），假定当前 HEAD 即 PR head
- 若需要校准到最新 PR head，提示用户手动 `git -C $WORKTREE pull --ff-only origin <BRANCH>`，不自动执行

### 情况 B：无既有 worktree → 自动创建 review-only worktree

```bash
git fetch origin <BRANCH>                                          # dangerouslyDisableSandbox: true
git worktree add --detach worktrees/review-pr<N> origin/<BRANCH>   # detached，不创建本地分支
WORKTREE="$(git rev-parse --show-toplevel)/worktrees/review-pr<N>"
```

- 用 `--detach` 避免创建本地分支（review 只读不写）
- 路径命名 `worktrees/review-pr<N>` 显式标记为 review 用途，与编号 worktree（`<NNN>-<name>`）的命名空间分离，不冲突
- **复用已存在的 review worktree**：若 `worktrees/review-pr<N>` 已存在，先刷新到最新：
  ```bash
  git -C worktrees/review-pr<N> fetch origin <BRANCH>
  git -C worktrees/review-pr<N> reset --hard origin/<BRANCH>       # 安全：detached + review-only
  ```
- **创建失败**（网络 / 权限 / origin 无该分支）→ 报错退出，提示具体失败原因，不静默回退

### 输出末尾追加清理提示（仅情况 B）

在阶段 5 总结输出末尾追加：

```
🧹 本次自动创建了 review worktree：worktrees/review-pr<N>
   清理命令：git worktree remove worktrees/review-pr<N>
```

情况 A 不提示清理（不动用户既有 worktree）。

---

## 阶段 3：分级表

区间左闭右开，边界归更高档：

| diff 行数 | reviewer 数 | 维度切分 |
|-----------|------------|---------|
| `diff < 200` | 1 | 单 agent 跑全六维度 |
| `200 ≤ diff < 600` | 2 | A：架构合规 + 测试 + 产品；B：安全 + 运维可观测 + DX |
| `600 ≤ diff < 1500` | 3 | A：架构合规 + 测试；B：安全 + 产品；C：运维可观测 + DX |
| `diff ≥ 1500` | 6 | 六维度各 1 agent（架构合规 / 安全 / 测试 / 运维可观测 / DX / 产品） |

---

## 阶段 4：并行派发 reviewer agent

单消息内多 `Agent` tool call 并行启动 `subagent_type: reviewer`（即使 `diff < 200` 单 reviewer 路径也走 Agent 派发，保持调用形态一致）。

每个 sub-agent prompt **必须自包含**：

- PR 编号 + 取 diff 命令（**全部 `gh` 命令须 `dangerouslyDisableSandbox: true`**）：
  ```bash
  gh pr diff <N>                                  # 完整 patch
  gh pr view <N> --json title,body,files,headRefOid  # 元数据
  ```
- **工作目录上下文**（来自阶段 2.5，恒有 worktree）：`$WORKTREE` 绝对路径 + 明确提示"所有 Read/Grep 路径前缀 `$WORKTREE/`"
- 分配的维度子集（来自阶段 3 表格）
- 必读：CLAUDE.md + `.claude/rules/gocell/*.md` 关键约束（路径相对工作目录）
- Finding 格式（沿用 `.claude/agents/reviewer.md`）：
  ```
  [P0/P1/P2] [Cx1-Cx4] [维度] 文件:行号
  问题: ...
  证据: `具体代码片段`
  建议: ...
  ```
- 输出要求：Finding 清单（P0→P2，同级 Cx1→Cx4）+ 复杂度汇总 + 维度内的初步判断

---

## 阶段 5：主 agent 分析与汇总（核心职责）

收到所有 sub-agent raw Finding 后，主 agent **必须自己读关键文件、做根因分析**，不允许原样转发 sub-agent 输出。

### 5.1 去重 / 冲突裁决

- 同 `文件:行号` + 同问题描述视为重复，保留证据/建议更详细的一条
- 同 `文件:行号` 不同 P 级 → **保留更高 P 级**（更保守），主 agent `Read` 代码裁定是否降级
- 同 `文件:行号` 不同 Cx → **重新评估整簇 Cx**（基于阶段 5.2 根因聚类后的整簇改动量），不简单取大
- 不同 reviewer 对同一处给出**冲突结论**（一个标 P0 一个标 LGTM）→ 主 agent `Read` 该处代码亲自裁决

### 5.2 根因归类（不允许跳过）

把所有 Finding 按**共同根因**聚类，而非按文件或维度。

典型根因示例（一个根因可能跨多个维度）：

- "缺少 ctx 取消传播" → 运维（goroutine 泄漏）+ 测试（race）+ DX（API 不一致）
- "errcode 未走 funnel" → 架构（违反 errcode 规约）+ 安全（PII 泄漏）+ DX（错误格式不统一）
- "consumer 未声明 disposition" → 架构（违反 eventbus 规约）+ 运维（DLX 路由失效）+ 测试（缺失幂等测试）

主 agent 用 `Grep` 验证根因是否系统性：**同一模式 ≥ 3 处 = 架构缺陷**，1-2 处 = 局部 bug。

每个根因簇标注：

- 涉及维度（可多个）
- Finding 数（按 P 级拆分）
- 系统性（是/否 + Grep 数）
- 整簇 Cx（按修复整簇改动量重新评估，可能高于单条 Finding 的 Cx）

### 5.3 输出固定 5 块（顺序固定）

**1. 根因簇视图**（主输出）：

```
根因 1：<一句话描述根本问题>
- 涉及维度：架构 + 安全
- Finding 数：4（P0×1, P1×3）
- 系统性：是（Grep 同模式 5 处：cells/foo/bar.go:23, cells/baz/qux.go:45, ...）
- 整簇 Cx：Cx3（跨 3 包，需改 interface）
- 子 Finding：F-001, F-003, F-007, F-012
- 建议：先改 kernel/X 接口，再传播到 cells/Y, cells/Z
```

**2. 原始 Finding 清单**（详表，按 P0→P2、同级内 Cx1→Cx4 排序）：

| # | 维度 | P | Cx | 文件:行号 | 问题摘要 | 所属根因簇 |
|---|------|---|----|----------|---------|----------|

**3. 复杂度汇总**（两套统计）：

```
按根因簇：Cx1: N / Cx2: N / Cx3: N / Cx4: N
按 Finding：Cx1: N / Cx2: N / Cx3: N / Cx4: N
```

**4. 修复分流建议**（优先按根因簇，避免逐条改同文件）：

- Cx1/Cx2 簇 → `/fix <PR>` 或 `/fix <根因描述>`
- Cx3/Cx4 簇 → 标"需人工决策"，给出三级方案种子（最小 / 彻底 / 重构）

**5. 总体结论**：

```
结论：LGTM / 需修复 / 需讨论
理由：<一句话>
```

### 5.4 主 agent 反思自检（输出前强制）

逐条自查：

1. **是否真的读过涉及的代码？** → 至少 `Read` 每个根因簇的代表文件 1 次；没读 = 没做根因分析
2. **根因归类是否真到了根因层？** → 问"为什么会出现这个症状"直到不能再问；停留在症状层 = 不合格
3. **系统性判定是否有 Grep 证据？** → 没有 Grep 结果不能标"系统性 = 是"

任一条不通过 → 补做再输出。

---

## 约束

- 不调用 `/fix`，不写代码，不评 CI（禁止自动循环等待 CI 结束）
- `gh` 命令用 `dangerouslyDisableSandbox: true`
- 单 reviewer 路径（`diff < 200`）也走 Agent 派发，保持汇总逻辑单源
- 主 agent **不允许**把 sub-agent Finding 原样转发——根因聚类、系统性判定、整簇 Cx 重评由主 agent 用 `Read`/`Grep` 亲自完成

---

## 验证清单（acceptance criteria）

每次实质修改本 SKILL 后，按下列清单走一遍：

1. **缺参 / 非法参数**：`/pr-review`、`/pr-review abc` → 立即输出错误并不执行后续阶段
2. **小 PR（diff < 200）**：`/pr-review <小 PR 号>` → 派 1 个 reviewer agent，输出含根因簇视图
3. **中 PR（600 ≤ diff < 1500）**：`/pr-review <中 PR 号>` → 派 3 个 reviewer agent 并行，主 agent 输出根因簇按主题聚类而非按维度
4. **大 PR（diff ≥ 1500）**：派 6 个 reviewer agent 并行，六维度各一
5. **无 worktree 自动创建**：执行前 `git worktree list` 不含 PR 分支 → 执行后 `worktrees/review-pr<N>` 存在
6. **既有 worktree 复用**：执行前 PR 分支已有 worktree → 直接使用，不创建 `review-pr<N>`
7. **主 agent 真做根因分析**：输出含 `Read`/`Grep` 证据；根因簇视图先于 Finding 详表
8. **维度名内部一致**：本 SKILL 内分级表、阶段 4 派发 prompt、阶段 5 输出模板使用同一组六维度名称
