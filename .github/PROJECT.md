# GoCell 项目管理真源

> **唯一真源 = GitHub Issues + [Project v2 #3](https://github.com/users/ghbvf/projects/3)**。
> 不再有 `docs/backlog/` markdown 副本。所有 backlog 条目、epic、状态、优先级、评级活在 GitHub。
>
> 本文件是 label 体系 / Project v2 字段 / 评级 rubric 的单源参考，由 issue 模版、`/pm-issue`、`/pm-epic`、
> `/pm-flow`、`/fix` 引用。

---

## 1. 真源与入口

| 维度 | 载体 | 写入方 |
|------|------|--------|
| 条目内容 / 状态描述 | GitHub Issue body | 人 / `/fix` / `/pm-issue` |
| 领域 / 类型 / 优先级 | Issue label（area / type / pri） | 模版 dropdown（pri 自动）+ CLI 追加（area/type） |
| 进度状态 / 复杂度 / wave | Project v2 字段（Status / Estimate / Wave） | Project UI / `/pm-epic` |
| 父子关系 | GitHub 原生 sub-issue | 人 / `/pm-epic` |

**新建条目**：
- Web UI：`Backlog item` 模版（Priority dropdown 自动贴 `pri-pX`）→ 建后 `gh issue edit <N> --add-label area-XX --add-label type-XX`
- CLI：`gh issue create --label backlog --label pri-pX --label area-XX --label type-XX --title "..." --body "..."`
  （漏贴 `pri-pX` 会被 `auto-label-priority.yml` workflow 贴 `pri-missing` 哨兵）

**新建 epic**：`Epic` 模版（`epic` label）；子任务用 GitHub 原生 sub-issue 关联。

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

### 2.3 pri-XX（优先级，1 个，模版自动贴）

`pri-p0` / `pri-p1` / `pri-p2` / `pri-p3`（语义见 §3 rubric）。
`pri-missing` = 哨兵（CLI 漏贴 priority 时 workflow 自动贴，需补评）。

### 2.4 工具 / 标记 label

- `backlog`（automation trigger，必贴，新 issue 入 Project）/ `epic`（跨多 PR 父 issue）/ `pr-fu`（PR review 派生）
- `flag-cond`（**条件延后**：该条目 gated 在某触发条件，body `## Trigger` 必填）。`flag-hard` / `flag-soft` /
  `flag-planned` 已删——分别与 `pri-p0/p1` / `pri-p3` / Project Status 语义重叠；`flag-cond` 保留是因为它携带
  pri/Status 表达不了的"触发门控"信息。

### 2.5 PR 状态 label（两正交轴）

| 轴 | Label | 含义 |
|----|-------|------|
| **pr-status**（流转） | `pr-status/in-progress` | ship 实施 + round-1 内置 review/fix 中 |
| | `pr-status/needs-codex` | round-1 完成，待外部 codex 二轮 review |
| | `pr-status/ready` | 二轮 fix 完成，可合并 |
| **pr-review**（codex 结论） | `pr-review/approved` | codex 二轮无需改 |
| | `pr-review/changes-requested` | codex 二轮提出需改项 |

流转见 §5。

---

## 3. 评级 rubric（P + Cx，单源）

> 取代被删的 `docs/backlog/20260520/RERATING-RUBRIC.md`。`/fix` 与 `/pm-*` 评级以本节为准。

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
| **Wave** | single-select | Wave 1 / Wave 2 / … | `/pm-epic`（epic 子任务拓扑排序结果） |
| **Parent issue** | built-in | 自动派生（原生 sub-issue） | GitHub |
| **Sub-issues progress** | built-in | 自动派生（子 issue close 比例） | GitHub |

> Priority 不是 Project 字段，是 `pri-pX` label（单源）。已删字段：Iteration（原 daily-planner 每日调度，技能已退役）、
> legacy Size（XS-XL，被 Estimate 取代）。

---

## 5. PR 双轮流程（ship → codex → fix）

```
/ship (或 /pm-flow)
  实施 → PR 创建 → 贴 pr-status/in-progress
  → round-1：内置 6 维 reviewer + /fix Cx1/Cx2
  → gh pr comment 贴 round-1 评论（<!-- pm:round-1 -->）
  → 切 pr-status/needs-codex → 停下交接

[外部] 你跑 codex 二轮 review → codex 把评论写进 PR

/fix --from-pr <N> (或 /pm-flow 续)
  → gh pr view --json reviews,comments + gh api .../pulls/{N}/comments 读 codex 评论
  → triage + round-2 修复
  → gh pr comment 贴 round-2 评论（<!-- pm:round-2 -->）
  → 切 pr-status/ready（全清）或 pr-review/changes-requested（仍有遗留）
```

> PR 状态流转 / 每轮评论是 SKILL.md 约定（无 CI 机器门），由 `/ship` `/fix` `/pm-flow` 自律执行。

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

# 漏评级哨兵
gh issue list --label pri-missing --state open

# epic
gh issue list --label epic --state open

# 某 PR 的 codex review 评论（fix --from-pr 入口）
gh pr view <N> --json reviews,comments
gh api repos/{owner}/{repo}/pulls/<N>/comments
```

> label 维度（area/type/pri）经 REST 可查；Status/Estimate/Wave 仅 Project UI 可见（REST token 缺 `project` scope）。
