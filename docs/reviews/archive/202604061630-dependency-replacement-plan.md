# GoCell 依赖替换计划

> 日期: 2026-04-06
> 基准: develop @ 643b214
> 状态: 待评审

---

## 1. 执行总结

经 6 个研究团队 + 4 个影响面分析，结论如下：

- **立即替换 3 个**（安全/正确性风险）
- **建议替换 4 个**（减少维护负担）
- **新增 2 个适配层**（生产就绪）
- **删除 1 个**（设计矛盾）
- **保留 15+ 个**（领域特定或已合理）
- **不替换 1 个**（Watermill，架构不匹配，留 Fix Pack 远期评估）

---

## 2. 替换项目详表

### Phase 0: 立即执行（安全风险，0.5-1d）

| # | 模块 | 替换为 | Stars | 影响面 | 预估 |
|---|------|--------|-------|--------|------|
| 0-1 | `adapters/s3` 手写 SigV4 签名 | **aws/aws-sdk-go-v2** (官方) | 2.8k | 零 import，接口隔离 | 0.5d |
| 0-2 | `adapters/oidc` 手写 JWKS 解析 | **coreos/go-oidc v3** (CoreOS/Red Hat) | 6.1k | 零 import，未使用 | 0.5d |
| 0-3 | `adapters/redis/distlock.go` FenceToken | **删除** | — | 零调用者 | 0.5h |

**0-1 详细方案:**
- 删除手写 SigV4 签名代码 (~250 行)
- 引入 `github.com/aws/aws-sdk-go-v2/service/s3`
- 保留 GoCell 的 `ObjectUploader` 接口，adapter 内部用 SDK
- MinIO 兼容: `UsePathStyle: true`

**0-2 详细方案:**
- 删除手写 OIDC discovery + JWK 解析 (~280 行)
- 引入 `github.com/coreos/go-oidc/v3`
- 保留 errcode 包装和 GoCell Config 绑定的薄 adapter

**0-3 详细方案:**
- 删除 `FenceToken()` 方法 + `fenceTokenScript` Lua + 对应测试
- 文档注释强化 efficiency lock only 声明
- 正确性场景用 Postgres 事务/乐观锁

---

### Phase 1: 快速收益（低风险，1-2d）

| # | 模块 | 替换为 | Stars | 影响面 | 预估 |
|---|------|--------|-------|--------|------|
| 1-1 | `pkg/uid` 手写 UUIDv4 | **google/uuid** (Google 官方) | 5.4k | 12 文件 18 调用点 | 0.5d |
| 1-2 | shutdown/bootstrap `firstErr` | **errors.Join** (stdlib) | — | 2 文件 6 行 | 0.5h |
| 1-3 | recovery/requestID/realIP 中间件 | **go-chi/chi/v5/middleware** (已有依赖) | 18.8k | 3 文件 ~200 行 | 0.5d |

**1-1 详细方案:**
- `go get github.com/google/uuid`
- 全局替换 `uid.New()` → `uuid.NewString()`
- 新增 helper: `func NewWithPrefix(prefix string) string { return prefix + "-" + uuid.NewString() }`
- 可放在 `pkg/uid` 内，改为包装 google/uuid，或直接内联

**1-2 详细方案:**
- `runtime/shutdown/shutdown.go:78-93` — `firstErr` 改为 `var errs []error` + `errors.Join(errs...)`
- `runtime/bootstrap/bootstrap.go:346-355` — 同上
- 零新依赖，Go stdlib

**1-3 详细方案:**
- 删除 `runtime/http/middleware/recovery.go` → 用 `chi/middleware.Recoverer`
- 删除 `runtime/http/middleware/request_id.go` → 用 `chi/middleware.RequestID`
- 删除 `runtime/http/middleware/real_ip.go` → 用 `chi/middleware.RealIP`
- 保留自定义: access_log.go, body_limit.go, rate_limit.go, security_headers.go
- 注意: chi Recoverer 输出格式可能与当前 slog JSON 不同，需确认

---

### Phase 2: 中等收益（需评审，2-3d）

| # | 模块 | 替换为 | Stars | 影响面 | 预估 |
|---|------|--------|-------|--------|------|
| 2-1 | `adapters/postgres/migrator` | **pressly/goose v3** | 7.5k | 零 import | 1d |
| 2-2 | `runtime/observability` 生产适配 | **go.opentelemetry.io/otel** + **prometheus/client_golang** | 5.3k + 56k | 已有接口，建 adapter | 1-2d |

**2-1 详细方案:**
- 引入 `github.com/pressly/goose/v3`
- goose 原生支持: pgx driver, embed.FS, advisory lock, up/down/status
- 保留现有 migration SQL 文件结构 (`{version}_{name}.up.sql`)
- 删除 ~418 行自建 migrator

**2-2 详细方案:**
- 升 OTel 为直接依赖 (已是 v1.41.0 间接依赖)
- 新建 `adapters/otel/` — 实现 `observability.Tracer` 接口
- 新建 `adapters/prometheus/` — 实现 `observability.Collector` 接口
- 保留 `runtime/observability/` 的接口定义和 dev/test 实现
- 保留 `runtime/observability/logging` 的 slog Handler

---

### 不替换: RabbitMQ → Watermill

| 维度 | 评估 |
|------|------|
| 架构匹配 | **不匹配** — GoCell callback-based vs Watermill channel-based |
| 代码量 | 1,198 行非测试代码，替换需要同等量的适配层 |
| 接口耦合 | `kernel/outbox.Publisher/Subscriber` 被 cells 广泛使用 |
| 净收益 | 低 — 需要 Watermill→GoCell 适配层，总代码量不减反增 |
| 决定 | **留 Fix Pack 远期评估**，当前 Solution B 修正更务实 |

---

## 3. 保留项目（确认不替换）

| 模块 | 理由 |
|------|------|
| `pkg/errcode` | ~100 行，领域特定，kernel 零外部依赖约束 |
| `pkg/httputil` | ~115 行，紧耦合 errcode |
| `kernel/*` 全部 | 领域模型（cell/slice/outbox/idempotency/metadata/governance/assembly/scaffold/journey），无等效替代 |
| `runtime/auth` | 已用 golang-jwt/jwt v5，薄封装合理 |
| `runtime/eventbus` | 进程内 dev/test bus，替换需同等适配 |
| `runtime/worker` | errgroup 只能替 Start 不能替 Stop，净收益低 |
| `runtime/config` | 340 行够用，不值得引入外部依赖 |
| `runtime/shutdown` | 97 行，trivial |
| `runtime/bootstrap` | 领域强耦合，已参考 fx |
| `adapters/redis/distlock` (Acquire/Release/Renewal) | efficiency lock 实现正确，redsync 不提供 renewal |
| `adapters/redis/cache + idempotency` | 薄封装 go-redis，领域接口 |
| `adapters/postgres/outbox` | 紧耦合 kernel outbox.Entry，~400 行 |
| `adapters/websocket` | 已用 nhooyr.io/websocket，Hub 是领域逻辑 |
| `adapters/rabbitmq` | 短期走 Solution B 修正，长期再评估 Watermill |

---

## 4. 新增依赖汇总

| 依赖 | 版本 | 类型 | 用途 |
|------|------|------|------|
| `github.com/aws/aws-sdk-go-v2/service/s3` | latest | 直接 | 替换手写 SigV4 |
| `github.com/coreos/go-oidc/v3` | v3 | 直接 | 替换手写 OIDC |
| `github.com/google/uuid` | latest | 直接 | 替换手写 UUID |
| `github.com/pressly/goose/v3` | v3 | 直接 | 替换手写 migrator |
| `go.opentelemetry.io/otel` | v1.41+ | 直接 (从间接升) | tracing adapter |
| `github.com/prometheus/client_golang` | latest | 直接 | metrics adapter |

已有依赖不变: `go-chi/chi/v5`, `golang-jwt/jwt/v5`, `rabbitmq/amqp091-go`, `redis/go-redis/v9`, `jackc/pgx/v5`, `nhooyr.io/websocket`

---

## 5. 删除代码汇总

| 文件 | 行数 | 原因 |
|------|------|------|
| `adapters/s3/*.go` (SigV4 部分) | ~250 | aws-sdk-go-v2 替换 |
| `adapters/oidc/verifier.go` + `provider.go` | ~280 | go-oidc 替换 |
| `adapters/redis/distlock.go` FenceToken + Lua | ~20 | 设计矛盾，零调用者 |
| `adapters/postgres/migrator.go` | ~418 | goose 替换 |
| `runtime/http/middleware/recovery.go` | ~60 | chi middleware |
| `runtime/http/middleware/request_id.go` | ~70 | chi middleware |
| `runtime/http/middleware/real_ip.go` | ~70 | chi middleware |
| **总计** | **~1,168** | |

---

## 6. 执行顺序与 Review Plan 对齐

```
Phase 0 (立即, 安全风险)
  0-1 aws-sdk-go-v2 替换 S3
  0-2 go-oidc 替换 OIDC
  0-3 删除 FenceToken
    ↓
Phase 1 (快速收益)
  1-1 google/uuid 替换 uid
  1-2 errors.Join 修 shutdown
  1-3 chi middleware 替换 3 个重复
    ↓
Phase 2 (需评审)
  2-1 goose 替换 migrator
  2-2 OTel + Prometheus adapter
    ↓
继续 Review Plan
  PR#39 合并 → PR#40 Outbox Relay → PR#42 Solution B
  R1D-4/5/6 Review → R1E cells → R2 数据流
```

Phase 0 + 1 可以合成一个 PR，Phase 2 各自独立 PR。

---

## 7. 风险与缓解

| 风险 | 缓解 |
|------|------|
| aws-sdk-go-v2 依赖树大 | 只引入 s3 service，用 `go mod tidy` 精简 |
| chi middleware 行为差异 | Recovery 输出格式需对比，可能需保留 slog 集成 |
| goose migration 文件格式 | 与现有 `{version}_{name}.up.sql` 兼容，需验证 |
| uid 全局替换遗漏 | 用 `go build ./...` 编译驱动，遗漏会编译失败 |

---

## 8. 参考文档

| 文档 | 路径 |
|------|------|
| 后续重构路线图 | `docs/reviews/202604061530-post-pr38-roadmap.md` |
| PR#38 Solution B 报告 | `docs/reviews/202604061449-pr38-solution-b-report.md` |
| Redis 底座决策 | `docs/reviews/202604061401-pr39-six-role/PR39-go-redis-base-plan.md` |
| Outbox Relay 跟进 | `docs/reviews/202604061401-pr39-six-role/PR39-postgres-outbox-followup.md` |
| Review 执行计划 | `docs/reviews/202604060739-review-plan/202604060830-001-review-plan.md` |
