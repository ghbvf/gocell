# PR-CFG-F: Examples Security and Consistency Baseline

## Context

本计划是 `docs/plans/202604260058-l4-virtual-taco.md` 中 PR-CFG-F 的彻底优化版，替换临时 plan `/Users/shengming/.claude/plans/tranquil-cooking-river.md` 里的旧设计分支。

目标保留原主线：examples 安全收口、real multi-pod 一致性 fail-fast、service-token replay 窗口修正、以及若干运维细节加固。实现上删除不必要设计，避免把 example/demo 策略下沉到 runtime，也避免引入新的 HS256 JWT verifier。

## Locked Decisions

- 不新增 `examples/internal/exampleauth`。三个 example 的 `main.go` 作为 composition root 直接读取 env，并调用现有 auth/cell API。
- 不新增 HS256 JWT verifier。`iotdevice`、`todoorder` 删除 demo token verifier 后使用现有 RS256 JWT verifier。
- 不把 corebundle 的 demo-key/topology 策略下沉到 `runtime/auth`。runtime 只提供通用 service-token 语义和 nonce store 接口。
- Redis username/sentinel 不在本 PR 扩展；本 PR 只使用现有 `adapters/redis.Config{Addr, Password, DB}` 能力。
- `30s` 是明确的 service-token clock-skew 上限，不是兼容 buffer。未来调整只改一个常量和对应测试。

## Change Set

### F.1 Service-token 时间窗口语义

文件：
- `runtime/auth/servicetoken.go`
- `runtime/auth/authenticator.go`
- `runtime/auth/nonce.go`
- `cmd/corebundle/controlplane.go`
- 现有 service-token / nonce 相关测试

改动：
- 在 `runtime/auth` 新增命名常量：
  - `ServiceTokenClockSkew = 30 * time.Second`
  - `ServiceTokenNonceTTL = ServiceTokenMaxAge + ServiceTokenClockSkew`
- 修改 service-token timestamp 校验：
  - 未来 token 只允许 `ServiceTokenClockSkew` 内的时钟偏移。
  - 未来超过 `ServiceTokenClockSkew` 立即拒绝。
  - 过去 token 仍按 `ServiceTokenMaxAge` 过期。
  - 删除当前 `abs(age)` 逻辑，避免未来 5 分钟 token 被接受。
- 所有 in-memory nonce store 构造统一使用 `auth.ServiceTokenNonceTTL`，删除散落的 `ServiceTokenMaxAge + 30*time.Second` / `nonceStoreBuffer`。

测试：
- future timestamp `now + 31s` 被拒。
- future timestamp `now + 30s` 可接受。
- old timestamp `now - ServiceTokenMaxAge` 被拒。
- nonce store TTL 构造使用 `ServiceTokenNonceTTL`。

### F.2 Redis-backed distributed nonce store

文件：
- 新建 `adapters/redis/nonce.go`
- 新建或扩展 `adapters/redis/nonce_test.go`
- 必要时扩展 `adapters/redis/integration_test.go`

改动：
- 新增 `redis.NonceStore`，实现 `auth.NonceStore`。
- `CheckAndMark(ctx, nonce)` 使用 Redis `SET key value NX EX ttl` 语义：
  - first-use 返回 nil。
  - duplicate 返回 `auth.ErrNonceReused`。
  - Redis I/O 或协议错误用 `errcode.Wrap(redis.ErrAdapterRedisSet, ...)` 包装。
- `Kind()` 返回 `auth.NonceStoreKindDistributed`。
- key namespace 固定在 adapter 内部，例如 `servicetoken:nonce:<nonce>`，避免和 idempotency/lock key 冲突。
- 构造函数接收现有 `*redis.Client` 和 TTL；corebundle 传 `auth.ServiceTokenNonceTTL`。

测试：
- first-use 成功。
- replay 返回 `auth.ErrNonceReused`，可用 `errors.Is` 匹配。
- TTL 过后可复用。
- Redis error 被 adapter errcode 包装。
- `Kind() == auth.NonceStoreKindDistributed`。

### F.3 Corebundle Redis wiring 与 topology dispatch

文件：
- 新建 `cmd/corebundle/redis.go`
- `cmd/corebundle/shared_deps.go`
- `cmd/corebundle/controlplane.go`
- `cmd/corebundle/bundle.go`
- `cmd/corebundle/main.go`
- `pkg/errcode/errcode.go`
- `docs/ops/env-vars.md`

改动：
- 新增 Redis env 装配：
  - `GOCELL_REDIS_ADDR`
  - `GOCELL_REDIS_PASSWORD`
  - `GOCELL_REDIS_DB`
- `real + multi-pod` 必须配置 Redis；否则 fail-fast。
- Redis client 由 `LoadSharedDepsFromEnv(ctx)` 构建一次，并复用于：
  - service-token distributed nonce store。
  - outbox `redis.IdempotencyClaimer`。
- `real + multi-pod`：
  - `InternalGuard` 使用 Redis nonce store。
  - `ConsumerBase` 使用 Redis claimer。
- `single-pod`：
  - `InternalGuard` 使用 `auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL)`。
  - `ConsumerBase` 使用 `idempotency.NewInMemClaimer()`。
- `buildConsumerBase` 改为 `buildConsumerBase(deps *SharedDeps) (*outbox.ConsumerBase, error)`，不保留旧签名。
- `SharedDeps` 增加 Redis client / consumer claimer 所需字段；`SharedDeps.Validate` 同时校验：
  - real multi-pod 下 nonce store 必须是 distributed。
  - real multi-pod 下 consumer claimer 必须是 Redis-backed distributed claimer。
- 新增 `errcode.ErrControlplaneClaimerNotDistributed`，错误码为 `ERR_CONTROLPLANE_CLAIMER_NOT_DISTRIBUTED`。
- Redis client 需要纳入 bootstrap lifecycle：
  - 关闭：`bootstrap.WithManagedCloser(redisClient)` 或等价的 managed resource wiring。
  - health：注册 Redis health checker。
  - readyz adapter info 暴露 Redis configured/in-memory 状态。

测试：
- real multi-pod 缺 Redis env fail-fast。
- real multi-pod 配 Redis 后，InternalGuard nonce store 为 distributed，ConsumerBase claimer 为 Redis-backed。
- real single-pod 使用 in-memory nonce 和 in-memory claimer。
- `buildConsumerBase(nil)` / missing claimer fail closed。
- `SharedDeps.Validate` 拒绝 real multi-pod + in-memory nonce。
- `SharedDeps.Validate` 拒绝 real multi-pod + in-memory claimer，并返回 `ErrControlplaneClaimerNotDistributed`。

### F.4 Examples auth 收口

文件：
- `examples/ssobff/main.go`
- `examples/iotdevice/main.go`
- `examples/todoorder/main.go`
- `examples/ssobff/README.md`
- `examples/iotdevice/README.md`
- `examples/todoorder/README.md`
- 必要时新增 example-local token mint helper，例如 `examples/<name>/cmd/mint-token`

改动：
- 三个 example 的 `InternalListener` 全部启用 service-token auth。
- 缺少以下 env 时直接 fail-fast，不再允许 unauth internal listener：
  - `GOCELL_SSOBFF_SERVICE_SECRET`
  - `GOCELL_IOTDEVICE_SERVICE_SECRET`
  - `GOCELL_TODOORDER_SERVICE_SECRET`
- 每个 `main.go` 直接调用：
  - `auth.NewHMACKeyRing`
  - `auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL)`
  - `cell.NewAuthServiceToken`
- `iotdevice` 删除：
  - `demoAdminToken`
  - `demoTokenVerifier`
- `todoorder` 删除：
  - `demoCustomerToken`
  - `demoTokenVerifier`
- `iotdevice`、`todoorder` 的 PrimaryListener 改用现有 RS256 JWT verifier：
  - 使用 `auth.LoadKeySetFromEnv()` 或等价现有 key provider。
  - 使用 `auth.NewJWTVerifier(..., auth.WithExpectedAudiences(...))` / `runtime/auth/config` 现有 factory。
  - 从 `GOCELL_JWT_ISSUER` / `GOCELL_JWT_AUDIENCE` 读取 issuer/audience。
- README 删除固定 bearer token，改为生成真实 RS256 测试 token：
  - 生成 RSA key pair。
  - export JWT issuer/audience/key env。
  - 用 example-local 小工具或明确命令生成带角色 claim 的访问 token。

测试：
- 缺 service secret 时三个 example fail-fast。
- 三个 example 的 InternalListener auth chain 非空，并包含 `cell.AuthServiceToken`。
- 源码不再包含 `demoAdminToken`、`demoCustomerToken`、`demoTokenVerifier`。
- `iotdevice` / `todoorder` 使用 RS256 verifier，拒绝 HS256 / unsigned token。

### F.5 运维小修

文件：
- `runtime/auth/middleware.go`
- `runtime/http/health/health.go`
- `adapters/postgres/pool.go`
- `runtime/bootstrap/bootstrap_phases.go`
- `runtime/bootstrap/managed_resource.go`
- `runtime/bootstrap/bootstrap.go`

改动：
- slog 全部改为 typed attrs：`slog.String` / `slog.Any` / `slog.Int`。
- health adapter map 在 RLock 内 shallow copy，锁外使用快照。
- `truncateErrMsg` 改为 rune-aware 截断，避免切断 UTF-8。
- `Pool.Stats()` 增加 nil receiver / nil inner 防御，返回 `"pool not initialized"`。
- pub/sub identity check 抽 helper，避免 `any(pub) != any(sub)` 在不可比较动态类型上 panic。
- managed resource teardown 错误带资源类型，复用现有 teardown descriptor `td.name`，不要扩 `ManagedResource` 接口。
- `WithAssemblyID` 注释收窄：auto-derived assembly ID 只影响 HTTP metrics collector 的 `cell_id` label，不影响 health handler / eventrouter。

测试：
- slog 捕获断言 attrs typed。
- adapter info snapshot 不受 handler 外部 map mutation 影响。
- 多字节字符串截断不产生非法 UTF-8。
- nil `Pool` / nil `Pool.inner` 不 panic。
- pub/sub same pointer / different pointer / non-comparable dynamic type 都不 panic。
- teardown phase error 包含资源类型。
- `WithAssemblyID` 仅文档行为，无新 runtime 行为。

## Implementation Order

1. `runtime/auth` 常量和 timestamp 语义，先补失败测试。
2. `adapters/redis.NonceStore` 和测试。
3. `cmd/corebundle` Redis env/client wiring、nonce/claimer dispatch、Validate fail-fast。
4. 三个 examples 的 InternalListener auth 和 `iotdevice`/`todoorder` RS256 verifier 替换。
5. README / env docs 更新。
6. 运维小修和对应 focused tests。

## Verification

```bash
go test ./runtime/auth/... ./adapters/redis/... ./cmd/corebundle/...
go test ./examples/ssobff/... ./examples/iotdevice/... ./examples/todoorder/...
go test ./runtime/http/health/... ./runtime/bootstrap/... ./adapters/postgres/...
go test ./...
go test -tags=integration -covermode=atomic \
  ./adapters/... ./tests/integration/... ./cmd/corebundle/... \
  ./examples/ssobff/... ./examples/iotdevice/... ./examples/todoorder/... \
  ./cells/accesscore/slices/identitymanage/... ./runtime/bootstrap/... -timeout 15m
golangci-lint run ./...
go run ./cmd/gocell validate --strict
```

生产 gate：

```bash
GOCELL_ADAPTER_MODE=real go run ./cmd/corebundle
# fail: missing Redis in real multi-pod, or missing service/JWT/control-plane env

GOCELL_ADAPTER_MODE=real GOCELL_SINGLE_POD=1 go run ./cmd/corebundle
# allowed only when all required single-pod real-mode secrets are set

GOCELL_ADAPTER_MODE=real go run ./examples/iotdevice
# fail: missing GOCELL_IOTDEVICE_SERVICE_SECRET and/or JWT verifier env

GOCELL_ADAPTER_MODE=real go run ./examples/todoorder
# fail: missing GOCELL_TODOORDER_SERVICE_SECRET and/or JWT verifier env
```

## Out Of Scope

- Redis username, TLS, sentinel, cluster support.
- New HS256 verifier.
- `examples/internal/exampleauth` shared package.
- Moving corebundle demo-key blacklist or topology policy into `runtime/auth`.
- Changing the core auth JWT algorithm beyond using the existing RS256 implementation.
