# Kernel 层第二组审查报告

**审查日期**：2026-05-04  
**审查范围**：kernel 层第二组 — 消息与数据基础设施  
**模块**：`kernel/outbox`、`kernel/persistence`、`kernel/idempotency`、`kernel/command`、`kernel/wrapper`

---

## Preflight

- repo: ghbvf/gocell
- reviewTargetType: manual-diff (kernel 层代码审查，分组2)
- pr: N/A
- base...head: 当前 HEAD (kernel/outbox, kernel/persistence, kernel/idempotency, kernel/command, kernel/wrapper)
- changedFiles: ~60 文件（含测试 + outboxtest/ + commandtest/）
- evidenceSource: local-workspace-code-read
- consistencyCheck: PASS（直接读取本地源码）

---

## 1. 审查范围与总体风险

### 范围

| 模块 | 主要责任 | 关键文件 |
|------|---------|---------|
| `kernel/outbox` | 事务性 Outbox 接口、ConsumerBase（幂等+重试+DLX）、Relay、failopen | outbox.go, consumer_base.go, emitter.go, relay_collector.go, failopen_tracker.go |
| `kernel/persistence` | TxRunner 接口（事务 context 传播） | tx.go, txctx.go |
| `kernel/idempotency` | Claimer 两阶段幂等接口（Claim/Commit/Release） | idempotency.go, inmem.go |
| `kernel/command` | L4 设备命令队列状态机（7 状态） | entry.go, queue.go, status.go, advance.go, sweeper.go |
| `kernel/wrapper` | Contract 绑定到可观测性原语（HTTP/Consumer span） | spec.go, consumer.go, carrier.go, tracer.go |

### 总体风险评估

**中风险**：第二组代码设计质量较高，分层方向正确（无向上依赖），状态机有完整约束，幂等设计两阶段远优于业界平均。主要问题集中在：
- **3 个 P1 级运维可见性缺失**（relay 积压无 Gauge、DLX 无 counter、sweeper 失败沉默）
- **2 个 P2 安全问题**（payload 无上限、命令缺授权钩子）
- **多处 P2 API 设计和可维护性问题**

---

## 2. 合并问题表

| ID | 严重级别 | 席位 | 文件:行号 | 问题 | 根因 | 修复方向 |
|----|---------|------|---------|------|------|---------|
| G2-01 | **P1** | 运维 | [relay_collector.go](../../kernel/outbox/relay_collector.go) | `RelayCollector` 无 pending queue depth Gauge，无法告警"消息积压超阈值" | `RecordBatchSize` 只记录批次大小 histogram，不记录当前 pending 条数 | 增加 `RecordPendingDepth(count int64)` Gauge，relay 在 `ClaimPending` 后调用 |
| G2-02 | **P1** | 运维 | [consumer_base.go](../../kernel/outbox/consumer_base.go) retry budget 耗尽路径 | retry budget 耗尽路由 DLX 时仅有 Warn 日志，无 Prometheus counter；违反 observability.md（影响正确性 → Error 级） | retryLoop reject 路径只调用 `logWithContext`，无 collector 注入 | 增加 `ConsumerCollector` 接口，在 reject 路径记录 `outbox_consumer_rejected_total{cell,topic,reason}` counter；日志级别改为 Error |
| G2-03 | **P1** | 运维 | [sweeper.go](../../kernel/command/sweeper.go) L143 | Sweeper `OnError` 为 nil 时 sweep 失败零告警、零日志；`runTick` 的 ScanActive/Queue.Ack 失败完全沉默 | `runTick` 错误分支调用 `s.OnError`（可 nil），无默认日志 | 在错误分支增加 `slog.Error` 兜底；增加 `command_sweep_errors_total` counter；文档要求 caller 必须设置 `OnError` |
| G2-04 | **P2** | 安全 | [outbox.go](../../kernel/outbox/outbox.go) L280 `Entry.Validate` | `Entry.Payload` 无上限校验，仅检查非空；超大 payload 可导致 DB 行过大、relay 加载 OOM、consumer handler 内存展开 | `Validate()` 只做 `len == 0`，无最大字节数约束 | 增加 `MaxPayloadSize` 常量（建议 512 KiB 可配）并在 `Validate()` 中校验；与 NATS JetStream 默认 1 MiB 上限对齐 |
| G2-05 | **P2** | 安全 | [command/queue.go](../../kernel/command/queue.go) L112 | `Cancel`/`Report`/`Ack` 无授权钩子；只有 `Enqueue` 有 `AuthzFunc`，终态写路径无 authz | 接口设计仅在入队处设防 | 在 `Queue` 接口增加 `CancelAuthz`/`AckAuthz`；或文档明确"终态写授权必须前置于适配层" |
| G2-06 | **P2** | 运维+安全 | [failopen_tracker.go](../../kernel/outbox/failopen_tracker.go) | fail-open 状态仅通过 `/readyz` 暴露，探针周期告警延迟（K8s 默认 10s）；drop 事件无 Prometheus counter | `RecordDrop()` 无指标侧路，只更新内部计数器 | 在 `RecordDrop()` 时 increment `outbox_failopen_drops_total{cell}` counter，与 tracker 解耦 |
| G2-07 | **P2** | 安全 | [idempotency/inmem.go](../../kernel/idempotency/inmem.go) L121 | `inMemReceipt` `Commit`/`Release` 共享同一 `sync.Once`；Release 先执行后 Commit 返回 nil（false-success） | `Commit`/`Release` 共享 `r.used sync.Once`，第二次调用静默返回 `r.err=nil` | 增加 `committed atomic.Bool` 标志区分"已 release"与"已 commit" |
| G2-08 | **P2** | 安全 | [envelope.go](../../kernel/outbox/envelope.go) L64 | `UnmarshalEnvelope` 中 `msg.ID` 只做非空检查，含 `\n`/`\r`/控制字符的 ID 可注入日志（CWE-117） | 缺少字符集/格式校验，`ObservabilityMetadata.Validate` 已用 `idutil.IsSafeID` 但 msg.ID 未对齐 | 复用 `idutil.IsSafeID(msg.ID)` 校验 |
| G2-09 | **P2** | 架构 | [outbox.go](../../kernel/outbox/outbox.go) L261 | `Writer.Write` 注释用 "SHOULD use context-embedded transaction"，弱契约；适配器不遵守时事务原子性静默失效（幽灵事件/事件丢失） | Go interface 无法强制 precondition；文档选择了 SHOULD | 改为 MUST；`Write` godoc 增加 invariant；在 `TxRunner.RunInTx` godoc 补充"fn 内 ctx 必须被所有参与者（outbox.Writer）使用" |
| G2-10 | **P2** | 架构 | [outbox.go](../../kernel/outbox/outbox.go) vs [command/metadata.go](../../kernel/command/metadata.go) | `MaxMetadataKeys/KeyLen/ValueLen/TotalSize` 和 `validateMetadata`/`truncate` 在两个包中各自独立定义，值完全相同，DRY 违规 | 历史平行开发时复制了常量和验证函数 | 提取到 `kernel/outbox` 或新建 `kernel/metautil`；command 包通过 import 使用，可组合添加 reserved-key 检查 |
| G2-11 | **P1** | 可维护性 | [outbox.go](../../kernel/outbox/outbox.go) L522 | `HandleResult.Receipt` 是 exported 字段但禁止 handler 读写；archtest 只守护写入，读取无保护；造成"禁止触碰的公开字段"认知负担 | "Subscriber 内部 hand-off" 复用了业务可见的公开 struct 字段 | 将 Receipt 改为 unexported 字段（或移入 internal 类型）；archtest 从"检测违规"升级为"编译期不可能" |
| G2-12 | **P2** | 可维护性 | [emitter.go](../../kernel/outbox/emitter.go) L27 | `DirectPublishFailureMode`（`iota+1`）与 `FailurePolicy`（`iota` 含 Default）是两个描述同一语义的类型，通过 `Resolve()` 桥接 | 分层开发复制了概念，未合并 | 合并为单一 `FailurePolicy`，构造时转换一次，消除桥接方法 |
| G2-13 | **P2** | 产品 | [outbox.go](../../kernel/outbox/outbox.go) L583 | 无 `HandleResult` 工厂函数（`Ack()`, `Requeue(err)`, `Reject(err)`），开发者需手写 struct literal；`PermanentError + DispositionRequeue` 不等于 DLX 路由的认知陷阱无类型保护 | 类型设计选择 struct literal，依赖 iota+1 零值防御 | 提供 `outbox.Ack()`, `outbox.Requeue(err)`, `outbox.Reject(err)` 工厂函数；`Reject()` 同时设置 Disposition + PermanentError，消除双字段协调负担 |
| G2-14 | **P2** | 产品 | [wrapper/spec.go](../../kernel/wrapper/spec.go) L28 | `Kind` 和 `Transport` 字段为裸 string，无枚举常量；合法值仅在 `Validate()` 错误消息和注释中，不可自动补全 | 以简洁为代价牺牲了 IDE 引导 | 增加 `const KindHTTP = "http"` / `KindEvent = "event"` / `TransportAMQP = "amqp"` 等常量 |
| G2-15 | **P2** | 测试 | [persistence/tx_test.go](../../kernel/persistence/tx_test.go) | `tx_test.go` 体为空（仅有 package 声明），`TxCtxKey` 事务传播核心零测试覆盖 | 文件从未填写，视为占位符遗留 | 补充 TxCtxKey round-trip 测试、TxRunner 契约描述 |
| G2-16 | **P2** | 测试 | [idempotency/inmem_test.go](../../kernel/idempotency/inmem_test.go) | 所有 Claim 测试均为串行；无 goroutine 并发场景（同一 key 两个 goroutine 同时 Claim）的 race 测试 | 只覆盖了"先后"语义，未覆盖"同时"语义 | 增加 `TestInMemClaimer_ConcurrentClaim_OnlyOneAcquires` + `-race` |
| G2-17 | **P2** | 测试 | [outbox/consumer_base_test.go](../../kernel/outbox/consumer_base_test.go) | 无测试验证 `HandleResult{}` 零值通过 ConsumerBase 后被降级为 Requeue（而非 Ack）；该行为是"侥幸正确"而非显式保证 | retryLoop 的零值降级行为未被 unit test 锁定 | 新增 `TestConsumerBase_Wrap_ZeroValueDisposition_TreatedAsRequeue` |
| G2-18 | **P2** | 运维 | [idempotency.go](../../kernel/idempotency/idempotency.go) | Redis 幂等 key TTL=24h，无容量规划文档；高吞吐场景 key 数量可能超出 Redis maxmemory，触发 eviction 导致幂等保护静默失效 | `DefaultTTL=24h` 硬编码，无注释估算 | 注释中增加容量估算公式；运维侧增加 redis keyspace 告警 |
| G2-19 | **P2** | 架构 | [command/sweeper.go](../../kernel/command/sweeper.go) | Sweeper 使用公开字段直接结构体（Public fields），nil 检查推迟到 `Start()` 运行时，与项目 fail-fast 构造器约定不一致（OUTBOX-SERVICE-01 守护范围外） | Sweeper 是 worker 结构，未应用 `NewSweeper() (*Sweeper, error)` 构造器模式 | 提供 `NewSweeper(scanner, queue, clk, ...)` 构造函数，构造期完成 nil 校验 |
| G2-20 | **P3** | 可维护性 | [command/advance.go](../../kernel/command/advance.go) | `AdvanceCommand`/`ResetForRetry` 标注"MUST NOT use"但 exported；IDE 自动补全直接推荐给 Cell 开发者 | 内部工具函数意外导出 | 移入 `kernel/command/internal/` 子包；或 unexport |

---

## 3. 根因分析

### 根因簇 A：运维可见性系统性缺失（G2-01、G2-02、G2-03、G2-06）

**症状**：
- relay 积压无 Gauge（G2-01）
- retry budget 耗尽无 counter，日志级别错误（G2-02）
- sweeper 失败沉默（G2-03）
- fail-open drop 无 Prometheus counter（G2-06）

**数据流**：

```
outbox relay path:
  DB: pending entries → ClaimPending → publish → update(relay_done)
  Metrics: RecordBatchSize(N) ← 只有流量速率，无当前 depth

consumer retry path:
  handler() → DispositionRequeue → retryLoop → budget 耗尽
  → Reject (broker Nack) → DLX
  Metrics: (nothing)  ← 只有 slog.Warn

sweeper path:
  ticker → runTick → ScanActive → Queue.Ack
  Error: s.OnError(err)  ← nil 时完全沉默
```

**根因**：outbox 层的 `RelayCollector` 和 `ConsumerCollector` 接口在设计时只关注了"流量速率"（batchSize、relayed count），而不是"状态深度"（pending depth）和"失败分类"（rejected count）。这是运维维度在接口设计期的遗漏，而非实现 bug。

**修复方向**：在 `RelayCollector` 增加 `RecordPendingDepth(count int64)`；新建 `ConsumerCollector` 接口（含 `RecordRejected(reason string)`）；sweeper 增加 slog.Error 兜底 + metrics counter。

---

### 根因簇 B：Payload 无边界约束（G2-04）

**症状**：`Entry.Payload []byte` 无 MaxPayloadSize 校验

**数据流**：
```
producer: entry.Validate() → pass (len > 0, no size check)
  → txRunner.RunInTx → writer.Write(ctx, entry)
  → PostgreSQL: INSERT INTO outbox(payload) ← 写入 ~1GB 行

relay: SELECT payload FROM outbox → full load to memory
  → MarshalEnvelope(entry) → broker publish ← message too large error?
  
consumer: unmarshal(entry.Payload) → handler(payload) ← OOM possible
```

**与 NATS JetStream 对比**：NATS 在发布侧（client.Publish）同步检查 `len(data) > MaxPayload（默认 1 MiB）` 并返回 `ErrMaxPayload`。GoCell 将错误发现延迟到 relay 或 consumer 端，从"同步发布侧 fail-fast"退化为"异步消费侧 OOM"。

---

### 根因簇 C：HandleResult 公开内部 hand-off（G2-11、G2-13、G2-17）

**症状**：
- `HandleResult.Receipt` exported 但禁用（G2-11）
- 无工厂函数 `Ack()/Requeue()/Reject()`（G2-13）
- 零值降级行为无 unit test 锁定（G2-17）

**根因**：`HandleResult` 的设计试图同时服务两个目的：(1) handler 开发者的返回值接口；(2) Subscriber 内部 hand-off 通道（含 Receipt）。将两者合并进同一 struct 导致了字段暴露悖论。与 Watermill 对比：Watermill 通过 `message.Message` 内部私有 ack/nack channel 完全封装，handler 只返回 `error`，Subscriber 自行决定 Ack/Nack。

---

### 根因簇 D：Transactional Outbox 弱契约（G2-09）

**症状**：`Writer.Write` 注释 SHOULD（非 MUST）参与调用方事务

**架构原因**：Go interface 无法在类型系统层面强制"此 context 必须包含事务"的 precondition。NATS JetStream（broker 负责原子性）和 Debezium（CDC 负责原子性）规避了此问题。GoCell 选择了"应用层事务+outbox"模型，因此这是架构设计的必然产物。但"SHOULD"而非"MUST"给了适配器实现者太多逃脱空间。

**修复**：改为 MUST；在 `TxRunner.RunInTx` godoc 强制要求"fn 内所有参与者必须使用传入的 ctx"。

---

### 根因簇 E：命令状态机遗漏中间执行态（轻微设计缺口）

**症状**：`StatusDelivered`（设备 ACK 收到）到 `StatusSucceeded`（执行完成）之间缺少"设备正在执行中"态

**对比**：Temporal `ActivityTaskStarted` 虽然延迟持久化，但 Temporal 的 Pending Activity Describe API 可返回"执行中"状态。GoCell 的 `StatusSent` 是"已发送到传输层"，不等于"设备正在执行"。对 IoT L4 场景，当前 7 状态已满足需求（`Delivered` 是 Temporal 没有的加分状态），但如果未来需要区分"设备确认开始执行"与"设备确认执行完成"，状态机需要扩展。

---

## 4. 开源项目对比表

### 主题 1：Consumer Retry + DLX + Disposition 设计

| 框架 | 检查来源 | Retry 机制 | DLX 路由 | Disposition 模型 | 幂等 | 可观测性 |
|------|---------|---------|---------|---------|------|---------|
| **Watermill** | [middleware/retry.go](https://github.com/ThreeDotsLabs/watermill/blob/master/message/router/middleware/retry.go), [middleware/poison.go](https://github.com/ThreeDotsLabs/watermill/blob/master/message/router/middleware/poison.go) | cenkalti/backoff，`MaxRetries`+`ShouldRetry`；退避可细粒度控制 | 应用层 re-Publish 到 poison topic（非 broker-native，可靠性低） | 隐式（error nil/non-nil），无显式 Ack/Reject 枚举 | `Deduplicator`（hash-based 内存，不支持分布式，无两阶段） | 无 retry counter，无 DLX counter，无 pending depth |
| **NATS JetStream** | [nats.go](https://github.com/nats-io/nats.go/blob/main/nats.go)，[JetStream 消费者文档](https://docs.nats.io/using-nats/developer/develop_jetstream/consumers) | `MaxDeliver` + `BackOff []time.Duration`（协议层退避配置） | broker-native DLQ（`$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.>`） | **5 值**：Ack/AckSync/Nak/NakWithDelay/Term/InProgress | 无内建（需应用层实现）| Server metrics + advisory events |
| **Temporal** | [retry-policies](https://docs.temporal.io/encyclopedia/retry-policies), [events](https://docs.temporal.io/references/events) | `RetryPolicy`（MaxAttempts+Interval，服务端持久化）| 无 DLX；`NonRetryable` 错误直接 terminal | 单字段 `NonRetryable bool`，与 Disposition 解耦，一次设置即路由停止重试 | 服务端 Activity Token（任务级别，不依赖外部存储）| Temporal Web UI + metrics server |
| **GoCell ConsumerBase** | [consumer_base.go](../../kernel/outbox/consumer_base.go) | `ExponentialDelay`（bits.Len64 + maxDelay + crypto/rand jitter），`RetryCount` 预算 | broker-native `Nack(requeue=false)` → DLX exchange | **3 值**：Ack/Requeue/Reject，显式 enum，零值安全降级为 Requeue | `kernel/idempotency.Claimer` 两阶段（Claim/Commit/Release，fail-closed）| 仅日志，无 reject counter/DLX counter |

**结论（≥3 项支撑）**：
1. broker-native DLX（GoCell/NATS）优于应用层 re-Publish（Watermill）——更少网络往返，publisher 宕机不丢 DLX 消息
2. 显式 Disposition 枚举（GoCell/NATS Term）优于隐式 error nil/non-nil（Watermill）——边界行为更可预测
3. 三个框架均未在 retry budget 耗尽时提供内置 Prometheus counter，属于普遍缺口，GoCell 应主动补充

---

### 主题 2：Transactional Outbox 事务原子性契约

| 框架/方案 | 事务保证方 | 机制 | 原子性强度 |
|---------|---------|------|---------|
| **Debezium CDC** | PostgreSQL WAL | Change Data Capture 监听 WAL，outbox 行即事件来源 | 硬性保证（数据库层）|
| **NATS JetStream** | Broker | PubAck 确认持久化；client 端 `PublishAsync` + `PublishAsyncComplete` 确认收到 | 软性（at-least-once + consumer ACK）|
| **MassTransit** | .NET `DbContext` + Outbox | `MassTransit.EntityFrameworkCore.OutboxMessage` 与业务实体同 `SaveChanges()` 事务 | 应用层事务（强）|
| **GoCell** | 应用代码 + `TxRunner` | `RunInTx` + `Writer.Write(ctx with tx)` | 应用层事务（强，前提：MUST 参与 tx）|

**结论**：GoCell 的应用层事务+outbox 模型与 MassTransit 同级，强度依赖适配器遵守契约。将 "SHOULD" 改为 "MUST" 并在 TxRunner.RunInTx godoc 中明确强制所有参与者使用传入 ctx，是正确方向。

---

### 主题 3：幂等设计（Claim/Commit/Release 模式）

| 方案 | 模型 | 崩溃恢复延迟 | Redis key 数/消息 | 容量估算 |
|------|------|---------|---------|---------|
| **go-zero SETNX** | 单 key（"已处理 or 处理中"无法区分）| 等待全 doneTTL（最差 24h）| 1 | 无官方指导 |
| **Watermill Deduplicator** | hash-based 内存，单 key | N/A（内存，不持久）| 0（内存）| 无指导（内存受限）|
| **GoCell Claimer** | 双 key（`lease:5min` + `done:24h`）| 等待 leaseTTL（5 min） | 稳态 1（处理中峰值 2）| `DefaultTTL=24h`, `DefaultLeaseTTL=5m`（有文档）|

**结论（≥3 项支撑）**：GoCell 两阶段 Claimer 在崩溃恢复延迟方面显著优于 go-zero SETNX（5 分钟 vs 24 小时），且 commitScript 在 Commit 后立即 DEL lease key，稳态与 SETNX 内存占用相当。唯一缺口是无容量规划文档，应在 `idempotency.go` 注释中补充。

---

## 5. 建议与修复优先级

### P1 必须修复（下一 Sprint）

**G2-01（relay 无积压 Gauge）**  
- 影响：无法构建消息积压告警，消费积压无可见性  
- 修复：`RelayCollector` 增加 `RecordPendingDepth(count int64)`，成本低（接口扩展 + adapter 实现）

**G2-02（retry budget 耗尽无 counter，日志级别错误）**  
- 影响：消息永久丢失（进 DLX）无 metrics，日志 Warn 级而非 Error，违反 observability.md  
- 修复：增加 `ConsumerCollector` 接口；日志改 Error 级；reject 路径 record counter

**G2-03（sweeper 失败沉默）**  
- 影响：sweeper DB 连接中断时命令无限期卡在非终态，运维无感知  
- 修复：`runTick` 错误分支增加 `slog.Error` 兜底；增加 counter

**G2-11（HandleResult.Receipt 公开内部字段）**  
- 影响：API 认知负担重，长期维护成本高  
- 修复：改为 unexported 字段或移入 internal 类型（需确认 K#12 roadmap 时间线）

### P2 规划入 Backlog

| 问题 | 操作 |
|------|------|
| G2-04（payload 无上限） | 增加 `MaxPayloadSize` 常量 + Validate() 检查 |
| G2-05（命令缺授权钩子） | 文档明确"终态写授权前置于适配层" |
| G2-06（fail-open 无 counter） | RecordDrop 时 increment metrics counter |
| G2-07（inmem Receipt Commit/Release 共享 Once） | 增加 committed atomic.Bool 标志 |
| G2-08（msg.ID 字符集校验） | 复用 idutil.IsSafeID |
| G2-09（Writer.Write SHOULD → MUST） | 改为 MUST，TxRunner.RunInTx godoc 补充 |
| G2-10（metadata 校验重复） | 提取共享常量和校验函数到 kernel/outbox 或 kernel/metautil |
| G2-12（FailureMode 双类型） | 合并为单一 FailurePolicy |
| G2-13（缺 HandleResult 工厂函数） | 提供 `outbox.Ack()`, `outbox.Requeue(err)`, `outbox.Reject(err)` |
| G2-14（ContractSpec 魔法字符串） | 增加 Kind/Transport 枚举常量 |
| G2-15（persistence 零测试） | 补充 TxCtxKey round-trip 测试 |
| G2-16（idempotency 无并发 race 测试） | 增加并发 Claim race test |
| G2-17（HandleResult 零值降级无测试） | 新增 zero-value disposition 降级测试 |
| G2-18（Redis 幂等 key 无容量规划） | 注释中增加容量估算公式 |
| G2-19（Sweeper 无构造器） | 提供 NewSweeper 构造函数 |
| G2-20（AdvanceCommand 导出但禁用） | 移入 internal/ 子包 |

---

## 6. 亮点

| 设计亮点 | 位置 | 说明 |
|---------|------|------|
| `Disposition = iota+1` 零值安全防御 | [outbox.go](../../kernel/outbox/outbox.go) | 零值 HandleResult{} 被安全降级为 Requeue 而非 Ack，fail-safe 设计 |
| `statusTransitions` 显式状态转移表 | [command/status.go](../../kernel/command/status.go) | 14 对合法转换集中维护，非法转换在领域层被阻断 |
| `ReservedMetadataKeys` + `ObservabilityMetadata` 物理字段分离 | [outbox.go](../../kernel/outbox/outbox.go) | 消除了 producer 伪造 trace_id 的攻击面，比 Watermill/NATS header map 更安全 |
| `kernel/wrapper.Tracer` 自定义接口 | [wrapper/tracer.go](../../kernel/wrapper/tracer.go) | 不直接导入 OTel SDK，保持 kernel 层对 adapters/otel 的解耦 |
| `RelayCollector` LIFO rollback 注册 | [outbox/relay_collector.go](../../kernel/outbox/relay_collector.go) | 防止 Prometheus "duplicate collector" 错误，原子回滚注册 |
| ConsumerBase `leaseLost` hard fence | [outbox/consumer_base.go](../../kernel/outbox/consumer_base.go) | stale holder 提交死租约被降级为 Requeue，防止幽灵 Ack |
| `ExponentialDelay` 溢出保护 + jitter | [outbox/consumer_base.go](../../kernel/outbox/consumer_base.go) | bits.Len64 溢出保护 + maxDelay 上限 + crypto/rand jitter，无无上限重试风险 |
| `InMemClaimer.Claim` crypto/rand 失败 → ClaimBusy | [idempotency/inmem.go](../../kernel/idempotency/inmem.go) | fail-safe 方向正确，无 nil receipt 泄漏 |
| 两阶段 Claimer 崩溃恢复 5 分钟 | [idempotency/idempotency.go](../../kernel/idempotency/idempotency.go) | 远优于 go-zero SETNX 的 24 小时恢复等待 |

---

*报告生成时间：2026-05-04*  
*审查使用六席位 + 3 项开源对标（ThreeDotsLabs/watermill, nats-io/nats.go+NATS JetStream, go-zero+Temporal）*
