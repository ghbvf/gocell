# Phase 3 Adapters — 6 席位集成审查报告

> 审查分支: `feat/002-phase3-adapters`
> 审查基准 commit: `af24e059ef1606f86436efd15949a7640b72812f`
> 审查日期: 2026-04-05
> 变更规模: 191 files changed, 16398 insertions(+), 284 deletions(-)
> 覆盖 Wave: 0-4（全量）

---

## 上下文获取记录

| 材料 | 获取方式 | 状态 |
|------|---------|------|
| kernel-constraints.md | 直接读取 | DONE |
| git diff develop...HEAD --stat | bash 运行 | DONE |
| spec.md | 直接读取 | DONE |
| git rev-parse HEAD | bash 运行 | DONE |
| product-context.md | 直接读取 | DONE |
| 代码逐文件审查 | 直接读取源码 | DONE |
| go test 运行 | bash 执行 | DONE |

---

## 执行摘要

Phase 3 Adapters 实现了 6 个核心 adapter（postgres/redis/rabbitmq/oidc/s3/websocket），完成了分层架构（kernel/runtime/adapters/ 依赖方向合规），bootstrap 接口重构，以及主要安全加固（RS256 迁移、RealIP trusted proxy、ServiceToken timestamp 防重放、UUID 替换、refresh rotation）。

**关键问题**: 2 条 P0 Finding（SEC-04 未完成 — access-core 仍用 HS256 签发 token；outbox 写入未在事务中与业务写入原子绑定），1 条 P0 Finding（所有 integration_test.go 均为 t.Skip 存根）。3 条 P1 Finding（postgres adapter 覆盖率 46.6%；环境变量前缀不一致；outbox relay FOR UPDATE SKIP LOCKED 缺事务包裹）。多条 P2 Finding（docs/errcode 违规、deprecated pkg/id 残留、docker-compose 缺 start_period）。

---

## Finding 列表

---

### F-01 [席位 2: 安全/权限] [P0] SEC-04 未完全兑现 — access-core 仍使用 HS256 签发 JWT

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/cells/access-core/slices/sessionlogin/service.go`
- `/Users/shengming/Documents/code/gocell/src/cells/access-core/slices/sessionrefresh/service.go`
- `/Users/shengming/Documents/code/gocell/src/cells/access-core/slices/sessionvalidate/service.go`

**证据**:

`sessionlogin/service.go:188`:
```go
token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
return token.SignedString(s.signingKey)
```

`sessionrefresh/service.go:156`:
```go
token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
return token.SignedString(s.signingKey)
```

`sessionvalidate/service.go:39`:
```go
if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
```

`cell.go:143`:
```go
c.signingKey = []byte(keyStr)  // signingKey 仍为 []byte (HMAC key)
```

同时，`runtime/auth/jwt.go` 中已正确实现了 `JWTVerifier`（RS256 pinned）和 `JWTIssuer`（RS256），但 access-core cell 完全没有使用这些实现，而是仍然在 slice 层自己持有 `signingKey []byte` 并调用 HS256。

**问题描述**: FR-9.2（SEC-04）要求将 JWT 从 HS256 迁移至 RS256。`runtime/auth/` 层已正确实现 RS256，但 access-core 三个 slice（sessionlogin, sessionrefresh, sessionvalidate）均未接入。access-core 当前仍用 `jwt.SigningMethodHS256` + `[]byte` 对称密钥签发 token。验收标准"Given HS256 token; When 验证; Then 401"当前无法通过，因为 access-core 本身就在发 HS256 token。

**修复建议**: 将 sessionlogin/sessionrefresh/sessionvalidate 的 `signingKey []byte` 替换为注入 `auth.JWTIssuer`（发布）和 `auth.JWTVerifier`（验证）。AccessCore cell 的 `WithSigningKey([]byte)` Option 应替换为 `WithJWTIssuer(auth.JWTIssuer)` + `WithJWTVerifier(auth.TokenVerifier)`。

**处置状态**: OPEN

---

### F-02 [席位 1: 架构一致性] [P0] outbox 写入未与业务写入原子绑定 — L2 一致性承诺未兑现

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/cells/access-core/slices/sessionlogin/service.go`
- `/Users/shengming/Documents/code/gocell/src/cells/access-core/slices/sessionlogout/service.go`
- `/Users/shengming/Documents/code/gocell/src/cells/access-core/slices/identitymanage/service.go`
- `/Users/shengming/Documents/code/gocell/src/cells/audit-core/slices/auditappend/service.go`
- `/Users/shengming/Documents/code/gocell/src/cells/audit-core/slices/auditverify/service.go`
- `/Users/shengming/Documents/code/gocell/src/cells/config-core/slices/configwrite/service.go`
- `/Users/shengming/Documents/code/gocell/src/cells/config-core/slices/configpublish/service.go`

**证据**:

`sessionlogin/service.go:141-162`:
```go
// Persist session.
if err := s.sessionRepo.Create(ctx, session); err != nil {   // 业务写入（无事务包裹）
    return nil, fmt.Errorf("session-login: persist session: %w", err)
}
// Publish event.
if s.outboxWriter != nil {
    entry := outbox.Entry{...}
    if writeErr := s.outboxWriter.Write(ctx, entry); writeErr != nil {  // outbox 写入（也无事务包裹）
        s.logger.Error(...)
    }
}
```

outboxWriter.Write 内部调用 `TxFromContext(ctx)`。但调用点 `s.sessionRepo.Create(ctx, session)` 和 `s.outboxWriter.Write(ctx, entry)` 均未被 `TxManager.RunInTx` 包裹。两个写操作各自独立执行，不在同一个 PostgreSQL 事务中。

**问题描述**: spec FR-1.4 要求"Write must execute within TxManager transaction scope"，即业务写入 + outbox 写入必须在同一事务中完成（原子性）。当前实现两步独立写入：若 sessionRepo.Create 成功后进程崩溃，outbox entry 丢失，事件永不发布（数据与事件不一致）；若 outboxWriter.Write 成功后 sessionRepo.Create 失败（其实顺序相反，但模式相同），会有游离事件。这违反了 L2 OutboxFact 一致性承诺，是 Phase 3 核心交付价值的根本性缺失。

**修复建议**: 每个 slice service 需要接受 `*postgres.TxManager`（或抽象的 `TxRunner` 接口），将业务 repo 写入和 outboxWriter.Write 包裹在同一 `RunInTx` 中：
```go
return s.txManager.RunInTx(ctx, func(ctx context.Context) error {
    if err := s.sessionRepo.Create(ctx, session); err != nil { return err }
    return s.outboxWriter.Write(ctx, entry)
})
```

**处置状态**: OPEN

---

### F-03 [席位 3: 测试/回归] [P0] 所有 integration_test.go 均为 t.Skip 存根 — S1/S2/S3 成功标准无法验证

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/adapters/postgres/integration_test.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/integration_test.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/redis/integration_test.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/oidc/integration_test.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/s3/integration_test.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/websocket/integration_test.go`
- `/Users/shengming/Documents/code/gocell/src/tests/integration/assembly_test.go`
- `/Users/shengming/Documents/code/gocell/src/tests/integration/journey_test.go`

**证据**:

`adapters/postgres/integration_test.go`:
```go
func TestIntegration_OutboxFullChain(t *testing.T) {
    t.Skip("stub: requires PostgreSQL + RabbitMQ (docker compose up)")
}
```

`tests/integration/journey_test.go`:
```go
func TestJourney_AuditLoginTrail(t *testing.T) {
    t.Skip("stub: requires full assembly (docker compose up)")
}
func TestJourney_ConfigHotReload(t *testing.T) {
    t.Skip("stub: requires full assembly (docker compose up)")
}
func TestJourney_ConfigRollback(t *testing.T) {
    t.Skip("stub: requires full assembly (docker compose up)")
}
```

6 个 adapter 集成测试文件均为占位符。`tests/integration/` 下所有测试全为 `t.Skip`。`testcontainers-go` 未出现在 `go.mod` 中（spec S9 列为新增直接依赖之一）。

**问题描述**: 产品验收标准 S1（所有 adapter 通过集成测试）、S2（outbox 全链路端到端验证）、S3（Phase 2 Soft Gate Journey 真实验证）均依赖真实集成测试。当前所有集成测试为 `t.Skip` 存根，`make test-integration` 执行后输出全为 SKIP。这意味着 Phase 3 最核心的价值证明（L2 一致性可验证）在代码层面完全缺失。

**修复建议**: 
1. 在 `go.mod` 中添加 `testcontainers-go` 依赖；
2. 为每个 adapter 实现至少 1 个真实 testcontainers 集成测试（替换 `t.Skip`）；
3. `TestIntegration_OutboxFullChain` 必须验证业务写+outbox写同一事务->relay轮询->publish->consume->idempotency 全链路。

**处置状态**: OPEN

---

### F-04 [席位 3: 测试/回归] [P1] postgres adapter 覆盖率 46.6% — 低于 80% 要求

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/adapters/postgres/`（整包）

**证据**:

运行 `go test -cover ./adapters/postgres/...`：
```
ok  github.com/ghbvf/gocell/adapters/postgres  1.404s  coverage: 46.6% of statements
```

对比：
- `adapters/redis`: 80.8%
- `adapters/rabbitmq`: 78.4%
- `kernel/` 所有包: 93-100%

Pool 的核心生命周期方法（`NewPool` 真实连接路径、`Health`、`Close`、`Stats`）、`TxManager.RunInTx` 的顶层事务路径（只有 savepoint 路径有 mock 测试，顶层事务的 Commit/Rollback 路径未覆盖）、`Migrator.Up`/`Down`/`Status` 方法均无非集成测试覆盖。

**问题描述**: 产品验收标准 S4 要求 adapters/ 每个包覆盖率 >= 80%。Postgres adapter 46.6% 距离目标差距超过 33 个百分点。

**修复建议**: 为 Pool、TxManager 顶层事务路径、Migrator 增加使用 mock pgxpool（或接口抽象）的单元测试，覆盖 Commit、Rollback、panic 回滚等路径，无需真实数据库连接。

**处置状态**: OPEN

---

### F-05 [席位 4: 运维/部署] [P1] 环境变量前缀不一致 — .env.example 与 pool.go 使用不同前缀

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/.env.example`
- `/Users/shengming/Documents/code/gocell/src/adapters/postgres/pool.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/redis/client.go`（推断）

**证据**:

`.env.example`:
```
GOCELL_PG_DSN=postgres://gocell:gocell_dev@localhost:5432/gocell_dev?sslmode=disable
GOCELL_PG_MAX_CONNS=10
GOCELL_REDIS_ADDR=localhost:6379
```

`adapters/postgres/pool.go:49`:
```go
DSN: os.Getenv("PG_DSN"),    // 无 GOCELL_ 前缀
```

`adapters/postgres/pool_test.go:14`:
```go
for _, key := range []string{"PG_DSN", "PG_MAX_CONNS", ...}
```

`.env.example` 使用 `GOCELL_PG_*` 前缀，而代码实际读取 `PG_*`（无前缀）。开发者按照 `.env.example` 配置环境后，adapter 读取不到任何值，将使用空 DSN 导致启动失败或静默使用默认值。

**问题描述**: FR-7.4 要求 `.env.example` 定义所有 adapter 连接参数的默认值。当前文档（.env.example）与代码实现（pool.go）之间存在不一致，会导致用户按文档配置后服务无法启动。

**修复建议**: 统一选择一种前缀策略：推荐在代码中使用 `GOCELL_PG_DSN`（与 `.env.example` 对齐，也与 `runtime/auth/keys.go` 的 `GOCELL_JWT_*` 规范一致），并同步更新 `ConfigFromEnv()` 和所有相关测试。

**处置状态**: OPEN

---

### F-06 [席位 1: 架构一致性] [P1] OutboxRelay 的 FOR UPDATE SKIP LOCKED 未在事务中执行

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/adapters/postgres/outbox_relay.go`

**证据**:

`outbox_relay.go:126-135`:
```go
func (r *OutboxRelay) pollOnce(ctx context.Context) error {
    const fetchQuery = `SELECT id, ...
        FROM outbox_entries
        WHERE published = false
        ORDER BY created_at
        LIMIT $1
        FOR UPDATE SKIP LOCKED`

    rows, err := r.db.Query(ctx, fetchQuery, r.config.BatchSize)  // 直接 Query，无事务
```

`FOR UPDATE SKIP LOCKED` 必须在显式事务中才能有效。pgxpool 自动提交模式下，每个语句独立执行，`FOR UPDATE` 锁在语句结束后立即释放，无法阻止其他实例在 SELECT 和随后的 UPDATE 之间竞争抢走同一批记录（TOCTOU 竞态）。

**问题描述**: 在多实例部署场景下，两个 relay 实例可能同时拿到相同的未发布条目（SELECT 各自返回同一批），然后各自发布，导致重复发布。`FOR UPDATE SKIP LOCKED` 的正确用法是：`BEGIN; SELECT ... FOR UPDATE SKIP LOCKED; ... UPDATE published=true; COMMIT;` 在一个事务中完成。当前实现丢失了这个事务边界。

**修复建议**: 将 `pollOnce` 中的 SELECT + markPublished 包裹在显式事务中：
```go
tx, err := r.db.Begin(ctx)  // 或通过 TxManager
// ... SELECT FOR UPDATE SKIP LOCKED
// ... publish
// ... UPDATE published=true
tx.Commit(ctx)
```
`relayDB` 接口需要增加 `Begin` 方法，或将 `*pgxpool.Pool` 替换为 `TxManager`。

**处置状态**: OPEN

---

### F-07 [席位 5: DX/可维护性] [P1] 多处 fmt.Errorf 外露 — 违反 errcode 规范

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/adapters/s3/client.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/redis/distlock.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/oidc/token.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/oidc/userinfo.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go`

**证据**:

`s3/client.go:241`:
```go
return nil, fmt.Errorf("s3: create request: %w", err)
```
`s3/client.go:252`:
```go
return nil, fmt.Errorf("s3: request failed: %w", err)
```
`redis/distlock.go:171`:
```go
return "", fmt.Errorf("random token: %w", err)
```
`oidc/verifier.go:183`:
```go
return fmt.Errorf("oidc jwks: %w", err)
```
`rabbitmq/connection.go:176`:
```go
return fmt.Errorf("rabbitmq: dial: %w", err)
```

共 9 处 `fmt.Errorf` 从 adapter 方法直接返回，暴露给调用方，违反编码规范"禁止裸 `errors.New` 对外暴露，使用 `errcode` 包"。

**问题描述**: 这些错误缺少 `errcode.Code`，上层无法通过 `errors.As(*errcode.Error)` 结构化处理，且 `httputil.WriteDomainError` 无法识别，会导致 500 响应而非更具体的错误码。

**修复建议**: 将上述 9 处替换为 `errcode.Wrap(ErrAdapterXXX, "msg", err)` 形式，并在相应的 `errors.go` 中声明对应 error code。例如 `s3/client.go` 中已有 `ErrAdapterS3Config`/`ErrAdapterS3Health` 等，需补充 `ErrAdapterS3Request`。

**处置状态**: OPEN

---

### F-08 [席位 4: 运维/部署] [P1] CI 配置缺失 — 无 .github/workflows 目录

**受影响文件**: 仓库根目录（无 `.github/` 目录）

**证据**:

运行 `ls /Users/shengming/Documents/code/gocell/.github` 返回 `no .github dir`。

产品验收标准中的 S1（`go test ./adapters/... -tags=integration` 全部 PASS）以及 spec FR-8 要求 CI 环境有 Docker 支持集成测试执行。当前没有任何 CI 配置，Phase 3 的集成测试无法在 PR 合并时自动验证。

**问题描述**: 没有 CI pipeline，所有测试均需手动执行。对于 Phase 3 的集成测试（依赖 Docker Compose），这意味着合并质量门控完全依赖人工。

**修复建议**: 新增 `.github/workflows/ci.yml`，至少包含：`go build ./...`、`go vet ./...`、`go test ./...`（单元测试，不含 integration 标签）三步。若 CI Runner 有 Docker，可增加 `docker compose up -d --wait && go test ./adapters/... -tags=integration` 步骤。

**处置状态**: OPEN

---

### F-09 [席位 2: 安全/权限] [P2] websocket/hub.go 使用已弃用的 pkg/id 包（64 位熵）

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/adapters/websocket/hub.go`

**证据**:

`websocket/hub.go:12`:
```go
"github.com/ghbvf/gocell/pkg/id"
```

`hub.go:161`:
```go
connID := id.New("ws")
```

`pkg/id/id.go` 文件头注释：
```go
// Deprecated: Use pkg/uid which provides UUID v4 (128-bit entropy) instead of
// the 64-bit hex IDs from this package.
```

**问题描述**: FR-9.5（SEC-08）要求将 ID 生成从 `pkg/id`（64-bit）迁移到 `pkg/uid`（128-bit UUID v4）。cells/ 层已完成迁移，但 `adapters/websocket/hub.go` 仍然使用已标注为 Deprecated 的 `pkg/id`。WebSocket 连接 ID 使用低熵 ID 存在可预测性风险，尤其是在高连接量场景。

**修复建议**: 将 `hub.go` 中的 `id.New("ws")` 替换为 `uid.NewWithPrefix("ws")`，并更新 import。

**处置状态**: OPEN

---

### F-10 [席位 3: 测试/回归] [P2] 3 个 adapter 单元测试因 httptest 沙箱限制失败

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/adapters/websocket/hub_test.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/oidc/oidc_test.go`
- `/Users/shengming/Documents/code/gocell/src/adapters/s3/client_test.go`

**证据**:

`adapters/websocket` 测试运行输出：
```
panic: httptest: failed to listen on a port: listen tcp6 [::1]:0: bind: operation not permitted
FAIL  github.com/ghbvf/gocell/adapters/websocket  1.423s
```

`adapters/oidc` 测试运行输出：
```
panic: httptest: failed to listen on a port: listen tcp6 [::1]:0: bind: operation not permitted
FAIL  github.com/ghbvf/gocell/adapters/oidc  0.328s
```

`adapters/s3` 类似 panic：
```
FAIL  github.com/ghbvf/gocell/adapters/s3  0.582s
```

**问题描述**: websocket、oidc、s3 的单元测试使用 `httptest.NewServer` 启动本地 HTTP 服务，在沙箱环境中因 `bind: operation not permitted` 失败。这些测试在实际 CI 或开发者本机可以通过，但在当前执行环境下无法通过，影响测试可靠性评估。注意：这是环境限制，非代码 bug。但测试设计依赖真实网络端口是可改善的。

**修复建议**: 对于 OIDC provider 和 S3 的 HTTP 层测试，可通过注入 `http.RoundTripper` mock 替代 httptest.Server，避免网络绑定依赖，使测试在沙箱环境下也可执行。WebSocket Hub 测试可类似处理。

**处置状态**: OPEN（环境限制已知，建议改善测试隔离设计）

---

### F-11 [席位 4: 运维/部署] [P2] docker-compose.yml 缺少 start_period — 30s 健康检查超时窗口风险

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/docker-compose.yml`

**证据**:

`docker-compose.yml` rabbitmq 服务：
```yaml
healthcheck:
  test: ["CMD", "rabbitmq-diagnostics", "-q", "ping"]
  interval: 10s
  timeout: 5s
  retries: 5
```

无 `start_period`。RabbitMQ 冷启动时间通常需要 10-20 秒，在此期间健康检查失败计入 retries。5 次重试 x 10s 间隔 = 50s 最大等待，但 `docker compose up -d --wait --timeout 30` 只等 30 秒。在 CI 环境低配置机器上，RabbitMQ 可能在 30s 内启动但尚未通过健康检查。

**问题描述**: FR-7.2 要求 30 秒内全部 healthy，但缺少 `start_period` 配置导致 rabbitmq/minio 的初始启动时间占用 retries 配额，可能在 30s 内无法达到 healthy 状态，导致 `make test-integration` 失败。

**修复建议**: 为 rabbitmq 和 minio 服务添加 `start_period: 15s`，使初始化期间的失败不计入 retries 配额：
```yaml
healthcheck:
  start_period: 15s
  interval: 10s
  timeout: 5s
  retries: 3
```

**处置状态**: OPEN

---

### F-12 [席位 6: 产品/用户体验] [P2] outboxWriter 用 nil guard + 静默 fallback — 生产配置错误不可见

**受影响文件**:
- 7 个 cell slice service 文件（同 F-02 列表）

**证据**:

所有 cell service 采用的模式（以 `sessionlogin/service.go:149-162` 为代表）：
```go
if s.outboxWriter != nil {
    // 写 outbox
} else if pubErr := s.publisher.Publish(ctx, TopicSessionCreated, payload); pubErr != nil {
    // 直接 publish（fallback）
}
```

**问题描述**: 当 `outboxWriter` 为 nil（即没有注入 postgres adapter）时，service 静默回退到 `publisher.Publish`（in-memory or RabbitMQ direct publish），不写 outbox，不保证事务原子性。对于 L2 一致性要求的场景，这个 fallback 是危险的 — 生产部署若忘记注入 `OutboxWriter`，服务正常运行但 L2 保证悄悄失效。开发者无任何运行时警告。

**修复建议**: 去掉 nil guard fallback。对于声明了 L2 一致性的 Cell，在 `Cell.Init` 阶段强制校验 `outboxWriter != nil`，启动时 fail-fast（返回错误）而不是静默降级。对于 L1/L0 场景可保留 publisher 路径，但应通过显式配置区分，不通过 nil 值隐式控制。

**处置状态**: OPEN

---

### F-13 [席位 5: DX/可维护性] [P2] OutboxRelay 未声明 worker.Worker 接口合规

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/adapters/postgres/outbox_relay.go`

**证据**:

`outbox_relay.go` 中的编译期接口断言：
```go
var _ outbox.Relay = (*OutboxRelay)(nil)
// 无 var _ worker.Worker = (*OutboxRelay)(nil)
```

`OutboxRelay` 实现了 `Start(ctx) error` 和 `Stop(ctx) error`，与 `worker.Worker` 接口签名完全相同，但没有编译期断言。spec FR-1.5 和 kernel-constraints.md C-14 明确要求"OutboxRelay implements worker.Worker for bootstrap integration"。

**问题描述**: 缺少编译期 `var _ worker.Worker = (*OutboxRelay)(nil)` 断言，若 worker.Worker 接口将来增加方法，或 OutboxRelay.Start/Stop 签名改变，编译不会告警。同时文档意图（通过 `bootstrap.WithWorkers(relay)` 注册）未在代码层面强制保证。

**修复建议**: 在 `outbox_relay.go` 添加：
```go
import "github.com/ghbvf/gocell/runtime/worker"
var _ worker.Worker = (*OutboxRelay)(nil)
```

**处置状态**: OPEN

---

### F-14 [席位 6: 产品/用户体验] [P2] testcontainers-go 未列入 go.mod — S9 依赖可控承诺失效

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/go.mod`

**证据**:

`go.mod` 中 `require` 块（直接依赖）：
```
github.com/fsnotify/fsnotify v1.8.0
github.com/go-chi/chi/v5 v5.2.5
github.com/golang-jwt/jwt/v5 v5.3.1
github.com/jackc/pgx/v5 v5.9.1
github.com/rabbitmq/amqp091-go v1.10.0
github.com/redis/go-redis/v9 v9.18.0
github.com/stretchr/testify v1.11.1
golang.org/x/crypto v0.49.0
gopkg.in/yaml.v3 v3.0.1
nhooyr.io/websocket v1.8.17
```

`testcontainers-go` 不在列表中，也不在 `go.sum` 中。产品验收标准 S9 声明新增直接依赖包含 `testcontainers-go`，但实际 F-03 已揭示集成测试均为存根，依赖自然也未引入。

**问题描述**: S9（外部依赖可控）声明"Phase 3 新增直接依赖 5 个"，其中包含 `testcontainers-go`，但 `go.mod` 中没有该依赖。这与 F-03（集成测试全为存根）共同表明集成测试层完全未实现。

**修复建议**: 与 F-03 一起修复。添加 `testcontainers-go` 依赖并实现真实集成测试后自然引入。

**处置状态**: OPEN（与 F-03 联动）

---

### F-15 [席位 1: 架构一致性] [P2] bootstrap 保留 WithEventBus 的具体类型参数

**受影响文件**:
- `/Users/shengming/Documents/code/gocell/src/runtime/bootstrap/bootstrap.go`

**证据**:

`bootstrap.go:86-91`:
```go
// WithEventBus is a convenience method that sets both Publisher and Subscriber
// from an InMemoryEventBus. It is equivalent to calling WithPublisher(eb) and
// WithSubscriber(eb). Retained for backward compatibility.
func WithEventBus(eb *eventbus.InMemoryEventBus) Option {
    return func(b *Bootstrap) {
        b.publisher = eb
        b.subscriber = eb
    }
}
```

**问题描述**: kernel-constraints.md KS-06 风险已部分修复（新增了 `WithPublisher` + `WithSubscriber` 接口方法），这是正确的方向。但 `WithEventBus` 仍然保留并接受具体类型 `*eventbus.InMemoryEventBus`。如果下游用户从文档/示例中找到 `WithEventBus`，仍然被绑定到 InMemory 实现。

**问题描述**: 这是 P2 因为已有替代方案，但 `WithEventBus` 的存在会引导用户使用不符合生产要求的实现，且未标注 `Deprecated`。

**修复建议**: 在 `WithEventBus` 的 godoc 中添加明确的弃用标注：
```go
// Deprecated: use WithPublisher and WithSubscriber with concrete adapter
// implementations (e.g., rabbitmq.Publisher, rabbitmq.Subscriber).
// This is retained for backward compatibility in tests and in-memory scenarios only.
```

**处置状态**: OPEN

---

## 合规检查矩阵

| 约束 | 状态 | 说明 |
|------|------|------|
| kernel/ 不引入 adapters/ | PASS | `grep '"github.com/ghbvf/gocell/adapters' src/kernel/` 返回 0 |
| adapters/ 不引入 cells/ | PASS | `grep '"github.com/ghbvf/gocell/cells' src/adapters/` 返回 0 |
| runtime/ 不引入 adapters/ | PASS | 注释中提及 adapters/ 但无实际 import |
| outbox.Writer 接口断言 | PASS | `var _ outbox.Writer = (*OutboxWriter)(nil)` 存在 |
| outbox.Relay 接口断言 | PASS | `var _ outbox.Relay = (*OutboxRelay)(nil)` 存在 |
| outbox.Publisher 接口断言 | PASS | `var _ outbox.Publisher = (*Publisher)(nil)` 存在 |
| outbox.Subscriber 接口断言 | PASS | `var _ outbox.Subscriber = (*Subscriber)(nil)` 存在 |
| idempotency.Checker 接口断言 | PASS | `var _ idempotency.Checker = (*IdempotencyChecker)(nil)` 存在 |
| worker.Worker 接口断言 (OutboxRelay) | FAIL | 缺失（见 F-13） |
| kernel/ 覆盖率 >= 90% | PASS | assembly 95.6%, cell 99.2%, metadata 97.1% 等 |
| adapters/ 覆盖率 >= 80% | PARTIAL FAIL | postgres 46.6% 不达标（见 F-04）；redis 80.8% PASS；rabbitmq 78.4% 略低 |
| JWT RS256 迁移 | FAIL | runtime/auth 已实现，access-core 未接入（见 F-01） |
| SEC-03 密钥环境变量化 | PARTIAL PASS | runtime/auth/keys.go 实现 GOCELL_JWT_* 读取，但代码实际读取 PG_DSN 无 GOCELL_ 前缀（见 F-05） |
| SEC-06 RealIP trusted proxies | PASS | real_ip.go 实现 trustedProxies 参数 |
| SEC-07 ServiceToken timestamp | PASS | servicetoken.go 实现 5m 窗口 |
| SEC-08 UUID crypto/rand | PARTIAL PASS | cells/ 已迁移到 uid.NewWithPrefix，adapters/websocket 仍用 id.New（见 F-09） |
| SEC-09 signing method 校验 | PARTIAL PASS | runtime/auth/jwt.go 已实现，access-core 未使用（见 F-01） |
| SEC-10 refresh rotation | PASS | sessionrefresh 实现 reuse detection |
| SEC-11 认证中间件 | PASS | AuthMiddleware 实现 public/protected 端点区分 |
| DLQ 可观测 | PASS | consumer_base.go 实现 DLQ slog.Error 记录 |
| go build ./... | PASS | 无编译错误 |
| go vet ./... | PASS | 无 vet 警告 |
| 禁止 fmt.Println/log.Printf | PASS | 适配层和运行时均使用 slog |
| errcode 包统一 | PARTIAL FAIL | 9 处 fmt.Errorf 外露（见 F-07） |
| docker-compose 健康检查 | PARTIAL PASS | 缺 start_period（见 F-11） |

---

## 成功标准对照

| 标准 | 状态 | 说明 |
|------|------|------|
| S1: adapter 集成测试全 PASS | FAIL | 所有 integration_test.go 为 t.Skip（F-03） |
| S2: outbox 全链路端到端 | FAIL | 集成测试存根 + 业务写未与 outbox 原子绑定（F-02、F-03） |
| S3: Phase 2 Journey 真实验证 | FAIL | journey_test.go 全为 t.Skip（F-03） |
| S4: adapters/ 覆盖率 >= 80% | PARTIAL FAIL | postgres 46.6%（F-04） |
| S5: 零分层违反 | PASS | 依赖方向合规 |
| S6: 安全类 tech-debt 清零 | PARTIAL FAIL | SEC-04 JWT RS256 access-core 未完成（F-01） |
| S7: tech-debt >= 60/74 RESOLVED | 未验证 | tech-debt-registry 不在本次变更中，需独立审查 |
| S8: docker compose up 30s healthy | 风险 | 缺 start_period（F-11） |
| S9: 外部依赖可控 | PARTIAL FAIL | testcontainers-go 未引入（F-14） |
| S10: kernel/ 零退化 | PASS | 覆盖率维持 93-100% |
| S11: RabbitMQ DLQ 可观测 | PASS | consumer_base.go DLQ slog 记录完整 |
| S12: adapter godoc 完整 | PASS | 6 个 adapter 均有 doc.go，导出类型有注释 |

---

## P0 Finding 汇总与合并建议

| Finding | 描述 | 优先修复顺序 |
|---------|------|------------|
| F-01 | access-core 仍用 HS256，SEC-04 未完成 | 2（安全） |
| F-02 | outbox 写入未与业务写入原子绑定，L2 承诺失效 | 1（核心架构） |
| F-03 | 所有集成测试为 t.Skip 存根 | 3（验证） |

F-02 的修复（引入 TxManager）是 F-03 中 `TestIntegration_OutboxFullChain` 能够有意义实现的前提。建议修复顺序：F-02 → F-01 → F-03。

---

*本报告由 6 席位 Reviewer Agent 基于代码直接审查生成，不参考开发者 Agent 自述。*
*审查基准 commit: `af24e059ef1606f86436efd15949a7640b72812f`*
