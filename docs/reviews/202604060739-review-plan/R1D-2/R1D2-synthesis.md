# R1D-2 综合裁决: adapters/redis

## 模块概况

| 指标 | 值 |
|------|-----|
| 生产代码 | 540 LOC (5 files) |
| 测试代码 | 889 LOC (6 files) |
| 实现接口 | `kernel/idempotency.Checker` |
| 组件 | Client, Cache, DistLock, IdempotencyChecker |

## 六角色审查结果

| 角色 | Verdict | P0 | P1 | P2 | P3 |
|------|---------|----|----|----|----|
| Architect | PASS_WITH_CONDITIONS | 1 | 3 | 4 | 0 |
| Kernel Guardian | **FAIL** | 1 | 2 | 5 | 0 |
| Security | PASS_WITH_CONDITIONS | 1 | 2 | 4 | 0 |
| Correctness | PASS_WITH_CONDITIONS | 0 | 2 | 9 | 1 |
| Product Manager | PASS_WITH_CONDITIONS | 0 | 1 | 4 | 9 |
| DevOps | PASS_WITH_CONDITIONS | 0 | 1 | 5 | 4 |

**综合 Verdict: FAIL** — Kernel Guardian FAIL 阻塞，P0 必须修复。

---

## P0 Findings (必须修复)

### P0-01: DistLock 缺少 Fencing Token（6 角色共识）

- **角色确认**: Architect (A-06), Kernel Guardian (KG-03), Security (S06)
- **交叉引用**: 确认 P0-F11S01
- **文件**: `distlock.go:96-133`
- **问题**: DistLock.Acquire() 返回的 Lock 只有 random ownership token 用于 Release 验证。没有 fencing token 供下游存储拒绝过期持有者的写入。
- **场景**: Lock A 持有锁 → TTL 过期 → Lock B 获得锁 → Lock A（不知道已过期）继续写入 → 数据覆盖 Lock B 的写入
- **影响**: L3 WorkflowEventual / L4 DeviceLatent 一致性级别不安全
- **修复方案**:
  1. Acquire 返回 monotonic fencing token（Redis INCR 或 EVAL 原子递增）
  2. Lock 结构体暴露 `FenceToken() int64` 方法
  3. 下游存储（如 postgres adapter）在写入时校验 fence token ≥ last seen
  4. 如果机械传播 >10 文件，考虑方案 B: 文档标注 DistLock 仅适用于 L1 idempotent 场景

---

## P1 Findings（Fix Pack B）

### P1-01: renewLoop 使用 context.Background() — goroutine 泄漏

- **角色确认**: Architect (A-03), Correctness (CR-01), DevOps (D-01)
- **交叉引用**: 确认 P1-J7
- **文件**: `distlock.go:117`
- **问题**: `context.WithCancel(context.Background())` 脱离调用方上下文。如果 Release() 未被调用（进程崩溃、panic），renewLoop goroutine 永远运行。Client.Close() 也不会停止它。
- **修复**: Acquire 接受 caller ctx，renewCtx 派生自 caller ctx（双 cancel: caller cancel OR Release cancel）

### P1-02: TTL=0 创建永久幂等键 — 内存泄漏

- **角色确认**: Kernel Guardian (KG-01), Correctness (CR-08)
- **交叉引用**: 确认 P1-K10
- **文件**: `idempotency.go:52-53, 65-66`
- **问题**: `MarkProcessed(ctx, key, 0)` 和 `TryProcess(ctx, key, 0)` 调用 `SetNX(..., 0)` — Redis 中 TTL=0 表示永不过期。
- **修复**: 如果 `ttl <= 0`，使用 `idempotency.DefaultTTL (24h)` 或返回错误

### P1-03: Cache/DistLock 缺少 kernel 层接口定义

- **角色确认**: Architect (A-01), Kernel Guardian (隐含)
- **交叉引用**: 确认 P1-J6
- **问题**: IdempotencyChecker 有 kernel 接口，但 Cache 和 DistLock 没有。消费方必须 import 具体适配器。
- **修复**: 在 `kernel/` 下定义 `cache.Cache` 和 `distlock.Locker` 接口

### P1-04: 连接池参数不可配置

- **角色确认**: Architect (A-04), DevOps (D-03)
- **交叉引用**: 确认 P1-M9
- **文件**: `client.go:33-63`
- **问题**: Config 未暴露 PoolSize, MinIdleConns, MaxRetries 等 go-redis 连接池参数。
- **修复**: Config 添加 PoolSize, MinIdleConns, MaxRetries 字段

### P1-05: Config.Password 可通过 Config() 方法泄漏

- **角色确认**: Security (S01)
- **文件**: `client.go:185-187`
- **问题**: `Client.Config()` 返回完整 Config 副本含明文 Password。任何调用方 log/serialize 即泄漏。
- **修复**: Password 字段实现 `fmt.Stringer` 返回 `"***"` 或 Config() 返回时清空 Password

### P1-06: localhost:6379 默认地址不安全

- **角色确认**: Security (S05)
- **交叉引用**: 确认 P1-K9
- **文件**: `client.go:82-84`
- **问题**: 生产环境忘记配置 Addr 时静默连接 localhost。
- **修复**: Addr 为空时返回配置错误，不默认 fallback

### P1-07: 错误码名/值不匹配

- **角色确认**: Correctness (CR-04), Product (PM-03), Kernel Guardian (KG-04)
- **文件**: `client.go:16`
- **问题**: 常量名 `ErrAdapterRedisLockAcquire` 值为 `"ERR_ADAPTER_REDIS_LOCK_ACQUIRED"`（ACQUIRE vs ACQUIRED）

---

## P2 Findings 汇总（Fix Pack D）

| ID | 问题 | 文件 | 角色 |
|----|------|------|------|
| P2-01 | Release 使用 acquire 错误码 | distlock.go:54 | CR, PM, KG |
| P2-02 | Delete 使用 Set 错误码 | cache.go:58 | CR, PM, KG |
| P2-03 | renewLoop 失败无重试/无通知 | distlock.go:149 | CR, DevOps |
| P2-04 | Cache.Get 无法区分缺失 vs 空值 | cache.go:31 | CR, PM |
| P2-05 | GetJSON/SetJSON 不是方法（发现性差） | cache.go:65,85 | PM |
| P2-06 | 错误消息含完整 key（可能敏感） | 多处 | Security |
| P2-07 | 无 key namespace/prefix 强制 | 全模块 | Security |
| P2-08 | 负 TTL 未校验 | 多处 | Security |
| P2-09 | Sentinel 模式日志 addr 为空 | client.go:146 | DevOps |
| P2-10 | MarkProcessed 丢弃 SetNX boolean | idempotency.go:53 | KG, CR |
| P2-11 | 零 metrics/instrumentation | 全模块 | DevOps |
| P2-12 | cmdable 泄漏 go-redis 类型 | client.go:89-97 | Architect |
| P2-13 | 无 Cluster 模式支持 | Config | Architect |
| P2-14 | Lock 返回具体类型非接口 | distlock.go:96 | PM |
| P2-15 | Health 无独立超时 | client.go:161 | DevOps |
| P2-16 | 缺 renewLoop/TTL/并发测试 | test files | CR |

---

## 交叉引用台账

| 原始 Finding | 当前状态 | 验证依据 |
|-------------|---------|----------|
| P0-F11S01 (DistLock fencing) | **CONFIRMED** | 6 角色共识: distlock.go 无 fencing token API |
| P1-J6 (Cache/DistLock 缺接口) | **CONFIRMED** | kernel/ 下无 cache/distlock 接口定义 |
| P1-J7 (renewLoop goroutine leak) | **CONFIRMED** | distlock.go:117 context.Background() |
| P1-K9 (localhost:6379 默认) | **CONFIRMED** | client.go:82-84 |
| P1-K10 (TTL=0 永久键) | **CONFIRMED** | idempotency.go SetNX with 0 TTL |
| P1-L7 (缺 TTL/并发测试) | **CONFIRMED** | 无 renewLoop 测试, 无并发测试 |
| P1-M3 (TOCTOU race) | **PARTIAL** | TryProcess 已添加; 但 IsProcessed+MarkProcessed 仍暴露 |
| P1-M9 (连接池不可配) | **CONFIRMED** | Config 无 pool 相关字段 |

---

## P0 Fix 优先级

按 review plan 要求，R1D Fix 阶段只修 P0:

1. **P0-01 DistLock Fencing Token** — 评估传播面后决定修复方案
   - 方案 A: 添加 fencing token（INCR + 暴露 API）— 传播面需评估
   - 方案 B: 文档限制 DistLock 适用范围（仅 idempotent 操作）— 零传播

P1-07 (错误码名值不匹配) 虽然是 P1，但修复成本极低（改一个字符串），建议顺带修。

---

## 详细报告索引

- [Architect Review](R1D2-architect.md)
- [Kernel Guardian Review](R1D2-kernel-guardian.md)
- [Security Review](R1D2-security.md)
- [Correctness Review](R1D2-correctness.md)
- [Product Manager Review](R1D2-product.md)
- [DevOps Review](R1D2-devops.md)
