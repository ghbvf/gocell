# GoCell Backlog

> Phase 0-4 已完成并合并到 develop。本文档汇总全部待办事项。
> 更新日期: 2026-04-11（同步至 PR#68）

---

## Tier 0: Review 修复 + 依赖替换（进行中）

> PR#37 (postgres) ✅ PR#38 (rabbitmq) ✅ PR#39 (redis) ✅ PR#40 (dep-cleanup) ✅ PR#41 (dep-sdk-replace) ✅ PR#42 (dep-migrator-otel) ✅ 已合并
> PR#43 (websocket-split) ✅ PR#49 (mapCodeToStatus fix) ✅ PR#50 (http-1-envelope) ✅ PR#51 (http-2-observability) ✅ PR#52 (http-3-cells-dechi) ✅ 已合并
> PR#53 (panic-observability) ✅ PR#54 (DecodeJSON) ✅ PR#55 (RouteMux.Use) ✅ PR#56 (http-3 集成) ✅ 已合并
> PR#59 (kernel C1 batch) ✅ PR#60 (kernel C2 batch) ✅ PR#61 (kernel/verify 重构) ✅ PR#62 (verify alignment) ✅ 已合并
> PR#63 (audit-core L2→L3 + errcode) ✅ PR#64 (GOV-5/GOV-6) ✅ PR#65 (strict YAML) ✅ PR#66 (CI fix) ✅ PR#67 (G-7 auto-derive) ✅ 已合并
> PR#68 (0-F Solution B 接口) 🔄 进行中

### 0-A: 依赖替换 Phase 0 — 安全风险（1d）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| D-01 | `adapters/s3`: aws-sdk-go-v2 薄 adapter（Config + Upload + Health + SDK 逃生口） | 0.5d | ✅ PR#41 |
| D-02 | `adapters/oidc`: go-oidc v3 薄 adapter（Config + Provider/Refresh，暴露原生类型） | 0.5d | ✅ PR#41 |
| D-03 | `adapters/redis/distlock`: 删除 FenceToken（零调用者） | 0.5h | ✅ PR#40 |

### 0-B: Outbox Relay Plan A（0.5d）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| R-01 | `pollOnce()` markQuery 失败 fail-fast | 2h | ✅ PR#46 |
| R-02 | Start/Stop handshake (`startedCh`) | 2h | ✅ PR#46 |

> 来源: `docs/reviews/202604061401-pr39-six-role/PR39-postgres-outbox-followup.md`

### 0-B2: Outbox Relay 三阶段重写（1.5d）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| RL-01 | migration `003_outbox_status_columns.sql`（status/attempts/next_retry_at/claimed_at） | 0.5h | TODO |
| RL-02 | `RelayConfig` 新增 MaxAttempts / BaseRetryDelay / ClaimTTL | 0.5h | TODO |
| RL-03 | 重写 `pollOnce` 三阶段（claim → publish → writeBack） | 2h | TODO |
| RL-04 | `reclaimStale` 加入 cleanupLoop（超时 claiming → pending） | 0.5h | TODO |
| RL-05 | `OutboxWriter.Write` 显式写 `status = 'pending'` | 0.5h | TODO |
| RL-06 | relay 状态机 enum（替换 bool running + startedCh） | 1h | TODO |
| RL-07 | slog 指标 + `outbox.Entry.Attempts` 字段 | 0.5h | TODO |
| RL-08 | 测试覆盖（8 个场景） | 2h | TODO |

> 设计文档: `docs/reviews/202604072154-outbox-relay-three-phase-plan.md`

### 0-C: 依赖替换 Phase 1 — 快速收益（1d）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| D-04 | `pkg/uid`: google/uuid 替换手写 UUIDv4（18 调用点） | 0.5d | ✅ PR#40 |
| D-05 | shutdown/bootstrap: errors.Join 替换 firstErr（2 文件 6 行） | 0.5h | ✅ PR#40 |
| D-06 | middleware: chi/middleware 替换 recovery/requestID/realIP（删 ~200 行） | 0.5d | WONTFIX — GoCell 需要结构化 JSON 错误体、自定义 UUID 注入、trusted proxy 校验，chi/middleware 不满足 |

### 0-D: RabbitMQ Solution B（2-3d）— 已展开为 0-F + 0-G

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| S-01 | `outbox.Subscriber` handler 返回 HandleResult{Disposition, Receipt} | 0.5d | 🔄 PR#68 (= A3-01) |
| S-02 | `idempotency.Checker` 升级为 Claim/Commit/Release | 0.5d | 🔄 PR#68 (= A3-02/A3-03) |
| S-03 | `ConsumerBase` 去掉应用侧 DLQ，返回 Disposition | 0.5d | 🔄 PR#68 (= A3-04) |
| S-04 | `Subscriber.processDelivery` 按 Disposition 做 Ack/Nack/Requeue | 0.5d | 🔄 PR#68 (= B-01) |
| S-05 | 重连策略完善 + backoff | 0.5d | TODO (= B-03) |
| S-06 | 测试覆盖（幂等时序、setup 重连、集成） | 0.5d | 🔄 PR#68 (= A3-07/B-04) |

> 来源: `docs/reviews/202604061449-pr38-solution-b-report.md`

### 0-E: 依赖替换 Phase 2（2d）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| D-07 | `adapters/postgres/migrator`: pressly/goose v3 替换（删 ~418 行） | 1d | ✅ PR#42 |
| D-08 | 新建 `adapters/otel` + `adapters/prometheus`（OTel + Prometheus） | 1d | ✅ PR#42 |

### 0-F: Solution B 接口 + 基础设施 — PR-A3（1.5d）— 🔄 PR#68 进行中

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| A3-01 | `kernel/outbox`: +Disposition, +Receipt, +HandleResult, +EntryHandler, +WrapLegacyHandler；~Subscriber, ~TopicHandlerMiddleware 签名变更 | 2h | 🔄 PR#68 |
| A3-02 | `kernel/idempotency`: +ClaimState, +Claimer；标记旧 Checker Deprecated | 1h | 🔄 PR#68 |
| A3-03 | `adapters/redis/idempotency.go`: IdempotencyClaimer 双 key Lua（lease:{k} + done:{k}） | 3h | 🔄 PR#68 |
| A3-04 | `runtime/eventbus/consumer.go`: ConsumerMiddleware 从 rabbitmq/consumer_base.go 迁入+重写 | 2h | 🔄 PR#68 |
| A3-05 | `runtime/eventbus/eventbus.go`: InMemoryEventBus 适配新 EntryHandler 签名 | 1h | 🔄 PR#68 |
| A3-06 | Cell handler 迁移（configsubscribe, auditappend 等返回 HandleResult via WrapLegacyHandler） | 1h | 🔄 PR#68 |
| A3-07 | 测试更新（mock 适配 + 新接口覆盖） | 2h | 🔄 PR#68 |

> 设计来源: `docs/reviews/202604061449-pr38-solution-b-report.md`
> 依赖: PR#44 ✅, PR#45 ✅

### 0-G: RabbitMQ Subscriber 重写 — PR-B（1.5d）— 部分随 PR#68 实现

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| B-01 | `subscriber.go`: processDelivery 读 HandleResult → Ack/Nack/Requeue + Receipt Commit/Release | 3h | 🔄 PR#68 |
| B-02 | 删除 `consumer_base.go`（已迁到 runtime/eventbus/consumer.go） | 0.5h | 🔄 PR#68 |
| B-03 | `connection.go`: setup 错误分类（recoverable vs permanent）+ anti-hot-loop backoff | 2h | TODO |
| B-04 | 测试覆盖（processDelivery 重写 + receipt 行为 + 集成） | 3h | 🔄 PR#68 |

> 依赖: 0-F (PR-A3) 必须先合并；B-01/B-02/B-04 已随 PR#68 一并实现

### 0-I: HTTP decode 兼容回归测试补强（0.5d）

> 来源: PR#56 / PR#57 复审 follow-up（2026-04-09 ~ 2026-04-11）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| HT-01 | `cells/order-cell/slices/order-create`, `cells/device-cell/slices/device-register`, `cells/device-cell/slices/device-command`: handler 级显式回归测试，锁定 malformed JSON / empty body / type mismatch 的错误 code 兼容性 | 2h | TODO |
| HT-02 | `pkg/httputil/response_test.go`: 补 `WriteDecodeError` 显式测试，覆盖 `ErrValidationFailed` → 400 / `ERR_VALIDATION_FAILED`、`ErrBodyTooLarge` → 413 / `ERR_BODY_TOO_LARGE`、`ErrInternal` → 500，以及 plain error fallback | 1h | TODO |

### 0-J: Observability panic guard follow-up（0.5d）

> 来源: PR#57 六角色复审（2026-04-11）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| OB-01 | `runtime/http/middleware/safe_observe.go`: recover 分支避免再次调用会 panic 的默认 logger；broken slog handler 场景下也必须吞掉 panic | 1h | ✅ recover + slog.Error 防御已实现 |
| OB-02 | `runtime/http/middleware/safe_observe_test.go` + `access_log_test.go`: 注入 panicking slog handler，补 `Recorder(AccessLog(...))` / `Metrics(...)` 集成测试，锁定 broken logger 路径 | 1h | 部分完成（有 panic 测试，缺 broken logger 注入） |
| OB-03 | `runtime/http/middleware/tracing.go`: 明确并修复 `Recorder -> Tracing -> Recovery` panic 链路的 `http.status_code` 记录语义；若不打算记录 500，则回退误导性注释并补测试 | 1h | ✅ status_code 语义已明确 |

### 0-K: HTTP/router 产品化收尾（1-1.5d）

> 来源: 2026-04-11 基于 `origin/develop` 复核 HTTP/router 现状；最新提交 `#65` / `#66` 未触及该区域，以下结论仍成立

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| HR-01 | `runtime/http/middleware/real_ip.go`, `runtime/http/router/router.go`, `runtime/bootstrap`: 对 `RealIP / trustedProxies` 做明确产品决策；若保留，补 `WithTrustedProxies` + bootstrap/config wiring + 测试；若不保留，移除默认链中的“框架级承诺”并补文档 | 2h | TODO |
| HR-02 | `runtime/http/router` + `middleware/{metrics,tracing,access_log}.go`: 提供统一 route pattern/route template 元数据，避免直接使用 `r.URL.Path` 造成 metrics label 基数爆炸；tracing/access log 复用同一语义 | 2h | TODO |
| HR-03 | `runtime/http/middleware/request_id.go`: 用 `chi/middleware.RequestID` + bridge 替换自研 UUID 生成，仅保留 `ctxkeys.RequestID` / `X-Request-Id` 兼容层 | 1h | TODO |
| HR-04 | `runtime/http/router` + `runtime/bootstrap`: 明确 tracing 是否作为框架官方能力暴露；若要支持，补 `WithTracer(...)` 及 wiring；若暂不支持，补文档并避免给出“已产品化”印象 | 1h | TODO |

### 执行顺序

```
已完成:
  0-A ✅ → 0-B ✅ (PR#46) → 0-C ✅ → 0-E ✅
  PR#44 (QueryBuilder) ✅ → PR#45 (TxRunner) ✅
  PR#49 (mapCodeToStatus fix) ✅ → PR#50 (http-1-envelope) ✅
  PR#51 (http-2-observability) ✅ → PR#52 (http-3-cells-dechi) ✅
  PR#53 (panic-observability) ✅ → PR#54 (DecodeJSON) ✅ → PR#55 (RouteMux.Use) ✅
  PR#56 (http-3 集成) ✅ → PR#59-64 (kernel 止血 6 个 PR) ✅
  PR#65 (strict YAML) ✅ → PR#66 (CI fix) ✅ → PR#67 (G-7 auto-derive) ✅
  0-J: OB-01 ✅, OB-03 ✅

进行中:
  PR#68 (0-F + 0-G 部分: Solution B 接口 + Subscriber 重写) 🔄

剩余（可并行）:
  0-B2 (Relay 三阶段重写)
  0-G B-03 (connection.go 重连 backoff) — 等 PR#68 合并
  0-H (DecodeJSONStrict)
  0-I (HTTP decode 兼容回归测试)
  0-J OB-02 (broken logger 注入测试)
  0-K (HTTP/router 产品化收尾) — 需产品决策
  → 继续 Tier 1 (R1E → R2)
```

> 完整分析: `docs/reviews/202604061630-dependency-replacement-plan.md`
> 路线图: `docs/reviews/202604061530-post-pr38-roadmap.md`

---

## Tier 1: 全量代码 Review（3-5 天）

### 目标
对 200 文件 / 18,840 行代码做跨 Phase 集成 review，产出依赖图和模块级 findings。

> 执行计划: `docs/reviews/202604060739-review-plan/202604060830-001-review-plan.md`

### 进度

| 层 | 状态 |
|---|------|
| R1A pkg | ✅ 已审 |
| R1B kernel | ✅ 已审 + 六角色深审完成（40+ findings，见 `tools/docs/reviews/2026-04-09-*`） |
| R1C runtime | ✅ 已审 + 已修 |
| R1D-1 postgres | ✅ 已审 + 已修 (PR#37) |
| R1D-2 redis | ✅ 已审 + 已修 (PR#39) |
| R1D-3 rabbitmq | ✅ 已审 + 已修 (PR#38) |
| R1D-4 oidc | ✅ 已审（6 份 review 文档，见 `docs/reviews/archive/202604060830-R0/`） |
| R1D-5 s3 | ✅ 已审（6 份 review 文档，见 `docs/reviews/archive/202604060830-R0/`） |
| R1D-6 websocket | ✅ 已审 + 已修 (PR#43) — 6 条 tech debt 记入 WS-* |
| R1E cells | 待审 |
| R1F+G delivery + YAML | 待审 |
| R2 数据流合并 | 待审 |
| R3-R5 PR追溯/集成/裁决 | 待审 |

### 任务

| # | 任务 | 预估 |
|---|------|------|
| T1-1 | 生成模块依赖图（`go list -json ./...` → DOT/SVG） | 2h |
| T1-2 | Review kernel/（11 包，4,429 行）— 接口稳定性、coverage 交叉验证 | 4h |
| T1-3 | Review cells/（6 cell，5,811 行）— 聚合边界、errcode 一致性 | 4h |
| T1-4 | Review runtime/（8 包，2,835 行）— 生命周期、中间件完整性 | 3h |
| T1-5 | Review adapters/（6 包，4,185 行）— 接口实现合规、集成测试 | 3h |
| T1-6 | Review examples/（3 项目，233 行）— 教学质量、可运行性 | 2h |
| T1-7 | CI 加 golangci-lint / staticcheck | 2h |
| T1-8 | 产出 review 报告 + 汇总 findings | 2h |

---

## Tier 2: Review 产出的修复 + Tech-Debt 清理

> 规模：R1A-R1D 旧有 35 条 + Kernel 六角色深审 19 条 = 54 条总计。已修 40+，**剩余 ~14 条未修**（含 PR#68 进行中的）

### Review Findings 汇总

> Review 原则：P0 当层修，P1/P2 记录到此处留 Fix Pack。
> R1A-R1D 来源报告已归档至 `docs/reviews/archive/`。
> Kernel 六角色深审报告：`tools/docs/reviews/2026-04-09-*`（4 组，覆盖全部 kernel 子模块）。

#### P1 — kernel 旧有（~~8~~ 1 条未修，6 条已修，1 条 PR#68 中）

| ID | 文件 | 问题 | 状态 |
|----|------|------|------|
| R1B1-01 | `kernel/cell/base.go` | Add*/collection accessor 无 mutex | ✅ 已修（Add 方法已有 Lock/Unlock） |
| R1B1-02 | `kernel/cell/base.go` | 读可变字段无锁 | ✅ 已修（读 accessor 已有 Lock，RWMutex 降级为 P2） |
| F-OB-02 | `kernel/outbox/outbox.go` | Entry 无显式 Topic 字段 | ✅ 已修（Topic + RoutingTopic 回退） |
| F-ID-01 | `kernel/idempotency/idempotency.go` | IsProcessed+MarkProcessed 旧接口未废弃 | 🔄 PR#68（已标记 Deprecated） |
| G-01 | `kernel/governance/rules_fmt.go` | owner.team/role, verify.smoke 必填校验 | ✅ validateFMT11 已实现 |
| G-02 | `kernel/governance/rules_verify.go` | verify.unit 未校验为必填 | ✅ validateFMT12 已实现 |
| F-5 | `kernel/journey/catalog.go` | Journey catalog 不校验引用 | TODO |
| F-2 | `kernel/assembly/assembly.go` | Start()/StartWithConfig() 重复代码 | ✅ 已重构为 startInternal |

#### P1 — kernel 六角色深审新发现（~~12~~ 0 条未修，全部已修）

> 来源：`tools/docs/reviews/2026-04-09-*`（4 组六角色审查）
> 全部修复于 PR#59-64。验证日期: 2026-04-11。

| ID | 问题 | 状态 |
|----|------|------|
| ASJ-2 | scaffold 路径穿越 | ✅ PR#59 validatePathComponent |
| CS-F1 | VerifySlice 不消费 metadata | ✅ PR#61 kernel/verify 重构 |
| CS-F2 | VerifyCell 硬编码 -run Smoke | ✅ PR#61 + PR#62 smoke stubs |
| CS-F3 | 零匹配测试当成功 | ✅ PR#61 ZeroMatch 检测 |
| CS-F4 | manual criteria 不参与判定 | ✅ PR#61 ManualPending |
| GOV-1 | TOPO-04 用 ownerCell 做约束 | ✅ PR#60 contractProvider |
| GOV-2 | DEP-03 空 assembly 绕过 | ✅ PR#59 |
| GOV-3 | active contract 无 provider | ✅ PR#60 VERIFY-04 |
| MR-R01 | belongsToCell 路径不一致 | ✅ PR#59 |
| MR-R02 | ownerCell 未推导 | ✅ PR#60 inferContractOwner |
| MR-R03 | integration test 硬编码 | ✅ PR#59 GreaterOrEqual |
| ASJ-1 | journey id 缺 J- 前缀 | ✅ PR#59 |

#### P2 — kernel 六角色深审新发现（~~7~~ 0 条未修，全部已修）

| ID | 问题 | 状态 |
|----|------|------|
| CS-F5 | BaseSlice 可变切片暴露 | ✅ PR#59 defensive copy |
| MR-R05 | registry 可变指针 | ✅ PR#60 deepCopy |
| ASJ-3 | Register(nil) panic | ✅ PR#59 nil guard |
| ASJ-4 | Catalog 可变指针 | ✅ PR#59 copyJourneyMeta |
| ASJ-5 | fingerprint 缺 contract | ✅ PR#59 boundary hash |
| GOV-5 | verify 格式无校验 | ✅ PR#64 VERIFY-05 |
| GOV-6 | select-targets 缺 L0 | ✅ PR#64 expandL0Dependents |

#### P1 — pkg（~~3~~ 0 条，全部已修）

| ID | 文件 | 问题 | 状态 |
|----|------|------|------|
| R1A1-F02 | `pkg/httputil/response.go` | mapCodeToStatus 子串匹配漏掉 ERR_AUTH_TOKEN_EXPIRED | ✅ PR#49 + PR#54 |
| R1A1-F03 | `pkg/httputil/response.go` | mapCodeToStatus 子串调度脆弱，顺序依赖 | ✅ PR#54 explicit mapping |
| R1A1-F04 | `pkg/httputil/response.go` | json.NewEncoder 错误被静默丢弃 | ✅ PR#50 slog.Error |

#### P1 — runtime（~~6~~ 2 条未修，1 条已修）

| ID | 文件 | 问题 | 状态 |
|----|------|------|------|
| F-01 | `runtime/auth/jwt.go` | ErrAuthUnauthorized 重复定义 | ✅ PR#50 errcode 统一 |
| F-02 | `runtime/auth/keys.go` | 无 RSA 最小 key size 校验 | ✅ MinRSAKeyBits=2048 已实现 |
| F-03 | `runtime/auth/keys.go` | LoadRSAKeyPairFromPEM 裸 fmt.Errorf | ✅ 已改用 errcode |
| R1C2-F01 | `runtime/eventbus/eventbus.go` | Close()+Subscribe() 竞态，channel read after close | TODO |
| R1C2-F02 | `runtime/eventbus/eventbus.go` | Subscribe 退出时 subs map 泄漏 stale channel | ✅ Unsubscribe() 正确清理 |
| R1C2-F03 | `runtime/worker/worker.go` | WorkerGroup.Start 首个失败不取消其余 worker | TODO |

#### P2 — kernel 旧有（~~7~~ 1 条未修，1 条 PR#68 中）

| ID | 文件 | 问题 | 状态 |
|----|------|------|------|
| R1B1-03 | `kernel/cell/base.go` | Init 不重置 shutdownCtx/Cancel | ✅ PR#59 |
| R1B1-04 | `kernel/cell/base.go` | sync.Mutex → sync.RWMutex | ✅ PR#59 |
| F-OB-01 | `kernel/outbox/outbox.go` | 无批量写支持 | TODO（随 0-F） |
| F-OB-03 | `kernel/outbox/outbox.go` | Entry 必填校验 | 🔄 PR#68（Entry.Validate() 已实现） |
| F-META-01 | `kernel/metadata/parser.go` | 未知 YAML 字段静默忽略 | ✅ PR#65 KnownFields(true) |
| F-META-02 | `cells/audit-core` | L2/L3 不一致 | ✅ PR#63 |
| F-3 | `kernel/assembly/assembly.go` | Stop() errors.Join | ✅ PR#59 |
| F-4 | `kernel/scaffold/templates.go` | 包注释冲突 | ✅ 已修 |

**附加架构风险（来自六角色深审）：**

| ID | 文件 | 问题 | 状态 |
|----|------|------|------|
| CS-AR-1 | `kernel/cell/base.go` | Start(ctx) 忽略传入 ctx | ✅ PR#59 context.WithoutCancel(ctx) |
| CS-AR-2 | `kernel/cell/interfaces.go` | Dependencies 暴露完整 Cell 图 | TODO（Tier 3） |
| CS-AR-3 | `kernel/cell/registrar.go` | kernel/cell 依赖 net/http + outbox | TODO（Tier 3） |

**PR#62-64 发现的新条目：**

| ID | 问题 | 状态 |
|----|------|------|
| F-HTTP-MAP-01 | ErrCheckRefInvalid/ErrZeroTestMatch 缺 HTTP 映射 | ✅ PR#63 |

#### P2 — pkg + runtime（~~4~~ 2 条未修，2 条已修）

| ID | 文件 | 问题 | 状态 |
|----|------|------|------|
| F-HTTP-MAP-01 | `pkg/httputil/response.go` | ERR_CHECKREF_INVALID/ERR_ZERO_TEST_MATCH 缺 HTTP 映射 | ✅ PR#63 |
| R1A1-F05 | `pkg/id/` | ~~已废弃包仍存在~~ | ✅ 已删除（PR#40） |
| R1A1-F06 | `pkg/ctxkeys/keys_test.go` | TestFromMissingKey 遗漏覆盖 | ✅ PR#50 |
| R1A1-F08 | `adapters/redis/client.go:16` | ErrAdapterRedisLockAcquire 常量名/值不一致 | ✅ 已修 |
| F-04 | `runtime/auth/middleware.go` | writeAuthError 忽略 encode 错误 | ✅ PR#50 |
| R1C2-F04 | `runtime/worker/periodic.go` | PeriodicWorker 缺编译时接口检查 | ✅ `var _ Worker = (*PeriodicWorker)(nil)` |
| R1C2-F05 | `runtime/worker/periodic.go` | PeriodicWorker.Stop double-Start 问题 | ✅ 支持重启，每次 Start 创建新 done channel |
| PROM-01 | `runtime/http/middleware/metrics.go` | metrics path label 基数爆炸（待 0-K / HR-02 用统一 route pattern 语义收口） | TODO |
| TX-NIL-01 | `cells/*/service.go` | txRunner nil-safe 未文档化 | TODO |

### 0-H: DecodeJSON 严格模式 — DisallowUnknownFields opt-in（0.5d）

> 来源: PR#54 review 讨论（2026-04-09）
> 架构师意见: 校验策略属于 handler 层决策，不应在 pkg/ 基础设施强制；需独立 PR + migration guide
> 产品意见: 内部项目 Fail Fast 收益大于成本；保留严格模式但需明确列出未知字段名

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| SF-01 | `DecodeJSONStrict` 启用 DisallowUnknownFields，复用 classifyDecodeError（含 unknown field 分支） | 1h | TODO |
| SF-02 | handler 逐个从 `DecodeJSON` 切到 `DecodeJSONStrict`（10 个 struct 目标） | 1h | TODO |
| SF-03 | `WriteDecodeError` 适配：严格模式返回 ERR_VALIDATION_FAILED + unknown field details，宽松模式保持 ERR_VALIDATION_REQUIRED_FIELD | 0.5h | TODO |
| SF-04 | CHANGELOG / API 文档标注 breaking change | 0.5h | TODO |

### 历史 Tech-Debt（合并保留）

#### P1（5 条）

| ID | 来源 | 问题 | 预估 |
|----|------|------|------|
| P4-TD-03 | S6 P1-8 | `IssueTestToken` HS256 死代码（测试陷阱） | 30min |
| P4-TD-04 | S6 P2-1 | order-cell 声明 L2 但无 outboxWriter enforce | 1h |
| P4-TD-05 | S6 INT-1 | 缺少 outbox 全链路 3-container 集成测试 | 2h |
| P3-TD-10 | Phase 2 #54 | Session refresh TOCTOU 竞态 | 4h（高风险） |
| P2-T-02 | Phase 2 | J-audit-login-trail e2e 测试 | 2h |

#### P2（7 条）

| ID | 来源 | 问题 | 预估 |
|----|------|------|------|
| P4-TD-01 | S6 P2-5 | 缺少共享 NoopOutboxWriter | 30min |
| P4-TD-02 | S6 P2-3 | ~~chi.URLParam 耦合（10 个文件）~~ | ✅ PR#52 PathValue 迁移 |
| P4-TD-09 | Tier0 F-06 | List 端点缺分页 | 2h |
| P4-TD-10 | Tier0 F-07 | POST 201 响应未包装 `{"data":...}` | 2h |
| P4-TD-11 | Tier0 F-14 | in-memory repository 缺并发测试 | 1h |
| P3-TD-11 | Phase 2 #56-59 | access-core domain 模型重构 | 4h（高风险） |
| P3-TD-12 | Phase 2 #62 | configpublish.Rollback version 校验 | 2h |
| P4-TD-12 | PR#62 | demo cell `TestDemo_Startup` 是 `t.Skip` 占位，不验证 Init/Start 行为；待 demo cell 实现后补真实 smoke 测试 | 30min |

---

## Tier 3: 核心能力完善 — v1.1（持续）

### metadata-model-v3 校验规则补全

来源: KG 分析 + 六角色深审，对照 `docs/architecture/metadata-model-v3.md`。

| # | 缺失规则 | 优先级 | 说明 | 状态 |
|---|---------|--------|------|------|
| G-1 | FMT-11: 动态状态字段禁入非 status-board 文件 | HIGH | V3 核心约束，完全未实现 | TODO |
| G-2 | TOPO-07: actor.maxConsistencyLevel 约束 | MEDIUM | 解析了但无校验 | TODO |
| G-3 | FMT: owner.team/owner.role 非空校验 | MEDIUM | 必填字段无验证 | ✅ validateFMT11 |
| G-4 | FMT: deprecated contract 引用阻断（非仅 warning） | MEDIUM | 当前仅警告不阻断 | TODO |
| G-5 | VERIFY: verify 标识符前缀格式严格校验 | ~~LOW~~ → P2 | 六角色深审确认（= GOV-5） | ✅ PR#64 VERIFY-05 |
| G-6 | Assembly boundary.yaml 存在性校验 | LOW | 派生文件，非真相源 | TODO |
| G-7 | slice.belongsToCell / contract.ownerCell 自动推导 | ~~LOW~~ → P1 | 六角色深审确认缺失造成矛盾视图（= MR-R01/R02） | ✅ PR#67 |

### 未实现的 Kernel 子模块

来源: master-plan Section 5 vs 实际实现。Phase 4 决策 5 正式记录为 v1.1 scope cut。

| 子模块 | master-plan 描述 | 实践评估 | v1.1 优先级 |
|--------|-----------------|---------|------------|
| **kernel/wrapper** | traced sync/event/command wrapper | ~~chi.URLParam 耦合~~ 已解（PR#52）；剩余价值：契约级可观测 | P1 |
| **kernel/command** | 命令队列接口 | iot-device 暴露 L4 无框架支持 | P1 |
| kernel/webhook | receiver + dispatcher | 无实际需求验证 | P2 |
| kernel/reconcile | 最终状态收敛 | 无实际需求验证 | P2 |
| kernel/replay | projection rebuild | 无实际需求验证 | P3 |
| kernel/rollback | rollback metadata | 无实际需求验证 | P3 |
| kernel/consumed | consumed marker | 已被 idempotency.Checker 覆盖 | DROP |
| runtime/scheduler | cron/定时任务 | 无实际需求验证 | P2 |
| runtime/retry | retry/backoff | 已在 ConsumerBase 中实现 | P3 |
| runtime/tls | TLS/mTLS | 无实际需求验证 | P3 |
| runtime/keymanager | 密钥管理 | 已在 auth/keys.go 中部分实现 | P3 |

### adapters/ 与 runtime/ 分层重整

> 来源: 2026-04-07 依赖替换期间分析。删除 adapters/s3 + adapters/oidc（零 import）后，
> 发现剩余 adapter 混合了两类职责：纯 SDK 胶水 vs 领域/框架逻辑。

| # | 当前位置 | 问题 | 方向 |
|---|---------|------|------|
| AL-01 | `adapters/postgres/outbox_relay.go` | 轮询调度逻辑属于 runtime，只有 SQL 执行属于 adapter | 拆出 `runtime/outbox/relay.go`，adapter 只提供 store 接口实现 |
| AL-02 | `adapters/redis/distlock.go` | 续期 goroutine + TTL 策略属于 runtime | 拆出通用 distlock 接口到 runtime，adapter 只做 Redis SET NX/Eval |
| AL-03a | `adapters/websocket/hub.go` | Hub（广播/连接管理/Start/Stop）是框架调度逻辑，不是 SDK 胶水 | ✅ PR#43: Hub 上提到 `runtime/websocket/`，定义 `Conn` 接口；adapter 只实现 nhooyr 绑定 |
| AL-03b | `adapters/websocket/hub.go` readLoop/pingLoop/pingAll | 循环调度与 nhooyr API 调用混在一起 | ✅ PR#43: 调度逻辑随 Hub 搬到 runtime/；adapter 的 nhooyrConn 实现 Conn 接口 |
| AL-04 | `runtime/auth` | 直接 import golang-jwt，按规则应通过接口解耦 | 评估是否值得拆（jwt 是事实标准，拆可能过度设计） |
| AL-05 | `runtime/http` | 直接 import chi | ✅ RouteMux 接口解耦 + Use() (PR#55)，可接受 |

**优先级:** AL-03 正在 PR#43 实施，其余 P3（等 0-B/0-D 完成后评估）

### Cell 接口审计

| 问题 | 说明 |
|------|------|
| Cell 接口 11 个方法 | 混合了 metadata accessor + lifecycle，考虑拆分为 Cell + CellLifecycle + CellMetadata |
| adapter 15 个 t.Skip | 6 个 adapter 共 15 个 skip 的集成测试待补全 |

---

## Tier 4: 发布准备

| # | 任务 | 说明 |
|---|------|------|
| R-1 | 仓库公开或 GOPRIVATE 配置文档 | `go get` 当前无法使用 |
| R-2 | v1.0.0 tag | 无 semver tag，pkg.go.dev 无法索引 |
| R-3 | CONTRIBUTING.md | 无贡献指南 |
| R-4 | 性能基准 | 无 benchmark |
| R-5 | 棕地迁移指南 | 已有项目如何接入 GoCell |
| R-6 | 错误码目录 | 统一 errcode 文档 |

---

## Tier 5: winmdm 外部需求（34 项六席位审查裁决）

> 来源: winmdm 团队框架分析（8 框架 + 竞品对标），2026-04-11 六席位审查（架构/安全/测试/运维/DX/产品）
> 原始文档: `winmdm/docs/reviews/20260410-gocell-migration-analysis/10-gocell-enhancement-backlog.md` + `11-opensource-benchmark.md`
> 裁决原则: 框架通用能力 Accept → backlog；MDM 专属需求 Reject → winmdm 自建
> 统计: 13 Accept + 3 已实现 + 9 Defer + 9 Reject

### Accept — P0（v1.0 阻塞，~2d）

| # | 需求 | 包位置 | 工作量 | 关键约束 | 六席投票 |
|---|------|--------|--------|---------|---------|
| WM-9 | errcode 内外分离 — Error 增加 InternalMessage，5xx 不泄露内部细节 | `pkg/errcode` + `pkg/httputil` | 0.5d | 与 WM-10/WM-11 合并为一个 PR。新增 `errcode.Safe(code, msg)` 便捷函数 | 6/6 Accept |
| WM-10 | errcode TraceID — 错误响应含 request_id 便于运维追踪 | `pkg/httputil/response.go` | 2h | 新增 `WriteErrorWithContext(w, r, ...)` 保持兼容；从 `ctxkeys.RequestIDFrom(ctx)` 读取 | 6/6 Accept |
| WM-12 | archtest 边界守护 — go/packages 断言分层依赖规则 | `tools/archtest/` | 1d | 断言 CLAUDE.md 4 条核心规则；CI 中运行不可 skip | 6/6 Accept |

### Accept — P1（v1.0 强烈建议，~10d）

| # | 需求 | 包位置 | 工作量 | 前置依赖 | 六席投票 |
|---|------|--------|--------|---------|---------|
| WM-11 | errcode 4xx/5xx OTel 分类 — IsClientError() 判断 span 错误状态 | `pkg/errcode` | 1h | 与 WM-9/WM-10 同 PR | 6/6 Accept |
| WM-1 | CSRF 中间件 — Origin/Referer 校验，BFF/Cookie-based 场景 | `runtime/http/middleware` | 0.5d | 无。CSRF token 必须 crypto/rand ≥32 字节 | 6/6 Accept |
| WM-6 | 游标分页 — keyset pagination（row-value 比较） | `pkg/query` | 1.5d | 无。cursor token 需 HMAC 签名防篡改；解决 P4-TD-09 | 6/6 Accept |
| WM-2 | 密钥轮换调度器 — JWT kid 轮换 + HMAC 轮换（范围限定） | `runtime/auth` 扩展 | 2d | 无。扩展现有 keys.go，不新建包；JWT 必须携带 kid | 6/6 Accept |
| WM-34 | 配置热更新回调 — Cell 级 OnConfigReload 通知 | `runtime/config` 扩展 | 1d | 无。在现有 Watcher.OnChange 基础上增加类型化回调 | 6/6 Accept |
| WM-20 | TestPubSub 认证测试套件 — adapter 标准化质量门槛 | `kernel/outbox/outboxtest/` | 1.5d | PR#68 合并。定义 TestPublisher/TestSubscriber 标准套件 | 6/6 Accept |
| WM-17 | 生命周期钩子细化 — BeforeStart/AfterStart/BeforeStop/AfterStop | `kernel/cell` 可选接口 | 1d | 无。**必须用可选接口**（type assertion），不改 Cell 必须方法 | 6/6 Accept |
| WM-15 | L4 队列状态机 — Pending→Sent→Acked/Timeout→Failed/DeadLetter | `kernel/outbox` 合入 0-B2 | 1.5d | 0-B2 Relay 重写。不独立建 pkg/statemachine | 6/6 Accept |
| WM-33b | 熔断器 — circuit breaker 中间件 | `adapters/` 实现 RateLimiter 接口 | 0.5d | 无。用 sony/gobreaker 包装，不自建 | 6/6 Accept |

### Accept — P2（v1.0 后，~1d）

| # | 需求 | 包位置 | 工作量 | 说明 | 六席投票 |
|---|------|--------|--------|------|---------|
| WM-7 | 批量操作 helper — BulkResult + 部分成功语义 | `pkg/httputil` 或 `pkg/bulk` | 1d | 批量操作中每条记录必须独立鉴权 | 5/6 Accept |

### 已实现（需告知 winmdm）

| # | 需求 | 现有代码位置 | 使用方式 |
|---|------|------------|---------|
| WM-13 | per-route Body size | `runtime/http/middleware/body_limit.go` + `runtime/http/router/router.go:157 With()` | `mux.With(middleware.BodyLimit(50<<20)).Post("/upload", h)` |
| WM-19 | Handler Middleware chain | PR#68: `runtime/eventbus/consumer.go` ConsumerMiddleware | PR#68 合并后可用 |
| WM-33 | 限流中间件 | `runtime/http/middleware/rate_limit.go` RateLimiter + WindowedRateLimiter | `mux.With(middleware.RateLimit(limiter))` |

### Defer v1.1（7 项）

| # | 需求 | 理由 |
|---|------|------|
| WM-18 | 延迟消息原语 | 等 0-B2 Outbox Relay 稳定后再做（3/6 票 Accept） |
| WM-32 | mTLS 中间件 | 安全席认为阻塞，但其他 5 席认为 MDM 场景非通用 P0。v1.1 做通用 TLS 配置构建器 |
| WM-4 | Webhook 出站 adapter | 依赖 outbox relay 稳定。实现时注意 SSRF 防护（URL 白名单/禁内网 IP） |
| WM-5 | 查询过滤语言 (OData $filter) | OData 是 MDM/微软生态偏好，非通用需求。当前 query.Builder 够用（2/6 票） |
| WM-22 | Visibility Query API | 运维/诊断能力，依赖 #17 和路由元数据（0-K HR-02）先稳定 |
| WM-23 | 模块化单体→微服务 | Assembly 已天然支持，需验证 + 文档而非新建。与 WM-28 Registry 绑定评估 |
| WM-16 | 投影按需重算 | 无实际需求验证，backlog 已列 kernel/replay 为 P3 |

### Defer v2+（2 项）

| # | 需求 | 理由 |
|---|------|------|
| WM-28 | 服务发现 Registry | K8s 提供，与 WM-23 绑定评估 |
| WM-29 | Saga 补偿 | L2 outbox + L3 最终一致已覆盖设计范围 |

### Reject（附理由 + 替代建议）

| # | 需求 | 裁决理由 | winmdm 替代方案 |
|---|------|---------|----------------|
| WM-3 | X.509 证书管理 | MDM 专属（CA/CSR/CRL/SCEP），非通用框架能力 | winmdm 在 `cells/cert-core` 自建或集成 step-ca/Vault PKI |
| WM-14 | Codec 注册表 | YAGNI — JSON-only 够用；SOAP/SyncML 是 MDM 专属 | winmdm handler 层自行实现 SyncML 编解码 |
| WM-21 | Mixin 共享逻辑 | 非 Go-idiomatic — Go 已有 embedding + interface 组合模式 | 用 BaseCell embedding + middleware 实现横切关注点 |
| WM-24 | Policy Engine | MDM 专属（证书签发策略），框架最多做钩子接口 | winmdm 集成 OPA/Casbin 作为 adapter |
| WM-25 | 短期证书 | MDM/PKI 领域，运维成本极高（5 分钟 RTO 签发） | winmdm 集成 step-ca |
| WM-26 | FanOut/FanIn | RabbitMQ 原生 fanout exchange 够用 | 使用 RabbitMQ exchange 类型配置 |
| WM-30 | 编译期 Contract 验证 | 已并入 WM-12 archtest 作为扩展规则集 | — |
| WM-31 | 跨协议元数据同步 | 纯 HTTP 够用，无第二协议 | 等引入 gRPC 时再做 |
| WM-34b | Kratos 两层中间件 | 保持现有 HTTP + Event 两套中间件，不强制统一 | 保持现有模式 |

### 运维审查发现的额外生产缺口

> 以下 5 项 winmdm 未提出，但运维席位审查中发现对 v1.0 生产就绪同样重要。

| # | 问题 | 关联已有 backlog | 建议 |
|---|------|----------------|------|
| OPS-1 | metrics 基数爆炸 — middleware/metrics.go 使用 r.URL.Path 作 label | 0-K HR-02 (PROM-01) | 已有，优先级不变 |
| OPS-2 | 日志缺 trace_id 关联 — AccessLog/ConsumerBase 日志无 trace_id/span_id | 新发现 | 随 WM-10 一并补充 |
| OPS-3 | readiness 探针未接 adapter — postgres/redis/rabbitmq 不报告健康状态到 /readyz | 新发现 | 建议 P1，各 adapter 注册 HealthChecker |
| OPS-4 | 优雅关闭缺 drain 期 — 新请求应在 shutdown 时返回 503 | 新发现 | 建议 P2，在 bootstrap shutdown 中增加 drain 阶段 |
| OPS-5 | 连接池无指标 — PG/Redis/RabbitMQ 连接池指标不可观测 | 新发现 | 建议 P2，暴露 pool stats 为 Prometheus metrics |

### MDM 偏见过滤说明

> 34 项中 5 项被识别为 MDM 特有需求伪装成通用框架能力（已 Reject）：
> - WM-3 X.509 CA/SCEP — 只有设备管理需要
> - WM-5 OData $filter — Windows MDM 协议偏好（已降为 Defer）
> - WM-14 SOAP Codec — OMA-DM SyncML 专属
> - WM-25 短期证书 — 设备证书场景
> - WM-24 Policy Engine（签发策略）— CA 专属
>
> 判断标准："如果换一个非 MDM 项目（电商/SaaS/IoT），至少 2 个领域也需要，才是框架能力。"

---

## 执行建议

```
Kernel 止血 ✅ (PR#59-67) → 0-F + 0-G 部分 🔄 (PR#68) → 0-B2 / 0-G B-03 → Review 剩余 → Tech-Debt → 发布
                                                          ↗ 0-H / 0-I / 0-K（可并行）
                                                          ↘ Tier 3（v1.1 持续）

Tier 5 与 Tier 0 并行:
  WM-9 + WM-10 + WM-11 errcode 三合一 (0.5d)
  WM-12 archtest (1d)

Tier 0 完成后 → Tier 5 P1:
  WM-1 CSRF → WM-6 游标分页 → WM-2 密钥轮换 → WM-34 配置热更新
  → WM-20 TestPubSub(依赖PR#68) → WM-17 生命周期钩子 → WM-15 L4状态机(随0-B2) → WM-33b 熔断器
```

**进度总结（2026-04-11）：**
- Kernel 止血全部完成：12 个 P1 + 7 个 P2 = 19 条 findings 已修（PR#59-64）
- metadata 增强：strict YAML (PR#65) + G-7 auto-derive (PR#67) 已合并
- 0-F/0-G 大部分工作在 PR#68 中，待合并
- 剩余关键路径：PR#68 合并 → 0-B2 (Relay 三阶段) → 0-G B-03 (重连 backoff)
- winmdm 34 项需求六席位审查完成：13 Accept + 3 已实现 + 9 Defer + 9 Reject
- 修复计划详见 `tools/docs/20260408-fix-order-plan.md`
