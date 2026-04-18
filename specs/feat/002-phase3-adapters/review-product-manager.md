# Product Manager Review -- Phase 3: Adapters

> Reviewer: Product Manager Agent
> Date: 2026-04-05
> Input: spec.md, product-context.md, phase-charter.md
> Scope: 验收标准覆盖率、开发者体验、范围一致性、兼容性风险

---

## 审查摘要

Spec 整体结构完整，14 个 FR 覆盖了 6 个 adapter、基础设施环境、集成测试、安全加固、tech-debt 偿还、文档和 DevOps。与 product-context 的 12 条成功标准对照后发现以下问题：1 处 Journey 范围遗漏（J-config-rollback）、多个 FR 缺少可量化的验收条件（尤其安全加固和 tech-debt 偿还）、adapter API 错误码体系与开发者消费方式的对齐缺失、以及部分默认值和超时参数未明确导致实现时自由度过大。

以下 9 条建议按优先级排列。

---

## PM-01 [验收标准缺失] J-config-rollback Journey 在 spec 中无对应 FR

**问题描述**

product-context.md 成功标准 S3 明确列出三个 Soft Gate Journey 需要端到端验证：

> J-audit-login-trail、J-config-hot-reload、**J-config-rollback**（回滚 -> event -> subscriber 重载）

但 spec.md FR-8.4 仅覆盖了 J-audit-login-trail 和 J-config-hot-reload 两个 Journey，J-config-rollback 被遗漏。phase-charter.md P3 persona 段落也提到了这三个 Journey。requirements.md checklist 同样只列出两个。

如果 S3 成功标准不调整，则 spec 当前无法满足 S3 要求。

**建议修改**

方案 A（推荐）：在 FR-8.4 中补增 J-config-rollback 集成测试场景，描述为：J-config-rollback（config rollback -> RabbitMQ event -> subscriber 重载为旧版本 -> DB 验证回滚后的 config 状态）。同步更新 requirements.md checklist。

方案 B：修订 product-context.md S3，将 J-config-rollback 移至 Phase 4，并注明理由（例如 rollback 语义依赖 version 管理持久化，与 DEFERRED #62 关联）。

---

## PM-02 [验收标准缺失] FR-9 安全加固 8 条缺少可验证的验收条件

**问题描述**

FR-9 的 8 条安全修复仅描述了"修复要求"（what to do），但没有给出可测试的验收条件（how to verify it passed）。对于安全类修复，仅靠代码审查不足以构成可重复验证。具体问题：

- FR-9.1（密钥环境变量化）：未说明 "fail-fast" 的具体行为 -- 是 `os.Exit(1)` 还是 `return error`？日志是否需要输出哪个变量缺失？
- FR-9.2（JWT RS256）：未说明如何验证旧 HS256 token 被拒绝。
- FR-9.4（ServiceToken timestamp）：5 分钟窗口的边界行为未定义 -- 恰好 5 分钟算通过还是失败？
- FR-9.7（refresh rotation reuse detection）：未定义 "全 session 吊销" 的范围 -- 同一 user 的所有 session 还是同一 device 的所有 session？
- FR-9.8（端点认证中间件）：未提供公开/保护端点列表。

**建议修改**

为每条安全修复补充 Given/When/Then 格式的验收条件。示例：

FR-9.1:
- Given: 环境变量 `GOCELL_JWT_PRIVATE_KEY` 未设置
- When: 服务启动
- Then: 进程在 5 秒内退出，退出码非 0，slog Error 日志包含 "missing required config: GOCELL_JWT_PRIVATE_KEY"

FR-9.7:
- Given: user A 持有 refresh_token_v1
- When: refresh_token_v1 被使用后获得 refresh_token_v2，随后 refresh_token_v1 再次被提交
- Then: refresh_token_v1 被拒绝，refresh_token_v2 被吊销，user A 的该 session 关联的所有 token 失效

FR-9.8:
- 在 spec 中列出完整的公开端点清单（如 `/api/v1/health`, `/api/v1/auth/login`, `/api/v1/auth/callback`），其余默认为保护端点。

---

## PM-03 [验收标准缺失] FR-10 Tech-Debt 偿还缺少完成定义和验证方式

**问题描述**

FR-10 将 80 条 tech-debt 分成 6 个子类（编码规范、架构修复、生命周期修复、测试补全、治理规则、运维/DX），但每个子类只列出了代表性条目（如 "#27", "#79"），没有：

1. 每个子类的完整条目清单（开发者不知道 FR-10.1 到底包含哪些条目）
2. 每条的验证方式（编码规范修复用 `go vet` 验证？架构修复用编译验证？测试补全看覆盖率报告？）
3. 与成功标准 S7 的明确映射 -- S7 要求 "80 条中至少 60 条 RESOLVED"，但 spec 未定义哪些条目属于 "必须 RESOLVED" vs "允许 DEFERRED"

这导致 Phase 结束时无法客观判定 S7 是否达标。

**建议修改**

1. 在 spec 中增加一个附录或引用文件，列出 80 条 tech-debt 的完整编号和目标状态（RESOLVED / DEFERRED + 理由）。
2. 为 FR-10 每个子类指定验证方式：
   - FR-10.1 编码规范: `grep -rn "fmt.Errorf" kernel/ cells/` 返回 0 匹配（对外暴露路径）
   - FR-10.3 生命周期: 增加 shutdown 顺序的集成测试或 table-driven 单元测试
   - FR-10.4 测试补全: 指定目标覆盖率数值（如 handler >= 80%, bootstrap >= 70%）
3. 明确 S7 的计数规则：DEFERRED 的 8 条（#54, #56-59, #60-62）是否计入分母。

---

## PM-04 [开发者体验] adapter 错误码体系未定义完整前缀清单

**问题描述**

spec 4.3 节定义 adapter 层使用 `ERR_ADAPTER_*` 错误码前缀，但只给了一个 PostgreSQL 示例（`ERR_ADAPTER_PG_QUERY`）。6 个 adapter 各自的错误码前缀和典型错误码未列出。

对 Cell 开发者（P1 persona）而言，当业务代码调用 adapter 方法出错时，他需要知道：
- 如何区分"连接超时"（可重试）和"查询语法错误"（不可重试）
- 不同 adapter 的错误码命名是否有统一模式
- `errcode.Is()` 或 `errors.Is()` 能否匹配到具体的 adapter 错误类型

缺少这些定义会导致 6 个 adapter 的错误码在实现阶段各自为政，后续难以统一。

**建议修改**

在 spec 4.3 节或新增附录中定义 adapter 错误码清单模板：

| adapter | 前缀 | 典型错误码 |
|---------|------|-----------|
| postgres | `ERR_ADAPTER_PG_*` | `ERR_ADAPTER_PG_CONNECT`, `ERR_ADAPTER_PG_QUERY`, `ERR_ADAPTER_PG_TX_TIMEOUT` |
| redis (adapter) | `ERR_ADAPTER_REDIS_*` | `ERR_ADAPTER_REDIS_CONNECT` |
| runtime/distlock | `ERR_DISTLOCK_*` | `ERR_DISTLOCK_ACQUIRE`, `ERR_DISTLOCK_RELEASE`, `ERR_DISTLOCK_TIMEOUT`, `ERR_DISTLOCK_LOST` |
| rabbitmq | `ERR_ADAPTER_AMQP_*` | `ERR_ADAPTER_AMQP_CONNECT`, `ERR_ADAPTER_AMQP_PUBLISH`, `ERR_ADAPTER_AMQP_CONFIRM_TIMEOUT` |
| oidc | `ERR_ADAPTER_OIDC_*` | `ERR_ADAPTER_OIDC_DISCOVERY`, `ERR_ADAPTER_OIDC_TOKEN_VERIFY` |
| s3 | `ERR_ADAPTER_S3_*` | `ERR_ADAPTER_S3_UPLOAD`, `ERR_ADAPTER_S3_NOT_FOUND` |
| websocket | `ERR_ADAPTER_WS_*` | `ERR_ADAPTER_WS_UPGRADE`, `ERR_ADAPTER_WS_SEND` |

同时明确：adapter 错误码必须实现 `errcode.IsRetryable() bool` 方法或携带 retryable 标记，让上层消费者能判断是否值得重试。

---

## PM-05 [开发者体验] NFR-6 合理默认值未量化，开发者无法预判行为

**问题描述**

NFR-6 要求 "提供合理默认值（连接池大小、超时、重试间隔）"，但 spec 未定义任何默认值的具体数值。FR-1.1（PostgreSQL Pool）提到 "连接池大小、idle timeout、max lifetime 可配"，但未说明不配时的默认行为。

对 Cell 开发者和平台架构师而言，adapter 的默认行为决定了"零配置体验"的质量。如果 PostgreSQL 默认连接池是 5 还是 50、Redis 锁的默认 TTL 是 10 秒还是 30 秒、RabbitMQ reconnect 的 backoff 是 1 秒还是 60 秒上限 -- 这些直接影响开发者调试和评估体验。

**建议修改**

在每个 adapter 的 FR 或 FR-12.4（配置参考）中列出关键配置参数的默认值：

| adapter | 参数 | 建议默认值 |
|---------|------|-----------|
| postgres | pool max conns | 10 |
| postgres | idle timeout | 5m |
| postgres | max conn lifetime | 1h |
| postgres | outbox relay poll interval | 1s |
| redis | dial timeout | 5s |
| redis | read timeout | 3s |
| redis | dist lock default TTL | 30s |
| rabbitmq | reconnect max backoff | 30s |
| rabbitmq | prefetch count | 10 |
| rabbitmq | consumer retry count | 3 |
| websocket | ping interval | 30s |
| websocket | pong timeout | 10s |

这些值不需要在 spec 阶段做到完美，但需要给实现者一个参考基线，也让产品验收时有据可查。

---

## PM-06 [范围偏移] FR-4.4 ArchiveStore 实现违反 NFR-1 分层隔离声明

**问题描述**

FR-4.4 要求 `adapters/s3/archive.go` 实现 `cells/audit-core/internal/ports.ArchiveStore` 接口。但 NFR-1 明确声明：

> adapters/ 仅 import kernel/ + runtime/ + pkg/ + 外部依赖
> adapters/ 不 import cells/

FR-4.4 的实现需要 import `cells/audit-core/internal/domain.AuditEntry`（因为 ArchiveStore 接口的 `Archive` 方法签名为 `Archive(ctx context.Context, entries []*domain.AuditEntry) error`），这直接违反了 NFR-1 的分层约束。

同时，ArchiveStore 位于 `cells/audit-core/internal/ports/` -- `internal` 包在 Go 中有访问控制语义，`adapters/s3/` 无法 import 它。

**建议修改**

方案 A（推荐）：将 ArchiveStore 接口提升到 kernel/ 层（如 `kernel/archival/store.go`），使用与 Cell 无关的通用数据类型（如 `[]byte` 或 `kernel/archival.Entry`），让 Cell 在调用时负责序列化。adapter 只实现 kernel 层接口，保持分层干净。

方案 B：在 spec 中明确声明 FR-4.4 为分层例外，修改 NFR-1 增加例外条款："adapters/s3 可 import cells/audit-core/internal/ports 用于 ArchiveStore 实现"。但这需要同时解决 Go `internal` 包的可见性问题（可能需要将 ports 从 internal 移出）。

方案 C：将 ArchiveStore 的 adapter 实现放在 `cells/audit-core/internal/adapters/s3archive/` 而非 `adapters/s3/`，由 Cell 自身维护其 adapter（与 spec 5.2 描述的 "Cell Repository 由各 Cell 自行实现 adapter 层" 一致）。此时需从 FR-4 中移除 FR-4.4。

---

## PM-07 [验收标准缺失] 成功标准 S8 的 "30 秒" 健康检查无对应的自动化验证

**问题描述**

product-context S8 要求：

> `docker compose up -d` 启动 PostgreSQL + Redis + RabbitMQ + MinIO，30 秒内全部 healthy

spec FR-7.2 也写了 "30 秒内全部 healthy"。但 spec 中没有定义如何自动化验证这个时间约束。如果仅靠手动验证，这条标准在 CI 和回归场景下不可重复。

**建议修改**

在 FR-13.2（Makefile）或 FR-7.2 中增加验收条件：

- `make test-integration` 的启动脚本应包含一个带超时的健康检查等待步骤（如 `docker compose up -d --wait --timeout 30`，或自定义脚本循环检查 healthcheck 状态，超过 30 秒 exit 1）。
- 验收条件：Given Docker Compose 服务全部未启动；When 执行 `docker compose up -d --wait`；Then 30 秒内命令返回 0，`docker compose ps` 显示所有服务状态为 healthy。

---

## PM-08 [兼容性风险] FR-5.3 Subscriber 接口签名与 kernel 定义不一致

**问题描述**

spec FR-5.3 描述 RabbitMQ Subscriber 实现 `kernel/outbox.Subscriber` 接口，签名为：

> `Subscribe(ctx, topic, handler) error`

但 spec 同时写道 "支持 consumer group（queue binding）、prefetch count 配置"。kernel 层的 `Subscriber.Subscribe` 方法签名是固定的（`Subscribe(ctx context.Context, topic string, handler func(context.Context, Entry) error) error`），不接受 consumer group 或 prefetch 参数。

如果开发者需要配置 consumer group 和 prefetch，这些参数应通过构造函数（`NewSubscriber(config)` 时传入）还是通过方法参数传入？如果是方法参数，则需要扩展 kernel 接口（违反 NFR-2 "接口实现不得扩展 kernel 接口签名"）。

**建议修改**

在 FR-5.3 中明确：consumer group 和 prefetch count 是 `SubscriberConfig` 的构造参数，在 `NewSubscriber(conn *Connection, cfg SubscriberConfig)` 时注入，不影响 `Subscriber.Subscribe` 方法签名。示例：

```go
type SubscriberConfig struct {
    QueueName     string // consumer group 对应的 queue
    PrefetchCount int    // 默认 10
}
```

这样既满足配置需求，又不违反 NFR-2。

---

## PM-09 [验收标准缺失] FR-11.3 Update user 扩展字段缺少 API 契约定义

**问题描述**

FR-11.3 要求"扩展可更新字段（name、email、status），部分更新语义"，但未定义：

1. HTTP 方法和语义 -- 使用 PATCH（部分更新）还是 PUT（全量替换）？
2. 请求体格式 -- 缺失字段表示"不更新"还是"设为零值"？
3. status 字段的合法值枚举 -- 开发者能设哪些 status？是否有状态机约束？
4. 向后兼容性 -- 现有只支持 email 的客户端是否需要修改？

这条修复虽标为 P3 优先级，但涉及 API 契约变更（可能影响 P4 persona 评估者对 API 稳定性的判断），需要明确约束。

**建议修改**

在 FR-11.3 中补充：

- HTTP method: `PATCH /api/v1/users/{id}`
- 请求体: JSON merge patch 语义（RFC 7396），缺失字段不更新
- 可更新字段: `name` (string, optional), `email` (string, optional, 需唯一校验), `status` (enum: active/suspended, optional)
- 向后兼容: 现有仅发送 `{"email":"..."}` 的客户端行为不变
- 验收条件: Given user 存在且 status=active；When PATCH 仅发送 `{"name":"newName"}`；Then 只有 name 被更新，email 和 status 保持不变

---

## 成功标准覆盖矩阵

下表验证 product-context 12 条成功标准是否在 spec 中有对应 FR 和可验证的验收条件。

| # | 成功标准 | 对应 FR | 验收条件是否可验证 | 状态 |
|---|---------|---------|-------------------|------|
| S1 | 6 adapter 集成测试 PASS | FR-1~6, FR-8.1, FR-14.2 | 可验证（`go test -tags=integration`） | OK |
| S2 | outbox 全链路端到端 | FR-1.4, FR-1.5, FR-5.2, FR-5.3, FR-8.2 | 可验证（testcontainers 测试） | OK |
| S3 | Soft Gate Journey 真实验证 | FR-8.4 | **部分覆盖** -- 缺 J-config-rollback | 见 PM-01 |
| S4 | adapters/ 覆盖率 >= 80% | FR-14.1, FR-14.2, NFR-3 | 可验证（`go test -cover`） | OK |
| S5 | 零分层违反 | NFR-1, NFR-2 | 可验证（`go build`） | **有风险** -- 见 PM-06 (FR-4.4) |
| S6 | 安全类 tech-debt 清零 | FR-9 (8 条) | **验收条件不足** -- 缺 Given/When/Then | 见 PM-02 |
| S7 | 80 条 tech-debt >= 60 条 RESOLVED | FR-10 (72 条范围) | **完成定义不足** -- 缺条目清单和计数规则 | 见 PM-03 |
| S8 | Docker Compose 30s 全 healthy | FR-7.1, FR-7.2, FR-13.1 | **缺自动化验证方式** | 见 PM-07 |
| S9 | 新增直接依赖 5 个 | FR-13.3, NFR-4 | 可验证（`go.mod` diff） | OK |
| S10 | kernel/ 零退化 | FR-14.4, NFR-3 | 可验证（`go test -cover` + `go vet`） | OK |
| S11 | RabbitMQ DLQ 可观测 | FR-5.5, FR-8.3 | 可验证（testcontainers + 日志断言） | OK |
| S12 | adapter godoc 完整 | FR-12.1 | 可验证（`go doc` + 代码审查） | OK |

**总结**: 12 条成功标准中，5 条（S3, S5, S6, S7, S8）存在验收覆盖缺口，需要 spec 修订后方可达到 P1 验收要求。

---

## 优先级排序

| 编号 | 类别 | 建议优先级 | 理由 |
|------|------|-----------|------|
| PM-06 | 范围偏移 | P1 | 分层违反是架构硬约束，影响 S5 成功标准，不修正会导致编译失败或架构退化 |
| PM-02 | 验收标准缺失 | P1 | 安全类修复无可测试验收条件，影响 S6 成功标准，且安全问题不容含糊 |
| PM-01 | 验收标准缺失 | P1 | 直接导致 S3 成功标准无法满足 |
| PM-03 | 验收标准缺失 | P1 | 80 条 tech-debt 无完成定义将导致 Phase 结束时 S7 判定存争议 |
| PM-08 | 兼容性风险 | P2 | 不影响编译但影响开发者理解和 kernel 接口稳定性承诺 |
| PM-04 | 开发者体验 | P2 | 错误码不统一会在 6 个 adapter 实现后难以追溯修正 |
| PM-09 | 验收标准缺失 | P2 | API 契约变更需提前定义，否则实现与预期偏差 |
| PM-05 | 开发者体验 | P3 | 默认值是 DX 优化，不阻塞功能交付，但影响评估者体验 |
| PM-07 | 验收标准缺失 | P3 | Docker Compose 健康检查自动化是 CI 改进，手动验证可临时替代 |
