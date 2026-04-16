# 底座优先实施方案

> 生成日期: 2026-04-16
> 基准: develop@23c9537 (PR#140 合并后)
> 策略: 不急于 v1.0 发布，优先加固 pkg/runtime/kernel/adapter 四层底座
> 替代: `20260416-post-wave3-implementation-plan.md`（发布驱动方案）

---

## 设计原则

1. **安全/正确性不降级** — H1-1(P0) + H1-3(P1) 仍排最前，这是正确性不是功能
2. **Batch 8 偿债项前置** — 原来标记 "v1.0 后" 的 pkg/runtime/kernel/adapter 项全部拉进主线
3. **功能扩展后移** — PR-FEAT(Device List / Flag Write)、Wave 2-3(BFF / SecureCookie) 降为后续
4. **发布仪式延后** — Wave 4 Review + v1.0 tag 在底座稳固后再做
5. **自底向上** — kernel → runtime → pkg → adapter，依赖方向倒序加固

---

## Phase 0: 正确性守护（~8h，立即做）

> 不论是否赶发布，这些是 bug 不是 feature。

| PR | 任务 | 层 | 工时 | 涉及文件 | 来源 |
|----|------|----|------|----------|------|
| **PR-SAFE** | H1-1 PROD-KEY-FAILFAST (P0): `loadKeySet` 空值走 dev 分支，生产密钥可预测 | cmd/core-bundle | 2h | `cmd/core-bundle/main.go` | PR#137-138 集成审查 P0 |
| | H1-3 DURABLE-NIL-GUARD (P1): `CheckNotNoop` 对 nil `continue` 跳过，durable+nil 旁路 | kernel/cell | 1.5h | `kernel/cell/durability.go` + 5 个 `cell.go` | PR#137-138 集成审查 P1 |
| | READYZ-VERBOSE-TOKEN-01: `/readyz?verbose` 匿名暴露内部拓扑 | runtime/http | 2h | `runtime/http/health/health.go` + `runtime/bootstrap/bootstrap.go` | PR#134 review |
| **PR-AUTHZ** | H1-2 IDENTITY-AUTHZ-01: identitymanage 7 端点仅鉴权无授权 | cells/access-core | 1.5h | `cells/access-core/slices/identitymanage/handler.go` | PR#137-138 集成审查 P1 |
| | H1-4 ROLE-ASSIGN-API: `POST /internal/v1/roles/assign` + `DELETE /internal/v1/roles/revoke` | cells/access-core | 2h | `cells/access-core/slices/rbacassign/` | backend_issues.md #3 |
| | H1-5 SEED-ADMIN: 启动时 seed admin（环境变量驱动） | cmd/core-bundle | 1h | `cmd/core-bundle/main.go` + `cells/access-core/internal/mem/` | backend_issues.md #3 |

> PR-SAFE 与 PR-AUTHZ 可并行。H1-2/H1-4/H1-5 必须同 PR（避免加了授权但无法分配角色的死锁）。

---

## Phase 1: kernel 层加固（~17h，3 个 PR 并行）

> kernel 是底座灵魂。先修内核，上层才有意义。

### PR-K-OUTBOX: outbox 治理 + 可观测（5h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| OUTBOX-GUARD-01: NoopWriter/DiscardPublisher lint 约束（go vet 或 golangci-lint 自定义规则） | 2h | `kernel/outbox/` | 6B review |
| DISCARD-OBS-01: DiscardPublisher Logger 注入 + counter（可观测静默丢弃量） | 1h | `kernel/outbox/outbox.go` | 6B review |
| OUTBOX-RECEIPT-01: `outbox.Receipt` alias 全仓迁移 `idempotency.Receipt` | 1h | `kernel/outbox/` + `kernel/idempotency/` | 6B review |
| META-SIZE-01: Metadata key 数量和大小上限，防 OOM | 1h | `kernel/outbox/outbox.go` | 6A review |

### PR-K-META: metadata parser + registry 健壮性（4h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| META-67-01: strict unknown-field reject | 1h | `kernel/metadata/parser.go` | PR#67 review |
| META-67-02: 位置信息错误报告 | 1h | `kernel/metadata/parser.go` | PR#67 review |
| META-67-03: cross-file 引用校验 | 0.5h | `kernel/metadata/parser.go` | PR#67 review |
| REGISTRY-CONSUMERS-UNKNOWN-KIND-01: `Consumers()` allowlist + error return | 1.5h | `kernel/registry/contract.go` | PR#135 review |

### PR-K-CELL: cell 元数据 + hook + 治理工具（8h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| #27b SLICE-ALLOWEDFILES-01: `BaseSlice.AllowedFiles()` 默认逻辑修复（kebab-case vs no-dash） | 2h | `kernel/cell/base.go` + all `slice.yaml` | PR#119 review |
| #17 WM17-F2-2: Hook ctx 超时 | 1.5h | `kernel/cell/` | WM-17 |
| #17 WM17-F4-3: Prometheus metrics via HookObserver 接口 | 1.5h | `kernel/cell/` | WM-17 |
| #21 F-5: Journey catalog 不校验引用 | 1h | `kernel/journey/catalog.go` | 6B |
| CONTRACT-LIST-LINT-01: `gocell check contract-health` list 响应格式检查 | 2h | `kernel/governance/` | PR#138 review |

---

## Phase 2: runtime 层优化（~17.5h，4 个 PR 并行）

> runtime 直接决定 cells 的运行质量。

### PR-R-ROUTER: 信任边界收敛（4h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| RTR-PUBLIC-POLICY-01: 三套独立入口收敛为 `WithPublicEndpoints` 组合选项 | 3h | `runtime/http/router/router.go` + `runtime/bootstrap/bootstrap.go` | PR#131 review |
| F1-ARCH-03: `WithSecurityHeadersOptions` 接线测试 | 0.5h | `runtime/http/router/router_test.go` | PR#133 review |
| F4-OPS-01: `bootstrap.WithSecurityHeadersOptions` 便利包装 | 0.5h | `runtime/bootstrap/bootstrap.go` | PR#133 review |

### PR-R-AUTH: 认证安全 + 测试隔离 + 可观测（7h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| AUTH-SLOG-01: KeySet/servicetoken 注入 slog.Handler 替代全局 `slog.SetDefault` | 2h | `runtime/auth/` | PR#131 review |
| AUTH-NOWFUNC-01: `var nowFunc` 包级状态改为实例字段注入 | 1h | `runtime/auth/jwt.go` | PR#131 review |
| WM-2-F2: HMAC replay 防护 | 2h | `runtime/auth/` | WM-2 |
| WM-2-F3: auth metrics (token verify latency/failure count) | 2h | `runtime/auth/` | WM-2 |

### PR-R-OBS: 可观测性补全（4.5h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| OBS-TABLE-01: observability bridge table-driven 改写 | 1.5h | `runtime/http/middleware/` | 6A review |
| OBS-METRIC-01: bridge counter/histogram 补全 | 1.5h | `runtime/http/middleware/` | 6A review |
| OBS-DX-01: cloneMetadata 导出 + wrapper 清理 + godoc | 1h | `kernel/outbox/` | 6A review |
| OBS-DOC-01: IsReservedMetadataKey usage example | 0.5h | `kernel/outbox/` | 6A review |
| OB-02: safe_observe broken logger 注入测试 | 1h | `runtime/http/middleware/safe_observe_test.go` | 历史 backlog 0-J |

> 注: OBS-DX-01/OBS-DOC-01 虽属 kernel/outbox，但与 OBS-TABLE/METRIC 同属可观测主题，合并一个 PR 减少 review 负担。

### PR-R-CFG: 配置治理 + 测试补全（2h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| CFG-KEYFILTER-WIRE-01: cell 通知循环 `KeyFilter.Matches()` 选择性通知 | 1h | `runtime/bootstrap/bootstrap.go` | PR#132 review |
| CFG-ERRCODE-01: `fmt.Errorf` → errcode 迁移评估 | 0.5h | `runtime/config/watcher.go` + `config.go` | PR#132 review |
| F2-SEC-03: bootstrap 信任边界测试补 `traceparent` 注入向量 | 0.5h | `runtime/bootstrap/bootstrap_test.go` | PR#133 review |

---

## Phase 3: pkg + 工具链（~10h，3 个 PR 并行）

### PR-P-CURSOR: 分页基础设施补全（5.5h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| WM-6-F6: 泛型 cursor helper | 1h | `pkg/query/` | WM-6 |
| WM-6-F7: cursor 日志收口 | 0.5h | `pkg/query/` | WM-6 |
| WM-6-F1: prod guard | 0.5h | `pkg/query/` | WM-6 |
| #15 CURSOR-TEST-01 + CUR-HDL-01: 5 个分页入口回归测试 | 2h | `cells/*/handler_test.go` + `service_test.go` | 6A review |
| #32 CURSOR-P2-02: cursor invalid 结构化日志 | 1h | `cells/audit-core/` | 6A review |
| TX-NIL-01: txRunner nil-safe 行为文档化 | 0.5h | `cells/*/service.go` | 历史 backlog |

### PR-P-CB: circuit breaker 接口清理（2h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| CB-IFACE-01: Allow/Report 拆分（满足 ISP） | 1h | `runtime/resilience/circuitbreaker/` | 6B |
| CB-ENCAP-01: 消除 gobreaker import 泄漏 | 1h | `runtime/resilience/circuitbreaker/` | 6B |

### PR-CMD: CLI 工具链优化（4h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| CMD-MODE-01: validate/scaffold fail-fast 模式 | 2h | `cmd/gocell/` | 6B review |
| CMD-REFACTOR-01: app 包提取（cmd 与 app 逻辑分离） | 1.5h | `cmd/gocell/` | 6B review |
| F-7 BUILD-OUTDIR-01: 统一 `go build -o bin/` 输出目录 | 0.5h | `Makefile` 或 build scripts | 6B |

---

## Phase 4: adapter 层加固（~16h，3 个 PR 并行）

> 前置: Phase 1 PR-K-OUTBOX（outbox 治理改造完成后再做集成测试）。

### PR-A-INTEG: 集成测试补全（9.5h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| #6 TPUB-01: TestPubSub conformance 替换 sleep + 接入 RabbitMQ adapter | 4h | `kernel/outbox/outboxtest/` + `adapters/rabbitmq/` | 6B |
| P4-TD-05: outbox 全链路 3-container 集成测试（PG+RMQ+app） | 2h | `adapters/postgres/` + `adapters/rabbitmq/` | Phase 4 review |
| RL-INT-01: Relay PG 集成测试 | 2h | `adapters/postgres/outbox_relay_test.go` | PR#46 review |
| OTEL-COV-01: OTel testcontainers 集成测试 | 1.5h | `adapters/otel/` | 6A review |

### PR-A-HARDEN: 生产安全加固（6.5h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| RL-MIG-01: `CREATE INDEX CONCURRENTLY` online-safe 索引 | 2h | `adapters/postgres/migrations/` | PR#46 review |
| RL-SUB-01: 入站 ID 校验（空/过长 message ID） | 1h | `adapters/rabbitmq/subscriber.go` | PR#46 review |
| #31 RabbitMQ 代码清理: backoff + FailOpen enum | 2h | `adapters/rabbitmq/` | Wave 2 残留 |
| POOLSTATS-IFACE-01: 三个 adapter PoolStats 公共接口 | 1h | `adapters/postgres/pool.go` + `redis/client.go` + `rabbitmq/connection.go` | PR#134 review |
| POOLSTATS-JSON-01: PoolStats `json:"camelCase"` tags | 0.5h | 同上 | PR#134 review |

### PR-A-CI: 供应链安全（2h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| CI-DIGEST-01: testcontainers 镜像 tag+digest 双固定 | 1h | `adapters/*/integration_test.go` | PR#139 review |
| CI-LINT-PIN-01: golangci-lint patch 级固定 + dependabot 升级 | 1h | `.github/workflows/ci.yml` | PR#139 review |

---

## Phase 5: 架构收敛（~10h，可选但推荐）

> 从 Batch 8 和 v1.1+ 拉入的、对底座稳定性有直接影响的架构改善。

### PR-SERIAL: 序列化边界收敛（3h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| EVENT-PAYLOAD-TYPED-01: 6 个 service payload `map[string]any` → typed event struct | 3h | 6 个 `service.go` + event contract schemas | PR#133 re-review |

### PR-ADAPTER-SPLIT: adapter 分层重整（4h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| AL-01: outbox_relay.go 轮询调度逻辑拆到 `runtime/outbox/relay.go` | 2h | `adapters/postgres/outbox_relay.go` → `runtime/outbox/relay.go` | 依赖替换分析 |
| AL-02: distlock.go 续期/TTL 策略拆到 `runtime/` | 2h | `adapters/redis/distlock.go` → `runtime/` | 依赖替换分析 |

### PR-CONTRACT: 契约缺口补全（3h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| H2-1 CONFIG-ROLLBACK-CONTRACT | 1.5h | `contracts/http/config/rollback/v1/` | PR#137-138 集成审查 |
| H2-2 CONFIGPUBLISH-REDACT-01 | 0.5h | `cells/config-core/slices/configpublish/handler.go` | PR#138 review |
| H2-3 IDENTITY-PATCH-CONTRACT + H2-4~H2-7 config/identity 列表契约 | 1h | `contracts/http/` 多目录 | 代码验证发现 |

---

## Phase 6: 延后项（底座稳固后再做）

> 以下任务在 Phase 0-5 完成后按需排期。

### 功能扩展

| 任务 | 原位置 | 工时 | 延后理由 |
|------|--------|------|----------|
| WM-35 BFF handler 接入 cookie session | Wave 2 | 2d | 功能扩展，不影响底座质量 |
| WM-36 SecureCookie key rotation | Wave 3 | 1.5d | 功能扩展，依赖 WM-35 |
| DEVICE-LIST-API | PR-FEAT | 3h | 新端点，与底座无关 |
| FLAG-WRITE-API | PR-FEAT | 3h | 新端点，与底座无关 |
| AUTH-CACHE-01 session Redis 缓存 | Batch 8 | 4h | 优化项，非正确性 |
| P3-TD-11 domain 模型拆分 | Batch 8 | 4h | cells 层重构，底座稳固后做 |

### 发布仪式

| 任务 | 原位置 | 工时 | 延后理由 |
|------|--------|------|----------|
| AUTH-DX-01 README 文档收口 | PR-README | 4h | 等 API 最终形态确定 |
| P2-T-02 audit e2e 测试 | Batch 8 | 2h | Journey 级验收，属发布前活动 |
| Review cells/ + examples/ | Wave 4 | 6h | 发布前活动 |
| v1.0 tag | Wave 4 | — | 底座稳固 + 功能补全后 |

### 大型独立项

| 任务 | 原位置 | 工时 | 延后理由 |
|------|--------|------|----------|
| PG-DOMAIN-REPO (5 个域 Repository PostgreSQL 实现) | Batch 8 | 3-5d | 规模大，独立排期 |
| SYSTEM-TOPOLOGY-API | Batch 8 | 4h | 运维功能，非底座 |
| WM-7 泛型 BulkResult | Batch 8 | 1d | 设计面广，独立排期 |
| AUTH-SIGNER-01 `crypto.Signer` | Batch 8 | 2h | 前置 golang-jwt v6 |

### 触发条件项（条件满足时做）

| 任务 | 触发条件 |
|------|----------|
| AUTH-PROVIDER-EXPORT-01 | 第二个 auth provider cell |
| AUTH-ISSUE-OPTIONS-01 | Issue() 第 5 个参数 |
| DEVICE-ENQUEUE-RBAC | 多租户 operator |

---

## 执行总览

```
Phase 0  正确性守护     ~8h    ← 立即做，不可跳过
  PR-SAFE (5.5h) ─┐
  PR-AUTHZ (4.5h) ┘ 并行
    ↓
Phase 1  kernel 加固    ~17h   ┐
  PR-K-OUTBOX (5h)             │
  PR-K-META (4h)               │
  PR-K-CELL (8h)               │
                               ├ 三层并行推进（层内各 PR 也可并行）
Phase 2  runtime 优化   ~17.5h │
  PR-R-ROUTER (4h)             │
  PR-R-AUTH (7h)               │
  PR-R-OBS (4.5h)             │
  PR-R-CFG (2h)                │
                               │
Phase 3  pkg + 工具链   ~10h   ┘
  PR-P-CURSOR (5.5h)
  PR-P-CB (2h)
  PR-CMD (4h)
    ↓
Phase 4  adapter 加固   ~16h   ← 依赖 Phase 1 outbox 改造
  PR-A-INTEG (9.5h)
  PR-A-HARDEN (6.5h)
  PR-A-CI (2h)
    ↓
Phase 5  架构收敛       ~10h   ← 可选但推荐
  PR-SERIAL (3h)
  PR-ADAPTER-SPLIT (4h)
  PR-CONTRACT (3h)
    ↓
Phase 6  功能 + 发布    ~15h+  ← 底座稳固后

总计 Phase 0-4: ~68.5h（约 9 工作日）
含 Phase 5:    ~78.5h（约 10 工作日）
```

---

## 与发布驱动方案的关键差异

| 维度 | 发布驱动方案 | 底座优先方案 |
|------|-------------|-------------|
| 优先级 | 安全→功能→文档→tag | 正确性→kernel→runtime→pkg→adapter |
| Batch 8 | v1.0 后做（~62h 偿债） | 按层拆入 Phase 1-4，偿债提前 |
| 功能扩展 | PR-FEAT 在 v1.0 前做 | 延后到 Phase 6 |
| Wave 2-3 | 关键路径上 | 降为 Phase 6 延后 |
| v1.0 tag | 目标 ~9 工作日 | 不设时间目标，底座 ready 了再发 |
| 质量收益 | 底座偿债积压 | 发布时底座已加固，v1.0 后维护成本低 |

---

## 风险与缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| Phase 1-3 并行推进时 kernel 接口变更波及 runtime/cells | 合并冲突 | PR-K-OUTBOX 和 PR-K-META 改 kernel 接口签名，优先合入；其余 PR rebase |
| AUTH-SIGNER-01 前置 golang-jwt v6 不可控 | 阻塞 | 已移入 Phase 6 延后，不阻塞主线 |
| adapter 集成测试依赖外部服务（PG/RMQ/OTel collector） | CI 环境配置 | testcontainers 已有基础设施，Phase 4 PR-A-CI 先固定镜像版本 |
| Phase 5 AL-01/AL-02 拆分涉及跨层移动 | 大范围重构 | 标记为"可选但推荐"，可根据精力决定 |
