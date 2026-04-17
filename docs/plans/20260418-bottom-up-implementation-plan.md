# 底座优先实施方案 v2（自底向上）

> 生成日期: 2026-04-18
> 基准: develop@042f405（PR#161 合并后）+ 2026-04-18 外部审查 finding
> 策略: kernel → runtime → pkg → adapter → slice/cell，按层偿债；P0 正确性项单独先行
> 替代: `20260416-foundation-first-plan.md`（phase 编号方案，阶段切分偏粗）
> 来源: 2026-04-18 backlog 全量扫描 + 外部审查 P0 回灌
>
> 标记说明:
> 🟡 可延后 = 不卡正确性或安全（latent risk / DX / 测试覆盖 / tech debt / 供应链 / 打磨）
> 🟠 条件延后 = 有明确触发条件（如首次 prod migration / PG 接线），触发前可延

---

## 设计原则

1. **自底向上偿债** — kernel → runtime → pkg → adapter，依赖方向倒序加固；**slice/cell 问题全部后置**，统一在 Phase S 收口
2. **P0 正确性例外** — 外部审查 P0 跨层 finding 单独排 Phase 0.5，不受层序约束
3. **跨层搭车按顶层归属** — 如 READYZ-BROKER-HEALTH-01 涉及 runtime/adapter 双层，归入更底的那层（adapter）一起做
4. **触发条件项不排期** — 等触发条件满足再做，不占工作量
5. **设计决策维持不修** — F1-3 durable+in-memory / F1.2/F2.5/F4.2/F6.2 auth 等历史裁决不重开

---

## Phase 0: ✅ 正确性守护（历史 PR 已全部闭合）

> PR#143 + PR#151 + PR#135/136/137 关闭 H1-1/H1-2/H1-3/H1-4/H1-5/H1-6。详见 backlog PR-H1 段。

---

## Phase 0.5: P0 正确性回归（2026-04-18 外部审查，插队执行，~10h）

> 跨层 P0 项，不受 bottom-up 约束；修复面包含 runtime + slice，一个 PR 一起落地才能形成完整语义。

### PR-P0-AUTH-INTENT: token intent 强约束（P0 阻塞，6.5h）

| 改动层 | 任务 | 涉及文件 |
|--------|------|----------|
| runtime | `TokenIntent` enum（access/refresh）→ `Issue()` 新入参 → 映射 JWT `aud` claim；verifier 按请求 scope 拒绝 intent 不匹配的 token | `runtime/auth/jwt.go` + `runtime/auth/middleware.go` |
| slice | 3 个 slice service 传 intent；`/auth/refresh` 只接受 `intent=refresh`，其余只接受 `intent=access` | `cells/access-core/slices/{sessionlogin,sessionrefresh,sessionvalidate}/service.go` |
| test | 2 条集成用例：refresh token → 业务接口 401；access token → /auth/refresh 401；**搭车 AUTH-INT-REACHABILITY-01**：补合法 token 到达性断言 + public handler 精确状态断言 | `cells/access-core/slices/*/auth_integration_test.go` |

### PR-P0-AUTHZ-CONFIGWRITE: config 管理面授权收口（P0 阻塞，1.5h）

| 改动层 | 任务 | 涉及文件 |
|--------|------|----------|
| slice | configwrite 的 create/update/delete 三端点加 `auth.RequireAnyRole(ctx, "admin")`，`roleAdmin` const 提到 `internal/dto/authz.go` 共享；补 401/403/200 测试 | `cells/config-core/slices/configwrite/handler.go` + `cells/config-core/internal/dto/authz.go`（新建）|

### PR-P0-READYZ-BROKER: broker 健康纳入 readyz（P2 但跨层，可延迟到 Phase A 合并，2h）

| 改动层 | 任务 | 涉及文件 |
|--------|------|----------|
| adapter | `Connection.Health() error` 暴露 | `adapters/rabbitmq/connection.go` |
| runtime | bootstrap 把关键 subscriber/connection 自动注册为 health checker；`WithBrokerHealth(opts...)` 控制开关 | `runtime/bootstrap/bootstrap.go` + `runtime/http/health/health.go` |

> 顺序建议: PR-P0-AUTH-INTENT → PR-P0-AUTHZ-CONFIGWRITE 可立刻做；PR-P0-READYZ-BROKER 可顺延至 Phase A 内与 POOLSTATS 改动合并（同改 connection.go）。

---

## Phase K: kernel 层偿债（🟡 可延后，~13h，3 个 PR 并行）

> kernel 是底座灵魂，但本 Phase 3 条 PR 全部是打磨类（API 整洁 / 工具 DX / 性能基准 / 启动边缘错误），不卡正确性或安全，可机会性纳入或 v1.0 后做。

### PR-K-VALIDATOR (🟡 可延后): validator 机器输出 + 定位 API 收敛（9h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **OUTPUT-JSON-SARIF-01** (P1, Cx3): `gocell validate` 新增 JSON + SARIF 输出通道；统一诊断模型（单一 `Issue` struct → 多 printer 映射）；文本格式声明非稳定。对标 golangci-lint / staticcheck / ESLint / kubectl print flags | 6h | `cmd/gocell/` + `kernel/governance/` 序列化 | PR#152 round-2 review |
| **METADATA-PROJECTLOC-IFACE-01** (P2, Cx3): 提取 `ProjectLocator interface { Locate(file, path string) Position }` 隐藏 yaml.v3 AST；`ProjectMeta.FileNodes` 不再泄漏 | 3h | `kernel/metadata/` + `kernel/governance/` + `cmd/gocell/` | PR#152 seat-1 |

> 搭车合规：JSON printer 和 ProjectLocator 都是 validator 统一抽象的一部分，同一 PR 做。

### PR-K-METAPERF (🟡 可延后): metadata parser 性能基准（4h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **METADATA-PERF-BENCH-01** (P1, Cx3): `BenchmarkParseFS_500Files` 性能基准 + goccy/go-yaml 单次解码迁移成本评估；构造 500+ MapFS fixture | 4h | `kernel/metadata/parser_test.go` + fixture | PR#152 seat-4 |

### PR-K-OBS-CONTRACT (🟡 可延后): outbox metric 注册契约（2h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **OBS-RELAY-REGISTER-ATOMIC-01** (P2, Cx3): `outbox.NewProviderRelayCollector` 5 个 metric 注册原子化，支持 Provider.Unregister 或文档化契约 | 2h | `kernel/outbox/` + `kernel/observability/metrics/` | PR#157 review S3-05 |

---

## Phase R: runtime 层偿债（~15h，3 个 PR 并行）

> Phase K 稳定后开工。Phase 0.5 的 runtime/auth 改动不影响这里 —— 不同文件，不同关注点。

### PR-R-BOOT-COGNIT (🟡 可延后): bootstrap 复杂度拆分（4h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **BOOTSTRAP-RUN-COGNIT-01** (P2, Cx3): `bootstrap.go::Run()` 认知复杂度 225（pre-existing，`//nolint:gocognit` 抑制）；拆 `validateOptions()` / `buildRouter()` / `startServers()` 三段式；每段独立可测 | 4h | `runtime/bootstrap/bootstrap.go` | PR#163 agent 报告 |

### PR-R-OBS-AUTOWIRE (🟡 可延后): 默认 HTTP collector 自动接线（3h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **OBS-HTTP-COLLECTOR-AUTOWIRE-01** (P2, Cx3): `bootstrap.WithMetricsProvider` 自动为默认 HTTP 中间件构造 `NewProviderCollector`；设计 `WithHTTPCollectorCellID` option | 2h | `runtime/bootstrap/bootstrap.go` + `runtime/http/middleware/` | PR#157 review S4-01 |
| **OB-02**: safe_observe broken logger 注入测试（历史 backlog 0-J）| 1h | `runtime/http/middleware/safe_observe_test.go` | 历史 backlog |

### PR-R-ROUTER-METHOD (🟡 可延后): 公共端点 method-aware 匹配（4h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **PUBLIC-ENDPOINT-METHOD-MATCH-01** (P1, Cx3): 当前 path-only 匹配是 latent risk；升级 `"METHOD /path"` 格式（向后兼容：无 method 前缀匹配所有 method）| 4h | `runtime/http/router/router.go` + `runtime/auth/middleware.go` + `runtime/bootstrap/bootstrap.go` + 调用方 | PR#158 six-seat review |

### PR-R-INTERNAL-LISTENER (🟡 可延后): internal 信任边界（4-8h，可选升级为 Phase X）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **INTERNAL-LISTENER-01** (P2, Cx4): `/internal/v1/` 当前与公网共用 listener + Bearer JWT；独立 listener 或 service-token/mTLS 策略 | 4-8h | `runtime/bootstrap/bootstrap.go` + cell 路由注册拆分 | PR#143 review F1 |

> 规模风险：Cx4 + 工时 8h 上界。如实施发现 blast radius 大，降级到 Phase X。

### PR-R-AUTH-STRICT (🟡 可延后): legacy token strict 模式（搭车，1h，产品确认后）

| 任务 | 工时 | 涉及文件 | 触发条件 |
|------|------|----------|----------|
| **AUTH-LEGACY-TOKEN-STRICT-01** (P2, Cx2): ~~3-part servicetoken 迁移完成~~ PR#162 已删 2-part 分支；本项改为增 strict 模式开关 + 淘汰计划 + legacy 占比 metrics 看板 | 1h | `runtime/auth/servicetoken.go` | 产品确认迁移窗口 |

---

## Phase P: pkg + 工具链偿债（~7h，1 个 PR）

> **2026-04-18 update**: PR#163 已完成 PR-P-CB（CB-IFACE-01 + CB-ENCAP-01）；PR#164 已完成 PR-P-CMD（CMD-MODE-01 + CMD-REFACTOR-01 + F-7 BUILD-OUTDIR-01）；`.env.example` GOCELL_S3_REGION=us-east-1 已存在（line 21）。本 Phase 仅剩 PR-P-QUERY。

### PR-P-QUERY: cursor 稳定性 + 轮换接线（7h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **PR-X2 pkg/query 稳定性**: `ParsePageRequest` cursor 长度上限 + `PR#165 F1-2` `configpublish.WithDemoFailOpen` 与 `query.RunMode` 语义整合（统一 cell 级 RunMode 注入）+ `PR#165 F3-1` `loadCursorCodec` helper 单测（wrap/errcode 链断言）+ `PR#165 F5-1` `RunModeForDemo` godoc "Do not extend" 警告 + PR160-P1-C codec nil 构造期 fail-fast（Service 层，非 cell 层 fallback） | 3h | `pkg/query/` + `cmd/core-bundle/` + `cells/*/slices/*/service.go` | PR#160 六席位审查 + PR#165 reviewer |
| **PR-X3 cursor key rotation 接线** (🟡 可延后): `NewCursorCodec(current, previous)` 启动接线 + `GOCELL_CURSOR_PREVIOUS_SIGNING_KEY` 双 env 通道 + 轮换兼容回归。对标 K8s `--service-account-key-file`、gorilla/securecookie `CodecsFromPairs` | 4h | `pkg/query/codec.go` + `cmd/core-bundle/main.go` | PR#160 六席位审查 |

> PR-X2 先做 → PR-X3 再做（依赖 PR-X2 完成 RunMode 语义统一）。

---

## Phase A: adapter 层偿债（~14.5h，3 个 PR 并行）

> 前置: Phase K PR-K-OBS-CONTRACT ✅ 完成（OBS-RELAY-REGISTER-ATOMIC 形成契约后 adapter 才能实现）。

### PR-A-INTEG (🟡 可延后): 集成测试补全（4h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **P4-TD-05**: outbox 全链路 3-container 集成测试（PG+RMQ+app） | 2h | `adapters/postgres/` + `adapters/rabbitmq/` | Phase 4 review |
| **RL-INT-01**: Relay PG 集成测试 | 2h | `adapters/postgres/outbox_relay_test.go` | PR#46 review |

### PR-A-HARDEN: 生产安全 + PoolStats + broker health（8.5h，混合标签）

> 搭车合规：PR-P0-READYZ-BROKER 改 `connection.go`，POOLSTATS-IFACE-01 也改 `connection.go`，合并一次落地避免两次 rebase。

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **PR-P0-READYZ-BROKER 搭车**: `Connection.Health() error` + bootstrap health checker 自动注册 | 2h | `adapters/rabbitmq/connection.go` + `runtime/bootstrap/` | 2026-04-18 外部审查 |
| **RL-MIG-01** (🟠 条件延后，首次 prod migration 前必做): `CREATE INDEX CONCURRENTLY` online-safe 索引 | 2h | `adapters/postgres/migrations/` | PR#46 review |
| **RL-SUB-01** (🟡 可延后): 入站 ID 校验（空/过长 message ID） | 1h | `adapters/rabbitmq/subscriber.go` | PR#46 review |
| **#31** (🟡 可延后): RabbitMQ backoff + FailOpen enum 清理 | 2h | `adapters/rabbitmq/` | Wave 2 残留 |
| **POOLSTATS-IFACE-01** (🟡 可延后): 三个 adapter PoolStats 公共接口（OTel collector 消费） | 1h | `adapters/postgres/pool.go` + `redis/client.go` + `rabbitmq/connection.go` | PR#134 review |
| **POOLSTATS-JSON-01**: PoolStats camelCase json tags | 0.5h | 同上 | PR#134 review |

### PR-A-CI-OTEL (🟡 可延后): CI 供应链 + OTel 夜间兼容性（4h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **CI-DIGEST-01** (🟡): testcontainers 镜像 tag+digest 双固定 | 1h | `adapters/*/integration_test.go` | PR#139 review |
| **CI-LINT-PIN-01** (🟡): golangci-lint patch 级固定 + dependabot 自动升级 | 1h | `.github/workflows/ci.yml` | PR#139 review |
| **OBS-LGTM-INTEGRATION-01** (P2, Cx3, 🟡): `//go:build integration` tag 的夜间 OTel collector 真实 OTLP 协议兼容性测试（grafana/otel-lgtm 或 otel-collector-contrib） | 2h | `adapters/otel/integration_test.go` | PR#157 review S6-04 |

---

## Phase S: slice/cell 收口（~21h，按紧迫度分 3 个 PR）

> 底座稳固后做。顺序：错误分类 → 授权/契约完整性 → DTO/事件 typing。

### PR-S-CONFIG-HARDEN: config-core 错误语义 + demo 路径（5h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **CONFIG-DEMO-FAILOPEN-01** (P1, Cx2): `configpublish/service.go:188-194` demo 模式 publisher 失败仅 `logger.Warn` 后 `return nil`，与 L2 声明不符；durable 模式移除 fail-open，demo fail-open 仅保留在显式 `DiscardPublisher{}` 或 `Assembly.Mode == Demo` | 2h | `cells/config-core/slices/configpublish/service.go` | PR#157 post-merge review |
| **REPO-SCAN-CLASSIFY-01** (P2, Cx2, 🟠 条件延后，PG-DOMAIN-REPO 接线前必做): `config_repo.go::GetByKey` / `GetVersion` 把所有 Scan 错误映射为 `ErrConfigRepoNotFound`；改用 `errors.Is(err, sql.ErrNoRows)` 判 not found，其他返回 `ErrInternal` 保留 `InternalMessage` | 2h | `cells/config-core/internal/adapters/postgres/config_repo.go` | PR#157 post-merge review |
| **CONTRACT-ERROR-SCHEMA-01** (P2, Cx1, 🟡 可延后): `publish/v1` + `rollback/v1` contract.yaml `responses` 新增 401/403 entries 引用共享错误 schema | 1h | `contracts/http/config/{publish,rollback}/v1/contract.yaml` | PR#157 post-merge review |

### PR-S-DTO-EVENT: DTO nil 语义 + 事件 typed payload（6h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **DTO-NIL-SEMANTIC-01** (P2, Cx2): 12+ handler 写成功响应前校验领域对象非 nil，返回 errcode 而非空 DTO；避免 converter nil guard 把上游不变量异常"平滑"为空 data 成功响应 | 3h | 12+ `cells/*/slices/*/handler.go` | PR#158 six-seat review |
| **EVENT-PAYLOAD-TYPED-01** (Cx2): sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct（对齐 cell-patterns.md 北极星） | 3h | 6 个 `service.go` + event contract schemas | PR#133 re-review |

### PR-S-RBAC: rbacassign 治理 + validate CI evidence（4h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **RBAC-REVOKE-POST-01** (🟡 可延后): `DELETE /internal/v1/access/roles/revoke` 改为 `POST` 避免 DELETE body 代理兼容问题 | 1h | `cells/access-core/slices/rbacassign/handler.go` + `contracts/http/auth/role/revoke/v1/contract.yaml` | PR#143 review 6.2 |
| **RBAC-LAST-ADMIN-GUARD**: `service.Revoke` 检查剩余 admin 数量；`ports.RoleRepository` 新增 `CountByRole` | 1h | `cells/access-core/slices/rbacassign/service.go` + `ports/` | PR#143 review 2.3 |
| **VALIDATE-EVIDENCE-CI-01** (P2, Cx2, 🟡 可延后): CI 新增独立 `metadata-check` job（`gocell validate` + `check contract-health`），失败阻断 PR；PR template 增"metadata gate"勾选项 | 1h | `.github/workflows/ci.yml` + PR template | PR#155 review F7 |
| **GOCELL-VALIDATE-FMT-REDESIGN** (P3, 搭车): `printResult` 改为 `[CODE] msg (field: X) / at file:line:col` 两行格式支持 IDE 点击跳转 — 已在 Phase K PR-K-VALIDATOR 搭车覆盖 | — | — | PR#152 follow-up |

### PR-S-RBAC-OUTBOX: 角色变更 outbox 化（6h，可选升级为 Phase X）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **H1-7 RBAC-OUTBOX-MIGRATION** (P2): `rbacassign.Service` "角色变更 → 会话失效"双写 → transactional outbox 原子写入 + consumer 异步失效 session；前置 outbox consumer 基础设施 | 6h | `cells/access-core/slices/rbacassign/service.go` + `cells/access-core/slices/sessionlogout/consumer.go`（新） + contract event schemas | PR#149 review round 2 |

---

## Phase F: 功能扩展 + 发布（~14h + 发布活动）

> 底座稳固 + slice 收口后做，让 README 和 v1.0 反映最终 API。

### PR-F-FEAT: 前后端联调缺口补全（8h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **FEAT-1 DEVICE-LIST-API** (P1): 新建 `device-list` slice + `GET /api/v1/devices` 分页 + contract + contract_test；同步验证 Phase K 的 CONTRACT-LIST-LINT-01 规则 | 3h | `cells/device-cell/slices/device-list/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| **FEAT-2 FLAG-WRITE-API** (P1): `PUT /api/v1/config/flags/{key}` 写入端点 + contract + contract_test | 3h | `cells/config-core/slices/configwrite/` + `contracts/http/config/flags/write/v1/` | backend_issues.md #2 |
| **SYSTEM-TOPOLOGY-API** (🟡 可延后): `GET /internal/v1/system/topology` 返回 cell/slice/contract 拓扑 JSON；基于 `kernel/registry` 现有数据构建 | 4h | 新 slice 或 `runtime/bootstrap/` | 历史 Batch 8 |

### PR-F-DOC: 文档 + 示例收口（7h）

| 任务 | 工时 | 涉及文件 | 来源 |
|------|------|----------|------|
| **#5 AUTH-DX-01**: README + seed 用户 + sso-bff walkthrough；修复 refresh curl `sessionId`→`refreshToken`、logout 204 空 body jq 坑、audit `.createdAt` vs `.Timestamp` | 4h | `README.md` + `cells/access-core/internal/mem/` + `examples/sso-bff/README.md` | 6B + P4 review |
| **ADR-RUNMODE-TRANSLATION-01** (P2, Cx1): 记录 `kernel/cell.DurabilityMode → pkg/query.RunMode` 的分层翻译模式 — pkg 不 import kernel，cell 构造期负责翻译；对标 zeromicro/go-zero `ServiceConf.Mode` 默认严格原则 | 1h | `docs/architecture/` 新 ADR | PR#165 reviewer F1-1 |
| **P2-T-02 audit e2e 测试**: Journey 级验收 | 2h | `journeys/` + integration test | 历史 Batch 8 |

### Wave 4 发布

| 任务 | 工时 |
|------|------|
| Review cells/ | 4h |
| Review examples/ | 2h |
| 性能基准 | 4h |
| **v1.0 tag** | — |

---

## Phase X: 大型独立项 + 深层重构（发布后排期）

### PR-X-PG-REPO: PostgreSQL 域 Repository（3-5d）

> 规模大，独立 PR 串。必须一次性覆盖联动项，否则元数据/代码漂移。

| 任务 | 工时 | 涉及文件 |
|------|------|----------|
| 4 个 migration DDL（users/sessions/roles/devices+commands）+ `CONFIG-VERSIONS-MIGRATION-01` (`004_create_config_entries_and_versions.sql`) | 1d | `adapters/postgres/migrations/` |
| 5 个域 Repository PostgreSQL 实现 | 2d | `cells/*/internal/adapters/postgres/` |
| 落地联动（必须同 PR 或紧邻 PR）：**RBAC-ASSIGN-LEVEL-UPGRADE-01** L0→L1；**SEED-ROLE-IFACE-01** 去 type assertion；**ACCESS-LEVEL-AUDIT-01** slice.yaml 校正；**AUTH-CACHE-01 激活** Redis session cache | 1-2d | 联动点 |

### PR-X-ADAPTER-SPLIT: adapter 分层重整（4h）

| 任务 | 工时 | 涉及文件 |
|------|------|----------|
| **AL-01**: `outbox_relay.go` 轮询调度 → `runtime/outbox/relay.go` | 2h | `adapters/postgres/outbox_relay.go` → `runtime/outbox/relay.go` |
| **AL-02**: `distlock.go` 续期/TTL → `runtime/` | 2h | `adapters/redis/distlock.go` → `runtime/` |

### PR-X-LINT-MODERN: 全仓 golangci-lint 现代化（6h）

| 任务 | 工时 | 涉及文件 |
|------|------|----------|
| **LINT-MODERN-01** (P3, Cx2): 预存量清理 — rangeint / stringsseq / forvar / inline / testingcontext / any / nhooyr.io websocket → github.com/coder/websocket；**不混入功能 PR**，独立 modernization PR；websocket 迁移单独 PR | 6h | 全仓 |

### Wave 2-3 功能

| 任务 | 工时 | 前置 |
|------|------|------|
| **WM-35 BFF handler 接入 cookie session** | 2d | WM-2-F1 ✅ |
| **WM-36 SecureCookie key rotation** | 1.5d | WM-35 |
| **WM-7 泛型 BulkResult** | 1d | 设计面广 |

### access-core 长期重构

| 任务 | 工时 | 前置 |
|------|------|------|
| **P3-TD-11 domain 模型拆分** User/Session/Role | 4h | PR-X-PG-REPO |
| **AUTH-CACHE-01 Redis session cache**（若未在 PG-REPO 激活） | 4h | PG-REPO |
| **SOL-B-01 Claimer lease 续租**（#28） | 4h | L4 API ✅ |

### 触发条件项（不排期）

| 任务 | 触发条件 |
|------|----------|
| AUTH-PROVIDER-EXPORT-01 | 第二个 auth provider cell |
| AUTH-ISSUE-OPTIONS-01 | `Issue()` 第 5 个参数 |
| DEVICE-ENQUEUE-RBAC | 多租户 operator |
| CB-RESILIENCE-PACKAGE-01 | 非 HTTP 的 CB 消费方 |
| AUTH-SIGNER-01 `crypto.Signer` | golang-jwt v6 发布 |

---

## 设计决策维持（2026-04-18 复核）

| 决策 | 结论 |
|------|------|
| F1-3 DurabilityDurable + in-memory | ✅ 维持不修（effectiveMode + adapterInfo + slog 日志已透明标注；PG-REPO 排队中是真修路径；fail-fast 会阻断开发路径） |
| F1.2/F2.5/F4.2/F6.2 auth 四项 | ✅ 维持不修（详见 backlog 设计决策记录 PR#159）|
| F6 slice.yaml allowedFiles 双路径 | ✅ 维持不修（项目惯例，FMT-14 守护）|
| I2 generator 全量遍历 contracts | ✅ 维持不修（FMT-09 守护 + fail-fast defense-in-depth）|

---

## 执行总览

```
Phase 0     ✅ 历史 PR 闭合

Phase 0.5   P0 正确性回归      ~10h（2-3 工作日内优先完成）
  PR-P0-AUTH-INTENT (6.5h)
  PR-P0-AUTHZ-CONFIGWRITE (1.5h)
  PR-P0-READYZ-BROKER (2h)  ← 可合并进 Phase A PR-A-HARDEN

↓ 自底向上偿债（Phase K/R/P/A 可在 Phase 0.5 完成后并行推进）

Phase K     kernel 层            ~13h
  PR-K-VALIDATOR (9h) + PR-K-METAPERF (4h) + PR-K-OBS-CONTRACT (2h)

Phase R     runtime 层           ~11-15h
  PR-R-BOOT-COGNIT (4h) + PR-R-OBS-AUTOWIRE (3h) + PR-R-ROUTER-METHOD (4h)
  PR-R-INTERNAL-LISTENER (4-8h, 规模风险) + PR-R-AUTH-STRICT (1h, 触发)

Phase P     pkg + 工具链          ~7h  ← PR-P-CB ✅ PR#163 / PR-P-CMD ✅ PR#164 扣除
  PR-P-QUERY (7h)

Phase A     adapter 层            ~14.5h
  PR-A-INTEG (4h) + PR-A-HARDEN (8.5h, 含 READYZ-BROKER 搭车) + PR-A-CI-OTEL (4h)

↓ 上层收口

Phase S     slice/cell 收口       ~21h
  PR-S-CONFIG-HARDEN (5h) + PR-S-DTO-EVENT (6h) + PR-S-RBAC (4h)
  PR-S-RBAC-OUTBOX (6h, 可选)

↓ 发布

Phase F     功能 + 发布           ~15h + 发布活动
  PR-F-FEAT (8h, Device List + Flag Write + Topology)
  PR-F-DOC (7h, README + ADR-RUNMODE-TRANSLATION + audit e2e)
  Wave 4 Review + v1.0 tag

Phase X     大型独立 + 长期重构    按需排期
  PG-REPO (3-5d) / ADAPTER-SPLIT (4h) / LINT-MODERN (6h)
  WM-35/36/7 / P3-TD-11 / AUTH-CACHE-01 / SOL-B-01

当前核心路径剩余（不含 Phase X，2026-04-18 扣除 PR#163+#164 后）:
  Phase 0.5 + K + R + P + A + S + F ≈ 91-95h（约 11-12 工作日）
```

---

## 与 20260416-foundation-first-plan.md 的差异

| 维度 | 20260416 版 | 20260418 v2 版 |
|------|-------------|----------------|
| 底座分层 | Phase 1-4 按层粒度但 PR 级包装仍按 "审查来源" 聚类 | 严格按层分 Phase K/R/P/A，PR 按层归属 |
| slice/cell 项 | 混在 Phase 1-5 的各 PR 里 | 全部收口到 Phase S，按错误语义 / DTO+事件 / RBAC 治理分组 |
| P0 回归 | Phase 0.5 单独一节（跨层） | Phase 0.5 保留，读者明确知道不受 bottom-up 约束 |
| Phase X 独立项 | 零散分布在 Phase 6 / Batch 8 | 统一集中到 Phase X，含 PG-REPO / 重构 / Wave2-3 / 触发项 |
| 大条目（PG-REPO / INTERNAL-LISTENER / RBAC-OUTBOX） | 散落 Phase 2-6 | Phase X（PG-REPO）/ Phase R 规模风险（INTERNAL-LISTENER）/ Phase S 可选（RBAC-OUTBOX），明确规模标注 |

---

## 风险与缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| Phase 0.5 PR-P0-AUTH-INTENT 改 `runtime/auth/jwt.go` 会影响所有认证调用点 | 回归面广 | Issue(intent) 默认 `access`；老调用 zero-diff；集成测试覆盖 refresh 路径拦截 |
| Phase A PR-A-HARDEN 合并 READYZ-BROKER + POOLSTATS 同改 `connection.go` | PR 内部冲突 | 同一 PR 一次改完，避免两次 rebase；文件级原子 |
| Phase R PR-R-INTERNAL-LISTENER 可能超时 | 拖慢关键路径 | Cx4 工时 4-8h 上界；若实施发现 blast radius 大，降级到 Phase X |
| Phase K PR-K-VALIDATOR 改 kernel 接口（Issue struct + ProjectLocator） | 上层 cmd/governance 回归 | 保留旧签名，新 JSON printer 走新接口；Governance 规则按顺序迁移 |
| Phase S 依赖 Phase A 完成（REPO-SCAN-CLASSIFY 需要 PG adapter 错误语义稳定）| 串行阻塞 | Phase S 只 touch cells/config-core/internal/adapters/postgres，不触 PG repo 接口；Phase A/S 可真并行 |
| 外部审查 F1-3 决策复议风险 | 若重开会牵动 PG-REPO 前置 | 2026-04-18 复核维持决策，风险收敛 |
| AUTH-SIGNER-01 前置 golang-jwt v6 不可控 | 阻塞 | 已标记触发条件项，不占工作量 |

---

## 并行度建议

- **Phase 0.5 三条 PR** 独立文件，可同时发起；PR-P0-READYZ-BROKER 如选择合并到 Phase A，则本 Phase 只发两个 PR
- **Phase K 三条 PR** 改不同文件（validator / parser bench / outbox metric），可并行
- **Phase R 三条主 PR** 改不同文件，可并行；INTERNAL-LISTENER 单独排
- **Phase P 只剩 PR-P-QUERY 一条**（PR-P-CB ✅ PR#163，PR-P-CMD ✅ PR#164）
- **Phase A 三条 PR** 独立，可并行
- **Phase S 三条 PR** 改不同 slice 域，可并行
- **Phase F PR-F-FEAT + PR-F-DOC** 可并行

底座偿债（K/R/P/A）四层之间建议**不严格阻塞**，但建议 Phase K PR-K-OBS-CONTRACT 先于 Phase A 完成（OBS-RELAY 契约定义后 adapter 才改实现）。其余跨层项无强依赖。
