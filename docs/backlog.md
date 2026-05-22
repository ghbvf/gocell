# GoCell Backlog

新条目入口：**GitHub Issues + Project v2 #3**。
不再写 markdown 表。2026-05-20 前历史快照在 [`backlog/20260520/`](backlog/20260520/)。

Project URL：https://github.com/users/ghbvf/projects/3

## 入口

两条互斥路径，**Priority 处理方式不同**——`gh issue create` 不会触发 Issue Forms dropdown UI，必须手工显式贴 label。

### Web UI（推荐）

`New Issue` → 选 `Backlog item` template → 填表（含必选 Priority dropdown）→ 提交。`.github/workflows/auto-label-priority.yml` 解析 body 中 `### Priority` section，自动贴 `pri-pX` label。

### CLI

```bash
# pri-pX 必填，必须显式贴：P0=pri-p0 / P1=pri-p1 / P2=pri-p2 / P3=pri-p3
# cap-XX / flag-XX / type-XX 分别从 cap-01..cap-x-cross / flag-{hard,cond,soft,planned} / type-* 选一
gh issue create --repo ghbvf/gocell \
  --label backlog \
  --label pri-pX \
  --label cap-XX --label flag-XX --label type-XX \
  --title "..." --body-file body.md
```

CLI 路径**不解析** [`.github/ISSUE_TEMPLATE/backlog.yml`](../.github/ISSUE_TEMPLATE/backlog.yml) 的 dropdown；workflow 仍会在 `issues.opened` 触发，但 body 中找不到 `### Priority` section，会贴 `pri-missing` 哨兵 label（除非创建者已显式贴 `pri-pX`，那种情况 workflow 跳过 sentinel）。

### 创建后

无论哪条路径，建后需追加 3 个 label：1 个 `cap-XX`、1 个 `flag-XX`、1 个 `type-XX`（CLI 路径如上一并贴；Web UI 路径建后用 `gh issue edit <N> --add-label cap-XX` 等手工补）。

## Label 体系

所有维度元数据走 labels（含 Priority）。Projects v2 仅用模板自带 3 个字段（Status / Estimate / Iteration），不加 custom field。

- **Capability**（15，单选）：`cap-01` … `cap-14`、`cap-x-cross`。能力域定义见 [`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md)
- **Flag**（4，单选）：
  | Label | 含义 |
  |---|---|
  | `flag-hard` | 硬约束，发布阻塞 |
  | `flag-cond` | 条件延后，issue body `## Trigger` 必填 |
  | `flag-soft` | 可延后 |
  | `flag-planned` | 已纳入 plan |
- **Type**（8，单选）：`type-feat` / `type-bug` / `type-refactor` / `type-arch-opt` / `type-doc` / `type-test` / `type-debt` / `type-fu`
- **Priority**（4，单选）：`pri-p0` / `pri-p1` / `pri-p2` / `pri-p3`
  - Web UI 创建：issue template Priority dropdown 触发 [`.github/workflows/auto-label-priority.yml`](../.github/workflows/auto-label-priority.yml) 自动贴
  - CLI 创建：必须显式 `--label pri-pX`（见 §"入口"）；workflow 检测不到 dropdown 时贴 `pri-missing` 哨兵
  - 评级规则真值源：[`backlog/20260520/RERATING-RUBRIC.md`](backlog/20260520/RERATING-RUBRIC.md)（含 P0 红线，仅 incident-driven）
  - `pri-missing`（哨兵）：workflow 找不到 dropdown 时贴；用 `gh issue list --label pri-missing` 查待补 priority 的 issue，处理方式：`gh issue edit <N> --add-label pri-pX --remove-label pri-missing`
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
| Estimate | single-select | `Cx1` / `Cx2` / `Cx3` / `Cx4` |
| Iteration | iteration | 可选，sprint 用，初期不开 |

> Priority 原为 Project field，2026-05-22 单源降级为 `pri-pX` label（PR #861，详见 §"云沙箱查询"）。

## 常用查询

```bash
gh issue list --repo ghbvf/gocell --label cap-05 --state open                # 按 cap
gh issue list --repo ghbvf/gocell --label flag-cond --state open             # cond 巡查队列
gh issue list --repo ghbvf/gocell --label pri-p0 --state open                # P0 红线巡查
gh issue list --repo ghbvf/gocell --label pri-p1 --label cap-05 --state open # cap-05 内 P1
gh issue list --repo ghbvf/gocell --state closed \
  --search "closed:>=2026-04-01 closed:<2026-07-01 -label:wontfix"           # 季度交付
```

## 云沙箱查询

云沙箱常用 token 只授 `repo` scope（无 `project`），`gh project item-list` 与 GraphQL `node(... ProjectV2Item)` 一律 403。所有 label 维度（Capability / Flag / Type / Priority）通过 REST `GET /repos/.../issues?labels=...` 直接命中；只有 Status / Estimate / Iteration 仅在 Project UI 可查。

| 维度 | sandbox 查询 | 仅 Project UI 可查 |
|---|---|---|
| Capability | `--label cap-XX` | — |
| Flag | `--label flag-XX` | — |
| Type | `--label type-XX` | — |
| Priority | `--label pri-pX` | — |
| Status | — | ✓（含中间态 Ready/In Progress/In Review）|
| Estimate | — | ✓ |
| Iteration | — | ✓ |

Token scope：日常查询 `repo` 足够；rerating 时如需在 Project UI 批改 Status/Estimate 才需要本地 `project` scope。Priority 自迁移后由 `pri-pX` label 单源承载，Rerating 流程改用 `gh issue edit --add-label --remove-label`（见 §"Rerating"）。

## Bundle / sub-issue

父 issue 贴 `bundle-parent` label，body 用 GitHub 原生 sub-issue task list：

```markdown
## Sub-items
- [ ] #234 子条 1 标题
- [ ] #235 子条 2 标题
```

子 issue 独立 cap/flag/type labels，可独立 close。父 issue 进度自动渲染。

## Rerating

Priority 批改通过 label edit（Project field 已下线）：

```bash
gh issue edit <N> --add-label pri-p1 --remove-label pri-p0   # 降级 P0 → P1
gh issue edit <N> --add-label pri-p0 --remove-label pri-p2   # 升级 P2 → P0
```

Project UI 仍可作为筛选/排序入口（按 label group），但写入面只剩 label。Phase 决策叙事如需文档化新建 `docs/backlog/RERATING-LOG-<YYYY-qN>.md`，规则真值源 [`backlog/20260520/RERATING-RUBRIC.md`](backlog/20260520/RERATING-RUBRIC.md)。
