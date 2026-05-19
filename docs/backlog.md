# GoCell Backlog

> **单源 backlog** — 按 14 capability units 主轴组织。  
> 主轴权威源：[`docs/reviews/capabilities/20260504-engineering-capability-domain-map.md`](reviews/capabilities/20260504-engineering-capability-domain-map.md) §1  
> 历史归档：[`docs/backlog/archive/`](backlog/archive/)
>
> 基线：`develop @ 7f886a621`（2026-05-19；**94 条 ✅/❌ 已增量归档至 [`backlog/archive/202605121700-backlog-completed-2026q2.md`](backlog/archive/202605121700-backlog-completed-2026q2.md) `## 2026-05-19 增量归档` 段**；新增收口 PR #575 B2.B / #582 G-08 / #585 C2 accountlockout / #588 C6 audit-e2e / #589 D3a-1 metric-pack；以下为上次基线 a835292c0 历史脉络）（2026-05-17；含 PR #481/#482/#483/#484/#485/#486/#487/#488/#490/#492/#493/#494/#495/#496/#497/#498/#499/#500/#507/#511/#522/#536 + plan 034 S4d/S4e ✅ (PR #494) + S4c 8/9 (T1 #514 / T2 #525 / T3 #515 / T4 #523 / T5 #524+#533 / FU-1 #513 / FU-2 #512 / FU-3 #516; 剩 FU-4) + plan 035 PR-CFG-CACHE-LIFECYCLE ✅ (#518) + SLICE-DECOMP configread 半部分 ✅ (#529 FMT-33) + plan 036 Wave 2/3 100% + plan 037r2 R2-P1/P2/P3 ✅ (#521/#540+#547/#528+#530) R2-P5 spike ✅ (#548 ADR 202605171200) P5.1 待启动 + plan 038 Wave 1 8/8 / Wave 2 PR-11 ✅ #504 (剩 PR-5) / Wave 4 4/5 ✅ (#499/#517/#518/#541; 剩 R-01+G-08) / Wave 5 1/3 ✅ (#531) / Wave 6 2/? ✅ (#538 S3-FAILURE-INJECTION + #543 PR-W6-1 CONTRACT-TEST-GATE A/B/C) + plan 029 F2 PR237-PM5/A4 ✅ (#549 listener-topology + godoc 入口) + plan 040 阶段 1 ✅ #492 / 1.5 ✅ #495 / 1.6 ✅ #500 / 1.7 ✅ #507 / 1.8 ✅ #511 / 阶段 2 全部 ✅ / 阶段 3 全部 ✅ (#500/#507/#522) / LegacyAllowlist 已清零 #522 / **Stage 4 ✅ #536 (a835292c0) — archtestmeta 删除 + RunTypedFixture funnel + R1+R1.1 (callee, arg) form-uniqueness Hard 闭环 + GuardListSync 简化** + plan 041 Lane A/B/C/D ✅ (#526/#527/#539/#544) Lane E 余 1 PR）

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
| B2-PROVISIONER-MUTEX-REVIEW | **Provisioner mutex 清理 review** — 现状: A26-R1 已删 initialadmin，但 provisioner mutex 残留；修复: PG adapter 落地后审视是否仍需 mutex（PG row-level lock + UNIQUE constraint 已覆盖并发场景，mutex 多半冗余）。**trigger 已达成（PR #482 S4a ship PG accesscore wiring）**，可立即审视 — 待 plan 039 P2 整理收口 | refactor | P2/Cx1 | 🟠 | trigger 已达成（PR #482）— 待 plan 039 排期 | `cells/accesscore/internal/adminprovision/provisioner.go` | backlog2 §13 + PR #482 unblock |
| C-04 | **CELLS-INIT-TEMPLATE-CONVERGE**（含 C-07 emitter health probe helper）— 3 cell Init 切分各异 + internal/ 子包不对称；修复: `kernel/cell` 提供 `BaseCell.RegisterStandard(reg, StandardInit{...})` 模板 + scaffold 预生成 `internal/{ports,domain,dto,events,mem}` 五目录 + 3 cell 改造 + scaffold 升级 + 抽 `cell.RegisterEmitterHealthProbes(reg, emitter)` helper（删 3 cell 4 处重复）| refactor | P2/Cx2 | 🟡 | K-06 落地后 | `kernel/cell/` + `cells/{accesscore,auditcore,configcore}/` + scaffold 模板 | 030 §3 C-04 + C-07 |
| C-06 | **L0-CELL-DECISION** — `l0Dependencies: []` 在 3 cell 全空，无任何 `type: l0` 实例，schema 字段是死代码路径；修复: 二选一 (a) 升 `pkg/query.CursorCodec` 等共享逻辑为示例 L0 cell；(b) 文档明确"L0 cell 是未来扩展点，当前无实例" | doc | P2/Cx1 | 🟡 | — | `cells/` + `kernel/metadata/` + docs | 030 §3 C-06 |
| C-09 | **CELL-SPLIT-LAYOUT-NORMALIZE** — accesscore + configcore 三文件范式不一致：(a) `configDirectPublishMode`/`ensureCursorCodec` 是 pure helper 但放 `cell_init.go`；(b) `RegisterSubscriptions` 放 `cell_routes.go` 名不副实；修复: 引入 `cell_lifecycle.go`（订阅注册）+ `cell_helpers.go`（pure helper）命名惯例；反向迁移 + scaffold 模板同步 | refactor | P2/Cx2 | 🟡 | K-07 一并 | `cells/accesscore/` + `cells/configcore/` + scaffold | 030 §3 C-09 |
| G-10 | **KERNEL-CELL-PACKAGE-DECOMPOSE**（2026-05-18 核实缩范围，4/5 子项残留）— ✅ 子项(4) `Cell` 接口已 ISP 拆分（`interfaces.go:118` 组合 `CellIdentity`/`CellLifecycle`/`CellStatus`/`CellInventory`，ADR `202605101800-adr-cell-interface-isp-split.md`，命名非原案 CellDescriptor）。**残留**: (1) `auth_plan.go` 仍在 `kernel/cell/`（14KB，未移 `kernel/auth/`，该目录不存在）；(2) `mode_resolver.go` 仍在 `kernel/cell/`（9.5KB，未移 `kernel/outbox/` 改名）；(3) `cell.Registry` 接口未改名 `Registrar`（`registry.go:48`）；(5) `health.go` alias 仍在（已演化为 import-cycle 规避单源，需评估保留）| refactor | P1/Cx3 | 🟡 | 与 029 #13 PR-A22 协同 | `kernel/cell/{auth_plan,mode_resolver,registry,health}.go` | 030 §3 G-10 → 2026-05-18 核实 |
| SWEEPER-OPAQUE-INTERFACE-HARD-UPGRADE-01 | **Sweeper Hard 升级** — 现状: Medium runtime fail-closed sentinel (built) 已建；修复: 改 NewSweeper 返回 opaque interface，零值不可表达 | arch-opt | P3/Cx3 | 🟢 | 出现第二个 zero-value `command.Sweeper{}` caller | `kernel/command/sweeper.go` | sweeper.go godoc + CHANGELOG PR441 |
| PR441-FU-CELLINVENTORY-METADATA-READONLY-VIEW-01 | **CellInventory.Metadata() deep-copy hot-path 优化** — 现状: 每次调用 b.meta.Clone() 防御复制；修复（推测性）: 改 MetadataView() metadata.CellMetaView 返回只读 view（type-system 不可变）；或 godoc 警示 "callers should cache result" | perf-opt | P3/Cx3 | 🟢 | Metadata() 出现在 hot path benchmark p99 退化 ≥ 10% | `kernel/cell/interfaces.go` + `kernel/cell/base.go` | PR441 review architect-F1（推测性，无 benchmark 数据）|
| SEALED-MARKER-DEFENSE-EXPANSION-BUNDLE | **Sealed marker / typed primitive 防线扩展束（PR #441/442 review 聚合，7 子条）** — 现状: sealed marker / typed primitive 主防线已 ship（CELL-RAW-INFRA-SEALED-MARKER-01 / SCAFFOLD-AUTOGEN-SCOPE-SEALED 等），扩展面 7 处待收口；修复: 各子条独立排期或合并 PR-A23 sealed marker 扩展批。子条：<ul><li>**PR441-FU-CELLEMITTER-SEALED-MARKER-01** (P2/Cx3, 🟠 立即排期 PR-A23) — 扩展 sealed marker 到 outbox.CellEmitter，WithEmitter 改签 + 3 cell + 新 archtest + ADR (Files: `kernel/outbox/{emitter,cell_marker}.go` + `cells/{accesscore,auditcore,configcore}/cell.go`，PR441 user-finding F1)</li><li>**PR441-FU-RAW-INFRA-PARAM-SIBLING-EXPAND-01** ✅ closed by PR #481 (PR-S7, 2026-05-13；决策反转) — 原 architect "不立项"（sealed marker Hard 主防线已覆盖）在 PR-S7 sealing 10 slice WithTxManager 时被推翻：ADR §D1 line 46 "服务签名零变化"与 slice-level sealing 新 scope 矛盾。PR-S7 同 PR 把 `isCellPackageRootFile → isCellSubtreeFile`（cell-package root → `cells/<x>/**/*.go` + `examples/<demo>/cells/<x>/**/*.go`，超原条目仅 sibling），即时扫出 `examples/todoorder/cells/ordercell/slices/ordercreate/service.go` 第 11 处 raw 暴露；ADR `202605101900` Amendment 2026-05-12 记录边界扩展 (Files: `tools/archtest/cell_public_option_param_test.go` + ADR 202605101900，PR441 reviewer F3-2)</li><li>**CELL-PUBLIC-OPTION-NAMED-IFACE-EMBED-01** (P1/Cx2, 🟢 PR #441 round-4 收口) — `canonicalFromType` 在 `*types.Named` 分支补 `Underlying().(*types.Interface)` walk；fixture 加 named local interface embed case (Files: `tools/archtest/cell_public_option_param_test.go`，PR441 round-3 follow-up)</li><li>**ADR-CELL-RAW-INFRA-WORDING-01** (P2/Cx1, 🟢 PR #441 round-4 同 PR) — ADR §"AI 写 WithFoo 在 cell.go 编译期被拒"措辞不准；改为"定义可编译但调用 site 因 sealed marker 缺失被拒"两层 (Files: ADR 202605101900，PR441 round-3 follow-up)</li><li>**SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01** (P2/Cx3, 🟠 跨包 spec 输入误用 / 第 4 个副本) — typed `ScaffoldID` value type + 共享 validator；三处 spec (cmd/gocell + cellgen + assembly) 字段类型升级 (Files: `pkg/scaffoldid/`(新) + 3 spec 包，K#09 PR#442 round-5 R6 + kernel-guardian F4)</li><li>**ASSEMBLY-META-SYNTHESIS-FIELD-GUARD** (P2/Cx2, 🟠 AssemblyMeta 字段集变更 / synthesizeAssemblyMeta 漏字段事故) — reflect 字段计数 guard archtest，AI-rebust godoc Medium → reflect Hard 升级候选 (Files: `tools/archtest/` + `kernel/assembly/generator.go` + `kernel/metadata/types.go`，K#09 PR#442 round-6 + kernel-guardian F2)</li><li>**SEALED-MARKER-FILE-LIST-AUTODISCOVER-01** (P3/Cx2, 🟢 sealed marker 第三个包 PR-A23 后) — `sealedMarkerFiles` hand-crafted 改 `packages.Load(./kernel/...)` + grep `internalCell` 前缀类型自动发现 (Files: `tools/archtest/sealed_marker_noop_transparency_test.go`，reviewer F4)</li></ul> | arch-opt | P1/Cx3 | 🟠 | A.1/A.5/A.6 立即排期；A.2/A.3/A.4/A.7 触发型按各自条件 | `kernel/{outbox,persistence}/` + `tools/archtest/` + `pkg/scaffoldid/`(新) + scaffold 输入校验 | PR #441/#442 六角色 review 聚合 + ADR 202605101900 |
| CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01 | **控制面时钟 Hard 升级** — 现状: `runtime/command` 内 `controlPlaneTicker` / `controlPlaneProbeTimer` 两个函数使用函数级 `//archtest:allow:clock-injection:control-plane` comment-guard，AI-rebust 评级 Medium（**PR #531 A1-6 锚定后**：marker + archtest 内 `controlPlaneClockCarveOut` `{rel→函数名}` allowlist 双门，marker 单独不再豁免；修复前等效 Soft，已闭环到 Medium，RED 自检 `control_plane_marker_wrong_path_violates` / `control_plane_marker_wrong_func_violates`）；修复方向: 引入 sealed typed real-only 控制面时钟 funnel，让"非真实时钟源驱动控制面 ticker/probe"在 Go 类型系统层不可表达（Hard）；典型实现: `type ControlPlaneClock struct{}` 私有字段，仅含 `NewTicker(d) *time.Ticker` / `NewTimer(d) *time.Timer` 两个方法，包外无法构造 fake 实现 + archtest 锁定 `controlPlaneTicker` / `controlPlaneProbeTimer` 唯二调用点。受豁免函数清单（截至 PR #531，权威源 = archtest `controlPlaneClockCarveOut`）: `controlPlaneTicker` + `controlPlaneProbeTimer`（均在 `runtime/command/lifecycle.go`）。ref: ADR `202605170000-adr-control-plane-business-plane-decouple.md` §D-A | arch-opt | P3/Cx3 | 🟢 | comment-guard 被 AI 绕过导致 fake clock 重新注入控制面 scheduling | `runtime/command/lifecycle.go` + `tools/archtest/clock_invariants_test.go` | ADR 202605170000 §D-A; ai-collab.md §Medium backlog 登记要求 |
---

## cap-02: 元数据解析与治理

> 详见 [`backlog/cap-02-metadata-governance.md`](backlog/cap-02-metadata-governance.md)（25 条 OPEN，按主题分 4 个 h2 子节；已完结见归档）

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
| PR392-FU-RATE-LIMITER-DISTRIBUTED | **BOOTSTRAP-RATELIMIT-DISTRIBUTED-01** — 现状: in-memory token bucket per pod；修复: 出现暴力枚举威胁时引入 Redis-backed | arch-opt | P3/Cx3 | 🟡 | bootstrap mode + 多 pod | `adapters/ratelimit/` + `cmd/corebundle/access_module.go` | PR #392 ADR §D10 |
| BOOTSTRAP-INTERNAL-LOCAL-ONLY-FAIL-FAST-01 | **internal listener LOCAL_ONLY fail-fast** — 现状: `cmd/corebundle/access_module.go:373` 注释明说 `GOCELL_HTTP_INTERNAL_ADDR=0.0.0.0:9090` 是 misconfiguration 但 corebundle 不拦截；只有 health listener 有 `GOCELL_HTTP_HEALTH_LOCAL_ONLY` 对称机制（`cmd/corebundle/shared_deps.go:246`）。internal 当前靠 ServiceToken Hard 兜底（archtest `SEC-FAIL-CLOSED-06`），但部署建议层无代码侧拦截，从 docs/ops/listener-topology.md §Deployment Recommendations Soft → Medium 路径缺位。修复: 给 internal listener 加 `GOCELL_HTTP_INTERNAL_LOCAL_ONLY` envvar + real adapter mode 检测 internal addr 非回环时 fail-fast（除显式 opt-in），对称 health 机制；加 archtest 守该机制 + 单测覆盖 dev/real × 回环/非回环 × opt-in 四象限。**AI HARD 评级**：Medium（runtime guard at corebundle）。 | arch-opt | P2/Cx2 | 🟡 | — | `cmd/corebundle/shared_deps.go` + `cmd/corebundle/shared_deps_validate.go` + `tools/archtest/security_defaults_test.go` + tests | PR #237 round-3 / PR #222 (PR237-PM5 Soft 升级路径) |
| PR237-PM7 | **EXAMPLE-INTERNAL-LISTENER-COMMENT-01** — 现状: examples/*/main.go 双 addr 缺注释；修复: 加注释或 `WithHTTPInternalDisable` | doc | Cx1 | 🟡 | — | `examples/*/main.go` | PR #237 round-2 PM-07 |
| LISTENER-API-SPEC-01 | **Listener API spec 化** — 现状: listener 选项散在代码；修复: contracts 化声明 | arch-opt | Cx2 | 🟡 | — | `contracts/http/` | PR#237 |
| ROUTE-ERROR-POLICY-01 | **Route error policy 统一** — 现状: 3+ route family 错误处理不一；修复: 定义共享 policy | arch-opt | Cx3-Cx4 | 🟠 | 3+ route 家族出现 | `runtime/http/` | systems review |
| T4 | **CB-RESILIENCE-PACKAGE-01** — 现状: Allower / CircuitBreakerRetryAfter 在 `runtime/http/middleware`；修复: 迁到 `runtime/resilience/circuitbreaker/` 独立包 (also: cap-x-cross) | refactor | — | 🟠 | 出现第 2 个非 HTTP CB 消费方 | `runtime/http/middleware/` + `runtime/resilience/circuitbreaker/` (新) | T4 |
| WM-32 | **mTLS 中间件** — 现状: 缺；修复: 加 TLS 构建器 + HTTP 证书提取钩子（折中：大规模环境 mTLS 卸载在 K8s/Service Mesh 解决，框架仅提供构建器） | feat | P2/Cx2 | 🟡 | V1.1 启动 | `runtime/http/middleware/` | backlog_later §7 WM-32（4/6 票）|
| B2-T-08 | **Config publish 失败码声明不完整** — 现状: contract 缺部分失败码声明；修复: 补 4xx/5xx 完整声明 | bug | P2/Cx1 | 🟡 | — | `contracts/http/config/publish/v1/contract.yaml` | backlog2 §8 B2-T-08 |
| J-04 | **CONTRACT-SCHEMA-NAMING-NORMALIZE** — (a) api-versioning.md 写 `pageSize`，contract 实际用 `limit`（规则与代码漂移）；(b) event headers `event_id`(snake_case) 与 cell-patterns.md "camelCase" 冲突；修复: 改规则文档 + 与 J-03 v1→v2 演练搭车统一 envelope | bug | P1/Cx1 | 🟡 | 与 J-03 同 PR | `.claude/rules/gocell/` + `contracts/` | 030 §3 J-04 |
| READYZ-PROBE-FAILURE-METRIC-01 | **Readyz probe 失败 Prometheus counter** — 现状: 四通道模型落地后 wire 简化，operator 唯一诊断信号是 slog；缺 `readyz_probe_failures_total{name=...}` 细粒度告警，无法在不解析 slog 的情况下触发 PagerDuty 等告警规则；修复: `runtime/http/health` 暴露 prometheus counter，与 `_ready` 命名约束对齐 | feat | P2/Cx2 | 🟡 | 监控有需求时 | `runtime/http/health/` | ADR 202605171200 §3 / PR #552 review C8 |
| FOUR-CHANNEL-HANDLER-EXTENSION-01 | **第二个 handler 引入 ops-diagnostics 通道 d 时的扩展协议** — 现状: 四通道模型已在 readyz handler 落地（ADR 202605171200）；当第二个 handler 引入 ops-diagnostics 通道 d（典型场景: recovery middleware panic dump / outbox last_error sanitize / auditquery payload redaction）时，需在 ADR §6 funnel matrix 追加条目，并对齐 typed redacted 包装类型 + archtest funnel 形态；修复: 在 ADR 202605171200 §5 §6 补充扩展协议说明 | doc | P3/Cx2 | 🟢 | 新增 handler 引入时 | `docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md` §5 §6 | PR #552 review C9 |

---

## cap-05: 身份认证 (Authn)

> 主要包：`runtime/auth` + `auth/refresh` + `auth/refresh/memstore` + `auth/config`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| ACCOUNT-LOCKOUT-POLICY-CONFIGURABLE-01 | **auto-lockout 常量 → per-tenant Policy struct 升级** — 现状（PR-618 落地后）: `cells/accesscore/internal/accountlockout/policy.go` 用 hardcoded `const Threshold=5 / StaleWindow=15min / LockoutTTL=15min`（Keycloak / ASP.NET Identity 业界默认对齐），无 per-tenant 调参。修复方向: 触发条件出现时（per-tenant SaaS / 行业合规要求异常阈值）抽 `Policy struct { Threshold int; StaleWindow time.Duration; LockoutTTL time.Duration }` + `Validate()` + `accountlockout.WithPolicy(p Policy)` Option，从 const → 注入；composition root 从 assembly.yaml / config 派生。**触发条件**：业务出现 per-tenant 策略需求 | feat | P3/Cx2 | 🟢 | 业务出现 per-tenant 策略需求 | `cells/accesscore/internal/accountlockout/policy.go` + `cmd/corebundle/access_module.go` | PR-618 (ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01) YAGNI 自审路径 |
| PR-A8-RESIDUAL | **Vault K8s auth E2E** — 现状: Vault K8s auth 实现已落，缺真 K8s e2e；修复: 跑 testcontainers k8s 验证 | arch-opt | Cx2 | 🟡 | — | `adapters/vault/` | PR#305 |
| PR338-FU-AUTH-FAIL-CLOSED-DOC-CLEANUP | **AUTH-FAIL-CLOSED-DOC-CLEANUP-01** — 现状: nonce.go docstring + archive quickstart 未跟 PR-CFG-I 更新；修复: 补 deprecation banner | doc | P3/Cx1 | 🟡 | — | `runtime/auth/nonce.go` + `docs/archive/specs/201-wm2-key-rotation/quickstart.md` | PR#338 round-1 |
| PR267-FU-AUTHTEST-INTERNAL | **Auth test 内部化** — 现状: testHelpers 暴露过多；修复: internal package | arch-opt | Cx1 | 🟡 | — | `cells/accesscore/` | PR#267 |
| PR267-FU-ROLE-PREFIX-ADR | **Role prefix ADR** — 现状: role 命名前缀约定无 ADR；修复: 写 ADR | doc | Cx1 | 🟡 | — | `docs/architecture/` | PR#267 |
| X3 | **WM-36 SecureCookie key rotation** — 现状: 无密钥轮转；修复: 接入 rotation worker | feat | P3/— | 🟡 | — | `runtime/auth/` | WM-35 后续 |
| X5 | **P3-TD-11 accesscore domain 拆分** — 现状: domain 包过大；修复: User/Session/Role 拆分 | refactor | P3/— | 🟡 | X1 落地后 | `cells/accesscore/internal/domain/` | 历史 Batch 8 |
| X13 | **REFRESH-PARTITION-01** — 现状: 批量 DELETE GC；修复: `expires_at` range 分区 + DROP PARTITION (also: cap-10) | feat | P3/Cx2 | 🟠 | 生产流量达阈值 | migration + ops runbook | 通用 PG 模式 |
| T5 | **AUTH-SIGNER-01** — 现状: SigningKeyProvider 返回 `*rsa.PrivateKey`；修复: 改 `crypto.Signer` 支持 HSM/KMS/EC | arch-opt | — | 🟡 | caller 需 HSM/KMS | `runtime/auth/` | T5 |
| C-AC7 | **JWT jti claim 支持** — 现状: 缺 jti，单 token 无法黑名单撤销；修复: Issue() 加 jti + jti 黑名单存储 | feat | P2/Cx2 | 🟡 | 出现单 token 撤销需求 | `runtime/auth/` | backlog_later §6 C-AC7 |
| AUTHZ-MUTATION-FUNNEL-UPSTREAM-HARD-01 | **funnel 上游 caller-set 由 Medium 升 Hard** — 现状: `AUTHZ-MUTATION-APPLY-FUNNEL-01` 下游 Hard（domain 字段私有化 compile-time 不可绕过）已闭合，但上游 caller-set 是文件级 archtest allowlist（Medium-by-necessity）：creation-time 调用无法用 sealed interface / codegen 在 compile 期表达"仅限构造时"语义，且 identitymanage.Delete / changePasswordInTx / rbacassign.Revoke 因 co-tx aggregate 语义必须直调 `invalidator.Apply`（经 authzmutate 是聚合语义错误，非疏漏）。#494 residual 对标 ent(`tx.Client()`=capability=Medium)/go-kratos(context-tx=Low) 实证：Go 在 tx-bound side-effect funnel 的类型天花板就是"下游 Hard + 上游 Medium"，任何 TxHandle/marker 是伪 Hard。修复方向（若未来出现可达 Hard 路径）: codegen 注入构造点 + sealed tx-scoped handle 让"非构造期 / funnel 外调用"包外不可表达。**charter §Funnel双向锁 mandated 显式登记项**，D1 archtest godoc 已点名本条。 | arch-opt | P3/Cx3 | 🟠 | 出现 codegen-injected 构造点可表达"仅构造期"语义时立项 | `cells/accesscore/internal/{authzmutate,credentialinvalidate}/` + `tools/archtest/domain_authz_mutation_funnel_invariants_test.go` | ADR-credential §A10; ai-collab.md §Funnel双向锁评级; #494 residual review |
| CREDENTIAL-AUTHORITY-FUNNEL-SCOPE-AUTO-DERIVE-01 | **funnel scope 自动派生** — 现状：`tools/archtest/credential_authority_assert_funnel_test.go` 的 `assertCallerAllowlist` + `sliceFunnelScopes` 是手维护的 3 个 slice 前缀；新增第 4 slice 必须手动更新 archtest allowlist 否则 funnel 失效。修复方向：从 `cells/accesscore/cell.yaml` + 各 `slice.yaml` 元数据派生 owner package 集合（参见 `kernel/governance` 现有元数据派生模式）。**触发条件**：accesscore 新增第 4 slice 时立项。 | arch-opt | P3/Cx2 | 🟠 | accesscore 新增第 4 slice | `tools/archtest/credential_authority_assert_funnel_test.go` + `tools/archtest/session_revoked_field_access_test.go` + cell.yaml 派生器 | PR #542 reviewer Cx3 #4 |
| SESSION-AUTHORITY-FUNNEL-CONDITIONAL-UPGRADE-01 | **session-state 独立 funnel 升级** — 现状：`SESSION-REVOKED-FIELD-ACCESS-01` 是单字段 typed FieldAccess allowlist。若未来 session-state 检查扩到 ≥3 字段（如 ExpiredAt + SuspendedAt + ProtocolVersion），应升级为 `cells/accesscore/internal/sessionauthority` 平行 funnel（sealed Check + factory），与 `credentialauthority` 对称。**触发条件**：session 状态字段进入第 3 个时立项。 | arch-opt | P3/Cx3 | 🟠 | session 状态字段 ≥3 | `cells/accesscore/internal/sessionauthority/` (新建) + `tools/archtest/session_authority_funnel_test.go` (重命名升级) | ADR-credential §A11.4 + §A13 |
| WIRE-UNIFORM-RESPONSE-ARCHTEST-01 | **wire-level single-envelope 升 Hard** — 现状：§A13 wire-uniformity 由 per-slice `service_test.go` 断言（结构化字段 + 组合用例 revoked+inactive、revoked+repoErr 等）守护，Medium 评级。修复方向：扫三 slice 入口（VerifyIntent / Refresh / Login）的 error return path 类型身份，按入口归类断言每类入口对应的 errcode 集合是 enumerable allowlist（typed `*types.Info` + `errcode.Code` const 集合）。**触发条件**：PR #542 §A13 已落地后立项作为后续 hardening。 | arch-opt | P2/Cx3 | 🟠 | 下一次 archtest hardening pass | `tools/archtest/wire_uniform_response_test.go`（新建）+ `cells/accesscore/slices/{sessionvalidate,sessionrefresh,sessionlogin}/service.go` | ADR-credential §A13; PR #542 reviewer P1-A 闭环 |
| SESSIONREFRESH-STALE-EPOCH-REJECT-HARDEN-01 | **SESSIONREFRESH-STALE-EPOCH-REJECT archtest Hard 升级** — 现状: `SESSIONREFRESH-STALE-EPOCH-REJECT-01` 所有 prong 使用 go/parser AST 名字/字符串锚点（Sel.Name / Ident.Name / BasicLit）而非 typeseval 类型身份解析，AI-rebust 评级为 Medium（PR #494 review F1）；修复: 将 `cascadeRevoke` 和 `handleReuseDetected` callee 识别从 Sel.Name 字符串锚点替换为 `typeseval.ResolveMethodCall` 类型身份解析，同时对 rejectIfStaleEpoch 调用点的 control-flow 形态做类型级锁定，升为 Hard | arch-opt | P2/Cx2 | 🟠 | 下一次 archtest hardening pass | `tools/archtest/sessionrefresh_stale_epoch_reject_test.go` | PR #494 review F1 |
| P4-TD-03 | **IssueTestToken HS256 dead code** — 现状: 测试 helper 仍保留 HS256 路径，JWTVerifier 全拒；修复: 删 dead code 防误用 | refactor | Cx1 | 🟡 | — | `runtime/auth/` (test helper) | tech-debt-registry P4-TD-03 |
| SECURECOOKIE-AEAD-NEG-01 | **SecureCookie AEAD 负向测试** — 现状: AEAD 失败路径无测试；修复: 截断/伪造/边界长度/解密失败类型断言 (`errors.Is(err, ErrAEADAuthFailed)`) | test | Cx2 | 🟡 | v1.0 GA 前 | `pkg/securecookie/securecookie_test.go` | backlog1 §2.5 |
| S1-CO-02-WIRING-OPTION-STICKY-DOCTRINE | **runtime-api.md sentinel sticky 通用契约明示** — 现状: 多处 wiring option（router.WithRateLimiter/WithCircuitBreaker/WithAuthMiddleware + session.WithFingerprint/WithOrdering）已实现 sentinel 粘滞失败行为，但 `.claude/rules/gocell/runtime-api.md §Option 范式分层` 未明示此为通用契约；session 包内已加 sticky test 锁定 Medium AI-rebust；修复: 章程层面明示 + 可选 archtest 跨 option 检测注释一致性 | doc+test | Cx2 | 🟡 | 下一次 wiring option 章程级修订 | `.claude/rules/gocell/runtime-api.md` + `runtime/auth/session/protocol.go` + `runtime/http/router/router.go` | PR#439 reviewer P1 follow-up |
| ACCOUNT-LOCKOUT-ESCALATION-01 | **N 次自动锁后升级永久锁** — 现状（PR-618 落地后）: 每次自动锁定 15 分钟后 lazy-unlock，攻击者可在 TTL 边界周期性 brute-force（每 14min 尝试 4 次永不触发锁定）。修复方向: `accountlockout.Service` 维护 `lockout_count` 列，达阈值（建议 N=3）后 `locked_until = NULL`（永久锁），需 admin 手动 unlock（Keycloak `maxTemporaryLockouts` 范式）。**触发条件**: 检测到对同一用户 N 次重复 auto-lock | feat | P3/Cx2 | 🟢 | 检测到对同一用户 N 次重复 auto-lock | `cells/accesscore/internal/accountlockout/` + `adapters/postgres/migrations/` (新增 lockout_count 列) | PR-618 review F7 |
| ACCOUNT-LOCKOUT-OOB-NOTIFICATION-01 | **账户自动锁定带外通知** — 现状: 账户因 auto-lock 触发时，用户不会收到任何通知（HTTP 响应刻意不暴露，符合枚举防御）。修复方向: 触发 auto-lock 时通过带外渠道（email notification event）通知账户所有人，不在 HTTP 响应中暴露。**触发条件**: 业务需要主动 UX 告知账户状态变更 | feat | P3/Cx2 | 🟢 | 业务需要主动 UX 告知账户状态变更 | `cells/accesscore/internal/accountlockout/` + 新建 notification event contract | PR-618 review F7 |
| ACCOUNT-LOCKOUT-WINDOW-SEMANTICS-01 | **stale-window vs 绝对滑动窗口语义评估** — 现状: 实现使用 Keycloak `maxDeltaTimeSeconds` 风格（`last_failed_at > 15min` 触发计数器 reset to 1），攻击者理论上可每 14min 尝试 4 次永不触发锁定。修复方向: 引入真实滑动窗口（ring-buffer 记录最近 N 次失败时间戳，窗口内计数超阈值即锁），或保留 stale-window 但将 window 缩短至 < lockout-TTL/threshold。**触发条件**: 观察到 stale-window-bypass 攻击 / 业务方要求绝对窗口语义 | arch-opt | P3/Cx2 | 🟢 | 观察到 stale-window-bypass 攻击 / 业务方要求绝对窗口语义 | `cells/accesscore/internal/accountlockout/service.go` | PR-618 review F7 |

---

## cap-06: 授权决策 (Authz)

> 主要包：`runtime/auth` (authz/policy)

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| T3 | **DEVICE-ENQUEUE-RBAC** — 现状: HandleEnqueue 无设备维度鉴权；修复: 加设备粒度策略 | feat | — | 🟠 | 多租户 operator | `cells/devicecell/` | T3 |
| T11 | **ADMIN-ROLE-DEDUP** — 现状: admin role 字符串散在多处；修复: 抽 const 单源 | arch-opt | — | 🟠 | role 命名漂移出现 | `pkg/auth/` + `cells/` | T11 |
| B2-T-07-FU-2 | **BUILTIN-SERVICE-ROLES 删除 FU** — 现状: scope 派生 builtin role 还在 hard-code；修复: 完全派生（A5 follow-up） | arch-opt | Cx3 | 🟠 | scope 派生工具就绪 | `runtime/auth/principal.go` | backlog2 §8 A5 follow-up |

---

## cap-07: 事务性事件发布 (Outbox Producer)

> 主要包：`kernel/outbox` + `runtime/outbox` + `adapters/postgres` (outbox table)

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| PR341-FU-OUTBOXTEST-CLOSE-BUDGET-COVERAGE | **OUTBOXTEST-CLOSE-BUDGET-COVERAGE-01** — 现状: conformance suite 仍裸调 `sub.Close(ctx)`；修复: 全部走 closeWithBudget 或 godoc 强约定 | test | P2/Cx1 | 🟡 | — | `kernel/outbox/outboxtest/conformance.go` | PR #341 round-1 |
| RBACASSIGN-L2-PG-ATOMICITY-01 | **RbacAssign L2 PG 原子性测试** — 现状: rbacassign 仅有 mem-based L2 路径测试（RecordingWriter），无 PG-level outbox 原子性验证；修复: testcontainer 驱动 PG outbox writer，故意 fail writer → 验证 domain 写成功 + outbox 失败 → tx rollback（与 AUDITAPPEND-L2-FAILURE-PROOF-01 同模式）。当前 mem adapter 不支持 RunInTx 故障注入，等 PG accesscore 仓储落地后补充。 | test | P1/Cx2 | 🟠 | X1 PG-DOMAIN-REPO 上线后 | `adapters/postgres/` (新) + `cells/accesscore/slices/rbacassign/` | PR #514 reviewer F9 |

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
| R-02 | **EVENTBUS-DROP-CONTEXTUAL-LOG** — InMemoryEventBus.broadcast/roundRobin drop 路径 slog.Warn 缺 entry_id/aggregate_id/event_type；修复: 升 Error 级 + 三字段（与 R-01 counter 对应）| bug | P2/Cx1 | 🟡 | — | `runtime/eventbus/eventbus.go` | 030 §2 R-02 |
| OUTBOX-HANDLERESULT-SLIM-01 | **HandleResult 字段精简** — 现状: ProcessReason/SettlementObservers 暴露在 handler 返回类型上，导致 ~15 处字面量无法用 factory 表达；修复方向: 把这两字段挪到 ConsumerBase internal state，handler 接口收敛为 Disposition+Err，达成 100% factory 覆盖。触发条件: (1) 新出现 ≥ 3 处需要 ProcessReason/SettlementObservers 字面量的业务 handler 调用点 / (2) HandleResult 需要加第 5 字段 / (3) 字面量回灌产生 ≥ 2 次 review finding。(also: cap-13) | refactor | P2/Cx2 | 🟡 | — | `kernel/outbox/outbox.go`, `kernel/outbox/consumer_base.go` | W9 plan §D2 |

---

## cap-09: 配置加载与热更新

> 主要包：`runtime/config` + watcher + `cells/configcore`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
| PR-CFG-A-DEFER-2 | **ConfigCore L2 divergence** — 现状: L2 与 L1 表项 schema 偏差；修复: 收口 | arch-opt | Cx1 | 🟡 | — | `cells/configcore/` | PR#268 |
| CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 | **ConfigReceive 业务 reload wire**（ID 名带 "PLACEHOLDER" "CLEANUP" 是历史误导，2026-05-10 已撤回直接删除主方案）— 现状: `cells/accesscore/slices/configreceive/service.go` 是**完整业务 reload 接入骨架已 ship**（不是 log placeholder）：event decode + `ConfigGetter.GetEntry` HTTP refetch (`http.config.internal.get.v1`) + 401/403/404/transient 四叉路错误分类 + DLQ + `obmetrics.ConfigEventCollector` metrics + cell wiring (cell.go:343-345, cell_init.go:279-283) + contract `endpoints.subscribers: [accesscore, auditcore, configcore]` 三处真值就位，骨架 ~10h 已沉没成本。修复: 业务侧 PR 提出 JWT TTL hot-reload / key rotation 真实需求时，把 `service.go:90` `slog.Debug("config upserted")` 替换为业务 reload 调用，同时把 `service.go:30` "placeholder per ADV-05" 注释（stale 表述）改为"业务骨架等触发"；不删除（撞 ADV-05 + 撞 contract subscribers + 业务侧重做 ~10h 三处真值）。参 `docs/plans/archive/202605101548-035-configcore-residuals-fix-plan.md` §6 | feat/refactor | P2/Cx2 | 🟠 | 业务侧 JWT TTL hot-reload / key rotation 需求出现 | `cells/accesscore/slices/configreceive/` + `cells/accesscore/internal/adapters/http/configclient.go` | systems layer review + 030 §2 C-01 + 035 §6 + 2026-05-18 亲眼核查 |
| PR320-FU-CONFIGCORE-CI-NOOP | **ConfigCore CI noop test** — 现状: noop publisher CI 路径未覆盖；修复: 加测 | test | P3/Cx1 | 🟡 | — | `cells/configcore/` | PR#320 |
| B2-A-33 | **Redis sentinel env & logvalue 缺** — 现状: sentinel 模式 env 配置不完整 + log value 缺；修复: 补 env 列表 + logvalue 透传 | bug | P2/Cx2 | 🟡 | sentinel 部署 | `cmd/corebundle/redis.go:18-22` + `adapters/redis/client.go:90-104` | backlog2 §5.3 B2-A-33 |
| PR238-FU8 | **CONFIGREPO-UPDATE-ROLLBACK-OP-LABEL-TEST-01** — 现状: `doUpdate` 通过 `op` 参数向 `scanConfigOrMapError` 传 `"Update"` 或 `"UpdateForRollback"`，`InternalMessage` 携带该 op，但 `TestConfigRepository_UpdateForRollback_NotFound` / `TestConfigRepository_UpdateForRollback` 均未断言 InternalMessage 含 `"UpdateForRollback"`，若有人把 op 硬编码回 `"Update"`，CI 不会 FAIL；修复: 相关 NotFound 测试追加 `assert.Contains(t, ec.InternalMessage, "UpdateForRollback")` | test | P3/Cx1 | 🟡 | — | `cells/configcore/internal/adapters/postgres/config_repo_test.go` | PR#238 L4 round-2 reviewer T-R4 + 029 master roadmap §errcode W4 |
| CONFIGREPO-OP-LABEL-TYPED-ENUM-HARD-01 | **op label typed enum (Hard 升级 PR238-FU8)** — 现状: PR#553 抽 `opUpdate / opUpdateForRollback` unexported const + `Update_NotFound` 双向 NotContains 锁定到 AI-rebust Medium；改字面量需改 const 单点。修复方向: doUpdate 接受 typed `updateOp` private type（2 valid values），InternalMessage 由 enum.String() 派生，硬编码 string 编译失败（违反不可表达）→ Hard | arch-opt | P3/Cx2 | 🟠 | 同模式 op-string-label-in-error-internal-message 在 ≥ 2 个其他仓储路径出现 | `cells/configcore/internal/adapters/postgres/config_repo.go` | PR#553 plan §AI-rebust evaluation |

---

## cap-10: 持久化与加密

> 主要包：`kernel/persistence` + `kernel/crypto` + `adapters/{postgres,vault}`

| ID | 描述 | Type | P/Cx | Flag | Trigger | Files | Source |
|---|---|---|---|---|---|---|---|
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
| COREBUNDLE-MAINTEST-FAIL-FAST-01 | **corebundle main_test fail-fast** — 现状: bind 错误被白名单吞掉；修复: 用 `net.Listen("tcp", "127.0.0.1:0")` 注入 + 断言关键装配里程碑 | test | Cx2 | 🟡 | — | `cmd/corebundle/main_test.go` | backlog1 §2.7 |
| B2-R-01 | **HealthListener 缺失时静默回退** — 现状: bootstrap 找不到 HealthListener 时静默回退到 main listener；修复: fail-fast 或显式 opt-in fallback | bug | P2/Cx2 | 🟡 | — | `runtime/bootstrap/bootstrap_phases.go:583-596` | backlog2 §3 B2-R-01 |
| B2-X-03 | **PG invalid index warn continue** — 现状: PG invalid index 仅 warn 继续启动；修复: 改 fail-fast 防隐藏数据完整性问题 | bug | P2/Cx2 | 🟡 | — | `cmd/corebundle/bundle.go:308-313` | backlog2 §7 B2-X-03 |
| B2-X-09 | **OUTBOX-FU-COREBUNDLE-NEGATIVE-INTEGRATION** — 现状: PR#384 N8 把 `claiming → lease_id NOT NULL` 升为 DB 级 CHECK 约束并删 `VerifyOutboxLeaseInvariant` 启动探针后，corebundle 真实 wiring 路径上不再可能产生 NULL lease residue（DB 先 fail），原计划"corebundle 负向集成测试 — NULL lease residue 真实 wiring 阻断启动"沦为不可达分支；修复: 触发条件式补集成回归 (also: cap-07) | test | P3/Cx2 | 🟠 | N3 改造 corebundle startup wiring 顺序 / 引入 cross-cluster outbox 同步路径（CHECK 不能跨 cluster 守护） | `cmd/corebundle/bundle.go` + `cmd/corebundle/consumer_base_integration_test.go` | PR#373/#374 review 二轮 won't-do 登记 + backlog2 archive §7 B2-X-09 |

---

## cap-13: 可观测性

> 详见 [`backlog/cap-13-observability.md`](backlog/cap-13-observability.md)（23 条 OPEN，按主题分 5 个 h2 子节；已完结见归档）

**子节索引**：
- [13.1 health / readyz / probe](backlog/cap-13-observability.md#13.1-health--readyz--probe)
- [13.2 audit chain observability](backlog/cap-13-observability.md#13.2-audit-chain-observability)
- [13.3 metrics / collector](backlog/cap-13-observability.md#13.3-metrics--collector)
- [13.4 slog / logging / OTel](backlog/cap-13-observability.md#13.4-slog--logging--otel)
- [13.5 adapter managed resource / 杂项](backlog/cap-13-observability.md#13.5-adapter-managed-resource--杂项)

## cap-14: 代码生成与治理工具链

> 详见 [`docs/backlog/cap-14-tooling.md`](backlog/cap-14-tooling.md)（90 条 OPEN，按主题分 6 个 h2 子节；已完结见归档）

**子节索引**：
- [14.1 archtest / typed funnel / scanner](backlog/cap-14-tooling.md#141-archtest--typed-funnel--scanner)
- [14.2 codegen / scaffold / verify](backlog/cap-14-tooling.md#142-codegen--scaffold--verify)
- [14.3 contract codegen + 兼容](backlog/cap-14-tooling.md#143-contract-codegen--兼容)
- [14.4 journey / status-board](backlog/cap-14-tooling.md#144-journey--status-board)
- [14.5 doc / ADR / NoLint / governance rules](backlog/cap-14-tooling.md#145-doc--adr--nolint--governance-rules)
- [14.6 杂项 / PR FU / T-*](backlog/cap-14-tooling.md#146-杂项--pr-fu--t-)

## cap-x-cross: 横切

> 详见 [`backlog/cap-x-cross.md`](backlog/cap-x-cross.md)（44 条 OPEN，按主题分 5 个 h2 子节；已完结见归档）

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
