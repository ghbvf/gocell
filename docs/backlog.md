# GoCell Backlog

新条目入口：**GitHub Issues + Project v2 "GoCell Backlog" (#3)**。
不再写 markdown 表。2026-05-20 前的历史快照在 [`backlog/20260520/`](backlog/20260520/)。

Project URL：https://github.com/users/ghbvf/projects/3

## 入口

```bash
gh issue create --repo ghbvf/gocell \
  --title "[<ID>] <简短标题>" \
  --label "backlog,cap-05,flag-cond,type-feat" \
  --body "$(cat <<'EOF'
## 现状
<...>

## 修复方向
<...>

## Files (≤3)
- <path>

## Trigger
<仅 flag-cond 必填>

## Source
PR# / review path
EOF
)"
# automation 自动加入 Project（trigger: label:backlog）
```

> Issue body 结构由 `.github/ISSUE_TEMPLATE/backlog.yml` 引导。

## Label 体系（31 个）

所有维度元数据走 labels。Projects v2 仅用模板自带 `Status` / `Priority` / `Estimate` / `Iteration` 4 个字段；不加 custom field。

### Capability（15 个，**单选**）

`cap-01` `cap-02` `cap-03` `cap-04` `cap-05` `cap-06` `cap-07` `cap-08` `cap-09` `cap-10` `cap-11` `cap-12` `cap-13` `cap-14` `cap-x-cross`

> "单选"由人 review 把关；GitHub label 不强制 enum。

### Flag（4 个，**单选**）

| Label | 含义 |
|---|---|
| `flag-hard` | 硬约束，发布阻塞 |
| `flag-cond` | 条件延后，issue body `## Trigger` 必填 |
| `flag-soft` | 可延后 |
| `flag-planned` | 已纳入 plan |

> done = issue closed (completed)；wontfix = issue closed (not_planned) + label `wontfix`。Flag 不编码完成态。

### Type（8 个，**单选**）

`type-feat` `type-bug` `type-refactor` `type-arch-opt` `type-doc` `type-test` `type-debt` `type-fu`

### 工具 labels（4 个）

| Label | 用途 |
|---|---|
| `backlog` | automation trigger，新 issue 入 project（**必贴**）|
| `bundle-parent` | 该 issue 含 sub-issues task list |
| `wontfix` | close reason 标注（与 `not_planned` close reason 同时使用）|
| `pr-fu` | PR review 派生 |

## Project v2 字段（仅模板原生）

| Field | Type | Values | 用途 |
|---|---|---|---|
| Status | single-select | `Backlog` / `Ready` / `In Progress` / `In Review` / `Done` | 工作流 lane |
| Priority | single-select | `P1` / `P2` / `P3` / `P4` | 优先级 |
| Estimate | single-select | `Cx1` / `Cx2` / `Cx3` / `Cx4` | 复杂度（模板原是 number，改为 single-select）|
| Iteration | iteration | 可选，PR review batch / sprint，初期不开 | sprint planning |

## 状态映射

| 旧 Flag emoji | 新表达 |
|---|---|
| 🔴 hard | issue=open, label `flag-hard`, Status=Backlog/Ready |
| 🟠 cond | issue=open, label `flag-cond`, body `## Trigger` 必填 |
| 🟡 soft | issue=open, label `flag-soft` |
| 🟢 planned | issue=open, label `flag-planned`, Status=Ready |
| ✅ done | **issue=closed (completed)**, Status auto→Done |
| WONTFIX | **issue=closed (not_planned)**, label `wontfix`, Status auto→Done |

## 常用查询

```bash
# 某 cap 当前 open
gh issue list --repo ghbvf/gocell --label cap-05 --state open

# P1 必做队列（label + project field 组合）
gh issue list --repo ghbvf/gocell --label flag-hard --state open
# Priority 在 project field 上，labels 不重复编码；Priority 排序用 web UI 或 project item-list

# Trigger 条件巡查
gh issue list --repo ghbvf/gocell --label flag-cond --state open

# 季度交付审计（区分 wontfix）
gh issue list --repo ghbvf/gocell --state closed \
  --search "closed:>=2026-04-01 closed:<2026-07-01 -label:wontfix"

# Bundle 父 issue 列表
gh issue list --repo ghbvf/gocell --label bundle-parent --state open
```

## Bundle / sub-issue

父 issue 贴 `bundle-parent` label，body 用 GitHub 原生 sub-issue task list：

```markdown
## Sub-items
- [ ] #234 子条 1 标题
- [ ] #235 子条 2 标题
```

子 issue 独立 cap/flag/type labels，可独立 close。父 issue 进度自动渲染。

## PR 关闭条目

PR description 写 `Closes #N` → merge → 自动 closed → Project automation 标 Status=Done。

## Rerating

打开 Rerating view（filter `-Status:Done`）→ 多选行 → 批改 `Priority` field → audit log 留在 Project history。
Phase 决策叙事如需文档化，新建 `docs/backlog/RERATING-LOG-<YYYY-qN>.md`（不复制状态，链接 GraphQL 查询）。

历史 RERATING-LOG / RUBRIC 见 [`backlog/20260520/`](backlog/20260520/)。

## 前置操作（admin，一次性）

1. ✅ Project v2 #3 已建（Iterative development 模板）
2. ✅ 31 个 labels 已建（cap-XX/flag-XX/type-XX + backlog/bundle-parent/pr-fu/wontfix）
3. **Project 调整模板字段**（web UI）：
   - `Priority` 值改为 `P1` / `P2` / `P3` / `P4`
   - `Estimate` 改为 single-select，值 `Cx1` / `Cx2` / `Cx3` / `Cx4`
4. **Project view 配置**（web UI）：
   - 默认 "Board"（by Status）保留
   - 新建 "By Cap"（Table, group by `Capability` label，但 label 不能 group——改 group by Status filter `-Status:Done`）
   - 实际可行 view 取决于 Projects v2 对 label 的 filter/group 支持；推荐做 4 个 saved table view 用 label filter：`Hard queue` (filter `flag-hard`) / `Cond queue` (filter `flag-cond`) / `By cap-05` (filter `cap-05`) / `Closed Q` (filter `is:closed`)
5. **Project automation**（web UI）：
   - `Auto-add to project`：filter `repo:ghbvf/gocell is:issue label:backlog` → enabled
   - `Item closed`：built-in workflow → enabled（issue closed → Status=Done）
6. 跑迁移脚本 [`scripts/migrate-backlog-to-project.sh`](../scripts/migrate-backlog-to-project.sh) `--apply`（203 OPEN 条目 → issues + labels）
