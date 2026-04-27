# Kernel 修复执行状态

> 日期: 2026-04-11
> 基准计划: `docs/plans/20260411-kernel-repair-plan.md`
> 会话产出: PR#67-73 + 跨框架架构分析 + 3 份 Wave C 设计

---

## 已完成 PR

| PR | 分支 | 内容 | 状态 |
|-----|------|------|------|
| #67 | `fix/pr-ga-auto-derive` | G-7 auto-derive belongsToCell/ownerCell | ✅ 已合并 |
| #68 | `fix/pr-d1-kernel-interface` | 0-F Solution B 接口 (17 files, +1191/-473) | ✅ 已合并 |
| #69 | — | errcode trinity (非本会话) | ✅ 已合并 |
| #70 | `fix/pr69-bootstrap-rollback-claimfailopen` | P0 止血: bootstrap rollback + ClaimFailOpen fail-closed + ctx cancel final retry | ✅ 已合并 |
| #71 | `fix/pr-gb-governance-rules` | governance rules: G-2/G-4/G-6 + review fixes | ✅ 已合并 |
| #72 | `fix/pr-r1-handleresult-zero-value` | HandleResult 零值改 invalid (DispositionAck = iota+1) | ✅ 已合并 |
| #73 | `feat/005-archtest` | WM-12 archtest 边界守护 (5 条 LAYER 规则, 49 tests) | ✅ 已合并 |

## 待实施任务（跨框架分析后修订）

> 2026-04-11 跨框架分析: 对比 Watermill / NATS JetStream / Sarama / Temporal / MassTransit / Axon / go-micro+Kratos+go-zero 7 个框架后，识别出 2 个根因和修复方向。
> 分析文档: `docs/reviews/20260411-architecture-root-cause-analysis.md` + `docs/reviews/20260411-cross-framework-architecture-analysis.md`

### 架构修复 Phase 1: PermanentError 提升到 kernel（非破坏性，1h）

> 根因: PermanentError 定义在 adapters/rabbitmq，但 kernel/outbox.WrapLegacyHandler 和 runtime/eventbus 需要引用它。
> 对标: Temporal `NonRetryableApplicationError`、NATS `Term()`、Watermill `backoff.Permanent()`——均在核心层。

| 任务 | 文件 | 预估 |
|------|------|------|
| 定义 `outbox.PermanentError` | `kernel/outbox/outbox.go` | 15min |
| WrapLegacyHandler 检测 PermanentError → DispositionReject | `kernel/outbox/outbox.go` | 15min |
| adapters 加 type alias 保持兼容 | `adapters/rabbitmq/consumer_base.go` | 10min |
| InMemoryEventBus 检测 PermanentError | `runtime/eventbus/eventbus.go` | 15min |
| 补充测试 | `kernel/outbox/outbox_test.go`, `runtime/eventbus/eventbus_test.go` | 30min |

### 架构修复 Phase 2: EventRouter 引入（1 个接口变更，4h）

> 根因: Subscribe() 混合 setup 和 run，导致 100ms 竞态、goroutine 无监管、InMemoryEventBus 偏离。
> 对标: Watermill Router.AddHandler()+Run()、Sarama Setup/ConsumeClaim/Cleanup、Temporal Register+Start、Kratos transport.Server。
> **取代**: 旧 R-3 (bootstrap 统一包装) 和 R-5 (接入 live subscriptions)

| 任务 | 文件 | 预估 |
|------|------|------|
| 定义 `cell.EventRouter` 接口 (AddHandler) | `kernel/cell/registrar.go` | 30min |
| 实现 `eventrouter.Router` (Run/Running/Close) | `runtime/eventrouter/router.go`（新建） | 2h |
| 迁移 3 个 Cell 的 RegisterSubscriptions | `cells/{audit,config,access}-core/cell.go` | 1h |
| bootstrap 集成 EventRouter | `runtime/bootstrap/bootstrap.go` | 30min |
| 测试 | `runtime/eventrouter/router_test.go` | 1h |

### 架构修复 Phase 3: Checker 清理 + Receipt 加固（清理，3h）

> 合并旧 R-2 (删 Checker) + Receipt 加固。
> 对标: Temporal heartbeat 续租、NATS InProgress。

| 任务 | 文件 | 预估 | 依赖 |
|------|------|------|------|
| 删除 legacy Checker 路径 (原 R-2) | `adapters/rabbitmq/consumer_base.go`, `kernel/idempotency/idempotency.go` | 1.5h | Phase 1 |
| Receipt 加 sync.Once 防双调 | `adapters/redis/idempotency.go` | 30min | 无 |
| LeaseTTL vs RetryLoop 时长校验 | `adapters/rabbitmq/consumer_base.go` | 30min | 无 |
| 补 Release 单元测试 (ARCH-02) | `adapters/redis/idempotency_test.go` | 30min | 无 |

### PR-Rollout 剩余任务（独立，不受架构修复阻塞）

| 序号 | ID | 任务 | 文件 | 预估 | 依赖 |
|------|-----|------|------|------|------|
| R-4 | SOL-B-01 | lease 续租 — Receipt.Renew() 或后台 renew loop | `kernel/idempotency/idempotency.go`, `adapters/redis/idempotency.go`, `adapters/rabbitmq/consumer_base.go` | 4h | Phase 3 |

> ~~R-3 (bootstrap 统一包装)~~: **废弃** → 被 Phase 2 EventRouter 取代
> ~~R-5 (接入 live subscriptions)~~: **废弃** → 合入 Phase 2 Cell 迁移

### PR-B03: RabbitMQ 重连（独立，可全程并行）

| ID | 任务 | 文件 | 预估 |
|----|------|------|------|
| B-03 | connection.go setup 错误分类 (recoverable vs permanent) + anti-hot-loop backoff | `adapters/rabbitmq/connection.go` | 2h |

### PR-Cleanup: Kernel 架构整理（独立，可全程并行）

| 序号 | ID | 任务 | 文件 | 预估 | 依赖 |
|------|-----|------|------|------|------|
| K-1 | CS-AR-2 | Dependencies 精简 — 移除 Cells/Contracts 字段 + 冻结注释 | `kernel/cell/interfaces.go`, `kernel/assembly/assembly.go`, ~20 test files | 1h | Phase 2 |
| K-2 | CS-AR-3 | net/http ADR 注释 — 文档化 net/http 作为 stdlib 允许依赖 | `kernel/cell/registrar.go` | 15min | 无 |
| K-3 | F-OB-01 | BatchWriter 接口 + WriteBatchFallback — godoc 明确事务前置条件 | `kernel/outbox/outbox.go`, `adapters/postgres/outbox_writer.go` | 3h | 无 |
| K-4 | SOL-B-02 | Receipt 移回 idempotency 包 — 修复依赖方向 | `kernel/idempotency/idempotency.go`, `kernel/outbox/outbox.go`, adapters + tests | 3h | Phase 3 |

### 独立任务

| 任务 | 说明 | 状态 |
|------|------|------|
| Wave C 设计文档提交 | `docs/designs/20260411-wave-c-*.md` (3 份) | 待 commit |
| PR#68 review 文档提交 | `docs/reviews/20260411-pr68-*.md` (4 份) | 待 commit |
| 跨框架分析文档提交 | `docs/reviews/20260411-cross-framework-architecture-analysis.md` | 待 commit |
| ARCH-01 architect review 已修正 | 标注 RETRACTED (误报) | 已修改未提交 |

---

## 四角色审查 findings 分拣

### 已修复（PR#70）

| Finding | 来源 | 修复 |
|---------|------|------|
| Bootstrap rollback Step 6 | 用户发现 | PR#70 commit 1 |
| ClaimFailOpen 默认 fail-open | 用户发现 | PR#70 commit 1 |
| P0-1: ctx cancel final retry → DLX | 运维审查 | PR#70 commit 2 |
| P0-2: cell tests 忽略 RegisterSubscriptions error | 运维审查 | PR#70 commit 2 |
| P1-6: eventbus.md 旧 API 文档 | 产品审查 | PR#70 commit 3 |

### 已修复（PR#72）

| Finding | 来源 |
|---------|------|
| S-02: DispositionAck=0 零值陷阱 | 内核审查 |
| S-05: ClaimAcquired=iota(0) 零值 | 内核审查 |

### 归入 PR-Rollout

| Finding | 来源 | 对应任务 |
|---------|------|---------|
| S-01: Stale-lease commit after expiry | 内核审查 | R-4 (lease 续租) |
| S-03/P1-5: 100ms timer 竞态 RegisterSubscriptions | 内核+运维 | R-3 (bootstrap 统一包装) |
| S-04: redisReceipt 缺 sync.Once 防双调 | 内核审查 | R-4 附近 |
| S-07: RetryLoop 总退避可能超 LeaseTTL | 内核审查 | R-4 (文档化) |
| ARCH-02: redis Release 无专门测试 | 架构审查 | R-2 |
| ARCH-03: consumer_base Release 错误被 `_ =` 丢弃 | 架构审查 | R-2 |
| ARCH-04: mock releaseCalls 从不被 assert | 架构审查 | R-2 |
| 产品P0-1: WrapLegacyHandler 不检测 PermanentError | 产品审查 | R-3 或 R-5 |
| P1-1: 双重日志 retry-exhausted | 运维审查 | R-2 |
| P1-2: processDelivery 缺 consumer_group | 运维审查 | R-2 |
| P1-3: checkerReceipt.Commit no-op 文档 | 运维审查 | R-2 (删除时一并处理) |
| P1-4: DLX 测试不验证 x-death headers | 运维审查 | R-5 |
| S-08: DLX 声明无验证 DLQ 绑定 | 内核审查 | B-03 |

### 归入 PR-Cleanup

| Finding | 来源 | 对应任务 |
|---------|------|---------|
| S-06/SOL-B-02: idempotency→outbox 耦合 | 内核审查 | K-4 |
| ARCH-05: SubscriberWithMiddleware 是 concrete | 架构审查 | K-3 附近 |
| ARCH-06: InMemoryEventBus retry/DLQ 行为不一致 | 架构审查 | K-3 |
| ARCH-07: Cell RegisterSubscriptions 用 context.Background() | 架构审查 | R-3 |
| 产品 P2-1~P2-7 | 产品审查 | PR-Cleanup |

---

## 并行矩阵（修订后）

```
PR#70/71/72 合并后:

时间 →  T1           T2           T3            T4           T5
       ┌───────────┐
       │ Phase 1   │ PermanentError → kernel
       └────┬──────┘
       ┌────┴──────┐  ┌──────────┐  ┌──────────┐
       │ Phase 2   │  │ B-03     │  │ K-2      │
       │ EventRouter│  │ 重连     │  │ ADR      │
       └────┬──────┘  └──────────┘  └──────────┘
       ┌────┴──────┐  ┌──────────┐
       │ Phase 3   │  │ K-3      │
       │ 删Checker │  │ BatchWriter│
       └────┬──────┘  └──────────┘
       ┌────┴──────┐  ┌──────────┐
       │ R-4       │  │ K-4      │
       │ lease续租  │  │ Receipt包│
       └────┬──────┘  └──────────┘
       ┌────┴──────┐
       │ K-1       │ Dependencies 精简
       └───────────┘

Phase 1 → Phase 2 → Phase 3 串行（核心架构修复链）
B-03 / K-2 / K-3 全程无阻塞，可随时并行
R-4 依赖 Phase 3
K-4 依赖 Phase 3
K-1 依赖 Phase 2（EventRegistrar 接口变更）

废弃任务:
  ~~R-3 (bootstrap 统一包装)~~ → Phase 2 EventRouter 取代
  ~~R-5 (live subscriptions)~~ → Phase 2 Cell 迁移包含
```

## Worktree 状态

> 注意：本次会话因 CWD 漂移，部分 worktree 被嵌套创建到 `.claude/worktrees/pr-69-bootstrap-fix/.claude/worktrees/` 下。
> 下次会话建议清理后重建。

| Worktree | 分支 | 用途 | 状态 |
|----------|------|------|------|
| `.claude/worktrees/pr-69-bootstrap-fix` | `fix/pr69-bootstrap-rollback-claimfailopen` | PR#70 | 已推送 |
| `.claude/worktrees/pr-69-bootstrap-fix/.claude/worktrees/pr-gb-governance` | `fix/pr-gb-governance-rules` | PR#71 | 已推送（嵌套） |
| `.claude/worktrees/pr-69-bootstrap-fix/.claude/worktrees/pr-r1-zero-value` | `fix/pr-r1-handleresult-zero-value` | PR#72 | 已推送（嵌套） |
| `.claude/worktrees/pr-ga-auto-derive` | `fix/pr-ga-auto-derive` | PR#67 | 已合并，可删 |
| `.claude/worktrees/pr-d1-kernel-interface` | `fix/pr-d1-kernel-interface` | PR#68 | 已合并，可删 |

---

## 设计文档产出

| 文件 | 内容 |
|------|------|
| `docs/designs/20260411-wave-c-architecture-design.md` | 架构师设计（CS-AR-2/3, F-OB-01） |
| `docs/designs/20260411-wave-c-product-review.md` | 产品审查（CONDITIONAL PASS） |
| `docs/designs/20260411-wave-c-kernel-review.md` | 内核守卫审查（PASS） |
| `docs/reviews/20260411-pr68-architect-review.md` | 架构审查（ARCH-01 RETRACTED, ARCH-02~08） |
| `docs/reviews/20260411-pr68-security-review.md` | 安全审查（S-01~S-08） |
| `docs/reviews/20260411-pr68-ops-test-review.md` | 运维+测试审查（P0-1/2, P1-1~5） |
| `docs/reviews/20260411-pr68-product-dx-review.md` | 产品+DX 审查（P0-1, P1-1~6, P2-1~7） |
