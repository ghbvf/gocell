# GoCell 项目管理真源

> **唯一真源 = GitHub Issues + [Project v2 #3](https://github.com/users/ghbvf/projects/3)**。
> 不再有 `docs/backlog/` markdown 副本。所有 backlog 条目、epic、状态、优先级、评级活在 GitHub。
>
> 本文件是 label 体系 / Project v2 字段 / 评级 rubric 的单源参考。

---

## 1. 真源与入口

| 维度 | 载体 | 写入方 |
|------|------|--------|
| 条目内容 / 状态描述 | GitHub Issue body | 人 / 自动化 |
| 领域 / 类型 / 优先级 | Issue label（area / type / pri） | CLI 显式 `--label` |
| 进度状态 / 复杂度 / wave | Project v2 字段（Status / Estimate / Wave） | Project UI / 自动化 |
| 父子关系 | GitHub 原生 sub-issue | 人 / 自动化 |

> 本仓 issue/PR 全程经 `gh` CLI / 技能创建，body 读 `.github/project-template/` 下对应模版（`--body-file`）。

**新建 backlog**：`gh issue create --label backlog --label pri-pX --label area-XX --label type-XX --title "[<ID>] ..." --body-file <填好的 backlog.md>`。pri/area/type 必须显式贴（取值见 §2/§3）。

**新建 epic**：`gh issue create --label epic --label backlog --label pri-pX --label area-XX --title "[EPIC] ..." --body-file <填好的 epic.md>`；子任务用 GitHub 原生 sub-issue 关联。

---

## 2. Label 体系（3 维 + 条件标记 + 工具 label + PR label）

### 2.1 area-XX（领域，1 个，8 选）

| Label | 领域 | 主要包 |
|-------|------|--------|
| `area-kernel` | Cell 声明/生命周期 + Bootstrap 启停编排 | `kernel/cell` `kernel/assembly` `runtime/bootstrap` `runtime/shutdown` `runtime/worker` |
| `area-auth` | 认证 + 授权 | `runtime/auth`（authn / authz / policy / refresh） |
| `area-http` | Contract 注册/发现 + HTTP 入站 | `kernel/wrapper` `kernel/registry` `runtime/http/*` |
| `area-eventing` | Outbox producer + Subscriber/Claimer + Saga L3 | `kernel/outbox` `kernel/idempotency` `kernel/saga` `runtime/outbox` `runtime/saga` `runtime/eventrouter` |
| `area-data` | Config 热更新 + 持久化/加密 + 分布式锁 | `runtime/config` `kernel/persistence` `kernel/crypto` `runtime/distlock` `adapters/{postgres,redis,vault}` |
| `area-observability` | Metrics / Tracing / Logging | `runtime/observability/*` `adapters/{prometheus,otel}` `pkg/redaction` |
| `area-tooling` | 元数据治理/archtest + codegen/工具链 | `kernel/metadata` `kernel/governance` `tools/*` `cmd/gocell` |
| `area-cross` | 跨 ≥4 领域 / 无明确归属 | — |

### 2.2 type-XX（类型，1 个，8 选）

`type-feat`（新功能）/ `type-bug`（缺陷）/ `type-refactor`（重构）/ `type-arch-opt`（架构优化）/
`type-doc`（文档）/ `type-test`（测试）/ `type-debt`（技术债）/ `type-fu`（PR follow-up）

### 2.3 pri-XX（优先级，1 个，CLI 显式贴）

`pri-p0` / `pri-p1` / `pri-p2` / `pri-p3`（语义见 §3 rubric）。建 issue 时必须显式 `--label pri-pX`。

### 2.4 工具 / 标记 label

- `backlog`（automation trigger，必贴，新 issue 入 Project）/ `epic`（跨多 PR 父 issue）/ `pr-fu`（PR review 派生）
- `flag-cond`（**条件延后**：该条目 gated 在某触发条件，body `## Trigger` 必填）。`flag-hard` / `flag-soft` /
  `flag-planned` 已删——分别与 `pri-p0/p1` / `pri-p3` / Project Status 语义重叠；`flag-cond` 保留是因为它携带
  pri/Status 表达不了的"触发门控"信息。

### 2.5 PR 状态 label（两正交轴）

| 轴 | Label | 含义 |
|----|-------|------|
| **pr-status**（流转） | `pr-status/in-progress` | ship 实施 + 内置 review/fix 中 |
| | `pr-status/needs-review-again` | ship 内置 / fix 完成，待再审（codex / `/pr-review`） |
| | `pr-status/needs-check-fix` | `/fix` 已修，待 `/pr-review --check` 验证修复是否到位 |
| | `pr-status/ready` | `--check` 验证全修复，可合并 |
| **pr-review**（审查结论） | `pr-review/approved` | review 无需改 |
| | `pr-review/changes-requested` | review 提出需改项 |

流转见 §5。PR 始终恰好一个 `pr-status/*`。`/fix` 不能直接到 `ready`——必过 `/pr-review --check` 验证（fix 不能自证完成）。

---

## 3. 评级 rubric（P + Cx，**单源在此**）

> P + Cx 评级 rubric 的**单源在此节**；评级处直接引用，不复制。

### 3.1 P 严重程度

| 级 | 含义 | 用法 |
|----|------|------|
| **P0** | 发布阻塞 / 数据丢失 / 安全 CVE / 编译失败 | **红线**，仅 incident-driven；body 须写 incident ID 或 CVE 编号 |
| **P1** | 架构/安全/正确性关键 + 抽象/去重/funnel 闭环关键 | 架构 refactor 的上限（即使跨 ≥3 领域也顶 P1，不进 P0） |
| **P2** | 常规债务、影响维护性但不阻塞功能 | 默认档 |
| **P3** | 触发型 / 可延后 / 性能微调 / 文档完善 | |

**架构/去重/抽象命中信号**（任一即命中 → P3 升 P2、P2 升 P1，P1 维持）：type ∈ {arch-opt/refactor/debt} 且描述含
*统一/合并/拆分/抽象/converge/unify/dedup/single source/funnel/sealed/Hard 升级* ；或触及 `kernel/` 多包 /
`tools/archtest/` typed funnel / ≥3 cell；或 AI-robust Soft→Hard / Funnel 双向锁未闭合；或影响 ≥3 领域。

**触发型例外**：`flag-cond` 风格触发型条目，若其守护的 invariant 已被 Medium archtest/governance 守住（CI 绿），
架构信号升级**封顶 P2**。**反向降级**：纯 feat/bug 触发型无业务推动、或"推测性/无 benchmark/待审视"无明确 outcome
的 P2 → 降 P3。

### 3.2 Cx 复杂度（= 改动量/实现风险，以 PR diff 为单位）

| 级 | 文件域 | 类型加载 | 典型 |
|----|--------|---------|------|
| **Cx1** | 单文件 / 同文件 ≤3 处 | 不需 typeseval | 改字面量、补 godoc、加单测 |
| **Cx2** | 同包 ≤5 文件 | 可能需 archtest typed | 加方法、抽 helper、补 archtest 单条 |
| **Cx3** | 跨包 5–15 文件 | 需 archtest typed / typeseval | 接口扩字段 + 多实现同步、funnel 双向锁、ADR amendment |
| **Cx4** | ≥15 文件 / ≥3 领域 | 跨包 type info + reflect/codegen | 接口 ctx 透传、cell 接口重构、codegen 链路改造 |

> Cx 映射 Project v2 Estimate 字段（Cx1–Cx4）。Cx5+ 必须拆为多 item / 多 wave。

---

## 4. Project v2 #3 字段

| 字段 | 类型 | 取值 | 写入方 |
|------|------|------|--------|
| **Status** | single-select | Backlog / Ready / In progress / In review / Done | 人（Project 内置 workflow + 手动） |
| **Estimate** | single-select | Cx1 / Cx2 / Cx3 / Cx4 | 人 / 评级时 |
| **Wave** | single-select | Wave 1 / 2 / 3 / 4（**仅 4 档**） | 自动化（epic OPEN 子任务**滚动**排序：已完成不动、未完成重排 Wave 1-4、超窗 >W4 不入字段。算法见 `issues` Part A） |
| **Parent issue** | built-in | 自动派生（原生 sub-issue） | GitHub |
| **Sub-issues progress** | built-in | 自动派生（子 issue close 比例） | GitHub |

> Priority 不是 Project 字段，是 `pri-pX` label（单源）。已删字段：Iteration（原 daily-planner 每日调度，技能已退役）、
> legacy Size（XS-XL，被 Estimate 取代）。

---

## 5. PR 流程（ship → review → fix → check）

```
/ship <issue>
  实施 → PR 创建 → 贴 pr-status/in-progress
  → ship：内置 6 维 reviewer + /fix Cx1/Cx2 → 贴 pm:ship → 冲突预检 + CI 绿
  → 切 pr-status/needs-review-again → 停下交接

[review 轮] codex review 或 /pr-review <PR#>
  → 贴 findings 评论（codex / pm:pr-review）
  → 有需改 → 切 pr-review/changes-requested

/fix <PR#>（有 changes-requested 时；可多次跑）
  → gh pr view --json reviews,comments 读评论（按 author/createdAt 过滤最新一轮）
  → triage + 修复 → 贴 pm:fix → 冲突预检 + CI 绿
  → 切 pr-status/needs-check-fix（待验证）

/pr-review <PR#> --check（验证上一轮 findings 是否修复 + 抓回归）
  → 逐条核对当前代码：✅已修复 / ❌未修复 / ⚠️回归 / 🔧部分 → 贴 pm:pr-review（--check）
  → 全 ✅ → 切 pr-status/ready + pr-review/approved
  → 有 ❌/⚠️/🔧 → 切 pr-review/changes-requested + pr-status/needs-review-again → 回 /fix
```

> 不变式：PR 始终恰好一个 `pr-status/*`（切换时同步移除旧态）；每阶段结束都贴评论留痕（约定，无 CI 机器门），标记按来源不编 round 号。
> `/fix` 不能直接到 `ready`——必过 `/pr-review --check` 独立验证（fix 不能自证完成）。
> 评论格式模板单源 = `.github/project-template/pr-comment.md`。

---

## 6. 常用查询

```bash
# 主线队列（P0+P1 未关）
gh issue list --label backlog --label pri-p0 --state open
gh issue list --label backlog --label pri-p1 --state open

# 按领域
gh issue list --label backlog --label area-eventing --state open

# 按类型
gh issue list --label backlog --label type-bug --state open

# epic
gh issue list --label epic --state open

# 某 PR 的 codex review 评论（fix 入口；每条带 body/id/url/createdAt）
gh pr view <N> --json reviews,comments
```

> label 维度（area/type/pri）经 REST 可查；Status/Estimate/Wave 仅 Project UI 可见（REST token 缺 `project` scope）。
