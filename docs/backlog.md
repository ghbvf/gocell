# GoCell Backlog

> 只含待办事项。已完成项（PR#67-91）归档至 `docs/reviews/archive/202604121800-backlog-pre-restructure.md`。
> 更新日期: 2026-04-12（PR#92-98 状态更新 + 六席位 findings 登记）
> Batch 1-4: ✅ 全部完成（PR#67-91，25 个 PR）

---

## Batch 5A: 六席位 P1 修复（~34h，6 路全并行）

> 来源: PR#87/89/90/91 六席位复核。正确性/安全问题，功能推进之前优先修复。

| PR | 任务 | 工时 | 文件范围 |
|----|------|------|---------|
| ~~CURSOR-FIX~~ | ~~P1-01~~ ✅ PR#94 + ~~P1-02~~ ✅ PR#94 + ~~P1-03(scope/context强制)~~ ✅ PR#95 + ~~P2-01~~ ✅ PR#94 + ~~P4-TD-11~~ ✅ PR#94 + ~~P4-TD-14~~ ✅ PR#94 + ~~WM-6-F2~~ ✅ PR#94 | ✅ 全部完成 | — |
| HTTP-SEC-FIX | HTTP-SEC-01(IP格式校验) + SEC-02(trusted proxy fail-fast) | 3h | `runtime/http/middleware/` + `router/` |
| CONTRACT-FIX | ~~STRICT-P1-01(contract覆盖)~~ ✅ PR#98 + ~~P1-02(contract_test可执行)~~ ✅ PR#98 + ~~SCHEMA-01(空壳schema)~~ ✅ PR#98 | 8h → ✅ done | `contracts/http/` + `cells/*/contract_test.go` |
| ~~CFG-WATCHER~~ | ~~CFG-P1-01(目录级监听)~~ + ~~P1-02(shutdown顺序)~~ + ~~P2-02(补测试)~~ | ✅ PR#97 | — |
| ~~CFG-RELOAD~~ | ~~CFG-P1-03(generation counter)~~ + ~~P1-04(DeepCloneValue)~~ + ~~WM-34-F4(commit语义)~~ | ✅ PR#97 | — |
| BATCH3-FIX | OB-02(safe_observe测试) + WriteErrorWithContext(25+ handler) + **PATCH-STRICT(identity PATCH strict decode)** | 4h | `runtime/http/middleware/` + `cells/*/handler.go` |
| **OBS-WIRE** | **HTTP observability 接入默认链** — tracing + metrics 上提到 router/bootstrap 默认装配面；当前只有 middleware helper，默认链无 Tracing。ref: Kratos server.Middleware / go-zero engine / otelchi root Use | 4h | `runtime/http/router/router.go` + `runtime/bootstrap/` |

---

## Batch 5B: 功能推进（~5d，2 轨道并行）

> 前置: Batch 5A 合并后。GAP 硬化: CorrelationID + rate limiter + metadata bridge。

| 轨道 | PR | 任务 | 工时 |
|------|-----|------|------|
| A | 韧性 | WM-33b(熔断器) + RL-WIRE-01(rate limiter激活) | 1d |
| A | 测试基础设施 | ~~WM-20 TestPubSub 认证套件~~ ✅ PR#93 | 1.5d |
| A | 可观测 | RL-METRICS-01 Relay Prometheus 指标 | 2h |
| B | 事件模型 | ~~WM-15 L4 队列状态机~~ ✅ PR#93 | 1.5d |
| B | 事件路由 | ER-ARCH-02 ConsumerGroup 支持 | 2h |
| B | 链路追踪 | CID-01(consumer侧) + META-BRIDGE-01(Entry.Metadata注入) | 5h |

---

## Batch 6A: 运维 + 正确性（~19h，4 路并行）

> 前置: Batch 5B 合并后。P3-DEFER-04+P4-TD-01 **必须最先合入**（阻塞 Batch 6C SOL-B-01）。

| PR | 任务合并 | 工时 | 文件 |
|----|----------|------|------|
| 运维健康体系 | OPS-3(pg/redis Health) + OPS-4(drain期) + ER-P2-03(Router health) + SEC-READYZ-01(/readyz隔离) + CFG-P2-01(watcher readyz) + **READYZ-ROOT(readyz 升级为 bootstrap 一等生命周期状态，非可选钩子)** + **R97-02(watcher debounce, 100ms窗口, ref: Viper)** (PR#97 review) + **R97-F1(watcher symlink-pivot支持, K8s ConfigMap ..data 更新检测, ref: Viper EvalSymlinks)** (PR#97 second review) | 10h | `runtime/http/health/` + `runtime/bootstrap/` + `router/` + `config/` |
| runtime 竞态修复 | R1C2-F01(eventbus race) + R1C2-F03(WorkerGroup首失败) + **R97-R3-01(reload WaitGroup Add-after-Wait edge, 改用 channel+select 或 singleflight 消除理论竞态)** (PR#97 round3 review) | 5h | `runtime/eventbus/` + `runtime/worker/` + `runtime/bootstrap/` |
| RabbitMQ 连接正确性 | RMQ-RACE-01(WaitConnected竞态) + P3-DEFER-05(Health状态区分) | 4h | `adapters/rabbitmq/connection.go` |
| kernel outbox 清理 | P4-TD-01(NoopOutboxWriter) + P3-DEFER-04(Receipt移包) | 4h | `kernel/outbox/` + `kernel/idempotency/` |
| **L4 API 收敛** | **L4-API-01**: Validate 改名 ValidateNew（create-only 语义）+ AdvanceCommand 统一 timestamps/attempt 副作用 + CommandStateAdvancer 暴露完整迁移契约。adapter 不应需要绕过状态机语义 | 4h | `kernel/outbox/l4.go` **(P1, discovered via PR#93 六席位复核)** |

---

## Batch 6B: Tech Debt 清理（~20.5h，9 路并行 + RabbitMQ 清理串行）

> 前置: Batch 6A RabbitMQ PR 合入后 RabbitMQ 清理才能开始；其余 9 PR 全并行。

| PR | 任务合并 | 工时 | 备注 |
|----|----------|------|------|
| RabbitMQ 代码清理 | P3-DEFER-01(backoff提取) + P3-DEFER-02(FailOpen enum) | 3h | **依赖 6A RabbitMQ PR** |
| Hook 增强 | WM17-F2-2(ctx超时) + WM17-F4-3(Prometheus metrics via HookObserver接口) | 3h | 需定义 kernel/cell HookObserver 接口 |
| CI 增强 | CI-01(integration路径) + T1-7(golangci-lint) | 2.5h | 同改 ci.yml |
| Session 安全 | P3-TD-10 Session refresh TOCTOU 乐观锁 | 4h | 高风险 |
| decode 加固 | DECODE-STR-01 classifyDecodeError 脆弱性 | 2h | `pkg/httputil/decode.go` |
| Journey 校验 | F-5 catalog 不校验引用 | 2h | `kernel/journey/catalog.go` |
| OTel 覆盖率 | OTEL-COV-01 testcontainers 集成测试 | 1h | `adapters/otel/` |
| **TestPubSub 真实 adapter 认证** | **TPUB-01**: conformance harness 替换 sleep 为显式 ready/setup-error 握手 + 接入 RabbitMQ adapter 验证。当前"绿色"仅代表 InMemory 通过，不代表 broker-backed adapter 满足 contract | 4h | `kernel/outbox/outboxtest/` + `adapters/rabbitmq/` **(P1, discovered via PR#93 六席位复核)** |
| cursor 可观测 | CURSOR-P2-02 cursor invalid 结构化日志 | 1h | `cells/audit-core/` |
| order+demo 修复 | P4-TD-04(outboxWriter enforce) + P4-TD-12(t.Skip) | 3h | `cells/order-cell/` + `cells/demo/` |
| **contract 命名修正** | **CONTRACT-NAME-01**: `http.auth.me.v1` 实际覆盖 identity CRUD（POST create/PUT update/DELETE），命名应为 `http.auth.user.v1`；或拆分为 me(GET only) + user(CRUD) | 2h | `contracts/http/auth/me/` + `cells/access-core/slices/identitymanage/` **(P1, discovered via PR#98 六席位复核)** |
| **ConfigEntry json tags** | **CFG-JSON-01**: `domain.ConfigEntry` 缺 json tags，config GET 响应用 PascalCase（`Key`/`Value`/`Version`），违反 camelCase 规范。同理 `domain.FeatureFlag` | 1h | `cells/config-core/internal/domain/config_entry.go` + `feature_flag.go` **(P2, discovered via PR#98 六席位复核)** |
| **flags request schema 拆分** | **FLAGS-SCHEMA-01**: `http.config.flags.v1/request.schema.json` 仅覆盖 POST evaluate 的 `{subject}` body，GET list/get 无 body。单 schema 无法描述多操作 | 0.5h | `contracts/http/config/flags/v1/` **(P2, discovered via PR#98 六席位复核)** |

---

## Batch 6C: P1 功能补全（~5d，2 轨道）

> 前置: Batch 6A P3-DEFER-04 合入（SOL-B-01 需要新 Receipt 接口）。

| 轨道 | PR | 任务 | 工时 | 前置 |
|------|-----|------|------|------|
| Auth | WM-2-F1 | KeyProvider 接口抽象 | 1d | WM-34 ✅ |
| Auth | WM-35 | BFF handler 接入 cookie session | 2d | WM-2-F1 |
| Auth | WM-36 | SecureCookie key rotation 双 key ring | 1.5d | WM-2-F1，与 WM-35 串行(cookie_session.go) |
| Kernel | SOL-B-01 | Claimer lease 续租 Receipt.Renew | 4h | 6A P3-DEFER-04 |

> Auth 轨道关键路径: WM-2-F1 → WM-35 → WM-36（串行，共 4.5d）
> Kernel 轨道与 Auth 轨道并行

---

## Batch 7: Review + 发布准备（~16h，5 路并行 + tag 最后）

> 前置: Batch 6A+6B+6C 全部合入（review 对象是最终代码）。

| PR | 任务 | 工时 | 并行 |
|----|------|------|------|
| Review cells/ | T1-3 审查 6 cell (5,811 行) | 4h | ✅ |
| Review examples/ | T1-6 审查 3 项目 (233 行) | 2h | ✅ |
| Review 报告 | T1-8 汇总 findings | 2h | 依赖 T1-3+T1-6 |
| 发布文档 | R-1(GOPRIVATE) + R-3(CONTRIBUTING) + R-5(迁移指南) + R-6(错误码) | 4h | ✅ |
| 性能基准 | R-4 benchmark 测试 | 4h | ✅ |
| **v1.0 tag** | R-2 git tag + CI 验收 | — | **全部完成后最后执行** |

---

## Batch 8: P2 偿债（v1.0 后，~54h，14 组全并行）

> 前置: v1.0 tag 发布后。不阻塞发布。

| PR 组 | 任务 | 工时 |
|-------|------|------|
| Cursor DX | WM-6-F6(泛型helper) + F7(日志收口) + F1(prod guard) + TX-NIL-01(nil-safe注释) + **CUR-HDL-01(4个分页handler补cursor回归: 垃圾cursor/旧格式缺scope/跨context replay，断言400+ERR_CURSOR_INVALID)** (PR#94 review) | 5.5h |
| Config 增强 | WM-34-F1(watcher目录级) + F2(metrics) + F3(key过滤) + **R97-04(Get()返回可变引用, 需DeepCloneValue防调用方篡改内部状态, C1)** (PR#97 review) + **R97-F3(Generation observedGeneration 状态面, 拆 desired vs applied, 需健康端点集成)** (PR#97 second review) + **R97-R3-02(ShutdownDrain测试改用channel确定性同步替代300ms时序)** (PR#97 round3 review) | 7h |
| metadata parser | META-67-01/02/03 | 2.5h |
| auth 增强 | WM-2-F2(HMAC replay) + WM-2-F3(auth metrics) | 4h |
| access-core 重构 | P3-TD-11 domain 模型 | 4h |
| config rollback | P3-TD-12 version 校验 | 2h |
| 集成测试补全 | P4-TD-05(outbox全链路) + RL-INT-01(Relay PG) + P2-T-02(audit e2e) | 6h |
| 迁移+订阅 | RL-MIG-01(online-safe索引) + RL-SUB-01(入站ID校验) | 3h |
| CMD 重构 | CMD-MODE-01(fail-fast) + CMD-REFACTOR-01(app包提取) | 3.5h |
| 批量操作 | WM-7 BulkResult helper | 1d |
| Entity→DTO | P4-TD-13 (8 handler) | 4h |
| demo 模式统一 | WM-6-F8 全局模式开关 (C3 架构级) | 3h |
| examples 更新 | P3-DEFER-03 新 API 示例 (依赖 B5B ER-ARCH-02) | 1h |
| 连接池指标 | OPS-5 PG/Redis/RabbitMQ pool stats | 2h |
| L4 构造函数纯化 | L4-PURE-01: `NewCommandEntry` 调用 `time.Now()`，kernel 构造函数应接受 `now time.Time` 参数保持纯函数 (PR#93 review) | 0.5h |
| L4 重试 API | L4-RETRY-01: 缺少 `ResetForRetry` 函数，adapter 手动重置 `Status=Pending` 绕过状态机不变量 (PR#93 review) | 1h |
| Flag repo 并发测试 | FLAG-RACE-01: FlagRepository 并发测试只校验读侧排序，缺 writerErrors 计数断言（对比 config_repo_test.go 已有完整模式）(PR#94 review) | 0.5h |

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

### 跨框架 GAP — v1.1 待评估

| GAP | 能力 | 预估 | 前置条件 |
|-----|------|------|---------|
| GAP-7 | Scheduler/cron | 1d spike | WM-17 ✅ |
| GAP-11 | Architecture dependency graph | 1d | archtest ✅ |
| GAP-13 | Auto API docs / OpenAPI | 2d | HR-02 ✅ |
| GAP-6 | Singleflight + cache helper | 1d | — |
| GAP-5 | Adaptive load shedding | 1.5d | WM-33b + RL-WIRE-01 |

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

| Batch | PR 数 | 工时 | 并行度 | 前置 | 里程碑 |
|-------|-------|------|--------|------|--------|
| 5A | 7 → 剩 3 | ~~39h~~ → ~11h | 3/3 | — | CURSOR ✅ CFG ✅ CONTRACT open，剩 HTTP-INFRA + BATCH3-FIX + OBS-WIRE |
| 5B | 6 | ~5d | 2 轨道 | 5A | 事件测试 + CorrelationID + 韧性 |
| 6A | 4+1 | ~24h | 5/5 | 5B | 生产级可靠性 + L4 API 收敛 |
| 6B | 10+1 | ~24.5h | 10/11 | 6A(RMQ) | Tech Debt 主体收敛 + TestPubSub adapter 认证 |
| 6C | 4 | ~5d | 2 轨道 | 6A(Receipt) | P1 功能补全 (BFF+SecureCookie) |
| 7 | 6 | ~16h | 5+tag | 6全完 | **v1.0 RC → v1.0** |
| 8 | 14 | ~54h | 14/14 | v1.0 | P2 偿债 |

```
Week 1:  Batch 5A 近完成（CURSOR ✅ PR#94/95, CFG ✅ PR#97, CONTRACT PR#98 open）
         剩余: HTTP-INFRA(PR#96 open) + BATCH3-FIX + OBS-WIRE
Week 2:  Batch 5B (功能推进, 2轨道) — WM-33b/RL-WIRE-01/RL-METRICS-01 + ER-ARCH-02/CID-01
Week 3:  Batch 6A (运维+正确性+L4 API) + 6B (tech debt+TPUB) + 6C Auth轨道启动
Week 4:  Batch 6C 收尾 + Batch 7 (review+发布) → v1.0 tag
Post:    Batch 8 (P2偿债, 按需排期)
```
