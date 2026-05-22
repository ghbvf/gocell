# GoCell Backlog

新条目入口：**GitHub Issues + Project v2 #3**。
不再写 markdown 表。2026-05-20 前历史快照在 [`backlog/20260520/`](backlog/20260520/)。

Project URL：https://github.com/users/ghbvf/projects/3

## 入口

Web UI: `New Issue` → 选 `Backlog item` template。
CLI: `gh issue create --label backlog` — template 接管 body（现状 / 修复方向 / Files / Trigger / Source），见 [`.github/ISSUE_TEMPLATE/backlog.yml`](../.github/ISSUE_TEMPLATE/backlog.yml)。

建后追加 3 个 label：1 个 `cap-XX`、1 个 `flag-XX`、1 个 `type-XX`（Priority 由 template dropdown 自动贴 `pri-pX`，见下）。

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
  - 创建时由 issue template Priority dropdown 触发 [`.github/workflows/auto-label-priority.yml`](../.github/workflows/auto-label-priority.yml) 自动贴
  - 评级规则真值源：[`backlog/20260520/RERATING-RUBRIC.md`](backlog/20260520/RERATING-RUBRIC.md)（含 P0 红线，仅 incident-driven）
  - 如需批量修复存量 label 漂移或一次性 backfill：[`hack/backfill-priority-labels.sh`](../hack/backfill-priority-labels.sh)（需 `project` + `repo` scope，幂等）
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

> Priority 原为 Project field，2026-05-22 单源降级为 `pri-pX` label（PR #861，详见 §"云沙箱查询" + §"一次性迁移 / 维护"）。

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

## 一次性迁移 / 维护

仓库首次启用本 schema（或 label 集合漂移恢复）按此顺序执行：

```bash
# 1. 创建 4 个 pri-pX label（已存在时用 --force 幂等）
gh label create pri-p0 --repo ghbvf/gocell --color d73a4a --description "Priority P0 — incident-driven 红线" --force
gh label create pri-p1 --repo ghbvf/gocell --color fbca04 --description "Priority P1" --force
gh label create pri-p2 --repo ghbvf/gocell --color fef2c0 --description "Priority P2" --force
gh label create pri-p3 --repo ghbvf/gocell --color c5def5 --description "Priority P3" --force

# 2. dry-run 全量 backfill（默认不写）
bash hack/backfill-priority-labels.sh --dry-run

# 3. 单条试跑确认幂等
bash hack/backfill-priority-labels.sh --apply --issue <一个低风险样本>

# 4. 全量执行（含 open + closed）
bash hack/backfill-priority-labels.sh --apply

# 5. 离线回归测试（验证脚本本身的 jq + 分类逻辑）
bash hack/backfill-priority-labels.sh --self-test
```

脚本 idempotent：已有目标 label 的 issue 直接 skip；可反复运行用于 label drift 恢复。

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
