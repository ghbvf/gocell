# 架构项 PR 实施计划（剩余开放项）

> 基线: `develop @ 34499ef5`（2026-04-28）
> 状态: Wave 1 / Wave 2 / **Wave 2.5 全部清零**；🎯 v1.0 发布硬约束已在 2026-04-25 全部达成。
> **2026-04-28 更新**：Wave 2.5 残余 PR-CFG-4 / PR-CFG-6 已迁入 `docs/plans/202604260058-l4-virtual-taco.md`（CFG-4 → PR-CFG-L 独立 / CFG-6 → PR-CFG-M.M.3 吸收）；Wave 2.5 板块从本 plan 完全清零。本 plan 此后只承载 Wave 3 / Wave 4 新模块路线图。
>
> **完整历史版本（含 Wave 1-2 已完工 PR 详情、13 轮 round 更新、所有完工 PR 描述）已归档**：
> [`docs/plans/archive/202604232330-025-architecture-pr-implementation-plan.md`](archive/202604232330-025-architecture-pr-implementation-plan.md)
>
> **后续工作请看**：[`docs/plans/202604252100-026-post-v1.0-cleanup-plan.md`](202604252100-026-post-v1.0-cleanup-plan.md)（PR-A39 起始的清债与加固计划）+ [`docs/plans/202604260156-pr-cfg-f-examples-security-consistency-baseline.md`](202604260156-pr-cfg-f-examples-security-consistency-baseline.md)（examples 安全一致性基线）。
>
> **本文件仅保留剩余开放项**——已完工任务（Wave 1 / Wave 2 / Wave 2.5 已清零项 / Wave 3 已完工 / Wave 4 已完工 / won't-do）全部移交归档。按需检阅 git log（`git log --oneline --grep='PR-A'`）或 backlog 关闭记录。
>
> 来源：`docs/plans/docs-backlog-md-docs-reviews-2026042219-graceful-backus.md` 架构层（P1/P2/P3）+ `202604191515-auth-federated-whistle.md`（F1-F7 基石）+ `202604211245-024-auth-rebaseline-implementation-plan.md`（A/B/C）+ `202604200313-v1.0-pre-release-plan.md` 残余 + 2026-04-24 六席位复核新发现。

---

## 剩余开放项（移交后续 plan / backlog 跟踪）

| Wave | 剩余 OPEN |
|---|---|
| **Wave 3** | PR-A15 / A16 / A17 / A36 / A37 / A38 |
| **Wave 4** | PR-A22 / A24 / A33 |

> Wave 1 / Wave 2 / **Wave 2.5 全部清零** / Wave 3 已完工（A14b/A18/A35）/ Wave 4 已完工（A19/A20/A23）/ won't-do（PR-CFG-5）—— 全部从本 plan 删除。
>
> **Wave 2.5 关闭索引**（2026-04-28）：
> - **PR-CFG-1** READYZ-RELAY-PROBE-FORWARD-01 — ✅ 2026-04-27 复核关闭（relay 已独立 ManagedResource 注册，最小修已过期）
> - **PR-CFG-4** CONFIG-READ-METADATA-ADMIN-GATE-01 — ➡️ 迁入 `202604260058-l4-virtual-taco.md` 作为 **PR-CFG-L** 独立 PR（🔴 P1 安全独占审）
> - **PR-CFG-5** — won't-do
> - **PR-CFG-6** OUTBOX-EMIT-FAILOPEN-DROP-COUNTER-01 — ➡️ 迁入 `202604260058-l4-virtual-taco.md` 作为 **PR-CFG-M.M.3** 吸收（与 archtest 加固批共用 packages.Load + go/types 范式）

### Wave 3 残余（6 PR）

#### PR-A15 KERNEL/WEBHOOK + WM-32 mTLS（P2, Cx3, ~3d）

**主线**：
- **LATER-K3 KERNEL/WEBHOOK** Webhook 出站 Receiver/Dispatcher 抽象（含 HMAC + SSRF 白名单）
- **WM-32 mTLS 中间件**（WinMDM defer，同批，因 mTLS 也是 outbound 安全面）

**前置**：L3 Outbox Relay 已稳定；SSRF 策略评审通过 + ADR 决议。
**文件面**：`kernel/webhook/`（新） + `runtime/http/outbound/`（可能新）。
**风险**：高；SSRF 策略 + HMAC 签名需安全评审。

#### PR-A16 KERNEL/RECONCILE + LATER-F-1 L3 示例（P2, ~2d）

**主线**：`kernel/reconcile/` L3 收敛控制循环（Reconciler 模式）+ `examples/l3projection/` 官方样板代码。
**搭车理由**：L3 Reconciler 模式发布时官方补 L3 reference cell 示范业务实现。
**文件面**：`kernel/reconcile/`（新） + `examples/l3projection/`（新）。
**风险**：中；Reconciler API 设计需 ADR。

#### PR-A17 RUNTIME/SCHEDULER + WM-18 延迟消息（P2, ~2d）

**主线**：`runtime/scheduler/` Cron + 完整定时任务支持（分布式防重 + 并发）；scheduler 稳定后探索 RabbitMQ `x-delayed-message`。
**文件面**：`runtime/scheduler/`（新） + 可能 `adapters/rabbitmq/delayed.go`。
**风险**：中；分布式协调依赖 Redis/etcd；测试桩需覆盖。

#### PR-A36 HTTP-METRICS-LABEL-REALIGN（P2, 🟠 多 cell assembly 部署前触发，~4h）

**问题**：`runtime/bootstrap/bootstrap_phases.go:675-683` `cellID := b.assemblyID`（fallback 到 `b.assembly.ID()` 再到 `"default"`）；`runtime/observability/metrics/provider_collector.go` label `"cell"` 值取自 `cfg.CellID`。多 cell assembly（如 corebundle 含 access/audit/config 三 cell）下所有 HTTP 指标会贴同一 `cell="corebundle"`，按 cell 维度 dashboard/告警会误归因。

**主线**：
- **Step 1 最小兼容**（2h）：provider_collector 输出两个 label — `assembly`（保留现有值）+ `cell`（暂时 = assembly，保留 dashboard 兼容性）；或直接把 `cell` 重命名为 `assembly` 并发 dashboard migration note
- **Step 2 真解**（2h）：`router.Route` 注册时把 owning cell 写入 request context；`middleware/metrics.go` 从 ctx 读取 cell；`NewProviderCollector` 配置改为 `AssemblyID string, CellResolver func(*http.Request) string`

**文件面**：`runtime/bootstrap/bootstrap_phases.go` + `runtime/observability/metrics/provider_collector.go` + `runtime/http/router/router.go` + `runtime/http/middleware/metrics.go` + `runtime/http/middleware/metrics_wiring_test.go`。
**ref**：Kratos request labels（operation/kind/code/reason 分层）、go-zero HTTP metrics（path/method/code 不混服务名）、OpenTelemetry Resource vs Semantic-attr 分层。

#### PR-A37 DEVTOOLS-METADATA-EXPORT（Cx2, ~1d，🟡 解锁 gocell-web 自包含构建）

**主线**：`gocell export metadata [--format=json|yaml] [--out=<path>] [--include-deps] [--root=<dir>]` 子命令——复用 `kernel/metadata.NewParser` 解析全部元数据 → 顶层结构 `{schemaVersion, generatedAt, cells, slices, contracts, assemblies, journeys, journeyStatuses, cellDependencyGraph}`。`cellDependencyGraph` 复用 `kernel/governance.DependencyChecker.buildDependencyGraph()`。
**部署模式**：静态导出优先——gocell-web Dockerfile build 阶段执行 `gocell export metadata --include-deps --out=public/metadata.json`，前端改 `fetch('/metadata.json')`，零 CORS、零 live endpoint 部署耦合。
**文件面**：`cmd/gocell/app/export.go`（新）+ `kernel/metadata/export.go`（新）+ `kernel/metadata/export_test.go`（新 golden）+ `kernel/governance/depcheck.go`（暴露 `Graph()` helper）+ `docs/guides/devtools-metadata-export.md`（新）。
**对标**：`kubectl get -o json` / `helm show all` / goda `pkgs -json`。
**依赖**：无（PR-A38 是可选增强）。

#### PR-A38 TOOLS/DEPGRAPH（Cx3, ~1.5-2d，🟡 v1.0 后做，goda-like 包级图）

**主线**：新模块 `tools/depgraph/`（**严禁放 `kernel/`**——`golang.org/x/tools/go/packages` 违反 kernel 依赖约束）。API：`Load(rootDir, opts) (*Graph, error)` → `Graph{Nodes []PkgNode, Edges []PkgEdge}`；`PkgNode` 含 `ImportPath / Layer / CellID / Files / LinesApprox`；输出 JSON（被 PR-A37 `--include-deps` 消费）+ DOT。
**搭车（可选）**：`LAYER-GO-IMPORT-01` governance rule — 用 depgraph 数据替换/增强 `tools/archtest/` 现有 file-level string scan，做"传递闭包"级校验；本搭车不在主线，留作 follow-up（避免 review 失焦）。
**ADR 决策点**：模块归属 `tools/depgraph/` ✅；导出粒度包级 ✅；缓存策略 in-memory（首次 ~3-10s）。
**文件面**：`tools/depgraph/{depgraph,graph,layer}.go` + 测试 + `cmd/depgraph/main.go`（独立 CLI 可选）+ `cmd/gocell/app/export.go`（PR-A37 `--include-deps` 实现）+ `docs/guides/depgraph.md`（新）。
**对标**：`loov/goda` reach/cut/nodes 不复刻；本 PR 仅提供"加载 + 输出图"基座。
**依赖**：PR-A37 落地（PR-A38 是 PR-A37 `--include-deps` 提供方）。

---

### Wave 4 残余（3 PR）

> **PR-A21 AL-04 Auth JWT 依赖评估** — 已移除（2026-05-01）。结论 won't-do：JWT 是事实标准、无第二个 provider；搭车的 T5 AUTH-SIGNER-01 解除 "golang-jwt v6 发布" 这条不可控的 gating（`crypto.Signer` 是 stdlib，jwt/v5 已 type-assert 支持），改为按 caller 需 HSM/KMS/EC 时独立 ship，详见 backlog T5 + 029 §四。

#### PR-A22 Cell ISP 拆分（~1.5d）

**主线**：`LATER-ARCH-1 CELL-IFACE-ISP-SPLIT-01` 12 方法基础接口 → `Cell` + `CellLifecycle` + `CellMetadata`。
**文件面**：`kernel/cell/` + 所有 `cells/*/cell.go`。
**风险**：高；接口破坏性变更，所有 cell + examples 需同步更新（分阶段迁移）。

#### PR-A24 DURABLE-TYPE + G-6 + kernel/replay + rollback（~2d）

**主线**（打包长期债）：
- **DURABLE-TYPE-01** L2/L3 持久化级别类型系统静态保护研究 + 实现
- **G-6 ASSEMBLY-BOUNDARY-DERIVED-01** boundary.yaml 存在性 + 一致性校验（关联 PR220-e2 GENERATED-BOUNDARY-STRATEGY 决策）
- **LATER-K6 KERNEL/REPLAY** 投影重算（v1.1）
- **LATER-K7 KERNEL/ROLLBACK** Rollback 元数据模型（v1.1）

**搭车理由**：都是低频、独立新模块；打包成一个 v1.1 sprint。
**文件面**：`kernel/replay/`（新） + `kernel/rollback/`（新） + `kernel/governance/` + metadata 类型探索。
**风险**：低（业务不紧迫），可随时排期。

#### PR-A33 REFRESH-OPAQUE-POLISH（X12 + X13 + X14，~8h）

**主线**：
- **X12 REFRESH-IDLE-EXPIRE-01**（3h）`refresh_store.go` 加 `idle_expires_at` 滑动窗口；每次 Rotate 刷新 `last_used + idle_ttl`；ref: Zitadel
- **X14 REFRESH-GRACE-COUNTER-01**（2h）`first_used_at` + `used_times` 列，grace 窗口内重用次数上限（默认 3）触发 `ErrTokenReused`；ref: Hydra Fosite
- **X13 REFRESH-PARTITION-01**（3h，🟠 生产流量阈值后）`refresh_tokens` 按 `expires_at` range 分区，`DROP PARTITION` 替代批量 DELETE

**搭车理由**：全部在 `adapters/postgres/refresh_store.go` + migrations；X12/X14 语义补强，X13 性能扩容，一批合测试工作量集中。
**依赖**：**PR-A29 ✅ PR#251 已合**（主链 opaque 生效）。
**文件面**：`adapters/postgres/refresh_store.go` + migration 010/011/012 + `runtime/auth/refresh/policy.go`。
**风险**：中；分区涉及数据迁移，建议 X13 单独 staging 演练。

---

## 设计原则（保留参考）

1. **文件亲缘**：同目录或同模块的修改塞进同一 PR，降低 review 成本
2. **语义内聚**：按"治理规则"、"Auth 收口"、"Contract spec"等单一主题切分
3. **抽取先于业务**：先落 helper / 新接口，再把业务切换过去
4. **Cx3 独立审**：高复杂度独立 PR，防互相污染 review
5. **风险由低到高**：pkg helper / CI 治理 → 业务 cell 拆分 → 协议级改造

## 验证方式（保留参考）

每个 PR 必须：
1. 本地跑 `golangci-lint run ./修改的包/...` 0 issues
2. 接口变更需跑 `go build -tags=integration ./...`
3. Cx3 复杂度 PR 先输出方案 ADR，6 席位审通过后开工
4. 高风险 PR 必须走 `/ultrareview`
5. 🔴 标记 PR 必须跑完整 `go test -race -tags=integration ./...`

完成标志：
- `gocell validate --strict` 0 error
- `gocell check contract-health` 0 warning
- v1.0 release 前 Wave 1-2 全部落地（含 PR-A29 refresh 主链）；Wave 3 按需；Wave 4 v1.1+

---

## 高风险 PR 清单（仅列剩余 OPEN）

- **PR-A15** KERNEL/WEBHOOK（Cx3，需 SSRF 安全评审 + HMAC 签名 ADR）
- **PR-A22** Cell ISP 拆分（破坏性，所有 cell + examples 同步）
- **PR-A24** DURABLE-TYPE + G-6 + replay/rollback（v1.1 长期债打包）
- **PR-A33** REFRESH-OPAQUE-POLISH（X12/X13/X14；X13 partition 涉及数据迁移）

---

## Lane 并行执行计划

> 10 条 OPEN 项按文件域 + 主题分 4 条 lane，lane 内串行、lane 间并行。文件域不重叠才能开 worktree 并行；下方 Sprint batch 已按冲突避让。
> Wave 2.5 残余的 L1 Config / L2 Outbox lane 已迁出本 plan（CFG-4 / CFG-6 → l4-virtual-taco PR-CFG-L / PR-CFG-M）；本节只承载 Wave 3 / Wave 4 lane。

### 4 条 lane（剩余开放项）

| Lane | 任务链 | 主要文件域 | 备注 |
|---|---|---|---|
| **L1 Auth / Refresh** | PR-A33 | `adapters/postgres/refresh_store.go` + migrations 010/011/012 | A33 X12+X13+X14 一批（PR-A21 已移除，见 Wave 4 残余说明） |
| **L2 Kernel 新模块** | PR-A15 ‖ PR-A16 ‖ PR-A17 → PR-A24 | `kernel/webhook/` / `kernel/reconcile/` / `runtime/scheduler/` / `kernel/replay/` / `kernel/rollback/` | A15/A16/A17 文件域不重叠可三路并行；A24 v1.1 长期债打包 |
| **L3 DevTools / Tooling** | PR-A37 → PR-A38 → PR-A36 | `cmd/gocell/app/export.go` + `kernel/metadata/export.go` + `tools/depgraph/` + `runtime/observability/metrics/provider_collector.go` + `runtime/http/middleware/metrics.go` | 串行：A38 是 A37 `--include-deps` 提供方；A36 HTTP metrics label realign（多 cell assembly 部署前触发，🟠） |
| **L4 Architecture (破坏性)** | PR-A22 Cell ISP | `kernel/cell/` + 所有 `cells/*/cell.go` + examples | 🔴 高风险；独占审，禁止与 L1/L2 同 batch（cells/* 大面积冲突） |

### 推荐执行 Sprint

> 默认双人 worktree 并行；单人按 sprint 拉成 1.6×。每 sprint ~5 个净工作日窗口，含 review 往返。

#### Sprint 1（~1d 净）— DX 短链路

| worktree | PR | 工时 | 文件域 | 冲突检查 |
|---|---|---|---|---|
| A | **PR-A37** DEVTOOLS-METADATA-EXPORT | 1d | `cmd/gocell/` + `kernel/metadata/export.go` | 单 lane |

**原则**：A37 是 gocell-web 自包含构建解锁。（原 worktree B PR-A21 已移除，见 Wave 4 残余说明）

#### Sprint 2（~2-2.5d 净）— Tooling 收口

| worktree | PR | 工时 | 文件域 | 依赖 |
|---|---|---|---|---|
| A | **PR-A38** TOOLS/DEPGRAPH | 1.5-2d | `tools/depgraph/`（新） | PR-A37（Sprint 1）已合 |
| A→ | **PR-A36** HTTP-METRICS-LABEL-REALIGN | 4h | `runtime/observability/metrics/provider_collector.go` + `runtime/http/middleware/metrics.go` + `runtime/bootstrap/bootstrap_phases.go` | 多 cell assembly 部署前触发；与 A38 同 lane 串行 |

**原则**：A38 + A36 同 lane 串行（都触 runtime tooling）；多 cell assembly 部署窗口决定 A36 是否需提前。

#### Sprint 3（~2-3d 净）— Wave 4 长期债打头阵

| worktree | PR | 工时 | 文件域 | 冲突检查 |
|---|---|---|---|---|
| A | **PR-A33** REFRESH-OPAQUE-POLISH | 1d | `adapters/postgres/refresh_store.go` + migrations | 与 B 无重叠 |
| B | **PR-A22** Cell ISP 拆分 | 1.5d | `kernel/cell/` + 所有 `cells/*/cell.go` | 🔴 独占审；🚫 禁止与任何 cells/* 修改同 batch |

**原则**：A22 是破坏性变更，所有 cell 同步迁移；同 sprint 不安排其他触 cells/* 的 PR；A33 lane 独立可并行。

#### Sprint 4（~5-7d 净）— Kernel 新模块批

| worktree | PR | 工时 | 文件域 | 依赖 |
|---|---|---|---|---|
| A | **PR-A15** KERNEL/WEBHOOK + WM-32 mTLS | 3d | `kernel/webhook/`（新） + `runtime/http/outbound/` | SSRF 安全评审 + ADR 通过 |
| B | **PR-A16** KERNEL/RECONCILE + L3 示例 | 2d | `kernel/reconcile/`（新） + `examples/l3projection/`（新） | Reconciler API ADR |
| B→ | **PR-A17** RUNTIME/SCHEDULER + WM-18 | 2d | `runtime/scheduler/`（新） + 可能 `adapters/rabbitmq/delayed.go` | 分布式协调依赖（Redis/etcd）评审 |
| A→ | **PR-A24** DURABLE-TYPE + G-6 + replay/rollback | 2d | `kernel/replay/` + `kernel/rollback/` + `kernel/governance/` | 无紧迫性，按 v1.1 sprint 排 |

**原则**：A15/A16/A17 文件域 100% 不重叠，可三路并行；A24 长期债排 sprint 末尾，作为收尾包；4 个 PR 全部 Cx3 起步，review bandwidth 是真瓶颈，单 sprint 不超过 2 个 Cx3 同时进 review 队列。

### 冲突避让矩阵（关键交叉）

| PR-A | PR-B | 冲突点 | 解决 |
|---|---|---|---|
| PR-A22 | PR-A15/A16/A17 | A22 改 `cells/*/cell.go`，A16 写 example cell，A17 可能加 cell-side scheduler hook | A22 必须独占 sprint，禁止与任何写 cells/* 的 PR 同窗口 |
| PR-A37 | PR-A38 | A38 是 A37 `--include-deps` 提供方 | 严格串行（Sprint 1 → Sprint 2） |
| PR-A38 | PR-A36 | 都触 runtime tooling | 同 lane 串行（Sprint 2 worktree A） |

### 时间线（双人并行估算）

| Sprint | 净工时 | 含 buffer 1.4× | 累计 |
|---|---|---|---|
| Sprint 1 | ~2d | ~2.8d | 2.8d |
| Sprint 2 | ~2.5d | ~3.5d | 6.3d |
| Sprint 3 | ~2.5d | ~3.5d | 9.8d |
| Sprint 4 | ~7d | ~10d | **19.8d（~4 周）** |

> Sprint 1+2 完成即可宣布 v1.0 后短链路（DX + tooling + auth 评估）全部清零；Sprint 3+4 是 v1.1 节奏，可与 026 plan / Wave 5+ 工作交错。
> Wave 2.5 残余 L 与 M 由 `202604260058-l4-virtual-taco.md` 负责，与本 plan 完全独立可并行。
> 单人场景把上表 ×1.6 → ~6.4 周。

---

## 备注

- **触发器项**：T1/T2/T4/T5 按条件延后；T3 已在 PR-A12 埋点
- **历史轮次（1-13）摘要**：详见归档全本 + git log；Wave 1 → Wave 2 主线在 2026-04-23 至 2026-04-25 三天完成，是发布密集窗口
- **auth/config 域源计划已委托并完工**：`202604191515-auth-federated-whistle.md` F1-F7 / `202604211245-024-auth-rebaseline-implementation-plan.md` A/B/C / `202604200313-v1.0-pre-release-plan.md` Batch 5/6 全部 ✅
- **剩余 OPEN 项跟踪**：本 plan + `docs/backlog.md` + `202604252100-026-post-v1.0-cleanup-plan.md` 三处对齐；新发现 finding 默认登记 backlog，不再续编号到本 plan
