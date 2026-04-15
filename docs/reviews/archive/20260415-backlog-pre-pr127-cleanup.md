# GoCell Backlog

> 只含待办事项。已完成项归档至 `docs/reviews/archive/`。
> 更新日期: 2026-04-15
> Batch 1-5: ✅ 全部完成 (PR#67-114, 48 PRs)
> Wave 1 进行中: ✅ PR#116-122+PR#124 (8 PRs 已合入), 🔄 PR#123 (待合并)
> 重构依据: `tools/docs/20260414-backlog-wave-restructure.md`
> 旧版备份: `docs/reviews/archive/20260414-backlog-pre-wave-restructure.md`

---

## Wave 1: 立即可做（32 项，~109h）

> PR#112 (trace propagation) / PR#113 (outbox cleanup) / PR#114 (Health/Readyz) 已合入，前置全部满足。
> 按优先级排序；单人执行时从上到下依次做，多人时全并行。
> 0414 调整: access-core / config-core / rabbitmq 按模块合并为加固 PR，一次性封口；反复被审查发现的模式嵌入自动化约束。
> 0415 进展: PR#116(flatten) PR#117(qodana) PR#118(WM-2-F1) PR#119(access-core 加固) PR#120(flatten 遗留) PR#121(L4 API) PR#122(config-core 加固) PR#124(RMQ 加固) 已合入；PR#123(Bootstrap 全家桶) 待合并。

### Auth 关键路径起点 ★

| # | 任务 | 工时 | 文件 |
|---|------|------|------|
| 1 | **WM-2-F1** KeyProvider 接口抽象 | **1d** | `runtime/auth/` | ✅ PR#118 |

> ★ v1.0 唯一关键路径：WM-2-F1 (1d) → WM-35 (2d) → WM-36 (1.5d) = 4.5d 串行。每延迟 1 天 = v1.0 推迟 1 天。

### P1 正确性

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| 2 | **L4 API 收敛** L4-API-01: ValidateNew 改名 + AdvanceCommand 统一副作用 + CommandStateAdvancer 迁移契约 + L4-PURE-01(time.Now 注入) + L4-RETRY-01(ResetForRetry) | 5.5h | `kernel/outbox/l4.go` | 6A | ✅ PR#121 |
| 3 | **CONTRACT-OP-01** HTTP operation model 收口: slice 元数据缺 HTTP serve contract、response.schema oneOf 混合 | 4h | `cells/config-core/slices/*/slice.yaml` + `contracts/http/config/` + `cells/access-core/slices/sessionlogout/slice.yaml` | 6B | ✅ PR#125 |
| 4 | **CONTRACT-TEST-02** 假阳性修复: contracttest helper 不验证真实 handler/outbox 输出 | 5h | `pkg/contracttest/` + `cells/*/contract_test.go` + `cells/device-cell/slices/deviceregister/` | 6B | ✅ PR#125 |
| 5 | **AUTH-DX-01** README + seed 用户 + sso-bff walkthrough: auth 已拦截全部业务路由，README 失效；sso-bff README 缺 refresh/GET user/event 消费 demo (P4-P1-6)。具体漂移: refresh curl 发 `sessionId` 实际需 `refreshToken`；logout 204 空 body 管道 jq 失败；audit jq 用 `.createdAt` 实为 `.Timestamp` | 4h | `README.md` + `cells/access-core/internal/mem/` + `examples/sso-bff/README.md` | 6B + P4 review + 0414 审查 |
| 6 | **TPUB-01** TestPubSub 真实 adapter 认证: conformance harness 替换 sleep + 接入 RabbitMQ adapter | 4h | `kernel/outbox/outboxtest/` + `adapters/rabbitmq/` | 6B |
| 7 | **API 响应格式统一** P4-TD-09(list endpoint 缺 `nextCursor/hasMore`) + P4-TD-10(POST 201 未包裹 `{"data":...}`) — v1.0 后修 = breaking change | 4h | `cells/*/handler.go` | B8 提前 | ✅ PR#126 |
| 8 | **Entity→DTO** P4-TD-13: 8 个 handler 直出 entity 含内部字段，需 DTO 映射隔离 API 契约 — v1.0 后修 = breaking change | 4h | `cells/*/handler.go` (user/session/config/flag/audit/order/device/demo) | B8 提前 | ✅ PR#126 |
| 8a | **L2-TX-01** txRunner 装配缺口: access-core/config-core 仅校验 `outboxWriter`，缺 `txRunner` 成对约束——业务写入成功但 outbox 写入可在事务外失败，破坏 L2 原子性。参照 order-cell XOR 约束模式修复 | 3h | `cells/access-core/cell.go` + `cells/config-core/cell.go` + 各 service `runInTx` | 0414 审查 | ✅ PR#119+PR#122 |
| 8b | **EVT-SUB-01** event contract subscriber 漂移: `contracts/event/config/changed/v1/contract.yaml` 声明 access-core 为 subscriber，但 `RegisterSubscriptions` 是 no-op；`J-config-hot-reload.yaml` passCriteria 不可达。需实现 handler 或从 contract subscribers 移除 | 3h | `cells/access-core/cell.go` + `contracts/event/config/changed/v1/contract.yaml` + `journeys/J-config-hot-reload.yaml` | 0414 审查 | ✅ PR#119 |

### 运维 + 基础设施

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| 9 | **Bootstrap 加固 + 端点隔离** OPS-4(graceful shutdown) + BOOT-PANIC-01 + BOOT-OPTION-01 + INFRA-EXPOSE-01(/metrics opt-in + health 分离) | 6h | `runtime/bootstrap/` + `runtime/http/router/` | 6A | 🔄 PR#123 |
| 10 | **Watcher 核心增强** R97-02(debounce) + R97-F1(symlink-pivot) + WM-34-F1(目录级监听) + F2(metrics) + F3(key 过滤) + R97-04(DeepCloneValue) + R97-R3-02(ShutdownDrain channel 同步) | 7h | `runtime/config/watcher.go` + `store.go` | 6A |
| 11 | **Watcher 状态面 + 连接池指标** R97-F3(Generation/observedGeneration) + OPS-5(PG/Redis/RMQ 连接池指标) | 4h | `runtime/config/` + `adapters/postgres/` + `adapters/redis/` + `adapters/rabbitmq/` | 6A |
| 12 | **RabbitMQ 连接正确性** RMQ-RACE-01(WaitConnected 竞态) + P3-DEFER-05(Health 状态区分) | 4h | `adapters/rabbitmq/connection.go` | 6A | ✅ PR#124 |

### PR#116 Flatten 遗留修复 ✅

| # | 任务 | 状态 | 来源 |
|---|------|------|------|
| F-1 | **GEN-BOUNDARY-01** generate 写盘前 isWithinRoot 路径边界校验 | ✅ fix/507-flatten-followup | PR#116 review P1 |
| F-2 | **QA-CWD-01** run-qa.sh + phase-gates.yaml S5/S7 cwd:src → 根目录 | ✅ fix/507-flatten-followup | PR#116 review P1 |
| F-3 | **DOC-CDSRC-01** 活跃文档 21 处 cd src 清扫（15 文件） | ✅ fix/507-flatten-followup | PR#116 review P1 |
| F-4 | **TEST-SCOPE-01** Makefile test-integration 与 CI 范围对齐 | ✅ fix/507-flatten-followup | PR#116 review P1 |
| F-5 | **SONAR-ROOT-01** Sonar 扫描范围补充根级包 | ✅ fix/507-flatten-followup | PR#116 review P2 |
| F-6 | **ARTIFACT-ALIGN-01** 二进制产物策略对齐（gitignore/clean/assembly） | ✅ fix/507-flatten-followup | PR#116 review P2 |
| F-7 | **BUILD-OUTDIR-01** 统一 `go build -o bin/` 输出目录，`.gitignore` 改为 `/bin/`，消除硬编码产物名 | 待定 | PR#116 review P2 |

### P2 Tech Debt

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| 13 | **Session 安全** P3-TD-10 Session refresh TOCTOU 乐观锁 + P4-TD-11(in-memory repo 并发 goroutine 测试) | 5h | `cells/access-core/internal/` | 6B 高风险 + P4 review | ✅ PR#119 |
| 14 | **order+demo+examples 修复** P4-TD-04 + P4-TD-12 + EVT-HDR-RESTORE + WM-6-F8(demo 模式开关) + P3-DEFER-03(examples 新 API) + NOOP-RENAME-01 + NIL-PUB-P1(device-cell nil publisher) | 7.5h | `cells/order-cell/` + `cells/demo/` + `cells/device-cell/` + `examples/` | 6B |
| 15 | **cursor 回归矩阵** CURSOR-TEST-01 + CUR-HDL-01: 5 个分页入口补 malformed/missing-scope/cross-context 三类回归 | 4h | `cells/*/handler_test.go` + `service_test.go` | 6B |
| 16 | **config-core 修正** CFG-JSON-01(json tags camelCase) + FLAG-RACE-01(并发测试) + P3-TD-12(rollback version 校验) | 3.5h | `cells/config-core/internal/domain/` | 6B | ✅ PR#122 |
| 17 | **Hook 增强** WM17-F2-2(ctx 超时) + WM17-F4-3(Prometheus metrics via HookObserver 接口) | 3h | `kernel/cell/` | 6B |
| 18 | **CB 接口+封装清理** CB-IFACE-01(Allow/Report 拆分) + CB-ENCAP-01(消除 gobreaker import) | 3h | `runtime/resilience/circuitbreaker/` | 6B |
| 19 | **CI 增强** ✅ CI-01(integration 路径, fix/507-flatten-followup Makefile 对齐) + ✅ Qodana(PR#117) + T1-7(golangci-lint) + TC-PIN-01(testcontainers 镜像 pin 到 patch 版本，当前全仓用 floating minor tag `3.12-management-alpine`，PR#124 review S4-F1) | 2.5h | `.github/ci.yml` + `adapters/*/integration_test.go` | 6B |
| 20 | **decode 加固** DECODE-STR-01 classifyDecodeError 脆弱性 | 2h | `pkg/httputil/decode.go` | 6B |
| 21 | **Journey 校验** F-5 catalog 不校验引用 | 2h | `kernel/journey/catalog.go` | 6B |
| 22 | **DELETE 无 body** DELETE-NOCONTENT-01: 204 + body=0 语义测试 | 1.5h | `contracts/http/auth/user/delete/v1/` | 6B | ✅ PR#125 |
| 23 | **OTel 覆盖率** OTEL-COV-01 testcontainers 集成测试 | 1h | `adapters/otel/` | 6B |
| 24 | **Trace trust policy** TRUST-POLICY-01: public-facing endpoint trust-boundary 策略（参考 otelhttp `WithPublicEndpoint`：new root + link），当前默认 trusted-upstream + **OBS-REQID-TRUST**: request_id middleware 无条件信任外部 `X-Request-Id`，需信任边界校验 | 4h | `runtime/http/middleware/tracing.go` + `request_id.go` | 5B PR#112 review + 217 tech-debt |
| 25 | **HSTS 加固** C-H4: `security_headers.go` 补 `includeSubDomains` | 0.5h | `runtime/http/middleware/security_headers.go` | P2 tech-debt |
| 26 | **.env.example 补全** ENV-S3: 补 `GOCELL_S3_REGION=us-east-1` — `s3.Config.Validate()` 必填但示例缺失 | 0.5h | `.env.example` | P4 review |
| 27 | **examples contract CI** INT-2: order-cell/device-cell contract YAML 存在且被 slice.yaml 引用，但 CI 未校验 | 1h | `.github/workflows/ci.yml` | P4 review |
| 27a | **RMQ-TEST-01** RabbitMQ 集成测试名实不符: `TestIntegration_ConsumerBaseRetry` 直调 handler 不过 broker（假阳性 P1）+ `TestIntegration_ConnectionRecovery` 仅做 Health check 无断连验证（P2）。`DLXBrokerNative` 已确认是真实集成测试无需改动 | 4h | `adapters/rabbitmq/integration_test.go` | 0414 审查 | ✅ PR#124 |
| 27b | **SLICE-ALLOWEDFILES-01** 全部 slice 默认 allowedFiles 不覆盖 Go 包目录（kebab-case YAML 目录 vs no-dash Go 包目录），需系统性补 allowedFiles 或改 `BaseSlice.AllowedFiles()` 默认逻辑 | 2h | `kernel/cell/base.go` + all `slice.yaml` | PR#119 review |
| 27c | **L2-HARD-GATE-01** L2 cell 启动门禁从 publisher 兜底升级为强制 outbox+txRunner（需配合 demo 模式显式开关 `WithDemoMode()`），消除声明能力与运行能力漂移 | 3h | `cells/access-core/cell.go` + `cells/config-core/cell.go` + `cells/audit-core/cell.go` | PR#119 review P1-1 |
| 27d | **OUTBOX-WRITE-ERR-01** `publishEvent` 吞 `outbox.Write` 错误: durable 模式下 outbox 写入失败仅日志不返回 error，事务内业务写入成功但事件丢失，违反 L2 原子性。需改 `publishEvent` 返回 error 并传播给 `runInTx` | 3h | `cells/config-core/slices/configpublish/service.go` + `cells/config-core/slices/configwrite/service.go` | PR#122 review F5-1 | ✅ PR#125 |
| 27e | **NOOP-TX-SHARED-01** `noopTxRunner` 在 5 处重复定义（order-cell/access-core/config-core test + core-bundle test + sso-bff main），提取为 `kernel/persistence.NoopTxRunner` 共享类型（类似 `outbox.NoopWriter`） | 1h | `kernel/persistence/tx.go` + 5 处调用方 | PR#122 review F4-1 |
| 27f | **TEST-UNUSED-VAR-01** `cells/access-core/cell_test.go:33` `testPrivKey` 未使用，应改为 `_` 或移除 | 0.5h | `cells/access-core/cell_test.go` | PR#122 review F3-5 |
| 27g | **DEMO-WARN-STRUCTURED-01** access-core/config-core Init() demo 模式 `logger.Warn` 缺少结构化字段（`cell_id`、`consistency_level`），应补充以便运维快速定位 | 0.5h | `cells/access-core/cell.go` + `cells/config-core/cell.go` | PR#122 review F2-2 |
| 27h | **DEMO-PUBLISH-WARN-01** demo 模式 publisher 失败日志级别 `slog.Error` → `slog.Warn`（Error 应保留给影响正确性的故障）。IN_SCOPE 3 处已在 PR#125 修复（configwrite/configpublish/identitymanage），剩余 4 处：`sessionlogout/service.go:115`、`sessionlogin/service.go:179`、`auditappend/service.go:130`、`auditverify/service.go:104` | 0.5h | `cells/access-core/slices/session*/service.go` + `cells/audit-core/slices/audit*/service.go` | PR#125 review S5-F3 |
| 27i | **CONTRACT-CLIENTS-01** 9 个 HTTP contract `endpoints.clients: []` 缺少消费方声明（http.auth.refresh、http.config.flags.*、http.order.*、http.device.*），需产品确认各端点的 BFF/edge 消费方后填入 | 1h | `contracts/http/*/contract.yaml`（9 文件） | PR#125 review S1-F2 |
| 27j | **CONFIG-SCHEMA-CASE-01** config-core schema 命名不一致：`config/get` response.schema.json 用 PascalCase（迁就 handler 错误输出），`config/flags/*` response.schema.json 用 camelCase（遵循 API 规范）。两个 handler 都直出无 json tags 的 domain entity。阻塞于 #8 Entity→DTO；需统一为 camelCase schema + DTO handler 后再改 contract test 为真实调用 | 1.5h | `contracts/http/config/get/v1/response.schema.json` + `cells/config-core/slices/{configread,featureflag}/contract_test.go` + `internal/domain/*.go` | PR#125 review P2 | ✅ PR#126 |
| 27k | **DTO-CONVERTER-UNIT-TEST** 8 个 DTO converter 函数（toXxxResponse）无独立单测，仅靠 handler httptest 间接覆盖。若 converter 增加条件逻辑需补专项测试 | 2h | 6 个 handler_test.go | PR#126 review F4 |
| 27l | **EVALUATE-RESULT-LAYER** featureflag `EvaluateResult` 定义在 service.go 而非 handler 层，轻微违反 DTO-in-handler 模式。功能等同 DTO（2 字段 + json tags），实际风险极低 | 0.5h | `cells/config-core/slices/featureflag/service.go` + `handler.go` | PR#126 review F6 |
| 27m | **RBAC-LIST-FORMAT** rbaccheck `handleListRoles` 用 `{"data":[], "total":N}` 而非标准分页 `hasMore/nextCursor`。当前是全量返回小集合（单用户角色），语义合理；若角色数增长需改分页 | 0.5h | `cells/access-core/slices/rbaccheck/handler.go` | PR#126 review F10 | ✅ PR#126 (total 已移除) |
| 27u2 | **AUTH-CACHE-01** session 验证 DB round-trip 缓存: 每请求 `GetByID` 查主库，real adapter 下需 Redis short-TTL（5-15s）session cache + 撤销时主动失效。可选: circuitbreaker 包住 `GetByID`（仅 infra error 触发） | 4h | `cells/access-core/slices/sessionvalidate/service.go` + `adapters/redis/` | PR#127 review #12 |
| 27v | **AUTH-HEALTH-01** session DB 健康检查: 当前 `/readyz` 不含 session repo 状态，DB 宕机时 K8s 持续导流。需 session repo 实现 `Health()` + main.go 注册 `WithHealthChecker` | 2h | `cells/access-core/internal/mem/` + `cmd/core-bundle/main.go` | PR#127 review #8 |
| 27w | **AUTH-PROVIDER-EXPORT-01** `authProvider` 接口定义在 `runtime/bootstrap`（unexported），与 `HTTPRegistrar`/`EventRegistrar`（kernel/cell exported）不一致。因 kernel→runtime/auth 层依赖限制无法直接移动。当出现第二个 auth provider cell 时需正式化 | 1h | `runtime/bootstrap/bootstrap.go` + `kernel/cell/registrar.go` | PR#127 review #6 |
| 27x | **AUTH-REFRESH-VERIFIER-01** `sessionrefresh.Service` 注入 raw `jwtVerifier`（非 session-aware `validateSvc`），JWT 层 sid fail-closed 语义对 refresh 端点不成立。当前 DB 路径（`GetByRefreshToken + IsRevoked`）已确保撤销有效，安全风险低但架构不对称 | 2h | `cells/access-core/cell.go:203` | PR#127 review #7 |
| 27y | **AUTH-TTL-CONST-01** `accessTokenTTL` 3 处重复定义（login/refresh/main），提取共享常量或配置化 | 0.5h | `cells/access-core/slices/session*/service.go` + `cmd/core-bundle/main.go` | PR#127 review F5-3 |
| 27z | **SSO-BFF-AUTH-SYNC-01** sso-bff 仍用 `WithAuthMiddleware(jwtVerifier)` + raw JWTVerifier → 示例演示旧不安全模式，需同步为 `WithPublicEndpoints` + AccessCore 发现模式 | 1h | `examples/sso-bff/main.go` | PR#127 review #9 |
| 27aa | **AUTH-ISSUE-OPTIONS-01** `JWTIssuer.Issue()` 4 参数，如需第 5 个 claim 参数时重构为 `IssueOptions` struct | 1h | `runtime/auth/jwt.go` | PR#127 review F5-1（触发条件: 第 5 参数） |
| 27n | **HANDLER-TEST-CAMELCASE-ASSERT** 13 个 handler_test.go 无显式 camelCase key 断言（如 `assert.Contains(body, "createdAt")`），camelCase 合规由 contract_test + schema 守护 | 2h | 12+ handler_test.go | PR#126 review F12 |
| 27o | **CONFIG-VALUE-SENSITIVE** ConfigEntry.Value 无脱敏直接暴露，可能泄露 API key/密码。需设计 `sensitive` 标记机制或字段级访问控制 | 4h | `cells/config-core/slices/config*/handler.go` + `internal/dto/` | PR#126 review 预存在安全 | ✅ PR#128 |
| 27p | **DEVICE-IDOR** HandleListPending/Ack 无跨设备所有权校验，任意已认证用户可操作他人设备 | 4h | `cells/device-cell/slices/device-command/handler.go` | PR#126 review 预存在安全 | ✅ PR#128 |
| 27q | **AUDIT-ACTOR-BIND** 审计查询无 ActorID 绑定，任意已认证用户可读全量审计日志 | 3h | `cells/audit-core/slices/auditquery/handler.go` | PR#126 review 预存在安全 | ✅ PR#128 |
| 27r | **RBAC-ENUMERATE** handleListRoles 路径参数 userID 无 JWT subject 比对，可枚举他人角色 | 2h | `cells/access-core/slices/rbaccheck/handler.go` | PR#126 review 预存在安全 | ✅ PR#128 |
| 27ab | **DEVICE-ENQUEUE-RBAC** HandleEnqueue 无设备维度鉴权——当前设计为 operator 管理端点（任何已认证 operator 可向任意设备下发命令）。当引入多租户 operator 模型时需补充 per-device RBAC | 2h | `cells/device-cell/slices/device-command/handler.go` | PR#128 review S2-F02（触发条件: 多租户 operator） |
| 27s | **CONTRACT-SCHEMA-COVERAGE** 6 个端点有 DTO 但无 HTTP contract schema + contract_test（configwrite/configpublish/auditquery/rbaccheck/device-command/order-query） | 6h | `contracts/http/` + 6 个 slice contract_test.go | PR#126 review RC-3 |
| 27t | **CONTRACT-TEST-REAL-HANDLER** contract_test 使用硬编码 fixture 而非调用真实 handler，schema 符合性 ≠ handler 符合性 | 4h | `cells/*/slices/*/contract_test.go` | PR#126 review P2 |
| 27u | **DTO-COVERAGE-GAP** sessionlogin/sessionrefresh TokenPair + featureflag EvaluateResult 仍为 service 层类型直出，需补 handler 层 DTO | 2h | `cells/access-core/slices/session*/handler.go` + `cells/config-core/slices/featureflag/handler.go` | PR#126 review RC-2 |
| 27v | **DOMAIN-JSON-TAG-REMOVAL** Order/Device/Command domain entities 保留 json tags（事件序列化依赖），需设计 event payload DTO 层解耦后才能移除 | 4h | `cells/order-cell/internal/domain/order.go` + `cells/device-cell/internal/domain/device.go` | PR#126 review F11 |

---

## Wave 2: 串行后续（6 项，~27h）

> 依赖 Wave 1 中的特定任务完成后启动。

| # | 任务 | 前置 | 工时 | 文件 |
|---|------|------|------|------|
| 28 | **SOL-B-01** Claimer lease 续租 Receipt.Renew | L4 API (#2) | 4h | `kernel/outbox/` |
| 29 | **Bootstrap tracing 测试** BOOT-TEST-01 | Bootstrap 加固 (#9) | 2h | `runtime/bootstrap/` + `router/` | 🔄 PR#123 |
| 30 | **Bootstrap 次要清理** BOOT-MINOR-01: panic(err) + access_log real_ip | Bootstrap 加固 (#9) | 1h | `runtime/http/router/` | 🔄 PR#123 |
| 31 | **RabbitMQ 代码清理** P3-DEFER-01(backoff 提取) + P3-DEFER-02(FailOpen enum) | RMQ 连接 (#12) | 3h | `adapters/rabbitmq/` |
| 32 | **cursor 可观测** CURSOR-P2-02 invalid 结构化日志 | cursor 回归 (#15) | 1h | `cells/audit-core/` |
| 33 | **WM-35** BFF handler 接入 cookie session | WM-2-F1 (#1) | **2d** ★ | `runtime/auth/` |

> 建议合并 PR: #9+#29+#30 → "Bootstrap 全家桶" (9h) 🔄 PR#123 待合并；#2+#28 → "outbox 串行包" (✅ #2 PR#121 已合入, #28 可启动)。

---

## Wave 3: Auth 收尾

| # | 任务 | 前置 | 工时 |
|---|------|------|------|
| 34 | **WM-36** SecureCookie key rotation 双 key ring | WM-35 (#33) | **1.5d** ★ |

---

## Wave 4: Review + 发布（~16h）

> 前置: Wave 1-3 全部合入。

| # | 任务 | 工时 | 并行 |
|---|------|------|------|
| 35 | Review cells/ T1-3 审查 6 cell | 4h | ✅ |
| 36 | Review examples/ T1-6 审查 3 项目 | 2h | ✅ |
| 37 | Review 报告 T1-8 汇总 findings | 2h | #35+#36 |
| 38 | 发布文档 R-1(GOPRIVATE) + R-3(CONTRIBUTING) + R-5(迁移指南) + R-6(错误码) | 4h | ✅ |
| 39 | 性能基准 R-4 benchmark 测试 | 4h | ✅ |
| 40 | **v1.0 tag** R-2 git tag + CI 验收 | — | **全部完成后最后执行** |

---

## 关键路径与 PR 合并建议

### 关键路径

```
★ Auth 链 (唯一关键路径):
  WM-2-F1 (1d) → WM-35 (2d) → WM-36 (1.5d) → Review (2d) = 6.5 工作日

  其余 Wave 1 全部任务并行执行，总工时 ~101h 但不在关键路径上。
```

### PR 合并建议（36→~21 PR）

> 0414 调整: 对 top-3 问题模块按模块合并为加固 PR，一次性封口，避免逐条零散修复再被下轮审查追加。

| 合并 PR | 包含任务 | 工时 | 理由 |
|---------|---------|------|------|
| **access-core 加固** | #8a(access) + #8b + #13 | 9.5h | 模块封口: txRunner XOR + event 订阅实现/清理 + session TOCTOU | ✅ PR#119 |
| **config-core 加固** | #8a(config) + #16 | 5h | 模块封口: txRunner XOR + JSON camelCase + flag race | ✅ PR#122 |
| **RabbitMQ 加固** | #12 + #27a | 8h | 模块封口: 连接竞态 + ConsumerBaseRetry/ConnectionRecovery 测试修正 | ✅ PR#124 |
| Bootstrap 全家桶 | #9 + #29 + #30 | 9h | 同目录相关改动 | 🔄 PR#123 |
| Contract 正确性 | #3 + #4 + #22 | 10.5h | contract 体系修正 |
| API 契约加固 | #7 + #8 | 8h | 都改 handler 响应格式，v1.0 前必修 |
| Trust boundary | #24 (TRUST-POLICY + OBS-REQID) | 4h | 同一信任边界主题 |
| Kernel 小修 | #20 + #21 | 4h | 独立小改 |
| cursor 全家桶 | #15 + #32 | 5h | 紧密相关 |
| outbox 串行包 | #2 + #28 | 9.5h | 同包串行一起 review |
| 快修合集 | #25 + #26 + #27 | 2h | 三个独立小修 |

### 防御性自动化（随加固 PR 嵌入，无独立工时）

> 将反复被审查发现的模式变成代码/CI 约束，阻断同类问题再生。

| 约束 | 嵌入 PR | 机制 |
|------|---------|------|
| L2 Cell writer+tx XOR | access/config-core 加固 | `Cell.Validate()` 加 `(outboxWriter==nil) != (txRunner==nil)` 检查，参照 order-cell |
| contract subscriber 一致性 | contract-health 扩展 (#21 Journey 校验) | `gocell check contract-health` 校验 subscribers 有对应 `RegisterSubscriptions` handler |
| Integration 测试真实性 | CI 增强 (#19) | `-tags integration` 要求 testcontainer setup；裸 handler 调用归入 unit test |
| README curl 可执行 | AUTH-DX-01 (#5) | CI smoke test 执行 README curl 命令，断言 HTTP status + response schema |

### 模块封口 checklist

> 加固 PR 合入后标记 reviewed-sealed，后续仅在功能变更时重新审查，不做全模块扫描。

**Cell 模块** (access-core / config-core / order-cell):
- [ ] `Cell.Validate()` 覆盖所有硬约束（L2 writer+tx、依赖注入完整性）
- [ ] `RegisterSubscriptions` 与 `contract.yaml` subscribers 列表一致
- [ ] handler 输出匹配 `response.schema.json`
- [ ] README/walkthrough curl 可执行

**Adapter 模块** (rabbitmq / postgres / redis):
- [ ] 所有 `TestIntegration_*` 过真实 broker / testcontainer
- [ ] 测试名与测试行为一致（无假阳性）
- [ ] `Health()` 状态机覆盖 connected → disconnected → recovering 路径

---

## Batch 8: P2 偿债（v1.0 后，~43.5h，12 组全并行）

> 前置: v1.0 tag 发布后。不阻塞发布。
> 整理: 23 组 → 12 组（5 个小项合并为 OBS 全家桶、3 个合并为 Outbox 治理、2 个合并为 order-cell 收口；4 项提前到 Wave 1；+1 Builder 可选优化）

| PR 组 | 任务 | 工时 |
|-------|------|------|
| **OBS 可观测全家桶** | META-SIZE-01(Metadata key 数/大小上限) + OBS-TABLE-01(table-driven 改写) + OBS-METRIC-01(bridge counter/histogram) + OBS-DX-01(cloneMetadata 导出 + wrapper 清理 + godoc) + OBS-DOC-01(IsReservedMetadataKey usage example) | 6h |
| **Outbox 治理** | OUTBOX-GUARD-01(NoopWriter/DiscardPublisher lint 约束) + DISCARD-OBS-01(DiscardPublisher Logger 注入 + counter) + OUTBOX-RECEIPT-01(`outbox.Receipt` alias 全仓迁移 `idempotency.Receipt`) | 4h |
| **order-cell 收口** | ORDER-DEMO-01(demo 模式产品行为决策) + NIL-PUB-P2(5 个 L2 service nil publisher 防护) | 3h |
| Cursor DX | WM-6-F6(泛型 cursor helper) + F7(cursor 日志收口) + F1(prod guard) + TX-NIL-01(nil-safe 注释) | 3.5h |
| metadata parser | META-67-01(strict unknown-field reject) + META-67-02(位置信息错误报告) + META-67-03(cross-file 引用校验) | 2.5h |
| auth 增强 | WM-2-F2(HMAC replay 防护) + WM-2-F3(auth metrics) + AUTH-SIGNER-01(`SigningKeyProvider` 返回 `crypto.Signer` 替代 `*rsa.PrivateKey`，需自定义 jwt SigningMethod，前置: golang-jwt v6 或 wrapper) | 4h+2h |
| auth 测试 DX | AUTH-SLOG-01(KeySet/servicetoken 注入 slog.Handler 替代全局 `slog.SetDefault`，消除并行测试风险) + AUTH-NOWFUNC-01(`var nowFunc` 包级状态改为实例字段注入) | 3h |
| access-core 重构 | P3-TD-11: domain 模型拆分 User/Session/Role（前置: Wave 1 #13 Session TOCTOU 先完成） | 4h |
| 集成测试补全 | P4-TD-05(outbox 全链路) + RL-INT-01(Relay PG 集成) + P2-T-02(audit e2e) | 6h |
| 迁移+订阅 | RL-MIG-01(online-safe 索引 CONCURRENTLY) + RL-SUB-01(入站 ID 校验) | 3h |
| CMD 重构 | CMD-MODE-01(fail-fast) + CMD-REFACTOR-01(app 包提取) | 3.5h |
| 批量操作 | WM-7: 泛型 `BulkResult[T]` helper | 1d |
| **Builder 可选优化** | PR#115 `fmt.Sprintf→strconv.Itoa` 微优化：补 benchmark 文件 + 修正 bolt.md 矛盾指导（Itoa vs AppendInt 分层适用）+ 删除未验证的 "40%" 声称。~~前置: close PR#115 DRAFT~~ ✅ PR#115 已关闭 | 2h |

---

## v1.1 — 核心能力完善

### metadata-model-v3 校验规则

| # | 缺失规则 | 优先级 |
|---|---------|--------|
| G-1 | FMT-11: 动态状态字段禁入非 status-board 文件 | HIGH |
| G-2 | TOPO-07: actor.maxConsistencyLevel 约束 | MEDIUM |
| G-4 | deprecated contract 引用阻断 | MEDIUM |
| G-6 | Assembly boundary.yaml 存在性校验 | LOW |

### 未实现的 Kernel 子模块

| 子模块 | 说明 | 优先级 |
|--------|------|--------|
| kernel/wrapper | 契约级可观测 traced wrapper | P1 |
| kernel/command | 命令队列接口（L4 框架支持） | P1 |
| kernel/webhook | receiver + dispatcher | P2 |
| kernel/reconcile | 最终状态收敛 | P2 |
| runtime/scheduler | cron/定时任务 | P2 |
| kernel/replay | projection rebuild | P3 |
| kernel/rollback | rollback metadata | P3 |

### adapters/ 与 runtime/ 分层重整

| # | 问题 | 方向 |
|---|------|------|
| AL-01 | outbox_relay.go 轮询逻辑属于 runtime | 拆出 `runtime/outbox/relay.go` |
| AL-02 | distlock.go 续期 goroutine 属于 runtime | 拆出通用 distlock 接口 |
| AL-04 | runtime/auth 直接 import golang-jwt | 评估是否值得拆 |
| RMQ-STATUS-01 | `ConnectionStatus()` 返回 raw `ConnectionState` enum，dashboard 集成需结构化类型 (state+message+lastError) | `adapters/rabbitmq/connection.go` — P2, discovered via PR#124 S6-F2 review |

### 跨框架 GAP — v1.1 待评估

| GAP | 能力 | 预估 | 前置条件 |
|-----|------|------|---------|
| GAP-7 | Scheduler/cron | 1d spike | WM-17 ✅ |
| GAP-11 | Architecture dependency graph | 1d | archtest ✅ |
| GAP-13 | Auto API docs / OpenAPI | 2d | HR-02 ✅ |
| GAP-6 | Singleflight + cache helper | 1d | — |
| GAP-5 | Adaptive load shedding | 1.5d | WM-33b + RL-WIRE-01 |

### contract 模型增强

| # | 需求 | 优先级 |
|---|------|--------|
| CONTRACT-META-01 | contract.yaml 补 method/path/pathParams/queryParams/successStatus/noContent 一等元数据 | P1 |

### spec tech-debt 遗留

| ID | 问题 | 来源 |
|----|------|------|
| C-AC7 | JWT 无 `jti` claim — token 不可单独撤销，需 invalidate 整个 session | P2 tech-debt |
| C-L6 | Contract ID 格式不一致：scaffold 用点分 vs generator 用斜杠 — 跨工具链断裂 | P2 tech-debt |
| C-DC9 | `auditarchive` slice 仍是 stub（`ErrNotImplemented`），S3 adapter 已就绪但 service 未接线 | P2 tech-debt |
| DURABLE-TYPE-01 | Durable repository 约束仅靠运行时 fail-fast，缺类型系统层面的仓储能力区分 | 216 tech-debt |

### 架构风险

| ID | 问题 | 状态 |
|----|------|------|
| Cell 接口 | 12 方法，考虑拆分 Cell + CellLifecycle + CellMetadata | 暂缓 |
| adapter 测试 | 15 个 t.Skip 集成测试待补全 | TODO |
| ER-ARCH-01 | Router startup heuristic 500ms，C4 架构级 | v1.1 |

### winmdm Defer v1.1

| # | 需求 | 票数 |
|---|------|------|
| WM-18 | 延迟消息原语 | 3/6 |
| WM-32 | mTLS 中间件 | 4/6 |
| WM-4 | Webhook 出站 adapter | 4/6 |
| WM-5 | OData $filter | 2/6 |
| WM-22 | Visibility Query API | 1/6 |
| WM-23 | 单体→微服务 | 2/6 |
| WM-16 | 投影按需重算 | 1/6 |

---

## v2+ — 长期

| # | 需求 | 票数 |
|---|------|------|
| WM-28 | 服务发现 Registry | 0/6 |
| WM-29 | Saga 补偿 | 0/6 |
| GAP-1 | gRPC 双协议 | 0/6 |
| GAP-2 | 服务发现 | 0/6 |
| GAP-8 | CQRS 组件 | 0/6 |
| GAP-12 | Saga 补偿 | 0/6 |
| GAP-14 | 本地 Dashboard | 0/6 |

---

## winmdm Reject（9 项）

| # | 需求 | 票数 |
|---|------|------|
| WM-3 | X.509 证书管理 | 1/6 |
| WM-14 | Codec 注册表 | 1/6 |
| WM-21 | Mixin 共享逻辑 | 2/6 |
| WM-24 | Policy Engine | 1/6 |
| WM-25 | 短期证书 | 1/6 |
| WM-26 | FanOut/FanIn | 0/6 |
| WM-30 | 编译期 Contract 验证 | 2/6 |
| WM-31 | 跨协议元数据同步 | 0/6 |
| WM-34b | Kratos 两层中间件 | 2/6 |

---

## 执行总览

| Wave | 项数 | 工时 | 前置 | 里程碑 |
|------|------|------|------|--------|
| 1 | 32 | ~109h | 无（PR#112-114 已合入） | Auth 关键路径启动 + P1 正确性 + API 契约加固 + 运维 |
| 2 | 6 | ~27h | Wave 1 特定任务 | Auth WM-35 + Bootstrap/RMQ/cursor 收尾 |
| 3 | 1 | ~12h | WM-35 | Auth WM-36 收尾 |
| 4 | 6 | ~16h | Wave 1-3 全部合入 | **Review → v1.0 tag** |
| 8 | 12 | ~43.5h | v1.0 | P2 偿债（不阻塞发布） |

```
已完成:
  Batch 1-4: ✅ PR#67-91 (25 PRs)
  Batch 5A:  ✅ PR#94-101 (8 PRs)
  Batch 5B:  ✅ PR#102-114 (13 PRs, 含 PR#112 trace + PR#113 outbox + PR#114 health)
  6A 部分:   ✅ PR#107 runtime 竞态 + PR#114 Health/Readyz + PR#113 outbox 清理
  Wave 1 部分: ✅ PR#116(flatten) + PR#117(qodana) + PR#118(WM-2-F1) + PR#119(access-core 加固) + PR#120(flatten 遗留) + PR#121(L4 API) + PR#122(config-core 加固) + PR#124(RMQ 加固) — 8 PRs
  Wave 1+2 待合并: 🔄 PR#123(Bootstrap 全家桶: #9+#29+#30)

当前:
  Wave 1: 已完成 #1+#2+#8a+#8b+#12+#13+#16+#27a+F1~F6, 待合并 #9(PR#123); 剩余 #3~#8+#10~#11+#14~#15+#17~#27
  Wave 2-4: 45 项, ~164h → v1.0
  Batch 8:  12 组, ~43.5h (从 23 组合并整理 + Builder 可选优化)
  关键路径: ✅ WM-2-F1 (PR#118) → WM-35 (2d) → WM-36 (1.5d) → Review (2d) = 剩余 5.5 工作日
```

---

## Wave 1 执行顺序（7 批次，每批 3-4 项）

> 按依赖链和数据流排序。同批内全并行，跨批串行。
> 依赖图:
> ```
> #8a → #8b → #13        (access-core cell.go 逐步改动)
>   └→ #16               (config-core cell.go)
> #9 → #10 → #11         (bootstrap → watcher → watcher 指标)
> #12 → #27a, #6          (RMQ 连接 → 测试修正 / TPUB)
> #3 → #4                 (contract model → contract test)
> #7 → #8 → #5            (API 格式 → DTO → README 反映最终状态)
> #19 → #27               (CI → examples CI)
> ```

### Batch W1-1: 基座层（4 项，~19h，全并行）✅ 全部完成

| 任务 | 工时 | 为什么先做 | 状态 |
|------|------|-----------|------|
| #1 WM-2-F1 KeyProvider ★ | 1d | 关键路径起点，每延 1 天 v1.0 推 1 天 | ✅ PR#118 |
| #2 L4 API 收敛 | 5.5h | kernel/outbox API 稳定后 Wave 2 #28 才可启动 | ✅ PR#121 |
| #8a L2-TX-01 txRunner XOR | 3h | 两个 cell 加固 PR 的前置，改 Validate() 模式 | ✅ PR#119+PR#122 |
| #19 CI 增强 | 2.5h | golangci-lint + integration 路径，后续所有 PR 受益 | 部分 ✅ Qodana PR#117 |

### Batch W1-2: 运行时 + 事件基础（4 项，~17h）3/4 完成

| 任务 | 工时 | 依赖 | 状态 |
|------|------|------|------|
| #9 Bootstrap 加固 | 6h | 无（#10 依赖它） | 🔄 PR#123 |
| #12 RabbitMQ 连接正确性 | 4h | 无（#27a、#6 依赖它） | ✅ PR#124 |
| #8b EVT-SUB-01 event 订阅 | 3h | ← #8a | ✅ PR#119 |
| #3 CONTRACT-OP-01 contract model | 4h | 无（#4 依赖它） | — |

### Batch W1-3: 模块加固收口（4 项，~20.5h）2/4 完成

| 任务 | 工时 | 依赖 | 完成的加固 PR | 状态 |
|------|------|------|-------------|------|
| #13 Session TOCTOU | 5h | ← #8a, #8b | **access-core 加固** (#8a+#8b+#13) | ✅ PR#119 |
| #16 config-core 修正 | 3.5h | ← #8a | **config-core 加固** (#8a+#16) | ✅ PR#122 |
| #4 CONTRACT-TEST-02 | 5h | ← #3 | Contract 正确性 (#3+#4) | — |
| #10 Watcher 核心增强 | 7h | ← #9 | — | blocked by #9 |

### Batch W1-4: API 契约 + RMQ 收口（4 项，~16h）1/4 完成

| 任务 | 工时 | 依赖 | 完成的加固 PR | 状态 |
|------|------|------|-------------|------|
| #7 API 响应格式统一 | 4h | 无 | — | — |
| #8 Entity→DTO | 4h | ← #7（同 handler 文件） | **API 契约加固** (#7+#8) | — |
| #27a RMQ-TEST-01 | 4h | ← #12 | **RabbitMQ 加固** (#12+#27a) | ✅ PR#124 |
| #6 TPUB-01 | 4h | ← #12（前置已满足） | — | — |

### Batch W1-5: 二级加固（4 项，~18.5h）

| 任务 | 工时 | 依赖 |
|------|------|------|
| #11 Watcher 状态面 + 连接池指标 | 4h | ← #10 |
| #15 cursor 回归矩阵 | 4h | 无 |
| #14 order+demo+examples 修复 | 7.5h | 无 |
| #17 Hook 增强 | 3h | 无 |

### Batch W1-6: 独立 Tech Debt（4 项，~11h，全并行）

| 任务 | 工时 | 合入 PR |
|------|------|--------|
| #18 CB 接口清理 | 3h | 独立 |
| #24 Trace trust policy | 4h | Trust boundary |
| #20 decode 加固 | 2h | Kernel 小修 (#20+#21) |
| #21 Journey 校验 | 2h | Kernel 小修 (#20+#21) |

### Batch W1-7: 快修 + 文档收尾（6 项，~8.5h）

| 任务 | 工时 | 依赖 |
|------|------|------|
| #22 DELETE 无 body | 1.5h | 无 |
| #23 OTel 覆盖率 | 1h | 无 |
| #25+#26+#27 快修合集 | 2h | #27 ← #19 |
| #5 AUTH-DX-01 README | 4h | ← #7, #8（反映最终 API 状态，最后做）|
