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

- **Capability**（16，单选）：`cap-01` … `cap-15`、`cap-x-cross`。能力域定义见 [`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md)
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

## Project v2 字段

| Field | Type | Values | 用途 |
|---|---|---|---|
| Status | single-select | `Backlog` / `Ready` / `In progress` / `In review` / `Done` | 进度状态（人控，daily-planner 不写）|
| Estimate | single-select | `Cx1` / `Cx2` / `Cx3` / `Cx4` | context 复杂度，rerating 时改 |
| Iteration | iteration | **1-day duration**（详见 §"Daily planning"）| 每日焦点队列，daily-planner 自动新建 + 写入 |
| Parent issue | built-in | 自动派生（GitHub 原生 sub-issue API）| 大型任务父子关系展示 |
| Sub-issues progress | built-in | 自动派生（子 issue close 比例）| 父 issue 完成度进度条 |

> Status 字面值是 `In progress` / `In review`（小写 p/r），不是首字母大写——2026-05-24 实测 option name 校准。
>
> Priority 原为 Project field，2026-05-22 单源降级为 `pri-pX` label（PR #861，详见 §"云沙箱查询"）。
>
> 还有一个老 `Size` 字段（XS/S/M/L/XL）是模板默认，**已被 Estimate (Cx1-4) 替代**，不再使用；保留只为不破坏历史 item，新条目不需要填。
>
> Parent issue + Sub-issues progress 是 GitHub 平台内建隐藏字段（2024-12 sub-issue API GA 后启用），**无需手工新增 custom field**——在 Project v2 Table View 右上角 `+` → Hidden fields 即可启用显示。

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
| Status | — | ✓（含中间态 Ready/In progress/In review）|
| Estimate | — | ✓ |
| Iteration | — | ✓ |

Token scope：日常查询 `repo` 足够；rerating 时如需在 Project UI 批改 Status/Estimate 才需要本地 `project` scope。Priority 自迁移后由 `pri-pX` label 单源承载，Rerating 流程改用 `gh issue edit --add-label --remove-label`（见 §"Rerating"）。

## Bundle / sub-issue（大型任务拆分与跟踪）

大型任务（Cx3/Cx4 或跨 ≥2 capability）拆子 issue 处理。两种承载形态：

### 形态 A：GitHub 原生 sub-issue（**推荐**，2024-12 GA）

```bash
# 建父子关系（父 issue 已存在 #230，新增子 issue #235 后挂载）
gh api -X POST /repos/ghbvf/gocell/issues/230/sub_issues \
  --field sub_issue_id=235

# 列父 issue 的 sub-issues
gh api -H "GraphQL-Features: sub_issues" graphql -f query='query {
  repository(owner:"ghbvf",name:"gocell"){
    issue(number:230){
      subIssuesSummary { total completed percentCompleted }
      subIssues(first:50){ nodes { number title state } }
    }
  }
}'
```

父 issue 仍贴 `bundle-parent` label 作 backlog 检索锚点；子 issue 独立 cap/flag/type/pri label，独立 close。Project v2 自动派生：
- **Parent issue** 字段 → 子 issue 行显示父 issue 链接
- **Sub-issues progress** 字段 → 父 issue 行显示进度条（completed/total）

### 形态 B：markdown task list（**legacy**，兼容 2024-12 前的存量）

```markdown
## Sub-items
- [ ] #234 子条 1 标题
- [ ] #235 子条 2 标题
```

存量父 issue 不强制迁移，但只有形态 A（原生 sub-issue）能被 `daily-planner` skill 识别——skill 只查 GraphQL `subIssues` 字段，**不**解析 markdown body task list。形态 B 的子条目相当于 daily-planner 视角下的"未追踪"，需要时升级到形态 A。

### 跟踪与排期

- **子 issue 入当日 Iteration**（可操作单元），父 issue 常驻 Backlog status；不强制把父 issue 加进 Iteration。
- **父 Status 不自动联动**：所有子 done 后人工把父改 Done。
- daily-planner brief 中子 issue 用 `(parent #N)` 扁平标注，不做缩进树。

## Rerating

Priority 批改通过 label edit（Project field 已下线）：

```bash
gh issue edit <N> --add-label pri-p1 --remove-label pri-p0   # 降级 P0 → P1
gh issue edit <N> --add-label pri-p0 --remove-label pri-p2   # 升级 P2 → P0
```

Project UI 仍可作为筛选/排序入口（按 label group），但写入面只剩 label。Phase 决策叙事如需文档化新建 `docs/backlog/RERATING-LOG-<YYYY-qN>.md`，规则真值源 [`backlog/20260520/RERATING-RUBRIC.md`](backlog/20260520/RERATING-RUBRIC.md)。

## Daily planning

backlog 池 → 每日工作焦点调度由 `/daily-planner` skill + `daily-planner` agent 承载。详见 `.claude/agents/daily-planner.md` 与 `.claude/skills/daily-planner/SKILL.md`。

**真值源**：Project v2 Iteration 字段（每个 1-day option = 一天的焦点队列）+ 对话窗 brief markdown 输出。**不在本地长期归档**任何 daily brief 或 audit log。

### 用法

```bash
# dry-run（默认）：在对话窗输出 brief，不写 Project v2
/daily-planner

# apply：把当日选中 issue（含昨日 carry-over）写入 today iteration
/daily-planner --apply

# 指定日期（按 1-day iteration 推断；缺失自动新建）
/daily-planner --apply --date=2026-05-25

# 强制周末模式（出差/补班等场景；影响 wave 数 → 容量）
/daily-planner --apply --weekend
/daily-planner --apply --weekday

# 扩输入集（默认仅 P0/P1；P2 是 follow-up / P3 是 nice-to-have，平时不进每日排期）
/daily-planner --apply --include-p2
/daily-planner --apply --include-p2 --include-p3
```

### 输入集（默认 P0+P1）

daily-planner 默认只把 `label:backlog AND (label:pri-p0 OR label:pri-p1)` 的 issue 纳入排期。理由：

- P0 是 incident-driven 红线（详 `backlog/20260520/RERATING-RUBRIC.md`），有就必排
- P1 是当季交付项，是日常 wave 的主力
- P2 默认是 review follow-up / 技术债登记，不阻塞主线 → 通过 `--include-p2` 显式纳入
- P3 是 nice-to-have / 长尾，平时不进每日排期 → 通过 `--include-p3` 显式纳入

实测（2026-05-24）：P0=0 / P1=31 / P2=63 / P3=145；默认 31 个候选完全够支撑 10-20 个/天的 wave 调度。

### Wave 模型

**容量公式**：`容量 = WAVE_COUNT × WAVE_SIZE`

| 模式 | WAVE_COUNT | WAVE_SIZE | 容量 (issue 数) |
|------|-----------|-----------|----------------|
| 工作日 | 2 | 5 | **10** |
| 周末 | 4 | 5 | **20** |

- **WAVE_SIZE=5** 固定，对齐 Miller's Law 认知槽（5-7 chunk）
- **超容量的 issue** → Unscheduled，标注 `[capacity overflow]`
- **周末检测** `date +%u >= 6`（自动）；`--weekend` / `--weekday` flag 显式覆盖

### Carry-over（昨日未完成 → 今日）

skill 拉昨日 iteration 的 items；满足 `issue.state == OPEN AND Project Status != Done` 的 issue **优先占 Wave 1 头部**（按原 WSJF score 排序）：

- carry-over > 5 → 顺延 Wave 2 头部（**不退** Unscheduled）
- 已完成判断用 OR 语义（覆盖手动改 Status=Done 但 issue 未 close 的脱节场景）
- **不写 audit comment**（保持 agent 对 issue 完全只读；历史回溯靠对话窗 brief + git log + Project v2 activity feed）

### Daily Iteration 自动管理

Project v2 的 Iteration 字段配置为 **1-day duration**（duration=1, startDay=1）。daily-planner 自动管理 option 集合：

- 启动检查 `$DATE` 是否落在已有 option；缺失则用 GraphQL `updateProjectV2Field.iterationConfiguration` append-only 追加 1-day option
- 历史 option 保留不动；carry-over 的 move 语义（iteration field 是 single-select，写入今日时昨日视图自动失去 item）是 GitHub Iteration 字段固有行为

### 排序算法（简化 WSJF）

```
score = pri_weight × flag_multiplier
  pri_weight:        P0=100 / P1=50 / P2=20 / P3=5 / pri-missing=110（强制首位）
  flag_multiplier:   hard=3 / planned=2 / cond(trigger 满足)=1.5 / cond(pending)=0.3 / soft=1
```

异常处理：`pri-missing` 强制首位 + `[NEEDS PRIORITY]`；缺 cap/flag/type 任一 → `[MISSING LABEL]` 不入队；`cap-x-cross` label → `[需人工确认]` 不入队；同 cap 已 ≥3 入队 → 后续 `[CAP COLLISION]` 退 Unscheduled；`bundle-parent` 父 issue（仍 OPEN）→ carry-over（子全 close 不自动闭父，让人手 close）。

### 写入边界

| 字段 | daily-planner |
|------|---------------|
| Iteration field **value**（item 行的 iteration 设置）| **可写**（仅 apply 模式；carry-over move + 新入队 set）|
| Iteration field **configuration**（追加 1-day iteration option）| **可写**（append-only，缺失目标日期 option 时自动追加）|
| Status / Estimate / labels（含 pri/cap/flag/type）| 只读 |
| issue body / title / **comment** | **不动**（含 carry-over audit comment 也不写）|

误写 Iteration value 影响半径 = 当日 brief 噪音 + Project v2 视图多出一条；apply 失败 skill 输出回滚命令清单（**逐条审查后执行**，不要批量复制粘贴）。Iteration configuration 是 append-only，无数据丢失风险。

### Token scope

token 必须有 `project` scope（detection: `grep -qiE "scopes:.*\bproject\b"`）。无则 fail-fast 退出（不再提供"仅 repo"降级模式——daily-planner 不在云沙箱跑）。本地缺则 `gh auth refresh -s project`。
