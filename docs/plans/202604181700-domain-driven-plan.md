# GoCell 域驱动实施计划

> 生成日期: 2026-04-18 17:00
> 更新: 2026-04-18（同步 PR#170~174）
> 基准: develop@dde5cae（PR#165-169 合并后）
> 策略: 按**功能域**聚合，每域所有层（kernel/runtime/adapter/slice）改动一次性在一个 PR 串内闭合
> 替代: `20260418-bottom-up-implementation-plan.md`（按层组织，Phase K/R/P/A/S/F）
>
> 标记说明:
> 🟡 可延后 = 不卡正确性或安全（latent risk / DX / tech debt / 打磨）
> 🟠 条件延后 = 有明确触发条件，触发前可延

---

## 已完成项（不重复排期）

- **Phase 0**：PR#143 + PR#135/136/137（H1-1~H1-6）
- **Phase 0.5**：PR#166（P0-1 AUTH-TOKEN-INTENT-01）、PR#168（P0-2 AUTHZ-WRITE-CONFIG-WRITE-01）
- **Phase P**：PR#163（CB）、PR#164（CMD）、PR-P-QUERY（P1-6/P1-7 + ADR-RUNMODE-TRANSLATION-01）
- **S1**（REPO-SCAN-CLASSIFY-01）：PR#169 合入
- **PR#170+171**：P1-11（aud 强验证 + VerifyIntent 单一 API）、P1-1（auth 集成测试）、S9（legacy token strict 可观测性）
- **PR#172**：P1-10 AUTH-DX-01（sso-bff walkthrough curl drift + .timestamp + 随机 seed 密码）、S20（config walkthrough）、S21（HMAC doc）
- **PR#173（pending merge，所有 review issues 已修复）**：A12（schema guard fail-fast）、A3（relay PG 集成测试）、A4（online-safe migration 006 + INVALID pre-check）、T7（config_versions config_id index）
- **PR#174**：S24（WithBrokerHealth nil fail-fast）、S25（A11 relay wiring guard）、S26（eventbus envelope 解包修复事件丢失）、S27（pool leak on metrics fail）；**A11 OUTBOX-RELAY-WIRE-PG-01 完成**

---

## 设计决策维持（不重开）

| 决策 | 结论 |
|------|------|
| F1-3 DurabilityDurable + in-memory 合法 | 维持（effectiveMode + adapterInfo + slog 日志已透明；PG-REPO 是真修路径）|
| F1.2/F2.5/F4.2/F6.2 auth 四项 | 维持（详见 backlog 设计决策记录 PR#159）|
| F6 slice.yaml allowedFiles 双路径 | 维持（项目惯例，FMT-14 守护）|
| I2 generator 全量遍历 contracts | 维持（FMT-09 守护 + fail-fast defense-in-depth）|
| T6 GOCELL-PER-CELL-ADAPTER-01 不做 | 已裁决：全量 PG 接入，全局开关直接复用 |

---

## 域 1：Auth 域

**目标**：完成 aud claim 强验证、auth 集成测试覆盖、AUTH-DX 文档，使认证层对标 RFC 8725 / RFC 9068。

**域间依赖**：无前置域，是其他域的安全基础，Wave 1 优先启动。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| ~~P1-11~~ | ~~**PR-R-AUTH-AUD-VALIDATION**~~ ✅ PR#170 + PR#293: aud 强验证 + `TokenVerifier`/`Verify()` 删除（双 API 合并）+ `ErrAuthVerifierConfig` fail-fast | — | — | PR#166 R1-F2-5 |
| ~~P1-1~~ | ~~**AUTH-INT-REACHABILITY-01**~~ ✅ `cells/access-core/auth_integration_test.go` 4 测试全覆盖 | — | — | 2026-04-18 外部审查 |
| ~~S9~~ | ~~**AUTH-LEGACY-TOKEN-STRICT-01**~~ ✅ 项目不向后兼容，strict 模式开关不需要；PR#293 可观测性已落地 | — | — | PR#159 外部审查 |
| ~~P1-10~~ | ~~**AUTH-DX-01**~~ ✅ PR#172：sso-bff walkthrough curl drift 修复 + .timestamp 字段 + 随机 seed 密码；seed 用户未移至 P1-12（仍保留，改用随机密码）| — | — | PR#172 |
| X10 | **AUTH-REFRESH-OPAQUE-01** 🟠（PG-REPO 域上线后触发）：refresh token 改 opaque string + server-side rotation store（RFC 6819 §5.2.2.2）| 1-2d | 🟠 | `runtime/auth/` + `adapters/postgres/` | PR#166 R1-F2-7 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| ~~PR-AUTH-AUD~~ | ~~P1-11：aud claim 强验证 + mismatch 测试~~ ✅ PR#170 + PR#293 | — |
| ~~PR-AUTH-INT~~ | ~~P1-1：auth 集成测试到达性补全~~ ✅ | — |
| PR-AUTH-DX | P1-10：README + sso-bff 修复 | 4h |
| ~~PR-AUTH-STRICT~~ | ~~S9：legacy token strict 模式~~ ✅ 不需要 | — |
| PR-AUTH-OPAQUE | X10：refresh token 不透明化（🟠 PG-REPO 后触发）| 1-2d |

**主线工时**：4h（P1-10 AUTH-DX）；条件项另算。

---

## 域 2：Config-core 域

**目标**：修复 demo fail-open 语义漂移、完善契约错误 schema、降低 Init 认知复杂度，使 config-core 发布路径语义正确。

**域间依赖**：无强前置，与 Auth 域可并行（改不同文件）。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| ~~P1-2~~ | ~~**CONFIG-DEMO-FAILOPEN-01**~~ ✅ `publishEvent` fail-open 已用 `runMode.IsDemo()` 守护；durable 模式 `runMode=RunModeProd`，`IsDemo()` 返回 false，不可触发 | — | — | — | PR#157 post-merge |
| ~~S2~~ | ~~**CONTRACT-ERROR-SCHEMA-01**~~ ✅ PR-CONFIG-POLISH：`publish/v1` + `rollback/v1` contract.yaml `responses` 401/403 + kernel HTTPResponseMeta + shared error schema + contracttest.ValidateErrorResponse | — | — | — | PR-CONFIG-POLISH |
| ~~S11~~ | ~~**CONFIG-CORE-INIT-COGNIT-01**~~ ✅ commit 8552b6c：`cell.go::Init()` 认知复杂度降至 ≈7，nolint 已移除 | — | — | — | PR#168 发现 |
| ~~S13~~ | ~~**CONFIGWRITE-4XX-OBSERVABILITY-01**~~ ✅ PR-CONFIG-POLISH：`writeErrcodeError` 4xx 分支 slog.Warn {code, status, message, request_id, trace_id, span_id} | — | — | — | PR-CONFIG-POLISH |
| ~~S10~~ | ~~**MODE-SEMANTIC-SPLIT-01**~~ ❌ 方向错误：P1-2 正确修法是删除 `configpublish.Service.runMode` + fail-open 整体，写路径不再有 mode 字段，PublishFailureMode 无处安放；裁定废弃 | — | — | — | PR#167 round-2 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| ~~PR-CONFIG-FAILOPEN~~ | ~~P1-2：demo fail-open 修复~~ ✅ 已完成，无需单独 PR | — |
| ~~PR-CONFIG-POLISH~~ | ~~S2 + S11 + S13：契约 schema + Init 重构 + 4xx 日志~~ ✅ 已完成 | — |
| ~~PR-CONFIG-MODE~~ | ~~S10：读写 RunMode 解耦~~ ❌ 废弃，随 P1-2 正确修法一并消除 | — |

**主线工时**：0h（P1-2 已完成）；🟡 全做约 5h（S10 废弃）。

---

## 域 3：PG 加固域

**目标**：强化 PostgreSQL readiness 检查、补齐在线安全索引、完善 relay 集成测试，使 PG adapter 生产就绪。

**域间依赖**：A12 无前置，Wave 1 可立即启动。A4/A3 可搭车或独立排期。

> **PR#173 状态（2026-04-18）**：`fix/294-pg-harden` 实现了 A12/A3/A4/T7；review 发现 9 issues，**全部已在后续 commits 修复**（errcode import、DBAhead 测试、unit test 覆盖、table name、identifier check、timing bug 等）；**pending CI 通过后 merge**。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| ~~A12~~ | ~~**READYZ-PG-SCHEMA-01**~~ ✅ PR#173（pending merge）：`VerifyExpectedVersion` 启动期 fail-fast；schema guard + integration test | — | — | PR#173 |
| ~~A4~~ | ~~**RL-MIG-01**~~ ✅ PR#173（pending merge）：migration 006 `CREATE INDEX CONCURRENTLY` + Up() 边界 INVALID index pre-check | — | — | PR#173 |
| ~~A3~~ | ~~**RL-INT-01**~~ ✅ PR#173（pending merge）：5 个 testcontainers PG+RMQ 测试；真实 TCP 断连/恢复测试已加入 | — | — | PR#173 |
| ~~T7~~ | ~~**CONFIG-VERSIONS-CONFIG-ID-INDEX**~~ ✅ PR#173（pending merge）：`006_add_config_versions_config_id_index.sql` | — | — | PR#173 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| ~~PR-PG-READYZ~~ | ~~A12~~：✅ PR#173 pending merge | — |
| ~~PR-PG-MIG~~ | ~~A4 + T7~~：✅ PR#173 pending merge | — |
| ~~PR-PG-INTEG~~ | ~~A3~~：✅ PR#173 pending merge | — |

**主线工时**：1h（A12 主线）；全做约 5.5h。

---

## 域 4：Outbox/RabbitMQ 域

**目标**：修复 outbox relay worker 未接入 PG 模式、补齐 broker health check、完善 subscriber 校验、将 relay/distlock 生命周期上抬到 runtime 层，使 outbox 链路端到端可靠。

**域间依赖**：K2 作为 PR-OUTBOX-WIRE 头部 commit 先行（不再独立 PR，理由：K2 为 Go 内部 API 契约，与 HTTP schema CONTRACT-* 任务类型不同，不合并）。本域 PR-OUTBOX-WIRE 完成后，RBAC 域 S8（`sessionlogout/consumer.go` 首个跨 cell outbox consumer）才具备落地基础。A11 e2e 复用 PR-PG-HARDEN 已落地的 `outbox_relay_integration_test.go` testcontainers harness（PG+RMQ），原 A2 3-container 范围吸收进 A11。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| K2 | **OBS-RELAY-REGISTER-ATOMIC-01**（WIRE 头部 commit）：`outbox.NewProviderRelayCollector` 5 个 metric 原子注册，`Provider.Unregister` 支持或文档化契约 | 2h | 🟡（先行 commit） | `kernel/outbox/` + `kernel/observability/metrics/` | PR#157 review S3-05 |
| ~~A11~~ | ~~**OUTBOX-RELAY-WIRE-PG-01**~~ ✅ PR#174：relay worker 接入 bootstrap OnStart/OnStop（S25）；eventbus envelope 解包修复 PG→eb 路径事件丢失（S26）；pool leak on metrics fail 修复（S27）；e2e 重写为真实 PG→eventbus→subscriber 链路（RMQ 依赖移除，以 in-memory eventbus 覆盖）| — | — | PR#174 |
| A1 | **READYZ-BROKER-HEALTH-01** (Cx3)：`Connection.Health() error` + bootstrap health checker 自动注册；`WithBrokerHealth(opts...)` 开关（**S24 PR#174 已完成 nil fail-fast 部分；`Connection.Health()` + health checker 注册仍待**）| 1.5h | P1 | `adapters/rabbitmq/connection.go` + `runtime/bootstrap/` | 2026-04-18 外部审查 |
| X7 | **AL-01 outbox_relay 调度 → runtime/outbox/relay.go** 🟡（搭车 A11）：轮询调度从 adapter 上抬到 runtime，使 A11 bootstrap 接线语义正确（框架托管 worker，非 adapter 自运行）| 2h | 🟡 | `adapters/postgres/outbox_relay.go` → `runtime/outbox/relay.go` | 依赖替换分析 |
| A5 | **RL-SUB-01** 🟡：入站 ID 校验（空/过长 message ID）| 1h | 🟡 | `adapters/rabbitmq/subscriber.go` | PR#46 review |
| A6 | **RabbitMQ backoff + FailOpen enum 清理** 🟡 | 2h | 🟡 | `adapters/rabbitmq/` | Wave 2 残留 |
| X6 | **SOL-B-01 Claimer lease 续租** 🟡（前置 L4 API ✅）：两阶段 Claim/Commit/Release 幂等路径补 lease 续租 | 4h | 🟡 | `kernel/outbox/` + `adapters/rabbitmq/consumer_base.go` | Wave 2 残留 |
| X8 | **AL-02 distlock 续期/TTL → runtime/** 🟡：distlock 生命周期上抬到 runtime，与 relay 共同组成 outbox 交付基础设施 | 2h | 🟡 | `adapters/redis/distlock.go` → `runtime/` | 依赖替换分析 |

**搭车说明**：
- A1 改 `adapters/rabbitmq/connection.go`，A7（POOLSTATS-IFACE-01，见域 9）也改同一文件，合并到 PR-OUTBOX-WIRE 一次落地。
- ~~原 A2 P4-TD-05（3-container 集成测试）吸收进 A11 e2e~~ → A11 e2e 已由 PR#174 以 PG+eventbus 形式落地，RMQ 3-container 路径留存为 A2 可选强化项。
- 原 A10 OBS-LGTM-INTEGRATION-01（OTel OTLP 夜间测试）与 outbox 无直接耦合，转投域 9 可观测性/Bootstrap。

**域内 PR 拆分**（A11 已完成，PR-OUTBOX-WIRE 缩减）：

| PR | 内容 | 工时 |
|----|------|------|
| ~~PR-OUTBOX-WIRE（A11 部分）~~ | ~~A11 relay 接线 + e2e~~ ✅ PR#174 | — |
| PR-OUTBOX-WIRE（剩余） | K2（头部 commit）+ A1 完整（Health() + 注册）+ X7 搭车 + A7 搭车 | ~5h |
| PR-OUTBOX-HARDEN | A5 + A6 + X6 + X8（🟡）| ~9h |

**主线工时**：5h（PR-OUTBOX-WIRE 剩余）；全做约 14h。

---

## 域 5：RBAC 域

**目标**：修复 revoke HTTP 方法、补齐最后 admin 保护、将角色变更接入 transactional outbox，使 RBAC 操作原子安全。

**域间依赖**：S5/S6 无前置，Wave 2 可提前并行；S8（RBAC-OUTBOX-MIGRATION）强依赖 Outbox/RabbitMQ 域（A11 outbox consumer 基础设施）。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| ~~S5~~ | ~~**RBAC-REVOKE-POST-01**~~ ✅ contract.yaml + handler.go 已是 `POST /revoke`，早于计划落地 | — | — | — | PR#143 review 6.2 |
| ~~S6~~ | ~~**RBAC-LAST-ADMIN-GUARD**~~ ✅ `CountByRole` 已在 ports + mem 实现；`RemoveFromUserIfNotLast` 原子持写锁，无 TOCTOU | — | — | — | PR#143 review 2.3 |
| S8 | **H1-7 RBAC-OUTBOX-MIGRATION** (P2)：角色变更 → 会话失效双写 → transactional outbox 原子写入 + consumer 异步失效 session；新建 `sessionlogout/consumer.go` | 6h | P2 | `cells/access-core/slices/rbacassign/service.go` + `cells/access-core/slices/sessionlogout/consumer.go`（新） | PR#149 review round 2 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| ~~PR-RBAC-GUARD~~ | ~~S5 + S6：revoke POST + last-admin guard~~ ✅ 已完成，无需单独 PR | — |
| PR-RBAC-OUTBOX | S8：RBAC-OUTBOX-MIGRATION（前置 Outbox 域 A11 完成）| 6h |

**主线工时**：6h（仅 S8）。

---

## 域 6：HTTP/Router 域

**目标**：升级公共端点 method-aware 匹配、收口 handler nil 语义、统一 auth guard inline，消除路由层 latent risk。

**域间依赖**：P1-3/S3 改 `runtime/http/router/router.go` + handler，与 Auth 域（改 `runtime/auth/jwt.go`）文件不重叠，Wave 2 可并行。R4（INTERNAL-LISTENER）改 `runtime/bootstrap/bootstrap.go`，与域 9 R1 同文件，建议 R4 先于 R1 或同域合并。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| P1-3 | **PUBLIC-ENDPOINT-METHOD-MATCH-01** 🟡 (Cx3)：路由公共端点升级为 `"METHOD /path"` 格式（向后兼容：无 method 前缀匹配所有 method）| 4h | 🟡 P1 | `runtime/http/router/router.go` + `runtime/auth/middleware.go` + `runtime/bootstrap/bootstrap.go` | PR#158 six-seat review |
| S3 | **DTO-NIL-SEMANTIC-01** (Cx2)：12+ handler 写成功响应前校验领域对象非 nil，避免空 data 成功响应 | 3h | P2 | 12+ `cells/*/slices/*/handler.go` | PR#158 six-seat review |
| S12 | **AUTH-GUARD-INLINE-UNIFY-01** 🟡 (Cx3)：全库 11 处 `RequireAnyRole → WriteDomainError → return` 统一提取，横跨 3 cell | 2h | 🟡 | `cells/*/slices/*/handler.go` × 11 | PR#168 review P2 |
| R4 | **INTERNAL-LISTENER-01** 🟡 (Cx4)：`/internal/v1/` 独立 listener 或 service-token/mTLS 策略；4-8h 上界，规模风险 | 4-8h | 🟡 | `runtime/bootstrap/bootstrap.go` + 路由注册拆分 | PR#143 review F1 |

**风险说明**：R4 若实施 blast radius 超预期，降级到 PG-REPO 大项后处理，不阻塞主线。

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-ROUTER-METHOD | P1-3：method-aware 公共端点匹配 | 4h |
| PR-ROUTER-DTO-NIL | S3：DTO nil 语义 12+ handler | 3h |
| PR-ROUTER-GUARD | S12：auth guard inline 统一（🟡）| 2h |
| PR-ROUTER-INTERNAL | R4：internal listener 独立（🟡 规模评估后排期）| 4-8h |

**主线工时**：7h（P1-3 + S3）；全做约 17h。

---

## 域 7：Events/DTO 域

**目标**：将 6 个 slice 的事件 payload 从 `map[string]any` 升级为 typed event struct，提升事件契约稳定性和类型安全。

**域间依赖**：S4 改动 3 个 cell（access-core + config-core + audit-core），建议 Auth 域和 Config-core 域稳定后再做，避免 rebase 冲突。Wave 3 启动。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| S4 | **EVENT-PAYLOAD-TYPED-01** (Cx2)：sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct | 3h | P2 | 6 个 `service.go` + event contract schemas | PR#133 re-review |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-EVENTS-TYPED | S4：6 个 service.go typed event struct + schemas | 3h |

**主线工时**：3h。

---

## 域 8：Features 域

**目标**：实现 Device List API 和 Flag Write API 补齐前后端联调缺口，AUTH-DX 文档更新在 Auth 域稳定后收口。

**域间依赖**：P1-8/P1-9 无强依赖，Wave 2 可启动；P1-10（AUTH-DX）前置 Auth 域（PR-AUTH-AUD + PR-AUTH-INT）稳定；F2（SYSTEM-TOPOLOGY-API）可独立延后。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| P1-8 | **FEAT-1 DEVICE-LIST-API**：新建 `device-list` slice + `GET /api/v1/devices` 分页 + contract + contract_test | 3h | P1 | `cells/device-cell/slices/device-list/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| P1-9 | **FEAT-2 FLAG-WRITE-API**：`PUT /api/v1/config/flags/{key}` 写入端点 + contract + contract_test | 3h | P1 | `cells/config-core/slices/configwrite/` + `contracts/http/config/flags/write/v1/` | backend_issues.md #2 |
| ~~P1-10~~ | ~~**AUTH-DX-01**~~ ✅ PR#172 | — | — | — | PR#172 |
| F2 | **SYSTEM-TOPOLOGY-API** 🟡：`GET /internal/v1/system/topology` 返回 cell/slice/contract 拓扑 JSON | 4h | 🟡 | 新 slice 或 `runtime/bootstrap/` | 历史 Batch 8 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-FEAT-DEVICE | P1-8：device-list slice + 分页 API | 3h |
| PR-FEAT-FLAG | P1-9：flag-write 端点 | 3h |
| PR-FEAT-AUTH-DX | P1-10：AUTH-DX README（前置 Auth 域稳定）| 4h |
| PR-FEAT-TOPOLOGY | F2：topology API（🟡 可延后）| 4h |

**主线工时**：10h（P1-8 + P1-9 + P1-10）。

---

## 域 9：可观测性/Bootstrap 域（🟡 全部可延后）

**目标**：拆分 bootstrap.go 认知复杂度、自动接线 HTTP collector、统一三 adapter PoolStats 接口、夜间 OTLP 协议兼容测试。

**域间依赖**：R1 改 `runtime/bootstrap/bootstrap.go`，与 HTTP/Router 域 R4 同文件，建议 R4 完成后再做 R1。A7（POOLSTATS-IFACE-01）改 `adapters/rabbitmq/connection.go`，建议搭车 Outbox 域 PR-OUTBOX-WIRE（同文件）。A10 从域 4 迁入（`adapters/otel/` 与 outbox 无直接耦合，归属可观测性域更合适）。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| R1 | **BOOTSTRAP-RUN-COGNIT-01** 🟡 (Cx3)：`bootstrap.go::Run()` 认知复杂度 225；拆 `validateOptions()` / `buildRouter()` / `startServers()` 三段式 | 4h | 🟡 | `runtime/bootstrap/bootstrap.go` | PR#163 agent 报告 |
| R2 | **OBS-HTTP-COLLECTOR-AUTOWIRE-01** 🟡 (Cx3)：`bootstrap.WithMetricsProvider` 自动构造 `NewProviderCollector`；`WithHTTPCollectorCellID` option | 2h | 🟡 | `runtime/bootstrap/bootstrap.go` + `runtime/http/middleware/` | PR#157 review S4-01 |
| R3 | **OB-02** 🟡：safe_observe broken logger 注入测试 | 1h | 🟡 | `runtime/http/middleware/safe_observe_test.go` | 历史 backlog 0-J |
| A10 | **OBS-LGTM-INTEGRATION-01** 🟡（从域 4 迁入）：`//go:build integration` 夜间 OTel collector 真实 OTLP 协议兼容性测试 | 2h | 🟡 | `adapters/otel/integration_test.go` | PR#157 review S6-04 |
| A7 | **POOLSTATS-IFACE-01** 🟡：三个 adapter PoolStats 公共接口（OTel collector 消费）；**建议搭车 Outbox 域 PR-OUTBOX-WIRE** | 1h | 🟡 | `adapters/postgres/pool.go` + `redis/client.go` + `rabbitmq/connection.go` | PR#134 review |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-OBS-BOOTSTRAP | R1 + R2 + R3 + A10：bootstrap 拆分 + HTTP collector 自动接线 + safe_observe 测试 + OTLP 夜间测试 | 9h |
| A7 搭车 Outbox 域 | POOLSTATS-IFACE-01 合并到 PR-OUTBOX-WIRE | 1h |

**主线工时**：10h（全部 🟡 可延后）。

---

## 域 10：DX/CI/工具链域（🟡 全部可延后，P3 优先级）

**目标**：机器可读验证输出、metadata 解析性能基准、CI 供应链加固、全仓库现代化清理。

**域间依赖**：P1-4 + K1 改 `kernel/governance/` + `cmd/gocell/`，相关合并一 PR；S7/A8/A9 改 `.github/workflows/ci.yml`，合并一 PR；X9 全仓清理独立 PR，不混功能 PR。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| P1-4 | **OUTPUT-JSON-SARIF-01** 🟡 (P1, Cx3)：`gocell validate` 新增 JSON + SARIF 输出通道；统一诊断模型 `Issue` struct → 多 printer | 6h | 🟡 P1 | `cmd/gocell/` + `kernel/governance/` | PR#152 round-2 review |
| K1 | **METADATA-PROJECTLOC-IFACE-01** 🟡 (P2, Cx3)：提取 `ProjectLocator interface` 隐藏 yaml.v3 AST；`FileNodes` 不再泄漏 | 3h | 🟡 | `kernel/metadata/` + `kernel/governance/` + `cmd/gocell/` | PR#152 seat-1 |
| P1-5 | **METADATA-PERF-BENCH-01** 🟡 (P1, Cx3)：`BenchmarkParseFS_500Files` 性能基准 + goccy/go-yaml 迁移成本评估 | 4h | 🟡 P1 | `kernel/metadata/parser_test.go` | PR#152 seat-4 |
| S7 | **VALIDATE-EVIDENCE-CI-01** (P2, Cx2)：CI 新增独立 `metadata-check` job 失败阻断 PR；PR template 增 metadata gate 勾选项 | 1h | P2 | `.github/workflows/ci.yml` + PR template | PR#155 review F7 |
| A8 | **CI-DIGEST-01** 🟡：testcontainers 镜像 tag+digest 双固定 | 1h | 🟡 | `adapters/*/integration_test.go` | PR#139 review |
| A9 | **CI-LINT-PIN-01** 🟡：golangci-lint patch 级固定 + dependabot | 1h | 🟡 | `.github/workflows/ci.yml` | PR#139 review |
| X9 | **LINT-MODERN-01** 🟡 (P3, Cx2)：全仓 modernization 清理（rangeint / stringsseq / nhooyr.io→coder/websocket）；独立 PR，不混功能 | 6h | 🟡 P3 | 全仓 | PR#163 post-review |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-DX-VALIDATOR | P1-4 + K1：JSON/SARIF 输出 + ProjectLocator 接口 | 9h |
| PR-DX-METAPERF | P1-5：metadata parser 性能基准 | 4h |
| PR-DX-CI | S7 + A8 + A9：CI metadata gate + 镜像固定 + lint 固定 | 3h |
| PR-DX-LINT-MODERN | X9：全仓现代化清理（独立 PR）| 6h |

**主线工时**：22h（全部 🟡/P3 可延后，Wave 4 机会性纳入或 v1.0 后做）。

---

## 域 11：PG-REPO 大项（Phase X，独立排期）

**目标**：完成 access-core/audit-core/device-cell 的 PostgreSQL Repository 实现，激活 Redis session cache，完成 RBAC level 升级，使所有 cell 脱离内存存储。

**域间依赖**：最重量级的独立大项。本域完成后可触发 Auth 域 X10（AUTH-REFRESH-OPAQUE-01）。RBAC 域 S8 依赖 Outbox 域，不依赖本域。

**已完成（PR#169）**：`004_create_config_entries_and_versions.sql`、`005_recreate_outbox_pending_concurrent.sql`、config-core PG repo + 集成测试、`GOCELL_CELL_ADAPTER_MODE` config-core 接线。

**待做**：

| # | 任务 | 工时 | 备注 |
|---|------|------|------|
| X1-DDL | migration DDL：users / sessions / roles / devices+commands | 0.5d | `adapters/postgres/migrations/006+.sql` |
| X1-ACCESS | access-core PG repo（User / Session / Role）| 1d | `cells/access-core/internal/adapters/postgres/`（新建）|
| X1-AUDIT | audit-core / device-cell / order-cell PG repo | 0.5-1d | `cells/*/internal/adapters/postgres/` |
| X1-LINK | 落地联动：RBAC-ASSIGN-LEVEL-UPGRADE-01 L0→L1 + SEED-ROLE-IFACE-01 去 type assertion + ACCESS-LEVEL-AUDIT-01 slice.yaml 校正 + AUTH-CACHE-01 激活 Redis session cache | 1-2d | 同 PR 或紧邻 PR |
| S16 | **RUNTIME-TOPOLOGY-SINGLE-SOURCE-01** 🟡 (P2, Cx3)：运行拓扑单一事实源，消除 ENV 分裂（最小修复已合 PR#169；彻底方案随本域）| 6h | PR#169 review F-NEW-2 |
| S17 | **POOL-FRAMEWORK-LIFECYCLE-01** 🟡 (P2, Cx3)：外部资源提升为 bootstrap 托管，统一 LIFO shutdown + 自动 health checker 注册（最小修复已合；彻底方案随本域）| 4h | PR#169 review F-NEW-3 |
| S14 | **CONFIG-VALUE-ENCRYPTION-01** (P1↑, Cx3)：sensitive=true 明文存库；需 KMS 选型 + key rotation 独立 ADR，独立 PR | TBD | PR#169 + 2026-04-18 静态审查 |
| S15 | **ERROR-CTX-CANCELLED-CLASSIFY** 🟡 (P3, Cx2)：`ctx.Canceled` 归类 `ErrContextCanceled` | 1h | PR#169 review F-T-3 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-PGREPO-DDL | X1-DDL：006+ migration DDL | 0.5d |
| PR-PGREPO-ACCESS | X1-ACCESS：access-core PG repo + 集成测试 | 1d |
| PR-PGREPO-AUDIT-DEVICE | X1-AUDIT：audit-core + device-cell PG repo | 0.5-1d |
| PR-PGREPO-LINK | X1-LINK：落地联动（RBAC level + seed + cache 激活）| 1-2d |
| PR-PGREPO-TOPOLOGY | S16 + S17：runtime topology 单一事实源 + pool 托管（🟡 搭车）| 10h |
| PR-PGREPO-ENCRYPT | S14：config value 加密（独立 PR，需 KMS ADR）| TBD |

**域估算（主线 DDL + ACCESS + AUDIT + LINK）**：约 3-5d；含 S16/S17 彻底方案 +10h；S14 独立排期。

---

## 推荐执行 Wave

```
Wave 1（✅ 全部完成）
  ├── Auth 域     PR-AUTH-AUD ✅ PR#170+171 + PR-AUTH-INT ✅ PR#171
  ├── Config 域   PR-CONFIG-FAILOPEN ✅
  └── PG 加固域   PR-PG-READYZ：PR#173 pending fix（F-1 compile error）

Wave 2（进行中）— 部分并行
  ├── Outbox 域   PR-OUTBOX-WIRE (A11 ✅ PR#174；剩余 K2+A1+X7 ~5h)
  ├── RBAC 域     PR-RBAC-GUARD ✅                         [S5/S6 均已完成]
  ├── HTTP 域     PR-ROUTER-METHOD (4h)
  └── Features 域 PR-FEAT-DEVICE (3h) + PR-FEAT-FLAG (3h)

Wave 3（依赖 Wave 1-2 完成）— 约 2-3 工作日
  ├── Events 域   PR-EVENTS-TYPED (3h)                     [等 Auth + Config 稳定]
  ├── RBAC 域     PR-RBAC-OUTBOX (6h)                      [A11 ✅ 依赖已满足，可启动]
  ├── HTTP 域     PR-ROUTER-DTO-NIL (3h)
  ├── Features 域 PR-FEAT-AUTH-DX (3h)                     [等 Auth 域稳定]
  └── Config 域   PR-CONFIG-POLISH (5h, 🟡)

Wave 4（🟡 可延后，机会性纳入）— 按资源排期
  ├── 可观测性/Bootstrap 域  R1 + R2 + R3 + A10 (9h)
  ├── DX/CI/工具链域         S7 + A8 + A9 (3h) + P1-4/K1 (9h) + P1-5 (4h) + X9 (6h)
  ├── Outbox 域              PR-OUTBOX-HARDEN (9h, 含 X6 + X8)
  ├── HTTP 域                PR-ROUTER-GUARD (2h) + PR-ROUTER-INTERNAL (4-8h, 规模评估)
  └── Features 域            PR-FEAT-TOPOLOGY (4h, 🟡)

Wave 5（Phase X，独立大项，发布后按需排期）
  PG-REPO 大项   DDL → ACCESS → AUDIT-DEVICE → LINK → TOPOLOGY → ENCRYPT
                 约 3-5d + S16/S17 10h + S14 TBD

  触发后做：
    X10 AUTH-REFRESH-OPAQUE     （PG-REPO 上线后触发，1-2d）
    T7  CONFIG-VERSIONS-INDEX   （>100w 行或 seq scan 触发，0.5h）
    S9  AUTH-LEGACY-STRICT      （产品确认迁移窗口后，1h）
    S10 MODE-SEMANTIC-SPLIT     （枚举扩展时触发，3h）
```

---

## 域间依赖总览

```
Auth 域 ──────────────────────────────────────────────────────────► Features 域（P1-10 前置）
                                                                     ▲
Config-core 域 ───────────────────────────────────────────────────► Events/DTO 域（稳定后再做）

PG 加固域（A12 独立）

Outbox/RabbitMQ 域（K2 作为 WIRE 头部 commit；吸收原 A2；A10 迁出至域 9）
  └──► RBAC 域（S8 强依赖 A11 outbox consumer 基础设施）

HTTP/Router 域（R4）
  └──► 可观测性/Bootstrap 域（R1 同文件，R4 先于 R1）

PG-REPO 大项（Phase X，独立）
  └──► 触发 Auth 域 X10（AUTH-REFRESH-OPAQUE-01）
```

---

## 工时汇总表

| 域 | P1 主线工时 | 🟡 可延后工时 | 🟠 条件项工时 | 总计 | 状态 |
|----|------------|--------------|--------------|------|------|
| 1. Auth 域 | **3h**（DX 剩余） | — | 1h + 1-2d | ~7h | Wave 1 核心 ✅；P1-10 剩余 3h |
| 2. Config-core 域 | 0h | 7h | 3h | ~10h | P1-2 ✅ |
| 3. PG 加固域 | 1h（A12）| 2h | 2.5h | ~5.5h | PR#173 pending fix |
| 4. Outbox/RabbitMQ 域 | **5h**（A1+K2+X7）| 9h | — | **~14h** | **A11 ✅ PR#174** |
| 5. RBAC 域 | 6h（S8）| — | — | ~6h | S5/S6 ✅ |
| 6. HTTP/Router 域 | 7h | 10h | — | ~17h | — |
| 7. Events/DTO 域 | 3h | — | — | 3h | — |
| 8. Features 域 | 9h（P1-10 剩 3h）| 4h | — | 13h | — |
| 9. 可观测性/Bootstrap 域 | — | 10h | — | 10h（全🟡）| — |
| 10. DX/CI/工具链域 | 1h | 21h | — | 22h | — |
| 11. PG-REPO 大项（Phase X）| — | 3-5d + 10h | TBD | 独立排期 | — |
| **Wave 1-3 核心路径合计（更新）** | **~34h（约 4-5 工作日）** | | | | A11 ✅ 节省 ~6h |
| **Wave 1-4 全量（不含 Phase X）** | | | | **~113h（约 14 工作日）** | |

---

## 触发条件项（不占主线排期）

| # | 任务 | 触发条件 |
|---|------|----------|
| T1 | AUTH-PROVIDER-EXPORT-01 | 第二个 auth provider cell |
| T2 | AUTH-ISSUE-OPTIONS-01 | `Issue()` 第 5 个参数 |
| T3 | DEVICE-ENQUEUE-RBAC | 多租户 operator |
| T4 | CB-RESILIENCE-PACKAGE-01 | 非 HTTP 的 CB 消费方 |
| T5 | AUTH-SIGNER-01 | golang-jwt v6 发布 |
| S9 | AUTH-LEGACY-TOKEN-STRICT-01 | 产品确认迁移窗口 |
| S10 | MODE-SEMANTIC-SPLIT-01 | 任一方向需新增非二元模式值 |
| T7 | CONFIG-VERSIONS-CONFIG-ID-INDEX | >100w 行或 EXPLAIN seq scan |
| X10 | AUTH-REFRESH-OPAQUE-01 | PG-REPO 域上线后 |
