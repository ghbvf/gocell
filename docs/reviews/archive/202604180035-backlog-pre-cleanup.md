# GoCell Backlog

> 只含待办事项。已完成项归档至 `docs/reviews/archive/`。
> 更新日期: 2026-04-18
> Batch 1-5: ✅ 全部完成 (PR#67-114, 48 PRs)
> Wave 1: ✅ 全部完成 (PR#116-142+146, Batch A-EF 全部合入)
> Post-Wave 1: PR#141+143-148+150-161 已合入（TPUB+RMQ+RBAC closure+outbox fixes+kernel governance+rabbitmq conformance isolation+access-core 安全加固+validator 诊断定位+CI 并行化+hook 生命周期+config rollback 契约+OBS-B test determinism+OBS-A provider-neutral metrics+router 信任边界收敛+auth slog/clock/nonce/metrics DI+cursor 泛型 helper+CI flake 收口）
> PR#158 ✅ PR-R-ROUTER: WithPublicEndpoints 收敛 + PR#133 C3 closure + ConfigKeyFilterer + DTO nil guard（RTR-PUBLIC-POLICY-01 + F1-ARCH-03 + F2-SEC-03 + F3-TEST-01 + F4-OPS-01 + CFG-KEYFILTER-WIRE-01 + CFG-ERRCODE-01）
> PR#159 ✅ PR-R-AUTH: slog/clock/nonce/metrics 全量 DI + HMAC replay + auth metrics（AUTH-SLOG-01 + AUTH-NOWFUNC-01 + WM-2-F2 + WM-2-F3 + 5 项搭车 review followup）
> PR#160 ✅ PR-P-CURSOR: 泛型 ExecutePagedQuery[T] + LogCursorError + DemoMode + 5 入口 3-scenario 回归 + TX-NIL-01（WM-6-F1/F6/F7 + CURSOR-TEST-01 + CUR-HDL-01）
> PR#161 ✅ CI flake 收口: rabbitmq 测试确定性 + hook duration 测量修复 + gocognit 15（RMQ-TIMING-FLAKE-01 + HOOK-DURATION-MEASURE-01 + GOCOGNIT-15-TIGHTEN）
> PR#162 ✅ AUTH-LEGACY-TOKEN-REMOVE-01: servicetoken 2-part 分支下线
> PR#163 ✅ circuitbreaker 接口隔离: CB-TYPED-NIL-GUARD-01 + WithCircuitBreaker fail-fast
> PR#164 ✅ cmd/gocell CLI foundation
> PR#165 🔄 PR-X1 DemoMode 显式化 + fail-closed 统一（review round 2 已修: F1 .env.example mode 漂移 + F2 wiring 级回归测试 + F3 durable 模式 nil cursorCodec fail-fast + ErrCellMissingCodec 新增）
> PR#160 follow-up: ⏳ PR-X2 pkg/query 稳定性 / ⏳ PR-X3 cursor key rotation 接线
> PR#154 ✅ kernel hook 生命周期超时 + HookObserver + outbox metadata DX（WM17-F2-2/F4-3 + OBS-DX-01 + OBS-DOC-01）
> PR#155 ✅ config-core rollback 契约 + publish redaction（H2-1 + H2-2 + AUTHZ-WRITE-CONFIG-01 + ERROR-MSG-SCRUB-01 + ROLLBACK-NEGPATH-TEST-01 + SCHEMA-SENSITIVE-DESC-01）
> PR#156 ✅ PR-R-OBS-B: test determinism + cursor log（CONFORMANCE-SLEEP-01 + OBS-TABLE-01 + CURSOR-P2-02）
> PR#157 ✅ PR-R-OBS-A: provider-neutral metrics + async hook dispatcher + pool statter（OBS-METRIC-01 + OTEL-COV-01 + HOOK-OBSERVER-ASYNC-01 + OBS-LEAK-02 + OBS-POOLSTATS-WAITCOUNT-01 + OBS-HOOK-DISPATCHER-CFG-01）
> PR#149 已被 PR#151 取代（access-core 安全加固 + READYZ，已合入）
> PR#129 (Sentinel DSN redaction) / PR#130 (Bolt journey catalog) — 外部 PR，未合入
> 旧版备份: `docs/reviews/archive/20260415-backlog-pre-pr127-cleanup.md`

---

## Wave 1: ✅ 全部完成

> 已合入 PR#116-142+146。DEFER 项已移至 Batch 8。

### P1 正确性

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| 5 | **AUTH-DX-01** README + seed 用户 + sso-bff walkthrough: auth 已拦截全部业务路由，README 失效；sso-bff README 缺 refresh/GET user/event 消费 demo (P4-P1-6)。具体漂移: refresh curl 发 `sessionId` 实际需 `refreshToken`；logout 204 空 body 管道 jq 失败；audit jq 用 `.createdAt` 实为 `.Timestamp` | 4h | `README.md` + `cells/access-core/internal/mem/` + `examples/sso-bff/README.md` | 6B + P4 review |
| 6 | ✅ **TPUB-01** TestPubSub 真实 adapter 认证: conformance harness 替换 sleep + 接入 RabbitMQ adapter | 4h | `kernel/outbox/outboxtest/` + `adapters/rabbitmq/` | 6B | PR#141 |

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
| 31 | ✅ **RabbitMQ 代码清理** backoff + FailOpen enum → ClaimPolicy typed enum | RMQ ✅ | 3h | PR#141 |
| 32 | ✅ **cursor 可观测** invalid 结构化日志（CURSOR-P2-02）| cursor (#15 B8) | 1h | PR#156 |
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
| 38a | **ADR-RUNMODE-TRANSLATION-01** (Cx1, P2): 记录 `kernel/cell.DurabilityMode → pkg/query.RunMode` 的分层翻译模式 — pkg 不 import kernel，cell 构造期负责翻译；对标 zeromicro/go-zero `ServiceConf.Mode` 默认严格原则。discovered via PR#165 reviewer F1-1 (2026-04-17) | 1h |
| 39 | 性能基准 | 4h |
| 40 | **v1.0 tag** | — |

---

## 关键路径

```
★ Auth 链: WM-2-F1 ✅ → WM-35 (2d) → WM-36 (1.5d) → H1(0.5d) → H2(0.5d) → FEAT(1d) → README(0.5d) → Review(2d) = 剩余 8 工作日
```

---

## Batch 8: P2 偿债（v1.0 后）

> 从 Wave 1 下沉的 DEFER 项 + 原 Batch 8 项，按 PR 组合并。
> 前置: v1.0 tag 发布后。不阻塞发布。

| PR 组 | 任务 | 工时 |
|-------|------|------|
| **OBS 全家桶** | ~~META-SIZE-01~~ ✅ PR#147 + ✅ OBS-TABLE-01 PR#156(kernel/metadata parser_test.go 合并 7 个 invalid-YAML 测试为 table-driven，删除 5 个被 EmptyStructFiles 覆盖的 EmptyID 测试) + ✅ OBS-METRIC-01 PR#157(provider-neutral metrics.Provider + prom/otel adapter + HTTP/Relay collector 迁移 + bootstrap.WithMetricsProvider + default-assembly wiring) + ✅ OBS-DX-01 PR#154(CloneMetadata 导出 + godoc) + ✅ OBS-DOC-01 PR#154(ExampleIsReservedMetadataKey) + ✅ #23 OTEL-COV-01 PR#157(mock OTLP via ManualReader + tracetest.InMemoryExporter, 覆盖 metrics + trace) + ✅ CONFORMANCE-SLEEP-01 PR#156(conformance.go 5 处 time.Sleep+count → harness.assertNoMoreDeliveries pure select，对齐 Watermill; buffer=100 non-blocking send 避免干扰被测行为) + ✅ HOOK-OBSERVER-ASYNC-01 PR#157(kernel/assembly/hook_dispatcher.go: 异步有界队列 + per-sink timeout + drop counter via Provider + goleak + failed-start cleanup via bootstrap Shutdown teardown) | 7h → 0h |
| **OBS 全家桶 follow-up** | ✅ OBS-LEAK-02 PR#157(`newTestAssembly(t, cfg)` helper + 51 sites 迁移，移除 goleak allowlist) + ✅ OBS-POOLSTATS-WAITCOUNT-01 PR#157(`db.client.connection.timeouts` ObservableCounter) + OBS-RELAY-REGISTER-ATOMIC-01(Cx3, P2): `outbox.NewProviderRelayCollector` 5 个 metric 顺序 register，3 失败则前 2 半注册；需 Provider.Unregister 支持或文档化契约 (discovered via PR#157 review S3-05) + ✅ OBS-HOOK-DISPATCHER-CFG-01 PR#157(`dispatcherConfig{}` 替代位置参数) + OBS-HTTP-COLLECTOR-AUTOWIRE-01(Cx3, P2): bootstrap.WithMetricsProvider 不自动为默认 HTTP 中间件构造 `NewProviderCollector`；需设计 WithHTTPCollectorCellID 或 auto-wire (discovered via PR#157 review S4-01) + OBS-LGTM-INTEGRATION-01(Cx3, P2): 添加 `//go:build integration` tag 的夜间 OTel collector 真实 OTLP 协议兼容性测试（grafana/otel-lgtm 或 otel-collector-contrib）; 防止 OTLP protobuf 版本不兼容 (discovered via PR#157 review S6-04) | 7h → 3h |
| **Outbox 治理** | ~~OUTBOX-GUARD-01~~ ✅ PR#147 + ~~DISCARD-OBS-01~~ ✅ PR#148 + ~~OUTBOX-RECEIPT-01~~ ✅ PR#148 | 4h → 0h |
| **order-cell 收口** | ✅ ORDER-DEMO-01 PR#136(统一 outbox 路径) + ✅ NIL-PUB-P2 PR#136(Init() fail-fast) + ✅ CheckNotNoop PR#137 | 2h → 0h |
| **Cursor 全家桶** | ✅ #15 CURSOR-TEST-01 + CUR-HDL-01 PR#160(5 个分页入口 3-scenario 回归：garbage/missing-scope/cross-context) + ✅ WM-6-F6 PR#160(泛型 `ExecutePagedQuery[T]` + PagedQueryConfig) + ✅ WM-6-F7 PR#160(`LogCursorError` factory，5 服务结构化 slice/reason/error/request_id) + ✅ WM-6-F1 PR#160(`IsDemoKey` + `KnownDemoKeys` + `DemoMode`，demo 模式 stale cursor 返回首页而非 400) + ✅ TX-NIL-01 PR#160(7 service runInTx/runPersist nil-safe godoc) + ✅ #27e(NOOP-TX-SHARED-01 PR#136: `kernel/persistence.NoopTxRunner`) + ✅ #32 CURSOR-P2-02 PR#156(auditquery cursor decode/scope 失败加 Info 结构化日志，字段 slice/reason/error；对齐 k8s/etcd/MinIO 不记录原串) | 8.5h → 0h |
| **metadata parser** | ✅ META-67-01 PR#142 (parser.go KnownFields(true)) + ✅ META-67-03 PR#142 (governance/rules_ref.go REF-01..REF-16) + ✅ META-67-02 PR#152 (two-phase decode + Position{Line,Column} + governance/locator.go + printResult :line:col) + **METADATA-PERF-BENCH-01** (Cx3, P1): `BenchmarkParseFS_500Files` 性能基准 + goccy/go-yaml 单次解码迁移成本评估，对比当前 yaml.v3 双解码（每文件 2 pass）。前置: 构造 500+ MapFS fixture (discovered via PR#152 seat-4) | 2.5h → 4h |
| **metadata API 收敛** | METADATA-PROJECTLOC-IFACE-01 (Cx3, P2): `ProjectMeta.FileNodes map[string]*yaml.Node` 把 yaml.v3 AST 泄漏到 kernel/governance + cmd。应提取 `ProjectLocator interface { Locate(file, path string) Position }` 或 `pm.Locate(file, path) Position` 方法，隐藏 AST 细节。涉及 kernel/metadata + kernel/governance + cmd/gocell (discovered via PR#152 seat-1) | 3h |
| **validator 机器输出** | OUTPUT-JSON-SARIF-01 (Cx3, P1): `gocell validate` 缺机器可读输出通道（JSON / SARIF），当前文本 `file:line:col -> field` 格式虽人类友好但不承诺脚本稳定。需统一诊断模型（单一 Issue struct → 多 printer 映射），文本格式声明为非稳定。对标：golangci-lint / staticcheck / ESLint / kubectl print flags / test2json。涉及 cmd/gocell + kernel/governance 序列化 (discovered via PR#152 round-2 review) | 6h |
| **auth 增强** | ✅ WM-2-F2 PR#159(HMAC replay 防护: NonceStore 接口 + InMemoryNonceStore + 3-part token format) + ✅ WM-2-F3 PR#159(auth metrics: AuthMetrics via metrics.Provider + bootstrap auto-wire) + AUTH-SIGNER-01(`SigningKeyProvider` 返回 `crypto.Signer` 替代 `*rsa.PrivateKey`，需自定义 jwt SigningMethod，前置: golang-jwt v6 或 wrapper) + RBAC-LAST-ADMIN-GUARD(service.Revoke 检查剩余 admin 数量，需 `CountByRole` 加入 RoleRepository 接口, `cells/access-core/slices/rbacassign/service.go`) (discovered via PR#143 review 2.3) + ✅ **AUTH-SVCTOKEN-QUERY-CANONICALIZE-01** PR#159: ServiceToken 签名绑定 canonical query（url.Values.Encode 排序 key）。对标 AWS SigV4。(discovered via PR#159 外部审查) + ✅ **AUTH-NONCE-ERROR-LAYER-01** PR#159: ErrNonceReused→401，其他 NonceStore 错误→500 + slog.Error + 独立 metrics reason `nonce_store_error`。(discovered via PR#159 外部审查) + ✅ **AUTH-NONCE-MAXAGE-GUARD-01** PR#159: NewInMemoryNonceStore 返回 error，maxAge≤0 fail-fast。(discovered via PR#159 外部审查) + ✅ **AUTH-CLOCK-NIL-GUARD-01** PR#159: WithVerifierClock/WithIssuerClock/WithServiceTokenClock 忽略 nil fn。(discovered via PR#159 外部审查) + ✅ **AUTH-DURATION-MISSING-TOKEN-01** PR#159: 缺 token 路径仅 recordTokenVerifyCounter，不写 0s duration。(discovered via PR#159 外部审查) + **AUTH-LEGACY-TOKEN-STRICT-01** (P2, Cx2): 兼容 2-part token 缺下线治理。增 strict 模式（拒绝 2-part）+ 淘汰计划 + legacy 占比 metrics 看板。当前已有 warn log，strict 模式待产品确认迁移窗口后实施。(discovered via PR#159 外部审查) | 7h → 1h |
| **rbac-assign 治理** | RBAC-REVOKE-POST-01: `DELETE /internal/v1/access/roles/revoke` 改为 `POST` 避免 DELETE body 代理兼容问题（`cells/access-core/slices/rbacassign/handler.go` + `contracts/http/auth/role/revoke/v1/contract.yaml`）(discovered via PR#143 review 6.2) | 1h |
| **Internal 信任边界** | INTERNAL-LISTENER-01: `/internal/v1/` 路由与公网 API 共用 listener + Bearer JWT 鉴权链，信任边界仅靠路径前缀。应为 internal 路由建独立 listener 或 service-token/mTLS 策略。涉及 `runtime/bootstrap/bootstrap.go` + cell 路由注册拆分 (discovered via PR#143 review F1, Cx4) | 4-8h |
| **Seed 接口抽象** | SEED-ROLE-IFACE-01: `doSeedAdmin` 依赖 `*mem.RoleRepository` type assertion 调用 `SeedRole()`。应在 `ports.RoleRepository` 新增 `SeedRole(ctx, *Role) error` 方法，或提取独立 `bootstrap.Seeder` 接口。前置: PG-DOMAIN-REPO (discovered via PR#143 review F2, Cx3) | 2h |
| **auth 测试 DX** | ✅ AUTH-SLOG-01 PR#159(KeySet/servicetoken/middleware/authz/provider 全量 slog.Logger DI，context-based logger 保持 17 cells/ 调用点不变) + ✅ AUTH-NOWFUNC-01 PR#159(`var nowFunc` 删除，JWTIssuer/JWTVerifier/ServiceTokenMiddleware 全部实例级 clock DI，golang-jwt v5 WithTimeFunc 集成) | 3h → 0h |
| **access-core 重构** | P3-TD-11 domain 模型拆分 User/Session/Role（前置: Wave 1 #13 Session TOCTOU ✅ PR#119） | 4h |
| **集成测试补全** | P4-TD-05(outbox 全链路) + RL-INT-01(Relay PG 集成) + P2-T-02(audit e2e) | 6h |
| **迁移+订阅** | RL-MIG-01(online-safe 索引 CONCURRENTLY) + RL-SUB-01(入站 ID 校验) | 3h |
| **CMD 重构** | CMD-MODE-01(fail-fast) + CMD-REFACTOR-01(app 包提取) | 3.5h |
| **批量操作** | WM-7 泛型 BulkResult | 1d |
| **Demo/日志规范化** | ✅ #27f(TEST-UNUSED-VAR-01) + ✅ #27g(DEMO-WARN-STRUCTURED-01) + ✅ #27h(DEMO-PUBLISH-WARN-01) + ✅ #28f(SSO-BFF-AUTH-SYNC-01) — 全部在 PR#135 搭车完成 | 3h → 0h |
| **Router 信任边界收敛** | ✅ RTR-PUBLIC-POLICY-01 PR#158: `router.WithPublicEndpoints(paths)` 单点信任边界配置（auth bypass + tracing new-root + request_id reject）。Bootstrap 简化为 1 行调用。+ ✅ 冲突治理 fail-fast（WithAuthMiddleware + WithPublicEndpoints 互斥校验）+ **PUBLIC-ENDPOINT-METHOD-MATCH-01** (Cx3, P1): 公共端点匹配为 path-only，不含 method 维度。当前无同路径多方法路由（latent risk）。升级为 `"METHOD /path"` 格式（向后兼容：无 method 前缀匹配所有 method）。涉及 router.go + auth/middleware.go + bootstrap.go + 调用方 (discovered via PR#158 six-seat review) | 3h → 4h |
| **Config 治理** | ✅ CFG-KEYFILTER-WIRE-01 PR#158(ConfigKeyFilterer 接口 + bootstrap selective notification + minimal exposure 配置快照裁剪) + ✅ CFG-ERRCODE-01 PR#158(评估结论: 保持 fmt.Errorf，runtime 基础设施层惯例) | 2h → 0h |
| **PR#133 review C3** | ✅ F1-ARCH-03 PR#158(SecurityHeaders 接线测试) + ✅ F2-SEC-03 PR#158(traceparent 信任边界测试) + ✅ F3-TEST-01 PR#158(12 个 converter nil guard + 零值断言) + ✅ F4-OPS-01 PR#158(bootstrap.WithSecurityHeadersOptions 便利包装) | 3h → 0h |
| **DTO nil 语义保护** | DTO-NIL-SEMANTIC-01 (Cx2, P2): converter nil guard 可能把上游不变量异常"平滑"为空 data 成功响应。关键 handler 在写成功响应前应校验领域对象非 nil，返回 errcode 而非空 DTO。涉及 12+ handler.go (discovered via PR#158 six-seat review) | 3h |
| **PR#158 post-merge followup** | **PUBLIC-ENDPOINT-METHOD-MATCH-01** (Cx3, P1): 公共端点匹配为 path-only，不含 method 维度；当前无同路径多方法路由（latent risk）。升级为 `"METHOD /path"` 格式（向后兼容：无 method 前缀匹配所有 method）。涉及 router.go + auth/middleware.go + bootstrap.go + 调用方 (discovered via PR#158 six-seat review) | 2h |
| **PR#159 post-merge followup** | ✅ **AUTH-LEGACY-TOKEN-REMOVE-01** PR#162: servicetoken 2-part `{ts}:{hmac}` 分支已删除，仅保留 3-part。(discovered via PR#159 外部审查) | 1h → 0h |
| **序列化边界收敛** | EVENT-PAYLOAD-TYPED-01: sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct (对齐 cell-patterns.md 北极星) (discovered via PR#133 re-review) | 3h |
| **统一健康注册** | ✅ HEALTH-CONTRIBUTOR-01 PR#135: `kernel/cell.HealthContributor` 接口 + bootstrap 自动发现 + access-core 实现 + 删除 main.go 手工接线 | 3h → 0h |
| **PoolStats DX** | POOLSTATS-IFACE-01(三个 adapter PoolStats 无公共接口，OTel collector 需 per-adapter switch — 等 OTel 需求明确后设计公共 `TotalConns()/IdleConns()` 子接口) + ~~POOLSTATS-JSON-01~~ ✅ PR#148(json tags + ConnectionState.MarshalText + roundtrip tests) | 1.5h → 0.5h |
| **PR#135 review C2-C3** | ✅ BOOTSTRAP-ADAPTERINFO-TEST-01 PR#135 + ✅ VALIDATE-MODE-ALLOWLIST-01 PR#135 + ✅ AUDIT-VERIFY-CAMELCASE-01 PR#135 + ✅ AUDIT-VERIFY-LEVEL-01 PR#135(audit-verify L0→L2: 实际写 outbox+txRunner 是 L2 行为，所有 peer slice 均 L2+；slice level 无运行时强制，仅改善元数据准确性) + ✅ TXRUNNER-FAIL-TEST-01 PR#135 (discovered via PR#135 6-seat review) | 3h → 1h |
| **Readyz 安全** | ✅ READYZ-VERBOSE-TOKEN-01 PR#151（rebased from #149）: `WithVerboseToken` bootstrap 选项 + `X-Readyz-Token` header + constant-time 比较 + main.go `GOCELL_READYZ_VERBOSE_TOKEN` 接线（real 模式必填） | 2h → 0h |
| **Bootstrap 发现机制加固** | ✅ AUTH-DISCOVERY-MULTI-PROVIDER-01 PR#135: 多 authProvider cell 发现循环从 first-wins+break 改为全量扫描+冲突 fail-fast | 1h → 0h |
| **Flaky test** | ✅ SECURECOOKIE-TAMPER-FLAKY-01: 位翻转修复（`encoded[mid]^1`），PR#137 + ✅ RMQ-CONFORMANCE-ISOLATION-01 PR#150（#230）: `TestRabbitMQ_Conformance` 每个 subtest 独立 Connection 隔离（shared conn teardown reconnect 窗口导致下一 subtest `acquire channel` fail-fast），root cause 确认 + `-count=3` 验证 + ✅ **RMQ-TIMING-FLAKE-01** PR#161(#288): mockChannel autoConfirmation/autoCloseConfirm 消除 Publish_Success/Nacked/ConfirmChannelClosed sleep-race；Subscribe 系 require.Eventually 替换 sleep+cancel；stress `-race -count=50` 洁净 + ✅ **HOOK-DURATION-MEASURE-01** PR#161: `invokeHook` `start := time.Now()` 前移到 `context.WithTimeout` 之前，HookEvent.Duration 覆盖 deadline 全窗口（修复 TestHookTimeout_BeforeStartExceeds flake） + ✅ **GOCOGNIT-15-TIGHTEN** PR#161: CI `gocognit` min-complexity 16→15 对齐 CLAUDE.md（only-new-issues 保护 28 预存量） | 0.5h → 0h |
| **Registry 健壮性** | ✅ REGISTRY-CONSUMERS-UNKNOWN-KIND-01: Consumers() + Provider() 签名改为 `(T, error)`，unknown kind / not found 返回 typed error（`ErrContractNotFound` / `ErrValidationFailed`）。PR#142 | 1.5h → 0h |
| **快修合集** | #26(.env.example 补 `GOCELL_S3_REGION=us-east-1`, `.env.example`) + ✅ #27(contract CI PR#139) + F-7(BUILD-OUTDIR-01 统一 `go build -o bin/` 输出目录) + ✅ #17 Hook 增强 PR#154(WM17-F2-2 per-hook ctx.WithTimeout + WM17-F4-3 LifecycleHookObserver + Prometheus impl 在 `adapters/prometheus/`) + #18(CB 接口+封装清理 CB-IFACE-01 Allow/Report 拆分 + CB-ENCAP-01 消除 gobreaker import, `runtime/resilience/circuitbreaker/`) + ~~#21(Journey 校验 F-5 catalog 不校验引用)~~ stale: REF-06+REF-07 已覆盖 journey cell/contract 引用校验 + ✅ **GOCELL-VALIDATE-FMT-REDESIGN** PR#152 follow-up: `printResult` 改为 `[CODE] msg (field: X) / at file:line:col` 两行，`at` 行纯净支持 IDE 点击跳转 | 9h → 6h |
| **CI 供应链加固** | CI-DIGEST-01(testcontainers 镜像 tag+digest 双固定) + CI-LINT-PIN-01(golangci-lint 版本固定到 patch 级 + dependabot 自动升级) (discovered via PR#139 review P2-3) | 2h |
| **Lint 全仓现代化** | **LINT-MODERN-01** (Cx2, P3): 全仓库 golangci-lint modernization baseline 清理。受 `only-new-issues: true` 保护的预存量：① `rangeint`（for i:=0;i<N;i++ → for i:=range N）约 20+ 处，集中在 `adapters/circuitbreaker/gobreaker_test.go` + `runtime/http/router/router_test.go`；② `stringsseq`（`strings.Split` → `strings.SplitSeq` 迭代器）多处；③ `forvar`（不必要的循环变量拷贝）；④ `inline`（constant 应内联）；⑤ `testingcontext`（`context.WithCancel(ctx)` → `t.Context()`，bootstrap_test.go 多处）；⑥ `any` over `interface{}`（bootstrap_test.go:1360）；⑦ **deprecated**：`nhooyr.io/websocket` 已迁到 `github.com/coder/websocket`（router_test.go 5 处）。**不混入功能 PR** — 独立 modernization PR 分 batch 处理（websocket 迁移单独 PR，其余 lint rule 可一次性）(discovered via PR#163 post-review diagnostics, 2026-04-17) | 6h |
| **bootstrap Run() 复杂度** | **BOOTSTRAP-RUN-COGNIT-01** (Cx3, P2): `runtime/bootstrap/bootstrap.go::Run()` 认知复杂度 225（pre-existing，远超 CLAUDE.md 上限 15），当前被 `//nolint:gocognit` 抑制。随着 PR#158 WithAuthMiddleware/WithPublicEndpoints fail-fast + PR#163 WithCircuitBreaker nil fail-fast 继续累积，该函数成为所有 option 错误收敛点。**方案**: 提取 `validateOptions()` / `buildRouter()` / `startServers()` 三段式；每段独立可测。不阻塞发布但 tech debt 累积中 (discovered via PR#163 agent 报告, 2026-04-17) | 4h |
| **PG 域 Repository** | PG-DOMAIN-REPO: 5 个域 Repository 的 PostgreSQL 实现（User/Session/Role/Device/Command）。当前全部只有 `cells/*/internal/mem/` 内存实现，无持久化——重启后数据丢失。`adapters/postgres/` 已有 outbox_writer/tx_manager/migrator 基础设施可参考。**前置准备（可并行）**: ① 4 个 migration DDL（users/sessions/roles/devices+commands，Session version 乐观锁 + Role permissions JSONB 策略）；② `ports.RoleRepository` 补 `CreateRole` 方法（当前缺 Create，seed 路径靠 type assertion）；③ **CONFIG-VERSIONS-MIGRATION-01** (discovered via PR#155 review F4, Cx2): `cells/config-core/internal/adapters/postgres/config_repo.go::PublishVersion` / `GetVersion` 引用 `config_versions(id, config_id, version, value, sensitive, published_at)` 表，但 `adapters/postgres/migrations/` 仅有 outbox 三个 migration，无 `config_entries` / `config_versions` DDL。当前 postgres adapter 仅被 mock 测试覆盖，运行时未接线（`cmd/core-bundle/main.go` 注释 "Storage is always in-memory for now"）。PG-DOMAIN-REPO 落地时必须同时新增 `004_create_config_entries_and_versions.sql`：`config_entries(id, key, value, sensitive boolean not null default false, version, created_at, updated_at)` + `config_versions(id, config_id, version, value, sensitive boolean not null default false, published_at)`，否则切换 postgres 模式即崩。**落地后联动**（必须同 PR 或紧邻 PR 完成，防止元数据/代码漂移）: ① **RBAC-ASSIGN-LEVEL-UPGRADE-01**: `cells/access-core/cell.go:300` `cell.L0` → `cell.L1`（真实事务语义，comment 已标注 "Upgrade to L1 when PostgreSQL adapter is introduced"）；② **SEED-ROLE-IFACE-01**: `doSeedAdmin` 去掉 `*mem.RoleRepository` type assertion（见本表 Seed 接口抽象行）；③ **ACCESS-LEVEL-AUDIT-01**: access-core 其余 slice（sessionlogin/sessionrefresh/sessionlogout/identitymanage）重新审视 L1/L2 声明是否匹配真实事务语义，校正 slice.yaml `consistencyLevel` 与 `AddSlice` 参数；④ **AUTH-CACHE-01 激活**: 原标记为可选（见 Wave 1 SHOULD #28a），PG 落地后升级为必做——真实 DB round-trip 触发 session cache 必要性，需补 Redis short-TTL session cache + 撤销失效路径 | 3-5d |
| **系统拓扑自省** | SYSTEM-TOPOLOGY-API: `GET /internal/v1/system/topology` 返回 cell/slice/contract 拓扑 JSON。当前前端被迫用 js-yaml 直接读取后端 YAML 文件拼接拓扑图。可基于 `kernel/registry` 现有数据构建 | 4h |
| **PR#142 review defer** | ✅ R2-KERNEL-SLOG-01(generator/depcheck 错误传播 fail-fast) + R4-ALLOWEDFILES-EMPTY-ID(默认推导移除) + R5-FMT15-PATH-ASSERT(schemaPath 断言) + P1#3(allowedFiles 模型统一为 required)。PR#146 | 1.5h → 0h |
| **PR#155 review CI evidence** | VALIDATE-EVIDENCE-CI-01 (Cx2, P2): PR 提交时缺少 `gocell validate` / `check contract-health` 通过的机器化证据，reviewer 仅能凭 commit message 信任。**处理方案**: ① CI workflow 新增独立 `metadata-check` job（`go run ./cmd/gocell validate` + `check contract-health`），失败阻断 PR；② PR template 增加 "metadata gate" 勾选项；③ 若已有 build-test 覆盖，仅需在该 job 顶部加这两条命令并把输出 upload 为 artifact。**根因**: cell-patterns.md 明确"新增 contracts/ 文件后必须运行 validate"是开发者手动职责，未上 CI gate。开源对比：buf check / openapi-cli 都有独立 lint job (discovered via PR#155 review F7) | 1h |
| **PR#155 6-seat review followup** | ✅ AUTHZ-WRITE-CONFIG-01 PR#155 (F1, Cx2, P1): publish + rollback handler 增加 `auth.RequireAnyRole(ctx, "admin")` + 401/403/200 测试 + happy-path 测试统一注入 admin 上下文。根因：高风险写端点仅靠全局 JWT 认证，缺角色守卫，违反 K8s/Kratos/go-zero 默认拒绝原则。+ ✅ ERROR-MSG-SCRUB-01 PR#155 (F3, Cx1, P1): postgres adapter 4xx Message 不再泄漏内部 id/version，转用 `errcode.Safe` / `Error{InternalMessage:...}` 二段式（公共 `"config not found"` + 内部 `config repo: GetByKey miss key=...`）。+ ✅ ROLLBACK-NEGPATH-TEST-01 PR#155 (F4, Cx1, P2): rollback 补 `KeyNotFound` / `VersionNotFound` service+handler 测试，断言 errcode 类型 + 响应 body 不含内部前缀。+ ✅ SCHEMA-SENSITIVE-DESC-01 PR#155 (F5, Cx1, P2): publish/v1 + rollback/v1 response.schema.json 增加 description 字段说明 `sensitive=true` 时 `value` 为占位符不可回写。 | 4h → 0h | PR#155 |
| **PR#165 review round 2 (已修 PR#165)** | ✅ **ENV-ADAPTER-MODE-DRIFT-01** PR#165 (Cx1, P1 运维): `.env.example` mode 注释对齐实际 allowlist（只认 `""` 和 `"real"`，不接受字面 "dev"）+ `TestValidateAdapterMode_DevLiteralRejected` 锁定语义。+ ✅ **WIRING-TESTS-01** PR#165 (Cx2, P1 测试): 3 条接线级回归：`TestAuditCore_Wiring_StaleCursor_DemoVsDurable`（DurabilityMode→cell.Init→service→ExecutePagedQuery，durable 400/demo 200）、`TestConfigCore_Wiring_PublisherFailure_DemoVsDurable`（ConfigCore.Init→WithDemoFailOpen，Publish+Rollback 两条路径）、`TestRun_RealMode_DemoKey_FailsFast`（三 env 通道 table-driven）。+ ✅ **DURABLE-MISSING-CODEC-FAILFAST-01** PR#165 (Cx2, P2 安全/架构): 4 个 cell (audit/config/order/device) 在 `DurabilityDurable` 且 `cursorCodec == nil` 时返回 `errcode.ErrCellMissingCodec` (HTTP 500)；Demo 模式保留公开 demo key fallback；对标 go-zero `MustSetUp` + K8s `kube-apiserver --service-account-key-file`。4 个回归测试 + httputil 映射。discovered via PR#165 外部审查 round 2 (2026-04-18) | 4h → 0h |
| **PR#160 follow-up PR-X2 (pkg/query 稳定性)** | ⏳ **PR160-P1-C** codec nil 构造期 fail-fast（Service 层，非 cell 层 fallback；见 cells/*/slices/*/service.go）+ **PR160-P2-2-parse** `ParsePageRequest` 层 cursor 长度上限（与 Decode 层形成双层校验）+ **PR#165 F1-2** `configpublish.WithDemoFailOpen(bool)` 与 `query.RunMode` 语义整合（统一 cell 级 RunMode 注入，淘汰两套表达）+ **PR#165 F3-1** `cmd/core-bundle/loadCursorCodec` helper 直接单测（含 wrap/errcode 链断言）+ **PR#165 F5-1** `pkg/query.RunModeForDemo` godoc 增 "Do not extend" 警告。~~errcode 余项~~ PR-X1 已全量覆盖生产路径；~~**PR160-P1-B** KnownDemoKeys 深拷贝~~ PR-X1 删除该 API 后自动消解。discovered via PR#160 六席位审查 + PR#165 reviewer (2026-04-17) | 3h |
| **PR#160 follow-up PR-X3 (cursor key rotation 接线)** | ⏳ **PR160-P1-D** `NewCursorCodec(current, previous)` 的启动接线与 env 通道：`GOCELL_CURSOR_PREVIOUS_SIGNING_KEY` 双 env var（对标 K8s `--service-account-key-file` 单签名/多验证、gorilla/securecookie `CodecsFromPairs`）+ 轮换兼容回归测试。依赖 PR-X1 先删除 `IsDemoKey` 语义耦合。discovered via PR#160 六席位审查 (2026-04-17) | 4h |
| **PR#157 post-merge 6-seat review followup** | **AUTHZ-WRITE-CONFIG-WRITE-01** (P0, Cx2, 阻塞, 2026-04-18 外部审查 re-confirm): `cells/config-core/slices/configwrite/handler.go` create/update/delete 三端点无 `auth.RequireAnyRole(ctx, "admin")`，与 `configpublish` 的 publish/rollback 已有 admin gate 不一致（同一资源域授权策略漂移）。PR#155 只补了 publish/rollback，write 侧遗漏。**修复**: 复用 `configpublish/handler.go` 的 `roleAdmin` const 模式（或提取到 `cells/config-core/internal/dto/authz.go` 共享），三端点入口加 gate + 401/403/200 测试。根因：授权策略分散在端点实现层，缺统一动作模型或中间件级入口。对标 K8s/Kratos/go-zero 统一授权属性。+ 🔄 **CONFIG-DEMO-FAILOPEN-01** (P1, Cx2, PR-X1 进行中): `cells/config-core/slices/configpublish/service.go:188-194` demo 模式 publisher 发布失败仅 `s.logger.Warn` 后 `return nil`，与 cell.yaml 声明 L2 一致性不符（L2 要求 outbox-fact 不丢）。**修复**: durable 模式下去掉 fail-open，统一路径都上抛发布错误；demo fail-open 仅保留在显式 `DiscardPublisher{}` 或 `Assembly.Mode == Demo` 的 assembly。对标 Watermill Forwarder/outbox fail-closed、Temporal 重试策略不吞错。+ **REPO-SCAN-CLASSIFY-01** (P2, Cx2, 高优先): `cells/config-core/internal/adapters/postgres/config_repo.go::GetByKey`(85-96) / `GetVersion`(204-212) 把所有 Scan 错误（含连接断开、驱动异常）映射为 `ErrConfigRepoNotFound`。**修复**: `errors.Is(err, sql.ErrNoRows)` 判 not found，其他错误返回 `ErrInternal` 并保留 `InternalMessage`；PR#155 的 message scrub 不变。+ **CONTRACT-ERROR-SCHEMA-01** (P2, Cx1): `contracts/http/config/publish/v1/contract.yaml` + `contracts/http/config/rollback/v1/contract.yaml` 仅声明 2xx 成功壳，未声明 401/403 错误体 schema（`{"error": {"code", "message", "details"}}`）。**修复**: 在两个 contract.yaml 的 `responses` 新增 401/403 entries 引用共享错误 schema；对齐 `pkg/errcode` 结构 + `error-handling.md` 规范。对标 Kubernetes Status / Kratos 统一错误 / go-zero error contract。discovered via PR#157 post-merge 六席位审查 (2026-04-17) | 6h |
| **2026-04-18 外部审查 followup** | **AUTH-TOKEN-INTENT-01** (P0, Cx3, 阻塞): login/refresh 共用 `JWTIssuer.Issue`，token 无 audience/purpose claim；全局 Bearer 中间件只做 `verifier.Verify`，session-validate 只看 `sid`，不区分用途 → **refresh token 可直接访问业务接口**。**修复**: Issue() 增 `TokenIntent`（"access"/"refresh"）映射到 JWT `aud`；verifier 按请求 scope 拒绝 intent 不匹配的 token；`/auth/refresh` 只接受 `intent=refresh`，其余路径只接受 `intent=access`。补 2 条集成测试：refresh token 不能访问业务接口、access token 不能调用 refresh 流程。对标 K8s TokenRequest audience 绑定、go-micro access/refresh purpose claim。涉及 `runtime/auth/jwt.go` + `runtime/auth/middleware.go` + `cells/access-core/slices/{sessionlogin,sessionrefresh,sessionvalidate}/service.go`。+ **AUTH-INT-REACHABILITY-01** (P1, Cx2): `cells/access-core/slices/*/auth_integration_test.go` 只断言匿名→401、public→非401，未覆盖带合法 token 的 handler 到达性；路由丢失 / 方法错误 / handler 500 不会被这组测试发现。**修复**: 保留匿名 401 覆盖的同时，加带合法 token 的到达性断言（200 + 响应体关键字段）+ public handler 精确状态/响应断言。对标 K8s integration test、Kratos http_test 完整状态断言。+ **READYZ-BROKER-HEALTH-01** (P2, Cx3): `runtime/http/health/` readyz 当前仅反映 `eventrouter.Health()` 生命周期，不反映 subscriber/connection 真实连通状态。当前 core-bundle 未接 RabbitMQ 时无爆炸，一旦 real/durable assembly 走真实 subscriber，router 绿 ≠ broker 可用，readiness 误报会继续摘流到已断连实例。**修复**: `adapters/rabbitmq/connection.go` 暴露 `Health() error`，`runtime/bootstrap/` 自动把关键 subscriber/connection 注册为 health checker；可选 `WithBrokerHealth(opts...)` 控制开关。对标 K8s readiness probe 反映后端依赖、Watermill `router.Running()` 不等于健康。discovered via 2026-04-18 外部审查 | 9h |

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

### 设计决策记录（PR#146 review 确认，不修）

> 以下 1 项在 PR#146 审查中提出，经根因分析后确认为设计正确，记录于此避免重复审查。

| # | Finding | 结论 | 理由 |
|---|---------|------|------|
| I2 | computeBoundaryContracts 遍历全量 contracts，无关 assembly 的 unknown-kind 导致生成失败 | 不修 | generator 必须遍历全量 contracts 才能发现 imported contracts（provider 在 assembly 外、consumer 在内）。scope 预过滤需先 resolve provider/consumers，而 resolve 本身是可能失败的操作——形成循环依赖。FMT-09 在 validate 阶段已保证所有 kinds 合法，generator fail-fast 是正确的 defense-in-depth。对标: K8s code-generator 上游标记预过滤但也 fail-fast；go-zero goctl 调用方传已缩减 spec；Kratos Wire 编译期保证图完整 |

### 设计决策记录（PR#155 review 确认，不修）

> 以下 1 项在 PR#155 审查中提出，经根因分析后确认为项目惯例，不修。

| # | Finding | 结论 | 理由 |
|---|---------|------|------|
| F6 | `slices/*/slice.yaml` 的 `allowedFiles` 同时列出 kebab 目录（`config-publish/`）和 no-dash 目录（`configpublish/`） | 不修 | 全项目统一惯例：YAML 元数据放 kebab 目录、Go 包目录用 no-dash（CLAUDE.md "Cell 开发规则"），所有 slice 都是双路径。FMT-14 治理规则已守护这种结构，单一化反而违反约定。`gocell scaffold slice` 模板默认产出双路径条目 |

### 设计决策记录（PR#159 review 确认，不修）

> 以下 4 项在 PR#159 6 席位 + /fix 审查中提出，经根因分析后确认为设计正确或收益不足，记录于此避免重复审查。

| # | Finding | 结论 | 理由 |
|---|---------|------|------|
| F1.2 | 7 个 Option type 分散在 auth 包 | 不修 | 每个 Option 针对不同构造函数（AuthMiddleware/ServiceTokenMiddleware/NewJWTVerifier/NewJWTIssuer/NewKeySet/NewEnvKeyProvider/NewInMemoryNonceStore），合并会丧失类型安全性（传错 option 不会编译报错）。对标 Kratos jwt.Option、logging.Option 也是每个中间件独立 option type |
| F2.5 | GenerateServiceToken 在 crypto/rand 失败时静默返回 "" | 不修 | Go 1.24+ `crypto/rand.Read` 文档明确 "always returns len(p) and a nil error"（底层 getrandom(2)，失败时 fatal）。空串触发下游 401 是 fail-closed。对标 PR#140 设计决策记录 A 项已有先例 |
| F4.2 | ServiceTokenMiddleware nil ring 每请求 500 而非构造时报错 | 不修 | 改签名从 `func(http.Handler) http.Handler` 变为 `(func(http.Handler) http.Handler, error)` 是 breaking change 且违背 Go 标准 middleware 签名惯例（net/http、go-chi、Kratos 均不返回 error）。构造时 panic 不符合 GoCell 编码规范。当前 fail-closed + slog.Error 行为安全 |
| F6.2 | 缺 service token duration metric | 不修 | HMAC-SHA256 验证是纯 CPU 操作 <0.1ms，histogram 分桶无法提供有意义的信息。JWT RS256 涉及 RSA 公钥运算 0.5-5ms 才需 histogram。增加无信息量 metric 增加 label 基数和 scrape 成本。等 Redis nonce store 引入网络 I/O 后再加 |

### 触发条件项（仅在条件满足时做）

| # | 任务 | 工时 | 触发条件 |
|---|------|------|----------|
| 28c | **AUTH-PROVIDER-EXPORT-01** `authProvider` 接口定义在 `runtime/bootstrap`（unexported），与 `HTTPRegistrar`/`EventRegistrar`（kernel/cell exported）不一致。因 kernel→runtime/auth 层依赖限制无法直接移动 | 1h | 第二个 auth provider cell |
| 28g | **AUTH-ISSUE-OPTIONS-01** `JWTIssuer.Issue()` 4 参数，重构为 `IssueOptions` struct (`runtime/auth/jwt.go`) | 1h | Issue() 第 5 个参数 |
| 28h | **DEVICE-ENQUEUE-RBAC** HandleEnqueue 无设备维度鉴权——当前设计为 operator 管理端点（任何已认证 operator 可向任意设备下发命令, `cells/device-cell/slices/device-command/handler.go`） | 2h | 多租户 operator |
| ~~28i~~ | ✅ **CB-TYPED-NIL-GUARD-01** PR#163 (b29cb0f): `middleware.IsTypedNilAllower(cb)` kind-gated reflect 检查 + `WithCircuitBreaker` option 层 fail-fast（bootstrap + router 双层登记 `circuitBreakerNil`，`Run()` / `NewE()` 返回 error）。原触发条件已不再适用，直接实施。 | — | — ✅ |
| 28j | **CB-RESILIENCE-PACKAGE-01** 把 `Allower` / `CircuitBreakerRetryAfter` 从 `runtime/http/middleware` 迁移到 `runtime/resilience/circuitbreaker/` 独立包，HTTP middleware 只消费。当前 `adapters/circuitbreaker` 为取接口定义反向 import `runtime/http/middleware`，接口与 HTTP 耦合。CLAUDE.md "不设计假想未来需求" + "只改需要改的" → 无真实非 HTTP 消费点前不做 (discovered via PR#163 review F3, 2026-04-17) | 4h | 出现第二个非 HTTP 的 CB 消费方（如出站 RPC / DB / consumer CB） |

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
> ✅ 全部完成（含 Batch A-EF + P1 穿插）

### Batch A: ✅ 安全加固 — PR#131

### Batch B: ✅ DTO 对齐 + 配套测试 + HSTS — PR#133

### Batch C: ✅ L2 Gate + Strict Mode + Order Cell — PR#135 + PR#136 + PR#137

### Batch D: ✅ 契约完整性 — PR#138

### Batch EF: ✅ pkg 加固 + CI + 治理 — PR#139+140+142+146

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
| ✅ #6 TPUB-01 TestPubSub | 4h | PR#141（+PR#144 fix +PR#145 review fix）|
| ✅ #10 Watcher 核心增强 | 7h | PR#132 |
| ✅ #11 Watcher 状态面 + 连接池指标 | 4h | PR#132 + PR#134 |

---

## 执行总览

```
已完成:
  Batch 1-5: ✅ PR#67-114 (48 PRs)
  Wave 1: ✅ 全部完成
    Batch A: ✅ PR#131
    Batch B: ✅ PR#133
    Batch C: ✅ PR#135+136+137
    Batch D: ✅ PR#138
    Batch EF: ✅ PR#139+140+142+146
    #10+#11: ✅ PR#132+PR#134
  P1 穿插:
    #6 TPUB+#31 RMQ: ✅ PR#141 (+PR#144 fix +PR#145 review fix)
  Post-Wave 3:
    PR-H1 部分: ✅ PR#143 (H1-2 IDENTITY-AUTHZ + H1-4 ROLE-ASSIGN + H1-5 SEED-ADMIN + H2-3 PATCH-CONTRACT)
    PR-H1 安全加固: ✅ PR#151 (H1-1 PROD-KEY-FAILFAST + H1-6 READYZ-VERBOSE-TOKEN，rebased from #149)
  底座加固 Phase 1 部分:
    PR-K-META: ✅ PR#142(META-67-01+03) + PR#152(META-67-02 file:line:col)
    PR-K-OUTBOX: ✅ PR#147(META-SIZE-01+OUTBOX-GUARD-01) + PR#148(DISCARD-OBS-01+OUTBOX-RECEIPT-01)
    PR-K-CELL: ✅ PR#142(#27b SLICE-ALLOWEDFILES-01+CONTRACT-LIST-LINT-01) + PR#154(#17 WM17-F2-2+F4-3) + PR#157(HOOK-OBSERVER-ASYNC-01 hook_dispatcher)
    PR-R-OBS: ✅ PR#154(OBS-DX-01+OBS-DOC-01) + PR#156(OBS-TABLE-01+CONFORMANCE-SLEEP-01+CURSOR-P2-02) + PR#157(OBS-METRIC-01 provider-neutral metrics + OTEL-COV-01 + OBS-LEAK-02 + OBS-POOLSTATS-WAITCOUNT-01 + OBS-HOOK-DISPATCHER-CFG-01)
  契约补全:
    PR-H2: ✅ PR#143(H2-3 IDENTITY-PATCH) + PR#155(H2-1 CONFIG-ROLLBACK + H2-2 CONFIGPUBLISH-REDACT + AUTHZ-WRITE-CONFIG + ERROR-MSG-SCRUB + SCHEMA-SENSITIVE-DESC)
  CI:
    PR#153: ✅ integration 与 build-test 并行 + SonarCloud 独立 job + rabbitmq testcontainer 包内共享（wall time ~40% 下降）

剩余:
  P1 (穿插):           ~4h — #5 README(4h)
  PR-H1 安全加固:       ✅ 全部完成（H1-1+H1-2+H1-3+H1-4+H1-5+H1-6）
  PR-H2 契约补全:       ✅ 全部完成（H2-1+H2-2 PR#155 / H2-3 PR#143）
  PR-FEAT 功能补全:     ~6h — Device List(3h) + Flag Write(3h)（List Lint ✅ PR#142）
  Wave 2: #28 SOL-B-01(4h) + ~~#32 cursor~~ ✅ PR#156 + #33 WM-35(2d)
  Wave 3: #34 WM-36(1.5d)
  Batch 8 (v1.0 后):   ~48h — 不阻塞发布（扣除 PR#154-157 完成项）

关键路径: WM-35 (2d) → WM-36 (1.5d) → H2(0.5d) → FEAT(1d) → README(0.5d) → Review(2d) = 剩余 ~7 工作日
```

---

## Wave 3 后实施计划（2026-04-16 整理）

> 按 PR 级别组织。所有问题已验证代码现状。
> 前置: Wave 3 (WM-35 + WM-36) 完成后执行。

### PR-H1: 安全加固（P0+P1，~3.5h 剩余，v1.0 阻塞）— H1-2/4/5 ✅ PR#143

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| H1-1 | cmd/core-bundle | ✅ **PROD-KEY-FAILFAST** P0: `loadKeySet` 改为先尝试 env key，real 模式下 fail-fast；`validateAdapterMode` 接受 `real`；`loadSecret` 取代 `envOrDefault` 并在 real 模式必填。PR#151（rebased from #149） | Wave 3 | 2h | `cmd/core-bundle/main.go` | PR#137-138 集成审查 P0 | PR#151（rebased from #149） |
| H1-2 | cells/access-core | ✅ **IDENTITY-AUTHZ-01** P1: identitymanage handler 7 端点授权守护（create/delete/lock/unlock → admin-only; get/update/patch → self-or-admin）+ `RequireAnyRole` auth helper | Wave 3 | 1.5h | `cells/access-core/slices/identitymanage/handler.go` | PR#137-138 集成审查 P1 | PR#143 |
| H1-3 | kernel/cell | ✅ **DURABLE-NIL-GUARD** P1: 5 个 L2 cell（access-core/audit-core/config-core/order-cell/device-cell）Init() 全部含显式 nil XOR guard + `CheckNotNoop` 拒绝 Nooper，durable+nil 旁路闭合。kernel 层 `CheckNotNoop` 文档明确 "nil checks belong in the caller"，caller 职责已履行 | Wave 3 | 完成于 PR#135/#136/#137（L2-HARD-GATE + BOOTSTRAP-STRICT-MODE + guard hardening） | `cells/*/cell.go` | PR#137-138 集成审查 P1 |
| H1-6 | runtime/http/health + cmd/core-bundle | ✅ **READYZ-VERBOSE-TOKEN** P1: `WithVerboseToken` 选项 + constant-time token 比较 + `X-Readyz-Token` header 守卫 + main.go 接线 `GOCELL_READYZ_VERBOSE_TOKEN`（real 模式必填）PR#151（rebased from #149） | Wave 3 | 2.5h | `runtime/http/health/health.go` + `runtime/bootstrap/bootstrap.go` + `cmd/core-bundle/main.go` | PR#134 review + PR#149 review round 2 | PR#151（rebased from #149） |
| H1-7 | cells/access-core | **RBAC-OUTBOX-MIGRATION** P2: `rbacassign.Service` 当前是"角色变更 → 会话失效"双写（partial-commit 窗口在错误日志中可观测）。迁移到 transactional outbox 模式：角色变更 + `role.assigned.v1` / `role.revoked.v1` 事件原子写入，consumer 异步失效 session。ref: Watermill outbox pattern | H1-8 outbox consumer 基础设施 | 6h | `cells/access-core/slices/rbacassign/service.go` + `cells/access-core/slices/sessionlogout/consumer.go`（新）+ contract event schemas | PR#149 review round 2 |
| H1-4 | cells/access-core | ✅ **ROLE-ASSIGN-API** P1: `rbacassign` slice — `POST /internal/v1/access/roles/assign` + `DELETE /internal/v1/access/roles/revoke`（admin-only, L0, idempotent）+ contracts | Wave 3 | 2h | `cells/access-core/slices/rbacassign/` + `contracts/http/auth/role/assign/v1/` | backend_issues.md #3 | PR#143 |
| H1-5 | cmd/core-bundle | ✅ **SEED-ADMIN** P1: `WithSeedAdmin(user, pass)` / `WithSeedAdminRole()` bootstrap options，从 `GOCELL_ADMIN_USER`/`GOCELL_ADMIN_PASS` 环境变量读取 | Wave 3 | 1h | `cmd/core-bundle/main.go` + `cells/access-core/internal/mem/` | backend_issues.md #3 | PR#143 |

### PR-H2: 契约补全（P1，✅ 全部完成）— H2-1+H2-2 ✅ PR#155 / H2-3 ✅ PR#143

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| H2-1 | contracts/config-core | ✅ **CONFIG-ROLLBACK-CONTRACT** P1: `cell.go:214` 注册了 `POST /{key}/rollback` 路由，有 handler_test 覆盖，但无 HTTP contract + schema + contract_test。路由暴露但契约体系无定义，变更无法被自动拦截。PR#155 | PR-H1 | 1.5h | `contracts/http/config/rollback/v1/` + `cells/config-core/slices/configpublish/contract_test.go` + `slice.yaml` | PR#137-138 集成审查 P1 | PR#155 |
| H2-2 | contracts/config-core | ✅ **CONFIGPUBLISH-REDACT-01** P1: publish 响应 schema required `value` 明文字段，未复用 configwrite 的 `RedactedValue` 脱敏逻辑。同改 ConfigVersion.Sensitive 字段 + DTO redaction + Rollback 也复用 snapshot 的 Sensitive flag（PR#155 review F1）。PR#155 | 无 | 0.5h | `cells/config-core/internal/domain/version.go` + `cells/config-core/slices/configpublish/handler.go` + `service.go` + `contracts/http/config/publish/v1/response.schema.json` | PR#138 review + 集成审查 | PR#155 |
| H2-3 | contracts/access-core | ✅ **IDENTITY-PATCH-CONTRACT** P2: identitymanage PATCH request schema validation + contract_test | 无 | 1h | `contracts/http/auth/identity/patch/v1/` + `cells/access-core/slices/identitymanage/contract_test.go` | PR#138 review P2 | PR#143 |

> H2-1 和 H2-2 同改 configpublish slice，一个 PR。

### PR-FEAT: 功能补全 + 治理（P1，~8h，v1.0 前建议）

> 来源: backend_issues.md #1 #2 前后端联调缺口 + #138b list lint 搭车。PR-H2 之后、PR-README 之前做，README 需反映最终 API。

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| FEAT-1 | cells/device-cell | **DEVICE-LIST-API** P1: 无 `GET /api/v1/devices` 列表端点，只有 `/{id}/status` 和 `/{id}/commands`。前端设备大盘无数据来源。新建 `device-list` slice + 分页查询 + contract + contract_test | PR-H2 | 3h | `cells/device-cell/slices/device-list/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| FEAT-2 | cells/config-core | **FLAG-WRITE-API** P1: config-core feature flag 仅有 GET + Evaluate，无写入能力。管理界面 feature flag 开关不可操作。新增 `PUT /api/v1/config/flags/{key}` 写入端点 + contract + contract_test | PR-H2 | 3h | `cells/config-core/slices/configwrite/` + `contracts/http/config/flags/write/v1/` | backend_issues.md #2 |
| FEAT-3 | kernel/governance | **CONTRACT-LIST-LINT-01** `gocell check contract-health` 增加 list 响应格式检查：response schema 含 `data: array` 时必须同时包含 `nextCursor` + `hasMore`（对齐 `api-versioning.md` 规定的 `{"data": [...], "nextCursor": "...", "hasMore": bool}` 格式）。与 FEAT-1 搭车——lint 规则上线时立即守护新 list 端点 | 无 | 2h | `kernel/governance/` | PR#138 review P1-3 + PR#141 review |

### PR-EF: ✅ pkg 加固 + CI + 治理 — PR#139+140+142

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| EF-1 | pkg/httputil + runtime/http | **#20 decode 加固 + REQID-RAND-ERR + HT-02 + MAIN-TEST-CLEANUP** | 无 | 4h | `pkg/httputil/decode.go` + `runtime/http/middleware/request_id.go` + `pkg/httputil/response_test.go` + `cmd/core-bundle/main_test.go` | 6B + 集成审查 |
| EF-2 | CI | **#19 CI 增强 + #27-CI contract YAML 校验** | 无 | 2.5h | `.github/ci.yml` + `adapters/*/integration_test.go` | 6B |
| EF-3 | kernel/cell | **#27b SLICE-ALLOWEDFILES-01** | 无 | 2h | `kernel/cell/base.go` + all `slice.yaml` | PR#119 review |

### PR-README: 文档收口（P1，~4h，Wave 4 前）

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| R-1 | docs | **#5 AUTH-DX-01** README + seed 用户 + sso-bff walkthrough | PR-H1（反映最终 API） | 4h | `README.md` + `examples/sso-bff/README.md` | 6B + P4 review |

### PR-TPUB: ✅ 已完成 — PR#141 (+PR#144 fix +PR#145 review fix)

| # | 模块 | 问题 | 前置 | 工时 | 文件 | 来源 |
|---|------|------|------|------|------|------|
| T-1 | kernel/outbox | ✅ **#6 TPUB-01** TestPubSub conformance harness + RabbitMQ conformance + backoff 统一 + ClaimPolicy enum | 无 | 4h | `kernel/outbox/outboxtest/` + `adapters/rabbitmq/` | 6B | PR#141 |

### 执行顺序

```
Wave 2 (WM-35 BFF handler, 2d)
     ↓
Wave 3 (WM-36 SecureCookie, 1.5d)
     ↓
  PR-H1 安全加固 (P0+P1, H1-1+H1-3 剩余, 阻塞 v1.0)  ─┐
  PR-H2 契约补全 (H2-1+H2-2 剩余, 依赖 H1)             ─┘ 可串行一个 PR
       ↓
  PR-FEAT 功能补全 (Device List + Flag Write, 依赖 H2)
       ↓
  PR-README #5 文档收口 (依赖 H1+H2+FEAT, 反映最终 API)
       ↓
  Wave 4 Review + v1.0 tag
       ↓
  Batch 8 (含 PG-DOMAIN-REPO + SYSTEM-TOPOLOGY-API)
```
