# Decisions — Phase 3: Adapters

## 裁决日期
2026-04-05

## 审查来源
- 架构师: review-architect.md (8 条建议)
- Roadmap 规划师: review-roadmap.md (10 条建议)
- Kernel Guardian: kernel-constraints.md (10 条建议)
- 产品经理: review-product-manager.md (9 条建议)

总计 37 条建议，去重合并后 27 条独立决策点。

---

## 重要决策

### 决策 1: Outbox 事务上下文使用 context-embedded 模式（非新 kernel 接口）

- **决策**: 不新增 `kernel/tx/TxContext` 接口。保持 `outbox.Writer.Write(ctx, Entry) error` 签名不变，采用 context-embedded transaction 模式：`TxManager.RunInTx` 将 `pgx.Tx` 存入 context，`OutboxWriter.Write` 从 context 提取 tx。在 `outbox.Writer` godoc 中文档化这一约定。若 context 无 tx 则 fail-fast 返回 `ERR_ADAPTER_NO_TX`。
- **理由**: context-embedded tx 是 Go 社区标准模式（database/sql、pgx 均支持），且不需要修改 kernel 接口签名。Watermill watermill-sql 也使用此模式。新增 kernel 接口（ARCH-01 方案 2）会增加接口面积且引入对 adapter 具体实现的间接依赖。
- **被否决的替代方案**: (A) `kernel/tx.TxContext` 新接口 — 否决：增加 kernel 接口面积，且 tx 语义与具体数据库绑定，不属于 kernel 通用抽象。(B) `UnitOfWork` 模式 — 否决：引入额外编排层，过度设计。
- **来源**: ARCH-01, KS-01, KS-05

### 决策 2: ArchiveStore 不由 adapters/s3 实现 — S3 提供通用 ObjectStore，Cell 内部封装

- **决策**: 移除 FR-4.4（adapters/s3 实现 ArchiveStore）。`adapters/s3/` 提供通用的 `Client`（Upload/Download/Delete/PresignedURL），不 import cells/。`cells/audit-core/internal/adapters/s3archive/` 包装 S3 Client 为 `ArchiveStore` 实现。这与 spec 5.2 "具体 Repository 实现由 Cell 内部 internal/adapters/ 子包完成" 一致。
- **理由**: 三份审查（ARCH-02, KS-10, PM-06）均指出 adapters/ import cells/ 违反分层规则且 Go `internal` 包可见性阻止编译。方案 C（Cell 内部实现）最干净。
- **被否决的替代方案**: (A) 提升 ArchiveStore 到 kernel/ — 否决：ArchiveStore 是 audit-core 特有概念，不够通用，不属于 kernel 层。(B) NFR-1 增加例外 — 否决：一旦开例外，分层约束形同虚设。

### 决策 3: Bootstrap 重构为接口注入 — WithPublisher + WithSubscriber 替代 WithEventBus

- **决策**: 重构 `runtime/bootstrap` 将 `WithEventBus(*eventbus.InMemoryEventBus)` 拆分为 `WithPublisher(outbox.Publisher)` + `WithSubscriber(outbox.Subscriber)`。保留 `WithEventBus` 作为便利方法同时设置两者。这是 Phase 3 Wave 0 前置任务。
- **理由**: 当前 bootstrap 绑定具体类型 `*eventbus.InMemoryEventBus`，无法注入 RabbitMQ adapter。是所有 adapter 接线的前置条件。
- **被否决的替代方案**: 在 cmd/core-bundle 手动调用 RegisterSubscriptions 绕过 bootstrap — 否决：绕过 bootstrap 的 lifecycle 管理，shutdown 顺序失控。
- **来源**: ARCH-04, KS-06, RISK-04

### 决策 4: Phase 3 实现最小 Cell Repository PG 实现（AuditRepository + ConfigRepository）

- **决策**: 在 Phase 3 实现 `cells/audit-core/internal/adapters/postgres/audit_repo.go` 和 `cells/config-core/internal/adapters/postgres/config_repo.go`，作为 J-audit-login-trail 和 J-config-hot-reload Journey 端到端测试的前置依赖。其余 Repository（User/Session/Role/Flag）延至 Phase 4。
- **理由**: 不实现任何 Cell Repository 则 outbox 全链路测试（FR-8.2）无法验证"业务写入 + outbox 写入同事务"，success criterion S2/S3 无法兑现。最小实现 2 个 Repository 足够证明 L2 模式可行。
- **被否决的替代方案**: (A) 用 test-only stub repository — 否决：不能证明真实场景的事务传播。(B) 全部 7 个 Repository — 否决：工作量过大，超出 Phase 3 adapter 核心目标。
- **来源**: ARCH-05, RM-10

### 决策 5: VictoriaMetrics adapter 明确延迟至 Phase 4

- **决策**: 在 spec 范围排除中明确声明 VictoriaMetrics adapter 延迟至 Phase 4。Phase 3 聚焦数据持久化和消息传递，指标推送优先级低于 outbox 全链路。同步更新 master-plan Phase 3 条目。
- **理由**: Phase 3 的核心价值是证明 L2 outbox 和消息传递链路可用，VictoriaMetrics 是可观测性增强，不阻塞核心目标。
- **来源**: RM-01

### 决策 6: 交付波次结构 — Wave 0-4 五波交付

- **决策**: 采纳 KG 建议的 Wave 0-4 结构，明确 Wave 1（adapter 核心 + DevOps）是 Phase 3 Gate 硬性前提，Wave 3-4（tech-debt + 测试 + 文档）中 P2/P3 级 tech-debt 允许 DEFERRED 溢出至 Phase 4。
- **理由**: 14 个 FR 中 6 个非 adapter 工作（tech-debt/安全/文档），需要明确优先级保护 adapter 核心交付。
- **来源**: RM-03, KG Wave 建议

### 决策 7: adapters/ 目录扁平化 — 不使用 family/ 子目录

- **决策**: 所有 6 个 adapter 放在 `adapters/` 顶层目录，不区分 First-class/Family。这是对 master-plan Layer 4/5 分层的有意偏离。
- **理由**: 子目录只增加 import 路径复杂度，6 个 adapter 的维护承诺差异不足以支撑子目录区分。master-plan 同步修订。
- **来源**: RM-02

### 决策 8: #60/#61 纳入 Phase 3 — 从 DEFERRED 列表移除

- **决策**: tech-debt #60（configsubscribe unmarshal → DLQ）和 #61（auditappend publish → outbox.Writer）纳入 FR-10 范围。#60 依赖 FR-5.4 ConsumerBase，#61 依赖 FR-1.4 OutboxWriter。从 spec 第 6 节排除列表移除。DEFERRED 降为 6 条。
- **理由**: charter 标注 "Phase 3 后期或 Phase 4" 但 spec 排除，自相矛盾。两条与 adapter 直接相关且修复有明确依赖。
- **来源**: RM-04

### 决策 9: Publisher.Publish 签名不变 — Relay 序列化 Entry 到 payload

- **决策**: 保持 `Publisher.Publish(ctx, topic string, payload []byte) error` 签名不变。Relay 将完整 `Entry`（含 ID, AggregateID, Metadata）序列化为 JSON 作为 payload。Subscriber 端反序列化还原 Entry。RabbitMQ message headers 携带 `event_id`（= Entry.ID）用于幂等。
- **理由**: 修改 kernel 接口签名影响所有 Publisher 实现（InMemoryEventBus + 未来 adapter），Phase 3 应最小化 kernel 变更。JSON 序列化的 overhead 对异步消息场景可接受。
- **被否决的替代方案**: 扩展为 `Publish(ctx, topic, entry Entry)` — 否决：kernel 接口稳定性优先。
- **来源**: KS-02

### 决策 10: Subscriber.Close 不加 context — 用 Config 超时

- **决策**: 保持 `Subscriber.Close() error` 签名不变。RabbitMQ Subscriber 通过 `SubscriberConfig.ShutdownTimeout` 控制 drain 时间。
- **理由**: 修改 kernel 接口影响 InMemoryEventBus.Close()。Phase 3 应最小化 kernel 变更。Config-based timeout 对 adapter 实现足够。
- **来源**: KS-09

### 决策 11: 新增 FR-8.5 多 adapter Assembly 组合集成测试

- **决策**: 新增 FR-8.5：至少 1 个 testcontainers 测试验证 postgres Pool + TxManager + OutboxWriter + rabbitmq Publisher + redis IdempotencyChecker 同时注入 CoreAssembly，执行 Start → 业务写入 → outbox relay → consume → idempotency → Stop 全生命周期。
- **理由**: Phase 4 examples 需要多 adapter 协同工作，Phase 3 不验证组合场景则 Phase 4 可能发现兼容问题。
- **来源**: RM-05

---

## Kernel Guardian 约束裁决

| 约束项 | 裁决 | 理由 |
|--------|------|------|
| KS-01: outbox.Writer tx context 文档化 | 采纳 | doc-only change，不修改签名（决策 1） |
| KS-02: Publisher.Publish 签名扩展 | 延迟 | kernel 接口稳定性优先，Relay 序列化解决（决策 9） |
| KS-03: Entry.ID 文档为 idempotency 标识 | 采纳 | doc-only change |
| KS-04: Dependencies 不改，用 Option 模式 | 采纳 | Phase 4 评估是否需要 Infra struct |
| KS-05: 7 处 Publish 改 outbox.Writer | 采纳 | Wave 3 Cell 重构（决策 4 配合） |
| KS-06: Bootstrap 接口化 | 采纳 | Wave 0 前置任务（决策 3） |
| KS-07: Relay 实现 worker.Worker | 采纳 | 集成 bootstrap lifecycle |
| KS-08: kernel/ 最小修改（仅 doc） | 采纳 | 确认 Phase 3 kernel 无 Go 签名变更 |
| KS-09: Subscriber.Close 不加 ctx | 延迟 | 用 Config timeout 替代（决策 10） |
| KS-10: ArchiveStore 不由 adapters/ 实现 | 采纳 | Cell 内部封装（决策 2） |
| C-01~C-05: 分层隔离约束 | 采纳 | 全部纳入 Phase 3 验证清单 |
| C-06~C-11: 接口合规约束 | 采纳 | 全部纳入 Phase 3 验证清单 |
| C-12~C-15: 生命周期约束 | 采纳 | 全部纳入 Phase 3 验证清单 |
| C-16~C-18: 元数据完整性约束 | 采纳 | 全部纳入 Phase 3 验证清单 |
| C-19~C-20: 错误处理约束 | 采纳 | 全部纳入 Phase 3 验证清单 |
| C-21~C-22: 一致性等级约束 | 采纳 | 全部纳入 Phase 3 验证清单 |
| C-23~C-25: 内核稳定性约束 | 采纳 | 全部纳入 Phase 3 验证清单 |

## 延迟到后续 Phase 的项目

| 项目 | 来源 | 延迟理由 | 计划 Phase |
|------|------|---------|-----------|
| Publisher.Publish 签名扩展（KS-02） | kernel-constraints.md | kernel 接口稳定性优先，Relay JSON 序列化足够 | Phase 4 评估 |
| Subscriber.Close(ctx) 参数（KS-09） | kernel-constraints.md | kernel 接口稳定性优先，Config timeout 足够 | Phase 4 评估 |
| Dependencies.Adapters typed field（KS-04） | kernel-constraints.md | Option 模式对 Phase 3 足够，adapter 数量 > 5 时再评估 | Phase 4 评估 |
| List/Query 分页接口（ARCH-07） | review-architect.md | 涉及 Cell port 接口重新设计，范围过大。Phase 3 在 PG 实现中加 LIMIT 1000 安全网 | Phase 4 |
| 统一 AdapterConfigs 加载（RM-06） | review-roadmap.md | 超出 Phase 3 adapter 核心目标，FR-12.4 文档推荐模式即可 | Phase 4 |
| Kernel 能力状态矩阵（RM-09） | review-roadmap.md | 有用但不阻塞 Phase 3，在 Phase 4 规划前补充 | Phase 4 |
| Grafana dashboard 模板（RM-08） | review-roadmap.md | 依赖指标端点稳定，与 VictoriaMetrics 一并延迟 | Phase 4 |
| VictoriaMetrics adapter（RM-01） | review-roadmap.md | Phase 3 聚焦持久化和消息传递，指标推送优先级低 | Phase 4 |
| User/Session/Role/Flag Repository PG 实现 | review-architect.md | Phase 3 仅实现 Audit + Config Repository 作为最小 L2 证明 | Phase 4 |

## 被拒绝的建议

| 建议 | 来源 | 拒绝理由 |
|------|------|---------|
| 新增 kernel/tx.TxContext 接口（ARCH-01 方案 2） | review-architect.md | context-embedded 模式足够，不增加 kernel 接口面积 |
| ArchiveStore 提升到 kernel/（PM-06 方案 A） | review-product-manager.md | ArchiveStore 是 audit-core 特有概念，不够通用 |
| NFR-1 增加分层例外（PM-06 方案 B） | review-product-manager.md | 一旦开例外，分层约束形同虚设 |
| adapters/family/ 子目录（RM-02 方案 A） | review-roadmap.md | 子目录只增加 import 复杂度，6 adapter 维护承诺差异不大 |

## 采纳的改进（编码回 spec）

| # | 来源 | 改动 |
|---|------|------|
| 1 | ARCH-03 | FR-1.5 明确 Relay 接受 outbox.Publisher 接口 |
| 2 | ARCH-06 | FR-1.5 增加轮询策略、批量大小、SKIP LOCKED、cleanup |
| 3 | ARCH-08 | NFR-8 补充完整 shutdown 顺序（含 Relay 位置） |
| 4 | RM-03 | 新增 Wave 0-4 交付波次章节 |
| 5 | RM-04 | #60/#61 纳入 FR-10，从排除列表移除 |
| 6 | RM-05 | 新增 FR-8.5 多 adapter Assembly 组合测试 |
| 7 | RM-07 | P2 tier 拆分为 P2-High / P2-Low |
| 8 | PM-01 | FR-8.4 补增 J-config-rollback |
| 9 | PM-02 | FR-9 补充 Given/When/Then 验收条件 |
| 10 | PM-03 | FR-10 补充验证方式和计数规则 |
| 11 | PM-04 | 新增 adapter 错误码前缀表 |
| 12 | PM-05 | 新增默认值参考表 |
| 13 | PM-07 | FR-7.2 补充 docker compose --wait 自动化验证 |
| 14 | PM-08 | FR-5.3 明确 consumer group 通过构造函数注入 |
| 15 | PM-09 | FR-11.3 补充 PATCH 语义和验收条件 |
| 16 | 决策 1 | kernel/outbox doc 增强（context-embedded tx 约定） |
| 17 | 决策 2 | FR-4.4 移除，S3 提供通用 ObjectStore |
| 18 | 决策 3 | 新增 FR-15 Bootstrap 重构 |
| 19 | 决策 4 | FR-8.4 前置：最小 Cell PG Repository |
| 20 | 决策 5 | 范围排除增加 VictoriaMetrics |
| 21 | 决策 7 | 范围排除增加 adapters/family/ 目录 |
| 22 | KS-03 | Entry.ID 文档化为 idempotency 标识 |
| 23 | KS-07 | FR-1.5 Relay 实现 worker.Worker |
