# User Signoff -- Phase 3: Adapters

> Branch: `feat/002-phase3-adapters`
> Role: Product Manager (User Sign-off)
> Date: 2026-04-05
> Tip commit: `538b304` (chore: S6 gate PASS)
> Evidence:
>   - `evidence/go-test/result.txt` -- 全量 `go test ./...` 全部 PASS (0 FAIL)
>   - `evidence/validate/result.txt` -- 0 error, 0 warning
>   - `evidence/journey/result.txt` -- 全部 SKIP (verify journey not implemented)
> Role roster: 前端开发者=OFF (SCOPE_IRRELEVANT), 视角 A (UI) = N/A:SCOPE_IRRELEVANT

---

## 评审范围

本次评审基于以下材料：
- product-context.md (12 条成功标准, 4 层 persona)
- spec.md (15 个 FR, 8 个 NFR)
- product-acceptance-criteria.md (54 P1 + 14 P2 + 11 P3 = 79 条 AC)
- review-findings.md (15 条 Finding: 3 P0 + 5 P1 + 7 P2)
- tech-debt.md (Phase 3 新增 12 条延迟项)
- tasks.md (90/90 完成)
- gate-audit.log (S0-S6 全部 PASS)
- 6 个 adapter 源码直接审查 (doc.go, errors.go, 核心 API 文件)
- 全量 go test 证据, validate 证据, journey 证据

---

## 视角 A -- UI (N/A:SCOPE_IRRELEVANT)

GoCell 是纯后端 Go 框架，无 UI 组件。前端开发者角色在 role-roster.md 中声明为 OFF (SCOPE_IRRELEVANT)。Phase 3 交付物全部为 Go 包、Docker Compose 配置和集成测试，不涉及任何前端/UI 交付。

评分: N/A

---

## 视角 B -- 开发者 (Cell 开发者, Go 后端工程师)

**评估对象**: 日常使用 GoCell 框架开发业务 Cell 的 Go 工程师。关注 adapter API 是否直觉、错误信息是否帮助定位问题、godoc 是否充分。

### B.1 Adapter API 设计 (良好)

6 个 adapter 的公共 API 遵循一致的模式：

- 构造函数: `New*(cfg Config)` 或 `New*(ctx, cfg)` -- 统一风格，Config 使用 struct 而非 functional option，降低认知负担。
- 健康检查: 每个 adapter 提供 `Health(ctx) error`，与 `runtime/http/health/` 可直接集成。
- 关闭: `Close()` 或 `Close(ctx) error` -- 支持优雅关闭。
- 接口合规: 所有 kernel 接口实现均有编译时断言 (`var _ outbox.Writer = (*OutboxWriter)(nil)`)。

具体亮点：
- `TxManager.RunInTx(ctx, fn)` 使用 context-embedded transaction 模式，业务代码在 fn 内执行，下游 `OutboxWriter.Write` 从 context 提取 tx，开发者无需手动传递 tx 引用。这与 Watermill watermill-sql 的模式一致，对 Go 开发者来说是熟悉的 pattern。
- `ConsumerBase.Wrap(topic, handler)` 返回一个增强版 handler，开发者只需关注业务逻辑，幂等/重试/DLQ 由 ConsumerBase 处理。`PermanentError` 类型包装清晰区分了不可重试错误。
- `IdempotencyChecker` 的 `IsProcessed/MarkProcessed` 签名简洁，TTL 显式传入而非隐藏在配置中。

存在的问题：
- S3 adapter 的 `ConfigFromEnv()` 使用 `S3_ENDPOINT`, `S3_REGION` 等前缀，而 .env.example 使用 `GOCELL_S3_*` 前缀。开发者按照 .env.example 配置后，S3 adapter 会读不到值。Postgres adapter 在 F-05 修复后已统一为 `GOCELL_PG_*`，但 S3 adapter 的修复遗漏。[开发者体验]
- OIDC adapter 没有 `ConfigFromEnv()` 方法，需要开发者手动构造 Config struct。与 postgres/redis 的 `ConfigFromEnv()` 便利方法风格不一致。[开发者体验]
- `Pool.Close()` 返回 void，而 `redis.Client.Close()` 返回 error -- 关闭方法签名不统一。[开发者体验]

### B.2 错误信息质量 (良好)

6 个 adapter 的错误码体系设计良好：

- 统一前缀: `ERR_ADAPTER_PG_*`, `ERR_ADAPTER_REDIS_*`, `ERR_ADAPTER_AMQP_*`, `ERR_ADAPTER_OIDC_*`, `ERR_ADAPTER_S3_*`, `ERR_ADAPTER_WS_*` -- 前缀即告知出错的 adapter。
- 每个错误码有 godoc 注释说明含义 (如 `ErrAdapterPGNoTx` 注释 "outbox.Writer.Write was called outside a transaction")。
- 错误消息包含上下文: `"outbox: failed to insert entry e-123"`, `"postgres: health check failed"`, `"redis: idempotency check failed (key=xxx)"` -- 开发者能直接从错误消息定位出错操作和关键参数。
- 底层驱动错误被 wrap 而非直接暴露: `errcode.Wrap(ErrAdapterPGQuery, "outbox: ...", err)` 保留了完整错误链。

亮点：
- `ErrAdapterPGNoTx` ("outbox write requires a transaction in context") 是一个典型的 fail-fast 设计 -- 当开发者忘记在 RunInTx 内调用 OutboxWriter.Write 时，错误消息直接说明原因，而不是在数据库层面报一个晦涩的 nil pointer。

小缺陷：
- RabbitMQ `consumer_base.go` 在 DLQ 路由失败时仅 slog.Error 然后 return，消息会被静默丢弃。虽然 DLQ 路由失败是极端场景，但应在错误消息中建议开发者检查 DLQ exchange 配置。[开发者体验]

### B.3 Godoc 质量 (良好)

- 6 个 adapter 全部有 `doc.go`，且内容充实 -- postgres doc.go 列出了子模块（Pool, TxManager, Migrator, RowScanner；QueryBuilder 已迁至 `pkg/query.Builder`），rabbitmq doc.go 说明了实现的接口和核心特性。
- 导出类型和关键函数均有注释 (如 `TxManager.RunInTx` 的注释详细说明了 nesting 行为、panic safety、context-embedded tx)。
- kernel 接口增强: `outbox.Writer.Write` godoc 明确说明了 context-embedded transaction 约定, `outbox.Entry.ID` 标注为 "canonical idempotency identifier"。
- 对标参考: doc.go 中注明了 ref 来源 (Watermill, coreos/go-oidc) 和 adopt/deviate 决策。

不足：
- redis adapter 的 doc.go 虽然列出了功能，但缺少使用示例。postgres doc.go 也无 Example 代码段。对于评估者 (P4 persona) 来说，go doc 输出缺乏快速上手的示例。[开发者体验]

### B 视角评分: 4/5

**理由**: API 设计整体一致且直觉，错误信息质量高，godoc 覆盖完整。扣分原因：(1) S3 adapter 环境变量前缀与 .env.example 不一致，这是一个会导致开发者首次集成失败的实际问题；(2) 跨 adapter 的关闭方法签名不统一（void vs error）；(3) doc.go 缺少 Example 代码段。这些问题均属于可快速修复的 DX 改进，不阻塞功能使用。

---

## 视角 C -- 框架集成者 (平台架构师 / Tech Lead)

**评估对象**: 使用 `go get` 引入 GoCell 并在 assembly 层组装 adapter 的架构师。关注依赖管理、分层隔离、Cell 脚手架支持、errcode 可定位性。

### C.1 go get 依赖管理 (良好，有风险)

product-context.md S9 要求新增直接依赖限 5 个: `pgx/v5`, `go-redis/v9`, `amqp091-go`, `nhooyr.io/websocket`, `testcontainers-go`。

实际状态：
- pgx/v5, go-redis/v9, amqp091-go 已引入 -- 通过 go test 证据确认编译通过。
- nhooyr.io/websocket 已引入 -- websocket adapter 使用。
- testcontainers-go 未引入 go.mod -- F-14 指出此问题，tech-debt.md #7 延迟至 Phase 4。

影响：所有 `integration_test.go` 均为 `t.Skip` 存根 (F-03)，因此当前集成测试不可执行。这意味着：
- S1 (adapter 集成测试全 PASS) -- 不可验证
- S2 (outbox 全链路端到端) -- 不可验证
- S3 (Phase 2 Journey 真实验证) -- 不可验证
- `evidence/journey/result.txt` 全部 SKIP/FAIL，无真实端到端证据

对框架集成者来说，这是最大的担忧：adapter 代码结构到位，单元测试通过，但缺乏真实基础设施的集成验证。

### C.2 分层隔离 (优秀)

review-findings.md 确认：
- `adapters/**/*.go` 不 import `cells/` -- PASS
- `kernel/**/*.go` 不 import `adapters/` 或 `runtime/` -- PASS
- `runtime/**/*.go` 不 import `adapters/` 或 `cells/` -- PASS
- `go build ./...` 和 `go vet ./...` 均通过

compile-time 接口断言覆盖：
- `var _ outbox.Writer = (*OutboxWriter)(nil)` -- PASS
- `var _ outbox.Relay = (*OutboxRelay)(nil)` -- PASS
- `var _ outbox.Publisher = (*Publisher)(nil)` -- PASS
- `var _ outbox.Subscriber = (*Subscriber)(nil)` -- PASS
- `var _ idempotency.Checker = (*IdempotencyChecker)(nil)` -- PASS
- `var _ worker.Worker = (*OutboxRelay)(nil)` -- PASS (F-13 修复后)

分层隔离证据充分，架构师可信赖 adapter 不会引入循环依赖。

### C.3 doc.go 可读性 (良好)

6 个 adapter doc.go + kernel 层新增 10 个 doc.go + runtime 层补全 -- 总计覆盖了所有公共包。对 `go doc ./adapters/...` 的输出，集成者能快速了解每个包的职责、实现的接口、对标参考。

### C.4 errcode 可定位性 (良好)

错误码按 adapter 分组，前缀清晰：
- `ERR_ADAPTER_PG_*` (7 codes): CONNECT, QUERY, TX_TIMEOUT, MIGRATE, NO_TX, MARSHAL, PUBLISH
- `ERR_ADAPTER_REDIS_*` (5 codes): CONNECT, LOCK_ACQUIRED, LOCK_TIMEOUT, SET, GET
- `ERR_ADAPTER_AMQP_*` (7 codes): CONNECT, CONNECT_PERMANENT, PUBLISH, CONFIRM_TIMEOUT, SUBSCRIBE, CONSUME, RECONNECT_EXHAUSTED
- `ERR_ADAPTER_OIDC_*` (6 codes): DISCOVERY, TOKEN, JWKS, VERIFY, USERINFO, CONFIG
- `ERR_ADAPTER_S3_*` (6 codes): CONFIG, UPLOAD, DOWNLOAD, DELETE, PRESIGN, HEALTH
- `ERR_ADAPTER_WS_*` (5 codes): UPGRADE, WRITE, READ, CLOSED, ORIGIN

每个错误码有 godoc 注释。集成者可以按前缀过滤日志/监控告警。

小问题：
- `ErrAdapterRedisLockAcquire` 名值不一致（值为 `"ERR_ADAPTER_REDIS_LOCK_ACQUIRED"`）— 保留兼容，拼写修正待后续 breaking change 处理。[开发者体验]

### C.5 Cell 脚手架 (部分)

- AuditRepository PG 实现 (`cells/audit-core/internal/adapters/postgres/`) -- 已到位
- ConfigRepository PG 实现 (`cells/config-core/internal/adapters/postgres/`) -- 已到位
- `cmd/core-bundle/main.go` 接线 -- F-02 修复后 outbox.Writer 已通过 TxManager.RunInTx 绑定
- bootstrap `WithPublisher/WithSubscriber` Option 已实现 (FR-15)，`WithEventBus` 保留向后兼容

但 access-core 的 RS256 迁移采用 WithSigningMethod Option (默认仍 HS256)，tech-debt.md #9 延迟至 Phase 4 强制注入。这意味着集成者如果不显式设置 Option，access-core 仍会使用 HS256 签发 JWT。[兼容性风险]

### C 视角评分: 3/5

**理由**: 分层隔离和 errcode 体系设计优秀，doc.go 覆盖完整。但扣分因素显著：(1) 集成测试全部为 t.Skip 存根，无真实基础设施验证证据 -- 对架构师评估框架生产可行性而言，这是关键缺失 (S1/S2/S3 不可验证)；(2) testcontainers-go 未引入 go.mod，集成者无法运行集成测试；(3) RS256 迁移默认仍 HS256，需要集成者额外配置。综合来看，代码骨架和设计质量达标，但验证证据不足以让架构师做出"生产就绪"判断，因此给出 3 分 (可接受但需改进)。

---

## 视角 D -- Vibe Coder (框架评估者 / 新接触 GoCell 的开发者)

**评估对象**: 第一次 clone GoCell 仓库、尝试理解框架能力的外部开发者。关注 API 文档是否自解释、错误是否告诉你怎么修、示例是否跑得起来。

### D.1 API 文档 / godoc (良好)

- 6 个 adapter 包 doc.go 存在且描述了核心能力。
- 导出类型有注释 -- `TxManager`, `ConsumerBase`, `IdempotencyChecker`, `Hub` 等关键类型都有说明。
- kernel 接口注释增强 -- `outbox.Writer.Write`, `outbox.Subscriber.Subscribe` 的 godoc 说明了 callback 约定和错误语义。

不足：
- 无 Example_* 测试函数 -- Go 社区惯例是在 `*_test.go` 中写 `func ExampleNewPool()` 作为可运行文档。当前 6 个 adapter 均无 Example 函数，`go doc -all` 输出缺少使用示例。对评估者来说，需要阅读测试代码才能理解 API 用法。[开发者体验]
- 无集成指南文档 -- FR-12.3 要求提供集成测试指南 (如何 `docker compose up` + `go test -tags=integration`)。当前 Makefile 有 `make test-integration` target，但无独立的指南文档解释前置条件和步骤。[开发者体验]

### D.2 错误自解释 (优秀)

错误消息质量是本次交付的一个亮点：
- `"outbox write requires a transaction in context"` -- 直接告诉你问题在哪。
- `"postgres DSN is empty"` -- 零歧义。
- `"s3: endpoint is required"`, `"oidc: issuer URL is required"` -- 配置校验错误明确指出缺失字段。
- `"redis: idempotency check failed (key=xxx)"` -- 包含关键参数，可直接用于调试。

对 Vibe Coder 来说，遇到错误时基本不需要翻源码就能定位问题。

### D.3 示例可运行性 (不足)

- `examples/` 目录不在 Phase 3 范围 (Phase 4 交付) -- 明确声明为非目标，合理。
- Docker Compose + .env.example 到位，`docker compose up -d` 可以启动完整基础设施。
- 但 `go test -tags=integration` 全部 skip -- 评估者实际无法看到端到端运行效果。
- Journey 测试全部 SKIP (证据 `evidence/journey/result.txt`) -- 无法验证框架声称的 L2 outbox 事务性保证。

对评估者的实际体验：clone -> `docker compose up` -> `go test ./...` 会看到所有单元测试通过（好！），但 `go test -tags=integration ./adapters/...` 会看到全部 skip（失望）。框架声称的核心卖点 (outbox 事务性、DLQ 路由、跨 Cell 事件最终一致) 无法通过运行测试验证。

### D.4 Godoc 覆盖 (良好)

除上述 doc.go 外，还有：
- kernel 层 10 个 doc.go (assembly, cell, governance, idempotency, journey, metadata, outbox, registry, scaffold, slice)
- pkg 层 4 个 doc.go (ctxkeys, errcode, httputil, uid)
- runtime 层 9 个 doc.go (auth, eventbus, health, router, logging, metrics, tracing, shutdown, worker)

总计 29 个 doc.go 覆盖了所有公共包。`go doc ./...` 输出完整。

### D 视角评分: 3/5

**理由**: 错误自解释质量优秀 (5/5 级别)，godoc 覆盖完整 (4/5)，但示例可运行性严重不足 (2/5)。评估者能读到好文档、遇到好错误消息，但无法通过运行测试验证框架的核心承诺。Phase 3 明确将 examples/ 排除在范围外（合理），但集成测试全部 skip 是超出预期的缺失 -- spec 中的 FR-8 (testcontainers 集成测试) 是 P1 验收标准，当前全未实现。综合给出 3 分。

---

## 产品评审 7 维度

| 维度 | 评定 | 理由 |
|------|------|------|
| A. 验收标准覆盖率 | 黄 | P1 54 条: 单元测试覆盖的部分 PASS，但 FR-8 (集成测试) 5 条全部未实现 (t.Skip stub)。P2 14 条: 无 FAIL，tech-debt 计数约 65/74 达标。P3 11 条: Docker Compose 和 doc.go 到位 |
| B. UI 合规检查 | N/A | 纯后端框架，无 UI |
| C. 错误路径覆盖率 | 黄 | 单元测试覆盖了配置校验错误、连接失败、事务回滚、panic recovery。但缺少真实基础设施的错误路径验证 (网络断开重连、超时、死锁等) |
| D. 文档链路完整性 | 绿 | 29 个 doc.go 覆盖全部公共包，.env.example 齐全，adapter 错误码有 godoc，kernel 接口注释增强 |
| E. 功能完整度 | 黄 | 6 adapter 代码结构完整 (90/90 tasks done)，但集成测试 stub 意味着 FR-8 (P1) 的 5 条 AC 未通过验收 |
| F. 成功标准达成度 | 黄 | S5/S10/S11/S12 PASS; S1/S2/S3 FAIL (集成测试 stub); S4 PARTIAL (postgres 46.6%); S6 PARTIAL (RS256 默认 HS256); S7 ~达标; S8/S9 风险项 |
| G. 产品 Tech Debt | 黄 | 12 条新增延迟项 (9 TECH + 3 PRODUCT)，其中 [PRODUCT] 3 条继承自 Phase 2 DEFERRED，新增的 [TECH] 集中在集成测试和 CI 基础设施 |

---

## 成功标准逐条对照

| # | 标准 | 状态 | 证据 |
|---|------|------|------|
| S1 | 6 adapter 集成测试全 PASS | NOT_VERIFIED | integration_test.go 全 t.Skip; go test 单元测试全 PASS |
| S2 | outbox 全链路端到端 | NOT_VERIFIED | TestIntegration_OutboxFullChain = t.Skip; F-02 修复后代码层面已绑定 TxManager |
| S3 | Phase 2 Journey 真实验证 | NOT_VERIFIED | evidence/journey/result.txt 全 SKIP |
| S4 | adapters/ 覆盖率 >= 80% | PARTIAL | postgres 46.6% 不达标; redis 80.8% PASS; rabbitmq 78.4% 略低; 其余未提供数据 |
| S5 | 零分层违反 | PASS | go build + go vet + grep import 全部合规 |
| S6 | 安全类 tech-debt 清零 | PARTIAL | 7/8 已修复; SEC-04 RS256 迁移为 Option 注入，默认仍 HS256 |
| S7 | tech-debt >= 60/74 RESOLVED | LIKELY_PASS | tech-debt.md 估算 ~65 条 RESOLVED，超过 60 条阈值 |
| S8 | Docker Compose 30s healthy | LIKELY_PASS | docker-compose.yml + healthcheck 到位; 缺 start_period |
| S9 | 外部依赖可控 | PARTIAL | 4/5 已引入; testcontainers-go 未引入 |
| S10 | kernel/ 零退化 | PASS | kernel 覆盖率 93-100%; go test 全 PASS |
| S11 | DLQ 可观测 | PASS | consumer_base.go 实现 DLQ slog.Error 记录 |
| S12 | adapter godoc 完整 | PASS | 6 个 doc.go + 导出类型注释 |

---

## 评分汇总

| 视角 | 评分 | 状态 |
|------|------|------|
| A. UI | N/A | SCOPE_IRRELEVANT |
| B. 开发者 | 4/5 | PASS |
| C. 框架集成者 | 3/5 | CONDITIONAL |
| D. Vibe Coder | 3/5 | CONDITIONAL |

---

## 判定: CONDITIONAL

**理由**: 视角 B (开发者) 评分 4/5 达标；视角 C (框架集成者) 和视角 D (Vibe Coder) 均为 3/5，满足 CONDITIONAL 阈值 (>=3) 但未达到 APPROVE 阈值 (>=4)。

核心障碍集中在一个维度：**集成测试全部为 t.Skip 存根**。这直接影响：
- S1/S2/S3 三条成功标准无法验证
- FR-8 的 5 条 P1 验收标准无法通过
- 框架集成者和评估者无法运行端到端验证

代码层面的交付质量是良好的 -- API 设计一致、错误信息清晰、godoc 完整、分层隔离合规、90/90 任务完成。Phase 3 的价值在于"从 in-memory 升级到真实基础设施"，adapter 代码骨架已到位，但"可验证"的承诺尚未兑现。

---

## 修复建议 (CONDITIONAL -> APPROVE 路径)

| 优先级 | 项目 | 预期影响 |
|--------|------|---------|
| P0 | 引入 testcontainers-go 到 go.mod，实现至少 postgres + rabbitmq + redis 3 个 adapter 的集成测试 (非 skip) | S1 从 NOT_VERIFIED -> PASS |
| P0 | 实现 TestIntegration_OutboxFullChain (写入 + relay + publish + consume + idempotency) | S2 从 NOT_VERIFIED -> PASS |
| P1 | 修复 S3 adapter ConfigFromEnv 环境变量前缀 (S3_* -> GOCELL_S3_*) | 消除开发者首次集成的配置陷阱 |
| P1 | RS256 迁移默认行为从 HS256 改为 RS256 (或至少在 HS256 fallback 时 slog.Warn) | S6 从 PARTIAL -> PASS |
| P2 | 为 postgres/redis/rabbitmq 添加 Example_* 测试函数 | D 视角从 3 -> 4 |
| P2 | 补充 OIDC adapter ConfigFromEnv 方法 | 跨 adapter API 一致性 |
| P3 | 统一 Pool.Close() 签名为 Close(ctx) error | 跨 adapter 关闭方法一致性 |

完成 P0 + P1 项后，预计 C 视角可提升至 4/5，D 视角可提升至 4/5，达到 APPROVE 标准。

---

*本报告由产品经理 Agent 基于源码直接审查和测试证据生成。*
