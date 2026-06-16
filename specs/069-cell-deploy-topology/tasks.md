# Tasks: Cell 部署拓扑 / 可重定位（Epic #1423 分解）

**Input**: `specs/069-cell-deploy-topology/spec.md` + `research.md`
**Prerequisites**: 探索结论（research.md）已确认 Wave-1 金丝雀整改、#1420、#1089 M8 均已落地，不在此列。
**Organization**: 按 user story 分组；每个 story = 一个 GitHub backlog issue（已登记：#1960-#1967，2026-06-13；event broker funnel 由既有 #1940 承载）。
**Tests**: 本仓约束（TDD、kernel ≥90% / 其余 ≥80%、archtest synthetic red case）对每个 story 默认生效。

## Format: `[ID] [P?] [Story] Description`

- **[P]**: 与同 phase 其它任务无文件交叉、可并行
- 路径为当前锚点，实施期以 US1 ADR 裁决为准

## Phase 1: 方向裁决（blocking — 除 US8 外所有 story 的前置）

### US1 — L3 方向 ADR「Cell 部署拓扑 / location-transparent transport seam」（P1）→ #1960

- [x] T001 [US1] 写 ADR `docs/architecture/202606131142-1423-adr-cell-deployment-topology.md`（PR #2001）：reconcile 宪法 V/Cell 数据主权 vs ADR `202605041430` §3.1「嵌入式框架」；裁决 ①topology 声明载体（默认方向：assembly.yaml 扩展）②sync transport 接口形态（contract-level HTTP，明确偏离 Service Weaver method-level RPC，引用其停更教训）③discovery 接口与 #303 共享 ④in-process 调用的 auth/可观测语义（不 bypass auth chain；trace 区分 in-proc/remote）+ 安全 gap matrix
- [x] T002 [P] [US1] 重开/改写 `030-review-0504` won't-do line 37（微服务化拆分）+ K-04（corecells 不迁移），可见推翻指向新 ADR；同步重评 `202605041430` 形态约束矩阵冲突段落（AI-robust 章程：amendment 必须同改）
- [ ] T003 [P] [US1] epic #1423 body 回填：Wave-1 完成状态（PR #1467/#1572/#2001 证据）+ 子 issue DAG（ship 收尾步执行）

**Checkpoint**: ADR 合入后 US2-US7 形态锁定

## Phase 2: 拓扑声明 + sync seam（ADR 后可双线并行）

### US2 — 拓扑声明作为一等 wiring（P1）→ #1962（blocked-by #1960）

- [ ] T010 [US2] assembly schema 扩展（`kernel/assembly/assembly.go` 模型 + `tools/codegen/` 派生）：`topology.colocated` 组 + `topology.remote[{cellID,endpoint}]`；colocated/remote 互斥校验；空拓扑 = 现状全 co-located
- [ ] T011 [US2] `gocell validate` 拓扑校验：cell 归属判定（本地/远程/缺失）静态导出；contractUsages 消费方的提供方 ∉ 本地∪远程 → 报错
- [ ] T012 [US2] `runtime/bootstrap/topology.go` 扩展：启动期 WriteOnce 拓扑判定 API（`IsColocated(cellID)` / `RemoteEndpoint(cellID)`），phase0 注入（`WithControlPlaneTopology` 先例）
- [ ] T013 [US2] codegen golden + synthetic red case（非法拓扑 fixture）

### US4 — CellTransport seam + 进程内短路（P1）→ #1963（blocked-by #1960，可与 US2 并行）

- [ ] T020 [US4] 新 runtime transport 包：`CellTransport` 接口（contract-level `DoContract`）+ `InProcessTransport`（bootstrap router build 后内存 dispatch，celltest mux 先例；不 bypass listener auth chain）
- [ ] T021 [US4] generated contract client 改走 `CellTransport` 注入（codegen funnel + golden），composition root 按拓扑选实现
- [ ] T022 [US4] trace/metrics 区分 in-process vs remote 调用（Service Weaver 可诊断性教训）
- [ ] T023 [US4] archtest：sync 调用必须经 CellTransport funnel（禁裸 http client 直拨兄弟 cell），synthetic red case + anti-vacuity

**Checkpoint**: 单进程形态下 transport seam 全接线、行为零变化

## Phase 3: 拆分形态（依赖 Phase 2）

### US3 — Broker-mandatory 双闸 fail-fast（P1）→ #1965（blocked-by #1962 + #1940）

- [ ] T030 [US3] `gocell validate` 静态闸：遍历 contractUsages，event contract 的 pub/sub 跨进程 ∧ EventBus=in-memory → 报错
- [ ] T031 [US3] bootstrap phase0 运行时闸：split 拓扑 ∧ in-memory bus → fail-fast（topology.validate()「postgres requires real」同形模板）；与 #1940 publisher funnel 对接（in-memory 仅 demo 拓扑可达）
- [ ] T032 [P] [US3] synthetic red case：split+in-mem 配置 fixture 两闸均红

### US5 — Sync 远程实现：Resolver + 内部 HTTP 客户端（P1）→ #1966（blocked-by #1962 + #1963；接口与 #303 共享）

- [x] T040 [US5] `Resolver` 接口 + 静态配置实现（`StaticResolver` over cellID→endpoint map；wiring 由 `celltransport.Resolve` 从 sealed topology 派生，避免 transport→bootstrap 反向 import 环）
- [x] T041 [US5] `RemoteHTTPTransport`：service token 出站签名（callerCell 身份，复用 HMAC keyring）、单预算（caller ctx）无 transport 重试、`RequiresDistributedReplay` 复用既有 callee 闸；`REMOTE-TRANSPORT-SEALED-01` reflect freeze
- [x] T042 [US5] principal 跨进程传播：`X-Gocell-Principal` 头（actor/subject/session，base64url JSON）**折进 service-token MAC**（防篡改），单一 sealed funnel `auth.SignInternalRequest`（裸 `GenerateServiceToken` 经 `SVCTOKEN-CALLER-CELL-REQUIRED-01` 收口）+ callee `principal_propagation.go` 重建（`CTXKEYS-PRINCIPAL-WRITE-CALLER-01` 扩展）；tenant 单源仍走 `X-Tenant-ID`
- [x] T043 [P] [US5] errcode：新增 `ERR_UPSTREAM_CELL_UNAVAILABLE`（既有 `KindUnavailable`）+ 前缀注册 + golden（`ERRCODE-PREFIX-OWNERSHIP-01`）；wire 折叠 503，专属码作服务端诊断
- [x] T044 [US5] 远端不可达/超时/5xx 错误映射 + 集成测试（单进程真实 TCP loopback，覆盖 happy + connection-refused/timeout/5xx/401/403/resolver-miss；真双进程端到端属 US7 journey）。另：解除 interim 门 TOPO-12 + `CheckRemotePlacementSupported`，topology.remote 经 `celltransport.Resolve`（`CELLTRANSPORT-SELECT-FUNNEL-01`）选型生效

### US6 — Per-cell 基建分区（P2）→ #1964（blocked-by #1960，与 Phase 2 同 wave 并行）

- [ ] T050 [US6] `runtime/capability` per-cell DB 凭据/连接注入 seam（单 cell assembly 独立凭据；缺失 fail-fast 不静默共享）
- [ ] T051 [US6] broker 连接 per-cell 注入点（与 #1940 funnel 对齐）
- [ ] T052 [US6] per-cell HMAC keyring / cell 身份颁发的安全模型评估（mTLS 缺口登记，threat matrix 进 US1 ADR amendment）

## Phase 4: 验收收口

### US7 — 双拓扑 journey 验收基建（P2）→ #1967（blocked-by #1965 + #1966）

- [ ] T060 [US7] 拆分拓扑 fixture：compose（多 cell-进程 + broker + PG）+ split assembly 双形态声明
- [ ] T061 [US7] 同一 journey 参数化双形态运行（run-journey 接入），结果一致性断言
- [ ] T062 [US7] CI integration job 接入（`.github/workflows/_build-lint.yml`）

### US8 — 跨 cell 直连禁制 archtest 收口（P2）→ #1961（无 blocker，no-regret 可立即开工）

- [ ] T070 [P] [US8] gRPC cross-cell 直连盲区规则（#1752 引入 `runtime/grpc` 后未覆盖）+ synthetic red case
- [ ] T071 [P] [US8] 「进程内跨 cell Go 直传 = 0」epic 验收信号的 archtest 永久化核对（LAYER-05/06 + MODULE-PROVIDE-NO-VALUE-HANDOFF-01 盲区清点，缺口补规则）

## Dependencies & Execution Order

```
US8 ──────────────────────────────┐（无依赖，立即可做）
US1(ADR) ─┬─ US2 ─┬─ US3 ←─ #1940 │
          │       └─ US5 ←─ US4   ├─→ US7（收口）
          ├─ US4 ─────┘           │
          └─ US6 ─────────────────┘
```

| Wave | Story | 性质 |
|------|-------|------|
| 1 | US1 #1960、US8 #1961、#1940 | 裁决 + no-regret，立即并行（先 #1960） |
| 2 | US2 #1962、US4 #1963、US6 #1964 | seam 主干，ADR 后并行（先 #1962，拓扑判定是消费源） |
| 3 | US3 #1965、US5 #1966 | 拆分形态，依赖 Wave 2 |
| 4 | US7 #1967 | epic 验收收口 |

## Notes

- 每 story 一个 issue、一个（或一簇）PR，各自走 `/ship`（worktree + TDD + 内置 review）。
- event broker funnel 不建新单：由 #1940 承载（US3 blocked-by）。
- 运行时动态 placement / sidecar / mTLS 全链 out of scope（归 #303 / 后续 threat-matrix 裁决）。
