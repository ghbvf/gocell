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

## Amendment 2026-06-03 — `/correlate` 改挂 PrimaryListener + admin 角色（取代 D4 的 InternalListener 姿态）

**起因**：D4 原决策把 `/correlate` 挂在 `/internal/v1/audit/correlate`（InternalListener，framework route，无 `Clients`）。但 `/internal/v1/*` 是 **cell→cell 控制面**命名空间，`kernel/contractspec/spec.go::validateHTTP` 在 `auth.Mount` 时**强制**该前缀路径声明非空 `Clients` caller-cell allowlist。framework route 经 `NewFrameworkHTTP` 构造、不能表达 `Clients`，于是 **corebundle 启动即 fail-closed 崩溃**（e2e `container exited (1)`：`internal path requires non-empty Clients`）。该崩溃此前无单测/集成测试覆盖——只有 e2e full-boot 触发（测试盲区，本次补 `runtime/observability/correlate/routes_test.go` mount+FinalizeAuth 回归）。

**决策**：`/correlate` 不是 cell→cell 控制面流量，而是 **ops/admin 流量**。框架对 admin/特权端点的既有范式是「合适 listener + 授权 Policy」（如 auditquery `/api/v1/audit/entries` = PrimaryListener + `auth.AnyRole(RoleAdmin)`），对齐 Kubernetes RBAC / Vault ACL。故：

- **路径**：`/internal/v1/audit/correlate` → **`/api/v1/observability/correlate`**（framework 区，与 `/api/v1/devtools/` 平行；**不**入 auditcore 拥有的 `/api/v1/audit/` 前缀，避免跨 owner 命名空间冲突）。
- **Listener**：InternalListener → **PrimaryListener**（JWT）。
- **授权**：无 caller-cell allowlist（service-token 任意放行）→ **`auth.Route.Policy = auth.AnyRole(auth.RoleAdmin)`**。这把 `actorId` 暴露从「任意合法 service-token」收紧为「仅 admin 角色」——**净改善**，且与更敏感的 auditquery 同款门。
- 端点仍是 framework route（`runtime/observability/correlate`，metric `cell=_runtime`），双模 + topology + minimal-PII 字段集 + F1 presence-XOR（`q.Has`）全部不变。

下方 **D3 `actorId` 缓解**、**D4 全文**、**AI-robust 表末行**、**Threat model 越权/PII 两行**已据此就地改写（原 InternalListener/service-token 表述不保留，避免两套真值源）。issue #1048。

---

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

### D3 — `/correlate` 双模：`traceId` XOR `cell`，不做 event_type→cell 推断

audit entry 不记录 source cell（且补 source cell 需动冻结 wire envelope，与 D1 矛盾）。两条反查
本质 keyed 不同，故双模、恰好二选一：

- `?traceId=<id>` → audit 反查：返回匹配 entries，wire 字段集为
  `id / eventId / eventType / actorId / occurredAt / timestamp / correlationId`，外加顶层元数据
  `hasMore` (bool) + `returned` (int)。
  - `eventId`：outbox 事件 UUID，**非 PII**，明确添回——用于 forward-correlation（从 audit entry
    回查 outbox relay 日志 / event payload 调试）。
  - `actorId`：触发主体 ID，是 trace 关联所需的**最小身份**，**刻意保留**。`actorId` 可能
    与终端用户身份重合（业务 actor 即 user UUID），缓解措施（见 Amendment 2026-06-03）：
    **admin 角色授权门**（`auth.AnyRole(RoleAdmin)`，非 admin → 403）；deeper/target identity
    （subjectId/tenantId/sessionId）仅通过同 admin 门的 auditquery 获取。
  - **刻意 EXCLUDE**：`subjectId / tenantId / sessionId / payload / hash / prevHash`——
    minimal-PII fail-closed 设计，该端点无 cell 调用方。
  - **单页 point-lookup**：结果上限 `traceQueryLimit=500`，**不支持游标分页**；响应带
    `hasMore` flag（对齐 Kubernetes list-completeness 模式）。`hasMore=true` 说明匹配
    entries 超过 500 条，结果已截断——oncall 应缩小时间窗口或改用 JWT-authed auditquery
    endpoint（`GET /api/v1/audit/entries`）进行完整游标分页。完整游标分页功能活在 auditquery，
    不在 `/correlate`（ops point-lookup 语义）。
- **注意：`?traceId=` 仅返回 audit entries，不含 cell owner 信息。**
  audit entries 刻意不记录 source cell（补 source cell 需改冻结 wire envelope，与 D1 矛盾）。
  oncall 两步工作流：先用 `?traceId=<id>` 定位活动（trace→audit），再用 `?cell=<id>` 查
  owner——这是接受的 UX trade-off，不是缺陷。
- `?cell=<id>` → owner 反查：cell.yaml owner（Topology）+ metric/alert selector 指针。alert 本就带
  `cell` label，故 alert→owner 从 cell 起步，不经 trace。
- both / neither → 400；未命中 → 404。

selector 是**派生字符串指针**（`metric: {cell="X"}`、alert 名提示），**非实时数据**——GoCell 不内嵌
TSDB，反查 API 不查 Prometheus。

### D4 — ops-plane runtime framework route（**非** cell contract）— *已据 Amendment 2026-06-03 改写*

`/correlate` 是 oncall/工具面端点，**无 cell 调用方**，是 ops/admin 流量而非 cell→cell 控制面流量。

- **cell-owned 路径架构不可能**：`CELLS-NO-CONTRACTSPEC-IMPORT-01` archtest 显式封禁 cells/ 调
  `contractspec.NewFrameworkHTTP`（含专门 `FunnelBlocked` 测试）。∴ 复用 `/healthz`·`/metrics`·
  `/api/v1/devtools/catalog` 的 runtime framework-route 机制：`runtime/observability/correlate` 提供
  service + handler + `CorrelateRouteGroups`，bootstrap `WithCorrelateRoutes` 在 phase5 挂载于
  **PrimaryListener**。物理 owner = 框架（metric `cell=_runtime`）。decision「auditcore 宿主」落为
  「用 auditcore 的 ledger QueryStore 供能」（经 `ModuleExports.AuditQueryStore` 同实例导出，无二次构造）。
- **认证/授权姿态**：PrimaryListener JWT（`Public:false`）+ **`auth.Route.Policy = auth.AnyRole(auth.RoleAdmin)`**
  —— 仅持 admin 角色的 JWT 放行，非 admin → 403。这与 auditquery（`/api/v1/audit/entries`，同样在
  PrimaryListener 读 audit 数据）同款 RBAC 门，对齐 Kubernetes RBAC / Vault ACL 对特权端点的范式。
  **为何不挂 `/internal/v1/*`**：该前缀是 cell→cell 控制面，`validateHTTP` 在 `auth.Mount` 强制非空
  `Clients` caller-cell allowlist；framework route 经 `NewFrameworkHTTP` 不能表达 `Clients`，挂上去启动即
  fail-closed 崩溃（见 Amendment 2026-06-03）。ops 端点无 caller cell，不属于该模型。
- wire-out minimal-PII：`?traceId=` 响应字段集为
  `id / eventId / eventType / actorId / occurredAt / timestamp / correlationId`（含 `hasMore` / `returned`
  顶层元数据）；`subjectId / tenantId / sessionId / payload / hash / prevHash` **刻意排除**
  （fail-closed）。`eventId` 是非 PII UUID；`actorId` 是 trace 关联最小身份，
  刻意保留（详见 D3 `actorId` 决策说明，现由 admin 角色门缓解）。`?cell=` 响应仅含 cell owner metadata，
  不含 audit 内容。slog 出口经全局 sink redaction。
- **路径与参数命名约定**：端点物理路径为 **`/api/v1/observability/correlate`**（framework 区，与
  `/api/v1/devtools/` 平行；**不**入 auditcore 拥有的 `/api/v1/audit/` 前缀，避免跨 owner 命名空间冲突）。
  查询参数为 `traceId`（camelCase，遵循 CLAUDE.md Query-param 约定），而非 `trace_id`（DB snake_case
  约定仅适用于数据库字段）。

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
| ops-route admin 角色授权姿态 | 非新 enforcement 机制（复用既有 auth Policy） | 不新增 archtest；`auth.AnyRole(RoleAdmin)` Policy（与 auditquery 同款）；mount 回归测试 `routes_test.go`（PrimaryListener mount + FinalizeAuth）守「不回退到 internal 路径」 |

## Threat model（§威胁矩阵）

| 向量 | 缓解 |
|------|------|
| 业务代码伪造 audit trace_id | appender 唯一 injection 站点（`AUDIT-TRACE-ID-WRITE-CALLER-01`）；且即便伪造，trace_id 非链入 HMAC，不影响被审计事件 tamper-evidence |
| 伪造 Correlation 视图绕过 W0 | sealed construction：包外不可构造/fabricate |
| `/correlate` 越权访问 | **admin 角色授权门**（`auth.AnyRole(RoleAdmin)` Policy；非 admin → 403）——与更敏感的 auditquery 同款 RBAC 门（见 Amendment 2026-06-03）；端点只读，`?traceId=` 仅返 `id/eventId/eventType/actorId/occurredAt/timestamp/correlationId`，`?cell=` 仅返 owner metadata。注：此前的「InternalListener loopback + service-token、无 caller-cell gate」姿态已废弃——它在 `/internal/v1/*` 前缀下启动即崩溃，且「任意 service-token 放行」弱于 admin 角色门 |
| 反查响应泄漏 PII | `?traceId=` 响应刻意排除 `subjectId / tenantId / sessionId / payload / hash / prevHash`（minimal-PII fail-closed）；`eventId` 是非 PII UUID（outbox 事件 ID），明确保留用于 forward-correlation；`actorId` 是 trace 关联最小身份，**刻意保留**——它可能与终端用户身份重合，缓解措施：**admin 角色授权门**，deeper/target identity 走同 admin 门的 auditquery；slog 出口全局 sink redaction |
| 截断响应（hasMore=true）误导 oncall 以为已完整查看 | 响应体明确携带 `hasMore` flag + `returned` 计数（K8s list-completeness 模式），指引使用 auditquery 完整分页；ops 文档记录 500 上限与缩小时间窗口建议 |
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
