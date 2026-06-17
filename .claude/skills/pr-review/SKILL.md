---
name: pr-review
description: "对指定 PR 跑自动分级六维度 review（默认）；或 --check 模式验证上一轮 findings 是否修复 + 抓回归。按 diff 净增删行数自动分配 2/3/6 reviewer agent 并行（< 200 行不派发，主 agent 自审）；主 agent 做根因聚类 + Cx 分级 + 修复分流建议，不自动 fix。"
argument-hint: "<PR 编号> [--check]"
allowed-tools: [Read, Glob, Grep, Bash, Agent]
---

# GoCell PR Review — 自动分级六维度审查

按 PR diff 净增删行数自动分配 2/3/6 个 `reviewer` agent 并行做六维度审查（< 200 行不派发，主 agent 自审），主 agent 做根因聚类与修复分流建议。**只 review，不自动 fix**。

---

## 阶段 1：输入解析

参数：`<PR 编号> [--check]`。剥离 `--check` flag 后，剩余必须是 PR 编号（纯数字或 `#NNN`）。

- 缺参 → 输出 `错误：缺少 PR 编号；用法：/pr-review <PR 编号> [--check]`，不执行后续
- 非法格式 → 输出 `错误：参数 "<原值>" 不是合法 PR 编号`，不执行后续
- **带 `--check`** → 走下方 **模式 B：--check 验证**（不跑全新六维 review），跑完即返回
- **不带** → 走默认全新六维 review（阶段 2-6）

---

## 阶段 2：取 diff 行数

```bash
gh pr view <N> --json additions,deletions --jq '.additions + .deletions'
```

取不到 PR → 报错退出。

---

## 阶段 2.5：定位或自动创建 review worktree

```bash
BRANCH=$(gh pr view <N> --json headRefName --jq .headRefName)
git worktree list   # 从输出中找 [<BRANCH>] 所在行，其首列路径即该分支 worktree，记为 WORKTREE；无匹配行 → 情况 B
```

**情况 A：找到既有 worktree** → 直接用 `$WORKTREE`，不动其状态。

**情况 B：无既有 worktree** → 自动创建 review-only worktree：

```bash
git fetch origin <BRANCH>
git worktree add --detach worktrees/review-pr<N> origin/<BRANCH>   # 已存在则改用 git -C ... reset --hard origin/<BRANCH> 刷新
WORKTREE="$(git rev-parse --show-toplevel)/worktrees/review-pr<N>"
```

创建失败 → 报错退出，不静默回退。情况 B 在阶段 5 末尾追加：`🧹 清理：git worktree remove worktrees/review-pr<N>`。

---

## 阶段 3：PR 元数据与规则上下文

主 agent 必须先取一次 PR 元数据，供所有档位共用：

```bash
gh pr view <N> --json title,body,files,headRefOid
```

以下读取全部以 `$WORKTREE` 为根；`gh pr view.files` 只提供 repo-relative path
输入，不作为文件内容来源。

必读：CLAUDE.md + `docs/guides/agent-instruction-surfaces.md` +
`.github/project-template/PROJECT.md` §3（P/Cx 评级单源）+
`.claude/rules/gocell/*.md`。rules 已瘦身，pr-review 阶段全量读取，避免本审查流程因条件过滤漏加载规则。

---

## 阶段 3.5：分级表

**派发档位（reviewer 数 + 维度切分）单源 = `.claude/agents/reviewer.md` §派发分档**，按阶段 2 的 diff 行数定档（区间左闭右开，边界归更高档）。

pr-review 的 `diff < 200` 约定：不派发 sub-agent，主 agent 在阶段 3 的共享上下文内按 reviewer.md（六维度 / Finding 格式 / 评级）+ PROJECT.md §3 评级 rubric，在 `$WORKTREE` 上 Read/Grep 自审，直接进入阶段 5。`diff ≥ 200` 按 §派发分档 派 2/3/6 个 reviewer。

---

## 阶段 4：并行派发 reviewer agent

> `diff < 200` 跳过本阶段，直接执行阶段 5。

单消息内多 `Agent` tool call 并行启动 `subagent_type: reviewer`。

每个 sub-agent prompt 必须自包含：

- PR 编号 + 取 diff 命令 `gh pr diff <N>` / `gh pr view <N> --json title,body,files,headRefOid`
- 工作目录 `$WORKTREE` 绝对路径，所有 Read/Grep 路径前缀 `$WORKTREE/`
- Finding 输出的 `文件:行号` 必须是 **repo-relative**（去掉 `$WORKTREE/` 与 `worktrees/<name>/` 前缀），便于主 agent 汇总后排版
- 分配的维度子集（见 reviewer.md §派发分档）
- 必读：阶段 3 的共享上下文和全部 `.claude/rules/gocell/*.md`。
- Finding 格式、Cx 分级、输出契约 → 沿用 `.claude/agents/reviewer.md`

---

## 阶段 5：主 agent 分析与汇总

收齐 Finding 后（`diff ≥ 200` 来自 sub-agent；`diff < 200` 来自主 agent 自审），主 agent **必须自己读关键文件、做根因分析**，不允许原样转发。

### 5.1 去重 / 冲突裁决

- 同 `文件:行号` + 同描述 → 重复，保留更详细一条
- 同 `文件:行号` 不同 P 级 → 保留更高 P 级，Read 代码裁定是否降级
- 同 `文件:行号` 不同 Cx → 按 5.2 整簇重评，不简单取大
- 冲突结论（P0 vs LGTM）→ Read 代码亲自裁决

### 5.2 根因归类（不允许跳过）

按**共同根因**聚类（不按文件 / 不按维度）。例：`errcode 未走 funnel` → 架构 + 安全 + DX 三维度同时被命中。

`Grep` 验证系统性：≥ 3 处 = 架构缺陷，1-2 处 = 局部 bug。

每簇标注：涉及维度 / Finding 数（按 P 级拆分）/ 系统性（Grep 数）/ 整簇 Cx（按整簇改动量重评）。

### 5.3 输出 5 块（**先打印到对话窗口给用户**，顺序固定）

**输出语言**：中文。**这 5 块是主交付物——必须先在对话/窗口完整打印给用户看**，阶段 6 再贴成 PR 评论留痕（窗口=主输出、评论=留痕，两者都做；输出纪律单源见 `PROJECT.md` §5）。

1. **根因簇视图**（主输出）— 每簇：根因一句 / 维度 / Finding 数 / 系统性 / 整簇 Cx / 子 Finding ID / 修复顺序建议
2. **Finding 详表** — list 形式（不用表格，避免 CJK 竖排）。按 P0→P2、同级 Cx1→Cx4 排序，每条两行：
   - `**F{n}** [P·Cx·维度] repo-relative-path:line → 簇 C{m}`
   - 缩进 2 空格的摘要 ≤ 60 字，纯文本，禁反引号包裹中文短语；详细建议放根因簇视图
3. **复杂度汇总** — 按根因簇 + 按 Finding 两套：`Cx1: N / Cx2: N / Cx3: N / Cx4: N`
4. **修复分流** — Cx1/Cx2 簇 → `/fix`；Cx3/Cx4 簇 → "需人工决策" + 三级方案种子（最小/彻底/重构）。若 PR body（阶段 3 已取）含 GitHub closing keyword（`close[sd]?` / `fix(e[sd])?` / `resolve[sd]?` / `refs`，大小写不敏感）后跟 `#<N>`，分流条目附 `← issue #<N>`
5. **总体结论** — `通过 / 需修复 / 需讨论` + 一句话理由

输出前自检：① 每个根因簇都 Read 过代表文件？② 根因到了根本层（不停在症状）？③ 系统性判定有 Grep 证据？— 任一不通过 → 补做。

---

## 阶段 6：贴 PR 评论（留痕，**不替代阶段 5 的窗口打印**）

阶段 5 的五块**已打印到窗口后**，把**同一份内容**写进 `.github/project-template/pr-comment.md` 的 `<!-- pm:pr-review -->` 模板，**额外**贴成 PR 评论留痕（输出纪律 + 无损约定单源见 `PROJECT.md` §5 与 `pr-comment.md`：窗口=主输出、评论=留痕两者都做，每条 Finding 带 `file:line` + 详表入 `<details>` 供 `/fix` 无损提取），贴到 PR：

贴评论命令 + **回显 comment URL/id** 的单源见 `issues` B4：

```bash
URL=$(gh pr comment <N> --body-file <填好的 pm:pr-review 模板>)   # stdout = https://.../pull/<N>#issuecomment-<id>
echo "✅ 已贴评论：$URL"                                           # 回显给用户（含 comment id）
```

贴失败（非 0 退出）则报错退出，不静默跳过。footer 格式见 `.github/project-template/pr-comment.md`（PR#/工具/分支/worktree/session，AI 自填）。

**追加机器块**（贴评论前，接口见 `pr-comment.md` §机器块）：verdict 按结论取 `approved`（无 finding）/ `changes-requested`（有 finding）；`bash hack/automation/pr-meta.sh emit-block --kind=pr-review --pr=<N> --phase=review --verdict=<上> --findings='<计数 json>'`（round carry / refs / 熔断全由 emit-block 派生），输出单行追加到填好的 `pm:pr-review` body 末尾再贴。`changes-requested` 且已达 3 轮上限时 emit-block 置 `next.agent=human`（熔断），窗口提示「review↔fix 已达 3 轮上限，转人工」。

贴完按结论切 label（命令见 `issues` B3）：有 finding → `pr-review/changes-requested` + `pr-status/needs-fix`（5-state：review 出 changes-requested 始终切 needs-fix，清对侧 `pr-review/approved` + `pr-status/needs-review-again`）；无 finding → `pr-review/approved` + `pr-status/ready`（清 `pr-review/changes-requested` + `pr-status/needs-review-again`）。

---

## 模式 B：--check 验证（确认上一轮 findings 是否修复 + 抓回归）

> `/pr-review <PR#> --check` 走本模式：**不做全新六维 review**，只验证上一轮发现的问题是否真修复，并在这些站点抓 `/fix` 引入的回归。配 `pr-status/needs-check-fix`（PROJECT.md §5：fix 不能自证完成，必过本验证才能 ready）。

### B1 读上一轮 findings（无损源）

**优先当前会话窗口**：同 session 内刚跑过 `/pr-review` 或 `/fix`、findings 已在上下文 → 直接用，不重复拉取。窗口没有 → `gh pr view <N> --json comments,reviews`，按 createdAt 倒序锁定**最近一次 review findings**（codex review/comment 或 `pm:pr-review` 的 `<details>` 无损详表，每条带 `file:line` + 证据 + 建议）+ 其后 `pm:fix`（fix 声称修了什么）。两者都无 → 报错退出（无可验证项）。

**额外解析 OSS 集合**：从其后 `pm:fix` 的 `🚦 OUT_OF_SCOPE` 指针 + 本 PR `pm:oos` 评论（每条 OSS 的 `file:line` + 已建 issue #N / `deferred:<原因>`）认出上一轮被**声明为 OUT_OF_SCOPE** 的 finding。据此把上一轮 findings 拆成两组：**IN_SCOPE**（B3 验代码）与**被声明 OSS**（B3 评估分类是否合理，不验代码）。

### B2 定位 worktree

同 阶段 2.5（复用既有 worktree 或自动建 review-only worktree，读当前 head 代码）。

### B3 逐条验证（Read 当前代码，只信代码）

**IN_SCOPE finding**：在 `$WORKTREE` Read 其 `file:line` 现状判定：

| 状态 | 判据 |
|------|------|
| ✅ 已修复 | 原问题代码已按建议改掉，证据充分 |
| ❌ 未修复 | 原问题代码仍在（pm:fix 声称修了但实际没改） |
| ⚠️ 回归 | 修了原问题，但在该站点 / 调用链引入新问题（`Grep` 调用方确认） |
| 🔧 部分 | 只修一部分 / 留了 TODO |

**每条必须 Read 实证，不凭 pm:fix 的"已修"自述**（review 只信代码，对齐 reviewer.md §Reasoning Blindness）。

**被声明 OUT_OF_SCOPE 的 finding（两步，不盲信 OSS 标签）**：

1. **评估分类是否合理**：Read finding 站点 + 本 PR diff（`gh pr diff <N>`），判断它是否确为「与本 PR 改动无关的不同包 / 模块」。
   - **不合理**（实为 in-scope、本该随本 PR 一起修）→ 判 `❌ 误判OSS（应在本 PR 修）`，**计入 needs-fix**（不能用 OSS 标签把本该修的问题甩出去）。
   - **合理** → 进第 2 步。
2. **核 pm:oos 留痕**：已建 issue #N / 显式 `deferred:<原因>` → 标 `🔲 OUT_OF_SCOPE(合理)`，**不计入 needs-fix**；合理但无留痕 → 仍标 🔲 + 附 action「应建 backlog issue 未建」，**不阻断 verdict**（backlog 跟踪缺口，非代码缺陷）。

### B4 输出（窗口=主输出）

1. **验证表**（主输出，逐条）：`F{n} [原 P·Cx·维度] repo-relative-path:line → ✅/❌/⚠️/🔧/🔲 + 一句证据`
2. **汇总**：已修复 N / 未修复 M / 回归 K / 部分 J / 范围外合理 R（🔲）/ 误判OSS S
3. **结论 + 流转建议**（判定规则收敛）：`verdict=changes-requested ⟺ ∃（IN_SCOPE 为 ❌/⚠️/🔧）或（被声明 OSS 经评估不合理）`；合理 OSS（🔲）一律不触发。
   - 无触发项 → 切 `pr-status/ready` + `pr-review/approved`
   - 有触发项 → 切 `pr-review/changes-requested` + `pr-status/needs-fix`（5-state；清 `pr-review/approved` + `pr-status/needs-check-fix`），未修/回归/误判OSS 项带 `file:line` 回 `/fix`

### B5 贴 pm:pr-review（--check 留痕）+ 切 label

窗口打印 B4 后，贴 `pm:pr-review` 评论（--check 变体：每条 finding 带 ✅/❌/⚠️/🔧/🔲 状态替代簇归属，summary 用 已修复N/未修复M/回归K/范围外合理R/误判OSS S）——窗口=主输出、评论=留痕，两者都做（见 `PROJECT.md` §5）。**追加机器块**（贴评论前，接口见 `pr-comment.md` §机器块）：verdict 取 `ready`（无触发项，🔲 合理 OSS 不算）/ `changes-requested`（有 IN_SCOPE ❌/⚠️/🔧 或 误判OSS）；`bash hack/automation/pr-meta.sh emit-block --kind=pr-review --pr=<N> --phase=check --verdict=<上> --findings='<计数 json，区分 in_scope / oss_reasonable / oss_misclassified>'`（round carry / refs 全派生）追加到 body 末尾。命令 + 回显见 `issues` B4；再按 B4 结论切 label（命令见 `issues` B3）：无触发项 → `pr-status/ready` + `pr-review/approved`；有触发项 → `pr-review/changes-requested` + `pr-status/needs-fix`（5-state；清 `pr-review/approved` + `pr-status/needs-check-fix`）。

---

## 约束

- 默认模式不调用 `/fix`，不写代码，不评 CI（贴 pm:pr-review 评论=留痕，不算改代码）
- `--check` 模式同样不写代码 / 不调 `/fix`；只读代码验证 + 贴评论 + 切 label（编排）

---

## 验证清单

1. 缺参 / 非法参数 → 立即输出错误，不执行后续
2. 分级处理：`diff < 200` 主 agent 自审不派发；`diff ≥ 200` 按行数派 2/3/6 个 reviewer agent（覆盖三档）
3. 无 worktree 自动创建 `worktrees/review-pr<N>`；既有 worktree 复用，不重建
4. 主 agent 输出含 Read/Grep 证据 + 根因簇视图先于 Finding 详表；维度名内部一致
5. 阶段 6 贴 `<!-- pm:pr-review -->` 评论（含 footer + 每条 finding 的 file:line）+ 回显 comment URL/id
6. `--check` 模式：读上一轮 findings（拆 IN_SCOPE / 被声明 OSS）→ IN_SCOPE 逐条 Read 验证 ✅/❌/⚠️/🔧（含抓回归），被声明 OSS 评估分类合理性（合理 + 留痕 → 🔲 不计入；误判 OSS → 计入 needs-fix）→ 窗口主输出验证表 + 贴 pm:pr-review（--check）→ 按 `changes-requested ⟺ IN_SCOPE 未修 或 误判OSS`（合理 🔲 不触发）切 label（无触发 → ready+approved / 有触发 → changes-requested+needs-fix，清 approved+needs-check-fix）
