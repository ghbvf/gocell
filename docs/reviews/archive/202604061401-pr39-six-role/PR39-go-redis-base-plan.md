# PR39 Redis 底座决策与最小修复清单

> 日期: 2026-04-06
> 目标 PR: `ghbvf/gocell#39`
> 范围: `src/adapters/redis/*`
> 结论: 现阶段继续以 `go-redis` 作为 gocell 的 Redis 底座，保持 adapter 薄封装；当前 PR 以最小修复为主，不建议借机重构为新的锁模型或切换到 `rueidis`。

---

## 1. 执行结论

结合当前代码、PR 39 的改动、以及后续 `windowsmdm` 面向 15 万量级设备的发展方向，建议如下：

1. `gocell` 现阶段继续使用 `go-redis` 作为 Redis client 底座。
2. `adapters/redis` 保持薄封装，不在基础 `Client` 上混入额外语义。
3. `Cache` 与 `IdempotencyChecker` 可继续保留，但应尽量直接映射到 Redis/`go-redis` 的稳定语义。
4. `DistLock` 只能明确定位为 **efficiency lock**，不能在现有 API 上偷偷升级为 correctness lock。
5. correctness-critical 场景必须依赖下游 CAS/条件写，或后续引入共识系统；不能仅凭 Redis 锁名或库名推断安全性。

本次 PR 不建议做“换库”或“大重构”。更合适的策略是先修复当前引入的兼容性与语义回归，再分阶段收缩 adapter。

---

## 2. 为什么此时不切到 `rueidis`

`rueidis` 已经是成熟可生产的项目，但当前更适合被视为“高性能进阶选项”，而不是 `gocell` 现阶段的默认底座：

1. `go-redis` 仍是 Go 生态中最主流、文档最完整、团队认知成本最低的 Redis client。
2. `windowsmdm` 面向 15 万设备时，先遇到的核心问题更可能是 key 设计、热点分布、幂等、背压、重试和观测性，而不是 Redis client 单点吞吐。
3. PR 39 当前暴露的问题，本质上是 adapter 语义设计问题，而不是底层 client 选择问题。
4. 如果后续压测证明瓶颈确实位于 Redis client 往返成本、auto pipelining 或 client-side caching，再单独评估 `rueidis` 更合理。

因此，当前阶段的最优动作不是换库，而是把现有 adapter 收薄、收稳。

---

## 3. Redis Adapter 重构计划

### Phase 0: 当前 PR 边界

目标：只修复 PR 39 引入或暴露的兼容性/语义问题，不扩大范围。

原则：

1. 不重写 `DistLock` 为新模型。
2. 不在本 PR 中引入新的第三方锁库。
3. 不借机替换 `go-redis`。

### Phase 1: 薄封装收缩

目标：把 `adapters/redis` 收敛为围绕 `go-redis` 的稳定薄壳。

建议动作：

1. 在 `Client` 上保留最核心的职责：`NewClient`、`Health`、`Close`、`Config`。
2. 新增 `Raw()` 之类的 escape hatch，让后续高性能路径能直接拿到底层 client。
3. `Config()` 始终保持无损拷贝，不承担日志脱敏职责。
4. 如需安全展示，新增 `RedactedConfig()`、`String()` 或 `LogValue()`，与可复用配置分离。
5. `Config` 字段尽量一一映射到 `go-redis` 的 `Options` / `FailoverOptions`。
6. 校验仅覆盖明显无效输入，不随意改变对外契约。

### Phase 2: 高层 helper 明确分层

目标：区分“缓存/幂等/协调”三类能力，避免基础 client 被业务语义污染。

建议动作：

1. `Cache` 继续作为轻量 helper，直接映射 Redis Get/Set/Del 语义。
2. `IdempotencyChecker` 明确为 Redis SET NX + TTL 方案，强调其适用边界。
3. `DistLock` 文档中显式声明仅用于减少重复工作或削峰，不承诺 correctness。
4. 若未来需要更强协调语义，应新建单独层级，而不是继续堆在 `adapters/redis/client.go` 上。

### Phase 3: correctness 方案独立设计

目标：将真正的 correctness 保障与 Redis 锁解耦。

建议动作：

1. 对关键写路径引入下游条件写/CAS/版本号校验。
2. 如果继续保留 fencing token，只把它当作“配合下游条件写的辅助机制”。
3. 不将 Redis 锁本身描述为 correctness 的最终来源。
4. 如后续确需锁服务本身承担 correctness，再单独评估 etcd / ZooKeeper 一类共识系统。

---

## 4. 当前 PR 最小修复清单

以下清单按“最小改动恢复语义稳定”的原则排序。

### 4.1 必修项

1. `src/adapters/redis/distlock.go`
   将 `renewCtx` 改回独立生命周期，不再直接继承 `Acquire(ctx, ...)` 的调用方 `ctx`。
   原因：调用方常用 timeout ctx 仅限制 acquire 耗时，不能隐式决定锁续租生命周期。

2. `src/adapters/redis/distlock.go`
   保留 `done` channel 解决 goroutine 泄漏，但 `Release(ctx)` 等待 `done` 时必须对 `ctx.Done()` 友好，避免无界阻塞。

3. `src/adapters/redis/distlock_test.go`
   增加回归测试：`Acquire` 使用超时 ctx 成功后，即使该 ctx 到期，只要未显式 `Release`，续租不应立即停止。

4. `src/adapters/redis/client.go`
   恢复 `Config()` 的无损返回语义。
   若需要脱敏展示，新增 `RedactedConfig()` 或同等接口，不要复用 `Config()`。

5. `src/adapters/redis/client_test.go`
   调整 `Config()` 测试回到 round-trip 语义。
   若新增 `RedactedConfig()`，为其单独补测试。

6. `src/adapters/redis/client.go`
   保持当前对外错误码字符串兼容，继续发出 `ERR_ADAPTER_REDIS_LOCK_ACQUIRED`。
   若未来要修拼写，应作为显式 breaking change 处理，并同步规格与迁移说明。

7. `src/adapters/redis/client.go`
   为 `ModeSentinel` 补 `SentinelMaster` 非空校验。

8. `src/adapters/redis/client_test.go`
   补充 Sentinel 无效配置测试，覆盖：
   - `SentinelAddrs` 为空
   - `SentinelMaster` 为空

9. `src/adapters/redis/doc.go` 与 `src/adapters/redis/distlock.go`
   调整示例和说明，避免继续鼓励 `defer lock.Release(ctx)` 直接复用 request ctx。
   更合适的文档表达应是使用新的、有界 cleanup ctx。

### 4.2 本 PR 可接受保留项

1. `src/adapters/redis/client.go`
   standalone 空 `Addr` 不再默认回落到 `localhost:6379`，改为直接报错。
   该项已按“Pre-1.0 有意加固”接受，不再视为当前阻塞问题。
   建议仅补一句迁移说明。

### 4.3 本 PR 不建议扩展处理项

以下内容值得记录，但不建议在当前 PR 内继续扩展：

1. 是否引入 `redislock` / `redsync`
2. 是否切换到底层 `rueidis`
3. 是否把 `DistLock` 重做成 correctness lock
4. 是否为 fencing counter 设计更复杂的生命周期管理

这些都更适合后续单独设计和压测验证。

---

## 5. windowsmdm 视角下的后续落点

面向 15 万量级设备时，优先级建议如下：

1. 先把 Redis 用作状态缓存、幂等键、速率控制和短期协调状态存储。
2. 关键业务写入必须落到可校验版本的持久层。
3. Redis 相关优化优先做 key 设计、TTL 策略、热点拆分、消费并发和观测性。
4. 只有在压测证明 Redis client 本身成为瓶颈时，再评估是否切换到底层 `rueidis`。

---

## 6. 推荐落地顺序

建议按以下顺序推进：

1. 合并当前 PR 的最小修复项。
2. 追加一轮 Redis adapter 契约测试，固定 `Config()`、错误码、Sentinel 校验和 `DistLock` 续租边界。
3. 为 windowsmdm 单独设计 Redis key 空间、TTL 和热点治理方案。
4. 在真实负载压测后，再决定是否需要 `rueidis` 或更激进的协调层重构。

---

## 7. 最终建议

当前阶段的正确方向不是“再发明 Redis 语义”，而是：

1. 用 `go-redis` 做稳定底座；
2. 让 `adapters/redis` 保持薄；
3. 把 correctness 保证留给下游条件写或未来单独的协调层设计。

这条路线对 `gocell` 当前演进速度、PR 风险控制、以及后续 `windowsmdm` 的可维护性都更稳妥。
