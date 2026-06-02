# Implementation Plan — CQRS Projection lifecycle harness (epic #1100)

> speckit-plan 形态实施计划。Spec：[202605261600-1100-cqrs-projection-harness-spec.md](./202605261600-1100-cqrs-projection-harness-spec.md)
>
> **行数预算约束**：每个 PR ≤ 2000 行（含测试 + docs；excluded：golden 文件、generated 产物）。预算 = `git diff --stat` 数字。

## 0. 总览

epic 拆为 **7 个 PR**，按依赖串行 + 局部并行：

```
PR-00 ADR + skeleton ─┬─> PR-01 Coordinator + mem store ────> PR-02 PG store + conformance
                     │                                                     │
                     │                                                     v
                     └─> PR-04 cellgen kind:projection ───> PR-03 rebuild + metrics + readyz
                                                                       │
                                                                       v
                                                              PR-05 Governance Hard
                                                                       │
                                                                       v
                                                  PR-06 orderprojection reference + ADR closeout
```

并行机会：PR-02 ⊥ PR-04（一个走 adapter 一个走 codegen，不冲突）；其余串行。

## 1. PR-00 — ADR + kernel/projection skeleton (~800 行)

### Scope

| 项 | 路径 | 行数估算 |
|----|------|---------|
| ADR `202605261620-adr-cqrs-projection-lifecycle-harness.md` | `docs/architecture/` | ~350 |
| `kernel/projection/doc.go` + 包 godoc | `kernel/projection/` | ~80 |
| `kernel/projection/types.go`（Apply / CheckpointStore / Coordinator interface 声明，**无实现**） | `kernel/projection/` | ~120 |
| `kernel/projection/cell_marker.go`（sealed CellCheckpointStore marker，对齐 outbox.CellPublisher 范式） | `kernel/projection/` | ~60 |
| `kernel/projection/phase.go`（**5 成员** Phase enum：PhaseLive + rebuild **4 相** Stop/Reset/Replay/Catchup，对标 Axon）+ PROJECTION-STATE-PHASE-FROZEN-01 AST const-set 锁（PR-00 即 green） | `kernel/projection/` | ~90 |
| spec / plan 文档 | `docs/plans/` | 已落 |
| backlog 登记 (#1100 评论) | gh | — |
| **预算总计** | | ~700 行 + ADR |

### ADR 必决项（spec §7 Q1-Q5）

逐项**列出选项 + 论证 + 预填决议 + 风险 + 回滚条件 + 对标框架引用**：

- Q1 → A（caller-provided tx + harness 内部 SaveOffset，与 outbox.Writer 同范式；`ref:` Axon JdbcTokenStore §同 tx 提交模型）
- Q2 → A（`apply(ctx, event) error` —— **ambient tx 经 ctx，无显式 tx handle 参数**（`persistence.TxHandle` 不存在、与 `PG-REPO-AMBIENT-TX-01` 冲突，见 ADR §3 Q2 Correction）；`ref:` Marten Async `IDocumentOperations` 形态；明确否决 eventhorizon `Project(ctx, evt, entity) (entity, error)` 形态）
- Q3 → A（单 slice 单 projection，v1 scope）
- Q4 → A（snapshot 不入 v1；触发条件：winmdm Stage 1 rebuild 全量实测 ≥ 30min；`ref:` Axon snapshot 设计动机 vs GoCell 单 cell event 流量级差异）
- **Q5 → A**（v1 单 pod 模式；schema 预留 `owner TEXT` 列；多 pod 并发安全由上层 leader election 保证；`ref:` Axon `token_entry.owner` 字段，v1.1 在此列上实现 pessimistic claim）

**新增章节**：

- **§对标** (CLAUDE.md 强制要求)：复制 spec §0 对标表 + 逐项引用 deeper：
  - Axon TokenStore 4.4 / `@ResetHandler` / Metrics
  - Marten async-daemon + projection lifecycle
  - eventhorizon `projector` 包（明确否决论证）
  - Watermill `components/cqrs`（GoCell 已有的 outbox + ConsumerBase 已对齐 Watermill EventProcessor，本 epic 是其上的 projection 扩展）
  - Commanded read-model-projections (Ecto.Multi)
- **GAP-8 seal 重审记录**：明文记录「harness 收窄 seal 到『框架不规定读模型表』；apply 函数 + schema 仍归业务」。喂给后续 winmdm Stage 1 六席位重审会议。

### Constraint fanout（PR 描述强制 matrix）

```
Contract: kernel/projection.{Apply, CheckpointStore, Coordinator}
Change: 引入 lifecycle harness 接口骨架（无实现）
Implementations: 本 PR 无（接口先行）；PR-01 落 mem, PR-02 落 postgres
Conformance test: kernel/projection/projectiontest.RunCheckpointConformance（PR-01 落实现）
Repro: bash hack/verify-archtest.sh
Dependent contracts (governance scan): kind:projection contract.yaml schema (PR-05 升 Hard)
```

### Done definition

- [ ] ADR 文件评 owner approval（review by architect agent）
- [ ] `go build ./...` 通过
- [ ] meta-archtest `PROJECTION-STATE-PHASE-FROZEN-01`（AST const-set + String-arm 锁，5 成员 enum）—— PR-00 即 **green**（仓库无 fail-pending 约定；enum 已在本 PR 声明，无需等 PR-03）
- [ ] backlog #1100 评论同步 ADR 文件路径

### 风险

- Q4=A 风险：若 winmdm Stage 1 实测 rebuild 全量耗时不可接受（≥ 30min），snapshot 需要 v1.1 紧急补——ADR 必须留触发条件文档化（不留 silent backlog）
- L1 finding 复杂度：4 个开放问题打包进单 ADR——若 review 期间发现张力间相互影响（如 Q1=A 强依赖 Q2=A），需重做拆分

---

## 2. PR-01 — Coordinator + mem CheckpointStore (~1800 行)

### Scope

| 项 | 路径 | 行数估算 |
|----|------|---------|
| `kernel/projection/coordinator.go`（Coordinator struct + Subscribe + 消费循环 + checkpoint write） | `kernel/projection/` | ~300 |
| `kernel/projection/coordinator_test.go`（cold-start / crash recovery / out-of-order / fail-closed） | `kernel/projection/` | ~500 |
| `kernel/projection/memstore.go`（in-memory CheckpointStore） | `kernel/projection/` | ~100 |
| `kernel/projection/memstore_test.go` | `kernel/projection/` | ~150 |
| `kernel/projection/projectiontest/conformance.go`（RunCheckpointConformance shared test） | `kernel/projection/projectiontest/` | ~200 |
| `tools/archtest/projection_apply_hook_funnel_test.go`（PROJECTION-APPLY-HOOK-FUNNEL-01 预填 stub，等 PR-04 完成 cellgen 时激活） | `tools/archtest/` | ~150 |
| `tools/archtest/projection_checkpoint_tx_bound_test.go`（PROJECTION-CHECKPOINT-TX-BOUND-01） | `tools/archtest/` | ~200 |
| Service `gocell:"required"` tag + `validateRequired` 生成 | `kernel/projection/` | ~80 |
| **预算总计** | | ~1680 行 |

### 关键设计点（落 ADR §决议）

> **以 ADR `202605261620` §3 为权威。** 本节早期草稿写的 `persistence.TxHandle`
> 显式参数已被 PR-00 ADR Q1/Q2 收敛为 **ambient-tx**（该类型不存在，且与
> `PG-REPO-AMBIENT-TX-01` 冲突）。下面是已对齐 PR-00 冻结签名的形态。

```go
// kernel/projection/types.go (declared in PR-00, Coordinator implemented in PR-01)
type Apply func(ctx context.Context, event outbox.Entry) error

type CheckpointStore interface {
    LoadOffset(ctx context.Context, cellID, projectionID string) (int64, error)
    SaveOffset(ctx context.Context, cellID, projectionID string, offset int64) error
}

// Sealed marker (cells/* 持有，composition root wrap) — mirrors outbox.CellPublisher
type CellCheckpointStore interface {
    CheckpointStore
    sealedCellCheckpointStore()  // sealed marker method
}

// Coordinator entry point (called by cellgen-derived wiring, NOT business code)
func (c *Coordinator) Subscribe(
    ctx context.Context,
    spec contractspec.ContractSpec,
    projectionID string,
    apply Apply,
    opts ...Option,
) error
```

**内部消费 handler 实现**（CellTx 包裹 apply + SaveOffset 同源 commit；tx 为 ambient）：

```go
// 伪码 — tx 不作显式参数，apply/SaveOffset 经 ctx 内 persistence.TxFromContext 取
func (c *Coordinator) buildHandler(cellID, projectionID string, apply Apply) outbox.EntryHandler {
    return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
        err := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
            current, err := c.store.LoadOffset(txCtx, cellID, projectionID)
            if err != nil { return err }
            pos := c.cursor.Position(entry)   // 由 PR-01 replay 源提供（outbox.Entry 无 Seq 字段）
            if pos <= current {
                return nil  // exactly-once: 已应用过，skip
            }
            if err := apply(txCtx, entry); err != nil { return err }
            return c.store.SaveOffset(txCtx, cellID, projectionID, pos)
        })
        // 错误分类 → Disposition（permanent error 经 outbox.NewPermanentError 包裹）
        if err == nil { return outbox.Ack() }
        if isPermanent(err) { return outbox.Reject(err) }
        return outbox.Requeue(err)
    }
}
```

### archtest 三件套（AI-robust 章程要求）

| ID | 评级 | 范式 |
|----|------|------|
| PROJECTION-APPLY-HOOK-FUNNEL-01 | Hard 下游 / Medium 上游 | `Coordinator.Subscribe` 仅允许在 generated 文件 + `kernel/projection/coordinator.go` 自身 + `_test.go` 调用；business 包内的手写 callsite fail（与 `HEALTHZ-WRITE-01` 同范式） |
| PROJECTION-CHECKPOINT-TX-BOUND-01 | Medium | `CheckpointStore.SaveOffset` 实现必须经 `persistence.TxFromContext(ctx)` 取 ambient tx；裸 `*sql.Tx` 参数 / `db.Exec` 形态 fail（与 outbox.Writer 同范式） |
| PROJECTION-STATE-PHASE-FROZEN-01 | Medium | AST 锁 `Phase` enum const 集 + String arms —— v1 为 5 成员 `{PhaseLive, PhaseStopped, PhaseReset, PhaseReplay, PhaseCatchup}`，新增需 PR 加 const + 更新 golden（PR-00 已 green） |

### Done definition

- [ ] `go test ./kernel/projection/...` 全绿
- [ ] coverage ≥ 90%（kernel 标准）
- [ ] conformance test 模板可被 PR-02 PG adapter import
- [ ] meta-archtest 三件套挂上（PROJECTION-APPLY-HOOK-FUNNEL-01 仍 fail-pending 等 PR-04 cellgen 接入）

### 风险

- L2 finding 风险：`Coordinator.Subscribe` 当前形态 + Apply 签名是 PR-00 ADR 直接落子；review 发现 Q2 选项需调整时**必须 amend ADR 并同 PR 修改**（不接受"先合再改"）
- 行数风险：1680 / 2000，若加 fail-open 模式 + verbose readyz 接入会超预算 → 拆 fail-open 行为到 PR-03（rebuild + metrics 同 PR）

---

## 3. PR-02 — PG CheckpointStore adapter (~1500 行)

### Scope

| 项 | 路径 | 行数估算 |
|----|------|---------|
| `adapters/postgres/projection_checkpoint_store.go` | `adapters/postgres/` | ~200 |
| migration `0NNN_create_projection_checkpoints.sql`（schema：`(cell_id, projection_id, offset, updated_at)` PK = `(cell_id, projection_id)`） | `adapters/postgres/migrations/` | ~40 |
| migration test fixture | `adapters/postgres/...` | ~80 |
| `adapters/postgres/projection_checkpoint_store_test.go`（testcontainers + 真实 PG） | `adapters/postgres/` | ~400 |
| conformance test runner（调 `projectiontest.RunCheckpointConformance`） | `adapters/postgres/` | ~80 |
| `kernel/projection/projectiontest/conformance.go` 补 PG-specific 测试 case | `kernel/projection/projectiontest/` | ~150 |
| `WrapCheckpointStoreForCell` 在 `kernel/projection/cell_marker.go` 落地（sealed marker wrap） | `kernel/projection/` | ~50 |
| `cmd/corebundle` wiring 接入 + bootstrap option `WithProjectionCheckpointStore` | `runtime/bootstrap/` + `cmd/corebundle/` | ~250 |
| corebundle test 补全 | `cmd/corebundle/` | ~200 |
| **预算总计** | | ~1450 行 |

### 关键 schema（对标 Axon `token_entry`）

```sql
CREATE TABLE projection_checkpoints (
    cell_id        TEXT NOT NULL,
    projection_id  TEXT NOT NULL,
    offset_seq     BIGINT NOT NULL DEFAULT 0,
    -- v1 预留字段：对标 Axon token_entry.owner，v1 不读不写
    -- v1.1 接 pessimistic claim 时启用（owner = pod ID / process ID）
    owner          TEXT NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (cell_id, projection_id)
);
```

`SaveOffset` 走 `INSERT ... ON CONFLICT (cell_id, projection_id) DO UPDATE SET offset_seq = EXCLUDED.offset_seq, updated_at = NOW()`，且必须用 caller-provided tx（由 `persistence.TxFromContext(ctx)` 拿到 `*sql.Tx`）—— **不可建独立连接**（exactly-once 关键）。

**owner 列 v1 行为约束**（archtest 强制）：

- v1 PG adapter 的 `INSERT/UPDATE` 语句**不得包含 owner 列**（避免静默写脏数据）
- v1.1 启用 pessimistic claim 时 schema 不变，只改 query——`owner` 列已存在，零 migration cost
- archtest `PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01` Medium：扫 SQL 字符串字面量 reject 含 `owner =` / `owner,` 的 INSERT/UPDATE 写入路径（仅在 v1 范围；v1.1 启用时 archtest 同 PR 删除）

### Constraint fanout matrix（PR 描述必填）

```
Contract: CheckpointStore interface
Change: 新 PG adapter 实现 + migration
Implementations: [x] memstore (PR-01)  [x] PG (本 PR)  [ ] fake (test-only, 走 mem)
Conformance test: kernel/projection/projectiontest.RunCheckpointConformance
Repro: go test -tags=integration ./adapters/postgres/...
Dependent contracts: 无（schema 是新增表）
Invariant inventory (DROP COLUMN 不适用)
```

### Done definition

- [ ] testcontainers integration test 通过（local 必须跑 `make test-integration` 等价命令）
- [ ] migration golden snapshot 落
- [ ] corebundle real 模式 wiring（demo 模式继续用 memstore）
- [ ] PROJECTION-CHECKPOINT-TX-BOUND-01 archtest 转 green（PG impl 形态匹配）

### 风险

- L2 finding 风险：migration 命名编号需查 `adapters/postgres/migrations/` 当前最大编号 + 1
- 行数风险：corebundle wiring 容易扩，限制只接 PG（demo 继续 mem，不要试图同 PR 改 demo）

---

## 4. PR-03 — Rebuild state machine + metrics + readyz probe (~1800 行)

### Scope

| 项 | 路径 | 行数估算 |
|----|------|---------|
| `kernel/projection/rebuild.go`（state machine **4 相**：Stop → Reset → Replay → Catchup，对标 Axon；`OnReset(ctx, tx) error` business hook 对标 Axon `@ResetHandler`） | `kernel/projection/` | ~280 |
| `kernel/projection/rebuild_test.go` | `kernel/projection/` | ~400 |
| `kernel/projection/metrics.go`（三个 metric 声明 + Register 函数） | `kernel/projection/` | ~120 |
| `kernel/projection/metrics_test.go` | `kernel/projection/` | ~150 |
| `kernel/projection/probe.go`（`<cell>_projection_<name>_ready` ReadyProbeName typed const + ProbeSet 实现） | `kernel/projection/` | ~100 |
| `kernel/projection/probe_test.go` | `kernel/projection/` | ~120 |
| `runtime/projection/rebuild_handler.go`（internal HTTP endpoint `POST /internal/v1/<cell>/projection/<name>/rebuild`） | `runtime/projection/` | ~150 |
| `runtime/projection/rebuild_handler_test.go` | `runtime/projection/` | ~200 |
| `contracts/http/projection/rebuild/v1/`（contract.yaml + payload schema） | `contracts/` | ~80 |
| PROJECTION-STATE-PHASE-FROZEN-01 保持 green（PR-00 已锁 5 成员 enum；PR-03 加 rebuild transition table 不改 enum 集，无新 archtest） | `tools/archtest/` | ~0 |
| metrics label 入 `kernel/observability/metrics.MustValidateLabels` 注册 | `kernel/observability/...` | ~50 |
| ops doc 更新 `docs/ops/`（projection_event_replay_lag_seconds alert example） | `docs/ops/` | ~80 |
| **预算总计** | | ~1800 行 |

### Metric 命名（registered 一次，多 projection 共享）

```
projection_event_replay_lag_seconds{cell, projection}    gauge   # 时间维度：read-model 落后多久
projection_rebuild_duration_seconds{cell, projection}     histogram
projection_pending_events{cell, projection}              gauge   # 记录维度：未应用事件数（source head − applied checkpoint）
```

> `projection_pending_events` 语义 = **未处理事件数 = replay 源 head 位置 − 已提交
> checkpoint**（积压深度，record 维度，replay_lag_seconds 的记录数版本）。原名
> `projection_event_log_length`（"日志长度"）易误读为日志总量，已按 Scenario F
> 「确认积压来源」语义 rename。

`cell` label 直接对齐 [observability.md HTTP Metrics cell Label 段](.claude/rules/gocell/observability.md) 既有约定。

### Readyz probe name typed funnel

> 经 `kernel/healthz.NewProbeName(...)` 构造，**无 `ReadyProbeName` 类型、无裸
> `healthz.ProbeName(string)` cast**（裸 cast 绕过 `PROBENAME-SEALED-FUNNEL-01`）。
> 名 `<cell>_projection_<name>_ready`，长度预算 `len(cellID)+len(projectionID) ≤ 46`
> （cap 64 − 固定段 18）。详见 ADR §5「Readyz probe name」。

```go
// kernel/projection/probe.go
func ProjectionReadyProbeName(cellID, projectionID string) (healthz.ProbeName, error) {
    // typed funnel — 唯一构造入口 healthz.NewProbeName（含长度/字符校验，fail-fast）
    return healthz.NewProbeName(cellID + "_projection_" + projectionID + "_ready")
}
```

### Done definition

- [ ] state machine **4 相**（Stop / Reset / Replay / Catchup）+ transitions 完整测试覆盖（含错误恢复路径，10+ transitions）
- [ ] `OnReset(ctx, tx) error` business hook 接口签名通过 ADR §对标 Axon `@ResetHandler` 论证
- [ ] `POST /internal/v1/<cell>/projection/<name>/rebuild` contract test
- [ ] metrics 三件套 fired in 集成测试
- [ ] readyz probe 在 lag > threshold 时返回 unhealthy
- [ ] PROJECTION-STATE-PHASE-FROZEN-01 保持 green（PR-00 已 AST 锁 5 成员 enum；PR-03 加 rebuild transition table 不改 enum 集）
- [ ] **rebuild 期 business read 不阻塞**——对标 Axon / Marten / Commanded 业界共识；harness 仅暴露 `Phase()` 让业务自定决策（默认不返回 503），契约文档明示
- [ ] Stop 相 graceful 退出验证：consumer 循环退出 + checkpoint claim 释放 + 新 event Requeue 至下一启动周期

### 风险

- L2 finding 风险：HTTP endpoint contract 必须走 `kind: command`（control-plane action），不是 `kind: http`——确认 contract metadata 正确
- 行数风险：1800 / 2000，rebuild test 复杂度高（state machine 10+ transitions），若过线拆 readyz probe → PR-05 合并
- **业界共识争议**：rebuild 期 read 不阻塞与 spec §4 Scenario C 旧版"503 阻塞"冲突。最终决议在 ADR §Q4-补充章节明示：harness 默认不阻塞（业界共识），但暴露 `Phase()` 供业务自选 503

---

## 5. PR-04 — cellgen `kind: projection` 派生 (~1800 行)

### Scope

| 项 | 路径 | 行数估算 |
|----|------|---------|
| `tools/codegen/cellgen/builder.go` 扩展（projection slice 检测 + ProjectionSpec 构建） | `tools/codegen/cellgen/` | ~150 |
| `tools/codegen/cellgen/templates/cell.tmpl` 扩展（projection wiring 渲染段） | `tools/codegen/cellgen/templates/` | ~50 |
| `tools/codegen/cellgen/templates/projection_slice.tmpl`（slice handler 派生：apply 函数 stub + Coordinator 调用） | `tools/codegen/cellgen/templates/` | ~100 |
| `tools/codegen/cellgen/builder_test.go` 扩展 | `tools/codegen/cellgen/` | ~300 |
| `tools/codegen/cellgen/scaffold_bundle.go` 扩展（projection slice scaffold 加 `internal/projection/` 起始 doc.go） | `tools/codegen/cellgen/` | ~100 |
| golden 文件更新（scaffold-projection-cell.golden） | `tools/codegen/cellgen/testdata/` | ~200 |
| cellgen e2e test：从 fixture cell.yaml + slice.yaml `kind: projection` 派生完整 cell_gen.go + slice_gen.go | `tools/codegen/cellgen/` | ~400 |
| `kernel/metadata` 扩展：`ContractMeta.Kind == "projection"` 时增加 `Projection.ID / Projection.RebuildEndpoint` 派生字段 | `kernel/metadata/` | ~100 |
| metadata parser 测试 | `kernel/metadata/` | ~200 |
| PROJECTION-APPLY-HOOK-FUNNEL-01 archtest 转 green（cellgen 生成的 wiring 满足 funnel 形态） | `tools/archtest/` | ~50 |
| docs `docs/guides/cell-development-guide.md` 补 projection 章节 | `docs/guides/` | ~150 |
| **预算总计** | | ~1800 行 |

### 派生模板伪码（slice.yaml `kind: projection`）

```go
// 生成的 slice_gen.go
package orderprojection

import (
    "github.com/ghbvf/gocell/kernel/projection"
    ordercreated "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1"
)

func (s *Service) Subscribe(ctx context.Context, coord *projection.Coordinator) error {
    return coord.Subscribe(ctx, ordercreated.SubscribeSpec(), "ordersummary", s.applyOrderCreated)
}

// hand-written hook in service.go:
//   func (s *Service) applyOrderCreated(ctx context.Context, evt outbox.Entry) error { ... }  // tx ambient via ctx
```

### contractUsages slice.yaml 新字段

```yaml
contractUsages:
  - contract: event.order-created.v1
    role: subscribe
    handler: applyOrderCreated   # 已有
    projection: ordersummary     # 新增：声明属于哪个 projection
```

### 测试纪律

PR-04 触碰 `.claude/skills/*`：无（仅 codegen + metadata），不触发 skill-test-discipline。

`tools/codegen/cellgen/testdata/` golden 必须人工 review +`make verify` 跑 archtest 全集。

### Done definition

- [ ] `gocell generate cell` 对 projection slice 派生 wiring + 不破坏既有 event subscribe slice
- [ ] cellgen scaffold golden 更新通过
- [ ] PROJECTION-APPLY-HOOK-FUNNEL-01 转 green（business 代码内手写 `Coordinator.Subscribe` 调用 fail）
- [ ] dev guide 章节包含 minimal projection slice 示例

### 风险

- L3 finding 风险：cellgen 改动覆盖三层（builder / template / scaffold），任一处漂移会让 generated code 编译失败但 archtest 通过
- 行数风险：1800 / 2000，e2e test 行数大，若超预算把 dev guide 拆 PR-06

---

## 6. PR-05 — Governance PROJECTION-CONSISTENCY-01 升 Hard (~1000 行)

> **SUPERSEDED (gh #960 delivered 2026-06-02).** 本节的「parser load-time
> `jsonschema.Validate` + `PROJECTION-CONSISTENCY-PARSE-TIME-01` archtest」方案在
> 落地时被**否决**：parse-time 校验按 AI-robust 章程是 **Medium** runtime guard（违反
> 可表达、不覆盖 in-memory `ContractMeta{}` 向量），不构成 Hard。实际交付为
> **contractgen codegen funnel**：`kind: projection` 契约生成的 `types_gen.go` 携带
> `const _ = uint(cellvocab.<level> - cellvocab.L3)`，L0/L1/L2 编译期 uint 溢出 →
> 不可构建（Hard 主门控，codegen:true）；governance rule 留作 codegen:false +
> in-memory 的 Medium 兜底。无 parser 改动、无 kernel-isolation 放宽、无性能基准（不
> 在 parse 热路径）、无 schema 改动。权威记录见 ADR `202605261620` §Amendment
> 2026-06-02 #960。下方原始 Scope/任务保留作历史脉络，不再执行。

### Scope

| 项 | 路径 | 行数估算 |
|----|------|---------|
| `kernel/metadata/parser.go`：contract load 路径增加 `jsonschema.Validate` 调用 | `kernel/metadata/` | ~80 |
| `kernel/metadata/parser_test.go` 扩展（L0/L1/L2 contract YAML 在 parse 时 reject） | `kernel/metadata/` | ~200 |
| `kernel/governance/rules_projection_consistency.go` godoc 升级（Medium → Hard） | `kernel/governance/` | ~30 |
| 移除 `gocell validate` 内 governance rule 中 schema enum 部分（schema 已是 parse-time gate）—— **同 PR 完成 fanout** | `kernel/governance/` | ~50 |
| ADR `202605261620-adr-cqrs-projection-lifecycle-harness.md` 补 §Hard upgrade 章节 | `docs/architecture/` | ~80 |
| 性能基准测试（contract parse 全套耗时增量 ≤ 5%） | `kernel/metadata/` | ~150 |
| archtest `PROJECTION-CONSISTENCY-PARSE-TIME-01`（parser 必调 jsonschema.Validate；调用点身份锁定） | `tools/archtest/` | ~150 |
| 内部 fixture 修正（test fixture 有 `kind: projection` + low level 的批量 grep + 修正） | `kernel/governance/` / fixtures | ~100 |
| backlog #960 评论关闭 | gh | — |
| **预算总计** | | ~840 行 |

### 关键性能验证

issue #960 trigger 条件「评估 parser load-time `jsonschema.Validate` 的增量成本」必须做：

```bash
go test -bench=BenchmarkLoadContract -benchmem ./kernel/metadata/...
```

before/after 对比，写进 ADR §Hard upgrade。增量 > 5% 时考虑 lazy validation（只对 `kind: projection` validate）。

### Constraint fanout matrix

```
Contract: contract.schema.json projection if/then enum
Change: schema enum 从 test-layer 升 parse-time gate
Implementations: [x] kernel/metadata.LoadContract (本 PR)
Conformance test: kernel/metadata.TestProjectionConsistencyLevelSchemaEnum (已存在，行为不变)
Repro: go test ./kernel/metadata/...
Dependent contracts (governance scan): 全部 kind:projection contract.yaml (orderprojection)
```

### Done definition

- [ ] parser benchmark 通过（< 5% regression）
- [ ] governance rule 文件 godoc 标 Hard
- [ ] archtest 守 parser callsite
- [ ] backlog #960 关闭，gh comment 引用本 PR

### 风险

- L1 finding 风险：parse-time validate 容易漏边界 case（in-memory ProjectMeta fixture bypass）—— governance rule 不能完全删除，仅 schema enum 部分由 parser 承接

---

## 7. PR-06 — orderprojection reference + ADR closeout (~1500 行)

### Scope

| 项 | 路径 | 行数估算 |
|----|------|---------|
| `examples/todoorder/cells/ordercell/internal/orderprojection/service.go` 重写：删 in-memory log + store.nextSeq + Rebuild() 自实现，接入 harness | `examples/todoorder/` | -200/+150 |
| 同 service 增加 `applyOrderCreated` / `applyOrderStatusChanged` apply 函数（业务 read-model 用 in-memory 仍可——demo 不强制 PG） | `examples/todoorder/` | ~250 |
| `examples/todoorder/cells/ordercell/slices/orderprojection/` 加 slice.yaml `kind: projection` 声明 | `examples/todoorder/` | ~40 |
| `examples/todoorder/cells/ordercell/slices/orderprojectionrebuild/` 删除（rebuild 由 harness HTTP endpoint 接管） | `examples/todoorder/` | -120 |
| 重新跑 cellgen 生成新 `cell_gen.go` / `slice_gen.go` | `examples/todoorder/` (generated) | excluded |
| 集成测试：`examples/todoorder` smoke 包括 cold-start / crash recovery / rebuild | `examples/todoorder/...` | ~400 |
| ADR closeout 文档 `docs/plans/archive/202605261600-1100-cqrs-projection-harness-implementation-plan-closeout.md` | `docs/plans/archive/` | ~150 |
| backlog #834 / #1079 / #961 / #960 / #1100 全部关闭 | gh | — |
| dev guide section "L3 Projection Reference" 链回 orderprojection | `docs/guides/` | ~80 |
| **预算总计** | | ~1140 行 |

### orderprojection 改造对比

| 字段/方法 | 改造前（手撕） | 改造后（harness reference） |
|----------|-------------|---------------------------|
| `store.nextSeq` | 自管 | **删** — checkpoint 由 harness |
| `store.log` | 自管 in-memory append-only log | **删** — replay 由 harness 从 event store 拉 |
| `HandleOrderCreated(ctx, entry) HandleResult` | service handler | **改名** `applyOrderCreated(ctx, entry, tx) error` |
| `Rebuild(ctx) RebuildReport` | 自实现 in-memory log replay | **删** — rebuild 由 harness state machine 编排 |
| `orderprojectionrebuild` slice | 独立 HTTP control-plane slice | **删** — rebuild endpoint 由 harness 提供 |

### Done definition

- [ ] examples/todoorder e2e smoke 全绿
- [ ] `gocell validate examples/todoorder` 通过
- [ ] backlog 5 issues 全部关闭
- [ ] closeout 文档归档到 `docs/plans/archive/`
- [ ] dev guide 更新

### 风险

- L1 finding 风险：orderprojection 是 spec example contract test 来源，删除 in-memory log 后部分测试需要重写（demo 业务 read-model 仍 in-memory，但 checkpoint 走 mem store）

---

## 8. 跨 PR 总览

### 8.1 行数预算汇总

| PR | 行数（含测试） |
|----|--------------|
| PR-00 | ~800 |
| PR-01 | ~1800 |
| PR-02 | ~1500 |
| PR-03 | ~1800 |
| PR-04 | ~1800 |
| PR-05 | ~1000 |
| PR-06 | ~1500 |
| **总计** | **~10200 行**（含测试 + docs，excluded golden / generated） |

### 8.2 archtest 新增清单

| ID | PR | 评级 | 形态 |
|----|----|------|------|
| PROJECTION-APPLY-HOOK-FUNNEL-01 | PR-01 stub / PR-04 green | Hard 下游 + Medium 上游 | callsite 唯一性 |
| PROJECTION-CHECKPOINT-TX-BOUND-01 | PR-01 stub / PR-02 green | Medium | ambient-tx 形态（SaveOffset 经 TxFromContext，禁裸 db.Exec） |
| PROJECTION-STATE-PHASE-FROZEN-01 | **PR-00 green** | **Medium** | AST const-set + String-arm 锁（5 成员 enum；Go enum 不可 reflect 成集，AST/golden 锁是天花板） |
| PROJECTION-CONSISTENCY-PARSE-TIME-01 | PR-05 | Hard 下游 | parser callsite identity |
| **PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01** | **PR-02** | **Medium** | **SQL 字面量扫 reject owner 写入**（v1 范围；v1.1 启用 claim 时同 PR 删除） |

每个 archtest 必须**同 PR 内**完成「godoc 范围说明 + 反向自检」两件套（AI-robust 章程要求）。

### 8.3 ADR 关联

主 ADR：`docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md`（PR-00 落，PR-05 amend）。

ADR 章节：
1. 上下文 + 范围（lifecycle harness ≠ GAP-8 read-model）
2. **§对标**（CLAUDE.md 强制：Axon / Marten / eventhorizon / Commanded / Watermill 引用 + `ref:` 链接）
3. Q1-Q5 决议 + 论证（每项含对标依据）
4. GAP-8 seal 重审记录（喂 winmdm Stage 1）
5. 性能基准（PR-05 amend）
6. 威胁矩阵（exactly-once / crash recovery / rebuild 期 read 一致性 / out-of-order replay / fail-closed / GAP-8 边界 / **多 pod 并发安全 v1 边界**）
7. AI-robust 评级（funnel 双向锁评级 + Hard 范本归属）

### 8.4 风险与回滚

| 风险 | 触发条件 | 回滚 |
|------|---------|------|
| Q1=A 决议错误 | review 发现 caller-tx + harness-SaveOffset 在跨 cell 投影下死锁 | PR-00 ADR amend → PR-01 重写 Coordinator |
| snapshot v1 缺失实际阻塞 winmdm | winmdm Stage 1 实测 rebuild 全量 ≥ 30min | 新 epic v1.1 紧急加 snapshot store |
| cellgen 派生破坏 existing event consumer | PR-04 实测 examples/todoorder 既有 event subscribe slice 编译失败 | builder.go 路径 isolation：`kind: projection` only |
| 行数预算超 2000 | PR-03 / PR-04 实际行数超预算 | 拆子 PR（PR-03 → PR-03a metrics / PR-03b rebuild；PR-04 → PR-04a builder / PR-04b template） |

### 8.5 ship 调度建议（每 PR）

| PR | ship Level | reviewer 数 | 理由 |
|----|-----------|------------|------|
| PR-00 | L3 | 6 | ADR + 范式设定，需 architect + product-manager + kernel-guardian 联审 |
| PR-01 | L3 | 3 | kernel 层核心，coverage ≥ 90% 强约束 |
| PR-02 | L2 | 2 | adapter 层，conformance test 兜底 |
| PR-03 | L3 | 3 | state machine + HTTP contract + ops 多面 |
| PR-04 | L3 | 6 | cellgen 改造影响所有未来 projection cell |
| PR-05 | L2 | 2 | parser + governance，影响面集中 |
| PR-06 | L2 | 2 | examples 重构，已有 smoke 兜底 |

### 8.6 依赖前置

- PR-00 必须先 merge（设 baseline）
- PR-01 必须先 PR-00
- PR-02 可与 PR-03/PR-04 并行（不冲突文件）
- PR-03 必须先 PR-01
- PR-04 必须先 PR-01（依赖 Coordinator API）
- PR-05 独立（可任意时机插入，建议 PR-04 之后给 cellgen 反应窗口）
- PR-06 必须最后（依赖 PR-01..05 全 merge）

任务级清单见 [tasks 文件](./202605261600-1100-cqrs-projection-harness-tasks.md)。
