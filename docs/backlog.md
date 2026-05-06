# GoCell Backlog

> **单源 backlog** — 按 14 capability units 主轴组织。  
> 框架设计稿：[`docs/plans/202605070330-031-backlog-capability-framework.md`](plans/202605070330-031-backlog-capability-framework.md)  
> 主轴权威源：[`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md) §1  
> 目录说明：[`docs/backlog/README.md`](backlog/README.md)
>
> 基线：`origin/develop @ 18a06ab7`（2026-05-07）  
> 状态：**P1 骨架阶段** — 各 capability 章节仅含 `[EXAMPLE]` 占位，等 P2-P6 迁移真实 item，P7 删占位。

---

## 顶部索引

| # | Capability | OPEN | IN_PROGRESS | TRIGGER | DONE(待归档) | 跳转 |
|---|---|---:|---:|---:|---:|---|
| 1 | Cell 声明与生命周期 | 0 | 0 | 0 | 0 | [↓](#cap-01-cell-声明与生命周期) |
| 2 | 元数据解析与治理 | 0 | 0 | 0 | 0 | [↓](#cap-02-元数据解析与治理) |
| 3 | Contract 注册与发现 | 0 | 0 | 0 | 0 | [↓](#cap-03-contract-注册与发现) |
| 4 | HTTP 入站处理 | 0 | 0 | 0 | 0 | [↓](#cap-04-http-入站处理) |
| 5 | 身份认证 (Authn) | 0 | 0 | 0 | 0 | [↓](#cap-05-身份认证-authn) |
| 6 | 授权决策 (Authz) | 0 | 0 | 0 | 0 | [↓](#cap-06-授权决策-authz) |
| 7 | 事务性事件发布 (Outbox Producer) | 0 | 0 | 0 | 0 | [↓](#cap-07-事务性事件发布-outbox-producer) |
| 8 | 异步事件消费 (Subscriber+Claimer) | 0 | 0 | 0 | 0 | [↓](#cap-08-异步事件消费-subscriberclaimer) |
| 9 | 配置加载与热更新 | 0 | 0 | 0 | 0 | [↓](#cap-09-配置加载与热更新) |
| 10 | 持久化与加密 | 0 | 0 | 0 | 0 | [↓](#cap-10-持久化与加密) |
| 11 | 分布式锁 | 0 | 0 | 0 | 0 | [↓](#cap-11-分布式锁) |
| 12 | 启停编排 (Bootstrap) | 0 | 0 | 0 | 0 | [↓](#cap-12-启停编排-bootstrap) |
| 13 | 可观测性 | 0 | 0 | 0 | 0 | [↓](#cap-13-可观测性) |
| 14 | 代码生成与治理工具链 | 0 | 0 | 0 | 0 | [↓](#cap-14-代码生成与治理工具链) |
| X | 横切（CI/lint、跨能力重构、文档、发布） | 0 | 0 | 0 | 0 | [↓](#cap-x-cross-横切) |
| **合计** | | **0** | **0** | **0** | **0** | |

> 索引手动维护（D-1 默认），漂移成痛点再升工具。计数排除 `[EXAMPLE]` 占位项。

---

## Triggered Index（仅 Flag=🟠 项）

按触发条件分组，跨 capability 全局可见：

| 触发条件 | 项数 | 关联 capability |
|---|---:|---|
| _（P1 骨架阶段，待迁移真实 item 后填充）_ | 0 | — |

---

## Item schema（速查）

```
#### ID — 标题一句话

| Field | Value |
|---|---|
| Capability     | cap-NN-xxx (primary) — 可选 Also: [cap-MM, cap-PP] |
| Type           | feat / bug / debt / refactor / arch-opt / doc / test / fu |
| Priority       | P0 / P1 / P2 / P3 |
| Complexity     | Cx1 / Cx2 / Cx3 |
| ReleaseBlocker | yes / no |
| Flag           | 🔴 硬约束 / 🟠 条件延后 / 🟡 可延后 / 🟢 已纳入 plan |
| Trigger        | （仅 🟠 必填）触发条件 |
| Files          | ≤ 3 个 |
| Source         | PR# / review 报告路径 |

**现状**: ...
**修复方向**: ...
```

完整字段约束、cross-domain primary 决策规则、归档策略：见框架设计稿 §4-§9。

`Type=arch-opt` 即"架构优化"标记；`ReleaseBlocker=yes` 即"发布阻塞项"，与 `Flag=🔴` 双向一致校验。

---

## cap-01: Cell 声明与生命周期

> 主要包：`kernel/cell` + `assembly` + `lifecycle` + `worker` + `runtime/worker`  
> domain-map ref：§1.A #1  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-01 — 示例 item（schema 占位，P7 删）

| Field | Value |
|---|---|
| Capability     | cap-01-cell-lifecycle |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 骨架占位，用于验证 schema 渲染与索引计数。  
**修复方向**：P7 删全部 `[EXAMPLE]` 项。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-02: 元数据解析与治理

> 主要包：`kernel/metadata` + `governance` + `verify` + `depgraph` + `tools/archtest` + `tools/generatedverify`  
> domain-map ref：§1.A #2  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-02 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-02-metadata-governance |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-03: Contract 注册与发现

> 主要包：`kernel/wrapper` + `kernel/registry` + `pkg/contracts`  
> domain-map ref：§1.A #3  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-03 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-03-contract-registry |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-04: HTTP 入站处理

> 主要包：`runtime/http/{router,middleware,health,devtools}`  
> domain-map ref：§1.A #4  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-04 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-04-http-inbound |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-05: 身份认证 (Authn)

> 主要包：`runtime/auth` + `auth/refresh` + `auth/refresh/memstore` + `auth/config`  
> domain-map ref：§1.B #5  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-05 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-05-authn |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-06: 授权决策 (Authz)

> 主要包：`runtime/auth` (authz/policy)  
> domain-map ref：§1.B #6  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-06 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-06-authz |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-07: 事务性事件发布 (Outbox Producer)

> 主要包：`kernel/outbox` + `runtime/outbox` + `adapters/postgres`  
> domain-map ref：§1.A #7  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-07 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-07-outbox-producer |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-08: 异步事件消费 (Subscriber+Claimer)

> 主要包：`kernel/{outbox,idempotency}` + `runtime/eventrouter` + `adapters/{redis,rabbitmq}`  
> domain-map ref：§1.A #8  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-08 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-08-subscriber-claimer |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-09: 配置加载与热更新

> 主要包：`runtime/config` + watcher  
> domain-map ref：§1.B #9  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-09 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-09-config-watcher |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-10: 持久化与加密

> 主要包：`kernel/persistence` + `kernel/crypto` + `adapters/{postgres,vault}`  
> domain-map ref：§1.B #10  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-10 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-10-persistence-crypto |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-11: 分布式锁

> 主要包：`runtime/distlock` + `adapters/redis`  
> domain-map ref：§1.B #11  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-11 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-11-distlock |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-12: 启停编排 (Bootstrap)

> 主要包：`runtime/bootstrap` + `runtime/shutdown`  
> domain-map ref：§1.A #12  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-12 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-12-bootstrap |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-13: 可观测性

> 主要包：`runtime/observability/{metrics,tracing,poolstats}` + `pkg/logutil` + `adapters/{prometheus,otel}`  
> domain-map ref：§1.B #13  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-13 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-13-observability |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-14: 代码生成与治理工具链

> 主要包：`tools/{archtest,codegen,depgraph,e2egate,metricschema,generatedverify}` + `cmd/gocell` 8 子命令  
> domain-map ref：§1.B #14  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-14 — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-14-codegen-tooling |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## cap-x-cross: 横切

> 不属于单一 capability 的项：CI / lint baseline、跨 capability 大重构（≥4 cap，无明确 owner）、仓库级文档、发布相关 checklist。  
> 进入条件：**严格** — 跨 ≥ 4 capability 且无明确 owner 才进；2-3 capability 跨域走 primary + Also tag，仍归 primary 章节。  
> Open: 0 | In-progress: 0 | Trigger: 0 | Done(待归档): 0

### OPEN

#### [EXAMPLE] EXAMPLE-CAP-X — 示例 item

| Field | Value |
|---|---|
| Capability     | cap-x-cross |
| Type           | doc |
| Priority       | P3 |
| Complexity     | Cx1 |
| ReleaseBlocker | no |
| Flag           | 🟡 |
| Files          | (n/a) |
| Source         | `docs/plans/202605070330-031-backlog-capability-framework.md` |

**现状**：P1 占位。  
**修复方向**：P7 删。

### IN_PROGRESS

_（待迁移）_

### TRIGGER-CONDITIONAL

_（待迁移）_

### DONE (待人工归档)

_（待迁移）_

---

## 历史与参考

- 旧 backlog：`docs/backlog.md`（本文件先前版本）/ `docs/backlog1.md` / `docs/backlog2.md` / `docs/backlog_later_detail.md` / `docs/tech-debt-registry.md` 将在 P2-P7 期间逐步并入本文件，最终改成重定向桩。
- 框架设计稿：[`docs/plans/202605070330-031-backlog-capability-framework.md`](plans/202605070330-031-backlog-capability-framework.md)
- 主轴权威源：[`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md)
