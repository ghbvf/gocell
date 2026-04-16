# Wave 3 后实施计划

> 生成日期: 2026-04-16
> 基准: develop@454f1af (PR#138 合并后)
> 覆盖范围: 当前 backlog 全部未完成项 + 历史 backlog 残留 + 新审查发现 + v1.1+/Batch 8
> 每个问题包含: 编号、所属模块、问题详情、前置任务、工时、涉及文件、来源

---

## 阶段 1: v1.0 阻塞项（Wave 3 后立即做）

### PR-H1: 安全加固（P0+P1，~5h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| H1-1 | cmd/core-bundle | **PROD-KEY-FAILFAST**: `loadKeySet` 空值 adapterMode 走 dev 分支，用 `MustGenerateTestKeyPair` 生成临时 RSA 密钥。生产误配时密钥可预测、重启后全部 token 失效。`validateAdapterMode` 拒绝了 `real`（未实现）和未知值，但允许空值默认走 dev — 需拆分 dev/prod 启动路径，生产缺密钥直接 fail-fast | Wave 3 | 2h | `cmd/core-bundle/main.go` | PR#137-138 集成审查 P0 |
| H1-2 | cells/access-core | **IDENTITY-AUTHZ-01**: identitymanage 全部 7 个 handler 端点（create/get/update/patch/delete/lock/unlock）仅鉴权无授权，任何有效 JWT 可执行管理操作。对比 device-command 和 rbaccheck 已有 `RequireSelfOrRole`。需加 `RequireRole("admin")` 或 `RequireSelfOrRole` | Wave 3 | 1.5h | `cells/access-core/slices/identitymanage/handler.go` + `handler_test.go` | PR#137-138 集成审查 P1 |
| H1-3 | kernel/cell | **DURABLE-NIL-GUARD**: `CheckNotNoop` (durability.go:74) 对 nil 依赖 `continue` 跳过，不拒绝。DurabilityDurable + outboxWriter=nil + txRunner=nil 时 CheckNotNoop 全部通过，cell 以 demo 语义运行。access-core/config-core/audit-core 均存在此路径。需在 durable 模式下强制 nil 也报错 | Wave 3 | 1.5h | `kernel/cell/durability.go` + 5 个 `cell.go` | PR#137-138 集成审查 P1 |

### PR-H2: 契约补全（P1，~3h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| H2-1 | contracts/config | **CONFIG-ROLLBACK-CONTRACT**: `cell.go:214` 注册了 `POST /{key}/rollback` 路由，handler_test 有覆盖，但 `contracts/http/config/` 下无 rollback 目录 — 无 contract.yaml/schema/contract_test。路由暴露但契约体系无定义 | PR-H1 | 1.5h | `contracts/http/config/rollback/v1/` + `cells/config-core/slices/configpublish/contract_test.go` + `slice.yaml` | PR#137-138 集成审查 P1 |
| H2-2 | contracts/config | **CONFIGPUBLISH-REDACT-01**: publish 响应 schema `response.schema.json` 将 `value` 列为 required，未复用 configwrite 的 `RedactedValue` 脱敏逻辑。敏感配置值明文返回 | 无 | 0.5h | `cells/config-core/slices/configpublish/handler.go` + `contracts/http/config/publish/v1/response.schema.json` | PR#138 review P1-5 (138a) |
| H2-3 | contracts/access | **IDENTITY-PATCH-CONTRACT**: identitymanage PATCH 无 contract schema（#27s 遗漏端点），未知字段策略未在 schema 层声明 | 无 | 1h | `contracts/http/auth/identity/patch/v1/` + `cells/access-core/slices/identitymanage/contract_test.go` | PR#138 review P2 |

### 其他契约缺口（已验证，搭 PR-H2 或独立）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| H2-4 | contracts/config | **CONFIG-LIST-CONTRACT**: `GET /api/v1/config/` 列表端点无 contract | 无 | 0.5h | `contracts/http/config/list/v1/` | 代码验证发现 |
| H2-5 | contracts/config | **CONFIG-UPDATE-CONTRACT**: `PUT /api/v1/config/{key}` 更新端点无 contract | 无 | 0.5h | `contracts/http/config/update/v1/` | 代码验证发现 |
| H2-6 | contracts/config | **CONFIG-DELETE-CONTRACT**: `DELETE /api/v1/config/{key}` 删除端点无 contract | 无 | 0.5h | `contracts/http/config/delete/v1/` | 代码验证发现 |
| H2-7 | contracts/access | **IDENTITY-LIST-CONTRACT**: `GET /api/v1/access/users` 列表端点无 contract | 无 | 0.5h | `contracts/http/auth/identity/list/v1/` | 代码验证发现 |

---

## 阶段 2: v1.0 前建议做（SHOULD，与阶段 1 并行）

### PR-EF: pkg 加固 + CI + 治理（~8.5h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| EF-1 | pkg/httputil | **DECODE-STR-01**: `classifyDecodeError` 用字符串匹配分类 JSON 解析错误，脆弱且依赖标准库错误文案。需改为类型断言 | 无 | 1.5h | `pkg/httputil/decode.go` + `decode_test.go` | 6B |
| EF-2 | runtime/http | **REQID-RAND-ERR**: `request_id.go:109` `_, _ = rand.Read(buf[:])` 错误被吞，违反 error-handling.md 第 3 条。需改为显式 err + slog.Error | 无 | 0.5h | `runtime/http/middleware/request_id.go` | PR#112-136 集成审查 P2-6 |
| EF-3 | pkg/httputil | **HT-02**: `WriteDecodeError` 无显式测试，需覆盖 ErrValidationFailed→400、ErrBodyTooLarge→413、ErrInternal→500、plain error fallback | 无 | 1h | `pkg/httputil/response_test.go` | 历史 backlog 0-I |
| EF-4 | cmd/core-bundle | **MAIN-TEST-CLEANUP**: `TestLoadKeySet_UnknownMode_StillGeneratesEphemeral` 是不可达语义残留（`validateAdapterMode` 在入口已拒绝未知值），应删除 | 无 | 0.5h | `cmd/core-bundle/main_test.go` | PR#135 review P2 |
| EF-5 | CI | **#19 CI 增强**: golangci-lint 集成 + testcontainers 镜像 pin 到 patch 版本（当前 `3.12-management-alpine` floating tag）+ contract YAML CI 校验 | 无 | 2.5h | `.github/ci.yml` + `adapters/*/integration_test.go` | 6B + PR#124 review S4-F1 |
| EF-6 | kernel/cell | **#27b SLICE-ALLOWEDFILES-01**: 全部 slice 默认 allowedFiles 用 kebab-case 目录拼接，但 Go 包目录是 no-dash — glob 永远不匹配。需改 `BaseSlice.AllowedFiles()` 默认逻辑或系统性补 allowedFiles | 无 | 2h | `kernel/cell/base.go` + all `slice.yaml` | PR#119 review |

### PR-README: 文档收口（~4h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| R-1 | docs | **#5 AUTH-DX-01**: README curl 示例过期（refresh 参数名错、logout jq 失败、audit 字段名错）+ sso-bff README 缺 refresh/user/event demo + seed 用户创建说明 | PR-H1+H2 | 4h | `README.md` + `cells/access-core/internal/mem/` + `examples/sso-bff/README.md` | 6B + P4 review |

### PR-TPUB: 独立穿插（~4h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| T-1 | kernel/outbox | **#6 TPUB-01**: TestPubSub conformance harness 用 sleep 等待 + 无真实 RabbitMQ 连接。需替换 sleep 为事件驱动 + 接入 RabbitMQ adapter 做真实 broker 测试 | 无 | 4h | `kernel/outbox/outboxtest/` + `adapters/rabbitmq/` | 6B |

---

## 阶段 3: Batch 8 — v1.0 后偿债

### PR-B8-OBS: OBS 全家桶（~7h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-1 | kernel/outbox | **META-SIZE-01**: Metadata key 数量和大小无上限，恶意/误用可 OOM | v1.0 | 1h | `kernel/outbox/outbox.go` | 6A review |
| B8-2 | runtime/http | **OBS-TABLE-01**: observability bridge table-driven 改写，减少重复代码 | v1.0 | 1.5h | `runtime/http/middleware/` | 6A review |
| B8-3 | runtime/http | **OBS-METRIC-01**: bridge counter/histogram 补全 | v1.0 | 1.5h | `runtime/http/middleware/` | 6A review |
| B8-4 | kernel/outbox | **OBS-DX-01**: cloneMetadata 导出 + wrapper 清理 + godoc | v1.0 | 1h | `kernel/outbox/` | 6A review |
| B8-5 | kernel/outbox | **OBS-DOC-01**: IsReservedMetadataKey usage example | v1.0 | 0.5h | `kernel/outbox/` | 6A review |
| B8-6 | adapters/otel | **#23 OTEL-COV-01**: OTel 覆盖率 testcontainers 集成测试 | v1.0 | 1.5h | `adapters/otel/` | 6A review |

### PR-B8-OUTBOX: Outbox 治理（~4h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-7 | kernel/outbox | **OUTBOX-GUARD-01**: NoopWriter/DiscardPublisher lint 约束（go vet 或 golangci-lint 自定义规则） | v1.0 | 2h | `kernel/outbox/` | 6B review |
| B8-8 | kernel/outbox | **DISCARD-OBS-01**: DiscardPublisher Logger 注入 + counter（可观测静默丢弃量） | v1.0 | 1h | `kernel/outbox/outbox.go` | 6B review |
| B8-9 | kernel/outbox | **OUTBOX-RECEIPT-01**: `outbox.Receipt` alias 全仓迁移 `idempotency.Receipt` | v1.0 | 1h | `kernel/outbox/` + `kernel/idempotency/` | 6B review |

### PR-B8-CURSOR: Cursor 全家桶（~7.5h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-10 | cells | **#15 CURSOR-TEST-01 + CUR-HDL-01**: 5 个分页入口补 malformed/missing-scope/cross-context 三类回归 | v1.0 | 3h | `cells/*/handler_test.go` + `service_test.go` | 6A review |
| B8-11 | pkg/query | **WM-6-F6/F7/F1**: 泛型 cursor helper + cursor 日志收口 + prod guard | v1.0 | 2h | `pkg/query/` | WM-6 |
| B8-12 | cells | **TX-NIL-01**: txRunner nil-safe 行为未文档化 | v1.0 | 0.5h | `cells/*/service.go` | 历史 backlog |
| B8-13 | cells/audit-core | **#32 CURSOR-P2-02**: cursor 可观测 invalid 结构化日志 | v1.0 | 1h | `cells/audit-core/` | 6A review |
| — | kernel/persistence | ~~#27e NOOP-TX-SHARED-01~~ | — | — | — | ✅ PR#136 |

### PR-B8-META: metadata parser（~2.5h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-14 | kernel/metadata | **META-67-01**: strict unknown-field reject | v1.0 | 1h | `kernel/metadata/parser.go` | PR#67 review |
| B8-15 | kernel/metadata | **META-67-02**: 位置信息错误报告 | v1.0 | 1h | `kernel/metadata/parser.go` | PR#67 review |
| B8-16 | kernel/metadata | **META-67-03**: cross-file 引用校验 | v1.0 | 0.5h | `kernel/metadata/parser.go` | PR#67 review |

### PR-B8-AUTH: auth 增强（~6h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-17 | runtime/auth | **WM-2-F2**: HMAC replay 防护 | v1.0 | 2h | `runtime/auth/` | WM-2 |
| B8-18 | runtime/auth | **WM-2-F3**: auth metrics (token verify latency/failure count) | v1.0 | 2h | `runtime/auth/` | WM-2 |
| B8-19 | runtime/auth | **AUTH-SIGNER-01**: `SigningKeyProvider` 返回 `crypto.Signer` 替代 `*rsa.PrivateKey`，需自定义 jwt SigningMethod。前置: golang-jwt v6 或 wrapper | golang-jwt v6 | 2h | `runtime/auth/jwt.go` | PR#127 review |

### PR-B8-AUTH-DX: auth 测试 DX（~3h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-20 | runtime/auth | **AUTH-SLOG-01**: KeySet/servicetoken 注入 slog.Handler 替代全局 `slog.SetDefault`，消除并行测试风险 | v1.0 | 2h | `runtime/auth/` | PR#131 review |
| B8-21 | runtime/auth | **AUTH-NOWFUNC-01**: `var nowFunc` 包级状态改为实例字段注入 | v1.0 | 1h | `runtime/auth/jwt.go` | PR#131 review |

### PR-B8-ACCESS: access-core 重构（~4h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-22 | cells/access-core | **P3-TD-11**: domain 模型拆分 User/Session/Role（前置: Session TOCTOU ✅ PR#119） | v1.0 | 4h | `cells/access-core/internal/domain/` | Phase 2 review |

### PR-B8-INTEG: 集成测试补全（~6h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-23 | adapters | **P4-TD-05**: outbox 全链路 3-container 集成测试（PG+RMQ+app） | v1.0 | 2h | `adapters/postgres/` + `adapters/rabbitmq/` | Phase 4 review |
| B8-24 | adapters/postgres | **RL-INT-01**: Relay PG 集成测试 | v1.0 | 2h | `adapters/postgres/outbox_relay_test.go` | PR#46 review |
| B8-25 | cells/audit-core | **P2-T-02**: audit e2e 测试 (J-audit-login-trail) | v1.0 | 2h | `cells/audit-core/` | Phase 2 review |

### PR-B8-MIGRATE: 迁移+订阅（~3h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-26 | adapters/postgres | **RL-MIG-01**: online-safe 索引用 `CREATE INDEX CONCURRENTLY` | v1.0 | 2h | `adapters/postgres/migrations/` | PR#46 review |
| B8-27 | adapters/rabbitmq | **RL-SUB-01**: 入站 ID 校验（防止空/过长 message ID） | v1.0 | 1h | `adapters/rabbitmq/subscriber.go` | PR#46 review |

### PR-B8-CMD: CMD 重构（~3.5h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-28 | cmd/gocell | **CMD-MODE-01**: validate/scaffold 等子命令无 fail-fast 模式（遇首个错误不停止） | v1.0 | 2h | `cmd/gocell/` | 6B review |
| B8-29 | cmd/gocell | **CMD-REFACTOR-01**: app 包提取（cmd 与 app 逻辑混合） | v1.0 | 1.5h | `cmd/gocell/` | 6B review |

### PR-B8-BATCH: 批量操作（~1d）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-30 | pkg | **WM-7**: 泛型 BulkResult + 部分成功语义 | v1.0 | 1d | `pkg/httputil/` 或 `pkg/bulk/` | WM-7 |

### PR-B8-ROUTER: Router 信任边界收敛（~3h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-31 | runtime/http | **RTR-PUBLIC-POLICY-01**: router 把 public endpoint 策略分散为三套独立入口（WithAuthMiddleware + WithTracingOptions + WithRequestIDOptions），需收敛为单一 `WithPublicEndpoints` 组合选项。bootstrap L584-596 改为调用 router 组合选项 | v1.0 | 3h | `runtime/http/router/router.go` + `runtime/bootstrap/bootstrap.go` + tests | PR#131 review |

### PR-B8-CFG: Config 治理（~2h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-32 | runtime/config | **CFG-KEYFILTER-WIRE-01**: KeyFilter bootstrap 接线，cell 通知循环使用 `KeyFilter.Matches()` 选择性通知，需产品确认语义 | v1.0 | 1h | `runtime/bootstrap/bootstrap.go` | PR#132 review |
| B8-33 | runtime/config | **CFG-ERRCODE-01**: runtime/config 包 `fmt.Errorf` 评估是否迁移 errcode — 仅在 config 错误需面向用户输出时迁移 | v1.0 | 1h | `runtime/config/watcher.go` + `config.go` | PR#132 review |

### PR-B8-PR133: PR#133 review 残留（~3h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-34 | runtime/http | **F1-ARCH-03**: router 层 `WithSecurityHeadersOptions` 接线测试 | v1.0 | 1h | `runtime/http/router/router_test.go` | PR#133 review |
| B8-35 | runtime/bootstrap | **F2-SEC-03**: bootstrap 信任边界测试补 `traceparent` 注入向量 | v1.0 | 1h | `runtime/bootstrap/bootstrap_test.go` | PR#133 review |
| B8-36 | cells | **F3-TEST-01**: converter 函数 nil 指针输入测试/文档 | v1.0 | 0.5h | 各 `handler_test.go` | PR#133 review |
| B8-37 | runtime/bootstrap | **F4-OPS-01**: `bootstrap.WithSecurityHeadersOptions` 便利包装 | v1.0 | 0.5h | `runtime/bootstrap/bootstrap.go` | PR#133 review |

### PR-B8-SERIAL: 序列化边界收敛（~3h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-38 | cells | **EVENT-PAYLOAD-TYPED-01**: sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct（对齐 cell-patterns.md 北极星）。含 snake_case → camelCase 统一 | v1.0 | 3h | 6 个 `service.go` + event contract schemas | PR#133 re-review |

### PR-B8-POOL: PoolStats DX（~1.5h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-39 | adapters | **POOLSTATS-IFACE-01**: 三个 adapter PoolStats 无公共接口，OTel collector 需 per-adapter switch | OTel 需求明确 | 1h | `adapters/postgres/pool.go` + `adapters/redis/client.go` + `adapters/rabbitmq/connection.go` | PR#134 review |
| B8-40 | adapters | **POOLSTATS-JSON-01**: PoolStats struct 缺 `json:"camelCase"` tags | `/debug/poolstats` 端点需求 | 0.5h | 同上 | PR#134 review |

### PR-B8-READYZ: Readyz 安全（~2h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-41 | runtime/http/health | **READYZ-VERBOSE-TOKEN-01**: `/readyz?verbose` 匿名暴露内部拓扑（cell 名、dependency 名、adapter info）。PR#135 新增 adapterInfo 扩大暴露面。需增加 `WithVerboseToken` bootstrap 选项或管理端口隔离 | v1.0 | 2h | `runtime/http/health/health.go` + `runtime/bootstrap/bootstrap.go` | PR#134 review + PR#137-138 集成审查 P1 |

### PR-B8-REGISTRY: Registry 健壮性（~1.5h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-42 | kernel/registry | **REGISTRY-CONSUMERS-UNKNOWN-KIND-01**: `contract.go:112` `Consumers()` 对不识别的 contract kind 静默 `return nil`，应改为 allowlist + error return（对齐 `cell.ParseLevel` 模式）。影响 kernel 接口签名 | v1.0 | 1.5h | `kernel/registry/contract.go` | PR#135 review |

### PR-B8-LINT: 契约治理工具（~2h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-43 | kernel/governance | **138b CONTRACT-LIST-LINT-01**: `gocell check contract-health` 增加 list 响应格式检查：response schema 含 `data: array` 时必须包含 `hasMore` | v1.0 | 2h | `kernel/governance/` | PR#138 review |

### PR-B8-QUICKFIX: 快修合集（~9h）

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-44 | docs | **#26**: `.env.example` 补 `GOCELL_S3_REGION=us-east-1` | v1.0 | 0.5h | `.env.example` | 6B |
| B8-45 | CI | **#27 CONTRACT-CI**: order-cell/device-cell contract YAML CI 未校验 | v1.0 | 1h | `.github/workflows/ci.yml` | 6B |
| B8-46 | cmd | **F-7 BUILD-OUTDIR-01**: 统一 `go build -o bin/` 输出目录 | v1.0 | 0.5h | `Makefile` 或 build scripts | 6B |
| B8-47 | kernel/cell | **#17 WM17-F2-2 + F4-3**: Hook 增强 ctx 超时 + Prometheus metrics via HookObserver 接口 | v1.0 | 3h | `kernel/cell/` | WM-17 |
| B8-48 | runtime/resilience | **#18 CB-IFACE-01 + CB-ENCAP-01**: circuit breaker Allow/Report 拆分 + 消除 gobreaker import | v1.0 | 2h | `runtime/resilience/circuitbreaker/` | 6B |
| B8-49 | kernel/journey | **#21 F-5**: Journey catalog 不校验引用 | v1.0 | 1h | `kernel/journey/catalog.go` | 6B |

### 历史残留（已验证状态）

| # | 模块 | 问题详情 | 状态 | 来源 |
|---|------|---------|------|------|
| OB-02 | runtime/http | broken logger 注入测试 | **未修** — safe_observe_test.go 无 logger 注入测试 | 历史 backlog 0-J |
| HR-01 | runtime/http | trustedProxies 产品决策 | **已修** — WithTrustedProxies 已实现 | 历史 backlog 0-K |
| HR-02 | runtime/http | route pattern 元数据 metrics label 基数 | **已修** — metrics 用 `RoutePatternFromCtx()` | 历史 backlog 0-K |
| HR-03 | runtime/http | chi/middleware.RequestID 替换 | **废弃** — PR#131 新增信任边界功能，替换即安全回退 | 历史 backlog 0-K |
| HR-04 | runtime/http | tracing 官方能力 | **已修** — WithTracer 有完整 godoc | 历史 backlog 0-K |
| SF-01 | pkg/httputil | DecodeJSONStrict | **已修** — 函数已实现 | 历史 backlog 0-H |
| R1C2-F01 | runtime/eventbus | Close()+Subscribe() 竞态 | **已修** — mu.Lock 保护 close | 历史 backlog Tier 2 |
| R1C2-F03 | runtime/worker | WorkerGroup.Start 首个失败不取消 | **已修** — cancel() 调用到位 | 历史 backlog Tier 2 |
| P4-TD-01 | kernel/outbox | 共享 NoopOutboxWriter | **已修** — `outbox.NoopWriter{}` PR#136 | 历史 backlog |
| P4-TD-03 | runtime/auth | IssueTestToken HS256 死代码 | **已修** — HS256 rejection test + jwt.go 拒绝 | 历史 backlog |
| P4-TD-04 | cells/order-cell | L2 无 outboxWriter enforce | **已修** — PR#135 CheckNotNoop | 历史 backlog |
| WM-12 | tools/archtest | archtest 边界守护 | **已实现** — 17.7KB 测试 | WM-12 |

**未修历史残留需登记:**

| # | 模块 | 问题详情 | 前置 | 工时 | 涉及文件 | 来源 |
|---|------|---------|------|------|---------|------|
| B8-50 | runtime/http | **OB-02**: safe_observe broken logger 注入测试 — 有 panic 测试但缺 broken slog.Handler 注入场景 | v1.0 | 1h | `runtime/http/middleware/safe_observe_test.go` | 历史 backlog 0-J |

---

## 阶段 4: 触发条件项（条件满足时做）

| # | 模块 | 问题详情 | 工时 | 触发条件 | 来源 |
|---|------|---------|------|----------|------|
| TC-1 | runtime/bootstrap | **28c AUTH-PROVIDER-EXPORT-01**: `authProvider` 接口 unexported，与 `HTTPRegistrar`/`EventRegistrar` (exported) 不一致。kernel→runtime/auth 层依赖限制无法直接移动 | 1h | 第二个 auth provider cell | PR#127 review |
| TC-2 | runtime/auth | **28g AUTH-ISSUE-OPTIONS-01**: `JWTIssuer.Issue()` 4 参数，重构为 `IssueOptions` struct | 1h | Issue() 第 5 个参数 | PR#127 review |
| TC-3 | cells/device-cell | **28h DEVICE-ENQUEUE-RBAC**: HandleEnqueue 无设备维度鉴权 — 当前为 operator 管理端点。PR#138 已加 `RequireSelfOrRole`，但资源级（按 deviceId 鉴权）未实现 | 2h | 多租户 operator | PR#125 review |

---

## 阶段 5: v1.1+ 长期规划

### 契约增强

| # | 模块 | 问题详情 | 工时 | 来源 |
|---|------|---------|------|------|
| V11-1 | cmd/gocell | **CONTRACT-BREAKING-01**: `gocell check contract-breaking` schema 破坏性变更检测，学 buf breaking 40+ 规则 | 4h | 架构设计 |
| V11-2 | cmd/gocell | **CONTRACT-CODEGEN-01**: Go DTO struct tags → JSON Schema 自动生成，学 oapi-codegen | 1d | 架构设计 |
| V11-3 | cmd/gocell | **CONTRACT-STUB-01**: 消费方 contract stub 测试，学 Spring Cloud Contract WireMock 模式 | 1d | 架构设计 |

### metadata 校验规则

| # | 模块 | 问题详情 | 工时 | 来源 |
|---|------|---------|------|------|
| V11-4 | kernel/governance | **G-1 FMT-11**: 动态状态字段禁入非 status-board 文件 | 2h | metadata-model-v3 |
| V11-5 | kernel/governance | **G-4**: deprecated contract 引用阻断（当前仅 warning） | 1h | metadata-model-v3 |
| V11-6 | kernel/governance | **G-6**: Assembly boundary.yaml 存在性校验 | 0.5h | metadata-model-v3 |

### Kernel 子模块

| # | 模块 | 问题详情 | 优先级 | 来源 |
|---|------|---------|--------|------|
| V11-7 | kernel/wrapper | traced sync/event/command wrapper — 契约级可观测 | P1 | master-plan |
| V11-8 | kernel/command | 命令队列接口 — iot-device L4 无框架支持 | P1 | master-plan |
| V11-9 | kernel/webhook | receiver + dispatcher | P2 | master-plan |
| V11-10 | kernel/reconcile | 最终状态收敛 | P2 | master-plan |
| V11-11 | runtime/scheduler | cron/定时任务 | P2 | master-plan |

### adapters 分层重整

| # | 模块 | 问题详情 | 工时 | 来源 |
|---|------|---------|------|------|
| V11-12 | adapters/postgres | **AL-01**: outbox_relay.go 轮询调度逻辑拆到 runtime/ | 2h | 依赖替换分析 |
| V11-13 | adapters/redis | **AL-02**: distlock.go 续期/TTL 策略拆到 runtime/ | 2h | 依赖替换分析 |

### 架构风险

| # | 模块 | 问题详情 | 来源 |
|---|------|---------|------|
| V11-14 | kernel/cell | Cell 接口 11 个方法 — 考虑拆分 Cell + CellLifecycle + CellMetadata | Tier 3 review |
| V11-15 | kernel/cell | CS-AR-2: Dependencies 暴露完整 Cell 图 | Tier 3 review |
| V11-16 | kernel/cell | CS-AR-3: kernel/cell 依赖 net/http + outbox | Tier 3 review |

---

## 执行顺序总览

```
Wave 3 完成后:

并行组 1（v1.0 阻塞）:
  PR-H1 安全加固 (5h)  ──→ PR-H2 契约补全 (3h) ──→ PR-README (4h) ──→ Wave 4 Review
  
并行组 2（SHOULD，独立）:
  PR-EF pkg+CI+治理 (8.5h)
  PR-TPUB #6 TestPubSub (4h)

v1.0 tag 后:
  Batch 8 按 PR 组独立执行（~62h）

v1.1+:
  长期规划按优先级排序
```
