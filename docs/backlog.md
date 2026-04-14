# GoCell Backlog

> 只含待办事项。已完成项归档至 `docs/reviews/archive/`。
> 更新日期: 2026-04-14
> Batch 1-5: ✅ 全部完成 (PR#67-114, 48 PRs)
> 重构依据: `tools/docs/20260414-backlog-wave-restructure.md`
> 旧版备份: `docs/reviews/archive/20260414-backlog-pre-wave-restructure.md`

---

## Wave 1: 立即可做（29 项，~99h）

> PR#112 (trace propagation) / PR#113 (outbox cleanup) / PR#114 (Health/Readyz) 已合入，前置全部满足。
> 按优先级排序；单人执行时从上到下依次做，多人时全并行。

### Auth 关键路径起点 ★

| # | 任务 | 工时 | 文件 |
|---|------|------|------|
| 1 | **WM-2-F1** KeyProvider 接口抽象 | **1d** | `runtime/auth/` |

> ★ v1.0 唯一关键路径：WM-2-F1 (1d) → WM-35 (2d) → WM-36 (1.5d) = 4.5d 串行。每延迟 1 天 = v1.0 推迟 1 天。

### P1 正确性

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| 2 | **L4 API 收敛** L4-API-01: ValidateNew 改名 + AdvanceCommand 统一副作用 + CommandStateAdvancer 迁移契约 + L4-PURE-01(time.Now 注入) + L4-RETRY-01(ResetForRetry) | 5.5h | `kernel/outbox/l4.go` | 6A |
| 3 | **CONTRACT-OP-01** HTTP operation model 收口: slice 元数据缺 HTTP serve contract、response.schema oneOf 混合 | 4h | `cells/config-core/slices/*/slice.yaml` + `contracts/http/config/` + `cells/access-core/slices/sessionlogout/slice.yaml` | 6B |
| 4 | **CONTRACT-TEST-02** 假阳性修复: contracttest helper 不验证真实 handler/outbox 输出 | 5h | `pkg/contracttest/` + `cells/*/contract_test.go` + `cells/device-cell/slices/deviceregister/` | 6B |
| 5 | **AUTH-DX-01** README + seed 用户 + sso-bff walkthrough: auth 已拦截全部业务路由，README 失效；sso-bff README 缺 refresh/GET user/event 消费 demo (P4-P1-6) | 3h | `README.md` + `cells/access-core/internal/mem/` + `examples/sso-bff/README.md` | 6B + P4 review |
| 6 | **TPUB-01** TestPubSub 真实 adapter 认证: conformance harness 替换 sleep + 接入 RabbitMQ adapter | 4h | `kernel/outbox/outboxtest/` + `adapters/rabbitmq/` | 6B |
| 7 | **API 响应格式统一** P4-TD-09(list endpoint 缺 `nextCursor/hasMore`) + P4-TD-10(POST 201 未包裹 `{"data":...}`) — v1.0 后修 = breaking change | 4h | `cells/*/handler.go` | B8 提前 |
| 8 | **Entity→DTO** P4-TD-13: 8 个 handler 直出 entity 含内部字段，需 DTO 映射隔离 API 契约 — v1.0 后修 = breaking change | 4h | `cells/*/handler.go` (user/session/config/flag/audit/order/device/demo) | B8 提前 |

### 运维 + 基础设施

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| 9 | **Bootstrap 加固 + 端点隔离** OPS-4(graceful shutdown) + BOOT-PANIC-01 + BOOT-OPTION-01 + INFRA-EXPOSE-01(/metrics opt-in + health 分离) | 6h | `runtime/bootstrap/` + `runtime/http/router/` | 6A |
| 10 | **Watcher 核心增强** R97-02(debounce) + R97-F1(symlink-pivot) + WM-34-F1(目录级监听) + F2(metrics) + F3(key 过滤) + R97-04(DeepCloneValue) + R97-R3-02(ShutdownDrain channel 同步) | 7h | `runtime/config/watcher.go` + `store.go` | 6A |
| 11 | **Watcher 状态面 + 连接池指标** R97-F3(Generation/observedGeneration) + OPS-5(PG/Redis/RMQ 连接池指标) | 4h | `runtime/config/` + `adapters/postgres/` + `adapters/redis/` + `adapters/rabbitmq/` | 6A |
| 12 | **RabbitMQ 连接正确性** RMQ-RACE-01(WaitConnected 竞态) + P3-DEFER-05(Health 状态区分) | 4h | `adapters/rabbitmq/connection.go` | 6A |

### P2 Tech Debt

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| 13 | **Session 安全** P3-TD-10 Session refresh TOCTOU 乐观锁 + P4-TD-11(in-memory repo 并发 goroutine 测试) | 5h | `cells/access-core/internal/` | 6B 高风险 + P4 review |
| 14 | **order+demo+examples 修复** P4-TD-04 + P4-TD-12 + EVT-HDR-RESTORE + WM-6-F8(demo 模式开关) + P3-DEFER-03(examples 新 API) + NOOP-RENAME-01 + NIL-PUB-P1(device-cell nil publisher) | 7.5h | `cells/order-cell/` + `cells/demo/` + `cells/device-cell/` + `examples/` | 6B |
| 15 | **cursor 回归矩阵** CURSOR-TEST-01 + CUR-HDL-01: 5 个分页入口补 malformed/missing-scope/cross-context 三类回归 | 4h | `cells/*/handler_test.go` + `service_test.go` | 6B |
| 16 | **config-core 修正** CFG-JSON-01(json tags camelCase) + FLAG-RACE-01(并发测试) + P3-TD-12(rollback version 校验) | 3.5h | `cells/config-core/internal/domain/` | 6B |
| 17 | **Hook 增强** WM17-F2-2(ctx 超时) + WM17-F4-3(Prometheus metrics via HookObserver 接口) | 3h | `kernel/cell/` | 6B |
| 18 | **CB 接口+封装清理** CB-IFACE-01(Allow/Report 拆分) + CB-ENCAP-01(消除 gobreaker import) | 3h | `runtime/resilience/circuitbreaker/` | 6B |
| 19 | **CI 增强** CI-01(integration 路径) + T1-7(golangci-lint) | 2.5h | `.github/ci.yml` | 6B |
| 20 | **decode 加固** DECODE-STR-01 classifyDecodeError 脆弱性 | 2h | `pkg/httputil/decode.go` | 6B |
| 21 | **Journey 校验** F-5 catalog 不校验引用 | 2h | `kernel/journey/catalog.go` | 6B |
| 22 | **DELETE 无 body** DELETE-NOCONTENT-01: 204 + body=0 语义测试 | 1.5h | `contracts/http/auth/user/delete/v1/` | 6B |
| 23 | **OTel 覆盖率** OTEL-COV-01 testcontainers 集成测试 | 1h | `adapters/otel/` | 6B |
| 24 | **Trace trust policy** TRUST-POLICY-01: public-facing endpoint trust-boundary 策略（参考 otelhttp `WithPublicEndpoint`：new root + link），当前默认 trusted-upstream + **OBS-REQID-TRUST**: request_id middleware 无条件信任外部 `X-Request-Id`，需信任边界校验 | 4h | `runtime/http/middleware/tracing.go` + `request_id.go` | 5B PR#112 review + 217 tech-debt |
| 25 | **HSTS 加固** C-H4: `security_headers.go` 补 `includeSubDomains` | 0.5h | `runtime/http/middleware/security_headers.go` | P2 tech-debt |
| 26 | **.env.example 补全** ENV-S3: 补 `GOCELL_S3_REGION=us-east-1` — `s3.Config.Validate()` 必填但示例缺失 | 0.5h | `.env.example` | P4 review |
| 27 | **examples contract CI** INT-2: order-cell/device-cell contract YAML 存在且被 slice.yaml 引用，但 CI 未校验 | 1h | `.github/workflows/ci.yml` | P4 review |

---

## Wave 2: 串行后续（6 项，~27h）

> 依赖 Wave 1 中的特定任务完成后启动。

| # | 任务 | 前置 | 工时 | 文件 |
|---|------|------|------|------|
| 28 | **SOL-B-01** Claimer lease 续租 Receipt.Renew | L4 API (#2) | 4h | `kernel/outbox/` |
| 29 | **Bootstrap tracing 测试** BOOT-TEST-01 | Bootstrap 加固 (#9) | 2h | `runtime/bootstrap/` + `router/` |
| 30 | **Bootstrap 次要清理** BOOT-MINOR-01: panic(err) + access_log real_ip | Bootstrap 加固 (#9) | 1h | `runtime/http/router/` |
| 31 | **RabbitMQ 代码清理** P3-DEFER-01(backoff 提取) + P3-DEFER-02(FailOpen enum) | RMQ 连接 (#12) | 3h | `adapters/rabbitmq/` |
| 32 | **cursor 可观测** CURSOR-P2-02 invalid 结构化日志 | cursor 回归 (#15) | 1h | `cells/audit-core/` |
| 33 | **WM-35** BFF handler 接入 cookie session | WM-2-F1 (#1) | **2d** ★ | `runtime/auth/` |

> 建议合并 PR: #9+#29+#30 → "Bootstrap 全家桶" (9h)；#2+#28 → "outbox 串行包" (9.5h)。

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

  其余 Wave 1 全部任务并行执行，总工时 ~91h 但不在关键路径上。
```

### PR 合并建议（36→~24 PR）

| 合并 PR | 包含任务 | 工时 | 理由 |
|---------|---------|------|------|
| Bootstrap 全家桶 | #9 + #29 + #30 | 9h | 同目录相关改动 |
| Contract 正确性 | #3 + #4 + #22 | 10.5h | contract 体系修正 |
| API 契约加固 | #7 + #8 | 8h | 都改 handler 响应格式，v1.0 前必修 |
| Trust boundary | #24 (TRUST-POLICY + OBS-REQID) | 4h | 同一信任边界主题 |
| Kernel 小修 | #20 + #21 | 4h | 独立小改 |
| cursor 全家桶 | #15 + #32 | 5h | 紧密相关 |
| outbox 串行包 | #2 + #28 | 9.5h | 同包串行一起 review |
| 快修合集 | #25 + #26 + #27 | 2h | 三个独立小修 |

---

## Batch 8: P2 偿债（v1.0 后，~41.5h，11 组全并行）

> 前置: v1.0 tag 发布后。不阻塞发布。
> 整理: 23 组 → 11 组（5 个小项合并为 OBS 全家桶、3 个合并为 Outbox 治理、2 个合并为 order-cell 收口；4 项提前到 Wave 1）

| PR 组 | 任务 | 工时 |
|-------|------|------|
| **OBS 可观测全家桶** | META-SIZE-01(Metadata key 数/大小上限) + OBS-TABLE-01(table-driven 改写) + OBS-METRIC-01(bridge counter/histogram) + OBS-DX-01(cloneMetadata 导出 + wrapper 清理 + godoc) + OBS-DOC-01(IsReservedMetadataKey usage example) | 6h |
| **Outbox 治理** | OUTBOX-GUARD-01(NoopWriter/DiscardPublisher lint 约束) + DISCARD-OBS-01(DiscardPublisher Logger 注入 + counter) + OUTBOX-RECEIPT-01(`outbox.Receipt` alias 全仓迁移 `idempotency.Receipt`) | 4h |
| **order-cell 收口** | ORDER-DEMO-01(demo 模式产品行为决策) + NIL-PUB-P2(5 个 L2 service nil publisher 防护) | 3h |
| Cursor DX | WM-6-F6(泛型 cursor helper) + F7(cursor 日志收口) + F1(prod guard) + TX-NIL-01(nil-safe 注释) | 3.5h |
| metadata parser | META-67-01(strict unknown-field reject) + META-67-02(位置信息错误报告) + META-67-03(cross-file 引用校验) | 2.5h |
| auth 增强 | WM-2-F2(HMAC replay 防护) + WM-2-F3(auth metrics) | 4h |
| access-core 重构 | P3-TD-11: domain 模型拆分 User/Session/Role（前置: Wave 1 #13 Session TOCTOU 先完成） | 4h |
| 集成测试补全 | P4-TD-05(outbox 全链路) + RL-INT-01(Relay PG 集成) + P2-T-02(audit e2e) | 6h |
| 迁移+订阅 | RL-MIG-01(online-safe 索引 CONCURRENTLY) + RL-SUB-01(入站 ID 校验) | 3h |
| CMD 重构 | CMD-MODE-01(fail-fast) + CMD-REFACTOR-01(app 包提取) | 3.5h |
| 批量操作 | WM-7: 泛型 `BulkResult[T]` helper | 1d |

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
| 1 | 29 | ~99h | 无（PR#112-114 已合入） | Auth 关键路径启动 + P1 正确性 + API 契约加固 + 运维 |
| 2 | 6 | ~27h | Wave 1 特定任务 | Auth WM-35 + Bootstrap/RMQ/cursor 收尾 |
| 3 | 1 | ~12h | WM-35 | Auth WM-36 收尾 |
| 4 | 6 | ~16h | Wave 1-3 全部合入 | **Review → v1.0 tag** |
| 8 | 11 | ~41.5h | v1.0 | P2 偿债（不阻塞发布） |

```
已完成:
  Batch 1-4: ✅ PR#67-91 (25 PRs)
  Batch 5A:  ✅ PR#94-101 (8 PRs)
  Batch 5B:  ✅ PR#102-114 (13 PRs, 含 PR#112 trace + PR#113 outbox + PR#114 health)
  6A 部分:   ✅ PR#107 runtime 竞态 + PR#114 Health/Readyz + PR#113 outbox 清理

当前:
  Wave 1-4: 42 项, ~154h → v1.0 (含 4 项从 Batch 8 提前)
  Batch 8:  11 组, ~41.5h (从 23 组合并整理)
  关键路径: WM-2-F1 (1d) → WM-35 (2d) → WM-36 (1.5d) → Review (2d) = 6.5 工作日
```
