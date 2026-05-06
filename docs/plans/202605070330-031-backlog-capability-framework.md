## GoCell Backlog Capability Framework v1（提案）

| 项 | 值 |
|---|---|
| 日期 | 2026-05-07 |
| 状态 | **提案 — 等待用户确认；满意后再迁移内容** |
| 主轴来源 | `docs/reviews/capabilities/20260504-engineering-capability-domain-map.md` §1（14 capability units）|
| 替代目标 | `docs/backlog.md` + `docs/backlog1.md` + `docs/backlog2.md` + `docs/backlog_later_detail.md` + `docs/tech-debt-registry.md`（合计 1282 行 / 4 套分类轴） |
| 不替代 | `docs/plans/*` 路线图、`docs/reviews/*` 审查报告 — 这两类是上游来源，不是 backlog |

---

## 1. 设计目标

1. **单源** — 一份权威 backlog；草案/分层/域别清单全部并入或归档。
2. **稳定主轴** — 用 capability 而非优先级作分类主轴；优先级会变，能力划分不变。
3. **机器可读** — 每条 item 含结构化 metadata，未来能 grep 出"P1 + Authn 域 + Cx2"。
4. **人工归档** — DONE 项打 ✅ Flag 留主表至人工迁 archive；无定时滚动避免无意义批处理。
5. **零重复** — 一条 item 只在一处出现，跨能力归到主能力 + 描述末尾 `(also: cap-XX)` tag。

非目标：
- 不替代 plan / roadmap（plan 描述"怎么打包成 PR"，backlog 描述"该做什么"）
- 不替代 ADR（ADR 记录决策，backlog 记录待办）

---

## 2. 物理结构

**推荐：单文件 + 归档目录**

```
docs/backlog.md                   ← 主入口，按 14 能力分章节（保留路径，工具/链接不需改）
docs/backlog/
  README.md                       ← 目录说明 + schema 速查（新建）
  archive/
    .gitkeep                      ← P1 建空，DONE / WONTFIX 项人工迁移到此（按季度命名 2026-q2-completed.md）
docs/backlog{1,2,_later_detail}.md  ← 老草案，P2-P6 内容并入 backlog.md 后改为 1 段重定向桩
docs/tech-debt-registry.md          ← 同上，重定向桩
```

**否决方案**：
- ❌ `docs/backlog/capability-NN-*.md` 14 个单独文件 — 每文件平均 ~10KB 太碎，跨能力查找成本高
- ❌ 完全不动现有路径只加交叉索引 — 不解决草案不回灌的根本问题

---

## 3. 主轴：14 capability 章节（强制）

每条 OPEN backlog item 必须挂在恰好一个 primary capability 之下。涉及多能力时挑"主拥有者"（owner cell / 主修改包归属）。次要能力作 tag 引用，不重复列。

| # | Capability | 章节 ID | 主要包归属 |
|---|---|---|---|
| 1 | Cell 声明与生命周期 | `cap-01-cell-lifecycle` | `kernel/cell` + `assembly` + `lifecycle` |
| 2 | 元数据解析与治理 | `cap-02-metadata-governance` | `kernel/metadata` + `governance` + `verify` + `tools/archtest` |
| 3 | Contract 注册与发现 | `cap-03-contract-registry` | `kernel/wrapper` + `kernel/registry` + `pkg/contracts` |
| 4 | HTTP 入站处理 | `cap-04-http-inbound` | `runtime/http/{router,middleware,health,devtools}` |
| 5 | 身份认证 (Authn) | `cap-05-authn` | `runtime/auth` + `auth/refresh` + `auth/config` |
| 6 | 授权决策 (Authz) | `cap-06-authz` | `runtime/auth` (authz/policy) |
| 7 | 事务性事件发布 (Outbox Producer) | `cap-07-outbox-producer` | `kernel/outbox` + `runtime/outbox` + `adapters/postgres` |
| 8 | 异步事件消费 (Subscriber+Claimer) | `cap-08-subscriber-claimer` | `kernel/{outbox,idempotency}` + `runtime/eventrouter` + `adapters/{redis,rabbitmq}` |
| 9 | 配置加载与热更新 | `cap-09-config-watcher` | `runtime/config` + watcher |
| 10 | 持久化与加密 | `cap-10-persistence-crypto` | `kernel/persistence` + `kernel/crypto` + `adapters/{postgres,vault}` |
| 11 | 分布式锁 | `cap-11-distlock` | `runtime/distlock` + `adapters/redis` |
| 12 | 启停编排 (Bootstrap) | `cap-12-bootstrap` | `runtime/bootstrap` + `runtime/shutdown` |
| 13 | 可观测性 | `cap-13-observability` | `runtime/observability/*` + `pkg/logutil` + `adapters/{prometheus,otel}` |
| 14 | 代码生成与治理工具链 | `cap-14-codegen-tooling` | `tools/{archtest,codegen,depgraph,e2egate,...}` + `cmd/gocell` |
| X | 横切项 | `cap-x-cross` | 工程基线、CI/lint、文档、跨能力重构 |

---

## 4. Item schema（P1 review 后定型）

**单 capability 单表**：每个 capability 章节一张表，每条 item 占一行。状态由 `Flag` 列编码，不另设 State 列、不分子段。

````markdown
| ID | 描述（**标题** — 现状: ...; 修复方向: ...） | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| ID-001 | **标题** — 现状: ...; 修复方向: ... | refactor | P1/Cx2 | 🟡 | — | a.go | PR#123 |
````

字段：

| 列 | 取值 | 说明 |
|---|---|---|
| ID | 沿用旧值；新建项 `<CAP_NUM>-<DOMAIN>-<NNN>`（如 `05-AUTHN-001`）| 唯一 |
| 描述 | `**标题** — 现状: ...; 修复方向: ...`（markdown 行内排版）| 主内容 |
| Type | `feat` / `bug` / `debt` / `refactor` / `arch-opt` / `doc` / `test` / `fu` | `arch-opt` = "架构优化" |
| P/Cx | `P1/Cx2`；DONE 行可填 `—` | Priority + Complexity 合一列 |
| Flag | 🔴 硬约束（即"发布阻塞项"）/ 🟠 条件延后 / 🟡 可延后 / 🟢 已纳入 plan / ✅ 已完成 | 状态由 Flag 编码，无 State 列 |
| Trigger | 仅 Flag=🟠 必填 | 触发条件文本 |
| Files | ≤ 3 个 | 主要涉及文件 |
| Source | PR# / review 报告路径 / issue# | 来源 |

**P1 review 删除/合并的字段**（vs 初版 schema）：
- 删 `Capability` 列：物理章节就是 capability，列里重复无意义；次要 capability 写描述末尾 `(also: cap-XX)`
- 删 `Priority` + `Complexity` 单独列：合一列 `P/Cx`
- 删 `ReleaseBlocker` 列：与 `Flag=🔴` 双向一致，单源即 Flag
- 删 `EstHours` 列 + 工时汇总段：用户明确不要工时
- 删 `### [STATE]` 子段（OPEN / IN_PROGRESS / TRIGGER-CONDITIONAL / DONE）：状态由 Flag 编码，避免计数维护负担

---

## 5. State 规约（隐式，由 Flag 编码）

| Flag | 等价 State | 行为 |
|---|---|---|
| 🔴 / 🟠 / 🟡 / 🟢 | OPEN（活动） | 留在主表 |
| 🟠 + Trigger 列填 | TRIGGER-CONDITIONAL | 同 OPEN，触发条件可见 |
| ✅ | DONE（待人工归档）| 留在主表至人工迁 archive |
| —（无 Flag）| 视为 WONTFIX | 立即移 archive，理由必填 |

无 IN_PROGRESS 状态：in-progress 由 Source 列的 PR# 自然表达（PR 在飞 = in-progress）。

无定时滚动归档：DONE / WONTFIX 全部人工迁移。

---

## 6. 章节模板（每个 capability 章节）

````markdown
## cap-NN: <能力名>

> 主要包: ...
> domain-map ref: §1.A #N / §1.B #N

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| ID-001 | ... | ... | P1/Cx2 | 🟡 | — | ... | PR# |
````

**不含**：顶部索引、Triggered Index 全局表、per-cap 计数行（`Open: X | ...`）。计数 / 索引漂移成痛点再补工具，不预占维护成本。

---

## 7. 横切项 cap-x-cross

不属于单一 capability 的项：
- 工程基线（CI/lint/golangci.yml/depguard）
- 跨能力大重构（K#01 typed depgraph、K#03 vocabulary collapse 这类）
- 仓库级文档、发布相关 checklist

cap-x-cross 章节内部按主题二级分组（CI / 重构 / 文档 / 发布）。**严格进入条件**：跨 ≥ 4 capability 且无明确 owner 才进；2-3 capability 跨域走 primary + 描述里 `(also: cap-XX)`，仍归 primary 章节。

---

## 8. Archive policy（人工，无定时滚动）

| 项 | 触发 | 操作 |
|---|---|---|
| Flag=✅ DONE item | 人工决定（用户每隔一段时间清理） | 移到 `docs/backlog/archive/<year>-q<N>-completed.md` |
| WONTFIX item | 状态切换即时 | 移到 archive，理由保留 |
| 章节累积 ≥ 50 条活动项 | 不自动 split | 人工评估 capability 是否拆得不够细 |
| 老 backlog{1,2,_later,tech-debt} | P2-P6 迁移完成 | 改成 1 段重定向桩（保留路径 + 当时 git SHA 引用，防外链 404） |

---

## 9. 迁移阶段（实施时）

| Phase | 操作 | 风险 |
|---|---|---|
| **P1 骨架** | 建 14 章节 + cap-x-cross + `docs/backlog/README.md` + `archive/` 空目录 + 每能力 1 条 `[EXAMPLE]` 占位 | 低 |
| **P2 backlog1 迁移** | backlog1.md OPEN 项按 primary 归类入章节；DONE 项进 legacy archive；原文件改重定向桩 | 低 |
| **P3 backlog2 迁移** | 同 P2，backlog2.md 处理；§10 索引段直接弃 | 低 |
| **P4 backlog.md 拆分** | OPEN 归类、DONE 标 ✅ Flag 留主表；重写为新结构；**小心 ID 不丢** | 中 |
| **P5 tech-debt-registry 并入** | TECH/PRODUCT 标签映射到 capability；原文件改重定向桩 | 低 |
| **P6 backlog_later 并入** | 标 🟡/🟠 进对应章节；原文件改重定向桩 | 低 |
| **P7 一致性扫尾** | 删 `[EXAMPLE]` 占位、补漏字段、grep 验证唯一性 | 低 |

每 Phase 完成后在 worktree 内 commit + push 给用户验证，过了再起下一 Phase。**P1 完成后强制 checkpoint**。

---

## 10. 不动的边界

以下文件**不**纳入本框架（保留独立轴）：
- `docs/plans/*` — 路线图 / PR 打包，主轴是时间和 PR，不是 capability
- `docs/reviews/*` — 审查 snapshot，不维护状态
- `docs/architecture/*` ADR — 决策记录
- `docs/product/roadmap/*` — 产品演进路径
- `journeys/status-board.yaml` — journey 验收状态机，独立工具链
- GitHub Issues — 仍可用作长期追踪入口；本 backlog 与 issue 单向引用（item 引 issue#，issue 不强制反向）
