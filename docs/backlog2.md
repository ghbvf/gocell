# GoCell Backlog2 — 四份归档审查的新增问题清单

> 日期：2026-04-29
> 基线：`origin/develop @ 4e2e00ad`（PR #331 已合）
> 输入：`bak/` 下 4 份团队归档审查
> - `20260426-develop-cross-layer-six-role-review/`（13 文件，跨层视角）
> - `20260426-layered-six-role-review/`（9 文件，分层视角 + master-summary）
> - `20260427-module-dataflow-six-role-review/`（12 文件，按 L2/L3 + 业务模块 + adapter 集成）
> - `20260427-per-relation-six-role-review/`（13 文件，逐对关系视角）
>
> 处理流程：4 个 reviewer 子 agent 并行通读 → 提取 finding → 对照 `develop @ 4e2e00ad` Grep/Read 验证 → 与 `docs/backlog.md` 交叉去重 → 合并视角重复项。
>
> **本文件只记录现有 `docs/backlog.md` 未覆盖的"新"开放问题**。已修复 / 已在 backlog 的项见 §10 索引段。
>
> 编号约定：`B2-{域}-NN`。{域} = K(kernel) / R(runtime) / C(cells) / A(adapters) / W(websocket) / X(cmd) / T(contracts)。

---

## §0 总览

| 项 | 数量 |
|---|---:|
| 4 份归档原始 finding（去重前） | ~84 |
| 已修复（最近 PR 关闭） | ~18 |
| 已在 `docs/backlog.md` | ~10 |
| 视角重复合并 | ~6 |
| **本文件登记新增项** | **70** |
| 其中 P0 | **6** |
| 其中 P1 | **40** |
| 其中 P2 | **24** |

**最高风险域（应优先评审）**：audit hash-chain 重启断链 / WebSocket 无认证授权 / RMQ 永久错误无限重连 / PG outbox claim 无 fencing / Redis Cluster 缺失 / setup admin 常驻 Public。

---

## §1 P0 高危项（6 条，发布前必做硬约束）

| # | 标题 | 文件:行 | 描述 | Cx | 来源 |
|---|---|---|---|---|---|
| **B2-A-01** | **POSTGRES-OUTBOX-CLAIM-FENCING-MISSING** | `adapters/postgres/outbox_store.go:81-206` | `markPublishedQuery` 仅对比 `status=claiming`，无 lease version / fencing token；旧 worker 的 in-flight publish 完成时可覆盖新 worker 的 claim 结果，破坏幂等保证。**修复**：claim 表加 `lease_id`/`claimed_at` + WHERE 条件 CAS，或迁 advisory lock。 | Cx3 | MD-01 |
| **B2-C-01** | **AUDITCORE-HASHCHAIN-RESTART-RECOVERY-MISSING** | `cells/auditcore/internal/domain/hashchain.go:31` + `cells/auditcore/cell.go` | `NewHashChain` 启动从空链开始，`initSlices` 未从 repo 恢复尾节点；多实例或重启后尾哈希不连续，链完整性无法验证；合规审计致命。**修复**：cell 启动时从 repo `SELECT last hash` 注入；考虑 leader 单写或全局 advisory lock。 | Cx4 | MD-02 |
| **B2-W-01** | **WEBSOCKET-UPGRADE-NO-AUTH-FAIL-OPEN** | `adapters/websocket/handler.go:51-65` | `UpgradeHandler` 升级后无 token / credential 校验，任意客户端可建立 WS 连接 → hub。**修复**：升级前必填 `auth.AuthMiddleware` 等价校验；构造期注入 Authenticator 接口；空 Authenticator fail-fast。 | Cx4 | MD-03 |
| **B2-W-02** | **WEBSOCKET-BROADCAST-NO-ACL** | `runtime/websocket/hub.go:405-420` | `Broadcast` 无 filter 参数；所有连接均可收到广播；多租户场景下信息越界。**修复**：Broadcast 接受 `func(conn) bool` filter 或 topic-scoped Send；hub 维护 `principal -> conn[]` 映射。 | Cx4 | MD-04 |
| **B2-A-02** | **RMQ-RECONNECT-PERMANENT-ERROR-NO-TERMINAL** | `adapters/rabbitmq/connection.go:436-670` | `reconnectLoop` 注释承认"keep trying"，认证失败 / Vhost not found 等永久错误也无限重试，readiness 持续挂起；凭证撤销后 pod 永不退出。**修复**：分类 amqp.Error code（403/404/530 等永久），permanent 时关 conn → readyz 503 → 容器重启拉新凭证。 | Cx4 | MD-05 |
| **B2-A-03** | **REDIS-CLUSTER-MODE-MISSING** | `adapters/redis/client.go:30-45` | `Config.Mode` 只支持 `standalone` / `sentinel`，无法接入 AWS ElastiCache Cluster / Azure Cache Cluster；生产部署受限。**修复**：新增 `ModeCluster`，使用 `redis.NewClusterClient`；DistLock / Idempotency 算法需重新评估 cluster slot 影响（hashtag）。 | Cx4 | MD-37 |
| **B2-C-02** | **SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT** | `cells/accesscore/cell_routes.go:73` + `slices/setup/handler.go:46-58` + `contracts/http/auth/setup/admin/v1/contract.yaml:5` | setup 端点常驻 `Public: true`；contract 标 `lifecycle: active`；410 Gone 仅在 admin 已存在时返回，未初始化窗口仍可被匿名首管抢注；多视角共识根因。**修复**：移到 `/internal/v1/setup/`（service-token only）+ contract `lifecycle: bootstrap`，或一次性 bootstrap token（env 注入消费）。`A26-R2` 限速只是缓解。 | Cx3 | CL-06 / LY-06 / PR-01 / PR-02 / PR-07 |

---

## §2 kernel / 治理 / 错误语义（7 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-K-01** | WRAPPER-ERROR-REDACTOR-DEFAULT-IDENTITY | P1 | `kernel/wrapper/consumer.go:43` | 默认 redactor = identity，error 文本原文进入 span / metric label，可能含敏感字段；显式注释承认设计取舍。**修复**：默认改最小脱敏（裁剪长度 + 关键字 mask）或构造期强制 caller 显式选择。 | Cx2 |
| **B2-K-02** | KERNEL-ERROR-FIRST-PANIC-RESIDUAL | P1 | `kernel/wrapper/handler.go:56` + `kernel/cell/auth_plan.go:107` + `pkg/contracttest/` | `MustNewAuthJWT` 等 Must 系列与 error-first 构造器混用；composition root 残留 panic。**修复**：审计所有 Must*，把生产路径改 error-first；保留 Must 仅作为 test-only / cmd 顶层 wrapper。 | Cx3 |
| **B2-K-03** | ASSEMBLY-REF-INTERFACE-IMPLICIT-CAST | P1 | `kernel/cell/auth_plan.go:175-181` + `runtime/bootstrap/auth_plan_apply.go:175` | `asmCellLookup` 通过 `asm.(assemblyWithCell)` 类型断言获取 Cell；kernel 与 runtime 间隐式契约，新 assembly 实现易漏配 method 静默失败。**修复**：把 `assemblyWithCell` 上提为 kernel 显式接口（`kernel/cell.AssemblyRef.Cell(id) Cell`）。 | Cx3 |
| **B2-K-04** | GOVERNANCE-CH-04-ERRCODE-MIRROR-DRIFT | P2 | `kernel/governance/rules_http_response_alignment.go:91,193` | `errcodeNameToStatus` 是手工镜像 `pkg/errcode/status.go` 的状态码映射；errcode 加新 code 时若漏改治理表，CH-04 静默通过。**修复**：从 `pkg/errcode` 单源导出 / 用 reflect 自动构建映射。 | Cx2 |
| **B2-K-05** | METADATA-PARSER-ERROR-PATH-LEAK | P2 | `kernel/metadata/parser.go:190,202` | parse error 公共消息直接含 fs 内部路径；低强度信息泄露 + 路径暴露 CI runner 结构。**修复**：error 双通道：public 仅含 cell/slice ID + 字段路径，internal slog 保留 fs path。 | Cx2 |
| **B2-K-06** | EVENTROUTER-CONSUMERGROUP-CELLID-CONFUSION | P2 | `runtime/eventrouter/router.go:364` | `Subscription.CellID = h.consumerGroup`；语义混淆，cell 切片与消费组名通过同一字段表达，下游 metrics label / 日志属性自相矛盾。**修复**：Subscription struct 显式拆分 `CellID` 与 `ConsumerGroup` 两字段。 | Cx3 |
| **B2-K-07** | CONTRACTTEST-UNDECLARED-REF-NO-OP | P1 | `pkg/contracttest/contracttest.go:170,189` | 测试调 `MustValidateRequest("not-declared-key", ...)` 时静默 `return`；key 写错时测试假通过。**修复**：未声明 key 改 `t.Fatalf`；保留显式 opt-in opt-out 时也只允许 explicit allowlist。 | Cx1 |

---

## §3 runtime / bootstrap / health / observability（9 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-R-01** | HEALTH-LISTENER-FALLBACK-NO-STRICT-FAIL-FAST | P2 | `runtime/bootstrap/bootstrap_phases.go:583-596` | HealthListener 缺失时静默回退 PrimaryListener；探针/业务入口隔离弱化；strict 模式 fail-fast 缺失。**修复**：bootstrap 加 `WithStrictHealthListener()`，缺失时 fail；至少 slog.Warn 升 Error 并暴露 audit attribute。 | Cx2 |
| **B2-R-02** | CELLS-READYZ-MISSING-REPO-PROBE | P1 | `cells/configcore/cell.go:204` + `cells/auditcore/cell.go:191` | configcore / auditcore 的 `HealthCheckers()` 仅接 outbox emitter；底层 repo（PG / 内存）健康未纳入；DB 故障下 readyz 仍 OK。**修复**：cell 注入 repo `Pingable` 接口，HealthCheckers 聚合 emitter + repo + downstream 三档。 | Cx2 |
| **B2-R-03** | BOOTSTRAP-ROLLBACK-ERROR-NOT-PROPAGATED | P1 | `runtime/bootstrap/run_state.go:113,121` | rollback 步骤失败仅 `slog.Warn` 后 `return cause`（原启动错误），rollback 错误未并入返回值；调用方无从感知"启动失败 + 资源未完全清理"双重故障。**修复**：用 `errors.Join(cause, rollbackErr)` 返回；或返回结构化 error 含两个 chain。 | Cx1 |
| **B2-R-04** | ERRCODE-4XX-CLASSIFY-DUAL-SOURCE-DRIFT | P1 | `pkg/errcode/classify.go:175` + `pkg/httputil/response.go:351` | `expected4xxCodes` 白名单与 `WriteDomainError` 状态码映射分散维护；新 errcode 加入时易两处不同步。**修复**：单源映射（status code → errcode 或反向），classify 改为查询同一 map。 | Cx2 |
| **B2-R-05** | OTEL-METRIC-PROVIDER-CTX-BACKGROUND | P1 | `adapters/otel/metric_provider.go:174,178,185` | metric record 固定使用 `context.Background()`；trace-metric exemplar 关联在 adapter 层断裂；分布式 trace 关联 metric 失败。**修复**：metric Record 接受 caller ctx，构造期注入 propagator。 | Cx4（评估范围） |
| **B2-R-06** | OTEL-TRACER-PROVIDER-NOT-GLOBAL | P1 | `adapters/otel/tracer.go:56,73` | `NewTracer` 只创建局部 TracerProvider，未注册到 `otel.SetTracerProvider`；三方依赖 `otel.GetTracerProvider()` 的 instrumentation 静默落到 noop。**修复**：composition root 调 `otel.SetTracerProvider`；或文档化"必须注入根"约束 + archtest 锁定。 | Cx2 |
| **B2-R-07** | OTEL-TRACER-SHUTDOWN-NO-DEADLINE | P1 | `adapters/otel/tracer.go:63,65` | shutdown 完全依赖外部 ctx；调用方传 `context.Background()` 时停机 flush 不可控；Pod 终止时可能挂起到 SIGKILL。**修复**：shutdown 内部派生 timeout（默认 5s），与外部 ctx Min。 | Cx1 |
| **B2-R-08** | OTEL-OBSERVABLE-CALLBACK-MANUAL-UNREGISTER | P1 | `adapters/otel/pool_collector.go:43,110` | `RegisterCallback` 返回 unregister fn 需调用方手工管理；未释放则 callback 持续触发；MeterProvider 重建时残留。**修复**：构造期 `WithLifecycle` 接 cell.Lifecycle，OnStop 自动 unregister；或返回 `io.Closer` 强制接入 cleanup。 | Cx3 |
| **B2-R-09** | OTEL-ATTR-CACHE-KEY-COLLISION-UNBOUNDED | P1 | `adapters/otel/metric_provider.go:84,96,101` | attrCache key 用字符串拼接（`"key1=val1,key2=val2"`），未做转义，且无上界；高基数 label / 注入字符均可造成 key 碰撞或内存膨胀。**修复**：key 用 sha256 hex / FNV 或 sorted (k,v) tuple；加 LRU max size + drop。 | Cx3 |

---

## §4 cells / 业务收尾（12 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-C-03** | CELL-INIT-INFRA-TYPE-LEAK | P1 | `cells/configcore/cell_init.go:34` + `cells/configcore/cell.go:157` | Cell.Init 仍直接构建 slice + 接收 PG pool 等 adapter 类型；Cell 边界泄漏基础设施类型；composition root 与 Cell 装配混杂。**修复**：cell 暴露 ports（接口），slice 装配下沉到 corebundle composition root；cell.Init 只接受 ports.* 接口。 | Cx3 |
| **B2-C-04** | AUDITAPPEND-L2-FAILURE-INJECTION-TEST-MISSING | P1 | `cells/auditcore/slices/auditappend/outbox_test.go:50` | 仅有 stub 级测试，无 PG-level 失败注入（DB 写成功 + outbox 失败 / DB 失败 + outbox 已写）；L2 原子失败语义未实证。**修复**：用 testcontainers PG + faulty hook（`pgmock` / sqlhooks），三个失败矩阵覆盖 + assert outbox 状态/事件未发。 | Cx2 |
| **B2-C-05** | AUDITAPPEND-ACTOR-MISSING-FALLBACK-SYSTEM | P1 | `cells/auditcore/slices/auditappend/service.go:133` | actor 缺失时降级为 `system` 仍 Ack；可追责性削弱；恶意攻击者借此清空 actor 字段。**修复**：缺 actor 改 `DispositionReject + PermanentError`，事件入 DLX；或 actor schema validation 在 contract 层 required（已 strict）+ 服务层 fail-closed。 | Cx2 |
| **B2-C-06** | SESSIONLOGOUT-CONSUMER-NO-ACTION-VALIDATE | P1 | `cells/accesscore/slices/sessionlogout/consumer.go:69` | role-change consumer 只校验 `userId` 非空，不校验 `action` 是否为合法枚举（`assigned`/`revoked`）；任意/未知 action 均触发 `RevokeByUserID`，可被滥用扩大会话回收范围。**修复**：白名单 action 枚举，未知值 PermanentError → DLX。 | Cx2 |
| **B2-C-07** | CONFIGCLIENT-STATUS-DISPOSITION-MISMAP | P1 | `cells/accesscore/internal/adapters/http/configclient.go:91` + `cells/accesscore/slices/configreceive/service.go` | configclient HTTP 请求 401/403 等不可恢复错误仍走 Requeue 重试，产生持续 noise。**修复**：状态码分级 → Disposition 映射（401/403/410 → Reject Permanent；404 已特判；429/5xx → Requeue）。 | Cx2 |
| **B2-C-08** | CONFIGCORE-EVENT-DECODER-STRICT-UNKNOWN-FIELDS | P1 | `cells/configcore/internal/events/config_events.go:82` | event decode 用 `json.NewDecoder + DisallowUnknownFields`；与 contract v1 演进策略（响应可加字段）冲突；上游 producer 加 optional 字段后，下游 consumer 全量拒收。**修复**：unknown field 默认接受（lenient），strict 改为可选 dev-only flag；或与 PR-CI-3 V1-RESPONSE-EVOLVE 协调统一。 | Cx2 |
| **B2-C-09** | AUDITQUERY-RAW-PAYLOAD-EXPOSURE | P1 | `cells/auditcore/slices/auditquery/handler.go:35,42` | `payload` 直接回传原始事件内容；可能含敏感字段（PII / 密码 / token）；audit query 鉴权后仍泄漏。**修复**：handler 层加 redaction policy（按 contract.yaml `sensitive` 字段标记或全局 deny-list）；查询响应字段可配。 | Cx2 |
| **B2-C-10** | AUDITAPPEND-GLOBAL-MUTEX-13-TOPIC-SERIAL | P1 | `cells/auditcore/slices/auditappend/service.go:93,165` | `s.mu.Lock()` 在 `HandleEvent` 入口；13 个 topic consumer 全局串行化；吞吐受限，单慢消息阻塞所有。**修复**：锁粒度降到 hash-chain 链头；或改用 single-writer goroutine + chan 模式（hash-chain 顺序仍单线程，但其他工作并行）。 | Cx3 |
| **B2-C-11** | CONFIGSUBSCRIBE-TOMBSTONE-UNBOUNDED | P2 | `cells/configcore/slices/configsubscribe/service.go:29,169` | tombstone 常驻内存无 TTL / 容量上限；长期高 churn 键空间持续增长；OOM 风险。代码注释已承认 out-of-scope。**修复**：加 TTL（默认 24h）+ max entries（默认 10k） + LRU 淘汰。 | Cx2 |
| **B2-C-12** | AUDIT-HMAC-KEY-MIN-LENGTH-MISSING | P2 | `cells/auditcore/cell.go:319` | HMAC key 仅校验非空，未做最小长度 / 强度门禁；32 字节以下 key 抗碰撞强度不足。**修复**：构造期校验 `len(key) >= 32`；HKDF 派生时 input 长度校验。 | Cx1 |
| **B2-C-13** | L2-CROSS-LAYER-E2E-REGRESSION-MISSING | P2 | `cells/accesscore/slices/setup/service_test.go` + `tests/integration/` | L2 跨层端到端回归不足：事务写入 + outbox insert + relay publish 联动场景仅 stub 验证；真实 PG + relay + broker 链路无全量覆盖。**修复**：与 `TEST-JOURNEY-ROOT-HARNESS-01` 协调，建 `tests/integration/l2_atomicity/` harness，覆盖 setup/identity/role-assign 三主链。 | Cx3 |
| **B2-C-14** | HASHCHAIN-CROSS-RESTART-CONTINUITY-TEST-MISSING | P2 | `cells/auditcore/slices/auditappend/service_test.go:110` | 缺跨重启 / 跨实例 hash-chain 连续性回归；本质同 B2-C-01，但额外补 test。**修复**：B2-C-01 实现后追加 testcontainers PG + restart 注入测试。 | Cx2 |

---

## §5 adapters 加固（21 条，按子模块）

### 5.1 PostgreSQL

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-A-04** | PG-OBSERVABILITY-INJECT-AFTER-VALIDATE | P1 | `adapters/postgres/outbox_writer.go:54` | observability collector 注入在 `Validate` 之后；validate 阶段未持有 collector，错误路径 metric 漏记；语义不一致。**修复**：collector 设为构造期必填（error-first）；或在 Validate 内显式说明仅校验非 obs 字段。 | Cx1 |
| **B2-A-05** | PG-RELAY-FAIL-WRITE-UNHANDLED-ROWS | P1 | `runtime/outbox/relay.go:534,551` | 失败回写后未检查 `RowsAffected==0`（updated=false）；retry/dead-letter 计数虚高。**修复**：分支 `updated bool`，false 时 log + emit `relay_publish_lost` metric，不再重复退避。 | Cx2 |
| **B2-A-06** | PG-RECLAIM-STALE-NO-LIMIT-LONG-TX | P1 | `adapters/postgres/outbox_store.go:192,206` | `reclaimStaleQuery` 全量 UPDATE 无 LIMIT；积压时单事务行数大、长事务持有 lock；vacuum/replication 受影响。**修复**：加 `LIMIT $batchSize`（默认 1000）+ 循环直到无更多；或 chunked CTE。 | Cx3 |
| **B2-A-07** | PG-OUTBOX-METADATA-NO-BYTE-LIMIT | P1 | `adapters/postgres/outbox_writer.go:61,163` | metadata 列写入无字节上限；恶意 / bug producer 可写 MB 级 JSON；relay 内存压力放大 + replication 延迟。**修复**：构造期 `MaxMetadataBytes`（默认 64KB）；超长 reject + permanent error。 | Cx2 |
| **B2-A-08** | PG-REFRESH-STORE-AMBIENT-AND-STANDALONE-TX-MIXED | P1 | `adapters/postgres/refresh_store.go:141,190,227` | 同一接口混合 ambient tx（依赖 `RunInTx`）和独立提交路径；事务契约对调用方不可见；潜在双写风险。**修复**：拆两接口（`RotateInTx` / `RotateStandalone`），或文档化所有方法均要求 ambient tx，archtest 锁定。 | Cx3 |
| **B2-A-09** | PG-REFRESH-REJECT-TIMING-SIDECHANNEL | P1 | `adapters/postgres/refresh_store.go:221,295,330` | 不同拒绝路径耗时 / 日志特征不一致；可被时序攻击区分"token 不存在"vs"token 已用过"。**修复**：所有拒绝路径走同一 fixed-time 比较 + 同一 slog 字段；增加 timing test。 | Cx3 |
| **B2-A-10** | PG-READYZ-NO-SCHEMA-COMPATIBILITY | P1 | `adapters/postgres/pool_resource.go:69` | `Checkers()` 调用 `pool.Health`（Ping）；schema_guard 检测到 invalid index 时未并入 readyz；migration 不兼容时 readyz 仍 OK。**修复**：`Checkers()` 聚合 Ping + schema_guard 结果；schema 不一致返回 503。 | Cx3 |
| **B2-A-11** | PG-CONSTRUCTOR-ERROR-MODEL-MIXED | P1 | `adapters/postgres/refresh_store.go:114` 等 | postgres 包对外暴露混杂 error / panic / nil deref / `MustNew` / 多 DB handle 并存；调用方判断成本高。**修复**：审计 New*/MustNew*；New* 全 error-first；MustNew 仅作 cmd 顶层 wrapper；archtest 锁定。 | Cx3 |
| **B2-A-12** | PG-SCHEMA-GUARD-QUALIFIED-NAME-DRIFT | P2 | `adapters/postgres/schema_guard.go:105,125,141` | `DetectInvalidIndexes` 注释承诺返回 qualified name（schema.table.idx），SQL 实际只返回裸 relname；多 schema 部署时同名误判。**修复**：SQL JOIN `pg_namespace` 拼 qualified name；或注释改"unqualified, single-schema only"。 | Cx1 |
| **B2-A-13** | PG-POOL-TX-ROLLBACK-LOG-LEAKS-DRIVER-ERROR | P2 | `adapters/postgres/pool.go:87,113` | 基础设施日志透出原始 driver error；脱敏边界与 errcode 公共消息不一致；可能泄露 query / param 片段。**修复**：rollback 日志只记 wrapped errcode + slog `internal_error` attribute；driver error 仅放 debug 级。 | Cx2 |

### 5.2 RabbitMQ

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-A-14** | RMQ-STOP-INTAKE-PREFETCH-NOT-DRAINED | P1 | `adapters/rabbitmq/subscriber.go:914` | StopIntake 与 runCtx cancel 耦合；prefetch 缓冲消息可能未排空就 cancel；丢消息或重投。**修复**：StopIntake 实现"先停拉新 + 排空 prefetch + 等 inflight ack" 三段；超时 fail。 | Cx3 |
| **B2-A-15** | RMQ-CHANNEL-COUNT-NO-UPPER-BOUND | P1 | `adapters/rabbitmq/connection.go:171` | 每个 publisher / subscriber 创建 channel 无上限；可触发 broker `channel_max`；连接死锁。**修复**：connection 加 `MaxChannelsPerConn`（默认 256）+ pool 复用；超限 fail-fast。 | Cx3 |
| **B2-A-16** | RMQ-PUBLISH-NACK-CONFIRM-TIMEOUT-NO-WARN | P1 | `adapters/rabbitmq/publisher.go:133,136,143` | NACK / confirm timeout 错误路径无 slog.Warn；静默丢失，下游基于"成功发布"假设导致状态分叉。**修复**：所有错误返回路径统一 slog.Warn + emit `publish_failed` metric；与 outbox relay 协调失败语义。 | Cx1 |
| **B2-A-17** | RMQ-CONFORMANCE-EVENTBUS-SEMANTIC-TEST-MISSING | P1 | `adapters/rabbitmq/conformance_test.go:18` | 缺 EventBus 真实语义集成测试（PermanentError → Reject DLX / Receipt commit / Disposition 三分支）。**修复**：testcontainers RMQ + 三 fixture：handler 返回 Ack / Requeue / Reject Permanent，断言 broker 端结果（DLX 入队 / 重新投递 / consumer ack 计数）。 | Cx3 |
| **B2-A-18** | ADAPTER-CONNECT-TIMEOUT-INCONSISTENT | P1 | `adapters/rabbitmq/connection.go:273` + `adapters/postgres/pool.go:69` | RMQ DefaultDial 直接调 `amqp.Dial` 无内置超时；PG pool 仅 ctx.Ping，无 adapter 级连接预算。**修复**：每个 adapter 暴露 `WithConnectTimeout(d)`，缺省 5s；构造期 fail-fast。 | Cx2 |

### 5.3 OTel / Prometheus / Redis / S3

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-A-19** | OTEL-SPAN-SETATTR-INSECURE-PLAINTEXT | P1 | `adapters/otel/span.go:43,51` | `SetAttributes` 对 string 直接透传；`Insecure=true` 时明文出站 collector；敏感字段（token / PII）泄露到 OTel backend。**修复**：构造期校验 `Insecure=false` 或显式 `WithUnsafePlaintext()`；attribute 值长度上限 + redaction hook。 | Cx2 |
| **B2-A-20** | OTEL-SIMPLE-TRACER-PROPAGATION-ASYMMETRY | P2 | `runtime/observability/tracing/tracer.go:77` | `simpleTracer` 与 OTel Tracer 共享接口但 propagator 语义不对称；从 simpleTracer 切到 OTel 时跨进程链接静默丢失（traceparent 不注入）。**修复**：简化 simpleTracer 仅 noop 或要求 caller 显式 propagator；archtest 锁定不可在生产路径用 simpleTracer。 | Cx2 |
| **B2-A-21** | OTEL-MESSAGING-COLLECTOR-FORMAT-PCT-LITERAL | P2 | `adapters/otel/messaging_channel_collector.go:65` | error 文案把 `%d` 当字面量输出（缺 `fmt.Errorf` / `Sprintf` 包装）；线上日志可读性差。**修复**：改 `fmt.Errorf("...: %d", n)`。 | Cx1 |
| **B2-A-22** | PROMETHEUS-METRICS-HANDLER-NO-TIMEOUT | P1 | `cmd/corebundle/metrics.go:83` | `promhttp.HandlerOpts{}` 零值无 Timeout；Registry.Gather 大表（如 hookobserver per-cell label）可阻塞 HTTP；scraping 慢拉爆 worker。**修复**：`HandlerOpts{Timeout: 5*time.Second}`；与 Prom scrape interval 协调。 | Cx1 |
| **B2-A-23** | PROMETHEUS-HOOKOBSERVER-CELLID-LABEL-NO-VALIDATE | P1 | `adapters/prometheus/hook_observer.go:114-117` | CellID 标签值无格式校验；恶意 / 错误 cell.yaml 可注入 `\n` / 空格污染指标输出；Prom parser 报错或 dashboard 错位。**修复**：构造期校验 `cellID` 匹配 `^[a-z][a-z0-9-]*$`，违例 fail-fast。 | Cx1 |
| **B2-A-24** | PROMETHEUS-RACE-TEST-MISSING | P1 | `adapters/prometheus/metric_provider_test.go` | CounterVec / HistogramVec 并发 Lock/Unlock 未加 `-race` 测试；潜在 goroutine 安全问题。**修复**：添加 `t.Parallel()` + 多 goroutine 并发 record fixture，CI 跑 `-race`。 | Cx2 |
| **B2-A-25** | PROMETHEUS-LOOKUP-VEC-LABELS-DUPLICATE | P2 | `adapters/prometheus/metric_provider.go:201-227` | `lookupCounterVecLabels` / `lookupHistogramVecLabels` 99% 重复；维护成本与漂移风险。**修复**：泛型抽 `lookupVecLabels[T metric.Vec](...)` 共用，或下沉到 helper。 | Cx2 |
| **B2-A-26** | REDIS-RECEIPT-COMMIT-RELEASE-RACE | P1 | `adapters/redis/idempotency.go:136-200` | Commit/Release 并发情况下幂等保证可破坏（Commit 完成同时另线程 Release 当前 receipt）。**修复**：Commit/Release 内部用 `Lua` 脚本原子 CAS（current_owner==caller && state==committing）。 | Cx3 |
| **B2-A-27** | REDIS-MULTI-TENANT-KEY-COLLISION | P1 | `adapters/redis/idempotency.go:127-130` | 不同 cell 间 Idempotency / Nonce / Cache key 共享前缀；多租户场景碰撞或越界访问。**修复**：构造期注入 `KeyNamespace`（cell ID），所有 key 自动 prefix；archtest 守护 cell 级隔离。 | Cx3 |
| **B2-A-28** | REDIS-PASSWORD-OPTIONAL-FAIL-OPEN | P1 | `adapters/redis/client.go:62-68` | `Password` 可选；未设时连接无认证 Redis 也通过；生产配置漏 password 时 fail-open。**修复**：real mode 下 `Password` 必填（与 TLS 一致 fail-closed）；dev 显式 `WithUnsafeNoPassword()`。 | Cx2 |
| **B2-A-29** | REDIS-RACE-TEST-MISSING | P1 | `adapters/redis/distlock_test.go` | DistLock / Cache / Nonce / Idempotency 并发竞争未在 `-race` 下覆盖。**修复**：补 testcontainers Redis + `t.Parallel()` 多 goroutine fixture。 | Cx3 |
| **B2-A-30** | REDIS-DISTLOCK-RENEW-TTL-PRECISION-LOSS | P2 | `adapters/redis/distlock.go:50-56` | TTL Seconds 转换截断；锁过期可能比预期早若干毫秒。**修复**：用 `PEXPIRE` (ms 精度) 替代 `EXPIRE`；或 ttl < 1s 时 fail-fast。 | Cx2 |
| **B2-A-31** | REDIS-SENTINEL-TLS-INCOMPLETE | P2 | `adapters/redis/client.go:200-215` | Sentinel 协议下 TLS 配置未完整透传；连接 Sentinel 无 TLS。**修复**：`SentinelOptions.TLSConfig` 同步注入。 | Cx2 |
| **B2-A-32** | S3-INTEGRATION-TEST-MISSING | P1 | `adapters/s3/s3_test.go:11` | 仅有 Config.Validate 单测，无 MinIO testcontainers 集成测试；put/get/list 真实路径未覆盖。**修复**：testcontainers MinIO + 完整 RW + 错误注入（403/超时/分片）。 | Cx2 |

---

## §6 WebSocket（独立模块，6 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-W-01** | (P0) UPGRADE-NO-AUTH | — | — | 见 §1 B2-W-01 | — |
| **B2-W-02** | (P0) BROADCAST-NO-ACL | — | — | 见 §1 B2-W-02 | — |
| **B2-W-03** | WS-OBSERVABILITY-MISSING | P1 | `runtime/websocket/hub.go` | 无 Prometheus 指标 / 连接生命周期追踪；连接数 / 消息率 / 错误 / 慢 client 全黑盒。**修复**：注入 metrics provider，暴露 `ws_connections{cell}` `ws_messages_total{direction,outcome}` `ws_send_duration_seconds`。 | Cx2 |
| **B2-W-04** | WS-MESSAGE-BUFFER-UNBOUNDED | P1 | `runtime/websocket/hub.go:510` | 慢连接导致 Broadcast 发送端 channel 缓冲无上限；OOM 风险。**修复**：每连接 sendCh 加 cap（默认 64），满则 drop oldest 或断连接 + slog.Warn。 | Cx2 |
| **B2-W-05** | WS-STOP-SYNC-CLOSE-ALL-CONNS | P1 | `runtime/websocket/hub.go:280-293` | Stop 时逐连接同步 Close；千连接场景下 Pod terminationGracePeriod 容易超时。**修复**：广播 close frame 后 fan-out goroutine 池 + 总 deadline；超时强制 conn.Close(）。 | Cx2 |
| **B2-W-06** | WS-DOC-MISSING | P1 | `docs/guides/`（缺失） | 无入门文档 / 协议规范 / 常见陷阱（origin / 心跳 / 重连）；外部接入者全靠读源码。**修复**：`docs/guides/websocket-integration.md`，含 origin 配置 / 心跳协议 / 重连退避 / 故障注入。 | Cx2 |

---

## §7 cmd / 装配 / 启动（5 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-X-01** | OUTBOX-E2E-FIXED-SLEEP | P2 | `cmd/corebundle/outbox_e2e_integration_test.go:169` | 50ms 固定 sleep 等待订阅注册；CI 慢节点 flake；本质等待 ready signal 缺失。**修复**：用 `eventbus.Ready()` 或 `EventRouter.WaitSubscribed(topics)` 同步等待。 | Cx1 |
| **B2-X-02** | SHARED-DEPS-AGGREGATE-TOO-WIDE | P2 | `cmd/corebundle/shared_deps.go:32` | 单结构体含 Topology/JWTDeps/PromStack/EventBus/PGPool/Redis/Claimer/InternalGuard/Addrs 等 ~20 字段；composition root 维护成本高 + 字段间依赖隐式。**修复**：拆 `coreDeps` / `eventDeps` / `httpDeps` / `metricsDeps` 4 组 sub-struct，与 PR-A66 BootstrapDecompose 风格一致。 | Cx3 |
| **B2-X-03** | PG-INVALID-INDEX-WARN-CONTINUE | P2 | `cmd/corebundle/bundle.go:308-313` | invalid index 仅 `slog.Warn` 后继续启动；migration 异常或半完成状态被掩盖；与 readyz 不联动。**修复**：与 B2-A-10 协调，invalid index → readyz 503 → strict mode fail-fast；或加 `WithPGStrictSchema()` 选项。 | Cx2 |
| **B2-X-04** | HEALTH-LISTENER-DEFAULT-LOOPBACK | P1 | `cmd/corebundle/shared_deps.go:461` | 默认 `127.0.0.1:9091`；real-mode 部署若不显式 PodIP / Service 绑定，K8s probe 不可达；现仅注释提醒。**修复**：real mode 缺显式 bind 时 fail-fast；或默认监听 `0.0.0.0:9091` + 治理 archtest 锁定 internal/healthz only via mTLS。 | Cx2 |
| **B2-X-05** | CMD-GENERATE-INDEXES-EXPOSED-NOT-IMPL | P1 | `cmd/gocell/app/generate.go:34` | `generate indexes` 子命令在 help 输出，实现返回 `not implemented`；用户调用得不到能力但又不报"未知命令"。**修复**：从 help 移除（返回 unknown subcommand）；或标注 `[experimental, not yet implemented]` 显示在 help。 | Cx1 |

---

## §8 contracts 漂移与演进（8 条）

| # | 标题 | 严重度 | 文件:行 | 描述 | Cx |
|---|---|---|---|---|---|
| **B2-T-01** | CONFIG-ROLLBACK-OPTIMISTIC-LOCK-MISSING | P1 | `contracts/http/config/rollback/v1/contract.yaml` + `cells/configcore/internal/ports/config_repo.go:23-25` (TODO 自承) | rollback 无 `expectedCurrentVersion` / If-Match；并发回滚同一 entry 可双写；contract 也无 409 响应声明；代码 `TODO(505-followup)` 已自承。**修复**：(1) request schema 加 `expectedCurrentVersion` required；(2) `UpdateForRollback` SQL 加 `WHERE version=$expected`；(3) contract responses 加 409 `ERR_CONFIG_VERSION_MISMATCH`。 | Cx2 |
| **B2-T-02** | RBACASSIGN-EVENT-CONTRACT-WAIVER-EXPIRY | P1 | `cells/accesscore/slices/rbacassign/contract_test.go:84,93` | `S8-FOLLOWUP (VERIFY-01 waiver expiry 2026-07-01)`；contract test 仅 schema marshal，无真实 outbox publish 路径覆盖；waiver 到期前必须闭合。**修复**：用 `outboxtest.Publisher` mock + assert 真实 emit；移除 waiver 标记。 | Cx2 |
| **B2-T-03** | V1-RESPONSE-SCHEMA-CLOSED-VS-EVOLUTION | P1 | `contracts/http/config/list/v1/response.schema.json:7` 等 30 文件 | response schema `additionalProperties: false` 与 "v1 只增不删" 冲突；新加可选字段时 client 拒收。**注**：与 batch2 plan PR-CI-3 V1-RESPONSE-EVOLVE 同主题，理应在 PR-CI-3 落地。本条仅作 backlog2 索引。 | Cx2 |
| **B2-T-04** | CONTRACT-NAMING-USERID-DRIFT | P2 | `contracts/event/user/created/v1/payload.schema.json:6` 等 | path param `{userID}`、payload `userId` 大小写驼峰风格混用；多处不一致。**修复**：统一 camelCase（与 CLAUDE.md JSON/Query/Path 规则一致）；扫全部 contracts 一次性改齐 + archtest 锁定。 | Cx2 |
| **B2-T-05** | INTERNAL-CONTRACT-EXTERNAL-ACTOR-BEARER-DRIFT | P1 | `contracts/http/auth/role/{assign,revoke}/v1/contract.yaml` + boundary.yaml | internal contract 混入 external actor / bearer 语义（与真实 service-token 链路不一致）；与 backlog `T9 HTTP-INTERNAL-CLIENTS-DECLARED-ADV-01` 互补。**修复**：清理 actor / authentication.kind；与 PR-CFG-I X2 boundary 对齐合并处理（PR-CFG-I 未启动 plan）。 | Cx2 |
| **B2-T-06** | CONTRACT-CLIENTS-NOT-COMPILED-INTO-RUNTIME | P1 | `contracts/http/config/internal/get/v1/contract.yaml:8` + `cells/configcore/slices/configread/handler.go:79` | `clients: [accesscore]` 仅元数据；handler 只查 `AnyRole(RoleInternalAdmin)`，任何持合法 service token 调用方都通过。backlog `PR266-CONTRACT-CLIENTS-ENFORCE` 已记录但触发条件项；本条侧重 internal 路由现实漂移。**修复**：runtime middleware 读 contract.clients → 强制 caller cell-id（X-Calling-Cell header / mTLS SAN）匹配；audit 失败请求。 | Cx3 |
| **B2-T-07** | INTERNAL-SERVICE-TOKEN-CALLER-IDENTITY-COLLAPSE | P1 | `runtime/auth/principal.go:108` + `runtime/auth/authenticator.go:160` | `BuiltinServiceRoles(ServiceNameInternal)` 返回固定 `[RoleInternalAdmin]`；所有 service token 调用方共享同一 principal；最小权限缺失。backlog `PR-CFG-I-DRAIN X1` 只解决 NonceStore fail-closed，未拆分 caller identity。**修复**：service token claims 加 `caller_cell` / `caller_service`，principal 注入对应 scope 角色；configread internal endpoint 校验 `caller_cell ∈ contract.clients`。 | Cx4 |
| **B2-T-08** | CONFIG-PUBLISH-CONTRACT-FAILURE-CODES-INCOMPLETE | P2 | `contracts/http/config/publish/v1/contract.yaml` | publish 失败语义（404 / 409）未完整声明在 contract.responses；client SDK 不知道处理这些路径。**修复**：补 404 ERR_CONFIG_NOT_FOUND / 409 ERR_CONFIG_VERSION_MISMATCH 到 contract.yaml。 | Cx1 |

### A5 Follow-ups (PR-V1-SVCTOKEN-CALLER-IDENTITY 衍生)

| # | Item | 复杂度 | 触发条件 / 修复方案 |
|---|------|--------|---------------------|
| **B2-T-07-FU-1** | RBACASSIGN-ACCESSCORE-CALLER-PRODUCTION-WIRING | Cx2 | A5 PR 把 rbacassign internal route 挂载并声明 `Clients: [accesscore]`（contract.yaml + spec literal 一致，FMT-18 PASS）。但生产代码中 accesscore 没有 caller-side configclient 风格的 RPC 路径调用 rbacassign — accesscore 在 spec/yaml 是 declared caller，运行时是 unwired caller。这非冲突（不挂没人调用 = 等价于 default-deny），但发布前需要核实预期生产调用是否真为 accesscore。**触发条件**：v1.0 GA 准备阶段。**修复方案**：(a) 若 accesscore setup 流程确实需要调用 rbacassign 完成初始 admin role 分配，按 configclient 模式加 caller-side RPC + 集成测试覆盖；(b) 若 accesscore 不是真实 caller，改 clients 为真实 caller（如 actors.yaml 注册 `external-admin-tool`）并迁移 token 颁发到 issuer-side；(c) 若 rbacassign 无 v1.0 生产用例，删 RouteGroup + clients=[] + 加回 archtest awaitingRealCallerAllowlist 入口 |
| **B2-T-07-FU-2** | BUILTIN-SERVICE-ROLES-REMOVED-FOLLOWUP | Cx3 | A5 PR 删除 `BuiltinServiceRoles`/`RoleInternalAdmin`/`ServiceNameInternal` 三常量，service principal `Roles=nil`、`Subject=""`。**触发条件**：未来若需要 service principal 基于 caller 派生不同 scope（如同一 caller 在不同 endpoint 走不同权限），方案候选：(a) 恢复 `BuiltinServiceRoles(callerCell)` 按 cell 派生 `role:internal:<callerCell>`；(b) 走 `auth.Route.Policy` 路径写自定义 Policy。当前 caller_cell + contract.clients 模型已足；不预先实现 |
| **B2-T-07-FU-3** | K04-CELLGEN-CONTRACTSPEC-CLIENTS-FIELD | Cx2 | A5 PR 给 `wrapper.ContractSpec` 加 `Clients []string` 字段。K#04 cellgen framework (PR#360) 当前模板不生成此字段，未来用 `gocell generate cell` 创建的 internal endpoint cell 可能漏写 Clients 导致 `ContractSpec.Validate()` fail。**触发条件**：K#06 contractgen PR 启动时一并处理；或 K#04 模板下次更新时同步。**修复方案**：cellgen `tools/codegen/cellgen` 模板对 `routeMounts` 中 path 以 `/internal/v1/` 开头的 entry 自动生成 `Clients: []string{...}`（来源 cell.yaml 元数据扩展或注释 marker） |
| **B2-T-07-FU-4** | SVCTOKEN-CALLER-CELL-SHARED-SECRET-LIMITATION | Cx4 | **设计限制**：A5 token 4-part 格式中 callerCell 由 caller 自行声明并用共享 `GOCELL_SERVICE_SECRET` HMAC 签名。HMAC 防止 token 在 cell 边界外被伪造或篡改，**但不防同一 ring 持有者之间的相互冒充**：accesscore 进程持有 ring → 可 mint `callerCell="auditcore"` 的 token 调用 auditcore 的 internal endpoint。`contract.clients` allowlist 因此是 governance-only labeling，不是 anti-spoofing identity boundary（同一 trust domain / assembly 内 cell 间彼此互信前提下可接受）。**触发条件**：(a) 多租户 / 跨 trust-domain 部署场景出现；(b) 安全审计要求 caller identity 必须 credential-derived；(c) 出现内部恶意 cell 威胁模型。**修复方案**：(a) **per-caller HMAC ring** — 每 cell 独立 secret，issuer 仅签发自己的 token（最小改动）；(b) **issuer-issued JWT-SVID** — 中心化 issuer 签发 caller_cell claim 的 short-lived JWT，对标 SPIFFE JWT-SVID（中等改动）；(c) **mTLS** — 每 cell 独立 X.509 证书，AuthorizationPolicy 取 source.principals from peer cert（大改动，对标 Istio）。建议路径：(a) → (b) → (c) 渐进式落地；本 PR 不做。**ADR 参考**：SPIFFE JWT-SVID + Istio AuthorizationPolicy + Consul Service Intentions。|

---

## §9 工时与 PR 拆分建议

### 工时汇总（仅 backlog2 新增项）

| 严重度 | 数量 | 估算工时 | 工作日 |
|---|---:|---:|---:|
| P0 | 6 | ~5d (Cx3-Cx4 多个) | 5 |
| P1 | 40 | ~80h | 10 |
| P2 | 24 | ~30h | 4 |
| **合计** | **70** | **~150h** | **~19 工作日** |

### 推荐 PR 打包（与现有 plan 不冲突，新批次）

```
Wave A — P0 评审先行（先做 ADR / 决策，不直接改）
  PR-B2-A1  WEBSOCKET-AUTH-ARCH-ADR (B2-W-01 + B2-W-02 设计)
  PR-B2-A2  RMQ-RECONNECT-TERMINAL-DESIGN (B2-A-02)
  PR-B2-A3  PG-OUTBOX-CLAIM-FENCING-DESIGN (B2-A-01)
  PR-B2-A4  AUDIT-HASHCHAIN-RECOVERY-DESIGN (B2-C-01)
  PR-B2-A5  REDIS-CLUSTER-SUPPORT-ADR (B2-A-03)
  PR-B2-A6  SETUP-PUBLIC-CLOSURE-ADR (B2-C-02)

Wave B — kernel/runtime 错误语义 + observability 收口（~30h，三路并行）
  PR-B2-B1  KERNEL-WRAPPER-ERRSEMANTIC-AND-CONTRACTTEST (B2-K-01/02/07, ~6h)
  PR-B2-B2  KERNEL-INTERFACE-LIFT-AND-GOVERNANCE (B2-K-03/04/05/06, ~10h)
  PR-B2-B3  OTEL-PROVIDER-LIFECYCLE-AND-GLOBAL (B2-R-05/06/07/08/09 + B2-A-19/20/21, ~14h)

Wave C — adapters 加固（~40h，按 adapter 串行）
  PR-B2-C1  PG-OUTBOX-AND-RELAY-HARDEN (B2-A-04/05/06/07/12, ~8h)
  PR-B2-C2  PG-REFRESH-AND-READYZ (B2-A-08/09/10/11/13, ~12h)
  PR-B2-C3  RMQ-LIFECYCLE-AND-CONFORMANCE (B2-A-14/15/16/17/18, ~10h)
  PR-B2-C4  PROMETHEUS-AND-S3-TEST-COVER (B2-A-22/23/24/25 + B2-A-32, ~6h)
  PR-B2-C5  REDIS-MULTITENANT-AND-AUTH (B2-A-26/27/28/29/30/31, ~16h)

Wave D — cells 业务收尾（~20h）
  PR-B2-D1  AUDIT-HARDEN (B2-C-04/05/09/10/12/14, ~10h)
  PR-B2-D2  CONFIG-CONSUMER-AND-EVENT-SCHEMA (B2-C-06/07/08/11, ~6h)
  PR-B2-D3  CELL-INIT-INFRA-LEAK-AND-L2-TEST (B2-C-03/13, ~14h, 含 ADR)
  PR-B2-D4  CONFIGCORE-READYZ-REPO-PROBE (B2-R-02, ~3h, 与 D1 并行)

Wave E — bootstrap/cmd/contracts 收口（~14h）
  PR-B2-E1  BOOTSTRAP-ROLLBACK-AND-HEALTH (B2-R-01/03 + B2-X-04, ~5h)
  PR-B2-E2  ERRCODE-CLASSIFY-SINGLE-SOURCE (B2-R-04, ~3h)
  PR-B2-E3  CMD-COMPOSITION-CLEANUP (B2-X-01/02/03/05, ~6h)
  PR-B2-E4  CONTRACT-DRIFT-FIX (B2-T-01/02/04/05/08, ~10h，与 PR-CI-3 协调)

Wave F — WebSocket 模块加固（B2-W-03~06，B2-W-01/02 设计落地后串行，~10h）
  PR-B2-F1  WEBSOCKET-OBSERVABILITY-AND-LIMITS (B2-W-03/04/05/06)

Wave G — 运行时授权深度（依赖 Wave A 决策结果，~16h）
  PR-B2-G1  CONTRACT-CLIENTS-RUNTIME-ENFORCE (B2-T-06)
  PR-B2-G2  SERVICE-TOKEN-CALLER-IDENTITY (B2-T-07)
```

### 与现有 plan 的协调

- **PR-CI-3 V1-RESPONSE-EVOLVE**（batch2-k8s-verify）：吸收 B2-T-03，本文件不重复
- **PR-CFG-I AUTH-FAIL-CLOSED-AND-OPS-RESIDUE**（l4 batch2.4）：B2-T-05 / B2-T-07 与其 X1 / X2 主题相关，建议合并到 PR-CFG-I 后续轮次
- **TEST-JOURNEY-ROOT-HARNESS-01**（backlog 主线）：吸收 B2-C-13 / B2-C-14
- **PR266-CONTRACT-CLIENTS-ENFORCE**（backlog P2）：升级为 B2-T-06 + B2-T-07 联合 PR

---

## §10 已修复 / 已在 backlog 的项（参考索引）

### 已修复（最近 PR 关闭，无需登记）

| 编号 | 关闭原因 |
|---|---|
| LY-02 | runner.go:215-222 已 fail-closed |
| LY-07 | auditcore slice.yaml 13 topic 已对齐 |
| LY-09 | audit query queryParams 已声明全字段 |
| LY-12 | listener.go authChain 必填 fail-fast |
| LY-13 | health.go verbose fail-closed |
| LY-15 | EventRouter AddContractHandler error-first |
| LY-16 | worker.go ErrWorkerExitedEarly |
| LY-18 | redis/vault/s3 ValidateTLSEndpoint |
| LY-20 | websocket AllowedOrigins 空 panic |
| LY-21 | postgres refresh_store error-first |
| LY-26 | securecookie 负向测试已覆盖 |
| LY-28 | dispatch.go 改 ContinueOnError + Usage 提示 |
| LY-30 | main_test.go 限定可接受错误 |
| LY-31 | controlplane.go SERVICE_SECRET 全模式必填 |
| LY-33 | actors.yaml + REF-17 已对齐 |
| LY-35 | login/config request 已加 minLength/maxLength/maximum |
| MD-09 | relay.go nil Metrics NoopRelayCollector |
| PR-03 | bootstrap_phases.go validateInternalGuardForDeclaredRoutes |
| PR-12 | health.go verboseDecision fail-closed |
| PR-20 | audit list contract from/to 已声明 |
| PR-24 | controlplane.go SERVICE_SECRET fail-fast |
| CL-11 | access_module.go ForceBootstrap |
| CL-14 | controlplane.go fail-fast |

### 已在 docs/backlog.md（不重复登记）

| 编号 | backlog 条目 |
|---|---|
| LY-04 | `PR252-F2 COMMAND-SWEEPER-PRODUCTION-GOVERNANCE-01` |
| LY-05 | `PR-CFG-G1-FU6` + `V-A11` |
| LY-17 | `PR237-T1 DUAL-LISTENER-TEST-GAPS-01` |
| LY-32 | `T6 GOCELL-PER-CELL-ADAPTER-01` |
| LY-36 | `TEST-JOURNEY-ROOT-HARNESS-01` + `PR-CFG-D-FU` |
| MD-06 | `PR-CFG-A-DEFER-2 CONFIGCORE-L2-MEMORY-MODE-DIVERGENCE-01` |
| MD-10 | `PR266-METADATA-ONLY-CONSUMER-BUSINESS` |
| MD-15 | `PR-CFG-I-DRAIN X1`（plan 未启动）|
| CL-06 / LY-06 | `A26-R2 SETUP-ADMIN-RATE-LIMIT-01`（仅缓解，不根治；本文升级为 B2-C-02）|
| CL-07 / CL-13 | `PR-CFG-I-DRAIN X1`（不含 caller identity；本文升级为 B2-T-07）|
| CL-12 | `PR245-F6/F10` 部分覆盖 |
| PR-10 | `INTERNAL-LISTENER-SERVICE-TOKEN-E2E-HARNESS-01`（部分覆盖）|

---

## §11 后续动作

1. **本文件作为 backlog2 索引**，不再扩展；条目修复时直接在表格行追加 `✅ #PR` 标记
2. **Wave A 6 个 ADR**优先派 architect agent 评审，确定方向后再派 developer 实施
3. **Wave B-G** 在 ADR 决策完成 + Wave A 主路径修复后启动；可与 `docs/plans/202604290500-backlog-residual-and-merge-roadmap.md` 的 Wave 并行（文件域基本不重叠）
4. backlog.md 中已被 backlog2 升级覆盖的条目（A26-R2 / PR-CFG-I X1 等），合并 backlog2 对应 PR 时同步关闭
