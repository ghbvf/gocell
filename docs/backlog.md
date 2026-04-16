# GoCell Backlog

> 只含待办事项。已完成项归档至 `docs/reviews/archive/`。
> 更新日期: 2026-04-16
> Batch 1-5: ✅ 全部完成 (PR#67-114, 48 PRs)
> Wave 1 进行中: ✅ PR#116-138 (23 PRs 已合入)
> PR#129 (Sentinel DSN redaction) / PR#130 (Bolt journey catalog) — 外部 PR，未合入
> 旧版备份: `docs/reviews/archive/20260415-backlog-pre-pr127-cleanup.md`

---

## Wave 1: 待做

> 已合入 PR#116-128。剩余按优先级分层：P1 正确性 → MUST（v1.0 阻塞项）→ SHOULD（建议 v1.0 前做）。
> DEFER 项已移至 Batch 8。

### P1 正确性

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| 5 | **AUTH-DX-01** README + seed 用户 + sso-bff walkthrough: auth 已拦截全部业务路由，README 失效；sso-bff README 缺 refresh/GET user/event 消费 demo (P4-P1-6)。具体漂移: refresh curl 发 `sessionId` 实际需 `refreshToken`；logout 204 空 body 管道 jq 失败；audit jq 用 `.createdAt` 实为 `.Timestamp` | 4h | `README.md` + `cells/access-core/internal/mem/` + `examples/sso-bff/README.md` | 6B + P4 review |
| 6 | **TPUB-01** TestPubSub 真实 adapter 认证: conformance harness 替换 sleep + 接入 RabbitMQ adapter | 4h | `kernel/outbox/outboxtest/` + `adapters/rabbitmq/` | 6B |

### 运维 + 基础设施

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| 10 | ✅ **Watcher 核心增强** R97-02(debounce) + R97-F1(symlink-pivot) + WM-34-F1(目录级监听) + F2(metrics) + F3(key 过滤) + R97-04(DeepCloneValue) + R97-R3-02(ShutdownDrain channel 同步) | 7h | `runtime/config/watcher.go` + `store.go` | 6A | PR#132 |
| 11 | ✅ **Watcher 状态面 + 连接池指标** R97-F3(Generation/observedGeneration) ✅ PR#132 + OPS-5(PG/Redis/RMQ 连接池指标) ✅ PR#134 + CFG-DRIFT-READYZ(HasDrift→/readyz hard-failure checker) ✅ PR#134 | 4h | `runtime/config/` + `adapters/postgres/` + `adapters/redis/` + `adapters/rabbitmq/` + `runtime/bootstrap/` | 6A |

### MUST: v1.0 前必须做（breaking change / 安全 / 阻塞 review）

| # | 任务 | 工时 | 文件 | 来源 | 合并 PR |
|---|------|------|------|------|---------|
| 25 | ✅ **HSTS 加固** C-H4: `security_headers.go` 补 `includeSubDomains; preload` | 0.5h | `runtime/http/middleware/security_headers.go` | P2 tech-debt | PR#131 |
| 28e | ✅ **AUTH-TTL-CONST-01** `accessTokenTTL` 3 处重复定义 → `auth.DefaultAccessTokenTTL` | 0.5h | `runtime/auth/jwt.go` | PR#127 review | PR#131 |
| 24 | ✅ **Trace trust policy** OBS-REQID-TRUST: `RequestIDWithOptions` + bootstrap 自动信任边界接线（TRUST-POLICY-01 已在 PR#128 完成） | 2h | `runtime/http/middleware/request_id.go` + `router.go` + `bootstrap.go` | 5B PR#112 review | PR#131 |
| 28d | ✅ **AUTH-REFRESH-VERIFIER-01** session-aware verifier 注入 refresh service，Init 重排序 | 2h | `cells/access-core/cell.go` | PR#127 review | PR#131 |
| 27v | ✅ **DOMAIN-EVENT-DTO-DECOUPLE** 事件 payload 解耦 + domain json tags 移除: order-create/device-register `json.Marshal(entity)` → typed event struct + domain.Order/Device/Command 移除 json tags + handler 裸 map → typed DTO | 1.5h | domain + 3 handler + 2 service | PR#126 review | PR#133 |
| 27u | ✅ **DTO-COVERAGE-GAP** 全量 handler typed DTO 对齐（含 #27l）: session TokenPair + featureflag EvaluateResult → handler DTO + order-create/device-register/device-status 裸 map → typed DTO | 2h | 5 handler + 3 service | PR#126 review | PR#133 |
| 27c | ✅ **L2-HARD-GATE-01** L2 cell 启动门禁对齐 order-cell Init() fail-fast 模式（`DiscardPublisher{}` 作为 demo 信号），消除声明能力与运行能力漂移。含 audit-verify durable 路径修复：VerifyChain 引入与 audit-append 同构的事务执行路径，durable 模式下 outbox 失败向上返回错误（当前被吞） | 4h | `cells/access-core/cell.go` + `cells/config-core/cell.go` + `cells/audit-core/cell.go` + `cells/audit-core/slices/auditverify/service.go` | PR#119 review P1-1 + PR#133 review Issue 2 | PR#135 |
| 27c-2 | ✅ **BOOTSTRAP-STRICT-MODE** DurabilityMode + Noop marker: durable assembly 拒绝 NoopWriter/NoopTxRunner/DiscardPublisher。kernel/cell 声明式 + Init() fail-fast | 2h | `kernel/cell/durability.go` + `kernel/assembly/assembly.go` + 5 cells | PR#133 review Issue 4 | PR#136 |
| 14 | ✅ **order+demo+examples 修复** 统一 outbox 路径(NoopWriter+NoopTxRunner)、删除 createDemo 分叉、NIL-PUB-P1 device-cell fail-fast、迁移 5 处 noopTxRunner、修复 sso-bff context-dropping bug | 3h | `cells/order-cell/` + `cells/device-cell/` + `examples/` + `kernel/persistence/` | 6B | PR#136 |
| 27s | ✅ **CONTRACT-SCHEMA-COVERAGE** 6 个端点有 DTO 但无 HTTP contract schema + contract_test（configwrite/configpublish/auditquery/rbaccheck/device-command/order-query）。含 PATCH-TYPE-VALIDATE: identitymanage PATCH handler 对字段类型错误静默忽略，补 contract schema 时同步加 handler 类型校验返回 400 | 6h | `contracts/http/` + 6 个 slice `contract_test.go` + `cells/access-core/slices/identitymanage/handler.go` | PR#126 review + PR#112-136 集成审查 P1-3 | PR#138 |
| 27t | ✅ **CONTRACT-TEST-REAL-HANDLER** contract_test 使用硬编码 fixture 而非调用真实 handler，schema 符合性 ≠ handler 符合性 | 4h | `cells/*/slices/*/contract_test.go` | PR#126 review | PR#138 |
| 27i | ✅ **CONTRACT-CLIENTS-01** 9 个 HTTP contract `endpoints.clients: []` 缺少消费方声明（http.auth.refresh、http.config.flags.*、http.order.*、http.device.*），需产品确认各端点的 BFF/edge 消费方后填入 | 1h | `contracts/http/*/contract.yaml`（9 文件） | PR#125 review | PR#138 |

### SHOULD: v1.0 前建议做（改善质量，不阻塞）

| # | 任务 | 工时 | 文件 | 来源 | 合并 PR |
|---|------|------|------|------|---------|
| 131a | ✅ **REFRESH-ROLE-WARN** sessionrefresh role-fetch error 静默忽略，补 `slog.Warn` 对齐 sessionlogin 模式 | 0.5h | `cells/access-core/slices/sessionrefresh/service.go` | PR#131 review F2-3 | PR#131 |
| 131b | ✅ **BOOTSTRAP-TRUST-TEST-01** bootstrap 信任边界自动接线（`authPublicEndpoints` → tracing + request_id）无集成测试。现有 bootstrap 测试全部依赖 `net.Listen`，需 router-only 测试路径或 mock listener | 2h | `runtime/bootstrap/bootstrap_test.go` | PR#131 review F3-1 | PR#133 |
| 27n | ✅ **HANDLER-TEST-CAMELCASE-ASSERT** 13 个 handler_test.go 无显式 camelCase key 断言（如 `assert.Contains(body, "createdAt")`），camelCase 合规由 contract_test + schema 守护 | 2h | 12+ `handler_test.go` | PR#126 review | PR#133 |
| 27k | ✅ **DTO-CONVERTER-UNIT-TEST** 8 个 DTO converter 函数（toXxxResponse）无独立单测，仅靠 handler httptest 间接覆盖。若 converter 增加条件逻辑需补专项测试 | 2h | 6 个 `handler_test.go` | PR#126 review | PR#133 |
| 20 | ✅ **decode 加固** DECODE-STR-01 classifyDecodeError 加固(CutPrefix+guard test) + REQID-RAND-ERR rand.Read 清理 + MAIN-TEST-CLEANUP 类型安全错误匹配 | 2h | `pkg/httputil/decode.go` + `runtime/http/middleware/request_id.go` + `cmd/core-bundle/main_test.go` | 6B | PR#140 |
| 19 | ✅ **CI 增强** T1-7(golangci-lint) + TC-PIN-01(testcontainers 镜像 pin 到 patch 版本) | 2.5h | `.github/ci.yml` + `adapters/*/integration_test.go` | 6B | PR#139 |
| 138b | ✅ **CONTRACT-LIST-LINT-01** `gocell check contract-health` 增加 list 响应格式检查：response schema 含 `data: array` 时必须包含 `hasMore`。FMT-15 治理规则 + injectable readFile | 2h | `kernel/governance/` | PR#138 review P1-3 | PR#142 |
| 27b | ✅ **SLICE-ALLOWEDFILES-01** SliceMeta 新增 AllowedFiles 字段 + AllowedFiles() kebab→no-dash 归一化 + FMT-14 治理规则 + 22 个 slice.yaml 显式声明 | 3h | `kernel/cell/base.go` + `kernel/metadata/types.go` + `kernel/governance/` + all `slice.yaml` | PR#119 review | PR#142 |
| 138a | ~~**CONFIGPUBLISH-REDACT-01**~~ 已归入 PR-H2 H2-2，此处删除 | — | — | — | PR-H2 |
| 28a | **AUTH-CACHE-01** session 验证 DB round-trip 缓存: 每请求 `GetByID` 查主库，real adapter 下需 Redis short-TTL（5-15s）session cache + 撤销时主动失效。可选: circuitbreaker 包住 `GetByID`（仅 infra error 触发） | 4h | `cells/access-core/slices/sessionvalidate/service.go` + `adapters/redis/` | PR#127 review | MG-G Auth ops |
| 28b | ✅ **AUTH-HEALTH-01** session DB 健康检查: session repo `Health()` + `SessionHealthChecker()` 类型断言发现 + main.go 注册 `WithHealthChecker("session-store", fn)` | 2h | `cells/access-core/internal/mem/` + `cells/access-core/cell.go` + `cmd/core-bundle/main.go` | PR#127 review | PR#134 |

---

## Wave 2: 串行后续

| # | 任务 | 前置 | 工时 |
|---|------|------|------|
| 28 | **SOL-B-01** Claimer lease 续租 | L4 API ✅ | 4h |
| 31 | **RabbitMQ 代码清理** backoff + FailOpen enum | RMQ ✅ | 3h |
| 32 | **cursor 可观测** invalid 结构化日志 | cursor (#15 B8) | 1h |
| 33 | **WM-35** BFF handler 接入 cookie session ★ | WM-2-F1 ✅ | **2d** |

## Wave 3: Auth 收尾

| # | 任务 | 前置 | 工时 |
|---|------|------|------|
| 34 | **WM-36** SecureCookie key rotation ★ | WM-35 | **1.5d** |

## Wave 4: Review + 发布（~16h）

| # | 任务 | 工时 |
|---|------|------|
| 35 | Review cells/ | 4h |
| 36 | Review examples/ | 2h |
| 37 | Review 报告汇总 | 2h |
| 38 | 发布文档 | 4h |
| 39 | 性能基准 | 4h |
| 40 | **v1.0 tag** | — |

---

## 关键路径

```
★ Auth 链: WM-2-F1 ✅ → WM-35 (2d) → WM-36 (1.5d) → Review (2d) = 剩余 5.5 工作日
```

---

## Batch 8: P2 偿债（v1.0 后）

> 从 Wave 1 下沉的 DEFER 项 + 原 Batch 8 项，按 PR 组合并。
> 前置: v1.0 tag 发布后。不阻塞发布。

| PR 组 | 任务 | 工时 |
|-------|------|------|
| **OBS 全家桶** | META-SIZE-01(Metadata key 数/大小上限) + OBS-TABLE-01(table-driven 改写) + OBS-METRIC-01(bridge counter/histogram) + OBS-DX-01(cloneMetadata 导出 + wrapper 清理 + godoc) + OBS-DOC-01(IsReservedMetadataKey usage example) + #23(OTel 覆盖率 OTEL-COV-01 testcontainers 集成测试, `adapters/otel/`) | 7h |
| **Outbox 治理** | OUTBOX-GUARD-01(NoopWriter/DiscardPublisher lint 约束) + DISCARD-OBS-01(DiscardPublisher Logger 注入 + counter) + OUTBOX-RECEIPT-01(`outbox.Receipt` alias 全仓迁移 `idempotency.Receipt`) | 4h |
| **order-cell 收口** | ✅ ORDER-DEMO-01 PR#136(统一 outbox 路径) + ✅ NIL-PUB-P2 PR#136(Init() fail-fast) + ✅ CheckNotNoop PR#137 | 2h → 0h |
| **Cursor 全家桶** | #15(cursor 回归矩阵 CURSOR-TEST-01 + CUR-HDL-01: 5 个分页入口补 malformed/missing-scope/cross-context 三类回归, `cells/*/handler_test.go` + `service_test.go`) + WM-6-F6(泛型 cursor helper)/F7(cursor 日志收口)/F1(prod guard) + TX-NIL-01(nil-safe 注释) + ✅ #27e(NOOP-TX-SHARED-01 PR#136: `kernel/persistence.NoopTxRunner`) + #32(cursor 可观测 CURSOR-P2-02 invalid 结构化日志, `cells/audit-core/`) | 8.5h → 7.5h |
| **metadata parser** | META-67-01(strict unknown-field reject) + META-67-02(位置信息错误报告) + META-67-03(cross-file 引用校验) | 2.5h |
| **auth 增强** | WM-2-F2(HMAC replay 防护) + WM-2-F3(auth metrics) + AUTH-SIGNER-01(`SigningKeyProvider` 返回 `crypto.Signer` 替代 `*rsa.PrivateKey`，需自定义 jwt SigningMethod，前置: golang-jwt v6 或 wrapper) + RBAC-LAST-ADMIN-GUARD(service.Revoke 检查剩余 admin 数量，需 `CountByRole` 加入 RoleRepository 接口, `cells/access-core/slices/rbacassign/service.go`) (discovered via PR#143 review 2.3) | 7h |
| **rbac-assign 治理** | RBAC-REVOKE-POST-01: `DELETE /internal/v1/access/roles/revoke` 改为 `POST` 避免 DELETE body 代理兼容问题（`cells/access-core/slices/rbacassign/handler.go` + `contracts/http/auth/role/revoke/v1/contract.yaml`）(discovered via PR#143 review 6.2) | 1h |
| **auth 测试 DX** | AUTH-SLOG-01(KeySet/servicetoken 注入 slog.Handler 替代全局 `slog.SetDefault`，消除并行测试风险) + AUTH-NOWFUNC-01(`var nowFunc` 包级状态改为实例字段注入) | 3h |
| **access-core 重构** | P3-TD-11 domain 模型拆分 User/Session/Role（前置: Wave 1 #13 Session TOCTOU ✅ PR#119） | 4h |
| **集成测试补全** | P4-TD-05(outbox 全链路) + RL-INT-01(Relay PG 集成) + P2-T-02(audit e2e) | 6h |
| **迁移+订阅** | RL-MIG-01(online-safe 索引 CONCURRENTLY) + RL-SUB-01(入站 ID 校验) | 3h |
| **CMD 重构** | CMD-MODE-01(fail-fast) + CMD-REFACTOR-01(app 包提取) | 3.5h |
| **批量操作** | WM-7 泛型 BulkResult | 1d |
| **Demo/日志规范化** | ✅ #27f(TEST-UNUSED-VAR-01) + ✅ #27g(DEMO-WARN-STRUCTURED-01) + ✅ #27h(DEMO-PUBLISH-WARN-01) + ✅ #28f(SSO-BFF-AUTH-SYNC-01) — 全部在 PR#135 搭车完成 | 3h → 0h |
| **Router 信任边界收敛** | RTR-PUBLIC-POLICY-01: router 把 public endpoint 策略分散为三套独立入口（`WithAuthMiddleware` L151 + `WithTracingOptions` L90 + `WithRequestIDOptions` L102），bootstrap 在 L584-596 临时收拢成统一 `isPublic`，但直接使用 router 的调用方（如 sso-bff、未来 edge cell）仍需手动保持三处一致。**处理方案**: ① Router 新增 `WithPublicEndpoints(paths []string)` 组合选项，内部构建 `isPublic` 并同时配置 auth bypass + tracing `WithPublicEndpointFn` + request_id `WithReqIDPublicEndpointFn`；② 保留现有细粒度选项（`WithTracingOptions`/`WithRequestIDOptions`）供需要不对称策略的场景，但标注 godoc `// Advanced: prefer WithPublicEndpoints for standard trust-boundary setup`；③ Bootstrap 的 L584-596 改为调用 `router.WithPublicEndpoints` 消除重复逻辑；④ 补测试: 验证 `router.WithPublicEndpoints` 同时生效 auth bypass + tracing new-root + request_id 拒继承。**根因**: PR#131 新增 request_id 信任边界时复制了 tracing 的配置面模式，但没有把 public policy 提升为一等抽象，导致三处配置漂移风险（F1 HSTS preload 越界即为同类"单处改动、多处影响"的实例）。**开源对比**: otelhttp 用单一 per-request public 判定函数；Kratos 组合层统一建模但不强绑所有中间件；go-zero 路由组层集中表达 auth。结论: 集中策略源是共识，但"一个开关绑定一切"需项目内取舍 | 3h |
| **Config 治理** | CFG-KEYFILTER-WIRE-01(KeyFilter bootstrap 接线: cell 通知循环使用 `KeyFilter.Matches()` 选择性通知，需产品确认语义, `runtime/bootstrap/bootstrap.go`) + CFG-ERRCODE-01(runtime/config 包 `fmt.Errorf` 评估是否迁移 errcode — 当前 runtime/ 层统一用 `fmt.Errorf`，仅在 config 错误需面向用户输出时迁移, `runtime/config/watcher.go` + `config.go`) (discovered via PR#132 6-seat review) | 2h |
| **PR#133 review C3** | F1-ARCH-03(RTR-HSTS-WIRING-TEST router 层 `WithSecurityHeadersOptions` 接线测试) + F2-SEC-03(TRUST-TRACEPARENT-TEST bootstrap 信任边界测试补 `traceparent` 注入向量) + F3-TEST-01(CONVERTER-NIL-INPUT converter 函数 nil 指针输入测试) + F4-OPS-01(BOOTSTRAP-SECHDR-CONVENIENCE 便利包装) (discovered via PR#133 6-seat review) | 3h |
| **序列化边界收敛** | EVENT-PAYLOAD-TYPED-01: sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct (对齐 cell-patterns.md 北极星) (discovered via PR#133 re-review) | 3h |
| **统一健康注册** | ✅ HEALTH-CONTRIBUTOR-01 PR#135: `kernel/cell.HealthContributor` 接口 + bootstrap 自动发现 + access-core 实现 + 删除 main.go 手工接线 | 3h → 0h |
| **PoolStats DX** | POOLSTATS-IFACE-01(三个 adapter PoolStats 无公共接口，OTel collector 需 per-adapter switch — 等 OTel 需求明确后设计公共 `TotalConns()/IdleConns()` 子接口) + POOLSTATS-JSON-01(PoolStats struct 缺 `json:"camelCase"` tags — 当前无 JSON 序列化场景，等 `/debug/poolstats` 端点需求时补加, `adapters/postgres/pool.go` + `adapters/redis/client.go` + `adapters/rabbitmq/connection.go`) (discovered via PR#134 review C3-F1, C3-F4) | 1.5h |
| **PR#135 review C2-C3** | ✅ BOOTSTRAP-ADAPTERINFO-TEST-01 PR#135 + ✅ VALIDATE-MODE-ALLOWLIST-01 PR#135 + ✅ AUDIT-VERIFY-CAMELCASE-01 PR#135 + ✅ AUDIT-VERIFY-LEVEL-01 PR#135(audit-verify L0→L2: 实际写 outbox+txRunner 是 L2 行为，所有 peer slice 均 L2+；slice level 无运行时强制，仅改善元数据准确性) + ✅ TXRUNNER-FAIL-TEST-01 PR#135 (discovered via PR#135 6-seat review) | 3h → 1h |
| **Readyz 安全** | READYZ-VERBOSE-TOKEN-01: `/readyz?verbose` 暴露内部拓扑(cell 名、dependency 名)，health.go godoc 已标注风险。当 health 端口公开可达时，需在 ingress 层限制 `?verbose` 或增加 `WithVerboseToken` bootstrap 选项 (pre-existing, confirmed during PR#134 review) | 2h |
| **Bootstrap 发现机制加固** | ✅ AUTH-DISCOVERY-MULTI-PROVIDER-01 PR#135: 多 authProvider cell 发现循环从 first-wins+break 改为全量扫描+冲突 fail-fast | 1h → 0h |
| **Flaky test** | ✅ SECURECOOKIE-TAMPER-FLAKY-01: 位翻转修复（`encoded[mid]^1`），PR#137 | 0.5h → 0h |
| **Registry 健壮性** | ✅ REGISTRY-CONSUMERS-UNKNOWN-KIND-01: Consumers() + Provider() 签名改为 `(T, error)`，unknown kind / not found 返回 typed error（`ErrContractNotFound` / `ErrValidationFailed`）。PR#142 | 1.5h → 0h |
| **快修合集** | #26(.env.example 补 `GOCELL_S3_REGION=us-east-1`, `.env.example`) + ✅ #27(contract CI PR#139) + F-7(BUILD-OUTDIR-01 统一 `go build -o bin/` 输出目录) + #17(Hook 增强 WM17-F2-2 ctx 超时 + WM17-F4-3 Prometheus metrics via HookObserver 接口, `kernel/cell/`) + #18(CB 接口+封装清理 CB-IFACE-01 Allow/Report 拆分 + CB-ENCAP-01 消除 gobreaker import, `runtime/resilience/circuitbreaker/`) + ~~#21(Journey 校验 F-5 catalog 不校验引用)~~ stale: REF-06+REF-07 已覆盖 journey cell/contract 引用校验 | 9h |
| **CI 供应链加固** | CI-DIGEST-01(testcontainers 镜像 tag+digest 双固定) + CI-LINT-PIN-01(golangci-lint 版本固定到 patch 级 + dependabot 自动升级) (discovered via PR#139 review P2-3) | 2h |
| **PG 域 Repository** | PG-DOMAIN-REPO: 5 个域 Repository 的 PostgreSQL 实现（User/Session/Role/Device/Command）。当前全部只有 `cells/*/internal/mem/` 内存实现，无持久化——重启后数据丢失。`adapters/postgres/` 已有 outbox_writer/tx_manager/migrator 基础设施可参考 | 3-5d |
| **系统拓扑自省** | SYSTEM-TOPOLOGY-API: `GET /internal/v1/system/topology` 返回 cell/slice/contract 拓扑 JSON。当前前端被迫用 js-yaml 直接读取后端 YAML 文件拼接拓扑图。可基于 `kernel/registry` 现有数据构建 | 4h |
| **PR#142 review defer** | R2-KERNEL-SLOG-01: generator.go/depcheck.go `_, _ :=` 忽略 Consumers()/Provider() error，kernel/ 无 slog 先例故未加日志。等 kernel/ 引入 slog 时补 `slog.Warn` (discovered via PR#142 6-seat review #2) + R4-ALLOWEDFILES-EMPTY-ID: `AllowedFiles()` 对空 ID 产出 `cells/x/slices//**`（双斜杠），parser 保证 ID 非空故不触发，等 `AllowedFiles()` 被治理规则消费时补 guard (discovered via PR#142 review #4) + R5-FMT15-PATH-ASSERT: FMT-15 测试 mock readFile 不验证 schemaPath 参数，`contractDirFromID` 有独立测试覆盖，低优先级 (discovered via PR#142 review #5) | 1.5h |

### 设计决策记录（PR#137 review 确认，不修）

> 以下 4 项在 PR#137 6 席位审查中提出，经根因分析后确认为设计正确，记录于此避免重复审查。

| # | Finding | 结论 | 理由 |
|---|---------|------|------|
| F1-2 | assembly 层不校验零值 + 非法枚举旁路 | ✅ PR#137 | assembly.startInternal 加 ValidateMode 入口闸门 + CheckNotNoop 改为 allowlist。原理由：分层设计：guard 属于 cell 层（有业务语义），BaseCell 不消费 DurabilityMode。全部 5 个 L2 cell 已覆盖 + `gocell scaffold` 模板保证新 cell。assembly_test 已有零值集成测试 |
| F1-3 | core-bundle DurabilityDurable + in-memory | 不修 | 语义正确：Durable 拒绝 Nooper 标记类型，nil（未注入）和 `eventbus.New()`（非 noop）合法通过。godoc 已在 PR#137 修正 |
| F3-1 | durability_test 非 table-driven | 不修 | 8 个测试断言模式差异大（有的查 NoError，有的查 errcode+Contains），table-driven 需参数化断言增加复杂度不增加覆盖率 |
| CI-DUP | SonarCloud 5.6% duplication | 不修 | cell-per-package 固有结构相似。5 个 cell 的 CheckNotNoop 参数列表各不相同（order 2 deps, device 1 dep, 其余 3 deps），不可提取 |

### 设计决策记录（PR#140 对标确认）

> 以下 2 项在 PR#140 实施前对标主流开源框架后确认为设计正确，记录于此避免重复审查。

| # | 主题 | 结论 | 对标来源 + 理由 |
|---|------|------|----------------|
| A | Request/Trace ID 生成不做 `rand.Read` 错误分支 | ✅ PR#140 | **chi** `middleware/request_id.go`: 同样不检查 `rand.Read` 返回值。**Kratos** `middleware/tracing/*.go`: 核心依赖 tracing 链路，不提供独立 request-id 生成失败模型。**OTel** `sdk/trace/id_generator.go`: ID 生成路径默认无 error 返回通道，强调链路可用性。Go 1.24+ `crypto/rand.Read` 已改为 always-succeed-or-fatal，`_, _ =` 是死代码 |
| B | JSON unknown field 用字符串匹配 + guard test | ✅ PR#140 | **Gin** `binding/json.go`: 开启 strict 后仍依赖底层错误文本语义。**Echo** `bind.go`: 默认宽松，strict 依赖自定义扩展；框架不提供统一结构化 unknown-field 类型。**go-zero** `rest/httpx/requests.go`: 默认不做 strict unknown-field 分类治理。Go 标准库 `encoding/json` 至 1.25 仍用 `fmt.Errorf("json: unknown field %q", key)`，无 typed error。单点字符串识别 + 守卫测试是稳妥且常见的工程化折中 |

### 设计决策记录（PR#143 review 确认，不修）

> 以下 4 项在 PR#143 6 席位审查中提出，经根因分析后确认为设计正确或当前阶段可接受，记录于此避免重复审查。

| # | Finding | 结论 | 理由 |
|---|---------|------|------|
| 4.1 | Seed admin password 通过环境变量传入 | 不修 | dev 模式标准做法（Casdoor/Zitadel 同模式）。bcrypt hash 后存储，不记录密码明文。生产环境应改用 secrets manager，但超出当前 scope，已在 Batch 8 PG-DOMAIN-REPO 时一并处理 |
| 1.2 | Slice 目录命名 `rbac-assign/` (YAML) vs `rbacassign/` (Go) | 不修 | 与项目既有惯例一致：`identity-manage/identitymanage`、`rbac-check/rbaccheck` 等。kebab-case YAML 目录 vs no-dash Go 包目录是 CLAUDE.md 约定 |
| 5.3 | `TestContext` 从非 `_test.go` 文件导出 | 不修 | 跨包测试需要（`cells/access-core/` 测试引用 `runtime/auth`）。`_test.go` 中的函数是 package-scoped 无法跨包调用。替代方案 `authtest` 子包增加维护负担但无实质收益 |
| 6.1 | POST /assign 返回 200 而非 201 | 不修 | 幂等操作返回 200（Casbin 模式：re-assignment is no-op）。contract.yaml 声明 `successStatus: 200` 一致。201 暗示每次创建新资源，不符合幂等语义 |

### 触发条件项（仅在条件满足时做）

| # | 任务 | 工时 | 触发条件 |
|---|------|------|----------|
| 28c | **AUTH-PROVIDER-EXPORT-01** `authProvider` 接口定义在 `runtime/bootstrap`（unexported），与 `HTTPRegistrar`/`EventRegistrar`（kernel/cell exported）不一致。因 kernel→runtime/auth 层依赖限制无法直接移动 | 1h | 第二个 auth provider cell |
| 28g | **AUTH-ISSUE-OPTIONS-01** `JWTIssuer.Issue()` 4 参数，重构为 `IssueOptions` struct (`runtime/auth/jwt.go`) | 1h | Issue() 第 5 个参数 |
| 28h | **DEVICE-ENQUEUE-RBAC** HandleEnqueue 无设备维度鉴权——当前设计为 operator 管理端点（任何已认证 operator 可向任意设备下发命令, `cells/device-cell/slices/device-command/handler.go`） | 2h | 多租户 operator |

---

## v1.1+ 长期规划

> **[详细内容请阅读: backlog_later_detail.md](./backlog_later_detail.md)**
>
> metadata 校验规则 (G-1~G-6) / Kernel 子模块 (wrapper/command/webhook/reconcile/scheduler/replay/rollback)
> adapters 分层重整 (AL-01~AL-04, RMQ-STATUS-01) / 架构风险 (Cell 接口拆分, adapter t.Skip, ER-ARCH-01)
> 契约增强: CONTRACT-BREAKING-01(`gocell check contract-breaking` schema 破坏性变更检测，学 buf breaking 40+ 规则) / CONTRACT-CODEGEN-01(Go DTO struct tags → JSON Schema 自动生成，学 oapi-codegen) / CONTRACT-STUB-01(消费方 contract stub 测试，学 Spring Cloud Contract WireMock 模式)
> spec tech-debt (C-AC7 jti / C-L6 contract ID / C-DC9 auditarchive stub / DURABLE-TYPE-01 / CONTRACT-META-01)
> winmdm defer (WM-18/32/4/5/22/23/16) / winmdm reject (WM-3/14/21/24/25/26/30/31/34b)
> v2+ (WM-28/29, GAP-1/2/5/6/8/11/12/13/14)

---

## Wave 1 执行顺序（7 批次，每批 3-4 项并行）

> ✅ A(PR#131) → ✅ B(PR#133) → ✅ C(PR#135+136+137) → ✅ D(PR#138) → EF
> 剩余: EF (SHOULD) + P1 穿插

### Batch A: ✅ 安全加固 — PR#131

### Batch B: ✅ DTO 对齐 + 配套测试 + HSTS — PR#133

### Batch C: ✅ L2 Gate + Strict Mode + Order Cell — PR#135 + PR#136 + PR#137

### Batch D: ✅ 契约完整性 — PR#138

### Batch EF: pkg 加固 + CI + 治理（6 项，~9.5h）

> PR: MG-EF。#28a 降级 Batch 8。

| 任务 | 工时 | 理由 |
|------|------|------|
| ✅ #20 decode 加固 + REQID-RAND-ERR + MAIN-TEST-CLEANUP | 2h | classifyDecodeError 安全 + rand.Read 清理 + main_test 类型安全 | PR#140 |
| ✅ #19 CI 增强 + #27-CI contract YAML 校验 | 2.5h | golangci-lint + testcontainers pin + contract CI | PR#139 |
| ✅ #27b SLICE-ALLOWEDFILES-01 + #138b CONTRACT-LIST-LINT-01 + REGISTRY-CONSUMERS | 6.5h | 元数据治理 + list lint + registry error | PR#142 |

### P1 正确性 + 运维（独立于上述 Batch，可穿插）

| 任务 | 工时 | 备注 |
|------|------|------|
| #5 AUTH-DX-01 README | 4h | 最后做（反映最终 API 状态）|
| #6 TPUB-01 TestPubSub | 4h | 无依赖，随时可做 |
| ✅ #10 Watcher 核心增强 | 7h | PR#132 |
| ✅ #11 Watcher 状态面 + 连接池指标 | 4h | PR#132 + PR#134 |

---

## 执行总览

```
已完成:
  Batch 1-5: ✅ PR#67-114 (48 PRs)
  Wave 1 已合入: PR#116-138 (23 PRs)
    Batch A: ✅ PR#131
    Batch B: ✅ PR#133
    Batch C: ✅ PR#135+136+137
    Batch D: ✅ PR#138
    #10+#11: ✅ PR#132+PR#134

Wave 1 MUST 路径: ✅ 全部完成（Batch A-D）
  Batch EF: ✅ PR#139+140+142
Wave 1 剩余:
  SHOULD (Batch EF):   ✅ 全部完成
  P1 (穿插):           ~8h — #5 README(4h) + #6 TestPubSub(4h)
  Batch 8 (v1.0 后):   ~66h — 不阻塞发布（含 PG-DOMAIN-REPO 3-5d + SYSTEM-TOPOLOGY-API 4h）
Post-Wave 3 新增:
  PR-H1 安全加固:       ~8h（含 ROLE-ASSIGN-API 2h + SEED-ADMIN 1h）
  PR-FEAT 功能补全:     ~8h（Device List 3h + Flag Write 3h + List Lint 2h）

关键路径: WM-2-F1 ✅ → WM-35 (2d) → WM-36 (1.5d) → H1+H2+FEAT (3d) → README (0.5d) → Review (2d) = 剩余 9 工作日
```

---

## Wave 3 后实施计划（2026-04-16 整理）

> 按 PR 级别组织。所有问题已验证代码现状。
> 前置: Wave 3 (WM-35 + WM-36) 完成后执行。

### PR-H1: 安全加固（P0+P1，~8h，v1.0 阻塞）

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| H1-1 | cmd/core-bundle | **PROD-KEY-FAILFAST** P0: `loadKeySet` dev 模式用 `MustGenerateTestKeyPair` 生成临时密钥，`validateAdapterMode` 已拒绝 `real` 但未拒绝空值——空值走 dev 分支。生产误配时密钥可预测、重启后 token 失效。需拆分 dev/prod 启动路径，生产缺密钥直接 fail-fast | Wave 3 | 2h | `cmd/core-bundle/main.go` | PR#137-138 集成审查 P0 |
| H1-2 | cells/access-core | **IDENTITY-AUTHZ-01** P1: identitymanage handler（lock/unlock/PATCH/DELETE）仅鉴权无授权，任何有效 token 可执行管理操作。需加 `RequireRole("admin")` 或 `RequireSelfOrRole` | Wave 3 | 1.5h | `cells/access-core/slices/identitymanage/handler.go` | PR#137-138 集成审查 P1 |
| H1-3 | kernel/cell | **DURABLE-NIL-GUARD** P1: `CheckNotNoop` 只拒绝 Nooper 标记类型，不拒绝 nil——DurabilityDurable 下 nil 依赖可旁路，L2 cell 仍可以 demo 语义运行。需在 L2 cell Init() 增加 durable+nil 必填校验 | Wave 3 | 1.5h | `kernel/cell/durability.go` + 5 个 `cell.go` | PR#137-138 集成审查 P1 |
| H1-4 | cells/access-core | **ROLE-ASSIGN-API** P1: `AssignToUser` 仅存在 repo 层接口（`ports/role_repo.go:13`）和 mem 实现（`mem/role_repo.go:71`），无 HTTP handler 暴露。新建 `rbacassign` slice：`POST /internal/v1/roles/assign` + `DELETE /internal/v1/roles/revoke`，仅 `RequireRole("admin")` 可调用。**必须与 H1-2 同 PR**——否则 H1-2 加了 `RequireRole("admin")` 后无法分配角色，形成死锁 | Wave 3 | 2h | `cells/access-core/slices/rbacassign/` + `contracts/http/auth/role/assign/v1/` | backend_issues.md #3 |
| H1-5 | cmd/core-bundle | **SEED-ADMIN** P1: 启动时检测 admin 角色不存在则自动创建 seed admin（用户名/密码来自环境变量 `GOCELL_ADMIN_USER` / `GOCELL_ADMIN_PASS`，无环境变量则跳过）。打破"先有鸡还是先有蛋"死锁，H1-2 + H1-4 的前提 | Wave 3 | 1h | `cmd/core-bundle/main.go` + `cells/access-core/internal/mem/` | backend_issues.md #3 |

### PR-H2: 契约补全（P1，~2h，v1.0 前建议）

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| H2-1 | contracts/config-core | **CONFIG-ROLLBACK-CONTRACT** P1: `cell.go:214` 注册了 `POST /{key}/rollback` 路由，有 handler_test 覆盖，但无 HTTP contract + schema + contract_test。路由暴露但契约体系无定义，变更无法被自动拦截 | PR-H1 | 1.5h | `contracts/http/config/rollback/v1/` + `cells/config-core/slices/configpublish/contract_test.go` + `slice.yaml` | PR#137-138 集成审查 P1 |
| H2-2 | contracts/config-core | **CONFIGPUBLISH-REDACT-01** P1: publish 响应 schema required `value` 明文字段，未复用 configwrite 的 `RedactedValue` 脱敏逻辑。和 H2-1 同改 configpublish | 无 | 0.5h | `cells/config-core/slices/configpublish/handler.go` + `contracts/http/config/publish/v1/response.schema.json` | PR#138 review + 集成审查 |
| H2-3 | contracts/access-core | **IDENTITY-PATCH-CONTRACT** P2: identitymanage PATCH 无 contract schema（#27s 遗漏），未知字段策略未在 schema 层声明。补 request/response schema + contract_test | 无 | 1h | `contracts/http/auth/identity/patch/v1/` + `cells/access-core/slices/identitymanage/contract_test.go` | PR#138 review P2 |

> H2-1 和 H2-2 同改 configpublish slice，一个 PR。

### PR-FEAT: 功能补全 + 治理（P1，~8h，v1.0 前建议）

> 来源: backend_issues.md #1 #2 前后端联调缺口 + #138b list lint 搭车。PR-H2 之后、PR-README 之前做，README 需反映最终 API。

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| FEAT-1 | cells/device-cell | **DEVICE-LIST-API** P1: 无 `GET /api/v1/devices` 列表端点，只有 `/{id}/status` 和 `/{id}/commands`。前端设备大盘无数据来源。新建 `device-list` slice + 分页查询 + contract + contract_test | PR-H2 | 3h | `cells/device-cell/slices/device-list/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| FEAT-2 | cells/config-core | **FLAG-WRITE-API** P1: config-core feature flag 仅有 GET + Evaluate，无写入能力。管理界面 feature flag 开关不可操作。新增 `PUT /api/v1/config/flags/{key}` 写入端点 + contract + contract_test | PR-H2 | 3h | `cells/config-core/slices/configwrite/` + `contracts/http/config/flags/write/v1/` | backend_issues.md #2 |
| FEAT-3 | kernel/governance | **CONTRACT-LIST-LINT-01** `gocell check contract-health` 增加 list 响应格式检查：response schema 含 `data: array` 时必须同时包含 `nextCursor` + `hasMore`（对齐 `api-versioning.md` 规定的 `{"data": [...], "nextCursor": "...", "hasMore": bool}` 格式）。与 FEAT-1 搭车——lint 规则上线时立即守护新 list 端点 | 无 | 2h | `kernel/governance/` | PR#138 review P1-3 + PR#141 review |

### PR-EF: pkg 加固 + CI + 治理（SHOULD，~8.5h，v1.0 前建议）

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| EF-1 | pkg/httputil + runtime/http | **#20 decode 加固 + REQID-RAND-ERR + HT-02 + MAIN-TEST-CLEANUP** | 无 | 4h | `pkg/httputil/decode.go` + `runtime/http/middleware/request_id.go` + `pkg/httputil/response_test.go` + `cmd/core-bundle/main_test.go` | 6B + 集成审查 |
| EF-2 | CI | **#19 CI 增强 + #27-CI contract YAML 校验** | 无 | 2.5h | `.github/ci.yml` + `adapters/*/integration_test.go` | 6B |
| EF-3 | kernel/cell | **#27b SLICE-ALLOWEDFILES-01** | 无 | 2h | `kernel/cell/base.go` + all `slice.yaml` | PR#119 review |

### PR-README: 文档收口（P1，~4h，Wave 4 前）

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| R-1 | docs | **#5 AUTH-DX-01** README + seed 用户 + sso-bff walkthrough | PR-H1（反映最终 API） | 4h | `README.md` + `examples/sso-bff/README.md` | 6B + P4 review |

### PR-TPUB: 独立穿插（P1，~4h，随时可做）

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| T-1 | kernel/outbox | **#6 TPUB-01** TestPubSub 真实 adapter 认证 | 无 | 4h | `kernel/outbox/outboxtest/` + `adapters/rabbitmq/` | 6B |

### 执行顺序

```
Wave 2 (WM-35 BFF handler, 2d)
     ↓
Wave 3 (WM-36 SecureCookie, 1.5d)
     ↓
  PR-H1 安全加固 (P0+P1, 含 ROLE-ASSIGN + SEED-ADMIN, 阻塞 v1.0)  ─┐
  PR-TPUB (#6, 独立并行)                                             ─┤ 并行
  PR-EF pkg+CI+治理 (SHOULD, 独立并行)                                ─┘
       ↓
  PR-H2 契约补全 (依赖 H1)
       ↓
  PR-FEAT 功能补全 (Device List + Flag Write, 依赖 H2)
       ↓
  PR-README #5 文档收口 (依赖 H1+H2+FEAT, 反映最终 API)
       ↓
  Wave 4 Review + v1.0 tag
       ↓
  Batch 8 (含 PG-DOMAIN-REPO + SYSTEM-TOPOLOGY-API)
```
