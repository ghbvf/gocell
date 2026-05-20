# GoCell Backlog

新条目入口：**GitHub Issues + Project v2 "GoCell Backlog"**。
不再写 markdown 表。2026-05-20 前的历史快照在 [`backlog/20260520/`](backlog/20260520/)。

## 入口

```bash
gh issue create \
  --repo ghbvf/gocell \
  --title "[<ID>] <简短标题>" \
  --label backlog \
  --body "$(cat <<'EOF'
## 现状
<...>

## 修复方向
<...>

## Files (≤3)
- <path>

## Trigger
<仅 Flag=cond 必填>

## Source
PR# / review path
EOF
)"
# automation 自动加入 Project；建后用 web UI 或 GraphQL 设 fields
```

> Issue body 结构由 `.github/ISSUE_TEMPLATE/backlog.yml` 引导。

## Project v2 Schema

Project URL: `https://github.com/users/ghbvf/projects/<NUM>` *(待 admin 操作创建后回填)*

基于 GitHub **Iterative development** 模板（保留模板原生字段 `Status` / `Priority` / `Estimate` / `Iteration`），追加 5 个 custom field：

| Field | 来源 | Type | Values |
|---|---|---|---|
| **Status** | 模板 | single-select | `Backlog` / `Ready` / `In Progress` / `In Review` / `Done`（工作流 lane）|
| **Priority** | 模板 | single-select | `P1` / `P2` / `P3` / `P4` |
| **Estimate** | 模板 | single-select | `Cx1` / `Cx2` / `Cx3` / `Cx4`（语义=复杂度；模板原是数值，改 single-select）|
| **Iteration** | 模板 | iteration | PR review batch / sprint 用，初期不强制 |
| Capability | 自加 | single-select | `cap-01` … `cap-14`, `cap-x-cross` |
| Flag | 自加 | single-select | `hard` / `cond` / `soft` / `planned`（done = issue closed，**不**作 field 值）|
| Type | 自加 | single-select | `feat` / `bug` / `refactor` / `arch-opt` / `doc` / `test` / `debt` / `fu` |
| Trigger | 自加 | text | 仅 `Flag=cond` 必填 |
| Source | 自加 | text | `PR#NNN` / review path |

**Status 与 Flag 正交**：
- `Status` = 工作流 lane（什么阶段在做：Backlog→Ready→In Progress→...→Done）
- `Flag` = 评级 metadata（优先级形态：hard 必做 / cond 条件触发 / soft 可延后 / planned 已纳入计划）

旧 markdown 把这两个语义压在 `Flag` 一列（`✅` = done lane），Projects v2 把它拆开更精确。

## Views

| 名称 | 用途 | 配置 |
|---|---|---|
| By Cap | 默认能力域看板 | group=`Capability`, layout=table |
| Ready Queue | 下个干啥 | filter=`Flag∈{hard,planned}`, sort=`Priority` |
| Triggered | cond 条件巡查 | filter=`Flag=cond` |
| Rerating | 批量改 P/Cx | layout=table, sort=`Priority` |
| Closed Q | 季度审计 | filter=closed && date∈Q |

## Labels（极简，只承担 v2 无法表达的角色）

| Label | 用途 |
|---|---|
| `backlog` | automation trigger（新 issue 自动入 project）|
| `bundle-parent` | 该 issue 含 sub-issues task list |
| `wontfix` | close reason 标注 |
| `pr-fu` | PR review 派生（pre-triage flag）|

## 状态机

| 旧 Flag | Project v2 表达 |
|---|---|
| 🔴 hard | issue=open, Status=Backlog/Ready, Flag=hard |
| 🟠 cond | issue=open, Status=Backlog, Flag=cond, Trigger 必填 |
| 🟡 soft | issue=open, Status=Backlog, Flag=soft |
| 🟢 planned | issue=open, Status=Backlog/Ready, Flag=planned |
| ✅ done | **issue=closed (completed)**, Status auto→Done |
| WONTFIX | **issue=closed (not_planned)**, Status auto→Done, label `wontfix` |

`✅` 和 `closed` 二选一表达；`Done` 由 issue closure 自动驱动（automation），不手 set。

## 常用查询

```bash
# 某 cap 当前 open
gh issue list --repo ghbvf/gocell --label backlog \
  --search "is:open project:ghbvf/<NUM> 'Capability:cap-05'"

# P1 必做队列
gh project item-list <NUM> --owner ghbvf --format json \
  | jq '.items[] | select(.content.state=="OPEN" and .Priority=="P1" and .Flag=="hard")'

# Trigger 条件巡查队列
gh project item-list <NUM> --owner ghbvf --format json \
  | jq '.items[] | select(.Flag=="cond")'

# 季度交付审计（含 wontfix 区分）
gh issue list --repo ghbvf/gocell --state closed \
  --search "closed:>=2026-04-01 closed:<2026-07-01 -label:wontfix"
```

## Bundle / sub-issue

父 issue 加 `bundle-parent` label，body 用 GitHub 原生 sub-issue task list：

```markdown
## Sub-items
- [ ] #234 子条 1 标题
- [ ] #235 子条 2 标题
```

子 issue 独立 Cap/P/Cx/Flag/Type，可独立 close。父 issue 进度自动渲染。

## PR 关闭条目

PR description 写 `Closes #N` → merge → 自动 closed → Project automation 标 Done。
**零中间步骤**。

## Rerating

打开 Rerating view → 多选行 → 批改 `Priority` field → audit log 留在 Project history。
Phase 决策叙事如需文档化，新建 `docs/backlog/RERATING-LOG-<YYYY-qN>.md`（不复制状态，链接 GraphQL 查询）。

历史 RERATING-LOG / RUBRIC 见 [`backlog/20260520/`](backlog/20260520/)。

## 前置操作（admin，一次性）

1. 在 GitHub web 建 Project v2，**Template = "Iterative development"**，name `GoCell Backlog`
2. 模板自带 `Status` / `Priority` / `Estimate` / `Iteration` 4 个字段；按上表 add 5 个 custom fields（Capability / Flag / Type / Trigger / Source）
3. 把 `Priority` 默认值改 P1..P4；`Estimate` 改 single-select Cx1..Cx4
4. 配 5 个 views（参考上表 — 含 By Cap / Ready / Triggered / Rerating / Closed Q）
5. 配 automation：
   - `Add to project` workflow：new issue with label `backlog` → 自动入 project（默认 Status=Backlog）
   - `Item closed` workflow：issue closed → 自动 Status=Done
6. PROJECT_NUMBER 回填本文件 + `scripts/migrate-backlog-to-project.sh` 顶部
7. 跑 `gh project field-list <NUM> --owner ghbvf --format json` 抓所有 field-id / option-id，填入脚本顶部 `*_FIELD_ID` 和 `*_option_id()` case 分支
8. 跑迁移脚本 [`scripts/migrate-backlog-to-project.sh`](../scripts/migrate-backlog-to-project.sh)（先 dry-run → 校对 → `--apply` 正式跑）
