# GoCell Backlog

新条目入口：**GitHub Issues + Project v2 #3**。
不再写 markdown 表。2026-05-20 前历史快照在 [`backlog/20260520/`](backlog/20260520/)。

Project URL：https://github.com/users/ghbvf/projects/3

## 入口

Web UI: `New Issue` → 选 `Backlog item` template。
CLI: `gh issue create --label backlog` — template 接管 body（现状 / 修复方向 / Files / Trigger / Source），见 [`.github/ISSUE_TEMPLATE/backlog.yml`](../.github/ISSUE_TEMPLATE/backlog.yml)。

建后追加 3 个 label：1 个 `cap-XX`、1 个 `flag-XX`、1 个 `type-XX`。

## Label 体系

所有维度元数据走 labels。Projects v2 仅用模板自带 4 个字段（Status / Priority / Estimate / Iteration），不加 custom field。

- **Capability**（15，单选）：`cap-01` … `cap-14`、`cap-x-cross`。能力域定义见 [`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md)
- **Flag**（4，单选）：
  | Label | 含义 |
  |---|---|
  | `flag-hard` | 硬约束，发布阻塞 |
  | `flag-cond` | 条件延后，issue body `## Trigger` 必填 |
  | `flag-soft` | 可延后 |
  | `flag-planned` | 已纳入 plan |
- **Type**（8，单选）：`type-feat` / `type-bug` / `type-refactor` / `type-arch-opt` / `type-doc` / `type-test` / `type-debt` / `type-fu`
- **工具 labels**：
  | Label | 用途 |
  |---|---|
  | `backlog` | automation trigger，新 issue 入 project（**必贴**）|
  | `bundle-parent` | 该 issue 含 sub-issues task list |
  | `wontfix` | close reason 标注（与 `not_planned` close reason 同时使用）|
  | `pr-fu` | PR review 派生 |

> "单选"由 review 把关；GitHub label 不强制 enum。完成态用 issue closure 表达，不编码进 Flag。

## Project v2 字段（仅模板原生）

| Field | Type | Values |
|---|---|---|
| Status | single-select | `Backlog` / `Ready` / `In Progress` / `In Review` / `Done` |
| Priority | single-select | `P0` / `P1` / `P2` / `P3`（P0 红线见 [`backlog/20260520/RERATING-RUBRIC.md`](backlog/20260520/RERATING-RUBRIC.md) §"P0 红线"，仅 incident-driven）|
| Estimate | single-select | `Cx1` / `Cx2` / `Cx3` / `Cx4` |
| Iteration | iteration | 可选，sprint 用，初期不开 |

## 常用查询

```bash
gh issue list --repo ghbvf/gocell --label cap-05 --state open                # 按 cap
gh issue list --repo ghbvf/gocell --label flag-cond --state open             # cond 巡查队列
gh issue list --repo ghbvf/gocell --state closed \
  --search "closed:>=2026-04-01 closed:<2026-07-01 -label:wontfix"           # 季度交付
```

## Bundle / sub-issue

父 issue 贴 `bundle-parent` label，body 用 GitHub 原生 sub-issue task list：

```markdown
## Sub-items
- [ ] #234 子条 1 标题
- [ ] #235 子条 2 标题
```

子 issue 独立 cap/flag/type labels，可独立 close。父 issue 进度自动渲染。

## Rerating

Project Rerating view（filter `-Status:Done`）→ 多选行 → 批改 Priority field。Phase 决策叙事如需文档化新建 `docs/backlog/RERATING-LOG-<YYYY-qN>.md`，规则真值源 [`backlog/20260520/RERATING-RUBRIC.md`](backlog/20260520/RERATING-RUBRIC.md)。
