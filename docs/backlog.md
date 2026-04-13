# GoCell Backlog

> 只含待办事项。已完成项（PR#67-91）归档至 `docs/reviews/archive/202604121800-backlog-pre-restructure.md`。
> 更新日期: 2026-04-13（PR#102-108 状态更新 + B8→6A/6B 合并 11 项，减少 ~10 PR）
> Batch 1-4: ✅ 全部完成（PR#67-91，25 个 PR）

---

## Batch 5A: 六席位 P1 修复（~34h，6 路全并行）

> 来源: PR#87/89/90/91 六席位复核。正确性/安全问题，功能推进之前优先修复。

| PR | 任务 | 工时 | 文件范围 |
|----|------|------|---------|
| ~~CURSOR-FIX~~ | ~~P1-01~~ ✅ PR#94 + ~~P1-02~~ ✅ PR#94 + ~~P1-03(scope/context强制)~~ ✅ PR#95 + ~~P2-01~~ ✅ PR#94 + ~~P4-TD-11~~ ✅ PR#94 + ~~P4-TD-14~~ ✅ PR#94 + ~~WM-6-F2~~ ✅ PR#94 | ✅ 全部完成 | — |
| ~~HTTP-SEC-FIX~~ | ~~HTTP-SEC-01(IP格式校验)~~ ✅ PR#96 `normalizeIPToken` + `net.ParseIP` 校验 + ~~SEC-02(trusted proxy fail-fast)~~ ✅ PR#96 `ValidateTrustedProxies` + `NewE` error return | ✅ done | — |
| CONTRACT-FIX | ~~STRICT-P1-01(contract覆盖)~~ ✅ PR#98 + ~~P1-02(contract_test可执行)~~ ✅ PR#98 + ~~SCHEMA-01(空壳schema)~~ ✅ PR#98 | 8h → ✅ done | `contracts/http/` + `cells/*/contract_test.go` |
| ~~CFG-WATCHER~~ | ~~CFG-P1-01(目录级监听)~~ + ~~P1-02(shutdown顺序)~~ + ~~P2-02(补测试)~~ | ✅ PR#97 | — |
| ~~CFG-RELOAD~~ | ~~CFG-P1-03(generation counter)~~ + ~~P1-04(DeepCloneValue)~~ + ~~WM-34-F4(commit语义)~~ | ✅ PR#97 | — |
| ~~BATCH3-FIX~~ | ~~OB-02(safe_observe测试)~~ ✅ PR#96 `safe_observe_test.go` + ~~WriteErrorWithContext(25+ handler)~~ ✅ 全部 48 处已用 `WriteDomainError`/`WriteDecodeError` + ~~PATCH-STRICT(identity PATCH strict decode)~~ ✅ PR#96 `handlePatch` 用 `DecodeJSON`(非 strict) + ~~CURSOR-DECODE-01~~ ✅ PR#99 | ✅ done | — |
| ~~OBS-WIRE~~ | ~~HTTP observability 接入默认链~~ ✅ PR#96 `WithTracer` + `Tracing` middleware 已在默认链 `RequestID→RealIP→Recorder→[Tracing]→AccessLog→[Metrics]→Recovery→SecurityHeaders→BodyLimit` | ✅ done | — |

---

## Batch 5B: 功能推进（~5d，2 轨道并行）

> 前置: Batch 5A 合并后。GAP 硬化: CorrelationID + rate limiter + metadata bridge。

| 轨道 | PR | 任务 | 工时 |
|------|-----|------|------|
| A | ~~韧性~~ | ~~WM-33b(熔断器) + RL-WIRE-01(rate limiter激活)~~ ✅ PR#104 | ✅ done |
| A | 测试基础设施 | ~~WM-20 TestPubSub 认证套件~~ ✅ PR#93 | 1.5d |
| A | ~~可观测~~ | ~~RL-METRICS-01 Relay Prometheus 指标~~ ✅ PR#102 | ✅ done |
| B | 事件模型 | ~~WM-15 L4 队列状态机~~ ✅ PR#93 | 1.5d |
| B | ~~事件路由~~ | ~~ER-ARCH-02 ConsumerGroup 支持~~ ✅ PR#105 | ✅ done |
| B | ~~链路追踪~~ | ~~CID-01(consumer侧) + META-BRIDGE-01(Entry.Metadata注入)~~ ✅ PR#108 | ✅ done |
| B | ~~config 契约收口~~ | ~~CFG-CONTRACT-01/02~~ ✅ PR#98 schema 填充 + PR#101 additionalProperties 加固 + contract_test 验证 | ✅ done |
| ~~⚠ A~~ | ~~🔴 auth 装配缺口~~ | ~~AUTH-WIRE-01(P0 阻塞)~~ ✅ PR#111: (1) router+bootstrap `WithAuthMiddleware` Option (2) core-bundle+sso-bff 生产入口接入 auth (3) `DefaultPublicEndpoints` 修正+清空(公开路径由 composition root 声明) (4) E2E 测试断言 11 条敏感路由 401 | ✅ done | — |
| B | **trace propagation** | **TRACE-PROP-01**: 补 inbound HTTP header extract（W3C traceparent/b3），tracer.Start 前先 extract 上游 context。当前默认每次开根 span，跨服务 trace_id 不连续 | 3h |
| — | ~~contract 运行时闭环~~ | ~~CONTRACT-CLOSURE-01~~: evt- prefix schema 描述修正 + contract route path 对齐 + outbox.Entry.Validate ID 校验 + FMT-13 反向约束 + order-cell durable 模式强化 ✅ PR#106 | ✅ done |

---

## Batch 6A: 运维 + 正确性（~38h，7 路并行）

> 前置: Batch 5B 合并后。P3-DEFER-04+P4-TD-01 **必须最先合入**（阻塞 Batch 6C SOL-B-01）。
> 从 B8 合入: L4-PURE/RETRY→L4-API, DISCARD-PUB→outbox清理, OPS-5→Watcher状态面+连接池, Config增强→Watcher核心(7h)+Watcher状态面(部分)
> 运维 17h 拆 3 PR: Health/Readyz(7h) + Bootstrap加固(6h) + Watcher状态面+连接池(4h); Config增强从B8合入拆为Watcher核心(7h)

| PR | 任务合并 | 工时 | 文件 |
|----|----------|------|------|
| Health/Readyz 体系 | OPS-3(pg/redis Health checker) + ER-P2-03(Router health) + SEC-READYZ-01(/readyz 与 /healthz 隔离) + CFG-P2-01(watcher readyz checker) + READYZ-ROOT(根 readyz 聚合) + **WATCHER-HEALTH-01**(watcher 初始化失败静默降级: `NewWatcher` 失败仅 warn 继续启动，readyz 不含 watcher 状态。修: watcher 注册为 readyz checker 或启动失败时 fail-fast) | 7h | `runtime/http/health/` + `runtime/config/watcher.go` + `runtime/bootstrap/` |
| Bootstrap 加固 + 端点隔离 | OPS-4(drain 期 graceful shutdown) + **BOOT-PANIC-01**(bootstrap panic 漏口: duplicate checker 校验 + registrar safe-call) + **BOOT-OPTION-01**(WithRouterOptions 覆盖框架能力: 拒绝冲突 option 或固定优先级) + **INFRA-EXPOSE-01**(infra 端点过度暴露: /metrics opt-in + health 公开/内部分离或独立 mux) | 6h | `runtime/bootstrap/` + `runtime/http/router/` |
| Watcher 核心增强 | R97-02(config reload debounce) + R97-F1(symlink-pivot 原子切换) + WM-34-F1(目录级监听，支持 configDir 整体变更检测) + F2(watcher metrics，暴露 reload 计数/延迟) + F3(key 过滤，Get/Watch 支持 prefix 过滤) + **R97-04**(Get()返回可变引用，需 DeepCloneValue 防调用方篡改内部状态, C1) + **R97-R3-02**(ShutdownDrain 测试改用 channel 确定性同步替代 300ms 时序) | 7h | `runtime/config/watcher.go` + `runtime/config/store.go` |
| Watcher 状态面 + 连接池指标 | **R97-F3**(Generation observedGeneration 状态面，拆 desired vs applied，需健康端点集成；**依赖 Health/Readyz PR**) + **OPS-5**(PG/Redis/RabbitMQ 连接池指标，从 B8 合入) | 4h | `runtime/config/` + `adapters/postgres/` + `adapters/redis/` + `adapters/rabbitmq/` |
| ~~runtime 竞态修复~~ | ~~R1C2-F01(eventbus 并发回归测试 + Close/Publish 锁序注释) + R1C2-F03(已验证: WorkerGroup cancel-on-error 已覆盖) + R97-R3-01(reload gate 替换 WaitGroup Add-after-Wait 窗口)~~ ✅ PR#107 | ✅ done | `runtime/eventbus/` + `runtime/worker/` + `runtime/bootstrap/` |
| RabbitMQ 连接正确性 | RMQ-RACE-01(WaitConnected竞态) + P3-DEFER-05(Health状态区分) | 4h | `adapters/rabbitmq/connection.go` |
| kernel outbox 清理 | P4-TD-01(NoopOutboxWriter) + P3-DEFER-04(Receipt移包) + **DISCARD-PUB-01**(discardPublisher 提升到 `kernel/outbox.DiscardPublisher`) | 4.5h | `kernel/outbox/` + `kernel/idempotency/` + `cells/order-cell/cell.go` |
| **L4 API 收敛** | **L4-API-01**: Validate 改名 ValidateNew（create-only 语义）+ AdvanceCommand 统一 timestamps/attempt 副作用 + CommandStateAdvancer 暴露完整迁移契约 + **L4-PURE-01**(构造函数 `time.Now()`→参数注入) + **L4-RETRY-01**(`ResetForRetry` 补缺，禁止 adapter 绕过状态机) | 5.5h | `kernel/outbox/l4.go` **(P1, discovered via PR#93 六席位复核; L4-PURE/RETRY 从 B8 合入)** |

---

## Batch 6B: Tech Debt 清理（~36.5h，10 路并行 + RabbitMQ 清理串行）

> 前置: Batch 6A RabbitMQ PR 合入后 RabbitMQ 清理才能开始；其余 10 PR 全并行。
> +11.5h 从 B8 合入: CUR-HDL→cursor回归, FLAG-RACE/P3-TD-12→config-core, CB-IFACE/ENCAP→新增CB清理, WM-6-F8/P3-DEFER-03→order+demo+examples

| PR | 任务合并 | 工时 | 备注 |
|----|----------|------|------|
| RabbitMQ 代码清理 | P3-DEFER-01(backoff提取) + P3-DEFER-02(FailOpen enum) | 3h | **依赖 6A RabbitMQ PR** |
| Hook 增强 | WM17-F2-2(ctx超时) + WM17-F4-3(Prometheus metrics via HookObserver接口) | 3h | 需定义 kernel/cell HookObserver 接口 |
| CI 增强 | CI-01(integration路径) + T1-7(golangci-lint) | 2.5h | 同改 ci.yml |
| Session 安全 | P3-TD-10 Session refresh TOCTOU 乐观锁 | 4h | 高风险 |
| decode 加固 | DECODE-STR-01 classifyDecodeError 脆弱性 | 2h | `pkg/httputil/decode.go` |
| Journey 校验 | F-5 catalog 不校验引用 | 2h | `kernel/journey/catalog.go` |
| OTel 覆盖率 | OTEL-COV-01 testcontainers 集成测试 | 1h | `adapters/otel/` |
| **TestPubSub 真实 adapter 认证** | **TPUB-01**: conformance harness 替换 sleep 为显式 ready/setup-error 握手 + 接入 RabbitMQ adapter 验证 | 4h | `kernel/outbox/outboxtest/` + `adapters/rabbitmq/` **(P1, PR#93 复核)** |
| cursor 回归矩阵 | **CURSOR-TEST-01**: 5 个分页入口 invalid-cursor 测试不齐 + **CUR-HDL-01**(4 个分页 handler 补 cursor 回归: 垃圾cursor/旧格式缺scope/跨context replay，断言400+ERR_CURSOR_INVALID)。补齐 malformed/missing-scope/same-scope-different-context 三类 | 4h | `cells/*/handler_test.go` + `service_test.go` **(P2, PR#95 复核; CUR-HDL-01 从 B8 合入)** |
| cursor 可观测 | CURSOR-P2-02 cursor invalid 结构化日志 | 1h | `cells/audit-core/` |
| order+demo+examples 修复 | P4-TD-04(outboxWriter enforce) + P4-TD-12(t.Skip) + **EVT-HDR-RESTORE**(outbox接入后恢复 event headers contract_test 验证) + **WM-6-F8**(demo 全局模式开关, C3) + **P3-DEFER-03**(examples 新 API 示例，前置 ER-ARCH-02 ✅) | 7h | `cells/order-cell/` + `cells/demo/` + `cells/device-cell/` + `examples/` **(WM-6-F8/P3-DEFER-03 从 B8 合入)** |
| ~~contract 命名修正~~ | ~~CONTRACT-NAME-01~~ ✅ PR#101: `http.auth.me.v1` → 7 个 `http.auth.user.{op}.v1` | ✅ done | — |
| **config-core 修正** | **CFG-JSON-01**: `domain.ConfigEntry` 缺 json tags，config GET 响应用 PascalCase 违反 camelCase 规范（同理 `domain.FeatureFlag`）+ **FLAG-RACE-01**(FlagRepository 并发测试补 writerErrors 断言) + **P3-TD-12**(config rollback version 校验) | 3.5h | `cells/config-core/internal/domain/config_entry.go` + `feature_flag.go` + `config_repo_test.go` **(P2; FLAG-RACE/P3-TD-12 从 B8 合入)** |
| ~~flags request schema 拆分~~ | ~~FLAGS-SCHEMA-01~~ ✅ PR#101: 拆为 `http.config.flags.list/get/evaluate.v1` | ✅ done | — |
| ~~contract 操作级拆分~~ | ~~CONTRACT-SPLIT-01~~ ✅ PR#101: 5 个多操作 contract → 18 个 per-operation contract + `required` + `additionalProperties: false` | ✅ done | — |
| ~~schema-driven contract-test helper~~ | ~~CONTRACT-TEST-01~~ ✅ PR#101: `pkg/contracttest/` + 16 个 contract_test.go 全部重写为 schema 验证 | ✅ done | — |
| **DELETE 无 body 语义闭环** | **DELETE-NOCONTENT-01**: delete contract request.schema.json 补 description + additionalProperties:false；contract_test 改为 handler 语义测试（断言 204 + body 长度 0 + 无 JSON envelope）。中期 contract 模型补 method/path/successStatus/noContent 一等元数据（→ CONTRACT-META-01） | 1.5h | `contracts/http/auth/user/delete/v1/` + `cells/access-core/slices/identitymanage/contract_test.go` **(P2, discovered via PR#101 二轮复核)** |
| bootstrap tracing 集成测试 | **BOOT-TEST-01**: bootstrap 业务路由 tracing 集成断言 + router panic→Recovery→Tracing error span 联通测试 | 2h | `runtime/bootstrap/bootstrap_test.go` + `router/router_test.go` **(P2, PR#96 复核)** |
| bootstrap 次要清理 | **BOOT-MINOR-01**: `router.New` panic(err.Error())→panic(err) 保留 error 链 + access_log 输出 real_ip 字段 | 1h | `runtime/http/router/router.go` + `middleware/access_log.go` **(P2, PR#96 复核)** |
| **README + seed 用户** | **AUTH-DX-01**: README.md 仍指导匿名创建用户/访问 config/audit，但 auth 已拦截全部业务路由。in-memory 无预置用户，dev 模式 first-user bootstrap 走不通。修: 更新 README walkthrough + 预置 demo 用户(或受控 seed 入口) | 2h | `README.md` + `cells/access-core/internal/mem/` **(P1, discovered via PR#111 review)** |
| CB 接口+封装清理 | **CB-IFACE-01**(CircuitBreakerPolicy.Allow() 拆为 Allow()+Report(success)，去 gobreaker TwoStep 耦合) + **CB-ENCAP-01**(Config/State 本地类型映射，消除 adapter 用户 import gobreaker) | 3h | `runtime/resilience/circuitbreaker/` **(PR#104 review 跟进; 从 B8 合入)** |
| **HTTP operation model 收口** | **CONTRACT-OP-01(P1)**: config-read slice 声明 1 个 contract(`http.config.get.v1`)但注册 2 个路由(GET list + GET single)；config-write/config-publish/session-logout 有真实 HTTP 路由但 slice 元数据只声明 event contract，缺 HTTP serve contract。response.schema.json 用 oneOf 混合单资源/列表。影响版本治理和 contract discoverability。与 CONTRACT-META-01(v1.1) 关联但可先修 slice 元数据 | 4h | `cells/config-core/slices/*/slice.yaml` + `contracts/http/config/` + `cells/access-core/slices/sessionlogout/slice.yaml` **(P1, discovered via PR#104 集成态复核)** |
| **contract test 假阳性** | **CONTRACT-TEST-02(P1)**: `contracttest` helper 只验证"手写 JSON 过 schema"，不验证真实 handler/outbox 输出，已出现测试绿但语义漂移。三类症状: (1) user delete contract_test 只加载不校验 204/no-body (2) access-core outbox_test 只断言 EventType 不验证 event_id (3) device-register contract 样例写 `registered` 但真实实现返回 `online`。根因解释了 EVT-HDR-RESTORE/DELETE-NOCONTENT-01 为何测试体系仍显示"覆盖完成" | 5h | `pkg/contracttest/` + `cells/*/contract_test.go` + `cells/device-cell/slices/deviceregister/` **(P1, discovered via PR#104 集成态复核)** |
| ~~session event_id 闭环~~ | ~~EVT-SESSION-01~~ ✅ PR#101: sessionlogin contract_test 验证 payload + headers event_id + MustReject | ✅ done | — |

---

## Batch 6C: P1 功能补全（~5d，2 轨道）

> 前置: Batch 6A P3-DEFER-04 合入（SOL-B-01 需要新 Receipt 接口）。

| 轨道 | PR | 任务 | 工时 | 前置 |
|------|-----|------|------|------|
| Auth | WM-2-F1 | KeyProvider 接口抽象 | 1d | WM-34 ✅ |
| Auth | WM-35 | BFF handler 接入 cookie session | 2d | WM-2-F1 |
| Auth | WM-36 | SecureCookie key rotation 双 key ring | 1.5d | WM-2-F1，与 WM-35 串行(cookie_session.go) |
| Kernel | SOL-B-01 | Claimer lease 续租 Receipt.Renew | 4h | 6A P3-DEFER-04 |

> Auth 轨道关键路径: WM-2-F1 → WM-35 → WM-36（串行，共 4.5d）
> Kernel 轨道与 Auth 轨道并行

---

## Batch 7: Review + 发布准备（~16h，5 路并行 + tag 最后）

> 前置: Batch 6A+6B+6C 全部合入（review 对象是最终代码）。

| PR | 任务 | 工时 | 并行 |
|----|------|------|------|
| Review cells/ | T1-3 审查 6 cell (5,811 行) | 4h | ✅ |
| Review examples/ | T1-6 审查 3 项目 (233 行) | 2h | ✅ |
| Review 报告 | T1-8 汇总 findings | 2h | 依赖 T1-3+T1-6 |
| 发布文档 | R-1(GOPRIVATE) + R-3(CONTRIBUTING) + R-5(迁移指南) + R-6(错误码) | 4h | ✅ |
| 性能基准 | R-4 benchmark 测试 | 4h | ✅ |
| **v1.0 tag** | R-2 git tag + CI 验收 | — | **全部完成后最后执行** |

---

## Batch 8: P2 偿债（v1.0 后，~44.5h，14 组全并行）

> 前置: v1.0 tag 发布后。不阻塞发布。
> 12 项已前置合入 6A/6B：L4-PURE/RETRY→6A·L4-API, DISCARD-PUB→6A·outbox清理, OPS-5→6A·Watcher状态面+连接池, Config增强→6A·Watcher核心+Watcher状态面, CUR-HDL→6B·cursor回归, FLAG-RACE/P3-TD-12→6B·config-core修正, CB-IFACE/ENCAP→6B·CB清理, WM-6-F8/P3-DEFER-03→6B·order+demo+examples

| PR 组 | 任务 | 工时 |
|-------|------|------|
| Cursor DX | WM-6-F6(泛型 cursor helper，减少 cells/ 重复分页代码) + F7(cursor 日志收口，统一 invalid-cursor 结构化日志) + F1(prod guard，cursor decode 失败默认空结果而非 500) + TX-NIL-01(nil-safe 注释，repo 层 nil tx 语义文档化) | 3.5h |
| metadata parser | META-67-01(strict unknown-field reject) + META-67-02(位置信息错误报告) + META-67-03(cross-file 引用校验) | 2.5h |
| auth 增强 | WM-2-F2(HMAC replay 防护: 请求签名加 nonce+timestamp 窗口，防止截获重放) + WM-2-F3(auth metrics: 登录成功/失败/token刷新/过期计数，接入 Prometheus) | 4h |
| access-core 重构 | P3-TD-11: domain 模型拆分——User/Session/Role 从单文件拆为独立 aggregate，消除 service 层对 domain 的循环感知 | 4h |
| 集成测试补全 | P4-TD-05(outbox 全链路: produce→relay→consume→idempotent-ack) + RL-INT-01(Relay PG 集成: testcontainers 验证 poll+relay+cleanup) + P2-T-02(audit e2e: HTTP→auditlog→query 验证完整审计链路) | 6h |
| 迁移+订阅 | RL-MIG-01(online-safe 索引: outbox_entries 大表加索引需 `CONCURRENTLY`，当前 migration 缺此保证) + RL-SUB-01(入站 ID 校验: subscriber 收到的 entry_id 格式校验，防止注入) | 3h |
| CMD 重构 | CMD-MODE-01(validate/check 子命令 fail-fast: 首个 fatal 即退出，当前静默累积) + CMD-REFACTOR-01(app 包提取: cmd/ 下 bootstrap 逻辑抽到 `internal/app/`，支持测试注入) | 3.5h |
| 批量操作 | WM-7: 泛型 `BulkResult[T]` helper，聚合批量操作的 success/fail/skip 计数 + per-item error，供 batch API handler 统一响应格式 | 1d |
| Entity→DTO | P4-TD-13: 8 个 handler 响应从 entity 直出改为 DTO 映射，隔离持久化模型与 API 契约（涉及 user/session/config/flag/audit/order/device/demo） | 4h |
| ~~OBS 信任边界~~ | ~~**OBS-TRUST-01**~~ ✅ PR#108: `isSafeID` + consumer 侧 `isSafeObservabilityID` 双重验证 | ✅ done |
| ~~OBS 命名空间~~ | ~~**OBS-NS-01**~~ ✅ PR#108: unexported `reservedMetadataKeys` + `IsReservedMetadataKey()` 公共 API | ✅ done |
| ~~OBS 运行时开关~~ | ~~**OBS-KILLSW-01**~~ ✅ PR#108: `bootstrap.WithDisableObservabilityRestore()` (移除 subscriber 双重 restore) | ✅ done |
| Metadata map 大小限制 | **META-SIZE-01**: `Entry.Metadata` 无 key 数/总大小上限，write path 全量 JSON 序列化入库。bridge 只加 3 key 但业务层可传入任意大 map。需在 Writer 接口层或 postgres adapter 加 guard (PR#108 review F2-03) | 1h |
| kernel 测试 table-driven | **OBS-TABLE-01**: `observability_metadata_test.go` 用独立 `Test*` 函数而非 table-driven subtests（CLAUDE.md 要求 kernel/ 层 table-driven）(PR#108 review F3-04) | 1h |
| bridge 指标可观测 | **OBS-METRIC-01**: MergeObservabilityMetadata/ContextWithObservabilityMetadata 无 counter/histogram。生产环境无法确认 bridge 是否工作、多少 entry 缺少 trace context (PR#108 review F4-01) | 1.5h |
| DX: OBS 代码清理 | **OBS-DX-01**: `cloneMetadata` 未导出但通用；`logAttrsWithContext`/`logWithContext` 是 1:1 wrapper；`contextValueGetter/Setter` 缺 godoc；access_log 用 `[]any` 而非 `slog.LogAttrs` (PR#108 review F5-01~04, 可一起处理) | 2h |
| collision guidance 文档 | **OBS-DOC-01**: `IsReservedMetadataKey` 缺 usage example，开发者不知何时该调用。补 package doc 示例 (PR#108 review F6-02) | 0.5h |

---

## v1.1 — 核心能力完善

### metadata-model-v3 校验规则

| # | 缺失规则 | 优先级 |
|---|---------|--------|
| G-1 | FMT-11: 动态状态字段禁入非 status-board 文件 | HIGH |
| G-2 | TOPO-07: actor.maxConsistencyLevel 约束 | MEDIUM |
| G-4 | deprecated contract 引用阻断 | MEDIUM |
| G-6 | Assembly boundary.yaml 存在性校验 | LOW |

### 未实现的 Kernel 子模块

| 子模块 | 说明 | 优先级 |
|--------|------|--------|
| kernel/wrapper | 契约级可观测 traced wrapper | P1 |
| kernel/command | 命令队列接口（L4 框架支持） | P1 |
| kernel/webhook | receiver + dispatcher | P2 |
| kernel/reconcile | 最终状态收敛 | P2 |
| runtime/scheduler | cron/定时任务 | P2 |
| kernel/replay | projection rebuild | P3 |
| kernel/rollback | rollback metadata | P3 |

### adapters/ 与 runtime/ 分层重整

| # | 问题 | 方向 |
|---|------|------|
| AL-01 | outbox_relay.go 轮询逻辑属于 runtime | 拆出 `runtime/outbox/relay.go` |
| AL-02 | distlock.go 续期 goroutine 属于 runtime | 拆出通用 distlock 接口 |
| AL-04 | runtime/auth 直接 import golang-jwt | 评估是否值得拆 |

### 跨框架 GAP — v1.1 待评估

| GAP | 能力 | 预估 | 前置条件 |
|-----|------|------|---------|
| GAP-7 | Scheduler/cron | 1d spike | WM-17 ✅ |
| GAP-11 | Architecture dependency graph | 1d | archtest ✅ |
| GAP-13 | Auto API docs / OpenAPI | 2d | HR-02 ✅ |
| GAP-6 | Singleflight + cache helper | 1d | — |
| GAP-5 | Adaptive load shedding | 1.5d | WM-33b + RL-WIRE-01 |

### contract 模型增强

| # | 需求 | 优先级 |
|---|------|--------|
| CONTRACT-META-01 | contract.yaml 补 method/path/pathParams/queryParams/successStatus/noContent 一等元数据。当前 contract 只描述 body schema，transport 语义（HTTP 方法、状态码、无 body）靠隐含约定。ref: goa operation model / Kratos method binding / go-zero route DSL | P1 |

### 架构风险

| ID | 问题 | 状态 |
|----|------|------|
| Cell 接口 | 12 方法，考虑拆分 Cell + CellLifecycle + CellMetadata | 暂缓 |
| adapter 测试 | 15 个 t.Skip 集成测试待补全 | TODO |
| ER-ARCH-01 | Router startup heuristic 500ms，C4 架构级 | v1.1 |

### winmdm Defer v1.1

| # | 需求 | 票数 |
|---|------|------|
| WM-18 | 延迟消息原语 | 3/6 |
| WM-32 | mTLS 中间件 | 4/6 |
| WM-4 | Webhook 出站 adapter | 4/6 |
| WM-5 | OData $filter | 2/6 |
| WM-22 | Visibility Query API | 1/6 |
| WM-23 | 单体→微服务 | 2/6 |
| WM-16 | 投影按需重算 | 1/6 |

---

## v2+ — 长期

| # | 需求 | 票数 |
|---|------|------|
| WM-28 | 服务发现 Registry | 0/6 |
| WM-29 | Saga 补偿 | 0/6 |
| GAP-1 | gRPC 双协议 | 0/6 |
| GAP-2 | 服务发现 | 0/6 |
| GAP-8 | CQRS 组件 | 0/6 |
| GAP-12 | Saga 补偿 | 0/6 |
| GAP-14 | 本地 Dashboard | 0/6 |

---

## winmdm Reject（9 项）

| # | 需求 | 票数 |
|---|------|------|
| WM-3 | X.509 证书管理 | 1/6 |
| WM-14 | Codec 注册表 | 1/6 |
| WM-21 | Mixin 共享逻辑 | 2/6 |
| WM-24 | Policy Engine | 1/6 |
| WM-25 | 短期证书 | 1/6 |
| WM-26 | FanOut/FanIn | 0/6 |
| WM-30 | 编译期 Contract 验证 | 2/6 |
| WM-31 | 跨协议元数据同步 | 0/6 |
| WM-34b | Kratos 两层中间件 | 2/6 |

---

## 执行总览

| Batch | PR 数 | 工时 | 并行度 | 前置 | 里程碑 |
|-------|-------|------|--------|------|--------|
| 5A | 7 → ✅ 全部完成 | ~~39h~~ → ✅ done | — | — | CURSOR ✅ PR#94/95, CFG ✅ PR#97, CONTRACT ✅ PR#98+101, HTTP-SEC ✅ PR#96, BATCH3 ✅ PR#96+99, OBS-WIRE ✅ PR#96 |
| 5B | 9 → 剩 1 | ~5d | 2 轨道 | 5A | CFG-CONTRACT ✅ PR#101，韧性 ✅ PR#104，RL-METRICS ✅ PR#102，ER-ARCH-02 ✅ PR#105，CONTRACT-CLOSURE ✅ PR#106，CID-01 ✅ PR#108，AUTH-WIRE-01 ✅ PR#111，剩 TRACE-PROP-01 |
| 6A | 7+1 | ~~25h~~ → ~38h | 8/8 | 5B | 运维拆3PR(Health+Bootstrap+Watcher状态面) + Watcher核心(B8 Config增强合入) + RMQ + outbox(+DISCARD-PUB) + L4(+PURE/RETRY)，runtime竞态 ✅ PR#107 |
| 6B | 18 → 剩 11 | ~~25h~~ → ~36.5h | 11/11 | 6A(RMQ) | 6 项 ✅ PR#101, +B8合入(CUR-HDL/FLAG-RACE/TD-12/CB/WM-6-F8/DEFER-03) |
| 6C | 4 | ~5d | 2 轨道 | 6A(Receipt) | P1 功能补全 (BFF+SecureCookie) |
| 7 | 6 | ~16h | 5+tag | 6全完 | **v1.0 RC → v1.0** |
| 8 | 20 → 剩 14 | ~~63.5h~~ → ~44.5h | 14/14 | v1.0 | P2 偿债，12 项前置合入 6A/6B(含 Config增强→6A)，OBS-TRUST/NS/KILLSW ✅ PR#108，+5 新增(META-SIZE/OBS-TABLE/OBS-METRIC/OBS-DX/OBS-DOC from PR#108 review) |

```
Week 1:  Batch 5A ✅ 全部完成 (PR#94-101)
Week 2:  Batch 5B 大部分完成 — 韧性 ✅ PR#104, 可观测 ✅ PR#102, 事件路由 ✅ PR#105,
         contract闭环 ✅ PR#106, runtime竞态 ✅ PR#107
         剩余: CID-01 ✅ PR#108 + AUTH-WIRE-01(P0) + TRACE-PROP-01
Week 3:  Batch 5B 收尾 + Batch 6A (~38h, +B8 Config增强合入) + 6B (~36.5h, +B8合入) + 6C Auth轨道启动
Week 4:  Batch 6C 收尾 + Batch 7 (review+发布) → v1.0 tag
Post:    Batch 8 (P2偿债, 按需排期, 14 项 ~44.5h)
```
