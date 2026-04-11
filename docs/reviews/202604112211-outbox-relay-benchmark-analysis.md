# Outbox Relay 三阶段重写 — 开源对标与架构审查报告

> 日期: 2026-04-11
> 状态: 审查完成，待裁决
> 关联: `docs/reviews/202604072154-outbox-relay-three-phase-plan.md` (设计文档)
> 对标框架: Watermill / pgx-outbox / go-outbox / NServiceBus / MassTransit / Axon / Debezium

---

## 1. 开源项目对标总览

### 1.1 Watermill (ThreeDotsLabs/watermill + watermill-sql)

| 维度 | 设计 |
|------|------|
| 架构 | 三组件: SQL Publisher (写入) + SQL Subscriber (轮询) + Forwarder (桥接到真实 broker) |
| Schema | `offset`/`uuid`/`payload`/`metadata`/`transaction_id`，**无 status 列/重试计数/DLQ** |
| 并发 | `FOR UPDATE` 锁 offsets 表行（非 message 行），单 consumer group 互斥 |
| 重试 | 扁平间隔（默认 1s），无指数退避，Nack 无限重试 |
| 状态机 | 无显式状态机，`atomic.Uint32` closed flag |
| 可观测 | `LoggerAdapter` 接口，无内建 metrics |
| 特色 | `xid8` + `pg_snapshot_xmin` 防脏读未提交事务；offset-based 顺序日志模型 |

**GoCell 采纳**: `xid8` 可见性过滤值得后续考虑（当前 `FOR UPDATE SKIP LOCKED` 已够用）。
**GoCell 偏离**: Watermill 无 per-row status/retry/DLQ，GoCell 的设计更成熟。

### 1.2 nikolayk812/pgx-outbox

| 维度 | 设计 |
|------|------|
| 架构 | 两阶段: Read (SELECT) → Publish → Ack (UPDATE published_at) |
| Schema | 极简 `published_at IS NULL` 区分状态，无 status/retry/lock |
| 并发 | **无行锁**，文档明确要求单实例运行，多实例产生重复投递 |
| 重试 | 无。Publish 失败 = fail-fast 返回，下次重试 |
| 特色 | pgx 原生、极简设计、`MessageFilter` 按 broker/topic 分区 |

**GoCell 采纳**: fail-fast on Ack 失败的策略（已在设计中）。
**GoCell 偏离**: 必须支持多 relay 实例，需要 `FOR UPDATE SKIP LOCKED`。

### 1.3 pkritiotis/go-outbox

| 维度 | 设计 |
|------|------|
| 架构 | 显式行锁: `locked_by`/`locked_on` + background lock-checker |
| Schema | `state` + `number_of_attempts` + `last_attempted_on` + `error` (varchar 1000) |
| 并发 | `locked_by` 字段标识实例，lock-checker 定期回收超时锁 |
| 重试 | `number_of_attempts` 追踪，有上限 |
| 特色 | **`error` 列记录最后失败原因** — 运维友好 |

**GoCell 采纳**: `error`/`last_error` 列值得加入 schema，降低日志关联调试成本。

### 1.4 企业级框架

| 框架 | 关键设计 | GoCell 启示 |
|------|---------|------------|
| **NServiceBus** (.NET) | 双层重试: immediate (5x) + delayed (递增延迟)，耗尽后进 error queue | GoCell 单层退避 + dead 对 v1.0 够用 |
| **MassTransit** (.NET) | 分离 inbox/outbox/outboxState 三表；DuplicateDetectionWindow 控制清理 | inbox 去重在消费端由 Claimer 实现，不需要额外表 |
| **Axon** (Java) | 按 aggregate 顺序死信 — 一条失败，同 aggregate 后续全部暂停 | L3/L4 有序投递场景可借鉴，L2 不需要 |
| **Debezium** (CDC) | WAL/CDC 替代轮询，近零延迟 | 运维复杂度高，GoCell 当前规模用轮询正确；schema 相同可后续切换 |

### 1.5 行业共识

| 问题 | 共识 | GoCell 对齐度 |
|------|------|--------------|
| Relay 生命周期 | 三阶段 (claim/publish/writeback) 是成熟模式 | ✅ 对齐 |
| Dead-letter | max-attempts + 退避 + dead 状态是标配 | ✅ 对齐 |
| Claim TTL / 超时回收 | background lock-checker 定期回收 | ✅ 对齐 |
| Jitter | 多实例场景需要 jitter 防 thundering herd | ⚠️ 设计缺失 |
| 错误记录 | `error`/`last_error` 列辅助调试 | ⚠️ 设计缺失 |
| CDC vs 轮询 | 先轮询，CDC 按需切换 | ✅ 对齐 |

---

## 2. 架构审查发现

### P0 — 必须在 PR 合入前修复

#### F-8: writeBack/reclaimStale 竞争条件 (并发安全)

**问题**: writeBack 的 UPDATE 语句 `WHERE id = $1` 没有检查当前 status。当 writeBack 执行时间跨越 ClaimTTL 边界，reclaimStale 可能已将条目回收为 pending 甚至被另一个 relay 重新 claim。writeBack 会覆盖新状态，导致事件丢失或状态不一致。

**时序**:
```
t=0      relay A claim 条目 X, status='claiming'
t=58s    relay A publish 成功
t=59s    relay A 开始 writeBack 事务
t=60s    reclaimStale 将 X 回收为 pending (claimed_at < now()-60s)
t=60.1s  relay B claim 条目 X, status='claiming'
t=60.2s  relay A writeBack: UPDATE SET status='published' WHERE id=X  ← 覆盖 relay B 的 claim!
```

**修复**: writeBack 的所有 UPDATE 加乐观锁:
```sql
UPDATE outbox_entries SET status = 'published', published_at = now()
WHERE id = $1 AND status = 'claiming'
```
affected rows = 0 时跳过（at-least-once 语义不受影响）。

#### F-9: Attempts 不应添加到 kernel/outbox.Entry (分层违规)

**问题**: `Attempts` 是 relay adapter 的运行时状态，不是事件领域属性。添加到 kernel 层 Entry 会:
- 违反 `kernel/ 不依赖 adapters/` 的分层约束（反向耦合）
- 通过 `json.Marshal(entry)` 序列化到 broker 消息，消费端看到无意义字段

**修复**: 在 adapter 层使用包装结构:
```go
// adapters/postgres/outbox_relay.go
type relayEntry struct {
    outbox.Entry
    Attempts int
}
```

#### F-4: reclaimStale 不增加 attempts — 崩溃无限循环 (状态机缺陷)

**问题**: 如果某条消息的 payload 导致 relay panic/OOM（在 Phase 2），relay 崩溃后条目由 reclaimStale 回收到 pending。但 reclaimStale 不增加 attempts（设计中只在 writeBack 增加），条目永远在 `pending → claiming → crash → reclaimStale → pending` 循环，永不进入 dead。

**修复**: reclaimStale 回收时增加 attempts，达到 MaxAttempts 时直接标记 dead:
```sql
UPDATE outbox_entries
SET status = CASE WHEN attempts + 1 >= $2 THEN 'dead' ELSE 'pending' END,
    attempts = attempts + 1,
    claimed_at = NULL,
    next_retry_at = CASE WHEN attempts + 1 >= $2 THEN NULL
                        ELSE now() + ($3 * power(2, attempts + 1))::interval END
WHERE status = 'claiming' AND claimed_at < now() - $1::interval
```

### P1 — 本 PR 建议修复

#### F-3: 索引列顺序与 claim SQL 的 ORDER BY 不匹配 (性能)

**问题**: 索引 `(next_retry_at NULLS FIRST, created_at) WHERE status = 'pending'`，但 claim SQL 是 `ORDER BY created_at`。PostgreSQL 无法利用索引排序，高流量下退化为 Index Scan + Sort。

**修复**: 将 claim SQL 的 ORDER BY 改为 `ORDER BY next_retry_at NULLS FIRST, created_at`，与索引对齐。语义更优：新条目 (NULL) 优先，重试条目按 retry 时间排序。

#### F-6: 退避无 jitter — 多实例 thundering herd (性能)

**问题**: 退避公式 `base * 2^attempts` 无 jitter。多 relay 实例对同批失败条目写入相同 `next_retry_at`，产生周期性 DB 负载尖峰。与 ConsumerBase 的 jitter 策略不一致。

**修复**:
```go
delay := r.config.BaseRetryDelay * (1 << newAttempts)
jitter := time.Duration(rand.Int64N(int64(delay/4) + 1))
delay += jitter
```

#### F-7: 无 MaxRetryDelay 封顶 (配置)

**问题**: `base * 2^attempts` 在 MaxAttempts 增大时无上限（MaxAttempts=10 时最大延迟 85 分钟）。ConsumerBase 有 `MaxRetryDelay=30s` 封顶。

**修复**: RelayConfig 增加 `MaxRetryDelay time.Duration` (默认 5m)，writeBack 中 `delay = min(delay, r.config.MaxRetryDelay)`。

### P2 — 记入延迟项

| 编号 | 发现 | 影响 |
|------|------|------|
| F-2 | `published` 列应在 migration 003 中删除（无外部调用方，双写增加复杂度） | 低 |
| F-5 | relayState spin-wait 应有超时或改用 channel 通知 | 低 |
| F-10 | 状态值 `"pending"`/`"claiming"` 应定义为 Go 常量，减少 SQL magic string | 低 |
| F-11 | v1.0 slog 可接受，建议预留 `RelayHooks` 回调为后续 metrics 降低集成成本 | 低 |

### 补充建议（来自生态调研）

| 编号 | 建议 | 来源 | 优先级 |
|------|------|------|--------|
| S-1 | Schema 增加 `last_error TEXT` 列记录最后失败原因 | go-outbox | P1 |
| S-2 | 设计文档补充 dead 条目操作恢复 SOP | NServiceBus | P1 |
| S-3 | 明确区分 relay dead-letter vs ConsumerBase DLX 的概念边界 | 架构审查 | P2 |
| S-4 | L3/L4 有序投递场景可借鉴 Axon 按 aggregate 顺序死信 | Axon | Backlog |

---

## 3. 综合评估

GoCell 的三阶段设计方案**总体合理**，在开源生态中属于中上水平：

- 比 Watermill 更完整（有 per-row status + retry + DLQ）
- 比 pgx-outbox 更健壮（多实例安全 + 退避）
- 接近 go-outbox 的成熟度，但需要补充 error 列和 jitter
- 距离 NServiceBus/MassTransit 的企业级还有差距（双层重试、inbox 去重表），但 v1.0 不需要

**3 个 P0 问题必须在实施前修复**，否则会在生产环境引发真实的数据一致性问题（F-8 竞争条件）或无限循环（F-4）。

---

## 4. 六席位团队审查

> Baseline commit: `34f6f2c` (HEAD of develop)

### 审查矩阵

| 席位 | 新发现 | P0 | P1 | P2 | 裁决 |
|------|--------|----|----|-----|------|
| S1 架构 | 4 | 0 | 3 | 1 | Accept with conditions |
| S2 安全 | 3 | 0 | 0 | 3 | Accept with conditions |
| S3 测试 | 4 | 0 | 3 | 1 | **Request changes** |
| S4 运维 | 4 | 0 | 2 | 2 | Accept with conditions |
| S5 DX | 5 | 0 | 3 | 2 | Accept with conditions |
| S6 产品 | 4 | 0 | 2 | 2 | Accept with conditions |
| **合计** | **24** | **0** | **13** | **11** | — |

### S1 架构席

**S1-F1 [P1] Entry 整体序列化泄漏基础设施字段到 broker**

`publishAll` 中 `json.Marshal(e)` 将整个 `outbox.Entry` 发到 broker，包含 `AggregateType`、`CreatedAt` 等 relay 内部元数据。且 Entry 无 JSON struct tags，默认 PascalCase 违反项目 camelCase 约定。未来任何 Entry 字段新增都是 wire-format breaking change。

建议: 定义 wire envelope 类型，只发 `id` + `eventType` + `topic` + `payload` + `metadata` + `createdAt`，并加 JSON tags。

**S1-F2 [P1] 设计文档缺少 `ref:` 标记**

CLAUDE.md 要求 commit message 注明 `ref: {framework} {file}`。设计文档只在散文中提到对标框架，实施时 commit 必须带正式 ref 标记。

**S1-F3 [P2] publishAll 顺序发布，未考虑并发**

BatchSize=100 条 × ~5ms/条 = ~500ms。bounded concurrency (`errgroup` + limit=10) 可降至 ~50ms。v1.1 吞吐优化。

**S1-F4 [P1] 新 CUD 操作未标注一致性级别**

claim/writeBack/reclaimStale 三个 UPDATE 操作均未标注 L2 (OutboxFact) 一致性级别。

### S2 安全席

**S2-F1 [P2] dead 条目无保留策略，payload 可能含敏感数据**

`deletePublishedBefore` 只清理 published 条目，dead 条目永久累积。建议: `DeadRetentionPeriod` (默认 30d) + 清理前记日志。

**S2-F2 [P2] reclaimStale 无速率限制**

如果 ClaimTTL 被误配为极低值 (如 5s)，reclaimStale 会不断回收正在处理的条目，形成级联故障。建议: 启动时断言 `ClaimTTL > PollInterval * 2`。

**S2-F3 [P2] dead-letter slog 可能通过 error 链泄漏 payload**

`slog.Any("last_error", res.err)` 如果 error 包装了部分 payload 内容，可能泄漏敏感数据。实现时需审查 error 链。

### S3 测试席 (Request changes)

**S3-F1 [P1] 测试计划遗漏 2 个关键状态转换**

- `pending → pending (next_retry_at 未到，被跳过)` — 验证重试调度正确性
- `published → deleted (cleanup)` — 旧测试需适配 status 列

**S3-F2 [P1] 无 writeBack 事务失败 (rollback 路径) 测试**

Commit 失败后所有 UPDATE 应回滚。此场景是三阶段设计缓解重复投递的关键路径。

**S3-F3 [P1] F-8 竞争条件测试构造指导**

推荐方案: 注入 `writeBackHook func()` (unexported)，在 Phase 2 和 Phase 3 之间注入超过 ClaimTTL 的 sleep，验证 `WHERE status = 'claiming'` 返回 0 affected rows。辅以集成测试 (两个 relay + 短 ClaimTTL + 慢 publisher)。

**S3-F4 [P2] Mock 未校验 Scan 字段数**

新 claim 查询返回 9 个字段 (增加 attempts)，mock 的 values 数组必须精确匹配。

### S4 运维席

**S4-F1 [P1] Migration 003 大表锁风险**

`UPDATE outbox_entries SET status = 'published' WHERE published = true` 是全表扫描 + 写，持 `ACCESS EXCLUSIVE` 锁。建议: 分批 backfill 或直接跳过 (旧条目不满足 `WHERE status = 'pending'` 条件，不会被重新 claim，72h 后被 retention 清理)。

**S4-F2 [P1] reclaimStale 执行频率与 ClaimTTL 不匹配**

reclaimStale 每 10s 执行一次，但 ClaimTTL=60s — 6/7 次扫描找不到行。建议: 独立 reclaimStale 间隔 (如 `ClaimTTL / 2 = 30s`)。

**S4-F3 [P2] 无 dead-letter 告警策略**

建议: 5min 窗口内 dead > 0 → Slack；dead > 10 → PagerDuty。

**S4-F4 [P2] Down-migration 静默丢弃 dead 条目状态**

回滚 migration 会丢失 dead-letter 标记。需文档标注为破坏性回滚。

### S5 DX 席

**S5-F1 [P1] 配置膨胀 — 7 个字段无预设**

建议: 至少提供 `DefaultRelayConfig()` 覆盖所有 7 个字段，可选 `ProductionRelayConfig()` / `DevelopmentRelayConfig()`。

**S5-F2 [P1] 状态机缺少 godoc 文档**

设计文档的 ASCII 状态机图应嵌入 `OutboxRelay` 或 `relayState` 的 godoc 注释中。

**S5-F3 [P1] 状态值 magic string (升级自 F-10)**

`"pending"` / `"claiming"` / `"published"` / `"dead"` 在 6+ 处 SQL 中作为裸字符串使用，超过项目 "≥3 次使用抽常量" 规范。

**S5-F4 [P2] Duration 位移溢出风险**

`BaseRetryDelay * (1 << newAttempts)` 在大 MaxAttempts 时可能溢出 int64。F-7 的 MaxRetryDelay cap 可防护，但建议显式 guard。

**S5-F5 [P2] dead-letter 日志缺少 event_type 和 aggregate_id**

调试时最先需要知道 "什么事件死了" 和 "属于哪个聚合"。

### S6 产品席

**S6-F1 [P1] 无 dead-letter 管理 API**

dead 条目对开发者不可见，只能靠 SQL 查询。建议: 跟踪为 follow-up，在 relay rewrite 合并后 2 个 Sprint 内提供 `gocell outbox dead-letter list/retry` CLI 和 `GET /internal/v1/outbox/dead-letters` API。

**S6-F2 [P1] MaxAttempts=5 未按事件重要性分级**

session 创建 (L1) 和设备命令回执 (L4) 的重试需求差异大。当前单一 RelayConfig 意味着不同策略需要运行不同 relay 实例。建议: 文档标注此限制 + 后续支持 per-topic config。

**S6-F3 [P2] at-least-once 语义缺乏按事件类型的容忍度文档**

应补充事件类型 × 重复容忍度 × 缓解措施矩阵。

**S6-F4 [P2] dead-letter 管理 API 响应格式预定义**

follow-up 实现时需遵循 `{"data":...,"total":...,"page":...}` 统一格式。

---

## 5. 阻塞项清单

### Must-fix before PR merge

| 来源 | 编号 | 问题 | 优先级 |
|------|------|------|--------|
| 对标 | F-8 | writeBack 加 `WHERE status='claiming'` 乐观锁 | P0 |
| 对标 | F-4 | reclaimStale 增加 attempts，达 max 标 dead | P0 |
| 对标 | F-9 | Attempts 留 adapter 层 `relayEntry`，不入 kernel Entry | P0 |
| S3 | S3-F1 | 补充 2 个遗漏状态转换测试 | P1 |
| S3 | S3-F2 | writeBack commit 失败回滚测试 | P1 |
| S4 | S4-F1 | migration backfill 分批或跳过 | P1 |
| S5 | S5-F3 | 状态值抽 Go 常量 | P1 |

### Should-fix in same PR

| 来源 | 编号 | 问题 | 优先级 |
|------|------|------|--------|
| 对标 | F-3 | 索引与 ORDER BY 对齐 | P1 |
| 对标 | F-6 | 退避加 jitter | P1 |
| 对标 | F-7 | 增加 MaxRetryDelay | P1 |
| S1 | S1-F1 | Entry JSON tags 或 wire envelope | P1 |
| S1 | S1-F4 | CUD 操作标注 L2 一致性级别 | P1 |
| S5 | S5-F1 | DefaultRelayConfig 覆盖全部字段 | P1 |
| S5 | S5-F2 | 状态机 godoc | P1 |
| S4 | S4-F2 | reclaimStale 独立间隔 | P1 |
| 补充 | S-1 | Schema 增加 `last_error TEXT` 列 | P1 |

### Track as follow-up

| 来源 | 编号 | 问题 | 建议时间 |
|------|------|------|---------|
| S6 | S6-F1 | dead-letter 管理 CLI + API | 2 Sprint |
| S2 | S2-F1 | dead 条目 retention policy | 同上 |
| S1 | S1-F3 | publishAll 并发优化 | v1.1 |
| S6 | S6-F2 | per-topic MaxAttempts | v1.1 |
| 对标 | S-4 | Axon 按 aggregate 顺序死信 | Backlog |
