# 底座优先实施方案

> 生成日期: 2026-04-16（原始）/ 更新: 2026-04-18（外部审查回灌 + 已完成项归档）
> 基准: develop@042f405（PR#161 合并后）+ 2026-04-18 外部审查 finding
> 策略: 不急于 v1.0 发布，优先加固 pkg/runtime/kernel/adapter 四层底座
> 替代: `20260416-post-wave3-implementation-plan.md`（发布驱动方案）

---

## 设计原则

1. **安全/正确性不降级** — P0 回归项立刻插队，不管原 phase 是否已收尾
2. **Batch 8 偿债项前置** — 原来标记 "v1.0 后" 的 pkg/runtime/kernel/adapter 项全部拉进主线
3. **功能扩展后移** — PR-FEAT(Device List / Flag Write)、Wave 2-3(BFF / SecureCookie) 降为后续
4. **发布仪式延后** — Wave 4 Review + v1.0 tag 在底座稳固后再做
5. **自底向上** — kernel → runtime → pkg → adapter，依赖方向倒序加固
6. **外部审查回灌** — 外部审查发现的 P0/P1 正确性项立即回灌到主线而非 v1.0 后（2026-04-18 新增）

---

## Phase 0: ✅ 全部完成

> PR#143 + PR#151 + PR#135/136/137 关闭 H1-1/H1-2/H1-3/H1-4/H1-5/H1-6。详见 backlog PR-H1 段。

---

## Phase 0.5: 外部审查正确性回归（2026-04-18 新增，插队执行）

> 来源: 2026-04-18 外部审查发现 2 条 P0 + 1 条 P1 正确性回归，等级和 Phase 0 同量级，必须先于 Phase 3-5 余量落地。

### PR-SAFE-2: token intent 强约束（P0 阻塞）

| 任务 | 工时 | 涉及文件 | 验收 |
|------|------|----------|------|
| **AUTH-TOKEN-INTENT-01** (P0, Cx3): Issue() 增 `TokenIntent`（access/refresh）→ JWT `aud` claim；verifier 按请求 scope 拒绝 intent 不匹配的 token；`/auth/refresh` 只接受 `intent=refresh`，其余路径只接受 `intent=access` | 5h | `runtime/auth/jwt.go` + `runtime/auth/middleware.go` + `cells/access-core/slices/{sessionlogin,sessionrefresh,sessionvalidate}/service.go` | 集成测试：① refresh token → /api/v1/* 业务接口 401；② access token → /auth/refresh 401 |
| **AUTH-INT-REACHABILITY-01** (P1, Cx2) [搭车]: 带合法 token 的 handler 到达性断言 + public handler 精确状态/响应断言（当前仅匿名→401 + public→非401 太弱） | 1.5h | `cells/access-core/slices/*/auth_integration_test.go` | 验证路由丢失/方法错误/handler 500 能被捕获 |

> 同批 PR 一次落地：AUTH-INT-REACHABILITY-01 改的正是 PR-SAFE-2 会碰到的测试文件，搭车合规。

### PR-AUTHZ-2: config 管理面授权收口（P0 阻塞）

| 任务 | 工时 | 涉及文件 | 验收 |
|------|------|----------|------|
| **AUTHZ-WRITE-CONFIG-WRITE-01** (P0, Cx2): configwrite 的 create/update/delete 三端点加 `auth.RequireAnyRole(ctx, "admin")`，与 publish/rollback 的 admin gate 对齐；把 `roleAdmin` const 提到 `cells/config-core/internal/dto/authz.go` 共享 | 1.5h | `cells/config-core/slices/configwrite/handler.go` + `cells/config-core/internal/dto/authz.go` (新建) | 401/403/200 三状态测试 + happy-path 注入 admin 上下文 |

### PR-HEALTH: readyz 纳入 broker 连通状态（P2，并行可做）

| 任务 | 工时 | 涉及文件 | 验收 |
|------|------|----------|------|
| **READYZ-BROKER-HEALTH-01** (P2, Cx3): `adapters/rabbitmq/connection.go` 暴露 `Health() error`；`runtime/bootstrap/` 把关键 subscriber/connection 自动注册为 health checker；`WithBrokerHealth(opts...)` 控制开关 | 2h | `adapters/rabbitmq/connection.go` + `runtime/bootstrap/bootstrap.go` + `runtime/http/health/health.go` | 断连 broker 时 readyz → 503 |

> Phase 0.5 合计: **10h**（PR-SAFE-2 6.5h + PR-AUTHZ-2 1.5h + PR-HEALTH 2h），两串行一并行 → 约 1.5 工作日。

---

## Phase 1: kernel 层加固 — ✅ 全部完成

| 模块 | 合并 PR |
|------|---------|
| PR-K-OUTBOX | ✅ PR#147 + PR#148 |
| PR-K-META | ✅ PR#142 + PR#152 |
| PR-K-CELL | ✅ PR#142（SLICE-ALLOWEDFILES）+ PR#154（#17 Hook 增强）+ PR#142（CONTRACT-LIST-LINT）|

> 衍生 follow-up（Batch 8 下沉）: METADATA-PROJECTLOC-IFACE-01 / OUTPUT-JSON-SARIF-01 / METADATA-PERF-BENCH-01，见 backlog。

---

## Phase 2: runtime 层优化 — ✅ 全部完成（含搭车 follow-up）

| 模块 | 合并 PR |
|------|---------|
| PR-R-ROUTER | ✅ PR#158 |
| PR-R-AUTH | ✅ PR#159 |
| PR-R-OBS | ✅ PR#154 + PR#156 + PR#157（OB-02 下沉 Batch 8）|
| PR-R-CFG | ✅ PR#158（CFG-KEYFILTER-WIRE + CFG-ERRCODE + F2-SEC-03）|

> 衍生 follow-up（Batch 8 下沉）: PUBLIC-ENDPOINT-METHOD-MATCH-01 / DTO-NIL-SEMANTIC-01 / AUTH-LEGACY-TOKEN-STRICT-01 / OBS-RELAY-REGISTER-ATOMIC-01 / OBS-HTTP-COLLECTOR-AUTOWIRE-01 / OBS-LGTM-INTEGRATION-01，见 backlog。
>
> **2026-04-18 新增**: AUTH-TOKEN-INTENT-01（runtime/auth）和 AUTH-INT-REACHABILITY-01（cells 侧 auth test）作为 Phase 2 "正确性回归" 独立排在 Phase 0.5，而非追加到 PR-R-AUTH。理由：PR#159 已经 merged，intent 强约束是契约语义新增，独立 PR 清晰。

---

## Phase 3: pkg + 工具链（剩余 ~6h）

### PR-P-CURSOR: ✅ 全部完成（PR#156 + PR#160）

### PR-P-CB: circuit breaker 接口清理（2h，剩余）

| 任务 | 工时 | 涉及文件 |
|------|------|----------|
| CB-IFACE-01: Allow/Report 拆分（满足 ISP） | 1h | `runtime/resilience/circuitbreaker/` |
| CB-ENCAP-01: 消除 gobreaker import 泄漏 | 1h | `runtime/resilience/circuitbreaker/` |

> PR#163 已完成 CB-TYPED-NIL-GUARD-01 + WithCircuitBreaker fail-fast；接口清理仍未覆盖。

### PR-CMD: CLI 工具链优化（4h）

| 任务 | 工时 | 涉及文件 |
|------|------|----------|
| CMD-MODE-01: validate/scaffold fail-fast 模式 | 2h | `cmd/gocell/` |
| CMD-REFACTOR-01: app 包提取 | 1.5h | `cmd/gocell/` |
| F-7 BUILD-OUTDIR-01: 统一 `go build -o bin/` | 0.5h | `Makefile` |

> PR#164 已完成 cmd/gocell CLI foundation；mode + refactor 仍未覆盖。

---

## Phase 4: adapter 层加固（剩余 ~10h）

> 前置: Phase 1 PR-K-OUTBOX ✅ 完成。

### PR-A-INTEG: 集成测试补全（剩余 4h）

| 任务 | 工时 | 涉及文件 |
|------|------|----------|
| P4-TD-05: outbox 全链路 3-container 集成测试（PG+RMQ+app） | 2h | `adapters/postgres/` + `adapters/rabbitmq/` |
| RL-INT-01: Relay PG 集成测试 | 2h | `adapters/postgres/outbox_relay_test.go` |

> ✅ #6 TPUB-01 PR#141；✅ OTEL-COV-01 PR#157。

### PR-A-HARDEN: 生产安全加固（6.5h）

| 任务 | 工时 | 涉及文件 |
|------|------|----------|
| RL-MIG-01: `CREATE INDEX CONCURRENTLY` online-safe 索引 | 2h | `adapters/postgres/migrations/` |
| RL-SUB-01: 入站 ID 校验 | 1h | `adapters/rabbitmq/subscriber.go` |
| #31 RabbitMQ backoff + FailOpen enum 清理 | 2h | `adapters/rabbitmq/` |
| POOLSTATS-IFACE-01: 三个 adapter PoolStats 公共接口 | 1h | `adapters/postgres/pool.go` + `redis/client.go` + `rabbitmq/connection.go` |
| POOLSTATS-JSON-01: `json:"camelCase"` tags | 0.5h | 同上 |

> **2026-04-18 关联**: Phase 0.5 PR-HEALTH 的 READYZ-BROKER-HEALTH-01 也会改 `adapters/rabbitmq/connection.go`（新增 `Health()` 方法），与 POOLSTATS-IFACE-01 改的是同一文件不同方法，PR 顺序建议 PR-HEALTH 先、PR-A-HARDEN 后，避免 connection.go 并行冲突。

### PR-A-CI: 供应链安全（2h）

| 任务 | 工时 | 涉及文件 |
|------|------|----------|
| CI-DIGEST-01: testcontainers 镜像 tag+digest 双固定 | 1h | `adapters/*/integration_test.go` |
| CI-LINT-PIN-01: golangci-lint patch 级固定 + dependabot | 1h | `.github/workflows/ci.yml` |

---

## Phase 5: 架构收敛（剩余 ~7h，可选但推荐）

### PR-SERIAL: 序列化边界收敛（3h）

| 任务 | 工时 | 涉及文件 |
|------|------|----------|
| EVENT-PAYLOAD-TYPED-01: 6 个 service payload `map[string]any` → typed event struct | 3h | 6 个 `service.go` + event contract schemas |

### PR-ADAPTER-SPLIT: adapter 分层重整（4h）

| 任务 | 工时 | 涉及文件 |
|------|------|----------|
| AL-01: outbox_relay.go 轮询调度 → `runtime/outbox/relay.go` | 2h | `adapters/postgres/outbox_relay.go` → `runtime/outbox/relay.go` |
| AL-02: distlock.go 续期/TTL → `runtime/` | 2h | `adapters/redis/distlock.go` → `runtime/` |

### PR-CONTRACT: ✅ 全部完成（PR#143 + PR#155）

---

## Phase 6: 延后项（底座稳固后再做）

### 功能扩展

| 任务 | 原位置 | 工时 | 延后理由 |
|------|--------|------|----------|
| WM-35 BFF handler 接入 cookie session | Wave 2 | 2d | 功能扩展 |
| WM-36 SecureCookie key rotation | Wave 3 | 1.5d | 依赖 WM-35 |
| DEVICE-LIST-API | PR-FEAT | 3h | 新端点 |
| FLAG-WRITE-API | PR-FEAT | 3h | 新端点 |
| AUTH-CACHE-01 session Redis 缓存 | Batch 8 | 4h | 优化项 |
| P3-TD-11 domain 模型拆分 | Batch 8 | 4h | cells 重构 |

### 发布仪式

| 任务 | 工时 | 延后理由 |
|------|------|----------|
| AUTH-DX-01 README 文档收口 | 4h | 等 API 最终形态 |
| P2-T-02 audit e2e 测试 | 2h | Journey 验收 |
| Review cells/ + examples/ | 6h | 发布前活动 |
| v1.0 tag | — | 底座稳固 + 功能补全后 |

### 大型独立项

| 任务 | 工时 | 延后理由 |
|------|------|----------|
| PG-DOMAIN-REPO (5 个域 PostgreSQL Repository) | 3-5d | 规模大，独立排期；含 CONFIG-VERSIONS-MIGRATION-01 |
| SYSTEM-TOPOLOGY-API | 4h | 运维功能 |
| WM-7 泛型 BulkResult | 1d | 设计面广 |
| AUTH-SIGNER-01 `crypto.Signer` | 2h | 前置 golang-jwt v6 |

### 触发条件项

| 任务 | 触发条件 |
|------|----------|
| AUTH-PROVIDER-EXPORT-01 | 第二个 auth provider cell |
| AUTH-ISSUE-OPTIONS-01 | Issue() 第 5 个参数 |
| DEVICE-ENQUEUE-RBAC | 多租户 operator |
| CB-RESILIENCE-PACKAGE-01 | 出现非 HTTP 的 CB 消费方 |

---

## 设计决策记录（2026-04-18 外部审查后仍维持）

| 决策 | 原记录位置 | 2026-04-18 复核结论 | 理由 |
|------|-----------|--------------------|------|
| **F1-3 DurabilityDurable + in-memory 不修** | backlog.md PR#137 review 设计决策 | ✅ 维持 | `main.go:143-145` 注释 + `effectiveMode="real-keys-in-memory-storage"` + `adapterInfo["storage"]="in-memory"` + slog 启动日志 4 路透明标注；真正修复路径 PG-DOMAIN-REPO 已在 backlog 排队（3-5d）；fail-fast on in-memory 会阻断当前开发路径，属时序倒置 |

> 如后续仍担心感知漂移，可增量做 **P3: durable+in-memory 启动告警升级**（slog.Info→slog.Warn），不需要重开 F1-3 决策。

---

## 执行总览

```
Phase 0    正确性守护         ✅ 全部完成（PR-SAFE + PR-AUTHZ）
    ↓
Phase 0.5  外部审查正确性回归    ~10h 剩余  ← 2026-04-18 新增，P0 插队
  PR-SAFE-2  (6.5h)  AUTH-TOKEN-INTENT + AUTH-INT-REACHABILITY 搭车
  PR-AUTHZ-2 (1.5h)  AUTHZ-WRITE-CONFIG-WRITE
  PR-HEALTH  (2h)    READYZ-BROKER-HEALTH（并行，需与 PR-A-HARDEN 顺序协调）
    ↓
Phase 1    kernel 加固          ✅ 全部完成
Phase 2    runtime 优化         ✅ 全部完成
    ↓
Phase 3    pkg + 工具链          ~6h 剩余
  PR-P-CB (2h) / PR-CMD (4h)
    ↓
Phase 4    adapter 加固          ~12.5h 剩余
  PR-A-INTEG (4h) / PR-A-HARDEN (6.5h) / PR-A-CI (2h)
  建议顺序: PR-HEALTH（Phase 0.5）→ PR-A-HARDEN，避免 connection.go 冲突
    ↓
Phase 5    架构收敛              ~7h 剩余（可选）
  PR-SERIAL (3h) / PR-ADAPTER-SPLIT (4h)
    ↓
Phase 6    功能 + 发布           ~15h+ ← 底座稳固后

当前剩余（含 Phase 0.5）: ~35.5h（约 4-5 工作日）
不含 Phase 5（可选）:    ~28.5h（约 3-4 工作日）
```

### 已合入底座 PR 速查

```
Phase 1/2 底座:
  PR#142/147/148/152/154 kernel 治理 + outbox 治理 + validator 定位
  PR#156/157 OBS 全家桶（provider-neutral metrics + async hook + test determinism）
  PR#158 PR-R-ROUTER（WithPublicEndpoints 信任边界收敛）
  PR#159 PR-R-AUTH（slog/clock/nonce/metrics 全量 DI + HMAC replay + auth metrics）
  PR#160 PR-P-CURSOR（泛型 helper + 日志 + DemoMode + 5 入口回归）
  PR#161 CI flake 收口（rabbitmq 测试确定性 + hook duration + gocognit 15）
  PR#162 AUTH-LEGACY-TOKEN-REMOVE（servicetoken 2-part 分支下线）
  PR#163 circuitbreaker 接口隔离（CB-TYPED-NIL-GUARD + fail-fast）
  PR#164 cmd/gocell CLI foundation

Phase 0/0.5 安全:
  PR#143/151 PR-H1 安全加固（7 项 H1-* 全闭合）
  PR#155 PR-H2 契约补全（rollback 契约 + publish redaction + AUTHZ-WRITE-CONFIG publish/rollback 侧 + ERROR-MSG-SCRUB）
```

---

## 与发布驱动方案的关键差异

| 维度 | 发布驱动方案 | 底座优先方案 |
|------|-------------|-------------|
| 优先级 | 安全→功能→文档→tag | 正确性→kernel→runtime→pkg→adapter |
| Batch 8 | v1.0 后做 | 按层拆入 Phase 1-4，偿债提前 |
| 功能扩展 | PR-FEAT 在 v1.0 前做 | 延后到 Phase 6 |
| 外部审查 P0 回灌 | 排到 Batch 8 | **立即 Phase 0.5 插队** |
| v1.0 tag | 目标 ~9 工作日 | 不设时间目标 |

---

## 风险与缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| Phase 0.5 PR-SAFE-2 改 `runtime/auth/jwt.go` 会影响所有认证调用点 | 回归面广 | Intent 默认值 = access，老调用 zero-diff；新增用例覆盖 refresh 路径 |
| Phase 0.5 PR-HEALTH 与 Phase 4 PR-A-HARDEN 同改 `adapters/rabbitmq/connection.go` | 合并冲突 | PR-HEALTH 先合入；PR-A-HARDEN rebase |
| 外部审查 F1-3 决策复议风险（durable + in-memory） | 若重开会牵动 PG-DOMAIN-REPO 前置 | 2026-04-18 复核维持决策，风险收敛 |
| AUTH-SIGNER-01 前置 golang-jwt v6 不可控 | 阻塞 | 已移入 Phase 6 延后 |
