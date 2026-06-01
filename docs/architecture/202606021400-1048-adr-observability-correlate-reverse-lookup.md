# ADR 1048 — 观测三栈反查链路 `/correlate`（缺口 11）

- 状态：Accepted
- 日期：2026-06-02
- 关联：issue #1048（[D7] 观测三栈反查链路 — 缺口 11）；004 §缺口 11
  (`docs/plans/framework-capability-gaps/202605131500-004-capability-gap-analysis.md`)；
  005 W1 (`…202605162100-005-framework-capability-roadmap-plan.md:53`)
- 前置：D1 W0 wire envelope（`kernel/outbox.ObservabilityMetadata`，已落地）

## Context

cell label 已在三栈对齐（HTTP metrics / span / audit ledger 的 `cell` 维度），但
`trace_id → audit → cell.yaml owner` 的反查链路缺失：oncall 拿到一条告警（带 `cell` label）
或一个 trace_id 时，只能手工拼接 audit 查询与 cell ownership。缺口 11 要求补齐反查 API。

W0 envelope 已存在：`kernel/outbox.ObservabilityMetadata{TraceID, TraceParent, RequestID,
CorrelationID}` 由 `NewEntry` 从 ctx 自动注入、consumer 自动 restore，且 `outbox.Entry` 是
sealed construction（`OUTBOX-ENTRY-SEALED-CONSTRUCTION-01`）。audit ledger 已持久化
`correlation_id`，但**无** `trace_id`、**不记录 source cell**（`namespace` 恒为 `auditcore`）。

本 ADR 落地缺口 11 的 **Core 切分**（与需求方确认）：correlation read-model + audit `trace_id`
自动注入 + 双模 `/correlate` 反查 API + cell→owner 拓扑派生。**OTel metric exemplar 自动注入延后**
（独立 OTel-SDK 写侧机制，反查 API 不依赖；见 D6）。

## Decisions

### D1 — Correlation envelope = 从 W0 派生的 sealed read-model（不新增 wire 类型）

`kernel/observability/correlation.Correlation` 是**视图类型**，字段全 unexported，唯一构造路径
`FromObservability(outbox.ObservabilityMetadata)`。包外既不能结构字面量构造也无法 fabricate。

- **不**新增 wire 类型、**不**改动冻结的 outbox wire 不变式（`PRINCIPAL-SEALED-FIELD-FROZEN-01` /
  `SAFEID-WIREMESSAGE-USAGE-01`）。单一真值源 = W0 envelope。
- 上游单源性（issue 要求的「上游 Hard」）= sealed construction：派生路径唯一 + 源 envelope 本身
  sealed。`TraceParent` 不进 Correlation（W3C 传播上下文，非 cross-cutting opaque ID）。

### D2 — audit `trace_id` 是非链入 HMAC 的 observability 列

`Entry.TraceID` 新增列，appender 从 `entry.Observability().TraceID` 自动注入（唯一 injection 写路径，
见 D-AIROBUST F1），mem + PG `Query` 支持 `AuditFilters.TraceID` 精确过滤，索引
`idx_audit_namespace_trace_id`。

- **刻意 EXCLUDE 出 HMAC hash chain**：`Protocol.ComputeHash` 的 12 字段输入不变。trace_id 是
  observability metadata，**不是 audited fact**——篡改 trace_id 不构成对被审计事件的伪造，故不需
  tamper-evidence。证据：`TestMemStore_TraceID_NotInHashChain`（仅 trace_id 不同 → 相同 Hash）。
- migration 046 **无软回退**：`ADD COLUMN trace_id TEXT NOT NULL DEFAULT ''` → `DROP DEFAULT`。
  `DEFAULT ''` 仅作既有行一次性 backfill，随即 drop，稳态下 app 必须显式供值（与既有 5 个身份列
  `correlation_id`/`actor_id`/… 的 NOT-NULL-no-default「explicit zero value」语义一致）。trace_id 非
  链入 HMAC，backfill `''` 不破坏既有行 hash。

### D3 — `/correlate` 双模：`trace_id` XOR `cell`，不做 event_type→cell 推断

audit entry 不记录 source cell（且补 source cell 需动冻结 wire envelope，与 D1 矛盾）。两条反查
本质 keyed 不同，故双模、恰好二选一：

- `?trace_id=<id>` → audit 反查：返回匹配 entries（id / eventType / actorId / subjectId / occurredAt /
  timestamp / correlationId）。
- `?cell=<id>` → owner 反查：cell.yaml owner（Topology）+ metric/alert selector 指针。alert 本就带
  `cell` label，故 alert→owner 从 cell 起步，不经 trace。
- both / neither → 400；未命中 → 404。

selector 是**派生字符串指针**（`metric: {cell="X"}`、alert 名提示），**非实时数据**——GoCell 不内嵌
TSDB，反查 API 不查 Prometheus。

### D4 — ops-plane runtime framework route（**非** cell contract）

`/correlate` 是 oncall/工具面端点，**无 cell 调用方**。诚实满足 FMT-31（`/internal/v1/*` contract 须声明
非空 `endpoints.clients`）的唯一路径 = **不建 contract.yaml**。

- **cell-owned 路径架构不可能**：`CELLS-NO-CONTRACTSPEC-IMPORT-01` archtest 显式封禁 cells/ 调
  `contractspec.NewFrameworkHTTP`（含专门 `FunnelBlocked` 测试）。∴ 复用 `/healthz`·`/metrics` 的
  runtime framework-route 机制：`runtime/observability/correlate` 提供 service + handler +
  `CorrelateRouteGroups`，bootstrap `WithCorrelateRoutes` 在 phase5 挂载于 InternalListener。
  物理 owner = 框架（metric `cell=_runtime`，与 /healthz·/metrics 一致）。decision「auditcore 宿主」落为
  「用 auditcore 的 ledger QueryStore 供能」（经 `ModuleExports.AuditQueryStore` 同实例导出，无二次构造）。
- **认证姿态**：`Public:false` → InternalListener service-token；contract 无 `Clients` ⇒ `auth.Mount`
  不注入 `RequireCallerCell` ⇒ **任意合法 service-token 放行（无 caller-cell allowlist）**。这是**刻意的
  ops 姿态**：网络隔离边界 = InternalListener loopback（`127.0.0.1:9090`）+ service-token（HMAC + nonce
  防重放）。FinalizeAuth 的 internal-path↔InternalListener 亲和性校验通过（已验证）。
- wire-out 仅携带 metadata，**不含 payload、不含 sessionId** 等敏感字段；slog 出口经全局 sink redaction。

### D5 — cell→owner 拓扑 codegen 单源派生

`generatedCellOwners() map[string]correlation.CellOwner` 由 `gocell generate assembly` 从每个 assembly
cell 的 `cell.yaml` owner 派生，写入 composition-API assembly 的 `modules_gen.go`（与
`generatedCapabilities()` 同范式）。corebundle 据此构造 `correlation.Topology` 注入 correlate service。

- 单源 + 上游 Hard 由**既有** `gocell verify codegen-assembly` regenerate-and-diff 字节锁覆盖
  （`modules_gen.go` 整体 golden-locked）——**无需新增独立 archtest**。
- 构造序：`generatedCellOwners()` 是编译期静态，先于 cell 构造可用，无 chicken-egg；audit QueryStore
  经 `ModuleExports` 在 module build 后交付。nil store → **fail-fast**（auditcore 是固定 cell，缺失即
  wiring bug，非可容忍降级——无软回退）。

### D6 — OTel metric exemplar 自动注入延后

issue 范围项 2 含「metric exemplar 自动注入」。本 PR **不实现**，理由：

1. OTel exemplar 是独立的 metric-SDK 写侧机制（exemplar reservoir + trace context），adapters/otel 当前
   不暴露任何 exemplar API——是与反查 API 正交的大表面。
2. 反查 API（D3）**不依赖** exemplar：exemplar 只服务 Grafana 中 trace↔metric 跳转，且无 TSDB 时无法被
   `/correlate` 反查。
3. 分离后 Core 切分完整自洽。延后项开 tracking issue 跟踪（见 §Follow-up），非静默丢弃。

## AI-robust 评级

| 机制 | 评级 | 载体 |
|------|------|------|
| Correlation 单源派生（上游） | **Hard**（sealed construction + 源 envelope sealed） | `kernel/observability/correlation` 字段私有 + 唯一构造器 |
| trace_id 进 audit 唯一 injection 写路径（下游） | **Medium** | `AUDIT-TRACE-ID-WRITE-CALLER-01`（caller-allowlist，type-aware）；上游单源继承自 sealed `outbox.Entry` + sealed `Correlation`，无独立上游 funnel |
| cell-owner 拓扑单源 | **Hard**（既有 codegen golden） | `gocell verify codegen-assembly` 字节锁 `modules_gen.go` |
| cells/ 禁构造 framework route | **Hard**（既有，type/import） | `CELLS-NO-CONTRACTSPEC-IMPORT-01` |
| ops-route 无 caller-cell 姿态 | 非新 enforcement 机制（auth 姿态） | 不新增 archtest；本 ADR 记录刻意设计 + 网络隔离边界 |

## Threat model（§威胁矩阵）

| 向量 | 缓解 |
|------|------|
| 业务代码伪造 audit trace_id | appender 唯一 injection 站点（`AUDIT-TRACE-ID-WRITE-CALLER-01`）；且即便伪造，trace_id 非链入 HMAC，不影响被审计事件 tamper-evidence |
| 伪造 Correlation 视图绕过 W0 | sealed construction：包外不可构造/fabricate |
| `/correlate` 越权访问（无 caller-cell gate） | InternalListener loopback 隔离 + service-token（HMAC + nonce 防重放）；端点只读、只回 metadata（无 payload / sessionId）；ops 面非业务面，无 cell 调用方故无 allowlist 可言 |
| 反查响应泄漏 PII | wire-out 排除 payload / sessionId；slog 出口全局 sink redaction |
| 拓扑漂移（owner 错配） | 单源 codegen + golden byte-lock；漂移 = CI 红 |

## Alternatives rejected

- **新增 Correlation wire 类型并重构 ObservabilityMetadata**：触碰冻结 wire 不变式，风险高，收益为零
  （W0 envelope 已承载所需字段）。
- **audit 加 source_cell 列 + trace→cell 直链**：需把 producing cell 塞进 outbox wire envelope（冻结），
  与 D1 矛盾；改用 D3 双模。
- **cell-owned `/correlate` slice + contract.yaml**：被 `CELLS-NO-CONTRACTSPEC-IMPORT-01` 封禁；且 ops 端点
  无真实 cell 调用方，塞占位 client 是谎报。见 D4。

## Follow-up

- OTel metric exemplar 自动注入（D6 延后项）——tracking issue **#1447**。
