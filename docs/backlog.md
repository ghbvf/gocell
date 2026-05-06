# GoCell Backlog

> **单源 backlog** — 按 14 capability units 主轴组织。  
> 框架设计稿：[`docs/plans/202605070330-031-backlog-capability-framework.md`](plans/202605070330-031-backlog-capability-framework.md)  
> 主轴权威源：[`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md) §1  
> 目录说明：[`docs/backlog/README.md`](backlog/README.md)
>
> 基线：`origin/develop @ 18a06ab7`（2026-05-07）  
> 状态：**P1 骨架阶段** — 各 capability 章节仅含 1 行 `[EXAMPLE]` 占位，等 P2-P6 迁移真实 item，P7 删占位。

---

## Item schema（速查）

每个 capability 章节用一张表承载所有 item，列含义：

| 列 | 取值 | 说明 |
|---|---|---|
| ID | 沿用旧值；新建项 `<CAP_NUM>-<DOMAIN>-<NNN>` | 唯一 |
| 描述 | `**标题** — 现状: ...; 修复方向: ...` | 主内容 |
| Type | `feat` / `bug` / `debt` / `refactor` / `arch-opt` / `doc` / `test` / `fu` | `arch-opt` = "架构优化" |
| P/Cx | 例 `P1/Cx2`；DONE 行可填 `—` | Priority + Complexity |
| Flag | 🔴 硬约束 / 🟠 条件延后 / 🟡 可延后 / 🟢 已纳入 plan / ✅ 已完成 | 🔴 = 发布阻塞项；✅ 列在表内即视为 DONE（待人工归档） |
| Trigger | 仅 🟠 必填 | 触发条件文本 |
| Files | ≤ 3 个 | 主要涉及文件 |
| Source | PR# / review 报告路径 / issue# | 来源 |

跨域处理：每条 item 物理只在一个 capability 章节出现（primary）；次要关联在描述里写 `(also: cap-XX)`。决策规则见框架设计稿 §3。

---

## cap-01: Cell 声明与生命周期

> 主要包：`kernel/cell` + `assembly` + `lifecycle` + `worker` + `runtime/worker`  
> domain-map ref：§1.A #1

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-01 | **示例 item** — 现状: P1 占位; 修复方向: P7 删全部 `[EXAMPLE]` 项 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-02: 元数据解析与治理

> 主要包：`kernel/metadata` + `governance` + `verify` + `depgraph` + `tools/archtest` + `tools/generatedverify`  
> domain-map ref：§1.A #2

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-02 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-03: Contract 注册与发现

> 主要包：`kernel/wrapper` + `kernel/registry` + `pkg/contracts`  
> domain-map ref：§1.A #3

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-03 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-04: HTTP 入站处理

> 主要包：`runtime/http/{router,middleware,health,devtools}`  
> domain-map ref：§1.A #4

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-04 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-05: 身份认证 (Authn)

> 主要包：`runtime/auth` + `auth/refresh` + `auth/refresh/memstore` + `auth/config`  
> domain-map ref：§1.B #5

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-05 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-06: 授权决策 (Authz)

> 主要包：`runtime/auth` (authz/policy)  
> domain-map ref：§1.B #6

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-06 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-07: 事务性事件发布 (Outbox Producer)

> 主要包：`kernel/outbox` + `runtime/outbox` + `adapters/postgres`  
> domain-map ref：§1.A #7

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-07 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-08: 异步事件消费 (Subscriber+Claimer)

> 主要包：`kernel/{outbox,idempotency}` + `runtime/eventrouter` + `adapters/{redis,rabbitmq}`  
> domain-map ref：§1.A #8

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-08 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-09: 配置加载与热更新

> 主要包：`runtime/config` + watcher  
> domain-map ref：§1.B #9

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-09 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-10: 持久化与加密

> 主要包：`kernel/persistence` + `kernel/crypto` + `adapters/{postgres,vault}`  
> domain-map ref：§1.B #10

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-10 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-11: 分布式锁

> 主要包：`runtime/distlock` + `adapters/redis`  
> domain-map ref：§1.B #11

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-11 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-12: 启停编排 (Bootstrap)

> 主要包：`runtime/bootstrap` + `runtime/shutdown`  
> domain-map ref：§1.A #12

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-12 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-13: 可观测性

> 主要包：`runtime/observability/{metrics,tracing,poolstats}` + `pkg/logutil` + `adapters/{prometheus,otel}`  
> domain-map ref：§1.B #13

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-13 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-14: 代码生成与治理工具链

> 主要包：`tools/{archtest,codegen,depgraph,e2egate,metricschema,generatedverify}` + `cmd/gocell` 8 子命令  
> domain-map ref：§1.B #14

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-14 | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## cap-x-cross: 横切

> 不属于单一 capability 的项：CI / lint baseline、跨 capability 大重构（≥4 cap，无明确 owner）、仓库级文档、发布相关 checklist。  
> 进入条件：**严格** — 跨 ≥ 4 capability 且无明确 owner 才进；2-3 capability 跨域走 primary + 描述里 `(also: cap-XX)`，仍归 primary 章节。

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| EXAMPLE-CAP-X | **示例 item** — 现状: P1 占位; 修复方向: P7 删 | doc | P3/Cx1 | 🟡 | — | (n/a) | `docs/plans/202605070330-031-backlog-capability-framework.md` |

---

## 历史与参考

- 旧 backlog：`docs/backlog.md`（本文件先前版本）/ `docs/backlog1.md` / `docs/backlog2.md` / `docs/backlog_later_detail.md` / `docs/tech-debt-registry.md` 将在 P2-P7 期间逐步并入本文件，最终改成重定向桩。
- 框架设计稿：[`docs/plans/202605070330-031-backlog-capability-framework.md`](plans/202605070330-031-backlog-capability-framework.md)
- 主轴权威源：[`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md)
