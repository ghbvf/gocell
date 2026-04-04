---
title: "GoCell — Cell-Native Go Engineering Foundation 最终方案 v2"
status: draft
owner: platform
audience: engineering
created: 2026-04-04
updated: 2026-04-04
source_of_truth: false
supersedes:
  - 202604041500-520-go-foundation-slice-cell-implementation-plan
  - 202604041700-521-go-foundation-final-plan
related_specs:
  - 202604041900-522-go-foundation-capability-map
  - 202604021548-512-consistency-core-cell-owned-slices-unified-plan
  - 202604022001-513-slice-cell-implementation-readiness-plan
  - 202604022013-514-greenfield-cell-native-roadmap
  - 202604030555-515-appendix-platform-tooling
  - 202604031230-516-winmdm-core-cell-slice-data-and-consistency-feasibility
  - 202604031330-519-current-code-task-classification-and-migration-plan
  - docs/products/phase/202603221900-phase1-development-plan.md
external_refs:
  - winmdm-mvp/docs/reviews/reports/202604040921-527-journey-catalog-lightweight-product-status-plan.md
  - winmdm-mvp/docs/reviews/reports/202604041138-go-foundation-discussion/202604041200-531-generic-go-foundation-core.md
  - winmdm-mvp/docs/reviews/reports/202604041138-go-foundation-discussion/202604041148-530-go-foundation-capability-tiering-analysis.md
---

# GoCell — 通用 Go Slice-Cell 工程底座最终方案 v2

## 1. 产品名称

**GoCell** — Cell-Native Go Engineering Foundation

Go + Cell — 以 Cell 为核心构建单元的 Go 工程底座。

```
github.com/xxx/gocell
```

---

## 2. 总览

### 2.1 一句话定义

GoCell = **Cell/Slice 运行时 + 治理工具链 + 一致性内核 + 安全内核 + 观测内核 + 内置 Cell + 正式 Adapter Family**

### 2.2 目标

新建独立 Go 仓库，打造以 **slice-cell 治理为核心** 的通用工程底座。全 AI 实验项目。

| 层 | 定位 | v1.0 内容 |
|---|---|---|
| **Kernel** | Cell/Slice 运行时 + 治理工具 + 一致性/安全/观测内核 | 45+ 项能力 |
| **Built-in Cells** | 开箱即用的通用 Cell | access-core + audit-core + config-core |
| **First-class Adapters** | 一等适配器 | PostgreSQL / Redis / OIDC / S3 / VictoriaMetrics |
| **Formal Adapter Family** | 正式适配器族 | RabbitMQ / WebSocket |
| **Runtime** | 通用运行时 | middleware / bootstrap / worker / observability |
| **Examples** | 示例项目 | sso-bff / todo-order / iot-device |

### 2.3 关键决策汇总

| 决策项 | 结论 | 来源 |
|-------|------|------|
| 产品名称 | **GoCell** | v2 新增 |
| 仓库策略 | **独立新仓库** | 讨论确认 |
| 底座核心 | **slice-cell Kernel 是最大差异化** | 531 + 讨论 |
| Assembly Generator | **Go 代码生成** | 讨论确认 |
| 工具形态 | **Go 库 API 为核心 + CLI 为薄包装** | 讨论确认 |
| Verify 实现 | **go test 智能包装**（unit/contract）+ 自定义 runner（journey） | 讨论确认 |
| 内置 Cell | **access-core + audit-core + config-core**（3 个） | v2 更新 |
| 内置 Journey | **8 条**（含 2 条跨 cell） | v2 更新 |
| 时序存储 | **VictoriaMetrics** | 研究推荐 |
| BI 看板 | **Grafana**（运维）+ Metabase（业务，按需） | 研究推荐 |
| 五层信息模型 | Journey Catalog / journeys/*.yaml / cell.yaml / slice.yaml / Status Board | 527 修订版 |
| Cell/Slice 运行时原语 | **必须提供** | 讨论确认 |
| Webhook | **进入 Kernel**（receiver + dispatcher） | 531 + 用户要求 |
| Feature flags | **进入 Kernel**（config-core 承载） | 531 + 用户要求 |
| Config 热更新 | **做成 Cell + Journey**（config-core） | 用户要求 |
| RabbitMQ | **提升为正式 Adapter Family** | 531 + 讨论确认 |
| WebSocket | **提升为正式 Adapter Family** | 531 + 讨论确认 |
| Reconcile runtime | **进入 Kernel** | 531 新增 |
| Scheduler / cron | **进入 Kernel**（从 v1.1 提前） | 531 |
| CQRS 多后端 | **降级**，v1 只做 PostgreSQL + Redis Streams | 用户指定 |

---

## 3. Cell/Slice 运行时原语

### 3.1 核心接口

```go
// Cell 运行时接口
type Cell interface {
    ID() string
    Type() CellType              // core | edge | support
    ConsistencyLevel() Level     // L0-L4
    Init(ctx context.Context, deps Dependencies) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Health() HealthStatus
    Ready() bool
    Metadata() CellMetadata
    OwnedSlices() []Slice
    ProducedContracts() []Contract
    ConsumedContracts() []Contract
}

// Slice 运行时接口
type Slice interface {
    ID() string
    BelongsToCell() string
    ConsistencyLevel() Level
    Init(ctx context.Context) error
    Verify() VerifySpec
    AllowedFiles() []string
    AffectedJourneys() []string
}

// Assembly 运行时
type Assembly interface {
    Register(cell Cell) error
    Start(ctx context.Context) error   // 按依赖顺序启动
    Stop(ctx context.Context) error    // 反序关闭
    Health() map[string]HealthStatus
}
```

### 3.2 使用方式

```go
app := gocell.NewAssembly("my-app")
app.Register(access.NewCore(oidcCfg, db))
app.Register(audit.NewCore(db))
app.Register(config.NewCore(db))
app.Register(myproject.NewOrderCore(db))
app.Start(ctx)
```

---

## 4. 内置 Cell（3 个）

### 4.1 access-core — SSO/BFF 认证

```yaml
# src/cells/access-core/cell.yaml（V3 格式）
id: access-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
```

Slices（5 个）：identity-manage / session-login / session-refresh / session-logout / authorization-decide
发布契约：event.session.created.v1 / event.session.revoked.v1 / event.user.created.v1 / event.user.locked.v1 / http.auth.login.v1 / http.auth.me.v1
消费契约：event.config.changed.v1
（契约关系由各 slice.yaml 的 contractUsages 声明，不在 cell.yaml 中）

能力：SSO/OIDC 登录 / 密码登录 / JWT RS256 / Session 管理 / 登录锁定 / RBAC / 服务间认证

### 4.2 audit-core — 审计追踪

```yaml
# src/cells/audit-core/cell.yaml（V3 格式）
id: audit-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_audit_core
verify:
  smoke:
    - smoke.audit-core.startup
```

Slices（3 个）：audit-write / audit-verify / audit-archive
发布契约：event.audit.integrity-verified.v1
消费契约：event.session.created.v1 / event.session.revoked.v1 / event.user.created.v1 / event.user.locked.v1 / event.config.changed.v1 / event.config.rollback.v1
（契约关系由各 slice.yaml 的 contractUsages 声明，不在 cell.yaml 中）

能力：事件消费写审计 / HMAC-SHA256 hash chain / 完整性验证 / 按月归档

### 4.3 config-core — 配置热更新 + Feature Flags（v2 新增）

```yaml
# src/cells/config-core/cell.yaml（V3 格式）
id: config-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_config_core
verify:
  smoke:
    - smoke.config-core.startup
```

Slices（4 个）：config-manage / config-publish / config-subscribe / feature-flag
发布契约：event.config.changed.v1 / event.config.rollback.v1 / http.config.get.v1 / http.config.flags.v1
消费契约：无
（契约关系由各 slice.yaml 的 contractUsages 声明，不在 cell.yaml 中）

能力：配置 CRUD / 版本管理 / 热更新事件推送 / Feature flags（功能开关/灰度/rollout）/ 版本回滚

### 4.4 三 Cell 交互关系

```
config-core ──event.config.*──→ access-core（热更新配置）
config-core ──event.config.*──→ audit-core（审计配置变更）
config-core ──event.config.*──→ 任何订阅 cell
access-core ──event.session.*──→ audit-core
access-core ──event.user.*────→ audit-core
```

### 4.5 内置 Journey（8 条）

| Journey | Cells | 验证要点 |
|---------|-------|---------|
| J-sso-login | access | OIDC 完整流程 |
| J-session-refresh | access | token 刷新 |
| J-session-logout | access | session 吊销 + 事件 |
| J-user-onboarding | access | 用户创建到可登录 |
| J-account-lockout | access | 锁定 + 解锁 |
| J-audit-login-trail | access + audit | **跨 cell** — L2 事件消费 + hash chain |
| J-config-hot-reload | config + all | **跨 cell** — 配置传播 + 健康验证 |
| J-config-rollback | config + all | **跨 cell** — 版本回滚 + 审计 |

---

## 5. 能力分层

### Layer 1: Kernel

```
kernel/
├── cell.go                # Cell / Slice / Assembly 接口 + BaseCell / BaseSlice
├── metadata/              # cell.yaml / slice.yaml / contract.yaml / assembly.yaml
├── consistency/           # L0-L4 定义 + 校验
├── outbox/                # transactional outbox writer
├── consumed/              # consumed marker（v2 显式化）
├── idempotency/           # 消费者幂等
├── replay/                # projection rebuild + checkpoint
├── reconcile/             # 最终状态收敛运行时（v2 新增）
├── verify/                # verify-slice / verify-cell / run-journey
├── trace/                 # caller trace 三层
├── governance/            # contract registry + dependency checker + impact analysis
├── catalog/               # Journey Catalog + Status Board
├── scaffold/              # new-cell / new-slice / new-contract
├── selector/              # select-targets
├── wrapper/               # traced sync/event/command wrapper
├── generator/             # assembly Go 代码生成
├── webhook/               # receiver（幂等+签名验证）+ dispatcher（outbox+重试）（v2 新增）
├── rollback/              # rollback metadata + kill switch（v2 新增）
└── support/               # support bundle / diagnostics（v2 新增）
```

### Layer 2: Built-in Cells

```
cells/
├── access/                # access-core
│   ├── cell.yaml
│   ├── slices/            # identity-manage / session-login / session-refresh / session-logout / authorization-decide
│   └── internal/          # domain / app / adapters
├── audit/                 # audit-core
│   ├── cell.yaml
│   ├── slices/            # audit-write / audit-verify / audit-archive
│   └── internal/
└── config/                # config-core（v2 新增）
    ├── cell.yaml
    ├── slices/            # config-manage / config-publish / config-subscribe / feature-flag
    └── internal/
```

### Layer 3: Runtime

```
runtime/
├── http/
│   ├── middleware/         # request_id / real_ip / recovery / access_log /
│   │                      # security_headers / body_limit / rate_limit
│   ├── health/            # liveness + readiness
│   └── router/            # chi-based
├── auth/
│   ├── jwt/               # RS256 钉扎
│   ├── servicetoken/      # 服务间认证
│   └── rbac/              # RBAC hook（v2 新增）
├── worker/                # Job/Worker 框架
├── scheduler/             # Cron/定时任务（v2 从 v1.1 提前）
├── retry/                 # retry / timeout / backoff 独立运行时（v2 新增）
├── security/
│   ├── tls/               # TLS / mTLS hook
│   └── keymanager/        # 密钥管理接口
├── observability/
│   ├── metrics/           # Prometheus/VM 兼容
│   ├── tracing/           # OpenTelemetry
│   └── logging/           # slog + log correlation
├── config/                # 通用配置加载
├── bootstrap/             # 统一启动器
└── shutdown/              # graceful shutdown
```

### Layer 4: First-class Adapters

```
adapters/
├── postgres/              # 连接 + TxManager + Migrator + 健康检查
├── redis/                 # 连接 + TLS + 分布式锁 + 健康检查
├── oidc/                  # SSO/OIDC
├── s3/                    # S3/MinIO
└── victoriametrics/       # 指标推送
```

### Layer 5: Formal Adapter Family（v2 新增层级）

```
adapters/family/
├── rabbitmq/              # Publisher + Consumer + DLQ（v2 从 Optional 提升）
└── websocket/             # signal-first + duplex 模式（v2 从无到有）
```

### Layer 6: Optional Adapters（留接口）

```
adapters/optional/
├── mysql/                 # MySQL/MariaDB
├── kafka/                 # Publisher + Consumer
├── sqlite/                # Edge 本地缓存
├── sse/                   # Server-Sent Events
├── grpc/                  # gRPC hook
├── search/                # OpenSearch/ES 抽象
├── notification/          # email / SMS / Slack
└── tenant/                # 多租户抽象
```

### Layer 7: Templates & Examples

```
templates/
├── adr/ / cell-design/ / contract-review/ / runbook/ / postmortem/ / grafana/

examples/
├── sso-bff/               # SSO + BFF 登录完整旅程
├── todo-order/            # CRUD + 事件驱动
└── iot-device/            # IoT 设备（L4 DeviceLatent）
```

---

## 6. 五层信息模型（527 修订版）

| 层 | 文件 | 职责 | 事实类型 |
|---|---|---|---|
| Journey Catalog | `journeys/catalog.yaml` | 产品全量 journey 总表 | 蓝图事实 |
| Journey Spec | `journeys/*.yaml` | 单条 journey 验收规格 | 验收事实 |
| cell.yaml | `cells/*/cell.yaml` | 稳定边界 + 治理事实 | 边界事实 |
| slice.yaml | `cells/*/slices/*/slice.yaml` | 施工映射 + 影响面 | 施工映射事实 |
| Status Board | `journeys/status-board.yaml` | 唯一动态状态快照 | 动态事实 |

---

## 7. 时序与可观测

| 组件 | 推荐 | 用途 |
|------|------|------|
| 时序存储 | VictoriaMetrics | 70x 压缩，push+pull |
| 运维看板 | Grafana | cell health / outbox lag |
| 业务分析 | Metabase（按需） | 趋势 / 合规率 |

---

## 8. 实施计划

### Phase 0：接口设计 + 项目骨架（Day 0-7）

1. 新仓库 `gocell` + `go.mod`
2. Cell / Slice / Assembly 运行时接口
3. BaseCell / BaseSlice 基础实现骨架
4. 元数据 JSON Schema（cell/slice/contract/assembly）
5. 五层信息模型数据结构
6. L0-L4 一致性等级定义
7. `examples/quickstart/` 骨架

**Gate：`app.Register(cell) + app.Start(ctx)` 模式可编译**

### Phase 1：Kernel 核心（Day 8-28）

**Week 1 — Metadata + Scaffold + Validate：**
- Cell/Slice 运行时基础实现
- metadata parser / L0-L4 validator / scaffolder / validate-meta
- Journey Catalog + Status Board 数据模型

**Week 2 — Assembly + Governance：**
- assembly generator（Go 代码生成）
- contract registry / dependency checker / select-targets

**Week 3 — Verify + Trace + Wrapper + Webhook：**
- verify-slice / verify-cell / run-journey
- caller trace / traced wrapper
- webhook receiver + dispatcher（v2 新增）
- reconcile runtime（v2 新增）
- rollback metadata + kill switch（v2 新增）
- CLI + Go 库 API

**Gate：CLI 创建示例 cell + slice + journey，全链路通过**

### Phase 2：Runtime + Built-in Cells（Day 29-63）

**Week 4 — HTTP + Config + Bootstrap：**
- 7 个中间件 / health / config loader / bootstrap / shutdown

**Week 5 — Observability + Worker + Security：**
- Prometheus/VM / OpenTelemetry / slog / Worker / Scheduler / retry runtime
- TLS / mTLS hook / KeyManager

**Week 6 — access-core Cell：**
- 5 个 slice / JWT / OIDC / 登录锁定 / RBAC hook
- J-sso-login 等 5 条 journey

**Week 7 — audit-core Cell：**
- 3 个 slice / HMAC-SHA256 hash chain
- J-audit-login-trail 跨 cell journey

**Week 8 — config-core Cell（v2 新增）：**
- 4 个 slice / 配置 CRUD / 版本管理 / feature flags / 热更新事件
- J-config-hot-reload + J-config-rollback journey
- 跨 cell 配置传播验证

**Gate：3 个内置 Cell 运行，8 条 journey 通过**

### Phase 3：Adapters + Formal Family（Day 64-77）

**Week 9 — First-class Adapters：**
- PostgreSQL / Redis / OIDC / S3 / VictoriaMetrics
- outbox writer + relay 全链路

**Week 10 — Formal Adapter Family + 集成测试（v2 新增）：**
- RabbitMQ adapter（Publisher + Consumer + DLQ）
- WebSocket adapter（signal-first + duplex）
- testcontainers 集成测试
- Grafana dashboard 模板

**Gate：outbox -> relay -> consume 全链路，OIDC 登录，RabbitMQ DLQ，WebSocket push**

### Phase 4：Examples + 文档 + Optional 接口（Day 78-91）

**Week 11 — Examples：**
- examples/sso-bff / todo-order / iot-device

**Week 12 — 文档 + Optional + WinMDM 接入：**
- README + Getting Started
- 全部 Templates
- Optional adapter 接口留桩（MySQL / Kafka / SSE / gRPC / search / notification / tenant）
- WinMDM 引用 GoCell 的 POC

**Gate：新项目 30 分钟内创建第一个 cell + slice + journey 并跑通**

---

## 9. 并行开发

| 阶段 | 并行 Agent 数 | 说明 |
|------|-------------|------|
| Phase 0 完成后 | 3-4 | metadata parser + trace + runtime 包 |
| metadata parser 完成后 | 5-6 | scaffolder + validator + registry + runtime |
| Phase 1 完成后 | 6-8 | 3 个 Cell + adapter + runtime 全部并行 |

关键路径：`metadata schema → parser → scaffolder → registry → dependency checker → generator`（14-18 天）

---

## 10. v1.0 vs v1.1 vs v2.0 边界

| 版本 | 内容 |
|------|------|
| **v1.0** | Kernel(45+项) + 3 Cell + 8 Journey + 5 一等 adapter + 2 正式 adapter + 3 examples |
| **v1.1** | support bundle + SSE/gRPC/MySQL/search/notification/tenant adapter + admin CLI |
| **v2.0** | Kafka/Debezium/ClickHouse/workflow/edge（按需） |

详细能力版图见 `522-go-foundation-capability-map.md`。

---

## 11. 优点

1. **Kernel + Cell 运行时是独特价值** — Go 生态没有 cell-native 框架
2. **3 个内置 Cell** — access + audit + config 覆盖认证/审计/配置，新项目开箱即用
3. **8 条内置 Journey** — 含 3 条跨 cell，验证 L1→L2→消费→幂等→热更新全链路
4. **Webhook + Feature flags 进 Kernel** — 高频通用能力不用每个项目重做
5. **RabbitMQ + WebSocket 正式 adapter** — 命令/回执/DLQ + 实时推送覆盖主流场景
6. **VictoriaMetrics + Grafana** — 存储效率 70x，运维+业务双用途
7. **531 能力版图对齐** — 外部团队建议全面采纳

## 12. 代价

1. **91 天到 WinMDM 可用** — Phase 2 完成（Day 63）即可并行
2. **Kernel 复杂度** — assembly generator + select-targets + reconcile 是最难部分
3. **两仓库维护** — gocell 和 WinMDM 版本同步
4. **三个 Cell 实现工作量** — 但每个 Cell 都是通用复用价值

## 13. 平台债警惕清单

| 债务 | 缓解 |
|------|------|
| metadata 漂移 | 单一 JSON Schema 为 source of truth |
| wrapper 旁路 | CI checker 阻断裸跨边界调用 |
| impact analysis 不准 | select-targets 集成测试验证 |
| generator 黑盒 | 生成结果可读、可 diff、可 debug |
| registry 垃圾场 | 注册强制 producer/consumer/owner |
| CI 规则过软 | blocking，不是 warning |
| fixture/replay 债 | 从第一个示例建立稳定测试数据 |
| 开发体验债 | `gocell init` 一条命令完成初始化 |

---

## 14. 最终定位

**GoCell 不是"再多几个中间件"，而是把 Cell/Slice 运行时 + metadata + assembly + outbox/replay/reconcile + verify + journey/status + webhook/feature-flags 做成真正可复用的 cell-native 框架，并内置 access-core + audit-core + config-core 三个开箱即用的通用 Cell。**
