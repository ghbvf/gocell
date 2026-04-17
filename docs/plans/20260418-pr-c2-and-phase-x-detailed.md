# PR-C2 + Phase X 详细实施说明

> 生成: 2026-04-18
> 前置: PR #169（PR-C1）合并后
> 归属: `docs/plans/20260418-bottom-up-implementation-plan.md` Phase C + Phase X

---

## PR-C2: config-pg-e2e + pilot template — pilot 最终收口

**规模**: ~4-5h 工程量，1 个 PR
**分支**: `feat/pr-c2-config-pg-e2e`
**基准**: PR-C1 合并后的 develop

### 目标

- 把 PR-C1 沉淀的 PG 接线跑通 **3-container 真实 e2e**（P4-TD-05）
- 清理 **CONFIG-DEMO-FAILOPEN-01**（durable/demo 分界线真正压实）
- 产出 **pilot 模板文档**（`docs/patterns/pg-cell-template.md`），让 access-core / audit-core 批量迁移按套路走
- 验证 **durable fail-fast 矩阵**（PR#165 固化的 L2 契约在真 PG 下）

### 范围

**IN scope**:
- docker-compose 3-container（PG + RabbitMQ + core-bundle app）
- 端到端 journey：`config write → PG config_entries + outbox_entries 原子 → relay → RMQ → subscriber`
- 移除 `configpublish.WithDemoFailOpen` option + 调用点
- fail-fast 矩阵测试（PG 断开 / outbox relay 挂 / RMQ 挂 时的行为）
- pilot 模板文档（7 节：migration / repo / session / TxManager / wire / test / lifecycle）

**OUT of scope**（推到 Phase X）:
- access-core / audit-core 的实际 PG 迁移（PR-X-PG-REPO）
- S15 拓扑单一源重构
- S16 资源框架化
- KMS 字段加密（S13）

### 任务分解

#### Task C2-1: CONFIG-DEMO-FAILOPEN-01 移除（1h, Cx2）

**现状**: `cell.go` 目前在 demo 模式下通过 `WithRunMode(runMode)` 让 publisher 失败被 swallow；PR#167 已把 `WithDemoFailOpen` 替换为 cell 级 RunMode。但 `configpublish/service.go` 仍有 fail-open 分支逻辑，耦合 demo 判断。

**改动**:
- `configpublish/service.go`: 把 `runMode.IsDemo()` fail-open 分支收束到一个 helper `handlePublisherError(ctx, err) error`，明确 durable 返回 err / demo log+nil
- 补单测：`TestPublishEvent_Durable_FailsClosedOnPublisherError` + `TestPublishEvent_Demo_LogsAndContinues`
- 删掉 `WithDemoFailOpen` option 定义和所有调用点（grep 确认清零）

**文件**:
- `cells/config-core/slices/configpublish/service.go`（改）
- `cells/config-core/slices/configpublish/service_test.go`（新增测试）
- `cells/config-core/cell.go`（删 `WithDemoFailOpen` 调用，已由 WithRunMode 替代）

#### Task C2-2: durable fail-fast 矩阵（1.5h, Cx2）

**目标**: 证明 PR#165 固化的"durable 必须 fail-closed"在真 PG 下成立。

**矩阵**（集成测试 `//go:build integration`）:

| 场景 | 期望行为 |
|------|---------|
| PG down during Create | `svc.Create` 返回 `ErrAdapterPGConnect`，无 config_entries 写入 |
| Outbox writer panics inside tx | tx rollback，config_entries 无残留 |
| RMQ relay 下游挂（demo） | Log warning, 继续（demo fail-open） |
| RMQ relay 下游挂（durable） | Relay 状态机标 claiming → retry → dead after N attempts |
| Migration 未跑 | `configwrite.Create` 返回 `relation "config_entries" does not exist` 包装为 `ErrAdapterPGQuery` |

**文件**:
- `cells/config-core/slices/configwrite/service_failfast_test.go`（新）
- `adapters/postgres/outbox_relay_failfast_test.go`（新，relay 退到 dead 的断言）

#### Task C2-3: 3-container e2e compose + journey（1.5h, Cx2）

**compose**（复用 `tests/e2e/` 现有结构如有）:
```yaml
services:
  postgres: postgres:16-alpine + migration init container
  rabbitmq: rabbitmq:3-management
  core-bundle: build from repo + GOCELL_CELL_ADAPTER_MODE=postgres GOCELL_ADAPTER_MODE=real
```

**journey 测试**: 用 Go `testing` 驱动 docker-compose up/down：
- `TestE2E_ConfigWriteToSubscriber` — POST /api/v1/config/foo/bar → PG 有 config_entries 行 + outbox_entries published 状态 → subscriber 收到 event.config.changed.v1
- `TestE2E_ConfigRollback` — 先 write v1, write v2, rollback → 验证 config_versions 历史 + outbox_entries 两行 changed/rollback

**文件**:
- `tests/e2e/config_pilot_compose.yaml`（新）
- `tests/e2e/config_pilot_e2e_test.go`（新，`//go:build e2e`）
- `.github/workflows/ci.yml`（可选：加 e2e job，`needs: integration-test`）

#### Task C2-4: Pilot 模板文档（1h, Cx1）

**文件**: `docs/patterns/pg-cell-template.md`

**7 节结构**:
1. **Cell domain 和 ports 设计** — `internal/ports/*.go` 接口定义原则
2. **PG repo 实现** — 放在 `cells/{cell}/internal/adapters/postgres/`；用 Session 抽象；错误分类（pgx.ErrNoRows → ErrNotFound, 其他 → ErrQuery）
3. **Session + TxCtxKey** — cell 不 import adapters/postgres；key 在 kernel/persistence
4. **TxManager 注入** — `cell.go` 加 `WithPostgresDefaults` + `WithTxManager`；slice service 用 TxRunner 包域写+outbox 写
5. **Migration 约定** — goose v3 + `-- +goose no transaction` + CONCURRENTLY 索引；migration 不可改
6. **测试三层** — unit（sqlmock/fake session）→ integration（testcontainers）→ e2e（compose）
7. **Wire 接线** — `cmd/core-bundle/main.go` 的 `buildXxxCoreOpts` 模式 + pool 生命周期

每节配一段 PR-C1 的真实代码 snippet 作为参考。

### 验证计划

```bash
# Unit + Integration
go test ./cells/config-core/... ./adapters/postgres/... -race -count=1
go test -tags=integration ./cells/config-core/... ./adapters/postgres/...

# e2e
go test -tags=e2e ./tests/e2e/... -count=1

# Lint + validate + coverage
golangci-lint run ./cells/config-core/... ./adapters/postgres/...
go run ./cmd/gocell validate
# SonarCloud coverage ≥ 80% on new code
```

### 工时估算

| 任务 | 工时 |
|------|------|
| C2-1 CONFIG-DEMO-FAILOPEN 移除 | 1h |
| C2-2 durable fail-fast 矩阵 | 1.5h |
| C2-3 3-container e2e | 1.5h |
| C2-4 pilot 模板文档 | 1h |
| **总计** | **~5h（0.7 工程日）** |

### 风险

| 风险 | 缓解 |
|------|------|
| e2e test CI 跑太慢 | 独立 e2e job，nightly only；PR CI 只跑 unit+integration |
| docker-compose 版本漂移 | tag + digest 双固定（已有 CI-DIGEST-01 backlog） |
| fail-fast 矩阵 flake | retry + timeout 调优；矩阵先 local run 稳定后上 CI |

---

## Phase X: 批量迁移 + 框架抽象 + 触发项

> 定位：PR-C1/C2 沉淀 pilot 后，按优先级批量实施其他 cell PG 接入 + 框架层抽象。
> 每项独立 PR，独立 review cycle。

### 优先级总览

```
┌─────────────────────────────────────────────────────────────┐
│  Phase X 依赖图                                              │
│                                                              │
│  PR-C2（pilot 模板）                                         │
│       │                                                      │
│       ├──→ PR-X-PG-REPO-ACCESS（access-core PG 迁移）       │
│       │                                                      │
│       └──→ PR-X-PG-REPO-AUDIT（audit-core PG 迁移）         │
│                                                              │
│  并行（不依赖 pilot，独立 PR）:                              │
│                                                              │
│  PR-X-INIT-COGNIT          S10 config-core Init 拆分         │
│  PR-X-TOPOLOGY-SSOT        S15 运行拓扑单一源重构            │
│  PR-X-POOL-FRAMEWORK       S16 资源框架化                    │
│  PR-X-KMS-ADR              S13 字段加密 ADR（独立 PR）       │
│                                                              │
│  触发条件项（不排期）:                                        │
│                                                              │
│  T6 per-cell env           Trigger: PR-X-PG-REPO-ACCESS 前   │
│  T7 config_versions 索引    Trigger: >100w 行 or seq scan   │
│  S14 ctx.Canceled 归类      Trigger: 加错误指标/tracing 时   │
└─────────────────────────────────────────────────────────────┘
```

---

### PR-X-PG-REPO-ACCESS — access-core 迁移（2d）

**前置**: PR-C2 合并 + T6 GOCELL-PER-CELL-ADAPTER-01 先做

**范围**:
- Migration 006: `users` / `sessions` / `roles` / `role_assignments` 4 张表
- PG Repository 实现 5 个（UserRepo / SessionRepo / RoleRepo / RoleAssignmentRepo / SessionCache）
- Session / TxManager 接入（复用 pilot 模板）
- `AUTH-CACHE-01 激活` Redis session cache（PG 主 + Redis 缓存）
- 联动 `RBAC-ASSIGN-LEVEL-UPGRADE-01` L0→L1
- 联动 `SEED-ROLE-IFACE-01` 去 type assertion
- 联动 `ACCESS-LEVEL-AUDIT-01` slice.yaml 校正

**前置 T6 理由**: access-core 接 PG 就是第 2 个 cell 用 PG，必须先把全局 `GOCELL_CELL_ADAPTER_MODE` 拆成 `GOCELL_CONFIG_CORE_ADAPTER_MODE` + `GOCELL_ACCESS_CORE_ADAPTER_MODE`，否则 config-core 和 access-core 被锁同步切换。

**文件**（~20 files）:
- `adapters/postgres/migrations/006_create_users_sessions_roles.sql`（新）
- `cells/access-core/internal/adapters/postgres/*.go`（5 个 repo）
- `cells/access-core/cell.go`（加 `WithPostgresDefaults`）
- `cells/access-core/slices/sessionlogin|sessionrefresh|sessionvalidate|sessionlogout/service.go`（RunInTx 包装）
- `cmd/core-bundle/main.go`（新增 `buildAccessCoreOpts`）
- `cmd/core-bundle/main_test.go`（补测）

### PR-X-PG-REPO-AUDIT — audit-core 迁移（1d）

**前置**: PR-X-PG-REPO-ACCESS 合并（验证两 cell PG 共存 + T6 生效）

**范围**:
- Migration 007: `audit_entries` + `audit_hmac_chain` 2 张表
- PG Repository 实现 2 个（AuditRepo + AuditCursorRepo）
- HMAC 链完整性约束：`ALTER TABLE ADD CONSTRAINT audit_hmac_chain_prev_ref`
- audit-core 典型查询是分页 `ORDER BY created_at DESC`，需要 `CREATE INDEX CONCURRENTLY idx_audit_entries_created_at`
- auditappend slice 用 RunInTx 写域 + outbox

**HMAC 链特殊处理**: audit 链式 HMAC 必须串行写入（当前版本的 HMAC 基于上一行），PG 层加 `FOR UPDATE` 锁上一行保证链的原子性。

**文件**（~15 files）:
- `adapters/postgres/migrations/007_create_audit_tables.sql`
- `cells/audit-core/internal/adapters/postgres/*.go`
- `cells/audit-core/cell.go` + `auditappend/service.go`
- `cmd/core-bundle/main.go`（新增 `buildAuditCoreOpts`）

---

### PR-X-INIT-COGNIT — config-core Init 拆分（3h, Cx3）

**独立于 PG 迁移**，可与 pilot 并行。

**现状**: `cells/config-core/cell.go::Init()` 认知复杂度 19（`//nolint:gocognit` 临时抑制）。

**拆分**:
```go
func (c *ConfigCore) Init(ctx context.Context, deps cell.Dependencies) error {
    if err := c.BaseCell.Init(ctx, deps); err != nil { return err }
    if err := c.validateDeps(deps); err != nil { return err }  // XOR + CheckNotNoop
    if err := c.buildCursorCodec(deps); err != nil { return err }
    runMode := query.RunModeForDemo(deps.DurabilityMode == cell.DurabilityDemo)
    c.initWriteSlice()
    if err := c.initReadSlice(runMode); err != nil { return err }
    c.initPublishSlice(deps, runMode)
    c.initSubscribeSlice()
    if err := c.initFlagSlice(runMode); err != nil { return err }
    return nil
}
```

**目标**: `Init()` 复杂度 ≤9，每个 helper ≤6。

**文件**:
- `cells/config-core/cell.go`
- `cells/config-core/cell_test.go`（补每个 helper 的单元测试）

---

### PR-X-TOPOLOGY-SSOT — 运行拓扑单一源（6h, Cx3）

**S15 彻底方案**。PR-C1 只做了最小 fail-fast，本 PR 做根因治理。

**设计**:
```go
// runtime/topology/topology.go
type Topology struct {
    KeyMode       KeyMode        // DevEphemeral | Real
    DataStoreMode DataStoreMode  // Memory | Postgres
    // 未来: EventBusMode, CacheMode, ...
}

func (t Topology) RequireProductionControlPlane() bool {
    return t.DataStoreMode == Postgres  // 真数据→控制面必须 production
}

func (t Topology) AdapterInfo() map[string]string { ... }

func Resolve(env Env) (Topology, error) { ... }  // 读 env + 校验一致性
```

**集成点**（替代当前分散的 env 读取）:
- `cmd/core-bundle/main.go`: 启动时 `topology, err := topology.Resolve(...)` 一次解析，全程只传 `topology`
- `loadSecret` / `loadKeySet` / `loadCursorCodec` 改签名接 `topology.KeyMode`
- `buildMetricsHandler` / `buildVerboseOpts` 改签名接 `topology.RequireProductionControlPlane()`
- `buildConfigCoreOpts` 改接 `topology.DataStoreMode`
- `buildAdapterInfo` 直接 `topology.AdapterInfo()`

**对标**:
- go-zero `conf.MustLoad` 单配置源 fail-fast
- Kratos `config.NewSource` + wire 单入口
- K8s `ControllerContext` 传递给所有 controller

**测试**:
- `TestTopology_PostgresRequiresReal`（F-NEW-2 回归守护）
- `TestTopology_AdapterInfo_ReflectsResolved`
- 端到端：demo / real+memory / real+postgres 三组启动测试

**文件**（~12 files）:
- `runtime/topology/topology.go`（新）+ 单测
- `cmd/core-bundle/main.go`（重构）
- 若干 helper 签名更新

---

### PR-X-POOL-FRAMEWORK — 资源生命周期框架化（4h, Cx3）

**S16 彻底方案**。PR-C1 在 cmd 层手接 `defer Close + 注册 healthchecker`；本 PR 提升到 bootstrap 框架。

**设计**:
```go
// kernel/resource/lifecycle.go
type Resource interface {
    Name() string
    Health(ctx context.Context) error
    Close() error
}

// runtime/bootstrap/bootstrap.go
func WithManagedResource(r resource.Resource) Option { ... }
// 自动注册 WithHealthChecker(name, Health) + 挂 LIFO Close
```

**适配**:
- `adapters/postgres.Pool` 实现 `resource.Resource`（Name "postgres" + 已有 Health + Close）
- `adapters/redis.Client`、`adapters/rabbitmq.Connection` 同步实现（追加，不必本 PR）
- `cmd/core-bundle/main.go` 把 `defer pool.Close()` + `pgHealthCheckerOpts` 替换为 `bootstrap.WithManagedResource(pool)`

**对标**:
- Uber fx `lifecycle.Append(fx.Hook{OnStart, OnStop})`
- K8s `postStartHook / preShutdownHook`
- Kubernetes `storage_readiness_hook.go`

**LIFO**: 倒序关闭保证依赖方先关闭（HTTP server → cell → pool → DB）。

**文件**（~8 files）:
- `kernel/resource/lifecycle.go`（新）+ 单测
- `runtime/bootstrap/bootstrap.go`（WithManagedResource + LIFO shutdown）
- `adapters/postgres/pool.go`（实现接口）
- `cmd/core-bundle/main.go`（用新 API 替代手接）

---

### PR-X-KMS-ADR — 字段加密设计（ADR + 独立 PR）

**S13 CONFIG-VALUE-ENCRYPTION-01**。

**分两步**:
1. **ADR 文档**（1d）：
   - `docs/architecture/202604XX-adr-config-value-encryption.md`
   - 选型：vault transit / AWS KMS / gcp-kms / 本地 AES-GCM（开发）
   - Rotation 策略：dual-key read（key_id 存 row），write 用 current
   - DDL 影响：`config_entries` 加 `value_cipher` + `key_id` 列
   - 迁移：现有明文 `value` 分两步，先 dual-write + backfill，再切只读 cipher
   - 性能影响：encrypt/decrypt 成本估算

2. **实施 PR**（3d）：
   - Migration 008 加列
   - `cells/config-core/internal/crypto/` 新 package
   - `config_repo.go` 写时 encrypt，读时 decrypt
   - Backfill 脚本

**本 PR 只做 ADR**，实施 PR 需产品确认后再排。

---

### 触发条件项（不排期）

| ID | 触发条件 | 工时 | 优先级 |
|----|---------|------|--------|
| **T6 GOCELL-PER-CELL-ADAPTER-01** | PR-X-PG-REPO-ACCESS 前必做 | 2h | 强制 |
| **T7 CONFIG-VERSIONS-CONFIG-ID-INDEX** | `config_versions` 行数 >100w 或 EXPLAIN 显示 seq scan | 0.5h | 性能驱动 |
| **S14 ERROR-CTX-CANCELLED-CLASSIFY** | 引入 error metrics / tracing dashboard 时 | 1h | 可观测性驱动 |

---

### Phase X 总工时估算

| PR | 工时 | 前置 |
|----|------|------|
| PR-X-PG-REPO-ACCESS | 2d（16h） | PR-C2 + T6 |
| PR-X-PG-REPO-AUDIT | 1d（8h） | PR-X-PG-REPO-ACCESS |
| PR-X-INIT-COGNIT | 3h | 无（可并行） |
| PR-X-TOPOLOGY-SSOT | 6h | 无（可并行） |
| PR-X-POOL-FRAMEWORK | 4h | 无（可并行） |
| PR-X-KMS-ADR | 1d（仅 ADR，实施 PR 独立排） | 产品确认 |
| **Phase X 总计** | **~4.5 工作日**（不含 KMS 实施 PR 的 3d） | — |

---

### 推荐执行顺序（时间轴）

**周 1**（基础 + 框架）:
- 周一-周二: PR-C2 合入（5h）
- 周三-周四: PR-X-INIT-COGNIT + PR-X-TOPOLOGY-SSOT 并行（3h + 6h）
- 周五: PR-X-POOL-FRAMEWORK（4h）

**周 2**（批量迁移）:
- 周一-周二: T6 GOCELL-PER-CELL-ADAPTER（2h）+ PR-X-PG-REPO-ACCESS 启动（16h）
- 周三-周四: PR-X-PG-REPO-ACCESS 合入
- 周五: PR-X-PG-REPO-AUDIT 启动（8h）

**周 3**（收口 + ADR）:
- 周一: PR-X-PG-REPO-AUDIT 合入
- 周二-周三: PR-X-KMS-ADR 草稿 + 评审
- 周四-周五: Phase X 收尾 + Phase F（发布）准备

---

### 与原 roadmap 的映射

| PR | 原计划条目 | 关系 |
|----|------------|------|
| PR-C2 | Phase C PR-C2（原 plan） | 一致 |
| PR-X-PG-REPO-ACCESS | Phase X PR-X-PG-REPO（原 plan） | 拆细 |
| PR-X-PG-REPO-AUDIT | Phase X PR-X-PG-REPO（原 plan） | 拆细 |
| PR-X-TOPOLOGY-SSOT | S15（本次新增） | PR#169 review 彻底方案 |
| PR-X-POOL-FRAMEWORK | S16（本次新增） | PR#169 review 彻底方案 |
| PR-X-INIT-COGNIT | S10（已在 backlog） | 可独立做 |
| PR-X-KMS-ADR | S13（已在 backlog） | 独立 ADR+PR |

---

### 风险 + 缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| PR-X-PG-REPO-ACCESS session 迁移涉及 Redis cache 激活（AUTH-CACHE-01），联动复杂 | 工时膨胀 | 先 PR-X-PG-REPO-ACCESS 只做 PG（Redis 先留 in-memory），AUTH-CACHE-01 独立小 PR |
| PR-X-TOPOLOGY-SSOT 改 `cmd/core-bundle/main.go` 核心调用 | 回归面 | 先写 `topology.Resolve` 单测覆盖边界，再替换；覆盖率 100% 再合 |
| PR-X-POOL-FRAMEWORK 升级 bootstrap API | 所有 cell 接线可能需要适配 | 保持老 API 兼容；新 API 标 recommended；dev message 引导迁移 |
| HMAC 链锁争用（audit-core） | 写吞吐降低 | PG 行锁 `FOR UPDATE` 只在 AuditAppend 路径用；测量写延迟 P99；必要时改为 advisory lock |
| KMS ADR 选型争议 | 排期拖延 | 先 ADR-only PR 聚焦决策；选型共识后再排实施 PR |

---

### 成功标准

Phase X 结束时应满足：
- 3 个生产 cell 全部跑 PG（access / audit / config）
- `GOCELL_ADAPTER_MODE=real` + `GOCELL_*_ADAPTER_MODE=postgres` 各 cell 独立切换
- `/readyz` 依赖 PG health 做判断
- 关停时 pool LIFO 正确释放
- F1-3 的 "durable + in-memory" 决策彻底兑现（pilot → 3 cell 全部真路径）
- Phase F 发布 README 可以去掉"排队中"标记
