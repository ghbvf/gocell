# 框架能力交互缺口分析（35 项）

> 系列文档 4/4 · 配套文档：[001 DDD 适用范围](./202605131500-001-ddd-scope-analysis.md) · [002 能力盘点](./202605131500-002-framework-capability-inventory.md) · [003 交互模式](./202605131500-003-capability-interaction-patterns.md)

本文盘点 GoCell 框架现有能力之间"**没接通**"的缺口——单看每个能力都能用，组合起来要业务自己拼胶水。

按三批组织：第一批为抽象缺失（framework 没做），第二批为组合断点（做了没串），第三批为运维/演进/外部边界。

---

## 第一批：抽象缺失（P0/P1 必须做）

### 缺口 1：HTTP 幂等无框架支持

| 维度 | 当前 |
|---|---|
| consumer 侧事件幂等 | ✅ `kernel/idempotency.Claimer` 透明 |
| HTTP 请求幂等（Idempotency-Key header） | ❌ 框架无，每个 cell 自己处理 |
| 跨 cell call 幂等 | ❌ |

**设计张力**：
- 存储窗口：consumer 全局唯一 event_id；HTTP 客户端任意 key 需 (userID, key) 命名空间
- 响应回放：consumer 幂等可 skip+Ack；HTTP "已处理"必须回放原始 status + body + headers
- 进行中并发：客户端同 key 并发重试策略
- 范围：单 cell / 跨 cell / 全 assembly 三种语义

**设计骨架**：`runtime/http/idempotency.Store` 接口 + `RecordedResponse` + middleware 装载在 Auth 之后、handler 之前；Store 实现复用 Redis namespace。

**依赖前置**：缺口 5（after-commit hook）。

### 缺口 2：L3 Saga / Workflow 运行时

**当前状态**：`cellvocab.L3` 词汇存在，`runtime/workflow` 不存在；现有 L3 场景全手撕。

**设计张力**：framework vs cell 边界 / 声明式 vs 编程式 / 与 outbox 的关系 / 补偿原语 / 超时语义。

**设计骨架**：`kernel/workflow` 提供 `Step{Run, Compensate, Timeout, Retries}`，framework 提供 state journal 持久化 + leader-elect engine。

**依赖前置**：缺口 5（after-commit）+ distlock leader。

### 缺口 3：跨 Cell In-Process Contract

**当前状态**：同 assembly 内 cell-A 想调 cell-B 只有 HTTP / event / import 三条路，都有问题。

**设计骨架**：codegen 同一份 contract.yaml 派生 server handler + client invoker；assembly.yaml 拓扑决定 inproc vs http transport，业务无感知。

### 缺口 4：Command Bus 半空壳

**当前状态**：`runtime/command` 只有 lifecycle，无 dispatcher / handler registry / idempotency / 与 outbox 桥。

**设计骨架**：`Dispatcher.Dispatch[C, R]`（同步）+ `DispatchAsync[C]`（异步，写 command outbox）；contracts/command codegen 派生 typed Command + Handler 接口。

**依赖前置**：缺口 1（HTTP idempotency-key ↔ command_id 映射）。

### 缺口 5：Tx After-Commit Hook

**当前状态**：`TxRunner.RunTx` 提交后无 hook；业务想"事务成功后清缓存"无路径。

**设计张力**：诱惑陷阱（复活 outbox 想消灭的反模式）；正当用途窄（cache invalidation / metric / log / WS broadcast）；不允许扩展到 stateful 副作用。

**设计骨架**：克制版 `RunTxWithBestEffortAfterCommit(ctx, fn, hooks...)`；Hook 签名无返回值；archtest 限制只允许 transient 副作用。

### 缺口 6：Schema Registry Runtime

**当前状态**：api-versioning.md 是约定，运行时无版本协商 / payload 兼容性检查 / 自动 routing。

**设计骨架**：build-time codegen 生成 schema hash 嵌入 binary；runtime 在 outbox.Entry header 带 hash；consumer 按 hash + CompatPolicy 决策 decode。

### 缺口 7：Health Dependency 感知

**当前状态**：cell.Health 是 boolean Up/Down/Degraded，依赖图扁平，无部分失败映射。

**设计骨架**：cell.yaml 声明 `dependencies: [{target, severity}]`；runtime 计算 `cell.HealthStatus = my-status × dependencies-status`；assembly.yaml 派生启动顺序。

---

## 第二批：组合断点（P1/P2 应做）

### 缺口 8：失败模式预算未共享

五个失败感知点（HTTP Circuit / Rate Limit / Outbox Consumer / Retry Budget / HTTP Client Circuit）各算各的，没有共享口径。

**设计骨架**：`kernel/failurebudget` 共享观测口径 + 声明式 action map（degrade-mode / shed / throttle）。

### 缺口 9：Backpressure 反压通道

下游 outbox 堆积 / consumer lag 增大时，无反压到上游 HTTP 入口。

**设计骨架**：`runtime/backpressure.Observer` 接口；HTTP middleware 装载 Observer，按 Pressure Level 走 throttle / shed。

### 缺口 10：跨 Transport 的 Principal 传播

Principal 是 transport-coupled——HTTP 路径才有，进入事件 / Worker / WS 后丢失。

**设计骨架**：`PrincipalEnvelope`（含 OnBehalfOf 链）；outbox.Entry.Headers 自动注入 envelope；consumer 端 ConsumerBase 自动 unmarshal 写回 ctx。

### 缺口 11：观测三栈反查链路断裂

cell label 对齐了，但 trace_id → audit / metric exemplar / alert → cell.yaml owner 反查链路缺失。

**设计骨架**：`kernel/observability/correlation.Correlation` envelope；outbox + audit ledger 自动注入 trace_id；提供 `/internal/v1/correlate?trace_id=` 反查 API。

### 缺口 12：Large Payload / Stream

outbox.Payload 无 size 限制 / 无 streaming；audit ledger JSONB 列超大撑爆；S3 adapter 与 outbox 无桥。

**设计骨架**：`runtime/blobref.BlobRef`（Storage / SHA256 / Size / Codec）；outbox.Emit 自动判断 size 走 inline 或 externalize。

### 缺口 13：数据生命周期 / GDPR

audit append-only 无 retention；outbox 无清理 worker；PII 无 right-to-be-forgotten 路径。

**设计骨架**：cell.yaml 声明 `dataClasses` + retention + onExpire；framework 提供 ErasureHandler 跨 cell 协调。

### 缺口 14：周期任务 / 定时调度

`worker.periodic` + `distlock` 要业务手动组合；缺统一 ScheduledTask 抽象。

**设计骨架**：`kernel/scheduler.ScheduledTask{Schedule, LeaderOnly, Idempotent, Run}`；framework 自动 leader-elect + fencing + 缺勤补跑策略 + run history。

### 缺口 15：版本偏移 / 滚动部署

部署窗口期间 mixed-version 行为不可控；api-versioning 约定无运行时检查。

**设计骨架**：`kernel/version.CompatMatrix`；启动期实例向 cluster registry 注册版本；检测混合版本时启用 compat mode。

### 缺口 16：故障注入 / Chaos / 回放

celltest / outboxtest 单元 OK，但故障注入 / 时间快进 / 网络分区 / 流量录制回放缺。

**设计骨架**：`pkg/testutil/chaos.FaultInjector`（build tag `chaos` 隔离）；adapter 接口埋 fault point。

### 缺口 17：Graceful Degradation

依赖部分失败 → readyz 红 → K8s 摘流量 → 整体不可用；缺降级运行模式。

**设计骨架**：cell.yaml 声明 `fallbacks: [{dependency, mode, fallbackImpl, alert}]`。

### 缺口 18：Cache 一致性

`adapters/redis Cache` 是单纯 KV；无 write-through / 失效广播 / Principal scope / negative cache 协议。

**设计骨架**：`runtime/cache.Region{Name, Scope, TTL, Invalidate}`；与 outbox event 桥接自动 invalidate。

### 缺口 19：Stream / Pagination / WS

列表 cursor 语义未标准化；WS 无 topic 抽象 / 无 EventBus 桥接；SSE 完全没有。

**设计骨架**：`runtime/stream.Cursor`（keyset + SnapshotID）；WS topic 抽象 + 自动 EventBus fanout；SSE codegen 支持。

### 缺口 20：Secret Rotation 跨能力路径

KeyProvider rotation 由 Vault 侧，但 JWT / HMAC / ValueTransformer KEK / ServiceToken / Refresh signing key 的 rotation 协议未明。

**设计骨架**：`kernel/crypto/rotation.Rotatable` 接口；所有签名/加密 key 必经 Rotatable；wire token 自带 kid 选 key。

### 缺口 21：Resource Sizing

连接池 / goroutine / 内存预算各 adapter 独立配置；缺统一 sizing 协议 + 资源耗尽行为契约。

**设计骨架**：`kernel/resource.Budget{Owner, Type, Limit, OnSaturation}`；saturation 触发 Backpressure。

### 缺口 22：AuthZ 策略 ↔ Contract 反向联动

contract.yaml 不声明权限；策略与 endpoint 是两套独立声明，靠 cell 内 wire 手动绑定。

**设计骨架**：contract.yaml 加 `authz.required` + `authz.resource`；codegen 派生 handler 签名要求 Principal 必带相应权限。

---

## 第三批：时间维度 / 外部边界 / DX 工具链

### 缺口 23：Time / Clock / Causality 统一模型

分布式时间维度缺：跨实例 wall clock 漂移 / monotonic vs wall / causality vs absolute time / 三种时刻（发生 / 落库 / 消费）。

**设计骨架**：`kernel/clock.Instant{Wall, Mono, Logical, CauseOf}`；outbox.Entry.Headers 自动注入 emitted_wall / emitted_logical / caused_by。

### 缺口 24：Event Ordering / Partition / FIFO

outbox 跨事务无序；relay 多 worker 并行；RabbitMQ 默认不保 FIFO；业务"同 user 状态变更按序消费"无声明式表达。

**设计骨架**：`outbox.Emit(... PartitionKey(userID), CausalAfter(prevEntryID))`；contract.yaml 声明 `ordering.partitionKey` + `guarantee: per-key-fifo`。

### 缺口 25：Migration ↔ Deploy 协同

`tools/pg-migrate` 有；但 migration 与 code 版本兼容矩阵 / rolling deploy 期间 mixed schema 行为 / 回滚协议缺。

**设计骨架**：`kernel/migration.Migration{Version, PreCodeMin, PreCodeMax, CompatPhases}`；CI 守 migration ↔ code 兼容交集非空。

### 缺口 26：Drain / Graceful Shutdown of In-Flight

shutdown_barrier 有 grace；但 in-flight HTTP / consumer / long-running workflow / WebSocket 的优雅完成协议未明。

**设计骨架**：`kernel/lifecycle/drain.Drainer` 接口；各 transport 注册 Drainer；ConsumerBase 检测 shutdown 后完成手头 + Release lease。

### 缺口 27：Outbound HTTP / Webhook

cells 调外部 API 无标准框架：无 retry / circuit / observability / idempotency / signing。L4 DeviceLatent 场景全是 outbound，但无支撑。

**设计骨架**：`contracts/http-outbound/` codegen client + outbound consumer 模式；service 写"调用三方"意图到 outbox（kind=outbound-call）；outbound worker 消费 + circuit。

### 缺口 28：Tenant Capacity / Quota / Cost Attribution

多租户维度缺；即便不做完整租户，资源归属维度本身缺。

**设计骨架**：`kernel/attribution.Attribution{Cell, Tenant, Contract}`；ctxkeys.AttributionFrom 在 listener-root 注入；metric 自动切片；Quota 同步检查。

### 缺口 29：Client SDK 生成

codegen 只生成 server-side；client SDK（Go 内部 / TS 前端 / Python 脚本）业务自己写。

**设计骨架**：`tools/codegen/sdkgen/{go-client, typescript, python}`；SDK 自带 errcode unmarshal / retry / trace propagation。

### 缺口 30：Contract Test / Consumer-Driven

跨服务实际兼容性靠 e2e；缺 consumer pact 收集 + producer "对所有已知 consumer 兼容" 自动验证。

**设计骨架**：`expectations.yaml` 声明 consumer 期望；producer CI 跑 generated consumer expectation suite。

### 缺口 31：Audit / Forensic Query API

auditquery slice 存在但能力薄；跨事件因果链 / 重放 / 反向查 / PIT 重建均缺。

**设计骨架**：扩 auditquery API：`/audit/causal-chain` `/audit/timeline?trace_id=` `/audit/replay` `/audit/state-at`。

### 缺口 32：Idempotent Restart / State Recovery

进程重启后各能力部分有恢复，缺统一 restart-safe 协议。

**设计骨架**：`kernel/recovery.Recoverable` 接口；bootstrap 启动期扫所有 Recoverable 注册 → 从 store 拉 unfinished tokens → Resume。

### 缺口 33：Local Dev / Replay / Time Travel

docker-compose + 单 cell run/test 有；但录制重放 / 跨 cell 联调 / 状态快照回滚 / 时间快进 / hot reload 缺。

**设计骨架**：`tools/devloop/` 提供 record / replay / snapshot / fast-forward 子命令。

### 缺口 34：Policy as Code（CORS / CSP / Cookies / Rate Limit）

HTTP 安全策略 / Cookie 标志 / Rate Limit 都是 global 配置或业务手撕；cell 不可声明。

**设计骨架**：cell.yaml / contract.yaml 加 `httpPolicy` 字段；framework 派生 middleware 统一应用。

### 缺口 35：Feature Flag ↔ Code Path 协同

flag 名是字符串硬编码；与代码路径关联靠 `if` check；rollout / sticky bucketing / audit 联动缺。

**设计骨架**：`contracts/featureflag/*.flag.yaml` codegen 派生 typed flag：`flags.NewLoginFlow.Enabled(ctx) bool`；自动 metric + audit 决策。

---

## 缺口性质分类矩阵

```
              抽象缺失          维度共享           生命周期            声明面窄
              ─────────────    ─────────────     ─────────────       ─────────────
长链路        [1] HTTP idem    [10] Principal    [15] Version skew   [22] AuthZ↔contract
              [2] Saga         [11] Correlation  [25] Migration      [29] SDK gen
              [3] InProc RPC                     [26] Drain          [30] Contract test
              [4] Command bus                    [32] Restart-safe   [35] Feature flag
              [5] AfterCommit                                        [34] HTTP policy

横切治理       [7] Health dep   [21] Resource     [13] Data lifecycle [23] Time/clock
              [8] Failure bud  [28] Attribution  [20] Secret rot     [24] Event order
              [9] Backpressure                   [17] Degradation
              [27] Outbound

观测/数据      [16] Chaos       [12] Large pay    [33] Local dev      [31] Forensic
              [18] Cache       [19] Stream/WS    [14] Scheduled
                                                  [6] Schema reg
```

---

## 强枢纽节点（Wire Envelope）

35 缺口中近半数依赖**同一份扩展信封**（outbox.Entry.Headers + ctx envelope）：

```
                     [10] Principal Envelope
                     [11] Correlation
                     [23] Time / Causality
                              │
                              ▼
                       共享 envelope
                              │
                ┌─────────────┼─────────────┐
                ▼             ▼             ▼
         outbox.Headers   audit.Entry   trace span
                │
                ▼
   一个 wire 信封解锁：[1][2][3][4][5][22][24][27][30][31][32][35]
```

**核心识别**：把 wire envelope 做对，下游缺口的 interaction 半径自动变小。

---

## 缺口依赖图（顶层）

```
                ┌──────────────────────────────────┐
                ▼                                  │
   [5] After-Commit Hook ──┐                       │
                           │                       │
                           ▼                       │
                  [1] HTTP Idempotency             │
                           │                       │
                           ▼                       │
                  [4] Command Bus                  │
                           │                       │
                           ▼                       │
                  [2] L3 Workflow / Saga ←─────────┘
                           │
                           ▼
                  [Projection / Replay] (P3)


   [3] In-Process Contract ←─ codegen 扩展
                           │
                           ▼
                  跨 cell typed RPC

   [6] Schema Registry ←─ codegen 扩展（独立线）

   [7] Health Dependency ←─ cell.yaml 字段（独立线）
                           │
                           └→ 启动依赖编排
```

**核心串联线**：**After-Commit Hook → HTTP Idempotency → Command Bus → Saga** 是递进，前面是后面的基础设施。

---

## 落地优先级建议

| Wave | 内容 | 周期估 | 收益 |
|---|---|---|---|
| **W0** | 扩 outbox.Entry.Headers + 定义 envelope schema（[10][11][23]） | 短 | 解锁后续多个缺口 |
| **W1** | After-Commit Hook（含 archtest 白名单） + Health Dependency 声明 + 观测反查 | 短 | 立即受益 |
| **W2** | HTTP Idempotency middleware + RecordedResponse Store | 中 | 缓解最高频痛点 |
| **W3** | Command Bus + codegen + 与 outbox / idempotency 协同 | 中 | 打通 CQRS 写侧 |
| **W4** | In-Process Contract + assembly 拓扑感知 transport bind | 中 | 解决跨 cell 调用合规缺口 |
| **W5** | Schema Registry（codegen hash + runtime check） + Version Skew | 短 | 把约定升机制，AI-rebust 升级 |
| **W6** | L3 Saga / Workflow Engine（基于 W1–W4） | 长 | L3 consistencyLevel 名实相符 |
| **W7** | Auth → Audit middleware + observability 反查链路 + AuthZ↔contract | 短 | 合规 + 排障基础 |
| **W8** | Outbound HTTP / Webhook + Drain / Restart-safe | 中 | 外部边界标准化 |
| **W9** | Migration ↔ Deploy + Secret Rotation 协议 | 中 | 生产演进基础 |
| **W10** | Projection / Replay runtime（基于 W5+W6） | 长 | L3 投影场景体系化 |
| **后置** | EventBus → WS bridge / 多租户 / 全套 DX 工具链 | — | 等业务驱动 |

---

## 缺口性质判断

### 三批缺口的本质差异

| 批次 | 数量 | 性质 |
|---|---|---|
| 第一批（1–7） | 7 | **抽象缺失**：framework 没做的 framework 范畴必填项 |
| 第二批（8–22） | 15 | **组合断点**：能力做了但没串起来（envelope / 共享口径 / 共有协议） |
| 第三批（23–35） | 13 | **时间维度 / 外部边界 / DX 工具链** |

### 三批的根本问题归约

35 个缺口的本质可归到 **3 个核心问题**：

1. **协议层不够薄**：wire envelope 字段集没收敛，导致每个能力各搞各的
2. **时间维度系统性缺位**：所有抽象假设静态
3. **声明面收口不全**：cell.yaml / contract.yaml 还能装更多事

### 缺口的"难度分布"

| 难度 | 缺口数 | 例子 |
|---|---|---|
| **改 wire envelope** | ~8 | [10][11][23] 等 |
| **加 codegen 字段** | ~10 | [6][22][24][29][34][35] |
| **新建 framework 抽象** | ~10 | [1][2][4][27] |
| **新建协议** | ~5 | [13][20][25][32] |
| **DX 工具链** | ~3 | [29][30][33] |

**改 wire envelope** 是最高 ROI 区——动 1 处接管多缺口。
**新建 framework 抽象** 是最大投入区——长周期。
**DX 工具链** 是最易拖延区——直到团队规模驱动。

---

## 边际收益递减说明

- **第一批（1–7）是必填项**——没做这些，L3+ 不可用
- **第二批（8–22）是应做项**——做了大幅减少业务 boilerplate，不做能撑住但脆弱
- **第三批（23–35）是可做项**——做了优雅，不做大多数业务团队也能挺过去

继续找缺口会越来越多变成"细化 / scope 决定 / 业务团队偏好"——不是 framework 设计问题。如：

- GraphQL 支持（业务偏好）
- gRPC adapter（部署形态偏好）
- Notification email/SMS/push（更像 outbound 子集）
- A/B testing platform（更像 feature flag 子集）
- BFF / API gateway（更像 cross-cell composition 子集）
- 文件上传 / 直传 S3（更像 large payload 子集）

这些是真实需求，但**不是 framework 必须答的问题**——是 framework 已有能力的业务侧组合应用。

---

## 总判断

1. **GoCell framework 当前阶段**：**单元精度高、组合精度低**的典型早期状态。每个独立能力（Cell / Outbox / Auth / Errcode / Codegen / Archtest）做得相当精致，但能力之间的 wire envelope / 共享口径 / 共有协议远没成型。

2. **修复优先序的杠杆点**在 **W0–W2**：扩 envelope + after-commit + idempotency 三件套，解锁后续多个缺口。

3. **AI-rebust 视角下的系统性升级机会**：当前 archtest 重点守"形态唯一"（panic / errcode / outbox 字面量等），下一阶段应该升级到守"**关系唯一**"（wire envelope 字段集 / contract.yaml ↔ 实现 ↔ test 三方一致）。这要求 archtest 工具能跨多文件做关系验证——`tools/archtest/internal/typeseval` 已有基础，但跨 yaml ↔ code 的双向关系测试尚未普及。

4. **真正的战略选择**：framework 接下来是"**做深现有能力的精度**"，还是"**做广能力的组合**"？两者投入截然不同。后者收益更高——前者已经处于 marginal returns，后者直接决定 L3+ 能否落地。

**简言之：第一批缺口是 "framework 没做的事"，第二批缺口是 "framework 做了但没串起来的事"。串起来的代价远小于重做的代价，应当优先。**
