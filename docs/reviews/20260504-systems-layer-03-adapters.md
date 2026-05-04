# GoCell 系统工程逐层审查 — adapters/ 层

| 项 | 值 |
|---|---|
| 审查日期 | 2026-05-04 |
| 基线 commit | 11600a4f |
| 审查层 | `adapters/`（12 个适配器） |
| 选定维度 | ① 接口完备性 / ⑤ 故障模式 / ⑥ 可观测性探针 / ⑦ 可测试性替身 |

## 0. 摘要

adapters/ 层整体工程化水准高，`adapterutil.CloseWithDeadline` 集中了 ctx-bound shutdown 模式、`vault` / `rabbitmq` 提供成熟的 fail-fast/fail-safe 状态机与错误分类、`postgres` 编译期 `var _ outbox.Writer = ...` 接口 assertion 普及。但在跨适配器一致性上有四类系统性缺口：（a）readyz probe 命名/暴露未在所有外部依赖适配器统一（`postgres` / `redis` / `oidc` / `s3` 均缺 `Checkers()` 入口）；（b）替身（fake）实现散落在 `_test.go` 中，外部 cell 测试无法复用；（c）故障分类（transient vs permanent）只有 `vault` / `rabbitmq` 真正落地，`postgres` / `redis` / `s3` 的错误码不区分；（d）`oidc` 完全没有 fail-fast 启动探测，discovery 仅在第一次 `Provider()` 调用时按需触发。

## 1. 评级表

| 维度 | 评级 | 理由 |
|------|------|------|
| ① 接口完备性 | ✅ 已具备 | 所有适配器都用 `var _ Interface = (*X)(nil)` 编译期 assertion；RabbitMQ `AMQPChannel` / `AMQPConnection` 自定义抽象保证可替换 |
| ⑤ 故障模式 | ⚠️ 部分具备 | `vault` (`classifyVaultError`) / `rabbitmq` (`isPermanentDialError` / `isTerminalConnectionError`) 已分类瞬态/永久；`postgres` / `redis` / `s3` 用单一错误码不区分；`oidc` 无任何分类 |
| ⑥ 可观测性探针 | ⚠️ 部分具备 | 只有 `rabbitmq.Connection`、`vault.TransitKeyProvider` 实现 `Checkers()` 并用 `_ready` 后缀；`postgres` / `redis` / `s3` 仅暴露 `Health(ctx)` 但未实现 `lifecycle.ManagedResource.Checkers`；`oidc` / `websocket` / `circuitbreaker` / `ratelimit` / `otel` / `prometheus` 完全无 probe |
| ⑦ 可测试性替身 | ⚠️ 部分具备 | 真实适配器测试已正确使用 `//go:build integration`（postgres / rabbitmq / redis / vault 全部覆盖）；但导出型 fake 几乎没有：`vault.fakeVaultClient` 不导出（`adapters/vault/transit_provider_test.go:1`，包内 white-box）、`postgres.OutboxWriter` / `redis.Client` 无 inmem 同包替身；`runtime/eventbus` 提供 in-memory publisher/subscriber 但仅替代 RabbitMQ 一个适配器 |

## 2. 问题清单

#### [P0] OIDC 缺 fail-fast 启动探测，discovery 故障被推迟到首个业务请求
- **维度**：⑤ 故障模式
- **位置**：`adapters/oidc/oidc.go:55-67` (`New`)、`adapters/oidc/oidc.go:80-117` (`Provider` / `discover`)
- **复杂度**：Cx2
- **现象**：`New(cfg)` 仅做字段校验后返回，从不连 issuer；OIDC discovery 只在第一次 `Provider(ctx)` 调用时才发起，违反 GoCell "构造期 fail-fast" 约束（CLAUDE.md / go-standards 第 28 行）。issuer 不可达、JWKS 无效、TLS 失败要等到首次 JWT 校验请求才暴露，此时 readiness 已经 OK 但请求会 500。
- **建议方向**：`New(ctx, cfg)` 期间同步执行一次 `discover(ctx, true)`，连不通直接返回 `ErrAdapterOIDCDiscovery`；同时实现 `Checkers()` 返回 `oidc_ready` probe。

#### [P0] 多个核心 adapter 未实现 `lifecycle.ManagedResource.Checkers`，readyz 监控缺位
- **维度**：⑥ 可观测性探针
- **位置**：`adapters/postgres/pool.go:110-118` (`Health` 存在但无 `Checkers`)、`adapters/redis/client.go:435-441`、`adapters/s3/s3.go:144-154`
- **复杂度**：Cx2
- **现象**：根据 `.claude/rules/gocell/observability.md`，"Adapter readiness probe 使用 stable snake_case + `_ready` 后缀"。`rabbitmq` (`rabbitmq_ready`，`adapters/rabbitmq/connection.go:776-780`) 与 `vault` (`vault_transit_ready`，`adapters/vault/transit_provider.go:1134-1143`) 已落地此规约，但 postgres、redis、s3 三个生产关键依赖只暴露 `Health(ctx) error` 方法，没有 `Checkers()` 注册入口；composition root 必须手工读 `pool.Health` 包成 closure 才能挂到 `/readyz`，每个 cmd 重复代码且容易遗漏。
- **建议方向**：在 `Pool` / `Client` / `s3.Client` 上各加一个 `Checkers() map[string]func(context.Context) error` 返回 `postgres_ready` / `redis_ready` / `s3_ready`；用 archtest（如 `tools/archtest/managed_resource_test.go`）守卫所有外部依赖 adapter 必须实现 `lifecycle.ManagedResource`。

#### [P1] postgres / redis / s3 错误码不区分瞬态与永久，consumer 无法做退避决策
- **维度**：⑤ 故障模式
- **位置**：`adapters/postgres/errors.go:1-29`、`adapters/redis/client.go:24-29`、`adapters/s3/errors.go`
- **复杂度**：Cx3
- **现象**：vault 已定义 `ErrKeyProviderTransient` 并通过 `classifyVaultError` / `isTransientHTTPStatus` 分流（`adapters/vault/transit_provider.go:986-1065`），rabbitmq 用 `isPermanentDialError` + `isTerminalConnectionError` 区分（`connection.go:122-180`）。但 postgres 所有写入失败一律 `ErrAdapterPGQuery`、redis `ErrAdapterRedisGet/Set/Delete`、s3 `ErrAdapterS3Upload`，调用方无法据错误码判断"该退避重试"还是"该 reject 进 DLX"。outbox handler（`.claude/rules/gocell/eventbus.md` Disposition 语义）只能盲选 Requeue。
- **建议方向**：复用 `errcode.WrapInfra` + `errcode.IsTransient`（vault 已是范例），对 PG `40001`/`40P01`（serialization/deadlock）、`08*`（connection_exception）、Redis `i/o timeout`、S3 5xx/429 标记为 transient；其余永久。

#### [P1] OIDC provider 缓存永不过期、无后台刷新 worker，JWKS 轮换失效
- **维度**：⑤ 故障模式 + ⑥ 探针
- **位置**：`adapters/oidc/oidc.go:78-90` (`Provider`)、`adapters/oidc/oidc.go:92-96` (`Refresh`)
- **复杂度**：Cx2
- **现象**：注释（`oidc.go:77-79`）提示"caller MUST call Refresh() periodically … 24h ticker"，但 adapter 自身既没有提供 `Worker() worker.Worker`（vault/rabbitmq 都通过 `lifecycle.ManagedResource.Worker` 注入），也没有 ticker；这把刷新责任甩给每个 composition root，一旦遗漏 IdP 轮换 JWKS 就全员鉴权失败。OIDC 也未实现 `lifecycle.ManagedResource`。
- **建议方向**：Adapter 内部启动 `tokenRenewalWorker` 同款 `worker.Worker`，遵循 OIDC `cache_max_age` 头（fallback 24h）；通过 `lifecycle.ManagedResource.Worker()` 暴露给 bootstrap。

#### [P1] 适配器 fake 实现仅在 `_test.go` 内部、不可被 cells 测试复用
- **维度**：⑦ 可测试性替身
- **位置**：`adapters/vault/transit_provider_test.go:1` (white-box，`fakeVaultClient` unexported)；`adapters/postgres/` 缺 `inmem` 子包；`adapters/redis/` 缺 fake（仅 `cmdable` 内部 interface 但无导出实现）
- **复杂度**：Cx3
- **现象**：项目宪法允许 cells 通过接口解耦 adapters（CLAUDE.md "通过接口解耦"），但当 cell 单测想注入"假 KeyProvider/假 OutboxWriter/假 Client"时，必须自己重写整套接口；`runtime/eventbus` in-memory publisher 是孤例。这导致 cell 测试要么 import adapters（违反 LAYER-04），要么手写一遍 fake，违反 DRY。
- **建议方向**：每个对外接口适配器开 `adapters/<name>/<name>fake/`（参考 `pkg/testutil` 模式），导出 `NewFakeClient()` / `NewMemKeyProvider()`，并在 cells 测试约定中默认引用。

#### [P1] circuitbreaker 构造错误未走 errcode 包
- **维度**：① 接口完备性（横切：错误编码统一性）
- **位置**：`adapters/circuitbreaker/gobreaker.go:100-103` (`New`)
- **复杂度**：Cx1
- **现象**：`return nil, fmt.Errorf("circuitbreaker: Name required")`。`.claude/rules/gocell/error-handling.md` 第 1 条要求"对外暴露错误必须 `errcode.New`"，本文件违反。其他 adapter（postgres / redis / vault / oidc / s3 / rabbitmq）均统一定义 `ErrAdapter*` 常量。
- **建议方向**：定义 `ErrAdapterCircuitBreakerConfig errcode.Code = "ERR_ADAPTER_CB_CONFIG"`，改用 `errcode.New(...)`。

#### [P1] s3 / oidc 缺 `_ready` probe，且 `s3.Health` 是 HeadBucket（每次探针实际请求 S3）
- **维度**：⑥ 可观测性探针
- **位置**：`adapters/s3/s3.go:144-154` (`Health`)
- **复杂度**：Cx2
- **现象**：S3 `Health` 直接打 HeadBucket，按 README 模型 readyz 高频探测会变成持续 S3 调用（成本+配额）；rabbitmq 已采用"读内存状态机，不发请求"模式（`adapters/rabbitmq/connection.go:766-770` 注释明确说"No broker round-trip per probe"），s3 未对齐。
- **建议方向**：仿 rabbitmq 改为 "状态机 + 后台 health-check goroutine"，probe 只读最新一次后台健康检查结果；同时按 P0 条目暴露 `s3_ready` 名称。

#### [P2] postgres `Pool` 未暴露 `lifecycle.ManagedResource.Worker`，但 outbox 需要后台 relay
- **维度**：① 接口完备性
- **位置**：`adapters/postgres/pool.go:62-101`
- **复杂度**：Cx2
- **现象**：`Pool` 只满足 `lifecycle.ContextCloser`（`pool.go:17`），未实现 `ManagedResource`。outbox relay worker 是单独类型，但 Pool 自身的 background metrics 收集（如 `MaxLifetimeDestroyCount` 累积）也没有 ticker，依赖 caller 主动 `PoolStats()`。一致性差。
- **建议方向**：明确接口契约 — 要么 `Pool` 升级到 `ManagedResource(Checkers + Worker = nil)`，要么在 doc.go 写明"Pool 是 ContextCloser，不需要 Worker"。

#### [P2] `Connection.WaitConnected` 和 `MaxReconnectAttempts` 字段语义不一致
- **维度**：⑤ 故障模式（文档 vs 行为）
- **位置**：`adapters/rabbitmq/connection.go:204-220` (字段已注释 "ignored")、`connection.go:867-872` (godoc 仍提 `ErrAdapterAMQPReconnectExhausted`)
- **复杂度**：Cx1
- **现象**：`MaxReconnectAttempts` 字段 godoc 写 "retained for field compatibility but ignored"，但 `WaitConnected` 的返回错误列表仍列举了 `ErrAdapterAMQPReconnectExhausted` 错误码——按当前代码该路径永不会触发（reconnect 已变无界），文档遗漏更新。
- **建议方向**：删 `WaitConnected` godoc 中 `ErrAdapterAMQPReconnectExhausted` 条目；或者干脆删 `MaxReconnectAttempts` 字段（项目宪法"不考虑向后兼容"，无外部消费方）。

## 3. 跨层观察

**adapters → kernel/runtime 接口**：编译期 assertion 普及度高（`var _ kcrypto.KeyProvider = (*TransitKeyProvider)(nil)`、`var _ outbox.Publisher = (*Publisher)(nil)`、`var _ rtws.Conn = (*Conn)(nil)`），但 `lifecycle.ManagedResource` 这个聚合接口（Checkers + Worker + Close）没有被普遍采用——只有 vault、rabbitmq 显式实现。`postgres.Pool` / `redis.Client` / `s3.Client` 仅满足 `lifecycle.ContextCloser`，导致 readyz / worker 注入需要每个 cmd 重复 wiring。建议在 kernel/lifecycle 加 archtest：所有 `adapters/<x>/Client|Pool|Connection` 类型必须实现 `ManagedResource`。

**adapters → cells 注入边界**：CLAUDE.md 与 go-standards.md 都规定 cells 不依赖 adapters，通过接口解耦——这一约束在 production 路径已成立（cells 只 import `kernel/crypto.KeyProvider` 这种接口）。但**测试路径破洞**：cells 单测要 mock KeyProvider / OutboxWriter，今天必须自己手写 fake；`adapters/vault/transit_provider_test.go` 中的 `fakeVaultClient` 是 unexported white-box，外面拿不到。这把"接口解耦"的好处局限在 prod，没传导到 test，违反 LAYER-04 的精神。建议参考 `runtime/eventbus`（in-memory publisher 公开导出）模式，对每个外向接口提供导出 fake 子包。

**adapters 内 utility 集中度**：`adapters/adapterutil` 已正确收敛 `CloseWithDeadline`，避免了过去 4 个 adapter 复制粘贴的"goroutine + select + slog"模式，这是非常好的工程实践。可以继续把 `Health → Checkers map` 包装、`Status → metric` 转换也下沉到此包。

## 4. 一句话结论

adapters 层"重型 adapter"（rabbitmq / vault）已达到生产级故障处理与探针标准，但"轻型 adapter"（postgres / redis / oidc / s3）在故障分类、readyz 探针接入、可测试替身导出三方面与重型不一致，需要一轮系统性补齐，否则每个 composition root 都得重复处理这些应当在 adapter 层解决的问题。
