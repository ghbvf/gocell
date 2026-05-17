# GoCell Backlog

> **单源 backlog** — 按 14 capability units 主轴组织。  
> 主轴权威源：[`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md) §1  
> 历史归档：[`docs/backlog/archive/`](backlog/archive/)
>
> 基线：`refactor/593-archtest-pass-consolidated`（2026-05-16；含 PR #481/#482/#483/#484/#485/#486/#487/#488/#490/#492/#493/#494/#495/#496/#497/#498/#499/#500/#507/#511/#522 + plan 034 S4d/S4e ✅ (PR #494) + plan 040 阶段 1 ✅ / 1.5 ✅ (#495) / 1.6 ✅ (#500) / 阶段 2 全部 ✅ / 阶段 3 全部 ✅ (#500/#507/#522) / LegacyAllowlist 已清零 (PR #522) + Stage 1.8 FindFirstChild+USAGE-02 (#511)）

---

## Schema

每个 capability 章节一张表，每条 item 一行：

| 列 | 取值 | 说明 |
|---|---|---|
| ID | 沿用旧值；新建项 `<CAP_NUM>-<DOMAIN>-<NNN>` | 唯一 |
| 描述 | `**标题** — 现状: ...; 修复方向: ...`；次要能力末尾 `(also: cap-XX)` | 主内容 |
| Type | `feat` / `bug` / `debt` / `refactor` / `arch-opt` / `doc` / `test` / `fu` | `arch-opt` = "架构优化" |
| P/Cx | 例 `P1/Cx2`；DONE 行可填 `—` | Priority + Complexity 合一列 |
| Flag | 🔴 硬约束（即"发布阻塞项"）/ 🟠 条件延后 / 🟡 可延后 / 🟢 已纳入 plan / ✅ 已完成 | 状态由 Flag 编码：✅ = DONE 待人工归档；其余视为 OPEN |
| Trigger | 仅 Flag=🟠 必填 | 触发条件文本 |
| Files | ≤ 3 个 | 主要涉及文件 |
| Source | PR# / review 报告路径 / issue# | 来源 |

**跨域决策**：(1) 主代码改动落处 → primary；(2) 平手则 contract owner cell 所属 capability；(3) 还平手按 `cells > runtime > kernel > tools` 优先级；(4) 跨 ≥ 4 cap 且无明确 owner 才进 `cap-x-cross`。次要 capability 在描述里写 `(also: cap-XX)`，物理只在 primary 章节出现一次。

**归档**：人工。Flag=✅ 留主表至人工迁 [`archive/`](backlog/archive/)（按季度命名 `2026-q2-completed.md`）；WONTFIX 立即移 archive + 理由必填。

---

## cap-01: Cell 声明与生命周期

> 主要包：`kernel/cell` + `assembly` + `lifecycle` + `worker` + `runtime/worker`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| B2-K-02 | **Kernel Must*/error-first 混用** — 现状: `MustNewAuthJWT` 等 Must 系列与 error-first 构造器混用，composition root 残留 panic；修复: 生产路径改 error-first，Must 仅 test-only/cmd 顶层 | bug | P1/Cx3 | 🟡 | — | `kernel/wrapper/handler.go` + `kernel/cell/auth_plan.go` + `pkg/contracttest/` | backlog2 §2 B2-K-02 |
| B2-PROVISIONER-MUTEX-REVIEW | **Provisioner mutex 清理 review** — 现状: A26-R1 已删 initialadmin，但 provisioner mutex 残留；修复: PG adapter 落地后审视是否仍需 mutex（PG row-level lock + UNIQUE constraint 已覆盖并发场景，mutex 多半冗余）。**trigger 已达成（PR #482 S4a ship PG accesscore wiring）**，可立即审视 — 待 plan 039 P2 整理收口 | refactor | P2/Cx1 | 🟠 | trigger 已达成（PR #482）— 待 plan 039 排期 | `cells/accesscore/internal/adminprovision/provisioner.go` | backlog2 §13 + PR #482 unblock |
| C-04 | **CELLS-INIT-TEMPLATE-CONVERGE**（含 C-07 emitter health probe helper）— 3 cell Init 切分各异 + internal/ 子包不对称；修复: `kernel/cell` 提供 `BaseCell.RegisterStandard(reg, StandardInit{...})` 模板 + scaffold 预生成 `internal/{ports,domain,dto,events,mem}` 五目录 + 3 cell 改造 + scaffold 升级 + 抽 `cell.RegisterEmitterHealthProbes(reg, emitter)` helper（删 3 cell 4 处重复）| refactor | P2/Cx2 | 🟡 | K-06 落地后 | `kernel/cell/` + `cells/{accesscore,auditcore,configcore}/` + scaffold 模板 | 030 §3 C-04 + C-07 |
| C-06 | **L0-CELL-DECISION** — `l0Dependencies: []` 在 3 cell 全空，无任何 `type: l0` 实例，schema 字段是死代码路径；修复: 二选一 (a) 升 `pkg/query.CursorCodec` 等共享逻辑为示例 L0 cell；(b) 文档明确"L0 cell 是未来扩展点，当前无实例" | doc | P2/Cx1 | 🟡 | — | `cells/` + `kernel/metadata/` + docs | 030 §3 C-06 |
| C-09 | **CELL-SPLIT-LAYOUT-NORMALIZE** — accesscore + configcore 三文件范式不一致：(a) `configDirectPublishMode`/`ensureCursorCodec` 是 pure helper 但放 `cell_init.go`；(b) `RegisterSubscriptions` 放 `cell_routes.go` 名不副实；修复: 引入 `cell_lifecycle.go`（订阅注册）+ `cell_helpers.go`（pure helper）命名惯例；反向迁移 + scaffold 模板同步 | refactor | P2/Cx2 | 🟡 | K-07 一并 | `cells/accesscore/` + `cells/configcore/` + scaffold | 030 §3 C-09 |
| G-10 | **KERNEL-CELL-PACKAGE-DECOMPOSE** — kernel/cell 是 god-package：含 AuthPlan(JWT/MTLS) + Outbox EmitterFactory + Health alias；`Cell` 接口 11 方法混合生命周期与元数据自省；3 个 "registry" 命名混乱；修复: (1) `auth_plan.go` → `kernel/auth/`；(2) `mode_resolver.go` → `kernel/outbox/` + 改名 `emitter_resolver.go`；(3) `cell.Registry` → `cell.Registrar`；(4) `Cell` 拆 `CellLifecycle` + `CellDescriptor`；删 `health.go` 单行 alias | refactor | P1/Cx3 | 🟡 | 与 029 #13 PR-A22 协同 | `kernel/cell/` + `kernel/auth/` + `kernel/outbox/` + `kernel/registry/` | 030 §3 G-10 |
| SWEEPER-OPAQUE-INTERFACE-HARD-UPGRADE-01 | **Sweeper Hard 升级** — 现状: Medium runtime fail-closed sentinel (built) 已建；修复: 改 NewSweeper 返回 opaque interface，零值不可表达 | arch-opt | P3/Cx3 | 🟢 | 出现第二个 zero-value `command.Sweeper{}` caller | `kernel/command/sweeper.go` | sweeper.go godoc + CHANGELOG PR441 |
| PR441-FU-CELLINVENTORY-METADATA-READONLY-VIEW-01 | **CellInventory.Metadata() deep-copy hot-path 优化** — 现状: 每次调用 b.meta.Clone() 防御复制；修复（推测性）: 改 MetadataView() metadata.CellMetaView 返回只读 view（type-system 不可变）；或 godoc 警示 "callers should cache result" | perf-opt | P3/Cx3 | 🟢 | Metadata() 出现在 hot path benchmark p99 退化 ≥ 10% | `kernel/cell/interfaces.go` + `kernel/cell/base.go` | PR441 review architect-F1（推测性，无 benchmark 数据）|
| SEALED-MARKER-DEFENSE-EXPANSION-BUNDLE | **Sealed marker / typed primitive 防线扩展束（PR #441/442 review 聚合，7 子条）** — 现状: sealed marker / typed primitive 主防线已 ship（CELL-RAW-INFRA-SEALED-MARKER-01 / SCAFFOLD-AUTOGEN-SCOPE-SEALED 等），扩展面 7 处待收口；修复: 各子条独立排期或合并 PR-A23 sealed marker 扩展批。子条：<ul><li>**PR441-FU-CELLEMITTER-SEALED-MARKER-01** (P2/Cx3, 🟠 立即排期 PR-A23) — 扩展 sealed marker 到 outbox.CellEmitter，WithEmitter 改签 + 3 cell + 新 archtest + ADR (Files: `kernel/outbox/{emitter,cell_marker}.go` + `cells/{accesscore,auditcore,configcore}/cell.go`，PR441 user-finding F1)</li><li>**PR441-FU-RAW-INFRA-PARAM-SIBLING-EXPAND-01** ✅ closed by PR #481 (PR-S7, 2026-05-13；决策反转) — 原 architect "不立项"（sealed marker Hard 主防线已覆盖）在 PR-S7 sealing 10 slice WithTxManager 时被推翻：ADR §D1 line 46 "服务签名零变化"与 slice-level sealing 新 scope 矛盾。PR-S7 同 PR 把 `isCellPackageRootFile → isCellSubtreeFile`（cell-package root → `cells/<x>/**/*.go` + `examples/<demo>/cells/<x>/**/*.go`，超原条目仅 sibling），即时扫出 `examples/todoorder/cells/ordercell/slices/ordercreate/service.go` 第 11 处 raw 暴露；ADR `202605101900` Amendment 2026-05-12 记录边界扩展 (Files: `tools/archtest/cell_public_option_param_test.go` + ADR 202605101900，PR441 reviewer F3-2)</li><li>**CELL-PUBLIC-OPTION-NAMED-IFACE-EMBED-01** (P1/Cx2, 🟢 PR #441 round-4 收口) — `canonicalFromType` 在 `*types.Named` 分支补 `Underlying().(*types.Interface)` walk；fixture 加 named local interface embed case (Files: `tools/archtest/cell_public_option_param_test.go`，PR441 round-3 follow-up)</li><li>**ADR-CELL-RAW-INFRA-WORDING-01** (P2/Cx1, 🟢 PR #441 round-4 同 PR) — ADR §"AI 写 WithFoo 在 cell.go 编译期被拒"措辞不准；改为"定义可编译但调用 site 因 sealed marker 缺失被拒"两层 (Files: ADR 202605101900，PR441 round-3 follow-up)</li><li>**SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01** (P2/Cx3, 🟠 跨包 spec 输入误用 / 第 4 个副本) — typed `ScaffoldID` value type + 共享 validator；三处 spec (cmd/gocell + cellgen + assembly) 字段类型升级 (Files: `pkg/scaffoldid/`(新) + 3 spec 包，K#09 PR#442 round-5 R6 + kernel-guardian F4)</li><li>**ASSEMBLY-META-SYNTHESIS-FIELD-GUARD** (P2/Cx2, 🟠 AssemblyMeta 字段集变更 / synthesizeAssemblyMeta 漏字段事故) — reflect 字段计数 guard archtest，AI-rebust godoc Medium → reflect Hard 升级候选 (Files: `tools/archtest/` + `kernel/assembly/generator.go` + `kernel/metadata/types.go`，K#09 PR#442 round-6 + kernel-guardian F2)</li><li>**SEALED-MARKER-FILE-LIST-AUTODISCOVER-01** (P3/Cx2, 🟢 sealed marker 第三个包 PR-A23 后) — `sealedMarkerFiles` hand-crafted 改 `packages.Load(./kernel/...)` + grep `internalCell` 前缀类型自动发现 (Files: `tools/archtest/sealed_marker_noop_transparency_test.go`，reviewer F4)</li></ul> | arch-opt | P1/Cx3 | 🟠 | A.1/A.5/A.6 立即排期；A.2/A.3/A.4/A.7 触发型按各自条件 | `kernel/{outbox,persistence}/` + `tools/archtest/` + `pkg/scaffoldid/`(新) + scaffold 输入校验 | PR #441/#442 六角色 review 聚合 + ADR 202605101900 |
| BOOTSTRAP-CONTROL-PLANE-DECOUPLE-BUNDLE | **Bootstrap 控制面/业务面解耦束（PR #441 review 聚合，3 子条）** — ✅ **已实施（PR #212 worktree 212-control-plane-decouple，2026-05-17）**。ship 摘要：<ul><li>**C.1 LIFECYCLE-CLOCK-CONTROL-PLANE-DECOUPLE-01** ✅ — kernel `Sweeper` 删时钟字段（Hard：类型不可表达）；`runtime/command` 控制面 ticker/probe 收敛到 `controlPlaneTicker` / `controlPlaneProbeTimer` 两个函数级 marker（Medium carve-out，backlog `CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01` 同 PR 登记）；frozen-clock deadlock 物理消除</li><li>**C.2 LIFECYCLE-OWNER-CTX-PROPAGATION-01** ✅ — `cell.LifecycleHook.OnStart(ctx)` 语义重定义为 owner ctx；bootstrap 派生 `ownerCtx=WithCancel(runCtx)`，ownerCancel 先于 lifecycle.Stop；`cells/accesscore/refresh_gc.go` + `gc_worker.go` 删 `context.WithoutCancel` 自 re-root；ADR 202605102000 §D1 RETRACTED / §D3 RESOLVED（同 PR 精确重写）</li><li>**C.3 SWEEPER-OBSERVABLE-01** ✅ — 删 `onError` 静默回调；`SweepTick` 返回 `errors.Join` 聚合错误；`SweeperLifecycle.runLoop` slog.Error + `command_sweep_errors_total{cell}` counter</li></ul>**second-round review 补强（PR #531，2026-05-17，ADR §Amendment A1）**：A1-1 P1-1 启动死锁 backstop（`bootstrap.superviseLifecycleStart` = caller ctx + `WithStartupTimeout` + `ErrBootstrapStartupTimeout`）；A1-2 P1-2 失败 hook goroutine 在 sibling rollback 前 `workCancel`；A1-3 P2-2 `SweeperLifecycle.BusinessClock` 供 `SweepTick` now（控制面 ticker 仍 real-time，kernel Sweeper 仍无时钟字段）；A1-4 P2-1 `Sweeper.Validate()` OnStart readiness gate；A1-5 P1-4 counter label preflight 防 crashloop；A1-6 P1-3 carve-out `controlPlaneClockCarveOut` allowlist 锚定（Soft→Medium）；A1-7 P2-3 StartTimeout godoc 同步。ADR：`docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md` | bug+arch-opt | — | ✅ PR #212 + #531 | — | `kernel/command/sweeper.go` + `runtime/command/lifecycle.go` + `runtime/bootstrap/{bootstrap,lifecycle,options_lifecycle}.go` + `tools/archtest/clock_invariants_test.go` | PR #441 second/third-round + PR #531 second-round review 聚合 + ADR 202605102000 |
| CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01 | **控制面时钟 Hard 升级** — 现状: `runtime/command` 内 `controlPlaneTicker` / `controlPlaneProbeTimer` 两个函数使用函数级 `//archtest:allow:clock-injection:control-plane` comment-guard，AI-rebust 评级 Medium（**PR #531 A1-6 锚定后**：marker + archtest 内 `controlPlaneClockCarveOut` `{rel→函数名}` allowlist 双门，marker 单独不再豁免；修复前等效 Soft，已闭环到 Medium，RED 自检 `control_plane_marker_wrong_path_violates` / `control_plane_marker_wrong_func_violates`）；修复方向: 引入 sealed typed real-only 控制面时钟 funnel，让"非真实时钟源驱动控制面 ticker/probe"在 Go 类型系统层不可表达（Hard）；典型实现: `type ControlPlaneClock struct{}` 私有字段，仅含 `NewTicker(d) *time.Ticker` / `NewTimer(d) *time.Timer` 两个方法，包外无法构造 fake 实现 + archtest 锁定 `controlPlaneTicker` / `controlPlaneProbeTimer` 唯二调用点。受豁免函数清单（截至 PR #531，权威源 = archtest `controlPlaneClockCarveOut`）: `controlPlaneTicker` + `controlPlaneProbeTimer`（均在 `runtime/command/lifecycle.go`）。ref: ADR `202605170000-adr-control-plane-business-plane-decouple.md` §D-A | arch-opt | P3/Cx3 | 🟢 | comment-guard 被 AI 绕过导致 fake clock 重新注入控制面 scheduling | `runtime/command/lifecycle.go` + `tools/archtest/clock_invariants_test.go` | ADR 202605170000 §D-A; ai-collab.md §Medium backlog 登记要求 |
---

## cap-02: 元数据解析与治理

> 详见 [`backlog/cap-02-metadata-governance.md`](backlog/cap-02-metadata-governance.md)（25 条目，按主题分 4 个 h2 子节）

**子节索引**：
- [02.1 kernel spec / contractspec / depgraph](backlog/cap-02-metadata-governance.md#02.1-kernel-spec--contractspec--depgraph)
- [02.2 typeseval / archtest helper](backlog/cap-02-metadata-governance.md#02.2-typeseval--archtest-helper)
- [02.3 governance rule (G-series + PR-FU)](backlog/cap-02-metadata-governance.md#02.3-governance-rule-g-series--pr-fu)
- [02.4 杂项](backlog/cap-02-metadata-governance.md#02.4-杂项)

## cap-03: Contract 注册与发现

> 主要包：`kernel/wrapper` + `kernel/registry` + `pkg/contracts`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| P1-8 | **DEVICE-LIST-API** — 现状: `cells/devicecell/slices/devicelist/` 缺；修复: 新建 slice + `GET /api/v1/devices` 分页 + contract + contract_test | feat | P1/— | 🟡 | — | `cells/devicecell/slices/devicelist/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| B2-T-04 | **Contract userId 风格混用** — 现状: payload schema 字段命名混用 userId/UserID；修复: 统一 camelCase | refactor | P2/Cx2 | 🟡 | — | `contracts/event/user/created/v1/payload.schema.json:6` | backlog2 §8 B2-T-04 |
| F-03 | **PKG-CONTRACTS-BOUNDARY-DOC + ARCHTEST** — `pkg/contracts` 角色未在 README/doc.go 说明，未来若放业务领域类型 archtest 不会立即报；`pkg/ctxkeys` 与 `kernel/ctxkeys` 边界微妙；修复: `pkg/contracts/doc.go` 明确"仅承载 contracts/*.yaml Go 类型镜像 + Schema helper" + archtest `PKG-CONTRACTS-NO-BUSINESS-TYPE` + `PKG-CTXKEYS-NO-CELL-MODEL` | doc | P1/Cx2 | 🟡 | — | `pkg/contracts/doc.go` (新) + `tools/archtest/` | 030 §3 F-03 |

---

## cap-04: HTTP 入站处理

> 主要包：`runtime/http/{router,middleware,health,devtools}`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| A26-R3 | **SETUP-PATH-NAMESPACE-POLICY-01** — 现状: 顶级 `/api/v1/setup/` 与 per-Cell 入口规则未明文；修复: 在 api-versioning.md 写明 | doc | Cx1 | 🟡 | — | `.claude/rules/gocell/api-versioning.md` | PR#247 round-2 N-01 |
| HTTPUTIL-WRITEERRORBODY-DOUBLE-MARSHAL | **错误响应双重 JSON marshal** — 现状: writeErrorBody marshal+unmarshal+encode 三次；修复: errcode.MarshalJSON 原生支持 envelope 注入 | bug | P3/Cx1 | 🟡 | HTTP 错误成 hot path | `pkg/httputil/response.go` + `pkg/errcode/errcode.go` | PR #391 review round-2 |
| PR391-HEALTH-VERBOSE-REDACTION-01 | **Readyz verbose redaction** — 现状: verbose 503 dependency error 仅 truncate，可能含 secret；修复: 走 `pkg/redaction` + 4 通道分明 | arch-opt | P1/Cx2 | 🟠 | 发布前安全收口 | `runtime/http/health/` + ADR | PR#391 review security |
| PR392-FU-RATE-LIMITER-DISTRIBUTED | **BOOTSTRAP-RATELIMIT-DISTRIBUTED-01** — 现状: in-memory token bucket per pod；修复: 出现暴力枚举威胁时引入 Redis-backed | arch-opt | P3/Cx3 | 🟡 | bootstrap mode + 多 pod | `adapters/ratelimit/` + `cmd/corebundle/access_module.go` | PR #392 ADR §D10 |
| PR237-PM5 | **DUAL-LISTENER-DEPLOYMENT-GUIDE-01** — ✅ closed by PR #222 (fix/222-pr237-dual-listener-doc-finalize)：扩 `docs/ops/listener-topology.md` 追加 6 章节（Deployment Boundaries Hard 不变式 / Deployment Recommendations 部署建议 / Docker Compose / NetworkPolicy / Migration from Single-Listener / Troubleshooting）；与 PR237-A4 合并收口；同 PR 迁 `docs/operations/first-run-setup.md` → `docs/ops/` 并删空目录。**未在本 PR 立项的 Soft 部署建议**（LB 映射 / internal 绑回环）已新登记 `BOOTSTRAP-INTERNAL-LOCAL-ONLY-FAIL-FAST-01`（Medium 升级路径，对称 health LOCAL_ONLY 机制）。 | doc | Cx2 | ✅ | — | `docs/ops/listener-topology.md` | PR #237 round-2 PM-05 |
| BOOTSTRAP-INTERNAL-LOCAL-ONLY-FAIL-FAST-01 | **internal listener LOCAL_ONLY fail-fast** — 现状: `cmd/corebundle/access_module.go:373` 注释明说 `GOCELL_HTTP_INTERNAL_ADDR=0.0.0.0:9090` 是 misconfiguration 但 corebundle 不拦截；只有 health listener 有 `GOCELL_HTTP_HEALTH_LOCAL_ONLY` 对称机制（`cmd/corebundle/shared_deps.go:246`）。internal 当前靠 ServiceToken Hard 兜底（archtest `SEC-FAIL-CLOSED-06`），但部署建议层无代码侧拦截，从 docs/ops/listener-topology.md §Deployment Recommendations Soft → Medium 路径缺位。修复: 给 internal listener 加 `GOCELL_HTTP_INTERNAL_LOCAL_ONLY` envvar + real adapter mode 检测 internal addr 非回环时 fail-fast（除显式 opt-in），对称 health 机制；加 archtest 守该机制 + 单测覆盖 dev/real × 回环/非回环 × opt-in 四象限。**AI HARD 评级**：Medium（runtime guard at corebundle）。 | arch-opt | P2/Cx2 | 🟡 | — | `cmd/corebundle/shared_deps.go` + `cmd/corebundle/shared_deps_validate.go` + `tools/archtest/security_defaults_test.go` + tests | PR #237 round-3 / PR #222 (PR237-PM5 Soft 升级路径) |
| PR237-PM7 | **EXAMPLE-INTERNAL-LISTENER-COMMENT-01** — 现状: examples/*/main.go 双 addr 缺注释；修复: 加注释或 `WithHTTPInternalDisable` | doc | Cx1 | 🟡 | — | `examples/*/main.go` | PR #237 round-2 PM-07 |
| LISTENER-API-SPEC-01 | **Listener API spec 化** — 现状: listener 选项散在代码；修复: contracts 化声明 | arch-opt | Cx2 | 🟡 | — | `contracts/http/` | PR#237 |
| ROUTE-ERROR-POLICY-01 | **Route error policy 统一** — 现状: 3+ route family 错误处理不一；修复: 定义共享 policy | arch-opt | Cx3-Cx4 | 🟠 | 3+ route 家族出现 | `runtime/http/` | systems review |
| T4 | **CB-RESILIENCE-PACKAGE-01** — 现状: Allower / CircuitBreakerRetryAfter 在 `runtime/http/middleware`；修复: 迁到 `runtime/resilience/circuitbreaker/` 独立包 (also: cap-x-cross) | refactor | — | 🟠 | 出现第 2 个非 HTTP CB 消费方 | `runtime/http/middleware/` + `runtime/resilience/circuitbreaker/` (新) | T4 |
| WM-32 | **mTLS 中间件** — 现状: 缺；修复: 加 TLS 构建器 + HTTP 证书提取钩子（折中：大规模环境 mTLS 卸载在 K8s/Service Mesh 解决，框架仅提供构建器） | feat | P2/Cx2 | 🟡 | V1.1 启动 | `runtime/http/middleware/` | backlog_later §7 WM-32（4/6 票）|
| B2-T-08 | **Config publish 失败码声明不完整** — 现状: contract 缺部分失败码声明；修复: 补 4xx/5xx 完整声明 | bug | P2/Cx1 | 🟡 | — | `contracts/http/config/publish/v1/contract.yaml` | backlog2 §8 B2-T-08 |
| J-04 | **CONTRACT-SCHEMA-NAMING-NORMALIZE** — (a) api-versioning.md 写 `pageSize`，contract 实际用 `limit`（规则与代码漂移）；(b) event headers `event_id`(snake_case) 与 cell-patterns.md "camelCase" 冲突；修复: 改规则文档 + 与 J-03 v1→v2 演练搭车统一 envelope | bug | P1/Cx1 | 🟡 | 与 J-03 同 PR | `.claude/rules/gocell/` + `contracts/` | 030 §3 J-04 |

---

## cap-05: 身份认证 (Authn)

> 主要包：`runtime/auth` + `auth/refresh` + `auth/refresh/memstore` + `auth/config`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| B5-FU-PG-RUNTIME-WIRING-AND-ARCHTEST-TYPE-AWARE-01 | **B5 follow-up PG runtime wiring + archtest 类型化** — ✅ closed by PR #482 (S4a, 2026-05-13) + PR #587-t3 (S4c T3, 2026-05-16)：(a) corebundle postgres 分支删 `WithInMemoryDefaults`，accesscore 显式注入 `session.MustNewProtocol(...)` + PG session/refresh store + type-aware archtest `SESSIONREFRESH-NO-SESSION-CREATE-01`（PR #482）；(b) 残余 `session_protocol_composition_root_test.go` / `refresh_invariants_test.go` Soft → Medium 落地（PR #587-t3）：两 archtest 切到 `archtest.RunTyped` + `ResolvePackageRef`/`ResolveMethodCall`，archtest_fixture-gated RED 夹具覆盖 qualified/aliased/dot import × `sessionStore.Get` 新 lookup 链；不进 `archtestmeta.LegacyAllowlist` | refactor+test | P1+P2/Cx2+Cx3 | ✅ PR #482 + #587-t3 | — | `cmd/corebundle/access_module.go` + `cells/accesscore/cell_init.go` + `tools/archtest/{sessionrefresh_no_session_create,session_protocol_composition_root,refresh_invariants}*_test.go` + `tools/archtest/internal/{session,refresh}*fixture/` | PR#399 review L2 → PR #482 (S4a) → PR #587-t3 (S4c T3) |
| ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 | **ACCOUNT-LOCKOUT-AUTO-LOCK-01** — 现状: sessionlogin 无失败次数累计 + 阈值 + auto-lock；修复: 完整业务设计 + PG schema + journey harness | feat | Cx3 | 🔴 | — | `cells/accesscore/slices/sessionlogin/` + user repo + integration test | PR-A63 复核 |
| CELLS-IDENTITYMANAGE-LEVEL-MISLABEL-01 | **identitymanage 一致性等级误标** — ✅ closed by T2 (slice consistency codegen funnel)：identitymanage slice.yaml 显式声明 `consistencyLevel: L2` (实际 publish 5 个 `event.user.*` 事件)；slice.yaml 是单一 SoR，codegen 投影到 slice_gen.go.sliceMeta；cell_init.go 通过 `cell.MustNewBaseSliceFromMeta(identitymanage.SliceMetadata())` 消费。drift 不可重新出现 (BASESLICE-CTOR-FUNNEL-01 archtest)。 | arch-opt | Cx1 | ✅ T2 | — | `cells/accesscore/slices/identitymanage/slice.yaml` + `kernel/cell/base.go` + `tools/codegen/cellgen/` | systems layer review |
| IDENTITYMANAGE-LOCKUNLOCK-CO-TX-UPGRADE-01 | ✅ **closed by Wave 5 (2026-05-17, PR #525 followup)**：authzmutator.Apply 删除，单入口 `ApplyInTx(ctx, txCtx, userID, m, now)` — caller 提供 outer RunInTx txCtx，funnel 不再持有 tx 边界。Lock/Unlock/applyUserUpdate 三处 caller 全部迁移：apply+publish 合并到同一 RunInTx 闭包，L2 OutboxFact 真正生效。Mutator.txMgr 字段删除。详见 ADR §A12。 | arch-opt | P3/Cx3 | ✅ Wave 5 | — | `cells/accesscore/slices/identitymanage/service.go` + `cells/accesscore/internal/authzmutate/` | PR #525 deep review P1-1 |
| OIDC-FAIL-FAST-DISCOVERY-01 | **OIDC discovery fail-fast** — ✅ closed by PR #485 (PR-8, 2026-05-13)：`oidc.New(ctx, cfg)` 改为同步 discover (`force=true`)，unreachable issuer 在构造期 fail-fast；Adapter 实现 `lifecycle.ManagedResource` + `oidc_ready` Checker 验证 cached provider；boot 时 fail-closed 替代旧 lazy at first request | bug | Cx2 | ✅ PR #485 | — | `adapters/oidc/oidc.go` + `adapters/oidc/discovery_test.go` | systems layer review → PR #485 (PR-8) |
| OIDC-JWKS-ROTATION-WORKER-01 | **OIDC JWKS 轮转 worker** — ✅ closed by PR-11/A-02：根因更正——backlog 原「`cache_max_age` 头（fallback 24h）」基于 pre-v3 go-oidc 心智模型；go-oidc v3.18 `RemoteKeySet` kid-miss 即反应式重拉 jwks_uri（删了 v1/v2 时间缓存），**JWKS 键轮转已由 go-oidc 自处理**，真正永不刷新的是 discovery 元数据（jwks_uri/endpoints/alg，`Provider` 永久缓存）。落地: `adapters/oidc/` 内 `oidcRefreshWorker` 经 `ManagedResource.Worker()` 周期全量 provider 重发现（`Config.RefreshInterval`，默认 24h）兜底元数据；fail-open 不覆盖旧 provider（结构性）；`oidc_jwks_refresh_total{cell,result}` CounterVec（镜像 rabbitmq collector 范式）+ slog Warn/Info；首启 fail-closed 由 A-01 同步 discover 保证，post-init 不 gate readyz（对标 K8s kube-apiserver OIDC / Envoy jwt_authn）。前置 A-01 ✅ PR #485 | feat | P1/Cx2 | ✅ PR-11 | — | `adapters/oidc/` | systems layer review + 030 §2 A-02 → A-01 unblocked PR #485 → PR-11 |
| OIDC-REFRESH-LOCK-FREE-01 | **OIDC refresh 阻塞读路径** — ✅ closed by PR #504 (review fix, 2026-05-16)：discover() 持 a.mu 写锁跨 gooidc.NewProvider 网络 round-trip（≤HTTPTimeout 10s），周期 refresh 期间 Provider/Verifier/OAuth2Config/readyz 全阻塞，违反 PR 自述 post-init fail-open availability。修复: provider 改 `atomic.Pointer`，删 RWMutex，网络锁外执行成功后 atomic.Store，删死代码 force 参数；invariant (a) 假 "structural Hard" 措辞诚实降级为 test-guarded（Medium）；新增 TestProvider_ConcurrentReadDuringSlowRefresh 永久并发 guard。附带修 pre-existing -race（worker 测试共享 bytes.Buffer，新增单源 `sloghelper.SyncBuffer`） | bug | Cx2 | ✅ PR #504 | — | `adapters/oidc/oidc.go` + `adapters/oidc/refresh_worker.go` + `pkg/testutil/sloghelper/sloghelper.go` | PR #504 review (OIDC discover lock-granularity) |
| PR-A8-RESIDUAL | **Vault K8s auth E2E** — 现状: Vault K8s auth 实现已落，缺真 K8s e2e；修复: 跑 testcontainers k8s 验证 | arch-opt | Cx2 | 🟡 | — | `adapters/vault/` | PR#305 |
| PR338-FU-LOGIN-DURABLE-TX-ATOMICITY-TEST | **登录 durable TX atomicity 集成测试** — ✅ closed by PR #482 (S4a, 2026-05-13)：`cmd/corebundle/setup_pg_integration_test.go::TestSessionLogin_OutboxFailureRollsBackPGRows` testcontainer 注入 `oneshotFailOutboxWriter` 在 `event.session.created.v1` emit 处失败，断言 PG tx rollback 完整回滚 session/refresh 行 + spy 记录失败 entry 主题 + HTTP 5xx envelope。`TestSessionRefresh_TwoHops_PG` 同 PR 覆盖 stable-sid 多跳 refresh 链 | test | Cx2 | ✅ PR #482 | — | `cmd/corebundle/setup_pg_integration_test.go` + `cells/accesscore/slices/sessionlogin/outbox_test.go` | PR#338 → PR #482 (S4a) |
| PR338-FU-AUTH-FAIL-CLOSED-DOC-CLEANUP | **AUTH-FAIL-CLOSED-DOC-CLEANUP-01** — 现状: nonce.go docstring + archive quickstart 未跟 PR-CFG-I 更新；修复: 补 deprecation banner | doc | P3/Cx1 | 🟡 | — | `runtime/auth/nonce.go` + `docs/archive/specs/201-wm2-key-rotation/quickstart.md` | PR#338 round-1 |
| PR267-FU-AUTHTEST-INTERNAL | **Auth test 内部化** — 现状: testHelpers 暴露过多；修复: internal package | arch-opt | Cx1 | 🟡 | — | `cells/accesscore/` | PR#267 |
| PR267-FU-ROLE-PREFIX-ADR | **Role prefix ADR** — 现状: role 命名前缀约定无 ADR；修复: 写 ADR | doc | Cx1 | 🟡 | — | `docs/architecture/` | PR#267 |
| X3 | **WM-36 SecureCookie key rotation** — 现状: 无密钥轮转；修复: 接入 rotation worker | feat | P3/— | 🟡 | — | `runtime/auth/` | WM-35 后续 |
| X5 | **P3-TD-11 accesscore domain 拆分** — 现状: domain 包过大；修复: User/Session/Role 拆分 | refactor | P3/— | 🟡 | X1 落地后 | `cells/accesscore/internal/domain/` | 历史 Batch 8 |
| X13 | **REFRESH-PARTITION-01** — 现状: 批量 DELETE GC；修复: `expires_at` range 分区 + DROP PARTITION (also: cap-10) | feat | P3/Cx2 | 🟠 | 生产流量达阈值 | migration + ops runbook | 通用 PG 模式 |
| T5 | **AUTH-SIGNER-01** — 现状: SigningKeyProvider 返回 `*rsa.PrivateKey`；修复: 改 `crypto.Signer` 支持 HSM/KMS/EC | arch-opt | — | 🟡 | caller 需 HSM/KMS | `runtime/auth/` | T5 |
| C-AC7 | **JWT jti claim 支持** — 现状: 缺 jti，单 token 无法黑名单撤销；修复: Issue() 加 jti + jti 黑名单存储 | feat | P2/Cx2 | 🟡 | 出现单 token 撤销需求 | `runtime/auth/` | backlog_later §6 C-AC7 |
| P3-TD-10 | **TOCTOU 竞态修复** — 现状: ✅ S4d (plan 034) 完关。S4b 用 JWT `authz_epoch` claim + live `users.authz_epoch` 比对，但漏了 (i) refresh chain 升级 (ii) login 与 invalidator 并发 (iii) RequirePasswordReset funnel 上游漏调（PR #490 review P1-#1/#2/#3）。**S4d 修复方向重构为 row-level provenance**：session/refresh 行携带 `authz_epoch_at_issue`，sessionvalidate 比对 row（非 claim），sessionlogin `SELECT ... FOR UPDATE` 行锁串行化 login vs Invalidator.Apply。access JWT 删 `authz_epoch` claim（claim 不是 provenance，是 validation cookie）。详见 ADR-credential §A1 RETRACTED / §A8-§A11（§A7 access-JWT epoch claim 段在 FU-1 同 PR 删除：claim 早已不写入，row SoR 是唯一真值）。 | bug | P2/Cx3 | ✅ | S4d ship | `cells/accesscore/` | tech-debt-registry P3-TD-10 + PR #482 F2 + PR #490 review |
| AUTHZ-MUTATION-FUNNEL-UPGRADE-01 | ✅ **LANDED PR #494 (2026-05-15)**：funnel 上游 Hard 化。AS-BUILT: (a) `domain.User` status/passwordResetRequired/authzEpoch 字段私有化 + 唯二 setter（SetStatus/SetPasswordResetRequired）收口到 authzmutate；(b) sealed `Mutation` interface + `Mutator.Apply` 唯一入口 + 5 个 variant（LockUser/SuspendUser/ActivateUser/RequirePasswordReset/ClearPasswordReset）；(c) archtest `DOMAIN-AUTHZ-FIELD-PRIVATE-01` + `AUTHZ-MUTATION-APPLY-FUNNEL-01` Hard 守卫。**关键偏差**：`CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01` allowlist 未能收窄到 {authzmutate, sessionrefresh}（co-tx atomicity 约束；实际 allowlist = credentialinvalidate/ + authzmutate/ + identitymanage/ + sessionrefresh/ + rbacassign/）；write-side Hard 保证来自 Rule (a) 字段私有化，不是 Rule (b) caller-set 收窄。详见 ADR-credential §A10。**#494 residual fix**：删除从未被实例化的 `RoleRevoked` 死 variant（伪统一残骸，rbacassign.Revoke 经聚合语义正确地直调 `invalidator.Apply`）；§A10 重写为 Medium 天花板显式锁定（co-tx atomicity + ent/kratos 对标实证 Go 类型天花板 = 下游 Hard + 上游 Medium-by-necessity）；funnel allowlist 由 package 前缀收窄到精确文件。 | arch-opt | P2/Cx2 | ✅ PR #494 + residual | — | `cells/accesscore/internal/{domain,authzmutate,credentialinvalidate,slices/identitymanage,slices/rbacassign}/` + `tools/archtest/` | ADR-credential §A10; PR S4d review checklist; #494 residual review |
| AUTHZ-MUTATION-FUNNEL-UPSTREAM-HARD-01 | **funnel 上游 caller-set 由 Medium 升 Hard** — 现状: `AUTHZ-MUTATION-APPLY-FUNNEL-01` 下游 Hard（domain 字段私有化 compile-time 不可绕过）已闭合，但上游 caller-set 是文件级 archtest allowlist（Medium-by-necessity）：creation-time 调用无法用 sealed interface / codegen 在 compile 期表达"仅限构造时"语义，且 identitymanage.Delete / changePasswordInTx / rbacassign.Revoke 因 co-tx aggregate 语义必须直调 `invalidator.Apply`（经 authzmutate 是聚合语义错误，非疏漏）。#494 residual 对标 ent(`tx.Client()`=capability=Medium)/go-kratos(context-tx=Low) 实证：Go 在 tx-bound side-effect funnel 的类型天花板就是"下游 Hard + 上游 Medium"，任何 TxHandle/marker 是伪 Hard。修复方向（若未来出现可达 Hard 路径）: codegen 注入构造点 + sealed tx-scoped handle 让"非构造期 / funnel 外调用"包外不可表达。**charter §Funnel双向锁 mandated 显式登记项**，D1 archtest godoc 已点名本条。 | arch-opt | P3/Cx3 | 🟠 | 出现 codegen-injected 构造点可表达"仅构造期"语义时立项 | `cells/accesscore/internal/{authzmutate,credentialinvalidate}/` + `tools/archtest/domain_authz_mutation_funnel_invariants_test.go` | ADR-credential §A10; ai-collab.md §Funnel双向锁评级; #494 residual review |
| CREDENTIAL-AUTHORITY-READSIDE-FUNNEL-01 | **read-side credential-authority Hard funnel**（S-next）— 现状: token issue（sessionlogin/sessionrefresh）和 token validate（sessionvalidate）各自散落检查 `CanAuthenticate()` + epoch，无单一 Hard 收口；sessionrefresh 漏检 `CanAuthenticate()`（P1.1/P1.3 class）。修复: 新建 `credentialauthority.Assert(ctx, user, opts...)` sealed function，所有 issue + validate 路径统一经过，archtest `CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01` Hard 锁 caller allowlist，与 authzmutate.Mutator.Apply 对称（write-side / read-side 双向闭合）。设计详见 ADR-credential §A11。 | arch-opt | P2/Cx2 | 🟡 | S4e ship（PR #494）后立即立项 | `cells/accesscore/internal/credentialauthority/` + `cells/accesscore/slices/{sessionlogin,sessionrefresh,sessionvalidate}/` + `tools/archtest/` | ADR-credential §A11; PR #494 review |
| SESSIONREFRESH-STALE-EPOCH-REJECT-HARDEN-01 | **SESSIONREFRESH-STALE-EPOCH-REJECT archtest Hard 升级** — 现状: `SESSIONREFRESH-STALE-EPOCH-REJECT-01` 所有 prong 使用 go/parser AST 名字/字符串锚点（Sel.Name / Ident.Name / BasicLit）而非 typeseval 类型身份解析，AI-rebust 评级为 Medium（PR #494 review F1）；修复: 将 `cascadeRevoke` 和 `handleReuseDetected` callee 识别从 Sel.Name 字符串锚点替换为 `typeseval.ResolveMethodCall` 类型身份解析，同时对 rejectIfStaleEpoch 调用点的 control-flow 形态做类型级锁定，升为 Hard | arch-opt | P2/Cx2 | 🟠 | 下一次 archtest hardening pass | `tools/archtest/sessionrefresh_stale_epoch_reject_test.go` | PR #494 review F1 |
| P4-TD-03 | **IssueTestToken HS256 dead code** — 现状: 测试 helper 仍保留 HS256 路径，JWTVerifier 全拒；修复: 删 dead code 防误用 | refactor | Cx1 | 🟡 | — | `runtime/auth/` (test helper) | tech-debt-registry P4-TD-03 |
| SECURECOOKIE-AEAD-NEG-01 | **SecureCookie AEAD 负向测试** — 现状: AEAD 失败路径无测试；修复: 截断/伪造/边界长度/解密失败类型断言 (`errors.Is(err, ErrAEADAuthFailed)`) | test | Cx2 | 🟡 | v1.0 GA 前 | `pkg/securecookie/securecookie_test.go` | backlog1 §2.5 |
| B2-C-02 | **SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT** — 现状: setup 端点常驻 Public，未初始化窗口可被匿名首管抢注；产品决议: 留 PrimaryListener + `auth.Route{Bootstrap: true}` HTTP Basic Auth + count(admin)>=1 时 409，bootstrap-only lifecycle（详见 `docs/architecture/202605101400-adr-admin-invariant.md` §3.3，替代"移 internal" 提议）；代码落地待 S3+S5 PR | feat | P0/Cx3 | 🔴 | — | `cells/accesscore/cell_routes.go:73` + `slices/setup/handler.go:46-58` + `contracts/http/auth/setup/admin/v1/contract.yaml:5` | backlog2 §1 B2-C-02; ADR-Admin §3.3 (PR#439) |
| S1-CO-02-WIRING-OPTION-STICKY-DOCTRINE | **runtime-api.md sentinel sticky 通用契约明示** — 现状: 多处 wiring option（router.WithRateLimiter/WithCircuitBreaker/WithAuthMiddleware + session.WithFingerprint/WithOrdering）已实现 sentinel 粘滞失败行为，但 `.claude/rules/gocell/runtime-api.md §Option 范式分层` 未明示此为通用契约；session 包内已加 sticky test 锁定 Medium AI-rebust；修复: 章程层面明示 + 可选 archtest 跨 option 检测注释一致性 | doc+test | Cx2 | 🟡 | 下一次 wiring option 章程级修订 | `.claude/rules/gocell/runtime-api.md` + `runtime/auth/session/protocol.go` + `runtime/http/router/router.go` | PR#439 reviewer P1 follow-up |
| AUTH-BOOTSTRAP-CLIENTS-MUTEX-01 | **BootstrapAuth × Clients 互斥闸门** ✅ PR#483 — runtime fail-fast 落 `validateBypassCompatibility`（HTTP Basic Auth via env credentials 与 service-token 4-part `ts:nonce:callerCell:mac` caller-cell allowlist 互斥）+ archtest 型 type-aware Hard 全覆盖 4 个 Contract-expression 形态（file-scope var / inline literal / func-body-local `:=` / cross-package SelectorExpr，0 KNOWN-GAP）+ 组合矩阵测试。PR#483 review type-aware 升级见 `tools/archtest/auth_bootstrap_invariants_test.go` + `tools/archtest/internal/authroutemutexfixture/` fixture 包 | arch-opt | P1/Cx2 | ✅ | — | `runtime/auth/route.go:validateBypassCompatibility` + `runtime/auth/route_test.go` + `tools/archtest/auth_bootstrap_invariants_test.go` | PR#451 外部 review feedback (2026-05-11) → PR#483 (2026-05-13) |

---

## cap-06: 授权决策 (Authz)

> 主要包：`runtime/auth` (authz/policy)

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| T3 | **DEVICE-ENQUEUE-RBAC** — 现状: HandleEnqueue 无设备维度鉴权；修复: 加设备粒度策略 | feat | — | 🟠 | 多租户 operator | `cells/devicecell/` | T3 |
| T11 | **ADMIN-ROLE-DEDUP** — 现状: admin role 字符串散在多处；修复: 抽 const 单源 | arch-opt | — | 🟠 | role 命名漂移出现 | `pkg/auth/` + `cells/` | T11 |
| B2-C-06 | **SessionLogout consumer action 无验证** — 现状: consumer.go 接受任意 action 字段；修复: 加 action enum 校验 | bug | P1/Cx2 | 🟡 | — | `cells/accesscore/slices/sessionlogout/consumer.go:69` | backlog2 §4 B2-C-06 |
| B2-T-02 | **RBACASSIGN event contract waiver expiry** — ✅ closed by S4c-T1 PR：`TestContract_EventRole{Assigned,Revoked}V1_Publish_PayloadValid` 重写为真实 `svc.Assign/Revoke` 经 `RecordingWriter` 捕获 payload + `c.ValidatePayload`/`c.ValidateHeaders`/`c.MustRejectPayload`/`c.MustRejectHeaders` 完整契约校验；waiver 注释删除 | bug | P1/Cx2 | ✅ S4c-T1 | — | `cells/accesscore/slices/rbacassign/contract_test.go` | backlog2 §8 B2-T-02 |
| B2-T-05 | **Internal contract external actor drift** — 现状: contract 声明 external actor 但实际是 internal；修复: 校正 boundary.yaml | arch-opt | P1/Cx2 | 🟡 | — | `contracts/http/auth/role/{assign,revoke}/v1/contract.yaml` + `boundary.yaml` | backlog2 §8 B2-T-05 |
| B2-T-07-FU-1 | **RBACASSIGN accesscore caller wiring** — ✅ closed by S4c-T1 PR：`handler.go` 显式 `RequireCallerCell("accesscore")` policy + 自动 caller-cell guard 双重防线已就位；`handler_test.go::TestHandler_{Assign,Revoke}` 已覆盖 accesscore 200/201 / configcore 403 / empty caller 403 / no-auth 401 四象限 | arch-opt | Cx2 | ✅ S4c-T1 | — | `cells/accesscore/slices/rbacassign/{handler,handler_test}.go` | backlog2 §8 A5 follow-up |
| B2-T-07-FU-2 | **BUILTIN-SERVICE-ROLES 删除 FU** — 现状: scope 派生 builtin role 还在 hard-code；修复: 完全派生（A5 follow-up） | arch-opt | Cx3 | 🟠 | scope 派生工具就绪 | `runtime/auth/principal.go` | backlog2 §8 A5 follow-up |

---

## cap-07: 事务性事件发布 (Outbox Producer)

> 主要包：`kernel/outbox` + `runtime/outbox` + `adapters/postgres` (outbox table)

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| PR341-FU-OUTBOXTEST-CLOSE-BUDGET-COVERAGE | **OUTBOXTEST-CLOSE-BUDGET-COVERAGE-01** — 现状: conformance suite 仍裸调 `sub.Close(ctx)`；修复: 全部走 closeWithBudget 或 godoc 强约定 | test | P2/Cx1 | 🟡 | — | `kernel/outbox/outboxtest/conformance.go` | PR #341 round-1 |
| AUDITAPPEND-L2-FAILURE-PROOF-01 | **AuditAppend L2 失败注入测试** — ✅ closed by PR #450 (S7, 2026-05-11)：`TestAuditLedgerStore_OutboxAtomicityFailureProof` testcontainer 故意 fail outbox writer 验证 DB 写成功 + outbox 失败 → tx rollback | test | P1/Cx3 | ✅ PR #450 | — | `adapters/postgres/audit_ledger_store_test.go` | backlog1 §2.5 → PR #450 (S7) |
| RBACASSIGN-L2-PG-ATOMICITY-01 | **RbacAssign L2 PG 原子性测试** — 现状: rbacassign 仅有 mem-based L2 路径测试（RecordingWriter），无 PG-level outbox 原子性验证；修复: testcontainer 驱动 PG outbox writer，故意 fail writer → 验证 domain 写成功 + outbox 失败 → tx rollback（与 AUDITAPPEND-L2-FAILURE-PROOF-01 同模式）。当前 mem adapter 不支持 RunInTx 故障注入，等 PG accesscore 仓储落地后补充。 | test | P1/Cx2 | 🟠 | X1 PG-DOMAIN-REPO 上线后 | `adapters/postgres/` (新) + `cells/accesscore/slices/rbacassign/` | PR #514 reviewer F9 |
| RBACASSIGN-L2-STATIC-HARD-UPGRADE-01 | **RbacAssign L2 静态检查 Hard 升级** — ✅ closed by T2 (slice consistency codegen funnel)：`cell.NewBaseSlice` 字面量被完全删除；rbacassign 的 L2 invariant 由两层防线接管：(a) codegen funnel `slice.yaml.consistencyLevel: L2 → slice_gen.go.sliceMeta` (Hard); (b) governance SLICE-CONSISTENCY-02 (publish role → ≥L2, Medium)。RBACASSIGN-L2-STATIC-01 archtest 同 PR 删除——premise (cell_init.go 字面量) 消失。 | arch-opt | P3/Cx2 | ✅ T2 | — | `kernel/cell/base.go` + `kernel/governance/rules_misc_advisory.go` + `tools/codegen/cellgen/` | PR #514 reviewer F2 → T2 |

---

## cap-08: 异步事件消费 (Subscriber+Claimer)

> 主要包：`kernel/{outbox,idempotency}` + `runtime/eventrouter` + `adapters/{redis,rabbitmq}`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| RELAY-RETRYDELAY-TABLE-TEST-01 | **Relay retry delay 表驱动测试** — 现状: retry delay 路径覆盖单一；修复: 加 table-driven test | test | Cx2 | 🟡 | — | `adapters/rabbitmq/` | — |
| K07-SUBSCRIPTION-REGISTRY-WRAPPER-BAN-01 | **K07 follow-up — `Registry.Subscribe` 不可被同形包装函数绕过** — 现状: REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01 archtest 仅 pin `kernel/cell/registry.go` 上的接口形态，未禁止业务方在 non-test/non-codegen 文件里新增 `func XxxSubscribe(reg Registry, spec, handler, cg, cellID string) error` 之类的兼容包装函数（包装内部可以将 cellID 改为空字符串 fallback）；修复: 增加 `REGISTRY-SUBSCRIBE-NO-WRAPPER-01` archtest，通过 typeseval 在非 codegen / 非 archtest fixture 路径下检测"参数链与 `Registry.Subscribe` 兼容"的函数定义（且第 4 参可能为 `string` 或 option），并拒绝；AI-rebust 维持 Hard（type system 探测）。**触发条件**：评估增量值，确认是否值得引入；当前 PR 已将主路径锁定，wrapper 绕过属于剩余 Medium 风险面 | arch-hard | P2/Cx2 | 🟡 | 当 cells/ 中出现"为 cellID 引入默认值"的 wrapper 提案时 / archtest 周期复盘 | `tools/archtest/` (新增) + 可能 `tools/archtest/internal/typeseval/` helper | PR #462 review F[P1/Cx2] |
| K07-SUBSCRIPTION-ARCHTEST-RED-FIXTURE-01 | **K07 follow-up — subscription_invariants_test.go 缺 RED fixture** — 现状: `tools/archtest/subscription_invariants_test.go` 三条 archtest 直接断言真实源代码状态，无独立 negative fixture（与 `eachnode_test.go` T1+T2 RED 范本对比）；archtest 自身有 bug（如 allowlist 键拼错）时会 silent pass；修复: 至少为 SUBSCRIPTION-FIELDS-FROZEN-01 加 in-test 合成验证（构造含额外字段的 AST string → 走相同判断逻辑 → 断言 unknown 非空），或新增 `testdata/subscription_negative_fixtures/` 目录走 packages.Load 的常规 fixture pattern；AI-rebust：Medium 留存档的合理补强，不阻塞合并 | test | P3/Cx2 | 🟡 | archtest 周期复盘 / 任一 subscription_invariants_test.go 自身被发现 bug 时 | `tools/archtest/subscription_invariants_test.go` + `tools/archtest/testdata/` | PR #462 review F[P1/Cx1] |
| CELL-CONSUMER-EXTRA-TOPICS-OPTION-01 | **Cell consumer extra topics option** — 现状: cell 无法订阅同 cell 外的 extra topics；修复: 加 Option | feat | Cx3 | 🟡 | — | `kernel/cell/` | GitHub #303 |
| KERNEL-REPLAY-01 | **kernel/replay 投影重算** — 现状: 缺 CQRS Projection rebuild；修复: 新建 replay 包 + 依赖 Consumer 模型稳定后实现 | feat | P3/Cx3 | 🟡 | Consumer 模型稳定 + 业务出现 CQRS rebuild 需求 | `kernel/replay/` (新) | backlog_later §2 |
| KERNEL-RECONCILE-01 | **kernel/reconcile L3 收敛循环** — 现状: 缺 Reconciler 模式；修复: 新建 reconcile 包 | feat | P2/Cx3 | 🟡 | L3 业务出现 | `kernel/reconcile/` (新) | backlog_later §2 |
| WM-18 | **延迟消息原语** — 现状: 缺 TTL；修复: RMQ x-delayed-message 插件绑定 + 测试桩支持（运维成本拉升，等 Outbox 稳定后探索） | feat | P2/Cx2 | 🟡 | V1.1 启动 + Outbox 彻底稳定 | `adapters/rabbitmq/` + outbox | backlog_later §7 WM-18（3/6 票）|
| B2-C-10 | **Auditappend 全局 mutex 串行化 13 topic** — ✅ closed by PR #450 (S7, 2026-05-11)：方案变更——原 backlog 提议「按 topic 分片细化锁」未采纳，改为 `runtime/audit/ledger.Store` PG advisory lock per namespace 串行化（hash-chain 顺序保证）；测试 `TestAuditLedgerStore_AdvisoryLockSerializesAppend` 100 goroutine 并发 Append；按 topic 分片不可行（hash-chain 必须 namespace 内严格串行） | bug | P1/Cx3 | ✅ PR #450 | — | `runtime/audit/ledger/store_pg.go` + `cells/auditcore/slices/auditappend/service.go` | backlog2 §4 B2-C-10 → PR #450 (S7) |
| R-02 | **EVENTBUS-DROP-CONTEXTUAL-LOG** — InMemoryEventBus.broadcast/roundRobin drop 路径 slog.Warn 缺 entry_id/aggregate_id/event_type；修复: 升 Error 级 + 三字段（与 R-01 counter 对应）| bug | P2/Cx1 | 🟡 | — | `runtime/eventbus/eventbus.go` | 030 §2 R-02 |
| G-08 | **OUTBOX-FAILOPEN-COUNTER + INMEM-RECEIPT-FIX** — (a) fail-open `RecordDrop()` 无 metrics；(b) `inMemReceipt.Commit/Release` 共享 `sync.Once`，Release 先于 Commit 静默 false-success；(c) `UnmarshalEnvelope` `msg.ID` 仅非空检查，可日志注入（CWE-117）；修复: increment `outbox_failopen_drops_total{cell}` + `committed atomic.Bool` 区分 + 复用 `idutil.IsSafeID` | bug | P1/Cx2 | 🟡 | — | `kernel/outbox/` + `runtime/outbox/` + `pkg/idutil/` | 030 §3 G-08 |
| OUTBOX-HANDLERESULT-SLIM-01 | **HandleResult 字段精简** — 现状: ProcessReason/SettlementObservers 暴露在 handler 返回类型上，导致 ~15 处字面量无法用 factory 表达；修复方向: 把这两字段挪到 ConsumerBase internal state，handler 接口收敛为 Disposition+Err，达成 100% factory 覆盖。触发条件: (1) 新出现 ≥ 3 处需要 ProcessReason/SettlementObservers 字面量的业务 handler 调用点 / (2) HandleResult 需要加第 5 字段 / (3) 字面量回灌产生 ≥ 2 次 review finding。(also: cap-13) | refactor | P2/Cx2 | 🟡 | — | `kernel/outbox/outbox.go`, `kernel/outbox/consumer_base.go` | W9 plan §D2 |

---

## cap-09: 配置加载与热更新

> 主要包：`runtime/config` + watcher + `cells/configcore`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| PR-CFG-A-DEFER-2 | **ConfigCore L2 divergence** — 现状: L2 与 L1 表项 schema 偏差；修复: 收口 | arch-opt | Cx1 | 🟡 | — | `cells/configcore/` | PR#268 |
| CONFIGCORE-CACHE-LIFECYCLE-OWNER-01 | **ConfigCore 缓存生命周期归属** — **won't-do（2026-05-16）**: Cache 确认 service-private（仅 `configsubscribe/service.go` + 其测试引用，无跨 slice 共享），满足本条原始决策准则；未引入 archtest 抽象。ref: PR 207-cfg-cache-lifecycle / plan 035 §2 + §5 | arch-opt | — | ✅ | — | `cells/configcore/slices/configsubscribe/` | systems layer review + PR 207-cfg-cache-lifecycle |
| CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 | **ConfigReceive 业务 reload 接入或清理** — 现状: `accesscore/configreceive` 注释自承「Real consumers (JWT TTL refresh, key rotation interval) will land in a follow-up」——为业务 reload 已搭好的接入骨架（订阅 + ConfigGetter HTTP refetch + 测试守卫，~10h 工作量）；2026-05-10 激进自审撤回「直接删除」主方案（参 `docs/plans/202605101548-035-configcore-residuals-fix-plan.md` §6）。修复: 业务侧 PR 提出 JWT TTL hot-reload / key rotation 真实需求时，把 placeholder log 替换为业务 reload，同时移除「placeholder per ADV-05」注释；不在 ADV-05 治理压力下提前删除已搭好的骨架 | feat/refactor | P2/Cx2 | 🟠 | 业务侧 JWT TTL hot-reload / key rotation 需求出现 | `cells/accesscore/slices/configreceive/` + `cells/accesscore/internal/adapters/http/configclient.go` | systems layer review + 030 §2 C-01 + 035 §6 |
| PR-CFG-G1-FU6 | **ConfigCore G1 follow-up 6** — 现状: PR-CFG-G1 余项；修复: 单独跟进 | arch-opt | Cx2 | 🟡 | — | `cells/configcore/` | PR-CFG-G1 |
| PR320-FU-CONFIGCORE-CI-NOOP | **ConfigCore CI noop test** — 现状: noop publisher CI 路径未覆盖；修复: 加测 | test | P3/Cx1 | 🟡 | — | `cells/configcore/` | PR#320 |
| B2-A-33 | **Redis sentinel env & logvalue 缺** — 现状: sentinel 模式 env 配置不完整 + log value 缺；修复: 补 env 列表 + logvalue 透传 | bug | P2/Cx2 | 🟡 | sentinel 部署 | `cmd/corebundle/redis.go:18-22` + `adapters/redis/client.go:90-104` | backlog2 §5.3 B2-A-33 |
| B2-C-11 | **Configsubscribe tombstone 无 TTL** — 现状: tombstone 永久保留导致内存膨胀；修复: 加 TTL + 定期清理 | bug | P2/Cx2 | 🟡 | — | `cells/configcore/slices/configsubscribe/service.go:29,169` | backlog2 §4 B2-C-11 |
| PR238-FU8 | **CONFIGREPO-UPDATE-ROLLBACK-OP-LABEL-TEST-01** — 现状: `doUpdate` 通过 `op` 参数向 `scanConfigOrMapError` 传 `"Update"` 或 `"UpdateForRollback"`，`InternalMessage` 携带该 op，但 `TestConfigRepository_UpdateForRollback_NotFound` / `TestConfigRepository_UpdateForRollback` 均未断言 InternalMessage 含 `"UpdateForRollback"`，若有人把 op 硬编码回 `"Update"`，CI 不会 FAIL；修复: 相关 NotFound 测试追加 `assert.Contains(t, ec.InternalMessage, "UpdateForRollback")` | test | P3/Cx1 | 🟡 | — | `cells/configcore/internal/adapters/postgres/config_repo_test.go` | PR#238 L4 round-2 reviewer T-R4 + 029 master roadmap §errcode W4 |
| CONFIGREPO-OP-LABEL-TYPED-ENUM-HARD-01 | **op label typed enum (Hard 升级 PR238-FU8)** — 现状: PR#553 抽 `opUpdate / opUpdateForRollback` unexported const + `Update_NotFound` 双向 NotContains 锁定到 AI-rebust Medium；改字面量需改 const 单点。修复方向: doUpdate 接受 typed `updateOp` private type（2 valid values），InternalMessage 由 enum.String() 派生，硬编码 string 编译失败（违反不可表达）→ Hard | arch-opt | P3/Cx2 | 🟠 | 同模式 op-string-label-in-error-internal-message 在 ≥ 2 个其他仓储路径出现 | `cells/configcore/internal/adapters/postgres/config_repo.go` | PR#553 plan §AI-rebust evaluation |
| C-02 | **CONFIGSUBSCRIBE-CACHE-LIFECYCLE** — configsubscribe.Cache 进程内无界 + 未挂 Lifecycle，长寿进程内存增长；修复: 挂 `kernel/cell.LifecycleHook` OnStart hydrate / OnStop snapshot；改 LRU + size cap；暴露 `eventbus_cache_size` metric | bug | P1/Cx2 | 🟡 | — | `cells/configcore/slices/configsubscribe/` | 030 §2 C-02 |

---

## cap-10: 持久化与加密

> 主要包：`kernel/persistence` + `kernel/crypto` + `adapters/{postgres,vault}`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| ACCESSCORE-PG-USERS-MIGRATION-01 | **AccessCore PG repository + migration** — 现状: 仅内存；修复: users/roles/role_assignments 表 + UNIQUE on admin role | feat | P1/— | 🔴 | — | `adapters/postgres/accesscore/` | PR #392 v2 review |
| PR-V1-PG-STARTUP-HARDEN-FU-RACE-COVERAGE | **TEST-RACE-COVERAGE-ADAPTERS-INTEGRATION-01** — 现状: PG concurrent Up CI 不带 -race；修复: test-race.yml 加 adapters/postgres 路径（评估） | test | P2/Cx3 | 🟡 | — | `.github/workflows/test-race.yml` | PR-V1-PG-STARTUP-HARDEN F5 |
| X1 | **PG-DOMAIN-REPO** — 现状: 5 个 Repository 仅内存；修复: User/Session/Role/Device/Command PG 实现 + 4 migration DDL；联动 RBAC-ASSIGN-LEVEL-UPGRADE ✅ closed by S4c-T1 (rbacassign 统一 L2 + RBACASSIGN-L2-STATIC-01 archtest 锁定字面量)/ SEED-ROLE-IFACE ✅ closed by S4c-T1 (adminprovision 重构隐性闭环 + SEED-ROLE-IFACE-01 Hard archtest 锁定生产代码零 `*mem.RoleRepository` 引用)/ AUTH-CACHE 激活 ✅ closed by S4c-T5 (`adapters/redis.CachingSessionStore` 默认关闭，env `GOCELL_SESSION_CACHE_TTL` 启用；触发型 backlog AUTH-CACHE-SUBJECT-REVERSE-INDEX-01 守 user.AuthzEpoch 进 cache 升级路径) | feat | P3/— | 🟡 | — | `adapters/postgres/*` | PR#155 review F4 |
| S14a | **AWS KMS provider** — 现状: 仅 Vault；修复: 加 KMS adapter | feat | — | 🟠 | 云平台部署需求 | `adapters/kms/` (新) | S14a |

---

## cap-11: 分布式锁

> 主要包：`runtime/distlock` + `adapters/redis`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|

---

## cap-12: 启停编排 (Bootstrap)

> 主要包：`runtime/bootstrap` + `runtime/shutdown`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| V-A8-DEFERRED | **CMD-CORE-INTERNAL-GUARD-PUBLIC-01** — 现状: cmd/corebundle/main.go 28 行，archtest 锁 ≤30；修复: 触发后评估提升为公开类型 | debt | Cx2 | 🟠 | runtime/bootstrap 子包出现 / 多消费方 | `runtime/bootstrap/` + `cmd/corebundle/` | PR-A64a deferred |
| PR252-F1 | **QueueRegistrar bootstrap 集成** — 现状: 当前仅 InMemQueue；修复: 下一个 durable command adapter 落地时加入 | arch-opt | Cx3 | 🟠 | 下一个 durable command adapter | `runtime/command/` | PR#252 |
| PR252-F2 | **Sweeper 生产治理** — 现状: 单 replica 假设；修复: multi-replica command consumer 时落 | arch-opt | Cx4 | 🟠 | multi-replica command consumer | `runtime/command/` | PR#252 |
| PR333-BOOTSTRAP-OPTION-CROSS-CONCERN | **Bootstrap option 跨 concern 拆分** — 现状: option 概念混杂；修复: 按 concern 拆 | arch-opt | Cx2 | 🟡 | — | `runtime/bootstrap/` | PR#333 |
| PR448-BUDGET-ISOLATION-PARENT-CHAIN-GUARD | **PHASE10-TEARCTX-PARENT-CHAIN-GUARD-01** — 现状: TestPhase10_BudgetIsolation_LIFOTeardownGetsFreshCtx 只断言 ctx 未 done，无法检测未来若有人把 tearCtx 改成 `context.WithTimeout(drainCtx, ...)` 形成继承链导致 budget 隐性泄漏；修复: 加一断言用 `context.WithCancel(Background)` 包装 tearCtx parent，drain 期间主动 cancel 并验证 tearCtx 不传播 cancel | test | Cx1 | 🟡 | — | `runtime/bootstrap/shutdown_ordering_test.go` | PR#448 reviewer F4 |
| STARTUP-ROLLBACK-ERR-JOIN-01 | **Startup rollback 错误聚合** — ✅ closed by PR #499 (2026-05-15)：`rollback()` 用 `errors.Join`（cause first）聚合全部 teardown error，新 `safeTeardown` 在 deferred recover 内把 panicking teardown 转 error 不中断 LIFO；assembly `rollbackCells` 返回 `[]error` 经 `failStart` join；`phaseError.Error()` 统一 `"phase: err"` 格式；抽 `pkg/testutil/errutil.FlattenJoined` 共享 helper。**未加 archtest**：errors.Join 因果顺序为业务语义，不可用 AST/type system 静态守卫（Soft，AI-collab 章程禁止 Soft 立项），已评估无 ≥Medium Hard 化路径，行为回归由单测闭环（4 个新单测覆盖 named panic/LIFO continuity/joined error tree）。 | bug | P1/Cx2 | ✅ PR #499 | v1.0 GA 前 | `runtime/bootstrap/run_state.go` + `kernel/assembly/` + `pkg/testutil/errutil/` | backlog1 §2.4 |
| COREBUNDLE-MAINTEST-FAIL-FAST-01 | **corebundle main_test fail-fast** — 现状: bind 错误被白名单吞掉；修复: 用 `net.Listen("tcp", "127.0.0.1:0")` 注入 + 断言关键装配里程碑 | test | Cx2 | 🟡 | — | `cmd/corebundle/main_test.go` | backlog1 §2.7 |
| B2-R-01 | **HealthListener 缺失时静默回退** — 现状: bootstrap 找不到 HealthListener 时静默回退到 main listener；修复: fail-fast 或显式 opt-in fallback | bug | P2/Cx2 | 🟡 | — | `runtime/bootstrap/bootstrap_phases.go:583-596` | backlog2 §3 B2-R-01 |
| B2-R-02 | **Readyz 缺少 repo probe** — ✅ closed by PR-REPO-READYZ (fix/202-repo-readyz, 2026-05-16)：typed funnel `cell.RegisterRepoReadiness` + `kernel/cell.RepoHealthProber` interface；3 cell 统一注册 `config_repo_ready` / `session_store_ready` / `audit_ledger_ready`；accesscore 死代码 duck-type probe 修复；`LedgerStore.Probes()` 特殊路径删除；`RunRepoReadinessConformance` real-failure conformance harness；archtest `CELL-REPO-READYZ-PROBE-01`；Cx2→Cx3（范围扩展：typed funnel + conformance + archtest）。范本：`cell.RegisterRepoReadiness` 可供未来 cell 直接复用。 | bug | P1/Cx3 | ✅ PR-REPO-READYZ | — | `kernel/cell/` + `cells/{configcore,accesscore,auditcore}/` + `kernel/cell/celltest/` + `tools/archtest/` | backlog2 §3 B2-R-02 |
| B2-X-03 | **PG invalid index warn continue** — 现状: PG invalid index 仅 warn 继续启动；修复: 改 fail-fast 防隐藏数据完整性问题 | bug | P2/Cx2 | 🟡 | — | `cmd/corebundle/bundle.go:308-313` | backlog2 §7 B2-X-03 |
| B2-X-09 | **OUTBOX-FU-COREBUNDLE-NEGATIVE-INTEGRATION** — 现状: PR#384 N8 把 `claiming → lease_id NOT NULL` 升为 DB 级 CHECK 约束并删 `VerifyOutboxLeaseInvariant` 启动探针后，corebundle 真实 wiring 路径上不再可能产生 NULL lease residue（DB 先 fail），原计划"corebundle 负向集成测试 — NULL lease residue 真实 wiring 阻断启动"沦为不可达分支；修复: 触发条件式补集成回归 (also: cap-07) | test | P3/Cx2 | 🟠 | N3 改造 corebundle startup wiring 顺序 / 引入 cross-cluster outbox 同步路径（CHECK 不能跨 cluster 守护） | `cmd/corebundle/bundle.go` + `cmd/corebundle/consumer_base_integration_test.go` | PR#373/#374 review 二轮 won't-do 登记 + backlog2 archive §7 B2-X-09 |

---

## cap-13: 可观测性

> 详见 [`backlog/cap-13-observability.md`](backlog/cap-13-observability.md)（32 条目，按主题分 5 个 h2 子节）

**子节索引**：
- [13.1 health / readyz / probe](backlog/cap-13-observability.md#13.1-health--readyz--probe)
- [13.2 audit chain observability](backlog/cap-13-observability.md#13.2-audit-chain-observability)
- [13.3 metrics / collector](backlog/cap-13-observability.md#13.3-metrics--collector)
- [13.4 slog / logging / OTel](backlog/cap-13-observability.md#13.4-slog--logging--otel)
- [13.5 adapter managed resource / 杂项](backlog/cap-13-observability.md#13.5-adapter-managed-resource--杂项)

## cap-14: 代码生成与治理工具链

> 详见 [`docs/backlog/cap-14-tooling.md`](backlog/cap-14-tooling.md)（61 条目，按主题分 6 个 h2 子节）

**子节索引**：
- [14.1 archtest / typed funnel / scanner](backlog/cap-14-tooling.md#141-archtest--typed-funnel--scanner)
- [14.2 codegen / scaffold / verify](backlog/cap-14-tooling.md#142-codegen--scaffold--verify)
- [14.3 contract codegen + 兼容](backlog/cap-14-tooling.md#143-contract-codegen--兼容)
- [14.4 journey / status-board](backlog/cap-14-tooling.md#144-journey--status-board)
- [14.5 doc / ADR / NoLint / governance rules](backlog/cap-14-tooling.md#145-doc--adr--nolint--governance-rules)
- [14.6 杂项 / PR FU / T-*](backlog/cap-14-tooling.md#146-杂项--pr-fu--t-)

## cap-x-cross: 横切

> 详见 [`backlog/cap-x-cross.md`](backlog/cap-x-cross.md)（36 条目，按主题分 5 个 h2 子节）

**子节索引**：
- [x.1 adapter / 外部系统](backlog/cap-x-cross.md#x.1-adapter--外部系统)
- [x.2 PR-specific 跨域 FU](backlog/cap-x-cross.md#x.2-pr-specific-跨域-fu)
- [x.3 B-floor findings (B-FLOOR-FOLLOWUP + F-* 系列)](backlog/cap-x-cross.md#x.3-b-floor-findings-b-floor-followup--f-*-系列)
- [x.4 tech-debt P3/P4 系列](backlog/cap-x-cross.md#x.4-tech-debt-p3/p4-系列)
- [x.5 kernel/runtime cross-cut + 其它](backlog/cap-x-cross.md#x.5-kernel/runtime-cross-cut--其它)

## 历史与参考

- 原 backlog 305 行已备份到 [`docs/backlog/archive/backlog.md`](backlog/archive/backlog.md)（develop @ 18a06ab7 快照），含被本次迁移**跳过**的 narrative 段：
  - `## 架构演进里程碑（M0-M4，源自 ADR-202605041430）` — **M0 已大部分完成**（poolstats 接口下沉 PR#387 / Noop archtest / CellMeta 合一）；**M1/M2/M3/M4 已提取为 4 条 backlog item**（M1→cap-13、M2→cap-02、M3→cap-02、M4→cap-14）；narrative 段保留在 archive 作为完整 ADR 上下文
  - `## 设计决策记录（历史 — 不修，避免重复审查）`
  - `## v1.1+ 长期规划`
  - `## 工时汇总`
- `docs/backlog1.md` (231 行，2026-04-26 草案) / `docs/backlog2.md` (431 行，2026-04-29 4-archive) / `docs/backlog_later_detail.md` (91 行，V1.1+ 详解) / `docs/tech-debt-registry.md` (224 行，跨 Phase 技术债) 已分别并入本文件，原档完整备份到 [`docs/backlog/archive/`](backlog/archive/) 同名文件，原路径改成 1 段重定向桩。
- 主轴权威源：[`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md)
