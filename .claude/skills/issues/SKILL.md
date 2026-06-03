---
name: issues
description: "GitHub Issues + Project v2 #3 项目管理单源技能。Part A：epic 拆解 + wave 实施顺序调度（找子任务 → blocked-by DAG → wave 拓扑排序 → 写 Project Wave 字段 + 回填 epic body + 回评）。Part B：issue/PR 原子操作（建/改 backlog issue、area/type/pri label、PR 双轴状态 label 流转、统一 PR 评论格式 ship/fix 共用）。非 epic issue 号 → 查代码判状态（只判不修，建议 /fix 或 close）。当用户要整理 epic 排 wave、建/改 backlog issue、贴 label、切 PR 状态、给 PR 留评论、核一个 issue 是否还成立时使用。"
argument-hint: "<epic #N | #issue（非epic→状态核查）| create-issue | edit-labels | pr-status | comment> [...]"
allowed-tools: [Read, Grep, Bash, Agent, AskUserQuestion]
---

# issues — 项目管理单源（Epic/Wave + Issue/PR/Label/评论）

> 真源 = GitHub Issues + Project v2 #3。**内容/结构 + 治理全在 `.github/project-template/`**：issue body → `backlog.md`/`epic.md`，PR body → `pull_request_template.md`，PR 评论 → `pr-comment.md`，label/字段/评级/流程 → `PROJECT.md`（索引见 `README.md`）。本技能只负责编排，不复制模版内容。
> 输入分派：**`epic #N` / 带 `epic` label 的 issue** → Part A（拆解 + wave 调度）；**普通 issue 号（无 `epic` label）** → 下方「非 epic issue 状态核查」；**动词**（create / edit / pr-status / comment）→ Part B 原子操作。
> 所有 `gh` 命令用 `dangerouslyDisableSandbox: true`；写入前 `gh auth status`；create 前先 search 查重（幂等）。
> 仓库：`ghbvf/gocell`。Project v2：`--owner ghbvf --number 3`（title `gocell`）。

---

## 非 epic issue 状态核查（查代码判状态，只判不修）

输入普通 issue 号（无 `epic` label）时，不排 wave，而是查代码判断该 issue 是否仍成立：

1. `gh issue view <N> --json title,body,labels` 读问题描述 + body 的 Files。
2. 按 Files / 关键字 Read/Grep 定位代码；跨 3+ 文件时并行派 `Agent(Explore)` 核查。
3. 判状态（**只判不修**）：**存在** / **已修复**（给证据：哪行 / 哪 PR）/ **已变更**（形态变化）/ **无法确认**。
4. 输出状态 + 证据 + 建议：需修 → 建议 `/fix #<N>`；已修复 / 过期 → 建议 Part B 关闭（`gh issue close --reason ...`）。

---

# Part A — Epic 拆解 + Wave 实施顺序

> 负责 epic 级「找子任务 → 排 wave → 写 Project Wave 字段 + epic body 实施顺序 + 回评」。issue/label/评论原子操作见 Part B。

## A1. 读 epic → 关键字查找相关 issues → 关联 → 汇总子 issues

1. **读 epic**：`gh issue view <epic#> --json number,title,body,labels`（确认 epic label；从 title + 目标/范围提取关键字）。
2. **关键字查找相关 issues**：`gh issue list --search "<关键字>" --state open --json number,title,labels`，挑出属于本 epic 的候选；用 AskUserQuestion 确认候选集（不擅自全关联）。
3. **关联到 epic**（建 sub-issue 关系，已是 sub-issue 的跳过）：
   ```bash
   id=$(gh api repos/ghbvf/gocell/issues/<child#> --jq .id)   # 取整数 id（非 number）
   gh api --method POST repos/ghbvf/gocell/issues/<epic#>/sub_issues -F sub_issue_id=$id
   ```
4. **汇总子 issues**：`gh api repos/ghbvf/gocell/issues/<epic#>/sub_issues --jq '.[]|{number,title,state}'`（含新关联）；对每个 OPEN 子任务读 label（area/type/pri）+ body 的 `Blocked-by: #NNN`（多行/逗号分隔，无声明=无前置）。

> **并行分析**：子任务 ≥4 或描述模糊 / 跨 3+ 包时，按子任务分组并行派 `Agent(Explore)` 核实各自的状态 / 归属 / `Blocked-by`，汇总后再排 wave；单条直接主 agent 读。

## A2. 建 blocked-by DAG + wave 拓扑排序

**算法**（拓扑分层 / longest-path layering，作用域 = epic 子任务）：

1. 节点 = OPEN 子任务；有向边 `blocker → dependent`（来自 `Blocked-by`）。
2. 检测环：若有环，AskUserQuestion 让用户裁定断哪条边（不静默）。
3. **wave 分层**（longest-path layering，`v` = 某子任务节点）：
   - `wave(v) = 1` 若 v 无 blocker；
   - `wave(v) = 1 + max(wave(b) for b in blockers(v))` 否则。
   - 即每个子任务落在「所有前置都在更早 wave」的最早 wave。
4. **wave 内排序**：按 `pri`（p0>p1>p2>p3）→ `Cx`（小先）→ issue 号。
5. 输出每个子任务的 `(wave, 序)`。

呈现给用户的 dry-run 表：

```
Wave 1: #aaa(P1/Cx1) #bbb(P2/Cx2)
Wave 2: #ccc(P1/Cx2, blocked-by #aaa)
Wave 3: #ddd(P2/Cx3, blocked-by #ccc)
```

## A3. 写 Project v2 Wave 字段（单源真值）

> Wave 字段是 epic 子任务排序的**机器单源**；epic body 段是派生视图。写前确认字段 / option / item 合法。

```bash
# 发现 Wave 字段 id + option id（single-select）
gh project field-list 3 --owner ghbvf --format json \
  | python3 -c "import sys,json;[print(f['id'],f['name'],[ (o['id'],o['name']) for o in f.get('options',[])]) for f in json.load(sys.stdin)['fields'] if f['name']=='Wave']"

# 找子任务对应的 project item id（按 content issue 号）
gh project item-list 3 --owner ghbvf --format json \
  | python3 -c "import sys,json;[print(i['id'],i['content'].get('number')) for i in json.load(sys.stdin)['items'] if i.get('content')]"

# 写 Wave（对每个子任务）
gh project item-edit --project-id PVT_kwHOBjsrB84BYQ3m \
  --id <ITEM_ID> --field-id <WAVE_FIELD_ID> --single-select-option-id <WAVE_N_OPTION_ID>
```

**写入门（人工核对，非机器门）**：每条写入前确认 `WAVE_N_OPTION_ID` 在 field-list 输出的 option 集中、
`ITEM_ID` 在 item-list（属本 project）。option 缺某 wave 档时先在 Project UI 加，再写。子任务未入 project
（无 item）时先 `gh project item-add 3 --owner ghbvf --url <issue-url>`。

## A4. 回填 epic body 实施顺序段 + 回评

```bash
# epic body 的「实施顺序」段重生成（派生视图）
gh issue edit <epic#> --body "$(...更新 ## 实施顺序 段...)"

# 回评通知（一行 + 标记）
gh issue comment <epic#> --body "$(cat <<'C'
<!-- pm:epic-wave -->
🌊 wave 实施顺序已更新（共 N wave，M 子任务）。Wave 字段 = 单源，详见 Project #3 / 本 issue body 实施顺序段。
C
)"
```

## A5. 沟通规则

- 环检测命中 / wave option 缺档 / 子任务未入 project：停下 AskUserQuestion。
- DAG 排序结果先 dry-run 呈现，确认后再写 Project（写 live 状态）。
- 不改子任务代码 / 不关 issue / 不改 area-type-pri label（那是 Part B / `fix` 职责）。

---

# Part B — Issue / PR / Label / 评论

> issue/PR 的 `gh` 编排，是 issue/PR/label/评论**固定 gh 命令形态的单源**——ship/fix/pr-review 引用本部分，不重印命令。body 骨架见 `.github/project-template/` 的 `backlog.md` / `epic.md` / `pull_request_template.md`；PR 评论格式见 `pr-comment.md`；label / 字段 / 评级 rubric 见 `PROJECT.md`。本部分不复制模版内容。

## B1. 新建 backlog issue

三维 label 齐全（area + type + pri）+ `backlog`，全部 CLI 显式贴（必须显式 `--label pri-pX`）：

```bash
gh issue create \
  --label backlog --label pri-p2 --label area-eventing --label type-bug \
  --title "[<ID>] <简短标题>" --body-file <填好的 backlog.md>
# body 骨架单源 = .github/project-template/backlog.md（现状 / 修复方向 / Files / Trigger / Source）——本技能不复制其结构
```

- **area-XX**（1 个，8 选）：见 `.github/project-template/PROJECT.md` §2.1。
- **type-XX**（1 个，8 选）：见 §2.2。
- **pri-pX**：评级 rubric 见 `.github/project-template/PROJECT.md` §3。`/fix` 派生默认 `pri-p2`；`pri-p0` 仅 incident-driven，停下 AskUserQuestion 确认。
- **flag-cond**（可选）：条件延后型加此 label + body 写 `## Trigger`。

> P0 红线不得默认贴。area/type 漏贴时用 `gh issue edit <N> --add-label area-X --add-label type-X` 补。

## B2. 编辑 label / 关闭 issue

```bash
gh issue edit <N> --add-label area-data --remove-label area-eventing      # 改领域
gh issue edit <N> --add-label type-debt                                   # 加类型
gh issue close <N> --reason completed --comment "Fixed in PR #<NNN>"      # 修复闭合
gh issue close <N> --reason "not planned" --comment "<理由>"              # wontfix
```

epic 用 `epic` label + GitHub 原生 sub-issue（不手写 body task list）。子任务关联用 issue 页 "Create sub-issue"
或 `gh`（sub-issue API）。wave 排序见 Part A。

## B3. PR 状态 label 流转（编排）

> 两正交轴（pr-status 流转 / pr-review 结论）的取值与「何时切」语义见 `.github/project-template/PROJECT.md` §2.5 + §5（单源，不在此复制表）。本节只给切换命令。

```bash
gh pr edit <N> --add-label pr-status/needs-codex --remove-label pr-status/in-progress
gh pr edit <N> --add-label pr-status/ready      --remove-label pr-status/needs-codex
gh pr edit <N> --add-label pr-review/changes-requested
```

## B4. PR 评论（编排）

留痕约定 / 标记规则见 `.github/project-template/PROJECT.md` §5；评论格式（`pm:ship` / `pm:fix` / `pm:pr-review` 三模板 + footer）见 `.github/project-template/pr-comment.md`。本节只给命令：

```bash
gh pr comment <N> --body-file <填好的 pr-comment.md 模板>
```

`gh pr comment` 成功返回新评论 URL（含 `#issuecomment-<id>`）——**回显给用户**作为权威留痕锚点。footer 由 AI 自填（PR# / Generated with Claude Code|Codex / head 分支）。

## B5. 沟通规则

- label 编辑 / PR 状态切换 / 评论：按流程自动执行，不逐条问。
