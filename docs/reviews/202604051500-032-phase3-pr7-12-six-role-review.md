# Phase 3 六角色审查报告: PR #7 ~ #12

> **审查日期**: 2026-04-05
> **审查基线**: develop 分支 commit `224ee5c`
> **审查方式**: 6 并行 reviewer agent, 每 PR 独立 6 席位 (架构/安全/测试/运维/DX/产品)
> **范围**: GitHub PR #7~#12 (Phase 3 Adapters Wave 0 + Wave 1), 56 文件

---

## Executive Summary

| PR | 标题 | Findings | P0 | P1 | P2 | Signoff |
|----|-------|----------|----|----|-----|---------|
| #7 | bootstrap interface injection + outbox doc | 10 | 1 | 5 | 4 | BLOCKED |
| #8 | crypto/rand UUID + seed script | 10 | 1 | 5 | 4 | BLOCKED |
| #9 | Docker Compose + Makefile + healthcheck | 17 | 2 | 8 | 7 | BLOCKED |
| #10 | PostgreSQL adapter (Pool/TxManager/Migrator) | 14 | 2 | 7 | 5 | BLOCKED |
| #11 | Redis adapter (Client/DistLock/Idempotency/Cache) | 12 | 2 | 5 | 5 | BLOCKED |
| #12 | RabbitMQ adapter (Publisher/Subscriber/ConsumerBase) | 12 | 2 | 6 | 4 | BLOCKED |
| **合计** | | **75** | **10** | **36** | **29** | **ALL BLOCKED** |

---

## 跨 PR 共识 (6 个系统性问题)

以下模式在多个 PR 中重复出现，建议作为统一修复批次处理：

### 1. 安全熵/凭证处理不当 (PR #8, #9, #12)
- **F-8S-01 [P0]**: `uid.New()` 静默丢弃 `crypto/rand.Read` 错误, 熵源不可用时生成固定 UUID
- **F-9S-01 [P0]**: `.env.example` 弱密码可直接使用, 无 CHANGE_ME 占位
- **F-12S-01 [P0]**: `sanitizeURL` 固定截断可泄露 AMQP 凭据片段

### 2. adapters/ 缺少 kernel/runtime 接口定义 (PR #10, #11)
- **F-10A-03 [P1]**: Pool 未实现任何预定义接口, Health() 签名不兼容
- **F-11A-01 [P1]**: Cache 和 DistLock 无对应 kernel/runtime 接口, 形成悬空实现

### 3. 测试覆盖率系统性不足 (PR #7, #10, #11, #12)
- **F-7T-01 [P0]**: bootstrap `Run()` 覆盖率仅 42.7%, 远低于 80% 门槛
- **F-11T-01 [P0]**: Redis adapter 完全缺失集成测试
- **F-10T-02/03**: TxManager 顶层 tx 路径、Migrator Up/Down/Status 零覆盖
- **F-12T-01/02**: 重连逻辑、并发消费场景零覆盖

### 4. L2 OutboxFact 一致性保证缺口 (PR #8, #10, #12)
- **F-8P-01 [P1]**: 4 个 Cell service 的 Publish 失败被 fire-and-forget, API 返回成功
- **F-8S-03 [P1]**: 事件 payload 缺少 `event_id`, 违反 eventbus.md 幂等键规范
- **F-10P-01 [P1]**: outbox_entries 表缺少 `attempt_count`/`last_error`, 无法追踪失败 entry

### 5. 可观测性缺失 (PR #11, #12)
- **F-11O-02 [P2]**: Redis adapter 无任何 metrics 埋点
- **F-12O-01 [P1]**: RabbitMQ adapter DLQ 计数仅有日志, 违反 eventbus.md "计数指标" 要求

### 6. 幂等检查 TOCTOU 竞态 (PR #11 + #12 交叉)
- **F-11P-01 [P1]**: `IsProcessed` + `MarkProcessed` 两步分离破坏原子性, 应改为 `CheckAndMark` 原子操作

---

## 全部 P0 Findings (10 条, 按修复优先级排序)

### P0-1. F-8S-01 — uid.New() 静默丢弃 crypto/rand 错误
- **文件**: `src/pkg/uid/uid.go:17`
- **影响**: 熵源不可用时生成固定 UUID `00000000-0000-4000-8000-000000000000`, session ID / audit ID 完全可预测
- **修复**: `if _, err := rand.Read(b); err != nil { panic("uid: crypto/rand unavailable") }`

### P0-2. F-10A-01 — Migrator 声称 advisory lock 但实现缺失
- **文件**: `src/adapters/postgres/migrator.go:39-40`
- **影响**: 多实例并发启动时重复执行 migration DDL, 可致数据损坏
- **修复**: 在 Up/Down 执行前后用 `pg_advisory_lock/unlock(hashtext('gocell_migration'))` 包裹

### P0-3. F-10S-01 — tableName 直接 fmt.Sprintf 插入 SQL, SQL 注入向量
- **文件**: `src/adapters/postgres/migrator.go:68,224,247,271,306,345` (6 处)
- **影响**: 若 tableName 来自外部配置, 可注入任意 SQL
- **修复**: `NewMigrator` 中白名单校验 `^[a-zA-Z_][a-zA-Z0-9_]*$`, 或用 `pgx.Identifier.Sanitize()`

### P0-4. F-11S-01 — DistLock 无 fencing token, 锁过期后并发写入不可防
- **文件**: `src/adapters/redis/distlock.go:96-133`
- **影响**: GC/网络停顿超过 TTL 后两个持锁方并发执行业务逻辑
- **修复**: godoc 明确标注安全边界 ("不适用于强一致性互斥"), 将 "Redlock 风格" 更正为 "single-node Redis lock"

### P0-5. F-12S-01 — sanitizeURL 固定截断泄露 AMQP 凭据
- **文件**: `src/adapters/rabbitmq/connection.go:373-380`
- **影响**: 短 username 时密码出现在日志中
- **修复**: 改用 `net/url.Parse` + `url.UserPassword(username, "***")`

### P0-6. F-12D-01 — ctx 取消时 ConsumerBase 返回 error 触发 NACK+requeue, 关闭时重复消费
- **文件**: `src/adapters/rabbitmq/consumer_base.go:160-165`
- **影响**: 程序关闭时已执行副作用的消息被 requeue, 幂等保护失效
- **修复**: ctx 取消时走 DLQ 路径并返回 nil (ACK), 避免 requeue 循环

### P0-7. F-9S-01 — .env.example 弱密码可直接使用
- **文件**: `.env.example:2,13,17,18`
- **影响**: `cp .env.example .env` 后弱凭证直接生效, DSN 含完整密码
- **修复**: 密码字段替换为 `CHANGE_ME` 占位符, 文件头添加警告注释

### P0-8. F-9S-02 — JWT 密钥字段为空, 运行时可能静默失效
- **文件**: `.env.example:23-24`
- **影响**: 空 key 若有 noop fallback, JWT 签发/验证完全失效
- **修复**: runtime/auth 层强制非空校验, .env.example 提供生成命令

### P0-9. F-7T-01 — bootstrap Run() 覆盖率 42.7%, 违反 >=80% 约束
- **文件**: `src/runtime/bootstrap/bootstrap_test.go`
- **影响**: io.Closer teardown、EventRegistrar 分支、graceful shutdown 等关键路径未测试
- **修复**: 至少补充 4 个测试 (listener shutdown / EventRegistrar / dual closer / worker rollback)

### P0-10. F-11T-01 — Redis adapter 完全缺失集成测试
- **文件**: `src/adapters/redis/` (无 *_integration_test.go)
- **影响**: TTL 过期、Sentinel 故障转移、锁原子性等关键场景从未真实验证
- **修复**: 创建 `redis_integration_test.go`, 使用 testcontainers-go 启动真实 Redis

---

## P1 Findings 索引 (36 条)

### 架构 (7 条)
| ID | PR | 文件 | 问题 |
|----|----|------|------|
| F-7A-01 | #7 | `bootstrap.go:28` | 直接 import `runtime/eventbus` 具体类型 |
| F-8A-01 | #8 | `websocket/hub.go:12` | 遗留 import 已废弃 `pkg/id` |
| F-8A-02 | #8 | configwrite/configpublish/configsubscribe | `TopicConfigChanged` 3 处重复定义, 应抽常量 |
| F-10A-02 | #10 | `tx_manager.go:104` | commit 失败用 `ErrAdapterPGConnect` 错误码, 语义错误 |
| F-10A-03 | #10 | `pool.go:87-159` | Pool 未实现任何 kernel/runtime 接口 |
| F-11A-01 | #11 | `cache.go:14`, `distlock.go:65` | Cache/DistLock 缺少接口定义 |
| F-11A-02 | #11 | `distlock.go:117` | renewLoop 用 `context.Background()`, goroutine 泄漏风险 |

### 安全 (10 条)
| ID | PR | 文件 | 问题 |
|----|----|------|------|
| F-7S-01 | #7 | `bootstrap.go:226` | `any(pub) != any(sub)` 接口相等判断存在 double-close 绕过 |
| F-7S-02 | #7 | `bootstrap.go:334-338` | Worker 失败 rollback 时 workerCancel() 未先行调用 |
| F-8S-02 | #8 | `sessionrefresh/service.go:110-122` | 旧 session 删除失败继续创建新 session, 双 session 并存 |
| F-8S-03 | #8 | `sessionlogin/service.go:132-134` | 事件 payload 缺少 event_id |
| F-9S-03 | #9 | `docker-compose.yml:34-35` | RabbitMQ Management UI 15672 绑定 0.0.0.0 |
| F-9S-04 | #9 | `docker-compose.yml:43` | `minio/minio:latest` 浮动 tag |
| F-10S-02 | #10 | `pool.go:104` | DSN 解析失败可能回显部分凭证 |
| F-10S-03 | #10 | `migrator.go:275` | `err.Error() == "no rows"` 字符串比较, 应用 `pgx.ErrNoRows` |
| F-11S-02 | #11 | `client.go:82-84` | Config.Addr 默认 localhost:6379, 违反无 localhost 回退约束 |
| F-11S-03 | #11 | `idempotency.go:52-58` | TTL=0 时幂等 key 永不过期, 内存泄漏 |
| F-12S-02 | #12 | `connection.go:26-45` | 无 TLS Config 扩展点 |
| F-12S-03 | #12 | `publisher.go:50-55` | 消息无 MessageId, 追踪链路断裂 |

### 测试 (9 条)
| ID | PR | 文件 | 问题 |
|----|----|------|------|
| F-7T-02 | #7 | `outbox_test.go` | 纯接口包无行为覆盖 |
| F-7T-03 | #7 | `bootstrap_test.go:151-153` | `_ = err` 掩盖所有断言 |
| F-8T-01 | #8 | `uid_test.go` | rand.Read 错误注入测试缺失 |
| F-8T-02 | #8 | `sessionrefresh/service_test.go` | rotation 后旧 token 不可再用未验证 |
| F-9T-01 | #9 | `healthcheck-verify.sh` | 仅验证进程存活, 无连通性探针 |
| F-9T-02 | #9 | `Makefile` | 无 CI pipeline, 测试门控全靠人工 |
| F-9T-03 | #9 | `Makefile:43-46` | test-integration 失败后容器不清理 |
| F-10T-02 | #10 | `tx_manager_test.go` | 顶层事务路径零覆盖 |
| F-10T-03 | #10 | `migrator_test.go` | Up/Down/Status 零覆盖 |
| F-11T-02 | #11 | cache/distlock/idempotency tests | TTL 过期、并发竞态测试缺失 |
| F-12T-01 | #12 | `rabbitmq_test.go` | 重连逻辑零覆盖 |
| F-12T-02 | #12 | `rabbitmq_test.go` | 并发消费场景零覆盖 |

### 运维 (3 条)
| ID | PR | 文件 | 问题 |
|----|----|------|------|
| F-9A-01 | #9 | `docker-compose.yml:18-25` | Redis 无持久化卷 |
| F-9A-02 | #9 | `docker-compose.yml:28-40` | RabbitMQ 无持久化卷 |
| F-9A-03 | #9 | `docker-compose.yml:54` | MinIO healthcheck 用不存在的 `mc` 命令 |
| F-9O-01 | #9 | `docker-compose.yml` | 四服务无 CPU/内存资源限制 |
| F-11O-01 | #11 | `client.go:32-63` | Client 缺连接池配置暴露 |
| F-12O-01 | #12 | `connection.go`, `consumer_base.go` | 无 metrics 埋点, DLQ 仅日志 |

### DX (3 条)
| ID | PR | 文件 | 问题 |
|----|----|------|------|
| F-9D-01 | #9 | `Makefile:43-46` | `cd src &&` 违反 CLAUDE.md Bash 规范 |
| F-10D-01 | #10 | `errors.go:21` | `ErrAdapterPGNoTx` 缺 `PG_` 中缀 |
| F-11D-01 | #11 | `cache.go:57` | Delete 用 `ErrAdapterRedisSet` 错误码 |

### 产品 (4 条)
| ID | PR | 文件 | 问题 |
|----|----|------|------|
| F-7P-01 | #7 | `bootstrap.go:265-272` | 半配置时 EventRegistrar 静默失效 |
| F-8P-01 | #8 | identitymanage/sessionlogin/configwrite/configpublish | Publish 失败 fire-and-forget, L2 违规 |
| F-10P-01 | #10 | `001_create_outbox_entries.up.sql` | 缺 attempt_count/last_error 字段 |
| F-11P-01 | #11 | `idempotency.go:37-47` | IsProcessed + MarkProcessed TOCTOU 竞态 |
| F-12A-01 | #12 | `subscriber.go:161-163` | processDelivery 串行, PrefetchCount 形同虚设 |

---

## P2 Findings 索引 (29 条, 按 PR 分组)

<details>
<summary>展开 P2 完整列表</summary>

### PR #7 (4 条)
- F-7A-02: CoreAssembly 具体类型注入
- F-7O-01: config watcher Warn 日志缺结构化字段
- F-7O-02: shutdown 只保留 firstErr
- F-7D-01/02: 半配置无文档, doc.go 示例过时

### PR #8 (4 条)
- F-8A-03: issueToken/TokenPair 两个 Slice 重复
- F-8O-01: seed-test-data.sh 空壳脚本
- F-8D-01: pkg/id 与 pkg/uid 并存, 迁移路径不清
- F-8D-02: configpublish Rollback 缺 targetVersion<=0 校验
- F-8T-03: auditappend 无并发写入测试

### PR #9 (7 条)
- F-9A-04: 无 networks 声明
- F-9T-04: healthcheck TIMEOUT=30 过短
- F-9O-02: 无 restart 策略
- F-9O-03: 无日志大小限制
- F-9O-04: 无 make reset target
- F-9D-02: 无 make help
- F-9D-03: .env.example 缺 OIDC 配置节
- F-9P-01: compose 仅基础设施, 无 app 服务

### PR #10 (5 条)
- F-10A-04: QueryBuilder AppendParam 设计误导
- F-10T-04: outbox_relay cleanupLoop 未测试
- F-10O-02: Pool 缺 MinConns/HealthCheckPeriod
- F-10O-03: Pool.Stats() 返回 string
- F-10D-02: NewMigrator 无 Option 模式
- F-10P-02: outbox_entries id 语义歧义

### PR #11 (5 条)
- F-11A-03: go.mod 声明 go 1.25.0 (未发布)
- F-11T-03: renewLoop goroutine 泄漏测试缺失
- F-11O-02: 无运维指标
- F-11D-02: ErrAdapterRedisLockAcquire 命名/值不一致
- F-11D-03: GetJSON 自由函数 vs Cache 方法, API 不一致
- F-11P-02: Cache 无一致性等级标注

### PR #12 (4 条)
- F-12A-02: adapters/ 依赖 kernel/outbox 合规 (确认无违规)
- F-12T-03: DLQ 发布失败路径无测试
- F-12O-02: Publisher 每次 Publish 重复 ExchangeDeclare+Confirm
- F-12D-02: 两层 DLQ 路径语义不一致
- F-12P-01: consumer 注释格式不符 eventbus.md
- F-12P-02: doc.go 缺 L2 OutboxFact 端到端示例

</details>

---

## 亮点

跨 PR 值得肯定的设计决策：

1. **outbox Publisher/Subscriber 接口隔离** (PR #7) — 从具体 InMemoryEventBus 到接口注入的重构方向正确, 使 bootstrap 可接入任意适配器
2. **TxManager savepoint 嵌套** (PR #10) — context 传递深度计数, sp_N 命名清晰, panic recovery+re-panic 正确
3. **FOR UPDATE SKIP LOCKED** (PR #10) — outbox relay 多实例安全, 无重复处理
4. **Lua 脚本原子锁释放** (PR #11) — GET-DEL 原子性, 正确防止释放他人锁
5. **ConsumerBase 三要素集成** (PR #12) — 幂等/退避/DLQ 内置, 通过接口解耦 Redis
6. **Publisher confirm mode** (PR #12) — ch.Confirm + NotifyPublish + DeliveryMode.Persistent 三重保障
7. **全包 errcode/slog 合规** (PR #8, #10, #11, #12) — 无裸 errors.New, 无 fmt.Println

---

## 修复优先级建议

### 第一批 (阻塞性安全+正确性, 建议立即修复)
1. F-8S-01: uid.New() panic on rand failure
2. F-10S-01: migrator tableName 白名单校验
3. F-10A-01: migrator advisory lock
4. F-12S-01: sanitizeURL 用 net/url.Parse
5. F-12D-01: ctx 取消时 DLQ 而非 requeue
6. F-9S-01 + F-9S-02: .env.example CHANGE_ME + JWT 非空校验

### 第二批 (P0 测试 + P1 安全)
7. F-7T-01: bootstrap Run() 补测试到 >=80%
8. F-11T-01: Redis 集成测试
9. F-11S-01: DistLock godoc 安全边界声明
10. F-11P-01: IdempotencyChecker CheckAndMark 原子方法

### 第三批 (P1 设计+产品)
11. F-8P-01: Publish helper 改为返回 error
12. F-8S-03: 事件 payload 加 event_id
13. F-10P-01: outbox_entries 加 attempt_count
14. F-12A-01: processDelivery 改 goroutine
15. 接口定义: kernel/ 中补 DBPool/Cache/Lock 接口

### 第四批 (P1 运维+DX + 所有 P2)
16. docker-compose 修复 (MinIO healthcheck, 持久化卷, 端口绑定, 资源限制)
17. Makefile 修复 (test-integration 清理, cd -> -C)
18. 错误码统一 (ErrAdapterPGNoTx, ErrAdapterRedisDel)
19. CI pipeline (.github/workflows/ci.yml)
20. 全部 P2 (可在后续 Sprint 处理)

---

## 详细 Finding 文件

各 PR 完整 finding 详情见：
- `/tmp/claude/review-pr7.md`
- `/tmp/claude/review-pr8.md`
- `/tmp/claude/review-pr9.md`
- `/tmp/claude/review-pr10.md`
- `/tmp/claude/review-pr11.md`
- `/tmp/claude/review-pr12.md`
