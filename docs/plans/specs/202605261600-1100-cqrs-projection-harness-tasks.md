# Tasks — CQRS Projection lifecycle harness (epic #1100)

> speckit-tasks 形态任务清单。Spec：[spec](./202605261600-1100-cqrs-projection-harness-spec.md)，Plan：[plan](./202605261600-1100-cqrs-projection-harness-implementation-plan.md)
>
> 每条 task 是可分派的 ship 单元，标注 ship Level / 依赖 / 验收 / 行数预算 / 关键命令。

## Task 命名约定

- `T-NN`：PR 内 task；`T-NN-x`：sub-task
- `ship: L1/L2/L3` —— `.claude/skills/ship`
- `block: T-MM` —— 依赖关系
- 每 task ≤ 1 工作日；超出拆 sub-task

---

## PR-00 — ADR + skeleton

### T-00-1 ADR 落地（设计张力收敛）

- **what**：写 `docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md`，逐项收敛 **Q1-Q5**（spec §7）
- **ship**：L3（架构决议，需 architect + kernel-guardian + product-manager 联审）
- **依赖**：无
- **验收**：
  - [ ] **§对标章节**（CLAUDE.md 强制）：Axon / Marten / eventhorizon / Commanded / Watermill 五框架表 + `ref:` 链接齐全
  - [ ] **Q1-Q5 每项**都有「选项 + 论证 + 决议 + 风险 + 回滚条件 + 对标框架引用」
  - [ ] **Q5（多 pod 并发安全）** 明文：v1 单 pod / owner 列预留 / v1.1 升级路径
  - [ ] **Q4 补充章节**：rebuild 期 read 不阻塞（业界共识），harness 暴露 `Phase()` 供业务自选 503
  - [ ] GAP-8 seal 重审记录章节明文
  - [ ] 威胁矩阵 7 行（exactly-once / crash recovery / rebuild 期 read 一致性 / out-of-order replay / fail-closed / GAP-8 边界 / **多 pod 并发安全 v1 边界**）
- **行数**：~400
- **关键命令**：`gh issue comment 1100 --body "ADR: docs/architecture/202605261620-..."`

### T-00-2 kernel/projection 包骨架

- **what**：建 `kernel/projection/` 目录 + types.go / cell_marker.go / phase.go / doc.go（**不写实现**）
- **ship**：L2（接口先行，无逻辑）
- **依赖**：T-00-1（接口签名以 ADR 决议为准）
- **验收**：
  - [ ] `go build ./kernel/projection/...` 通过
  - [ ] 包 godoc 引用 ADR 文件路径
  - [ ] PROJECTION-STATE-PHASE-FROZEN-01 archtest 登记并 **green**（AST const-set + String-arm 锁，5 成员 Phase enum；enum 在本 PR 声明，PR-00 即 green，非 pending）
- **行数**：~340
- **关键命令**：`make verify` 跑 archtest 全集（pending test 跳过）

---

## PR-01 — Coordinator + mem store

### T-01-1 Coordinator 主体实现

- **what**：`kernel/projection/coordinator.go` 实现 Subscribe + 内部消费 handler（CellTx 包 apply + SaveOffset）
- **ship**：L3（kernel 核心，三 agent 探索）
- **依赖**：PR-00 merged
- **验收**：
  - [ ] cold-start / crash recovery / out-of-order replay / fail-closed 4 路径单元测试
  - [ ] coverage ≥ 90%
  - [ ] `gocell:"required"` tag 标注所有 required 依赖（txRunner / store / tracer）
- **行数**：~800

### T-01-2 mem CheckpointStore + conformance template

- **what**：`memstore.go` + `projectiontest/conformance.go`（PG adapter 后续 import 复用）
- **ship**：L2
- **依赖**：T-01-1
- **验收**：
  - [ ] conformance.go 提供 `RunCheckpointConformance(t, store)` 公共测试 funnel
  - [ ] memstore 跑通 conformance
- **行数**：~450

### T-01-3 archtest 三件套 stub

- **what**：登记 PROJECTION-APPLY-HOOK-FUNNEL-01 / PROJECTION-CHECKPOINT-TX-BOUND-01 archtest（pending → 等下游 PR 转 green）
- **ship**：L2
- **依赖**：T-01-1
- **验收**：
  - [ ] 每个 archtest 文件头 `// INVARIANT:` + 反向自检测试 + godoc 盲区清单
  - [ ] AI-robust 评级文档化在 archtest godoc
- **行数**：~350

---

## PR-02 — PG checkpoint store

### T-02-1 Migration + PG adapter

- **what**：`projection_checkpoints` 表 migration（schema 包含 `owner TEXT NOT NULL DEFAULT ''` 列对标 Axon `token_entry`）+ `adapters/postgres/projection_checkpoint_store.go`
- **ship**：L2（adapter 层）
- **依赖**：PR-01 merged
- **验收**：
  - [ ] migration 编号 = 当前 max + 1（查 `adapters/postgres/migrations/`）
  - [ ] schema 含 `owner TEXT NOT NULL DEFAULT ''` 列（v1 预留，参照 ADR §Q5）
  - [ ] `SaveOffset` 强制使用 `persistence.TxFromContext(ctx)` 获取 tx，不自建连接
  - [ ] **SaveOffset INSERT/UPDATE 不含 owner 列**（`OwnerCheckpointStore.AdvanceIfOwner` 是唯一 owner 写路径；`PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01` archtest 已在 #1630 Batch 2 退役，替换为 `SAGA-OWNER-CHECKPOINT-CONFORMANCE-ENROLL-01`）
  - [ ] PROJECTION-CHECKPOINT-TX-BOUND-01 archtest 转 green
- **行数**：~820

### T-02-2 testcontainers integration test

- **what**：PG adapter integration test（真实 PG）
- **ship**：L2
- **依赖**：T-02-1
- **验收**：
  - [ ] `go test -tags=integration ./adapters/postgres/...` 通过
  - [ ] conformance test 走 PG 路径
- **行数**：~480

### T-02-3 corebundle wiring

- **what**：`cmd/corebundle` 注入 PG checkpoint store + `bootstrap.WithProjectionCheckpointStore` option
- **ship**：L2
- **依赖**：T-02-1
- **验收**：
  - [ ] real 模式构造 PG store；demo 模式继续 mem store
  - [ ] `bootstrap.WithProjectionCheckpointStore` 走 builder-noop 范式（runtime-api.md Option 范式分层）
  - [ ] corebundle smoke 通过
- **行数**：~450

---

## PR-03 — Rebuild + metrics + readyz

### T-03-1 Rebuild state machine

- **what**：`rebuild.go` **4 相**状态机（Stop → Reset → Replay → Catchup，对标 Axon）+ business `OnReset(ctx, tx) error` hook（对标 Axon `@ResetHandler`）
- **ship**：L3
- **依赖**：PR-01 merged
- **验收**：
  - [ ] **4 相 + 10+ transitions** 完整测试（含错误回退 / Stop 阶段 graceful 退出 / claim 释放）
  - [ ] PROJECTION-STATE-PHASE-FROZEN-01 保持 green（PR-00 已 AST 锁 5 成员 Phase enum const 集；PR-03 加 rebuild transition table 不改 enum 集）
  - [ ] **harness 默认不阻塞 business read**（对标 Axon / Marten 业界共识）；暴露 `Phase() Phase` 方法供业务自选 503
  - [ ] `OnReset` hook 签名通过 ADR §对标 Axon `@ResetHandler` 论证
- **行数**：~720

### T-03-2 Metrics 三件套

- **what**：`metrics.go` 注册 `projection_event_replay_lag_seconds` / `projection_rebuild_duration_seconds` / `projection_pending_events`
- **ship**：L2
- **依赖**：T-03-1
- **验收**：
  - [ ] cell + projection 双 label
  - [ ] label 注册经 `MustValidateLabels`
  - [ ] alerting rule 示例文档化 in `docs/ops/`
- **行数**：~270

### T-03-3 Readyz probe + control-plane endpoint

> **Amendment (PR-03 #1175)**：HTTP control-plane endpoint（`POST /internal/v1/<cell>/projection/<name>/rebuild` contract + handler）**移至 PR-04**——PR-03 无宿主 cell / internal listener，平台级 active 契约会触发 `DEAD-CONTRACT-01`（决议见 ADR 202605261620 §Rebuild control-plane endpoint Amendment 2026-05-31）。PR-03 冻结程序化触发面 `Coordinator.Rebuild(ctx)` + `Close(ctx)`；readyz probe 留在 PR-03。下方 ② contract test 随端点移 PR-04。

- **what**：`<cell>_projection_<name>_ready` probe（PR-03）+ ~~`POST /internal/v1/<cell>/projection/<name>/rebuild` contract & handler~~（移 PR-04）
- **ship**：L3
- **依赖**：T-03-1 + T-03-2
- **验收**：
  - [x] ① ReadyProbeName typed const（funnel 范式同 postgres.ProbeReady）—— `healthz.ProjectionReadyProbeName`（PR-03 落地）
  - [ ] ② contract test：service-token auth + caller-cell allowlist —— **随 HTTP endpoint 移 PR-04**
  - [x] ③ readyz lag > threshold 时 unhealthy（PR-03 落地）
- **行数**：~880（PR-03 实际仅 probe 部分；endpoint 行数计入 PR-04）

---

## PR-04 — cellgen kind:projection 派生

### T-04-1 metadata 扩展

- **what**：`kernel/metadata` 增 `ContractMeta.Projection` 派生字段 + slice.yaml `projection:` 字段解析
- **ship**：L2
- **依赖**：PR-01 merged
- **验收**：
  - [ ] parser 测试：projection 字段从 slice.yaml contractUsages 派生
  - [ ] 多 projection 同 cell 唯一性校验
- **行数**：~300

### T-04-2 cellgen builder + template

- **what**：`builder.go` ProjectionSpec 构建 + `cell.tmpl` + 新 `projection_slice.tmpl`
- **ship**：L3（cellgen 改动影响所有未来 projection cell）
- **依赖**：T-04-1
- **验收**：
  - [ ] generated slice_gen.go 调用 `coord.Subscribe(...)`
  - [ ] business `service.go` 持 apply 函数 stub（scaffold）
  - [ ] PROJECTION-APPLY-HOOK-FUNNEL-01 archtest 转 green（手写 Subscribe 调用在 business 代码内 fail）
- **行数**：~600

### T-04-3 cellgen e2e + scaffold golden

- **what**：从 fixture cell.yaml / slice.yaml 派生完整 cell_gen.go / slice_gen.go 的 e2e test + scaffold golden 更新
- **ship**：L3
- **依赖**：T-04-2
- **验收**：
  - [ ] golden diff 人工 review pass
  - [ ] scaffold 新 projection slice 产生 `internal/projection/` 起始 doc.go
- **行数**：~700

### T-04-4 dev guide 章节

- **what**：`docs/guides/cell-development-guide.md` 加 L3 Projection 章节（含 minimal slice 示例）
- **ship**：L1
- **依赖**：T-04-3
- **验收**：
  - [ ] 示例 ≤ 50 行可粘贴
  - [ ] 链回 orderprojection reference（PR-06 落实）
- **行数**：~200

---

## PR-05 — PROJECTION-CONSISTENCY-01 升 Hard

> **SUPERSEDED (gh #960 delivered 2026-06-02).** 下方 T-05-1/2/3 描述的「parser
> load-time `jsonschema.Validate`」方案在落地时被**否决**——parse-time 校验是
> Medium runtime guard（违反可表达、不覆盖 in-memory 向量），不满足 Hard。实际交付
> 为 **contractgen codegen funnel**：`kind: projection` 契约的生成 `types_gen.go`
> 携带 `const _ = uint(cellvocab.<level> - cellvocab.L3)`，L0/L1/L2 编译期 uint 溢出
> → 无法构建。governance rule 留作 codegen:false + in-memory 的 Medium 兜底。无
> parser 改动、无 kernel-isolation 放宽、无 `PROJECTION-CONSISTENCY-PARSE-TIME-01`
> archtest。权威记录见 ADR `202605261620` §Amendment 2026-06-02 #960。下方原始任务
> 保留作历史脉络，不再执行。

### T-05-1 parser load-time jsonschema.Validate

- **what**：`kernel/metadata/parser.go` 在 contract load 路径调 `jsonschema.Validate`
- **ship**：L2
- **依赖**：PR-04 merged（cellgen 反应窗口）
- **验收**：
  - [ ] L0/L1/L2 + kind:projection contract YAML 在 parse 时 reject
  - [ ] in-memory ProjectMeta fixture 保留 governance rule 兜底（Medium → Hard 升级不删除 rule）
- **行数**：~280

### T-05-2 性能基准

- **what**：`BenchmarkLoadContract` 对比 before/after 增量
- **ship**：L1
- **依赖**：T-05-1
- **验收**：
  - [ ] 增量 ≤ 5%；> 5% 时改 lazy validation (only validate kind=projection)
  - [ ] benchmark 结果写进 ADR §Hard upgrade 章节
- **行数**：~150

### T-05-3 archtest + governance rule godoc 升级

- **what**：`PROJECTION-CONSISTENCY-PARSE-TIME-01` archtest 锁 parser callsite + rule godoc 升 Hard
- **ship**：L2
- **依赖**：T-05-1
- **验收**：
  - [ ] archtest pass + 反向自检
  - [ ] backlog #960 关闭，gh comment 引用 PR
- **行数**：~250

---

## PR-06 — orderprojection reference + closeout

### T-06-1 orderprojection 重写

- **what**：删 in-memory log + nextSeq + 自实现 Rebuild + orderprojectionrebuild slice；接入 harness apply 签名
- **ship**：L2（examples 有 smoke 兜底）
- **依赖**：PR-01..05 全 merged
- **验收**：
  - [ ] `examples/todoorder` smoke 全绿
  - [ ] `gocell validate examples/todoorder` 通过
  - [ ] generated cell_gen.go / slice_gen.go 由 cellgen 产出（手工 diff review）
- **行数**：~520（net diff）

### T-06-2 集成测试

- **what**：cold-start / crash recovery / rebuild 三场景 e2e（真实 PG）
- **ship**：L2
- **依赖**：T-06-1
- **验收**：
  - [ ] crash recovery：杀进程 → 重启 → 第一个 tx 即从 checkpoint 恢复
  - [ ] rebuild：POST /internal endpoint 触发 → 业务 read **不阻塞**（harness 不强制 503，对标 Axon / Marten；业务可自查 `Phase()` 自行返回 503）→ catch-up 完成后继续消费（见 ADR §5）
- **行数**：~400

### T-06-3 closeout + backlog 关闭

- **what**：`docs/plans/archive/202605261600-1100-...-closeout.md` + 关闭 backlog #1100 / #1079 / #834 / #960 / #961
- **ship**：L1
- **依赖**：T-06-1 + T-06-2
- **验收**：
  - [ ] closeout 列各 sub-issue 关闭 PR
  - [ ] backlog 5 issues 全部关闭 + 评论引用 closeout
  - [ ] dev guide 反向链回 orderprojection reference
- **行数**：~180

---

## 跨 PR 总览

### 验收里程碑

| 里程碑 | 完成定义 |
|--------|---------|
| **M1 设计收敛** | PR-00 merged，ADR 4 张力决议 + GAP-8 seal 重审记录入存档 |
| **M2 kernel 核心** | PR-01 + PR-02 merged，Coordinator + mem store + PG store + conformance test |
| **M3 codegen 派生** | PR-04 merged，cellgen `kind: projection` 派生通路打通 |
| **M4 生命周期完整** | PR-03 merged，rebuild + metrics + readyz 三件套 |
| **M5 consistency Hard** | PR-05 merged，PROJECTION-CONSISTENCY-01 升 Hard：contractgen codegen funnel（生成 `types_gen.go` 编译期 uint overflow），governance rule 留 Medium 兜底（parser-jsonschema 方案被否决，见 ADR §Amendment 2026-06-02 #960） |
| **M6 reference** | PR-06 merged，orderprojection 改造为 L3 官方 reference，所有 sub-issues 关闭 |

### ship 调度落点

| PR | reviewer 数 | diff 行数实测后调整 |
|----|-----------|-------------------|
| PR-00 | 6 | ADR + skeleton 800 行 |
| PR-01 | 3 | 1800 行 |
| PR-02 | 2 | 1500 行 |
| PR-03 | 3 | 1800 行 |
| PR-04 | 6 | 1800 行 |
| PR-05 | 2 | 1000 行 |
| PR-06 | 2 | 1500 行 |

> 实际 ship 时跑 `wc -l $(git diff --name-only develop...HEAD)` 验证 ≤ 2000；超线立即拆。

### sub-issues 关闭路径

| sub-issue | 关闭 PR |
|-----------|---------|
| #1100 (epic) | PR-06 closeout |
| #1079 [H2/W10] | PR-04（主体 cellgen），PR-06 备注关闭 |
| #834 [L3-EXAMPLE-PROJECTION-01] | PR-06 |
| #960 PROJECTION-CONSISTENCY-01 升 Hard | PR-05 |
| #961 L3 投影可观测 metrics | PR-03 |

### 关键命令清单

```bash
# 每 PR 前 baseline 检查
bash hack/verify-archtest.sh
go test -tags=integration ./...

# Spec/Plan 一致性检查
go run ./cmd/gocell validate

# 行数预算检查（PR 提交前）
git diff --stat develop...HEAD | tail -1

# cellgen 重生（PR-04 / PR-06）
go run ./cmd/gocell generate cell --all
```

### 不可妥协项（AI-robust 章程 + feedback memory）

- **PR review findings 默认 in-scope**（feedback memory）—— 不接受 P2/follow-up 标签
- **archtest 新增必须 ≥ Medium**（AI-robust 章程）—— Soft 严禁立项
- **funnel 双向锁评级**（上游 Hard + 下游 Hard 才算闭环；过渡形态用 gh issue 跟踪）
- **每 archtest 反向自检**（盲区清单 + 自检测试是 ≥ Medium 评级前置举证）
- **DROP COLUMN / forbiddenColumns 不适用**（本 epic 是新增 table，无 DROP）
- **ADR 与代码同 PR 同源**（不接受先合代码再补 ADR）
- **不留小尾巴**（feedback memory：defer 必须有真实 blocker）—— snapshot v1 缺失明确文档化在 ADR 触发条件，不开 silent backlog
- **CLAUDE.md 强制开源对标**——PR-00 ADR 必含 §对标章节 + `ref:` 注明对标框架（Axon / Marten / eventhorizon / Commanded / Watermill），commit message 含 `ref: {framework} {file}`
