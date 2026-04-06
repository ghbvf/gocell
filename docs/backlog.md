# GoCell Backlog

> Phase 0-4 已完成并合并到 develop。本文档汇总全部待办事项。
> 更新日期: 2026-04-06

---

## Tier 0: Review 修复 + 依赖替换（进行中）

> PR#37 (postgres) ✅ PR#38 (rabbitmq) ✅ PR#39 (redis) ✅ 已合并

### 0-A: 依赖替换 Phase 0 — 安全风险（1d）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| D-01 | `adapters/s3`: 删除整个 adapter（零 import，aws-sdk-go-v2 由 wiring 层直接用） | 0.5d | ✅ PR#41 |
| D-02 | `adapters/oidc`: 删除整个 adapter（零 import，消费方直接用 go-oidc + oauth2） | 0.5d | ✅ PR#41 |
| D-03 | `adapters/redis/distlock`: 删除 FenceToken（零调用者） | 0.5h | ✅ PR#40 |

### 0-B: Outbox Relay Plan A（0.5d）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| R-01 | `pollOnce()` markQuery 失败 fail-fast | 2h | TODO |
| R-02 | Start/Stop handshake (`startedCh`) | 2h | TODO |

> 来源: `docs/reviews/202604061401-pr39-six-role/PR39-postgres-outbox-followup.md`

### 0-C: 依赖替换 Phase 1 — 快速收益（1d）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| D-04 | `pkg/uid`: google/uuid 替换手写 UUIDv4（18 调用点） | 0.5d | TODO |
| D-05 | shutdown/bootstrap: errors.Join 替换 firstErr（2 文件 6 行） | 0.5h | TODO |
| D-06 | middleware: chi/middleware 替换 recovery/requestID/realIP（删 ~200 行） | 0.5d | TODO |

### 0-D: RabbitMQ Solution B（2-3d）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| S-01 | `outbox.Subscriber` handler 返回 HandleResult{Disposition, Receipt} | 0.5d | TODO |
| S-02 | `idempotency.Checker` 升级为 Claim/Commit/Release | 0.5d | TODO |
| S-03 | `ConsumerBase` 去掉应用侧 DLQ，返回 Disposition | 0.5d | TODO |
| S-04 | `Subscriber.processDelivery` 按 Disposition 做 Ack/Nack/Requeue | 0.5d | TODO |
| S-05 | 重连策略完善 + backoff | 0.5d | TODO |
| S-06 | 测试覆盖（幂等时序、setup 重连、集成） | 0.5d | TODO |

> 来源: `docs/reviews/202604061449-pr38-solution-b-report.md`

### 0-E: 依赖替换 Phase 2（2d）

| # | 任务 | 预估 | 状态 |
|---|------|------|------|
| D-07 | `adapters/postgres/migrator`: pressly/goose v3 替换（删 ~418 行） | 1d | TODO |
| D-08 | 新建 `adapters/otel` + `adapters/prometheus`（OTel + Prometheus） | 1d | TODO |

### 执行顺序

```
0-A (安全替换) → 0-B (Outbox Relay) → 0-C (快速收益)
  → 0-D (Solution B) → 0-E (migrator + OTel)
  → 继续 Tier 1 Review (R1D-4/5/6 → R1E → R2)
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
| R1B kernel | ✅ 已审 |
| R1C runtime | ✅ 已审 + 已修 |
| R1D-1 postgres | ✅ 已审 + 已修 (PR#37) |
| R1D-2 redis | ✅ 已审 + 已修 (PR#39) |
| R1D-3 rabbitmq | ✅ 已审 + 已修 (PR#38) |
| R1D-4 oidc | 待审（Tier 0 替换后审新代码） |
| R1D-5 s3 | 待审（Tier 0 替换后审新代码） |
| R1D-6 websocket | 待审 |
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

## Tier 2: Review 产出的修复 + Tech-Debt 清理（5-7 天）

### 全量 Review Findings（R1A-R1D，35 条未修）

> Review 原则：P0 当层修，P1/P2 记录到此处留 Fix Pack。
> 来源报告已归档至 `docs/reviews/archive/`。

#### P1 — kernel（8 条）

| ID | 文件 | 问题 |
|----|------|------|
| R1B1-01 | `kernel/cell/base.go:165-171` | Add*/collection accessor 无 mutex 保护，潜在 data race |
| R1B1-02 | `kernel/cell/base.go:64-82` | OwnedSlices/ProducedContracts/ConsumedContracts 读可变字段无锁 |
| F-OB-02 | `kernel/outbox/outbox.go:98-107` | Entry 无显式 Topic 字段，EventType 兼做路由 |
| F-ID-01 | `kernel/idempotency/idempotency.go` | IsProcessed+MarkProcessed 两步有 TOCTOU（TryProcess 已加但旧接口未废弃） |
| G-01 | `kernel/governance/rules_fmt.go` | cell.yaml owner.team/role, verify.smoke 必填校验缺失 |
| G-02 | `kernel/governance/rules_verify.go` | slice.yaml verify.unit 未校验为必填 |
| F-5 | `kernel/journey/catalog.go:18-35` | Journey catalog 不校验引用的 contract/cell 是否存在 |
| F-2 | `kernel/assembly/assembly.go:83-132` | Start()/StartWithConfig() ~40 行重复代码 |

#### P1 — pkg（3 条）

| ID | 文件 | 问题 |
|----|------|------|
| R1A1-F02 | `pkg/httputil/response.go:86-107` | mapCodeToStatus 子串匹配漏掉 ERR_AUTH_TOKEN_EXPIRED，回退 500 |
| R1A1-F03 | `pkg/httputil/response.go:86-107` | mapCodeToStatus 子串调度脆弱，顺序依赖 |
| R1A1-F04 | `pkg/httputil/response.go:18,27,51` | json.NewEncoder 错误被静默丢弃 |

#### P1 — runtime（6 条）

| ID | 文件 | 问题 |
|----|------|------|
| F-01 | `runtime/auth/jwt.go:54` | ErrAuthUnauthorized 重复定义（local var + errcode import 冲突） |
| F-02 | `runtime/auth/keys.go:26-36` | 无 RSA 最小 key size 校验，接受 512/1024-bit |
| F-03 | `runtime/auth/keys.go:82,90,101,109` | LoadRSAKeyPairFromPEM 裸 fmt.Errorf，未用 errcode |
| R1C2-F01 | `runtime/eventbus/eventbus.go:138-148` | Close()+Subscribe() 竞态，channel read after close |
| R1C2-F02 | `runtime/eventbus/eventbus.go:118-149` | Subscribe 退出时 subs map 泄漏 stale channel |
| R1C2-F03 | `runtime/worker/worker.go:47-72` | WorkerGroup.Start 首个失败不取消其余 worker |

#### P2 — kernel（7 条）

| ID | 文件 | 问题 |
|----|------|------|
| R1B1-03 | `kernel/cell/base.go:85-93` | Init 不重置 shutdownCtx/Cancel，Stop→Init→Start 复用过期 context |
| R1B1-04 | `kernel/cell/base.go:39` | sync.Mutex 应改 sync.RWMutex（读多写少） |
| F-OB-01 | `kernel/outbox/outbox.go:68` | 无批量写支持，Writer.Write 只接受单条 Entry |
| F-OB-03 | `kernel/outbox/outbox.go:99-107` | Entry 必填字段（ID, AggregateID, EventType）无校验 |
| F-META-01 | `kernel/metadata/parser.go` | 未知 YAML 字段静默忽略，未启用 KnownFields(true) |
| F-3 | `kernel/assembly/assembly.go:148-157` | Stop() 只返回首个错误，吞后续（同 shutdown firstErr 问题） |
| F-4 | `kernel/scaffold/templates.go:1-9` | doc.go 和 templates.go 包注释冲突 |

#### P2 — pkg + runtime（6 条）

| ID | 文件 | 问题 |
|----|------|------|
| R1A1-F05 | `pkg/id/` | 已废弃包仍存在，无 // Deprecated 标注 |
| R1A1-F06 | `pkg/ctxkeys/keys_test.go:118-140` | TestFromMissingKey 遗漏 RequestID/RealIP/Subject 覆盖 |
| R1A1-F08 | `adapters/redis/client.go:16` | ErrAdapterRedisLockAcquire 常量名/值不一致（Acquire vs ACQUIRED） |
| F-04 | `runtime/auth/middleware.go:133` | writeAuthError 忽略 JSON encode 错误 |
| R1C2-F04 | `runtime/worker/periodic.go` | PeriodicWorker 缺编译时接口检查 |
| R1C2-F05 | `runtime/worker/periodic.go:18-52` | PeriodicWorker.Stop 不防 double-Start，done channel 复用 |

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
| P4-TD-02 | S6 P2-3 | chi.URLParam 耦合（10 个文件） | 2h |
| P4-TD-09 | Tier0 F-06 | List 端点缺分页 | 2h |
| P4-TD-10 | Tier0 F-07 | POST 201 响应未包装 `{"data":...}` | 2h |
| P4-TD-11 | Tier0 F-14 | in-memory repository 缺并发测试 | 1h |
| P3-TD-11 | Phase 2 #56-59 | access-core domain 模型重构 | 4h（高风险） |
| P3-TD-12 | Phase 2 #62 | configpublish.Rollback version 校验 | 2h |

---

## Tier 3: 核心能力完善 — v1.1（持续）

### metadata-model-v3 校验规则补全

来源: KG 分析，对照 `docs/architecture/metadata-model-v3.md`。

| # | 缺失规则 | 优先级 | 说明 |
|---|---------|--------|------|
| G-1 | FMT-11: 动态状态字段禁入非 status-board 文件 | HIGH | V3 核心约束，完全未实现 |
| G-2 | TOPO-07: actor.maxConsistencyLevel 约束 | MEDIUM | 解析了但无校验 |
| G-3 | FMT: owner.team/owner.role 非空校验 | MEDIUM | 必填字段无验证 |
| G-4 | FMT: deprecated contract 引用阻断（非仅 warning） | MEDIUM | 当前仅警告不阻断 |
| G-5 | VERIFY: verify 标识符前缀格式严格校验 | LOW | 隐式匹配可接受 |
| G-6 | Assembly boundary.yaml 存在性校验 | LOW | 派生文件，非真相源 |
| G-7 | slice.belongsToCell / contract.ownerCell 自动推导 | LOW | DX 改善 |

### 未实现的 Kernel 子模块

来源: master-plan Section 5 vs 实际实现。Phase 4 决策 5 正式记录为 v1.1 scope cut。

| 子模块 | master-plan 描述 | 实践评估 | v1.1 优先级 |
|--------|-----------------|---------|------------|
| **kernel/wrapper** | traced sync/event/command wrapper | 解决 chi.URLParam 耦合 + 契约级可观测 | P1 |
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

## 执行建议

```
Tier 1（全量 Review）→ Tier 2（修复）→ Tier 4（发布）
                                      ↘ Tier 3（v1.1 持续）
```

Tier 1 产出的 findings 决定 Tier 2 的实际范围。Tier 3 和 Tier 4 可并行。
