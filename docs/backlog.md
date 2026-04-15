# GoCell Backlog

> 只含待办事项。已完成项归档至 `docs/reviews/archive/`。
> 更新日期: 2026-04-15
> Batch 1-5: ✅ 全部完成 (PR#67-114, 48 PRs)
> Wave 1 进行中: ✅ PR#116-128 (13 PRs 已合入) + PR#132 (#10 Watcher 核心增强 + #11 observedGeneration)
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
| 11 | **Watcher 状态面 + 连接池指标** R97-F3(Generation/observedGeneration) ✅ PR#132 + OPS-5(PG/Redis/RMQ 连接池指标) 待做 + CFG-DRIFT-READYZ(HasDrift→/readyz checker: bootstrap 注册 `config.HasDrift` 为 health checker，partial-apply 失败时 generation≠observedGeneration → readyz 503；需产品决策: 硬失败 vs 仅 verbose 输出、瞬态漂移(reload 进行中)与持久漂移(cell 回调失败)的区分策略, discovered via PR#132 review) | 4h | `runtime/config/` + `adapters/postgres/` + `adapters/redis/` + `adapters/rabbitmq/` + `runtime/bootstrap/` | 6A |

### MUST: v1.0 前必须做（breaking change / 安全 / 阻塞 review）

| # | 任务 | 工时 | 文件 | 来源 | 合并 PR |
|---|------|------|------|------|---------|
| 25 | ✅ **HSTS 加固** C-H4: `security_headers.go` 补 `includeSubDomains; preload` | 0.5h | `runtime/http/middleware/security_headers.go` | P2 tech-debt | PR#131 |
| 28e | ✅ **AUTH-TTL-CONST-01** `accessTokenTTL` 3 处重复定义 → `auth.DefaultAccessTokenTTL` | 0.5h | `runtime/auth/jwt.go` | PR#127 review | PR#131 |
| 24 | ✅ **Trace trust policy** OBS-REQID-TRUST: `RequestIDWithOptions` + bootstrap 自动信任边界接线（TRUST-POLICY-01 已在 PR#128 完成） | 2h | `runtime/http/middleware/request_id.go` + `router.go` + `bootstrap.go` | 5B PR#112 review | PR#131 |
| 28d | ✅ **AUTH-REFRESH-VERIFIER-01** session-aware verifier 注入 refresh service，Init 重排序 | 2h | `cells/access-core/cell.go` | PR#127 review | PR#131 |
| 27v | ✅ **DOMAIN-EVENT-DTO-DECOUPLE** Order/Device/Command domain entities json tags 移除 + 事件 payload 显式 map + handler 裸 map → typed DTO | 1.5h | `cells/order-cell/internal/domain/order.go` + `cells/device-cell/internal/domain/device.go` + 3 handler + 2 service | PR#126 review | PR#133 |
| 27u | ✅ **DTO-COVERAGE-GAP** sessionlogin/sessionrefresh TokenPair + featureflag EvaluateResult 仍为 service 层类型直出，需补 handler 层 DTO | 2h | `cells/access-core/slices/session*/handler.go` + `cells/config-core/slices/featureflag/handler.go` | PR#126 review | PR#133 |
| 27l | ✅ **EVALUATE-RESULT-LAYER** featureflag `EvaluateResult` 定义在 service.go 而非 handler 层，轻微违反 DTO-in-handler 模式。功能等同 DTO（2 字段 + json tags），实际风险极低 | 0.5h | `cells/config-core/slices/featureflag/service.go` + `handler.go` | PR#126 review | PR#133 |
| 27c | **L2-HARD-GATE-01** L2 cell 启动门禁从 publisher 兜底升级为强制 outbox+txRunner（需配合 demo 模式显式开关 `WithDemoMode()`），消除声明能力与运行能力漂移 | 3h | `cells/access-core/cell.go` + `cells/config-core/cell.go` + `cells/audit-core/cell.go` | PR#119 review P1-1 | MG-C L2 加固 |
| 14 | **order+demo+examples 修复** P4-TD-04 + P4-TD-12 + EVT-HDR-RESTORE + WM-6-F8(demo 模式开关) + P3-DEFER-03(examples 新 API) + NOOP-RENAME-01 + NIL-PUB-P1(device-cell nil publisher) | 7.5h | `cells/order-cell/` + `cells/demo/` + `cells/device-cell/` + `examples/` | 6B | MG-C（部分） |
| 27s | **CONTRACT-SCHEMA-COVERAGE** 6 个端点有 DTO 但无 HTTP contract schema + contract_test（configwrite/configpublish/auditquery/rbaccheck/device-command/order-query） | 6h | `contracts/http/` + 6 个 slice `contract_test.go` | PR#126 review | MG-D 契约完整 |
| 27t | **CONTRACT-TEST-REAL-HANDLER** contract_test 使用硬编码 fixture 而非调用真实 handler，schema 符合性 ≠ handler 符合性 | 4h | `cells/*/slices/*/contract_test.go` | PR#126 review | MG-D |
| 27i | **CONTRACT-CLIENTS-01** 9 个 HTTP contract `endpoints.clients: []` 缺少消费方声明（http.auth.refresh、http.config.flags.*、http.order.*、http.device.*），需产品确认各端点的 BFF/edge 消费方后填入 | 1h | `contracts/http/*/contract.yaml`（9 文件） | PR#125 review | MG-D |

### SHOULD: v1.0 前建议做（改善质量，不阻塞）

| # | 任务 | 工时 | 文件 | 来源 | 合并 PR |
|---|------|------|------|------|---------|
| 131a | ✅ **REFRESH-ROLE-WARN** sessionrefresh role-fetch error 静默忽略，补 `slog.Warn` 对齐 sessionlogin 模式 | 0.5h | `cells/access-core/slices/sessionrefresh/service.go` | PR#131 review F2-3 | PR#131 |
| 131b | ✅ **BOOTSTRAP-TRUST-TEST-01** bootstrap 信任边界自动接线（`authPublicEndpoints` → tracing + request_id）无集成测试。现有 bootstrap 测试全部依赖 `net.Listen`，需 router-only 测试路径或 mock listener | 2h | `runtime/bootstrap/bootstrap_test.go` | PR#131 review F3-1 | PR#133 |
| 27n | ✅ **HANDLER-TEST-CAMELCASE-ASSERT** 13 个 handler_test.go 无显式 camelCase key 断言（如 `assert.Contains(body, "createdAt")`），camelCase 合规由 contract_test + schema 守护 | 2h | 12+ `handler_test.go` | PR#126 review | PR#133 |
| 27k | ✅ **DTO-CONVERTER-UNIT-TEST** 8 个 DTO converter 函数（toXxxResponse）无独立单测，仅靠 handler httptest 间接覆盖。若 converter 增加条件逻辑需补专项测试 | 2h | 6 个 `handler_test.go` | PR#126 review | PR#133 |
| 20 | **decode 加固** DECODE-STR-01 classifyDecodeError 脆弱性 | 2h | `pkg/httputil/decode.go` | 6B | MG-E |
| 19 | **CI 增强** T1-7(golangci-lint) + TC-PIN-01(testcontainers 镜像 pin 到 patch 版本，当前全仓用 floating minor tag `3.12-management-alpine`，PR#124 review S4-F1) | 2.5h | `.github/ci.yml` + `adapters/*/integration_test.go` | 6B | MG-F 治理 |
| 27b | **SLICE-ALLOWEDFILES-01** 全部 slice 默认 allowedFiles 不覆盖 Go 包目录（kebab-case YAML 目录 vs no-dash Go 包目录），需系统性补 allowedFiles 或改 `BaseSlice.AllowedFiles()` 默认逻辑 | 2h | `kernel/cell/base.go` + all `slice.yaml` | PR#119 review | MG-F |
| 28a | **AUTH-CACHE-01** session 验证 DB round-trip 缓存: 每请求 `GetByID` 查主库，real adapter 下需 Redis short-TTL（5-15s）session cache + 撤销时主动失效。可选: circuitbreaker 包住 `GetByID`（仅 infra error 触发） | 4h | `cells/access-core/slices/sessionvalidate/service.go` + `adapters/redis/` | PR#127 review | MG-G Auth ops |
| 28b | **AUTH-HEALTH-01** session DB 健康检查: 当前 `/readyz` 不含 session repo 状态，DB 宕机时 K8s 持续导流。需 session repo 实现 `Health()` + main.go 注册 `WithHealthChecker` | 2h | `cells/access-core/internal/mem/` + `cmd/core-bundle/main.go` | PR#127 review | MG-G |

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
| **order-cell 收口** | ORDER-DEMO-01(demo 模式产品行为决策) + NIL-PUB-P2（5 个 L2 service nil publisher 防护） | 2h |
| **Cursor 全家桶** | #15(cursor 回归矩阵 CURSOR-TEST-01 + CUR-HDL-01: 5 个分页入口补 malformed/missing-scope/cross-context 三类回归, `cells/*/handler_test.go` + `service_test.go`) + WM-6-F6(泛型 cursor helper)/F7(cursor 日志收口)/F1(prod guard) + TX-NIL-01(nil-safe 注释) + #27e(noopTx 提取 NOOP-TX-SHARED-01: 5 处重复 `noopTxRunner` 提取为 `kernel/persistence.NoopTxRunner`, `kernel/persistence/tx.go`) + #32(cursor 可观测 CURSOR-P2-02 invalid 结构化日志, `cells/audit-core/`) | 8.5h |
| **metadata parser** | META-67-01(strict unknown-field reject) + META-67-02(位置信息错误报告) + META-67-03(cross-file 引用校验) | 2.5h |
| **auth 增强** | WM-2-F2(HMAC replay 防护) + WM-2-F3(auth metrics) + AUTH-SIGNER-01(`SigningKeyProvider` 返回 `crypto.Signer` 替代 `*rsa.PrivateKey`，需自定义 jwt SigningMethod，前置: golang-jwt v6 或 wrapper) | 6h |
| **auth 测试 DX** | AUTH-SLOG-01(KeySet/servicetoken 注入 slog.Handler 替代全局 `slog.SetDefault`，消除并行测试风险) + AUTH-NOWFUNC-01(`var nowFunc` 包级状态改为实例字段注入) | 3h |
| **access-core 重构** | P3-TD-11 domain 模型拆分 User/Session/Role（前置: Wave 1 #13 Session TOCTOU ✅ PR#119） | 4h |
| **集成测试补全** | P4-TD-05(outbox 全链路) + RL-INT-01(Relay PG 集成) + P2-T-02(audit e2e) | 6h |
| **迁移+订阅** | RL-MIG-01(online-safe 索引 CONCURRENTLY) + RL-SUB-01(入站 ID 校验) | 3h |
| **CMD 重构** | CMD-MODE-01(fail-fast) + CMD-REFACTOR-01(app 包提取) | 3.5h |
| **批量操作** | WM-7 泛型 BulkResult | 1d |
| **Demo/日志规范化** | #27f(TEST-UNUSED-VAR-01 `cells/access-core/cell_test.go:33` `testPrivKey` 未使用) + #27g(DEMO-WARN-STRUCTURED-01 access-core/config-core Init() demo 模式 `logger.Warn` 缺结构化字段 `cell_id`/`consistency_level`, `cells/access-core/cell.go` + `cells/config-core/cell.go`) + #27h(DEMO-PUBLISH-WARN-01 demo 模式 publisher 失败日志 `slog.Error` → `slog.Warn`，剩余 4 处: `sessionlogout/service.go:115` + `sessionlogin/service.go:179` + `auditappend/service.go:130` + `auditverify/service.go:104`) + #28f(SSO-BFF-AUTH-SYNC-01 sso-bff 仍用 `WithAuthMiddleware(jwtVerifier)` + raw JWTVerifier → 需同步为 `WithPublicEndpoints` + AccessCore 发现模式, `examples/sso-bff/main.go`) | 3h |
| **Router 信任边界收敛** | RTR-PUBLIC-POLICY-01: router 把 public endpoint 策略分散为三套独立入口（`WithAuthMiddleware` L151 + `WithTracingOptions` L90 + `WithRequestIDOptions` L102），bootstrap 在 L584-596 临时收拢成统一 `isPublic`，但直接使用 router 的调用方（如 sso-bff、未来 edge cell）仍需手动保持三处一致。**处理方案**: ① Router 新增 `WithPublicEndpoints(paths []string)` 组合选项，内部构建 `isPublic` 并同时配置 auth bypass + tracing `WithPublicEndpointFn` + request_id `WithReqIDPublicEndpointFn`；② 保留现有细粒度选项（`WithTracingOptions`/`WithRequestIDOptions`）供需要不对称策略的场景，但标注 godoc `// Advanced: prefer WithPublicEndpoints for standard trust-boundary setup`；③ Bootstrap 的 L584-596 改为调用 `router.WithPublicEndpoints` 消除重复逻辑；④ 补测试: 验证 `router.WithPublicEndpoints` 同时生效 auth bypass + tracing new-root + request_id 拒继承。**根因**: PR#131 新增 request_id 信任边界时复制了 tracing 的配置面模式，但没有把 public policy 提升为一等抽象，导致三处配置漂移风险（F1 HSTS preload 越界即为同类"单处改动、多处影响"的实例）。**开源对比**: otelhttp 用单一 per-request public 判定函数；Kratos 组合层统一建模但不强绑所有中间件；go-zero 路由组层集中表达 auth。结论: 集中策略源是共识，但"一个开关绑定一切"需项目内取舍 | 3h |
| **Config 治理** | CFG-KEYFILTER-WIRE-01(KeyFilter bootstrap 接线: cell 通知循环使用 `KeyFilter.Matches()` 选择性通知，需产品确认语义, `runtime/bootstrap/bootstrap.go`) + CFG-ERRCODE-01(runtime/config 包 `fmt.Errorf` 评估是否迁移 errcode — 当前 runtime/ 层统一用 `fmt.Errorf`，仅在 config 错误需面向用户输出时迁移, `runtime/config/watcher.go` + `config.go`) (discovered via PR#132 6-seat review) | 2h |
| **PR#133 review C3** | F1-ARCH-03(RTR-HSTS-WIRING-TEST router 层 `WithSecurityHeadersOptions` 接线测试, `runtime/http/router/router_test.go`) + F2-SEC-03(TRUST-TRACEPARENT-TEST bootstrap 信任边界测试补 `traceparent` 注入向量, 需 `WithTracer` 设置, `runtime/bootstrap/bootstrap_test.go`) + F3-TEST-01(CONVERTER-NIL-INPUT converter 函数 nil 指针输入测试/文档, 各 `handler_test.go`) + F4-OPS-01(BOOTSTRAP-SECHDR-CONVENIENCE `bootstrap.WithSecurityHeadersOptions` 便利包装, `runtime/bootstrap/bootstrap.go`) (discovered via PR#133 6-seat review) | 3h |
| **快修合集** | #26(.env.example 补 `GOCELL_S3_REGION=us-east-1`, `.env.example`) + #27(contract CI: order-cell/device-cell contract YAML CI 未校验, `.github/workflows/ci.yml`) + F-7(BUILD-OUTDIR-01 统一 `go build -o bin/` 输出目录) + #17(Hook 增强 WM17-F2-2 ctx 超时 + WM17-F4-3 Prometheus metrics via HookObserver 接口, `kernel/cell/`) + #18(CB 接口+封装清理 CB-IFACE-01 Allow/Report 拆分 + CB-ENCAP-01 消除 gobreaker import, `runtime/resilience/circuitbreaker/`) + #21(Journey 校验 F-5 catalog 不校验引用, `kernel/journey/catalog.go`) | 9h |

### 触发条件项（仅在条件满足时做）

| # | 任务 | 工时 | 触发条件 |
|---|------|------|----------|
| 28c | **AUTH-PROVIDER-EXPORT-01** `authProvider` 接口定义在 `runtime/bootstrap`（unexported），与 `HTTPRegistrar`/`EventRegistrar`（kernel/cell exported）不一致。因 kernel→runtime/auth 层依赖限制无法直接移动 | 1h | 第二个 auth provider cell |
| 28g | **AUTH-ISSUE-OPTIONS-01** `JWTIssuer.Issue()` 4 参数，重构为 `IssueOptions` struct (`runtime/auth/jwt.go`) | 1h | Issue() 第 5 个参数 |
| 28h | **DEVICE-ENQUEUE-RBAC** HandleEnqueue 无设备维度鉴权——当前设计为 operator 管理端点（任何已认证 operator 可向任意设备下发命令, `cells/device-cell/slices/device-command/handler.go`） | 2h | 多租户 operator |

---

## v1.1+ 长期规划

> metadata 校验规则 (G-1~G-6) / Kernel 子模块 (wrapper/command/webhook/reconcile/scheduler/replay/rollback)
> adapters 分层重整 (AL-01~AL-04, RMQ-STATUS-01) / 架构风险 (Cell 接口拆分, adapter t.Skip, ER-ARCH-01)
> spec tech-debt (C-AC7 jti / C-L6 contract ID / C-DC9 auditarchive stub / DURABLE-TYPE-01 / CONTRACT-META-01)
> winmdm defer (WM-18/32/4/5/22/23/16) / winmdm reject (WM-3/14/21/24/25/26/30/31/34b)
> v2+ (WM-28/29, GAP-1/2/5/6/8/11/12/13/14)

---

## Wave 1 执行顺序（7 批次，每批 3-4 项并行）

> MUST 路径: Batch A-D = ~31.5h（约 4 工作日），v1.0 最低门禁。
> SHOULD 路径: Batch E-G = ~16.5h（约 2 工作日），提升 review 通过率。
> 依赖图:
> ```
> A(安全) → B(DTO, 依赖 A 的 TTL 统一) → C(L2 gate + order) → D(契约)
>                                                                  ↓
>                                        E(测试, 依赖 B 的 DTO 变更) → F(CI) → G(Auth ops)
> ```

### Batch A: 安全加固（4 项，~7h，A1-A3 并行）

> PR: MG-A 安全加固

| 任务 | 工时 | 为什么先做 |
|------|------|-----------|
| #25 HSTS 加固 | 0.5h | 1 行安全修复，零风险 |
| #28e AUTH-TTL-CONST 统一 | 0.5h | TTL 不一致是安全隐患，后续 Batch 依赖 |
| #24 Trace trust policy | 4h | 外部 trace 注入，信任边界修复 |
| #28d AUTH-REFRESH-VERIFIER | 2h | 验证链不对称，依赖 #28e 完成 |

### Batch B: Domain/DTO 对齐（3 项，~6.5h，全并行）

> PR: MG-B DTO 对齐。依赖 Batch A（TTL 统一后 service 文件稳定）。

| 任务 | 工时 | 为什么第二做 |
|------|------|-------------|
| #27v DOMAIN-JSON-TAG-REMOVAL | 4h | v1.0 后改 = breaking change，改 order/device domain |
| #27u DTO-COVERAGE-GAP | 2h | TokenPair + EvaluateResult 补 DTO |
| #27l EVALUATE-RESULT-LAYER | 0.5h | 随 #27u 一起，featureflag 层修正 |

### Batch C: L2 Gate + Order Cell（3 项，~10.5h，C1 先行）

> PR: MG-C L2 加固。依赖 Batch B（domain json tags 清理后 order-cell 才稳定）。

| 任务 | 工时 | 依赖 |
|------|------|------|
| #27c L2-HARD-GATE-01 | 3h | 先改 Init 门禁 |
| #14 order+demo+examples 修复 | 7.5h | 在新门禁下修复 order-cell + demo + examples |

### Batch D: 契约完整性（3 项，~11h，全并行）

> PR: MG-D 契约完整。依赖 Batch B+C（DTO 和 handler 稳定后才写 schema）。

| 任务 | 工时 | 为什么这个顺序 |
|------|------|---------------|
| #27s CONTRACT-SCHEMA-COVERAGE | 6h | 6 端点补 contract schema |
| #27t CONTRACT-TEST-REAL-HANDLER | 4h | contract_test 改用真实 handler |
| #27i CONTRACT-CLIENTS-01 | 1h | 9 个 contract 补 clients 声明 |

### Batch E: 测试质量（3 项，~6h，全并行）

> PR: MG-E 测试质量。依赖 Batch B（DTO 变更后测试才有意义）。

| 任务 | 工时 | 理由 |
|------|------|------|
| #27n HANDLER-TEST-CAMELCASE-ASSERT | 2h | 断言 camelCase key |
| #27k DTO-CONVERTER-UNIT-TEST | 2h | 8 个 converter 补单测 |
| #20 decode 加固 | 2h | classifyDecodeError 安全 |

### Batch F: CI + 治理（2 项，~4.5h，全并行）

> PR: MG-F 治理。独立于前序 Batch，但建议在代码改动收口后再跑 lint。

| 任务 | 工时 | 理由 |
|------|------|------|
| #19 CI 增强 golangci-lint | 2.5h | CI 基础设施 |
| #27b SLICE-ALLOWEDFILES-01 | 2h | 元数据治理 |

### Batch G: Auth 运维（2 项，~6h，全并行）

> PR: MG-G Auth ops。SHOULD 优先级最低，v1.0 前可选。

| 任务 | 工时 | 理由 |
|------|------|------|
| #28a AUTH-CACHE-01 Redis 缓存 | 4h | session 验证性能 |
| #28b AUTH-HEALTH-01 健康检查 | 2h | session DB 运维可见性 |

### P1 正确性 + 运维（独立于上述 Batch，可穿插）

| 任务 | 工时 | 备注 |
|------|------|------|
| #5 AUTH-DX-01 README | 4h | 最后做（反映最终 API 状态）|
| #6 TPUB-01 TestPubSub | 4h | 无依赖，随时可做 |
| ✅ #10 Watcher 核心增强 | 7h | PR#132 |
| #11 Watcher 状态面 (OPS-5 待做) | 2h | observedGeneration ✅ PR#132，剩余 OPS-5 连接池指标 |

---

## 执行总览

```
已完成:
  Batch 1-5: ✅ PR#67-114 (48 PRs)
  Wave 1 已合入: PR#116-128 (13 PRs) + PR#132 (#10+#11 partial)

Wave 1 剩余:
  MUST (Batch A-D):    ~35h / 4-5 工作日 — v1.0 最低门禁
  SHOULD (Batch E-G):  ~16.5h / 2 工作日 — 提升 review 通过率
  P1+运维 (穿插):      ~8h — #5 README(4h) + #6 TestPubSub(4h) + #11 OPS-5(2h，observedGeneration 已完成)
  Batch 8 (v1.0 后):   ~59h — 不阻塞发布（含新增 Config 治理 2h）

关键路径: WM-2-F1 ✅ → WM-35 (2d) → WM-36 (1.5d) → Review (2d) = 剩余 5.5 工作日
```

---

## 北极星校准（2026-04-15 审查）

> 基于 `.claude/rules/gocell/cell-patterns.md` 和 `runtime-api.md` 对现有任务方向的修正。
> 规则：序列化边界用 typed struct；Init() fail-fast；contract test 走真实 handler；auth 用 WithPublicEndpoints。

### 任务描述修正

| 任务 | 原描述 | 修正 | 工时变化 |
|------|--------|------|---------|
| **#27v** | DOMAIN-JSON-TAG-REMOVAL: 需设计 event payload DTO 层解耦后才能移除 | → **DOMAIN-EVENT-DTO**: order-create/device-register 2 处 `json.Marshal(entity)` 改为 typed event struct（`OrderCreatedEvent`/`DeviceRegisteredEvent`），然后移除 domain json tags。参照 cell-patterns 规则 1 | 4h → **1.5h** |
| **#27u** | DTO-COVERAGE-GAP: session/featureflag 补 handler 层 DTO | → 扩大范围：同时补 order-create 和 device-register handler 的裸 `map[string]any` → typed DTO + converter。合入 #27l（featureflag EvaluateResult 移到 handler 层是同一工作）| 2h+0.5h → **3h** |
| **#27l** | EVALUATE-RESULT-LAYER: 单独任务 | → **合入 #27u**，不再单列 | 删除 |
| **#27c** | L2-HARD-GATE-01: 需配合 demo 模式显式开关 `WithDemoMode()` | → 删除 WithDemoMode()。方向：对齐 order-cell Init() fail-fast 模式，L2 cell 强制要求 outboxWriter+txRunner，demo 模式传 `DiscardPublisher{}`。参照 cell-patterns 规则 2 | 3h 不变 |
| **#14** | order+demo+examples: 7 个子项 7.5h | → 清理已修/无 trace 子项（P4-TD-04/P4-TD-12/EVT-HDR-RESTORE/NOOP-RENAME）。剩余：WM-6-F8 demo 模式开关 + NIL-PUB-P1（方向：Init() fail-fast 对齐 order-cell，非 service 层 nil check）+ P3-DEFER-03 待验证 | 7.5h → **3h** |
| **#27t** | CONTRACT-TEST-REAL-HANDLER: 全部 contract_test 改真实 handler，4h | → 多数已用真实 handler（device-register/order-create/order-query/sessionlogout）。剩余 sessionlogin/configread 等 3-4 个需迁移。事件侧同步补 recordingPublisher 捕获验证。参照 cell-patterns 规则 3 | 4h → **1.5h** |
| **#27k** | DTO-CONVERTER-UNIT-TEST: 8 个 converter 无单测 | → 7 个 converter，2 个已有直接测试（toCommandResponse/toAuditEntryResponse），剩 5 个需补 | 2h → **1h** |
| **#27n** | HANDLER-TEST-CAMELCASE-ASSERT: 13 个无断言 | → 15 个 handler_test，7 个已有 camelCase 断言，剩 8 个需补 | 2h → **1h** |

### 优先级调整

| 任务 | 原位置 | 调整 | 原因 |
|------|--------|------|------|
| **#28a** AUTH-CACHE-01 | SHOULD / Batch G | → **Batch 8**，`blocked by: real DB adapter` | 当前只有 in-memory repo，无真实 DB 可缓存 |
| **#28b** AUTH-HEALTH-01 | SHOULD / Batch G | → **Batch 8**，`blocked by: real DB adapter` | 同上，in-memory store 的 Health() 永远 healthy |
| **#131b** BOOTSTRAP-TRUST-TEST | SHOULD / Batch E | → **Batch B**，PR#131 review 升级为 P1 | bootstrap 生产装配路径无验收测试 |
| **F1** HSTS-PRELOAD-ROLLBACK | 未登记 | → **Batch B**，搭车 0.3h | PR#131 引入的 preload 越界，收回到 includeSubDomains |
| **#27k** | Batch E | → **Batch B** | Batch B 新增 converter，应同步补测试 |
| **#27n** | Batch E | → **Batch B** | Batch B 改 handler 响应，应同步断言 camelCase |

### Batch 8 补充

| 任务 | 修正 |
|------|------|
| **NIL-PUB-P2** | 明确方向：Init() fail-fast 对齐 order-cell，非 service 层 nil check |
| **HR-03** | 废弃。PR#131 在 request_id.go 新增了信任边界（WithReqIDPublicEndpointFn），chi/middleware.RequestID 无此能力，实施即安全回退 |
