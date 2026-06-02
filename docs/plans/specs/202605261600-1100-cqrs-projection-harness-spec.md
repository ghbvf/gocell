# Spec — CQRS Projection lifecycle harness (epic #1100)

> speckit-specify 形态规格。**只描述「做什么 / 为什么 / 验收条件」，不描述实现。**
> 实施切分与 PR 边界见 [202605261600-1100-cqrs-projection-harness-implementation-plan.md](./202605261600-1100-cqrs-projection-harness-implementation-plan.md)。

## 0. 对标 (open-source benchmarks)

CLAUDE.md 强制要求新模块先做开源对标。本节是 explorer agent 的提炼，详细论证在 [PR-00 ADR §对标](#) 章节复制；本 spec 只记录直接影响 spec 决议的结论。

| 框架 | apply hook 签名 | checkpoint 存储 | rebuild 编排 | exactly-once 模式 | replay 期 read |
|------|---------------|--------------|------------|----------------|---------------|
| **Axon** (Java, 主要对标) | C — `@EventHandler void(MyEvent)` + 业务自取 repo | `token_entry` 表 (processor_name, segment, token, **owner**) | 4 相：Stop → Reset (`@ResetHandler`) → Replay → Catch-up | token 与 read model **同 tx** | 不阻塞 (stale) |
| **Marten** Async (.NET) | B — `Apply(IDocumentOperations ops, Event e)` | `mt_event_progression` 表 | `daemon.RebuildProjectionAsync<T>` 单步 | 不同 tx，幂等 upsert | 不阻塞 (stale) |
| **eventhorizon** (Go) | A — `Project(ctx, event, entity) (entity, error)` | **无内置 checkpoint** | 无 rebuild API | entity 乐观锁 | 业务自管 retry |
| **Commanded** (Elixir) | B — `project %Event{}, meta, do: Ecto.Multi.x(multi, ...)` | EventStore `subscriptions` 表 | 业务清表 + 重启 subscriber | 不同 tx | 不阻塞 (stale) |
| **Watermill CQRS** (Go) | C — `Handle(ctx, event) error` | broker-side offset | 无 rebuild | broker 保证 | 不阻塞 (stale) |

**对 GoCell 的关键启示**：

1. **eventhorizon 形态 A 不适配 GoCell**——单 entity read-modify-write 锁死业务（GoCell 投影常需要跨表写）。明确否决。
2. **Axon `token_entry` + 同 tx 提交是最严谨的 exactly-once 模式**——与 GoCell `persistence.TxRunner.RunInTx` 范式完全吻合。直接对标。
3. **Axon rebuild 是 4 相（Stop → Reset → Replay → Catch-up）**，原 spec/plan 写的 3 相需要补 Stop。
4. **业界共识：rebuild 期 read 不阻塞**（Axon / Marten / Commanded / Watermill 一致），stale read 是业务侧责任。原 spec §4 Scenario C「业务读 endpoint 返回 503」需要重审。
5. **Axon `owner` 列实现分布式 claim**——多 pod 并发消费同一 projection 的安全机制。引入新决策点 **Q5（v1 是否支持多 pod 并发同一 projection）**。
6. **snapshot v1 不必要的论证更扎实**：Axon 引入 snapshot 是因为 event log 全局共享 + replay 量级 ≥ 数百万条；GoCell outbox 是单 cell 局部事件流，全量重放量级远小于此。

`ref:` 开源对标列表（详引见 PR-00 ADR §对标）：

- Axon TokenStore 4.4 / 5.0、`@ResetHandler`、Metrics
- Marten async-daemon
- Commanded read-model-projections (Ecto.Multi)
- eventhorizon `projector` package
- Watermill `components/cqrs`

## 1. 上下文

GoCell L3 投影场景当前**手撕**（`examples/todoorder/cells/ordercell/internal/orderprojection` 一个实例，~375 行机械部件 + 业务部件混在同一手写 `store`）。subscription wiring 已由 cellgen 收口，但 **checkpoint / replay / rebuild / metrics** 全部业务自行实现：

- 没有持久化 checkpoint（demo `nextSeq` 仅内存）
- rebuild 是单纯 in-memory log 重放，无 event store 重放协议
- 无 replay lag / rebuild duration / event-log length 可观测
- L3 测试纪律已强制每个投影有 replay/rebuild 测试，但**框架不提供机制**

≥ 2 个 roadmap-committed 消费方（winmdm `unified_device_id` / unified view + zerotrust `trustscore` / `eventcorrelation`）即将上线流式聚合读模型。AI 跨 ~19 个 winmdm/zt cell 手搓投影循环（序列号 / replay / checkpoint）= 字面约定 + 必然漂移的 Soft 形态。harness 化一次 vs 散弹手写——前者是 Hard funnel，后者是 AI-robust 章程禁止的 Soft 增殖。

## 2. 范围

### 2.1 In-scope（本 epic 落地）

| 投影部件 | 谁负责 |
|---------|-------|
| 订阅 wiring | harness 已有（cellgen `reg.Subscribe`），本 epic 扩 `kind: projection` 派生路径 |
| 幂等消费 | harness 已有（`ConsumerBase`），不动 |
| replay 源 | 复用既有 outbox journal / event store，本 epic 定义 Cursor 读契约 |
| **checkpoint / offset** | **新**：框架自有 offset 表，与业务 apply 走同一 `CellTx` 提交（exactly-once，不碰业务表 schema） |
| **rebuild 编排** | **新**：harness 壳（reset → replay → catch-up state machine）+ business hook |
| **可观测 metrics** | **新**：`projection_event_replay_lag_seconds` / `projection_rebuild_duration_seconds` / `projection_pending_events`（covers #961） |
| **PROJECTION-CONSISTENCY-01 升 Hard** | **新**：~~parser load-time `jsonschema.Validate`~~ → **已交付为 contractgen codegen funnel**（covers #960；parser-jsonschema 方案被否决，见 ADR §Amendment 2026-06-02 #960） |
| **examples L3 reference** | orderprojection 改造为 harness 官方 reference（covers #834） |

### 2.2 Out-of-scope（六席位 GAP-8 封存）

| 部件 | 归属 | 状态 |
|------|------|------|
| event→state apply **函数体** | business / GAP-8 sealed | 业务写 |
| 读模型 **schema** | business / GAP-8 sealed | 业务写 |
| 框架级 CQRS（规定读模型表） | 六席位封存 | 不触碰 |

> **明确不越界**：harness 在 CellTx-offset 方案下**不规定业务 read-model 表 schema**，故不在 GAP-8 封存理由「不绑死业务用表自由度」射程内。封存仍守 apply 函数 + 读模型表。

### 2.3 v1 不做（待 v1.1 或未来 epic）

- snapshot / 部分 replay（`createTokenAt` 等价物）—— PR-00 ADR 决议 v1 不纳入；触发条件待 winmdm Stage 1 reset 性能数据落地后重评
- 多投影并发 rebuild 编排（v1 单投影串行 reset→replay→catch-up）
- 跨 cell projection（v1 一个 projection slice → 一个 cell 内一个 read-model）

## 3. Actors

| Actor | 角色 |
|-------|------|
| **business cell（projection consumer）** | 实现 `apply(ctx, event) error` hook（tx ambient，经 ctx；见 ADR §3 Q2），声明 `cell.yaml kind: projection` + slice contractUsages[subscribe] |
| **harness（kernel/projection）** | 托管订阅循环 / checkpoint / rebuild 编排 / metrics |
| **composition root (`cmd/*`)** | 通过 `WithProjectionCheckpointStore(...)` 注入 checkpoint adapter（mem / PG） |
| **operator** | 通过 internal HTTP endpoint（`POST /internal/v1/<cell>/projection/<name>/rebuild`，service-token + caller-cell allowlist，internal-only；契约见 ADR §5「Rebuild control-plane endpoint」）触发 rebuild；通过 readyz `<cell>_projection_<name>_ready` probe 监控 lag |
| **AI co-author** | 写 slice handler apply 函数体；其余生命周期 wiring 由 codegen 派生 |

## 4. User Scenarios（验收场景）

### Scenario A — Cold-start (greenfield)

1. cell 启动，projection slice 已声明 `kind: projection` + contractUsages[subscribe]
2. cellgen 派生 harness wiring：订阅、checkpoint 读取、apply hook 调用
3. 启动时 checkpoint 表无该 projection 行 → harness 视作 offset = 0
4. 从 offset = 0 开始消费事件流，每条事件在一个 CellTx 内：
   - 调用 business `apply(ctx, event)`（tx ambient，经 ctx）
   - harness 更新 checkpoint 行（offset = replay-cursor 位置；outbox.Entry 无 seq 字段）
   - tx commit / rollback 决定 apply + checkpoint 是否同时生效
5. 启动 60s 内 lag 收敛到 0，`<cell>_projection_<name>_ready` probe 转 ready

### Scenario B — Crash recovery

1. cell 处理事件 N 后崩溃，checkpoint 表 offset = N（apply + offset 已同 tx commit）
2. cell 重启，harness 读 checkpoint = N，从 N+1 开始消费
3. **不会重放 0..N**——apply 函数无需自己实现幂等（apply 函数确实写了 idempotent guard 是允许的，但 harness 保证 exactly-once delivery to apply 函数）

### Scenario C — Rebuild (apply 逻辑变更)

1. 业务变更 apply 函数（如调整聚合维度），需要从零重建 read-model
2. operator 调用 `POST /internal/v1/<cell>/projection/<name>/rebuild`
3. harness 执行 state machine（**对标 Axon 4 相**）：
   - **Stop**：停止消费循环，释放 checkpoint claim（防止并发写 checkpoint 表）
   - **Reset**：调用 business `OnReset(ctx) error` hook（tx ambient，经 ctx；对标 Axon `@ResetHandler`；业务 TRUNCATE/DROP 自己的 read-model 表）+ harness 重置 checkpoint 行 offset = 0（同一 CellTx）
   - **Replay**：从 offset = 0 起遍历事件流，每条事件一个 CellTx（apply + checkpoint），可观测 `projection_rebuild_duration_seconds`
   - **Catch-up**：replay 追上"开始 rebuild 时的 head offset"后 transition 到 catch-up 模式，继续消费新事件
4. **rebuild 期业务 read 不阻塞**（对标 Axon / Marten / Commanded 业界共识）—— stale read 是业务侧责任；业务可显式在 read 路径检查 `harness.Phase()` 返回 503 但 harness 不强加；
5. catch-up 完成后 `<cell>_projection_<name>_ready` probe 转 ready

### Scenario D — Out-of-order replay during catch-up

1. catch-up 模式继续消费新事件，offset 单调递增
2. 若同一事件因 broker redelivery 出现 offset ≤ checkpoint，harness 视为已处理，**不调用 apply**（exactly-once delivery to apply）
3. 业务无需自己实现"已应用过"判断

### Scenario E — Projection consistency violation (PROJECTION-CONSISTENCY-01 Hard upgrade)

1. 开发者写 `contract.yaml: kind: projection, consistencyLevel: L2`
2. `gocell validate`（Medium 兜底，覆盖 `codegen: false` + in-memory fixture）：governance rule 报 error 阻断 CI
3. Hard 主门控（gh #960 已交付）：contractgen codegen funnel——`kind: projection` 契约生成的 `types_gen.go` 携带 `const _ = uint(cellvocab.<level> - cellvocab.L3)`，L0/L1/L2 编译期 uint 溢出 → `codegen: true` 的非法 projection 契约**无法构建**（违反不可表达，compile-error 下游）。parser-jsonschema 方案被否决，见 ADR §Amendment 2026-06-02 #960

### Scenario F — Replay lag alerting

1. consumer 处理速度 < producer 速度，`projection_event_replay_lag_seconds` 持续升高
2. operator dashboard 触发告警（lag > 5min for 10min）
3. operator 查 `projection_pending_events` 确认积压来源（vs broker 卡死）

## 5. Acceptance Criteria

### 5.1 Functional

- [ ] `kernel/projection.Coordinator` 提供 `Subscribe(ctx, spec, projectionID, apply, opts...) error` API；apply 签名为 `func(ctx context.Context, event outbox.Entry) error`（tx ambient，经 ctx；见 ADR §3 Q1/Q2 —— 原 `persistence.TxHandle` 显式参数已被收敛，该类型不存在）
- [ ] checkpoint store 抽象 `CheckpointStore` interface；提供 `mem` + `postgres` 两个 adapter
- [ ] postgres adapter 的 `LoadOffset` / `SaveOffset` 在 caller-provided CellTx 内执行（exactly-once 与 business apply 同 commit）
- [ ] rebuild state machine **4 相**（Stop / Reset / Replay / Catchup，对标 Axon；见 ADR §3 Phase enum），状态对外可读（HTTP endpoint 返回 `{phase, replayLagSeconds, pendingEvents}`）
- [ ] rebuild control-plane endpoint `POST /internal/v1/<cell>/projection/<name>/rebuild` 的安全 + HTTP 契约（service-token + caller-cell allowlist / internal-only / 202 async·409 已在 rebuild·404 未知 / `{"data":...}` envelope）—— forward contract 见 **ADR §5「Rebuild control-plane endpoint」**；完整 contract.yaml 在 PR-03（T-03-3）落地
- [ ] cellgen 派生：`kind: projection` slice 自动生成 harness wiring（订阅 + Subscribe 调用 + apply 字段桥接），业务只填 apply 函数体
- [x] `PROJECTION-CONSISTENCY-01` 由 Medium 升 Hard（gh #960，已交付）：contractgen codegen funnel——`kind: projection` 契约的生成 `types_gen.go` 携带 `const _ = uint(cellvocab.<level> - cellvocab.L3)`，L0/L1/L2 编译期溢出拒绝（parser-jsonschema 方案被否决，见 ADR §Amendment 2026-06-02 #960）
- [ ] 三个 metrics：`projection_event_replay_lag_seconds` / `projection_rebuild_duration_seconds` (histogram) / `projection_pending_events` (gauge)
- [ ] `<cell>_projection_<name>_ready` readyz probe：lag < threshold && state != reset/replay 时 ready
- [ ] examples/todoorder/orderprojection 改造为 harness 形态，作为 L3 reference

### 5.2 Non-functional

- [ ] 单 cell 单 projection cold-start 60s 内 lag 收敛到 0（10k events / s 假设）
- [ ] crash recovery 启动后第一个 tx 即从 checkpoint 恢复，无重放窗口
- [ ] rebuild 期间业务 read endpoint **不被 harness 强制 503**（非阻塞设计，业界共识；见 ADR §5）；业务可自查 `Phase()` 自行返回 503
- [ ] kernel/projection ≥ 90% test coverage（kernel 层标准）

### 5.3 Governance / archtest

- [x] `PROJECTION-CONSISTENCY-01` 升 Hard（gh #960 已交付）：contractgen codegen funnel（生成 `types_gen.go` 编译期 uint overflow），**非 archtest**；governance rule 留 Medium 兜底。parser-jsonschema / schema-enum-parse 方案被否决，见 ADR §Amendment 2026-06-02
- [ ] **新增 archtest funnel（≥ Medium，AI-robust 章程要求）**：
  - **PROJECTION-APPLY-HOOK-FUNNEL-01**：cellgen 派生的 harness wiring 是 business apply 函数唯一注册路径；手写 `Coordinator.Subscribe(..., apply, ...)` callsite 仅允许在 generated 文件
  - **PROJECTION-CHECKPOINT-TX-BOUND-01**：`SaveOffset` 实现必须经 `persistence.TxFromContext(ctx)` 取 ambient tx；裸 `*sql.Tx` 参数 / `db.Exec` 形态 fail（与 outbox.Writer 同范式）
  - **PROJECTION-STATE-PHASE-FROZEN-01**：Phase enum const 集冻结（5 成员 PhaseLive/Stopped/Reset/Replay/Catchup，AST 锁；PR-00 已 green）
- [ ] cellgen scaffold golden 更新（新增 `kind: projection` 派生模板）
- [ ] L2-OUTBOX-ATOMICITY-COVERAGE-01 不变（projection 仍是 L3 consumer，与 L2 producer 测试体系正交）

## 6. Dependencies

### 6.1 必须先收敛

- **PR-00 ADR**：本 epic 第一个 PR 必须落 ADR，收敛 4 个设计张力（见 §7）。**禁止凭猜先做**（issue body 明文）。

### 6.2 复用基础设施（已就绪）

- `kernel/outbox.Entry` / `ConsumerBase` / `EntryHandler` / `HandleResult`
- `kernel/persistence.TxRunner` + `RegisterAfterCommit`
- `kernel/cell.Registrar.Subscribe` + cellgen `cell.tmpl` Subscriptions 渲染
- `kernel/healthz.Probe` / `ProbeSet` / `RepoProber`
- `runtime/observability/metrics` collector / register 模式
- `kernel/governance` rule registry + jsonschema 基础设施
- ADR 真值源：
  - `202605051600-adr-pg-outbox-fencing.md`（fencing token 模式参考）
  - `202605101730-adr-shutdown-budget-decouple.md`（lifecycle 退出）
  - `202605161030-adr-cell-repo-readyz-probe.md`（readyz probe funnel）
  - `202605241940-adr-l2-atomicity-subtypes.md`（projection 是 L3 consumer，与 L2 atomicity 正交）

### 6.3 软依赖

- W6 saga（#1078）journal/replay 基建——**harness 不阻塞于 saga 补偿语义收敛**（projection replay ⊂ event-log replay，比 saga 简单）
- 后续 winmdm Stage 1 启动时 GAP-8 seal 重审：PR-00 ADR 必须显式记录「harness 收窄 seal 结论到『框架不规定读模型表』，apply 函数 + schema 仍归业务」

## 7. Open Questions（PR-00 ADR 必须收敛）

> **术语对齐**：下表 ABCD 编号是 GoCell 自定义，与 §0 对标表中开源框架的 ABC 不对应。每项注明对标框架对应。

| ID | 张力 | 选项 |
|----|------|------|
| Q1 | **checkpoint exactly-once 协议** | A: harness 拿 caller-provided tx 在内部 SaveOffset（**对标 Axon JdbcTokenStore**）/ B: harness 暴露 `RegisterCheckpoint(tx, offset)` 让 business 主动提交 / C: after-commit hook 异步写 offset（非 exactly-once；对标 Marten Async / Commanded） |
| Q2 | **business apply hook 签名** | A: ~~`apply(ctx, event, txHandle) error`~~ **→ 已收敛冻结为 `apply(ctx, event) error`（ambient tx 经 ctx；`txHandle` 类型不存在、与 `PG-REPO-AMBIENT-TX-01` 冲突，见 ADR §3 Q2 Correction）**（对标 Marten Async `Apply(IDocumentOperations, e)`）/ B: `apply(ctx, event, entity) (entity, error)` + harness read-modify-write（**对标 eventhorizon**——已明确否决）/ C: `apply(ctx, event) error` + business 自管 tx（**对标 Watermill**）/ D: 注解驱动（对标 Axon `@EventHandler`，Go 不适用） |
| Q3 | **kind:projection codegen funnel 接法** | A: 单 slice 单 projection（subscribe 集合 = 单 projection 输入流）/ B: 多 slice 共享 projection（需新 metadata 节点） |
| Q4 | **snapshot / 部分 replay 是否 v1** | A: v1 不做（rebuild 全量）/ B: v1 留 hook 但无 PG store / C: v1 完整支持 |
| **Q5** | **多 pod 并发同一 projection 是否支持**（对标 Axon `token_entry.owner` 列） | A: v1 单 pod（leader election 走上层 `cmd/corebundle`，schema 预留 `owner` 列但不写）/ B: v1 内置 pessimistic claim（owner 列 + advisory lock） |

**预填倾向**（待 ADR 论证后定）：Q1=A、Q2=A、Q3=A、Q4=A、**Q5=A**。理由（对标增强）：

- **Q1=A**：与 outbox `Writer.Write` 同 pattern（caller 用 RunInTx 包，writer 内部用 TxFromContext join），AI-robust 形态一致；**对标 Axon JdbcTokenStore + 同 tx 提交模式**——业界最严谨的 exactly-once 实现。
- **Q2=A**：ambient ctx-tx 是最小依赖（**无显式 tx handle 参数**——与 `outbox.Writer.Write` 同范式，hook 内经 `persistence.TxFromContext` 取 tx），不绑死读模型 schema / entity 类型；**对标 Marten Async `IDocumentOperations` 形态**。eventhorizon 的 entity read-modify-write（形态 B）已明确否决——GoCell L3 投影常需跨表写，单 entity 模型锁死业务。
- **Q3=A**：单输入流单 projection 覆盖 winmdm `unified_device_id` / zerotrust `trustscore` 二条 roadmap-committed 场景；多 slice 多入是 v1.1 扩展点。
- **Q4=A**：snapshot 的 PG 表抽象会越过 GAP-8 封存边界；v1 故意留白；**对标论证更扎实**——Axon 引入 snapshot 是因为 event log 全局共享 + replay 量级 ≥ 数百万条，GoCell outbox 是单 cell 局部事件流，全量重放量级远小于此。触发条件：winmdm Stage 1 实测 rebuild 全量 ≥ 30min 时新 epic 紧急加。
- **Q5=A**：v1 单 pod 模式 complexity 最低；schema 预留 `owner TEXT` 列（对标 Axon `token_entry.owner`），v1 不写不读；多 pod 安全由上层 leader election 保证（`cmd/corebundle` 责任）；v1.1 在 owner 列上实现 pessimistic claim 即可。

## 8. Non-goals (clarifications)

- **不做** `reconcile`（#661 是 L4 desired-state 收敛，本 epic 是 L3 投影；同 GoCell 范式不同 controller）
- **不做** saga 补偿（#1078，本 epic 不依赖也不阻塞）
- **不做** 业务 read-model schema 抽象（GAP-8 sealed）
- **不做** cross-cell projection（一个 projection slice 属于一个 cell；cross-cell aggregation 走多个 projection 串联）

## 9. Sub-issues coverage

| sub-issue | 覆盖 PR |
|-----------|---------|
| #1079 [H2/W10] Projection / Replay runtime | PR-00 + PR-01 + PR-02 + PR-04（主体） |
| #834 [L3-EXAMPLE-PROJECTION-01] | PR-06（orderprojection 改造为 reference） |
| #960 PROJECTION-CONSISTENCY-01 升 Hard | PR-05（独立先行候选） |
| #961 L3 投影可观测 metrics | PR-03（rebuild + metrics + readyz 三件套同包） |

Q&A、风险、PR 切分细节见 [implementation plan](./202605261600-1100-cqrs-projection-harness-implementation-plan.md)。
