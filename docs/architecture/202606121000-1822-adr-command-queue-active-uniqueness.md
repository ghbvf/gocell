# ADR-1822: Command Queue Active-Uniqueness — cert-renewal correctness ownership

| 字段 | 值 |
|------|---|
| ADR ID | 1822 |
| 状态 | **Accepted** |
| 日期 | 2026-06-12 |
| Issue | [#1820](https://github.com/ghbvf/gocell/issues/1820)（cert-renewal producer 重设计） |
| 关联 | ADR `202606040550-1044-adr-command-bus-dispatch-funnel.md` §Amendment 2026-06-12(#1820)（dispatch funnel 不变，本 ADR 是 renewal 正确性的新单源）/ ADR `202605291600-661-adr-kernel-reconcile-design.md` §4.4（消费方幂等契约）/ ADR `202606101200-1821-adr-reconcile-system-producer-identity.md`（系统身份） |
| 对标 | River `insert_opts.go UniqueOpts.ByState`（非终态唯一性）/ Temporal（at-most-one-open WorkflowId）/ client-go workqueue（key 收敛）/ cert-manager issue #4642（observe-then-decide 的竞态） |

> **真值边界**：本 ADR 是 command-queue active-uniqueness 设计的概念单源。PG partial index 形态 / `WithActiveUniqueness` 类型形状 / conformance test 目标以各自代码 godoc 和 archtest 为执行真源，本文件汇总决策 + 评级矩阵，不复制实现细节。

---

## 1. 上下文与问题

### 1.1 旧设计的根本缺陷：producer 用时间窗代理队列所有权

ADR-1044 §Amendment 2026-06-11(#1820) 落地的设计（下称"时间窗方案"）：

- `devices.renewal_requested_at` 记录上次请求时间，`certRenewalRetryInterval`（25h）是重发窗口。
- producer 在扫描层用 `renewal_requested_at <= retryBefore` 做抑制判断。
- relay Claimer（24h done-TTL）做二级兜底去重。

**根本缺陷**：producer 用自己的侧表状态（`renewal_requested_at`）来模拟「命令还在队列中」这个事实——这是间接观测（observe-then-decide 模式）而非结构保证。

具体风险场景：

1. **离线设备积压无界**：设备长期离线期间，每个 retryInterval 过后 producer 重发一条 rotate-cert 命令进 outbox → relay → 命令队列，命令因设备离线停在 Pending/Sent/Delivered。retryInterval 为 25h、near-expiry 窗口 30d，意味着单台离线设备最多可积压 `30d / 25h ≈ 28` 条活跃 rotate-cert 命令。Claimer done-TTL 只在 dispatch 成功后才产生记录，对 Pending 态命令无效。
2. **多副本并发扫描**：多个 reconcile 副本（multi-pod，leader-elect 窗口期内）或并发 tick 可绕过单实例时间窗（`renewal_requested_at` CAS 只有 `cert_epoch` 不变式，同 epoch 时间戳可被并发 mark 多次）。
3. **有界但非零速率累积**：即使单副本，每 25h emit 一条 rotate-cert 到设备命令队列，命令永不 terminal（设备离线、DLX 重试预算耗尽后只进死信，不在活跃队列），活跃队列项单调增长。

**根本原因（与 cert-manager #4642 同构）**：产生"命令是否在飞行中"的答案的正确地方是命令队列（状态机拥有者），不是 producer。producer 去观测侧表做抑制，是把队列语义外包给 producer，这在分布式系统中注定有竞态。

### 1.2 目标

单台设备在任意时刻至多有**一条活跃的 rotate-cert 命令**（Pending/Sent/Delivered），不受以下因素影响：
- producer 副本数量（leader-elect 仍有重叠窗口）
- 设备在线状态
- retryInterval / TTL 参数选择
- 命令 terminal 失败后的重试路径

命令队列本身拥有这个保证，producer 不拥有。

---

## 2. 决策 D1–D6

### D1：命令队列持有「非终态内活跃唯一性」（state-aware active-uniqueness）

`kernel/command.EnqueueOptions.IdempotencyKey`（或等价字段）在命令处于非终态（Pending / Sent / Delivered）时唯一，在命令到达终态（Succeeded / Failed / Expired / Canceled）时释放，使同一 key 的下一条命令可以入队。

**语义来源**：
- River `UniqueOpts.ByState`：`{ByState: []river.JobState{river.JobStateAvailable, river.JobStateRunning, river.JobStateRetryable}}`——非终态内唯一，终态自动释放，完全对齐。
- Temporal：same WorkflowId 的新 execution 在旧 execution 非 terminal 时被拒或排队。
- client-go workqueue：key 收敛，同 key 在 processing 期间不重入队。

**实现（双存储，两者须一致）**：
- **PG**：命令表（`device_commands`）建 **partial unique index**：
  ```sql
  CREATE UNIQUE INDEX CONCURRENTLY device_commands_active_idempotency_key_uidx
      ON device_commands (idempotency_key)
      WHERE status IN (1, 2, 3); -- 1=Pending, 2=Sent, 3=Delivered
  ```
  入队时用 `ON CONFLICT (idempotency_key) WHERE status IN (1,2,3) DO NOTHING`（或 `DO UPDATE SET updated_at=now()`），命令达到终态（4/5/6/7）时 index 条目自动退出，key 释放。
- **In-mem**（`kernel/command` 内存实现）：`Enqueue` 在 status ∈ {Pending,Sent,Delivered} 的命令中按 `IdempotencyKey` 做集合查找；命令达到终态时从活跃集移除。
- **一致性**：两者必须对"活跃"的定义完全相同（同一 status 值集），由 `commandtest` 跨存储 conformance 套件强制。

| Enforcement 载体 | 评级 |
|-----------------|------|
| PG partial index DDL（`schema_guard.go` + golden 锁 index 定义字符串） | **Hard**（index 定义字节锁，drift 即红） |
| in-mem `Enqueue` 活跃集检查（sealed `commandtest` conformance 套件跨两个实现运行） | **Medium+**（conformance test 可执行；两实现必须通过同一 conformance，偏差 CI 红） |

### D2：单耦合 option `WithActiveUniqueness(deadline time.Duration)` — 不安全组合不可表达

「有 active-uniqueness key」但「命令无 deadline（可能永远非终态）」是最危险的组合：key 被永久持有，重试路径永久阻断。

**决策**：active-uniqueness 和终态保障**必须一起请求**，通过单一 option：

```go
// WithActiveUniqueness 同时声明：
// 1. 此命令在非终态期间的 IdempotencyKey 唯一（队列去重）
// 2. 此命令在 deadline 后若仍非终态，Sweeper 将其置 Expired（终态，释放 key）
//
// 不可以只请求 (1) 不请求 (2)——该不安全组合在此 API 上不可表达。
func WithActiveUniqueness(deadline time.Duration) EnqueueOption
```

`EnqueueOption` 是私有类型（包外不可构造），消费方**唯一**能设 `IdempotencyKey` 的路径是 `WithActiveUniqueness`（不能手动传 `IdempotencyKey` 同时跳过 `OverallDeadline`）。

**Sweeper 是前提条件**：命令 Sweeper（`kernel/command.Sweeper`，已有）周期扫描非终态命令，对超过 `OverallDeadline` 的命令置 Expired。Expired 是终态，partial index 条目自动退出，key 释放。没有 Sweeper，有 deadline 的命令也不会自动 expire——但 Sweeper 是框架组件（reconcile Loop 驱动），不是业务负担。

| Enforcement 载体 | 评级 |
|-----------------|------|
| `EnqueueOption` sealed 类型 + `WithActiveUniqueness` 是 `IdempotencyKey` 的唯一写入点（包外 `EnqueueOptions{IdempotencyKey: x}` 字面量构造编译报错） | **Hard**（Go type system + unexported 字段，「有 key 但无 deadline」字面量不可表达） |
| 如果未来需要公开 `EnqueueOptions` 结构体（跨包构造），则改为 builder funnel：`EnqueueOptions` 无 export `IdempotencyKey` 字段，`WithActiveUniqueness` 是写入点 | **Hard**（同上，sealed field） |

### D3：relay Claimer 降级为 dispatch-time 优化（不再是正确性负担）

旧设计中 relay Claimer 的 24h done-TTL 是 single-emit 保证的**第一道防线**（§Amendment 2026-06-11(#1820)），producer 的时间窗是"与 TTL 协调"的上游。

**新设计**：队列的 active-uniqueness（D1）是第一道、也是结构性防线。relay Claimer 降级为：
- **dispatch-time 去重优化**：防止同一 outbox entry 被多次消费（relay 的固有功能，不变）。
- **不再是「命令唯一」的正确性来源**：Claimer done-TTL 过期不代表命令已 terminal（命令可能还在队列 Pending），但 D1 保证新 enqueue 被幂等 ignored/noop——key 还活着。

Claimer 的接口、wiring、3-arg `WithCommandDispatch` 均不变（ADR-1044 §4 funnel 矩阵不变）。

| Enforcement 载体 | 评级 |
|-----------------|------|
| 不新增 enforcement：Claimer 现有 archtest `COMMAND-ASYNC-DISPATCH-CALLER-01`（下游 Hard）完整保留 | 无变化 |

### D4：producer 变为无状态（stateless）

旧 producer 需要维护 `devices.renewal_requested_at` 来避免重复入队。新 producer 不需要：

- 每个 reconcile tick 扫描近过期证书，对每台设备无条件调用 `command.Enqueue`（带 `WithActiveUniqueness`）。
- 队列的 D1 保证至多一条活跃：Pending 时 enqueue 被幂等 rejected（key 冲突）；terminal 后 key 释放，下次扫描正常 enqueue。
- `renewal_requested_at` 和 `renewal_requested_epoch` 列从 `devices` 表移除（迁移 DROP COLUMN），相关 repo 接口方法（`MarkCertRenewalRequested` / `ListCertificateRenewalCandidates` 的 `retryBefore` 参数）归于简化。

扫描 batch 上限保留（D5），前向进度由 batch requeue 保证。

| Enforcement 载体 | 评级 |
|-----------------|------|
| `devices` 表 `renewal_requested_at` / `renewal_requested_epoch` 列不存在（`schema_guard.go` golden 锁列集，列存在即红） | **Hard**（schema guard golden 字节锁） |

### D5：bounded scan — 批量扫描上限保留

producer 每 tick 最多扫 `BatchSize`（100）条近过期证书，满批 → `Result{RequeueAfter: batchRequeueDelay}`（1s）继续下一批，非满批 → `Result{}`（回 ticker）。此机制与队列 active-uniqueness 正交，保留不变。

### D6：relay 注入 active-uniqueness key + deadline 到 ctx（ClaimAcquired 路径）

relay 在 Claimer Acquired 后、dispatch 前，把 command 的 `IdempotencyKey`（由 `ClaimKeyFromEntry` 派生，形态与 D2 `WithActiveUniqueness` 一致）和 `OverallDeadline`（来自命令 contract metadata 或 composition-root 配置）注入 ctx，供下游 `kernel/command.Enqueue` 读取。

这使「relay 消费 command entry → 在设备命令队列 enqueue」路径全程带 active-uniqueness 语义，不依赖 producer 侧的任何状态。

---

## 3. 拒绝的备选

### 备选 A：保留时间窗（retryInterval）并修复并发问题

用 PG row-level lock（`SELECT FOR UPDATE`）或 advisory lock 保护 `renewal_requested_at` 更新，消除并发扫描导致的重复 mark。

**拒绝理由**：
1. 并发问题是可以修，但**根本问题**（离线设备积压无界）无法通过锁解决——锁只防止同 tick 并发，不防止跨 tick 累积。
2. 依然是 observe-then-decide 模式：producer 的侧表状态替代了队列语义，两套状态需要协调（与 cert-manager #4642 反对理由完全一致）。
3. 复杂度增加（锁 + TTL 协调），不减少。

### 备选 B：cert-manager 风格 — observe in-flight CertificateRequest

在 producer 中主动查询命令队列，检查是否存在活跃 rotate-cert 命令（相当于 observe-then-decide），如无则 enqueue。

**拒绝理由**：cert-manager issue #4642 是该模式的权威案例：observe 和 enqueue 之间存在 TOCTOU 窗口，在并发 reconcile（多副本 / 多 worker）下不可消除。GoCell 的 reconcile Loop 是 multi-worker 模型（`WithConcurrency`），该竞态是结构性问题，非调优可消。"队列拥有正确性"比"observer 查询正确性"的层次更高，不应在 producer 层重建队列语义。

### 备选 C：全局幂等 key 替换 active-uniqueness（绝对唯一，不释放）

用 `(deviceID, certEpoch)` 做全生命周期唯一 key（命令到 terminal 仍保持唯一记录，只是 status 变化）。

**拒绝理由**：cert epoch advance 后需要允许新命令入队，全生命周期唯一 key 无法释放——除非 epoch 本身即 key，这与 D1 的"终态释放"等价（因为 epoch 不变意味着命令不变，epoch advance 意味着终态已发生），语义等价但实现复杂度更高（需要 epoch-keyed cleanup）。D1 直接用 partial index 语义更简洁。

---

## 4. AI-robust 评级矩阵

| 约束 | 载体 | 上游 | 下游 |
|------|------|------|------|
| PG partial unique index（`status IN (1,2,3)`） | `schema_guard.go` expectedIndexes + golden 字节锁；drift 即 CI 红 | **Hard**（DDL 定义字节锁） | **Hard**（partial index 由 PG 引擎强制，`ON CONFLICT DO NOTHING` 幂等 enqueue） |
| In-mem active-uniqueness 与 PG 一致（同 status 集） | `commandtest` 跨存储 conformance 套件：同一测试矩阵对 PG 实现 + in-mem 实现各跑一遍 | **Medium+**（conformance 可执行；两实现偏差 CI 红；不能 Hard 是因 Go 类型无法在接口层表达「两实现必须对 status 集一致」） | **Medium+**（同上） |
| `WithActiveUniqueness` 是 `IdempotencyKey` 唯一写入点（不安全组合不可表达） | `EnqueueOption` sealed 类型 + `EnqueueOptions.IdempotencyKey` unexported 字段 | **Hard**（Go type system：包外字面量构造 `EnqueueOptions{IdempotencyKey: x}` 编译失败） | **Hard**（`WithActiveUniqueness` 位置参 deadline 是 required，不可省略；无 deadline 的 active-uniqueness 在 API 上不可构造） |
| producer 无状态（`renewal_requested_at` 不存在） | `schema_guard.go` golden 锁 `devices` 列集（列存在即红） | **Hard**（schema guard golden 字节锁） | **Hard**（`schema_guard` 拦截引入列的 migration，不注册即红） |
| relay identity 注入（active-uniqueness key+deadline 进 ctx） | E2E acceptance test（`TestCertRenewalActiveUniquenessE2E`）：relay 收到 command entry → 下游 enqueue 带 active-uniqueness，第二次 enqueue 同 key 被幂等 noop，设备离线积压维持单条 | **Medium**（E2E 可执行 regression，行为可被测试抓住；不能 Hard 是因为 relay 路径跨异步 store 边界，Go 类型系统无法端到端表达「凡经 relay 的 command entry 必带 active-uniqueness」，与 ADR-1044 §4 COMMAND-ASYNC-EMIT-FUNNEL-01 同族结构天花板） | **Hard**（队列 partial index 在 PG 层强制，即使 relay 注入遗漏，重复 enqueue 也被 `ON CONFLICT DO NOTHING` 吸收；PG 是最终守门） |

**闭环论证**：PG partial index（Hard）+ in-mem conformance（Medium+）+ `WithActiveUniqueness` sealed API（Hard）= 三路结构保证"活跃唯一性"，relay identity 注入（Medium）+ E2E 测试（Medium）覆盖"正确 key 进入队列"路径。最终兜底永远在队列层（PG partial index），producer 侧状态（`renewal_requested_at`）不存在、relay 侧 Claimer 是优化——任何一路 Medium 失手，PG Hard 兜底。

---

## 5. 与 ADR-1044 dispatch funnel 的关系

ADR-1044 管辖 command-bus 的 dispatch funnel（codegen typed Handler/Register/DispatchAsync，relay 异步路径，Claimer 两阶段）。本 ADR 管辖 command **队列内**的 active-uniqueness 语义。两者正交：

| 关注点 | ADR | 载体 |
|--------|-----|------|
| 命令如何从 producer 经 outbox 到达消费者（dispatch funnel） | ADR-1044 | `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01` / `COMMAND-ASYNC-DISPATCH-CALLER-01` / `COMMAND-ASYNC-EMIT-FUNNEL-01` |
| 命令在队列内"活跃期间至多一条"（active-uniqueness） | **本 ADR** | PG partial unique index / in-mem conformance / `WithActiveUniqueness` sealed API |

ADR-1044 §4 评级矩阵的四行 invariant **不受本 ADR 影响**：本 ADR 在**队列写入层**增加了幂等保障，没有改变 dispatch funnel 的任何类型、接口或 callsite allowlist。

---

## 6. 对标（prior art）

| 框架 / 系统 | 机制 | 与本 ADR 对齐点 |
|------------|------|----------------|
| **River** `UniqueOpts.ByState` | 非终态（available/running/retryable）内按 key unique，终态释放 | D1 完全对齐（status 集 / 释放语义） |
| **Temporal** WorkflowId 唯一性 | 同一 WorkflowId 的新 execution 在旧 execution 非 CLOSED 时被 reject/buffer | D1 语义等价（非终态期间 at-most-one） |
| **client-go workqueue** key-coalescing | processing 中同 key 写入 dirty，不重入主队列 | D1 的 in-mem 形态（coalescing = 幂等 noop） |
| **cert-manager issue #4642** | observe-then-decide（查 in-flight CertificateRequest）有 TOCTOU 竞态，建议"队列拥有正确性" | 与备选 B 拒绝理由完全对齐；本 ADR 采纳"queue owns correctness" |

---

## 7. 威胁模型

### 7.1 旧威胁（时间窗方案）及新处置

| 威胁 | 旧方案处置 | 新方案处置 |
|------|-----------|-----------|
| 离线设备积压 N 条活跃命令 | 有界（≤ 30d/25h ≈ 28 条），但非零 | **消除**：PG partial index 保证至多 1 条 |
| 命令永久 Pending（设备离线 + Sweeper 未触发） | 无路径 | **消除**：`WithActiveUniqueness(deadline)` 必带 OverallDeadline → Sweeper expire → 终态 → key 释放 |
| 并发扫描绕过时间窗（多副本） | 部分（CAS on cert_epoch + 时间窗），有竞态 | **消除**：PG partial index 唯一性由 PG 引擎串行化，并发 `ON CONFLICT DO NOTHING` 安全 |
| Claimer done-TTL 过期后重发积压 | 依赖 retryInterval ≥ TTL 协调（Soft doc 约束） | **移除该协调约束**：Claimer 降为 dispatch 优化，queue uniqueness 不依赖 TTL |

### 7.2 新威胁（本 ADR 引入）及处置

| 威胁 | 描述 | 处置 |
|------|------|------|
| active-uniqueness key 被 deadline-less 命令永久持有 | key 持有者无 OverallDeadline，Sweeper 永不 expire，key 永不释放 | **不可表达**（D2 `WithActiveUniqueness` sealed API，无 deadline 的 active-uniqueness 构造不出来） |
| PG partial index 与 in-mem 实现 status 集不一致 | 例如 in-mem 认为 Delivered 不是活跃态，PG 认为是（或反之） | `commandtest` conformance 套件强制两实现通过同一测试矩阵（Medium+，见 §4） |
| schema_guard 未覆盖 partial index 的 WHERE 谓词 | DDL drift：index 存在但 `WHERE` 条件变化 | `schema_guard.go` 冻结 index 定义完整字符串（含 `WHERE` 谓词），字节 diff 即红（Hard） |
| producer 直接写 `IdempotencyKey`（绕过 `WithActiveUniqueness`） | 通过其他路径设置 `IdempotencyKey`，但 `OverallDeadline` 为零 | `EnqueueOptions.IdempotencyKey` 是 unexported 字段（Hard，Go 可见性） |

### 7.3 继承威胁（保持不变）

- **audit-identity downgrade**：由 ADR-1821 `installSystemProducerIdentity`（`kernel/reconcile/identity.go` unexported + CTXKEYS-PRINCIPAL-WRITE-CALLER-01 allowlist）处置，本 ADR 不展宽该面。
- **dispatch funnel bypass**：由 ADR-1044 §4 的 COMMAND-ASYNC-EMIT-FUNNEL-01（Medium 上游 + Hard 下游）处置，本 ADR 不修改。
- **残余双执行窗口**：由 ADR-661 §4.3 FencedRepository + consumer 幂等契约（§4.4）处置，active-uniqueness 是额外纵深，不替代 fencing。

---

## 8. Enforcement 索引

| 载体 | ID / 名 | 文件 |
|------|---------|------|
| PG schema guard | `device_commands_active_idempotency_key_uidx`（partial index DDL 字节锁） | `adapters/postgres/schema_guard.go`（expectedIndexes） |
| commandtest conformance | `ActiveUniqueness/EnqueueConflict`、`ActiveUniqueness/TerminalReleasesKey`、`ActiveUniqueness/StatusSetConsistency` | `kernel/command/commandtest/conformance_active_uniqueness_test.go` |
| sealed API | `kernel/command.EnqueueOptions` unexported `idempotencyKey` 字段 + `WithActiveUniqueness` 构造唯一入口 | `kernel/command/options.go` |
| schema guard（无 renewal 侧表列） | `devices` 列集 golden（`renewal_requested_at` / `renewal_requested_epoch` 不存在） | `adapters/postgres/schema_guard.go`（expectedColumns for devices） |
| E2E acceptance | `TestCertRenewalActiveUniquenessE2E`（单条活跃、离线积压不增长） | `examples/iotdevice/cert_renewal_e2e_test.go` |
