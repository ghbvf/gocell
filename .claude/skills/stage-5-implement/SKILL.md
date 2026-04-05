---
name: stage-5-implement
description: "并行实施: 契约先行+batch划分+开发者派发+进度跟踪"
argument-hint: "[branch-name]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Skill, Agent]
---

# 阶段 5: 并行实施

**执行者**: 项目经理规划 batch + 总负责人派发 + 项目经理跟踪

**入口条件**: S4 出口通过（tasks.md + product-acceptance-criteria.md 存在）

---

## 契约先行 QA 规则

对含 API 变更的 batch，强制执行以下顺序：

```
1. 后端开发者先产出 OpenAPI 片段（contracts/ 目录更新）
2. QA 自动化基于 contract 编写/更新测试
3. 后端/前端实现业务逻辑
4. batch 末尾集成运行测试
```

项目经理在 batch 划分时必须识别含 API 变更的 batch，并在 batch 方案中标注契约先行顺序。

## Playwright 配置要求（仅 role-roster.md 中 QA自动化=ON 且存在 UI 组件时适用）

QA 在 S5 编写测试时，`playwright.config.ts` 必须开启以下配置：

```typescript
// playwright.config.ts 必须包含:
trace: 'on',                    // 始终录制 trace
video: 'on-first-retry',        // 首次重试时录制视频
screenshot: 'only-on-failure',  // 失败时截图
```

---

## 操作步骤（per-PR 循环）

### 5.1 按 Wave 顺序执行

读取 `specs/{branch}/pr-plan.md`，按 Wave 顺序处理。Wave 内的 PR 并行派发。

**Wave 开始前检查**:
1. 清理残留 worktree: `git worktree list`，有上一 Wave 残留的先 `git worktree remove`
2. 确认 feature 分支已包含前置 Wave 的所有 PR: `git log --oneline -5`

**并行冲突预防**: 同一 Wave 内的 PR 不应修改同一个文件。如果 pr-plan.md 中同 Wave 的两个 PR 有文件交叉，将其中一个移到下一个 Wave 串行执行。

### 5.2 每个 PR 的实施流程

对每个 PR，总负责人执行以下步骤：

**a) 创建 worktree**:
```bash
git worktree add .claude/worktrees/{pr-name} -b {pr-branch}
```
其中 `{pr-name}` 和 `{pr-branch}` 来自 pr-plan.md。

**b) 派发 Developer Agent**（每个 PR 一个 agent，Wave 内并行）:

```
Agent(run_in_background=true):
  工作目录: .claude/worktrees/{pr-name}
  任务: 实施 pr-plan.md 中该 PR 的 tasks
  
  对标规则（当任务涉及 kernel/cells/runtime/adapters 时执行）:
  1. 查 docs/references/framework-comparison.md 找到对标文件路径
  2. WebFetch 从 GitHub 拉取对标源码
  3. 在 handoff note 中注明: ref: {framework} {file} + 采纳/偏离理由
  
  完成后执行提交脚本:
  bash .claude/skills/stage-5-implement/scripts/pr-submit.sh \
    --branch {pr-branch} --title "{PR 标题}" --base {branch}
  注意: --base 填 feature 分支名（如 feat/002-xxx），不是 {pr-branch}
  
  回报格式:
  - PR URL
  - build/vet/test: PASS/FAIL
  - 变更摘要
  - handoff note（含对标引用）
```

**c) 等待 Wave 内所有 Developer Agent 完成**

**d) 对每个 PR 派发 Reviewer Agent**（与 Developer 不同的 agent）:

```
Agent(run_in_background=true):
  任务: 审查 PR diff（读取 PR 分支代码，不需要 worktree）
  
  启动后自行获取:
  1. gh pr diff {pr-url}
  2. 读取 specs/{branch}/kernel-constraints.md
  3. 读取 specs/{branch}/spec.md
  
  审查纪律: 直接审查代码，不参考 Agent 自述。
  
  回报格式:
  - PASS 或 findings 列表（P0/P1/P2）
```

**e) 处理 Review findings**:

如有 P0/P1 findings:
1. findings 追加到 `specs/{branch}/review-findings.md`（按 PR 分节）
2. 派发 Fixer Agent（与 Reviewer 不同的 agent）到同一 worktree
3. Fixer 修复后 commit + push
4. 再派 Reviewer re-review
5. 最多 3 轮

**3 轮后仍有 P0**:
- 该 PR 拒绝 merge，findings 已保存在 review-findings.md
- 总负责人裁决：**拆分**（scope 太大）/ **重写**（方向错误）/ **升级**（派发 architect 裁决）
- worktree 保留，直到问题解决或明确废弃后删除

**f) Merge + 清理**:

```bash
gh pr merge {pr-url} --squash
git worktree remove .claude/worktrees/{pr-name}
```

**g) 更新进度**:

在 pr-plan.md 中标记该 PR 为 merged。在 tasks.md 中标记对应 tasks 为 [x]。

### 5.3 Wave 完成后

当前 Wave 所有 PR merge 后：
1. `git checkout {branch}`
2. `git pull origin {branch}`

进入下一个 Wave（回到 5.2）。

### 5.4 全部 Wave 完成

所有 PR merge 后，运行 S5 exit gate（全量集成验证）：
```bash
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S5 --branch {branch} --check exit
```

---

## 硬性产出物

| 文件/产出 | 路径 | 责任角色 |
|----------|------|---------|
| 代码实现 | `src/` | 后端/前端开发者 |
| OpenAPI spec | `contracts/` 或 `docs/` | 文档工程师 |
| 部署配置 | `docker-compose*.yml` 等 | DevOps |
| E2E 测试 | `examples/{project}/ui/e2e/*.spec.ts`（有 UI 时） | QA 自动化 |
| 种子数据 | `seed-test-data.sh` 或 Go test helper | 后端开发者/DevOps |
| tasks.md | 全部标记 `[x]` | 项目经理 |
| Handoff notes | 每 batch 每 Agent | 开发者们 |
| pr-plan.md（已完成） | `specs/{branch}/pr-plan.md` 全部标记 merged | 项目经理 |

## 出口条件

```
[ ] tasks.md 所有任务标记 [x]
[ ] 开发者 Agent 报告 build + test 全绿
[ ] e2e/*.spec.ts 存在（有 UI 时；否则标注 N/A）
[ ] 本 Phase 新增的 API endpoint 在 contracts/ 或 OpenAPI spec 中有对应定义
[ ] playwright.config.ts 含 trace: 'on' 配置（有 UI 时；否则标注 N/A）
[ ] pr-plan.md 中所有 PR 标记 merged
[ ] 每个 PR 已通过 review（无 P0 遗留）
[ ] phase-gate-check.py --stage S5 --branch {branch} --check exit = PASS
```

**约束**: 只执行 tasks.md 中的任务，不自行添加。
