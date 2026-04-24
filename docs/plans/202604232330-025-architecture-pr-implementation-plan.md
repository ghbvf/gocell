# 架构项 PR 实施计划

> 日期: 2026-04-23
> 来源: `docs/plans/docs-backlog-md-docs-reviews-2026042219-graceful-backus.md` 架构层（P1/P2/P3）+ `202604191515-auth-federated-whistle.md`（F1-F7 基石）+ `202604211245-024-auth-rebaseline-implementation-plan.md`（A/B/C）+ `202604200313-v1.0-pre-release-plan.md` 残余
> 基线: `develop @ f32d54d`（PR#222 合入后）
> 目标: 把架构层约 40 条任务 + auth/config 域剩余任务拆成 35 个内聚 PR，明确 wave 顺序、搭车关系、依赖、风险
> **工期**: 净编码 ~36 工作日（~288h）；双人并行 + buffer **~30 工作日（~6 周）**；v1.0 路径（Wave 1+2）双人 **~13-15 工作日（~3 周）**
> 2026-04-23 更新:
> - 第一轮融入：3 条搭车（PR-A5a/A5b/A6）+ 6 条新 PR（A25-A30 auth/config）
> - 第二轮修正（基于现状复核）：F3/F6 非"已完工"而是"基础设施完工+应用层仍有过渡态"；PR-A5a A5 lifecycle 迁移从 0.5h 修正为 2-3h；PR-A14 拆分为 A14a MIN（Wave 1 必做）+ A14b FULL（Wave 3）；新增 PR-A27 CONFIGWRITE-RETURNING / PR-A28 CONFIG-DOCS / PR-A29 AUTH-REFRESH-MAIN（X11+X15 上提 Wave 2 必做）/ PR-A32 SELECTOR-CLOSURE / PR-A33 REFRESH-OPAQUE-POLISH
> - 第三轮修正：工期从虚高"95 工作日 / v1.0 路径 40-45d"校正为**净编码 36d / v1.0 双人 ~3 周**
> - 识别已完工基石：F1 JWT Registry / F5 Errcode Classifier / F7 Principal API / L10 / S42 / F2 PG RefreshStore（详见末尾"已完工基石声明"）
>
> 2026-04-24 更新（第五轮 · PR-A5a delivered）:
> - **PR-A5a 已落地**（分支 `refactor/513-pr-a5a-lifecycle-autodiscovery`，PR #234）。实际工期 ~10h（vs 原估 6-7h），因升级为**彻底方案**：
>   - V-A15 cell.go 拆分：`cells/accesscore/cell.go` 625 → 173 行，新建 `cell_init.go`(189) + `cell_routes.go`(112)
>   - V-A16：RunnerOrNoop 已由 PR #224 落地；本 PR 顺手删 identitymanage/rbacassign 残留 `runInTx()` 死层 wrapper
>   - A5：`WithBootstrapWorkerSink` / `bootstrapWorkerSink` / `adminBootstrapWorkerOpts` / `worker.Lazy()` 彻底删除
>   - **超出原范围的优雅升级**（用户指示"方案要彻底"）：
>     1. 新增 `kernel/cell.LifecycleContributor` 接口 + `runtime/bootstrap` phase3b 自动发现，镜像既有 HealthContributor 模式，消灭 composition root 手写 `bootstrap.WithLifecycle` boilerplate
>     2. 新增 `kernel/cell.ResolveEmitter` 抽取三 cell（accesscore/configcore/auditcore）重复的 durability-mode emitter 解析逻辑（-145 行）
>     3. `cells/accesscore/internal/initialadmin/` 搬出 `internal/` 到 `cells/accesscore/initialadmin/`，成为一等公开 subpackage（类比 slices/），新增 `Lifecycle` 编排类型
> - **对下游 PR 的影响**：PR-A5b（configcore 拆分）现已复用 `cell.ResolveEmitter`（无需再重复抽），只剩 cell.go 物理拆分；PR-A5c OUTBOX-EMITTER-UNIFY 的 Emitter 抽象已大部分由 PR #224 落地，剩余工作被本 PR 的 `ResolveEmitter` + PR-A5a 的模式间接推进
>
> 2026-04-24 更新（第六轮 · PR-A5a review 尾巴清零）:
> - **6 角色 review 合计 ~30+ findings**（doc-engineer / kernel-guardian / architect / product-manager / devops / reviewer），PR 交付批已通过 commits `45777dd` / `83c3c62` / `8a5352a` 修掉 P0/P1。
> - 本轮追加 **4 个 fold-in commit 关闭 5 条 P2/P3 尾巴**（`3ae8645..c4133f5`）：
>   1. `3ae8645` docs(initialadmin,kernel): 一致性级别 L1 godoc + LifecycleHook 顺序语义（architect P2 #7+#8，R6+R7）
>   2. `2d2bf31` obs(bootstrap): `Hook.CellID` + phase3b stamp + `slog.String("cell", …)` + drift guard 放宽（devops #3，R8）。对标 fx `callerFrame` + k8s kubelet `containerName/pod` 两独立字段模式，kernel 侧 `LifecycleHook` 故意不镜像 `CellID`（注册方身份不由 cell 自声明）
>   3. `433e2ad` refactor(accesscore/initialadmin): 导出面从 ~25 缩到 ~20，`Bootstrapper/Cleaner/Sweep/WriteCredentialFile/...` 全 lowercase；4 个 external-test 文件（`package initialadmin_test`）迁内部白盒（kernel-guardian G3，R1）
>   4. `c4133f5` test(archtest): LAYER-06 + `cellOwnedSubpackages` 表，守 cell-owned public subpkg 的跨 cell 导入（kernel-guardian G4，R2）
> - **移交 PR-A5b 的 follow-up（R4/R5）**：architect P2 #4 DirectPublishMode helper 下沉（`cell.DirectPublishModeForDurability`）+ P2 #5 `cell_routes.go` providers 子拆。两项都落在 configcore 拆分的自然范围内，移下去更省评审成本；R4 实施后三 cell 统一语义、A5a 的硬编码 FailOpen/FailClosed 也顺手收口
> - **登记 backlog（R3）**：`A5a-R3 ACCESSCORE-INITIALADMIN-THIN-WRAPPER-01` 🟡 **评估后可能 won't-do**（PM + architect review 一致倾向保持现状）

---

## 设计原则

1. **文件亲缘**：同目录或同模块的修改塞进同一 PR，降低 review 成本
2. **语义内聚**：按"治理规则"、"Auth 收口"、"Contract spec"等单一主题切分
3. **抽取先于业务**：先落 helper / 新接口，再把业务切换过去（V-A14 adapterutil 先于 V-A15 cell 拆分用到）
4. **Cx3 独立审**：高复杂度（CONTRACT-META-01、kernel/wrapper、INTERNAL-LISTENER）独立 PR，防互相污染 review
5. **风险由低到高**：pkg helper / CI 治理 → 业务 cell 拆分 → 协议级改造（ER-ARCH-01、Subscriber Setup/Run）

---

## PR 切分总览

35 个 PR 分 4 Wave，**净编码合计 ~36 工作日**（288h），含 P3 长期架构。

| Wave | 目标 | PR 数 | 净编码 | 含 buffer（单人/双人） |
|---|---|---|---|---|
| **Wave 1** — 低风险抽取 + 治理 + auth v1.0 必做 + config 样板收敛 + INTERNAL-LISTENER-MIN | 为后续业务改造铺平基础 + 发布硬约束 | 14 | 9.3d | 13d / 6-7d（三路 worktree 并行） |
| **Wave 2** — 中等架构收口 + auth refresh 主链 + auth 测试 + DX | Contract 模型 / kernel 新模块 + refresh opaque 主链（X11+X15）+ auth 收尾 | 8 | 8.5d | 12d / 7-8d（A9 Cx3 是瓶颈） |
| **Wave 3** — P2 架构延展 + INTERNAL-LISTENER-FULL + F3 Selector 收尾 | v1.1 kernel 子模块 + listener 完整版 | 6 | 8.75d | 12d / 7d |
| **Wave 4** — P3 长期架构演进 + refresh opaque 收尾 | 分层重整 / 接口拆分 / 类型保护 / refresh polish | 7 | 9.5d | 13d / 8d |
| **合计** | | 35 | **~36d** | **~50d / ~30d** |

> **v1.0 路径（Wave 1 + Wave 2）**：净编码 ~18d；含 buffer **单人 ~25d（~5 周）/ 双人并行 ~13-15d（~3 周）**。
>
> Buffer 1.4x 乘数含 review 往返、integration 调试、Cx3/Cx4 ADR 讨论。实际 PR 编码时间通常只占项目时间 60-70%。

> **⚠️ 发布前必做（🔴 硬约束）**：PR-A25 / PR-A26 / PR-A14a / PR-A27 / PR-A28（Wave 1 auth+config v1.0 必做）+ PR-A29（Wave 2 refresh 主链）+ PR-A5a 修正（initialadmin Lifecycle 迁移，~2-3h 不是 0.5h）。
> **🟡 已完工基石（不占 Wave 计划）**：F1 JWT Registry / F3 Selector 基础设施 / F5 Errcode Classifier / F6 Lifecycle 基础设施 / L10 RoleInternalAdmin / S42 ROLELIST-CURSOR。详见末尾"已完工基石声明"章节。

---

## Wave 1 — 低风险抽取 + 治理（~5 工作日）

> 先落这批：纯 helper 抽取 + governance 规则扩展 + 入口缩减，review 快、冲突小、可并行 worktree。

### PR-A1 治理规则 + CI 门禁打底 — ✅ 已完工

**实况摘要**：探索阶段发现主线 4 条中有 3 条已在源码层实现（parser `KnownFields(true)` + TOPO-07/08 `SeverityError`）。实际落地为**零新治理规则代码 + 一套回归测试 + 一组可复用 CI workflow**。

**主线**（含实际做法）：
- **G-1 FMT-11 DYNAMIC-FIELD-ISOLATION-01** ✅ — `kernel/metadata/parser.go:414` `dec2.KnownFields(true)` 已解析期拒绝；新增 `kernel/metadata/parser_strict_test.go`（7 dynamic × 5 file types = 35 rejection + 3 status-board 接受回归）
- **G-2 TOPO-07 MAXCONSISTENCYLEVEL-ENFORCE** ✅ — `kernel/governance/rules_topo.go:273-293` 已 `SeverityError`；新增 `TestTOPO07_EnforcesMaxConsistencyLevel`（3 cases）
- **G-4 DEPRECATED-CONTRACT-BREAK** ✅ — `rules_topo.go:323-324` 已 `SeverityError, IssueForbidden`；新增 `TestTOPO08_BlocksDeprecatedReference`（2 cases）
- **V-A11 GOVERNANCE-EXAMPLES-COVERAGE** ✅ — parser `fs.WalkDir(".", ...)` 已自然覆盖 examples/**；新增 `TestProjectWalksExamples` 固化；放弃原计划新增 `rules_examples.go`；**放弃原 `--root=examples/*` matrix CI**（examples 引用根 `actors.yaml`，standalone `gocell validate` 会报 5-10 REF-14 错误）

**搭车**（含实际做法）：
- **L11 GOVERNANCE-CI-MAINBRANCH-01** ✅ — governance.yml 触发扩 `[develop, main, 'release/**']`
- **PR220-4 CI-LINT-EVENT-SEMANTIC-SPLIT-01** ✅ — 采用 reusable `workflow_call` 模式：新建 `_build-lint.yml`，`ci.yml`（push 触发，full lint）+ 新建 `pr-check.yml`（pull_request 触发，`--new-from-merge-base`），ci.yml 净瘦身 175→93 行，零重复

**搭车转移**：
- **PR220-2 DOC-NAMING-GUARD-01** → **转移到 PR-A13**：baseline 试跑发现 develop HEAD 有 109 处 kebab 硬编码（capability-inventory.md / capability-map.md / master-plan.md / roadmap/* / examples READMEs / templates/adr.md），同时 PR-A13 文档事实源重写本就要处理这批文件，合并处理更经济。架构 plan 本节原注（"需先完成 PR220-1 / PR220-e1 文档收敛"）已印证。

**文件面**：`kernel/metadata/parser_strict_test.go`（新） + `kernel/governance/validate_test.go`（扩） + `.github/workflows/_build-lint.yml`（新） + `pr-check.yml`（新） + `ci.yml` + `governance.yml` + `CLAUDE.md`（补注 parser 强制） + backlog 关单。

**实际工时**：~5h（比计划 10h 少一半，因 3 条主线已实现，只需补回归测试 + CI 重构）。

---

### PR-A2 pkg 共享 helper 三连 ✅ 已落地 PR #508（实际净编码 5-6h）

> **实际范围修正**：探索发现 L8 / A7 已在仓库内完工，本 PR 实际只做 V-A5 + V-A14 + backlog 状态回灌。

**实际主线**：
- **V-A5 VALIDATION-HELPER-EXTRACT-01** ✅ `pkg/validation/validation.go` `NamedValue` + `F()` + variadic `RequireNotBlank`；26 处 service 层 blank-check 迁移（runtime/auth 语义不同站点 + sessionvalidate JWT claim + auditappend fallback 共 3 类站点不适用）
- **V-A14 ADAPTER-CLOSE-HELPER-01** ✅ `adapters/adapterutil/close.go` `CloseWithDeadline`（吸收 slog 日志，归一为 `<name>: closed` / `<name>: close budget exceeded`）；5 adapter 迁移（postgres/redis/rabbitmq×3）
- **L8 PAGINATION-HELPER-EXTRACT-01** ✅ pre-existing — `pkg/httputil/pagination.go:13` `ParsePageParamsOrWrite` 已存在并被 handler 消费
- **A7 POOLSTATS-IFACE-01** ✅ pre-existing — `runtime/observability/poolstats/statter.go:50` `Statter` + `Snapshot` 已统一，三 adapter 实现 + OTel collector 消费

**搭车实际**：runtime/auth/*.go 多数 `token == ""` 站点语义是"无凭证透传"或 authz 断言，**不适用** validation helper（保持原样）；helper 主战场是 cells/*/slices/*/service.go。

**文件面**：`pkg/validation/`（新） + `adapters/adapterutil/`（新） + `adapters/{postgres,redis,rabbitmq}/` + `cells/{accesscore,configcore}/slices/*/service.go`

**风险**：低；helper 是净增 API，被迁移的代码点是调用方样板替换；adapter Close 公共签名 `Close(ctx) error` 未变。日志消息归一是破坏性改动（旧 `"postgres pool closed"` → 新 `"postgres: closed"`），按"不保留向后兼容"原则接受。

---

### PR-A3 入口收口 + per-cell adapter（实际 ~10h，PR #227，2026-04-24 合并）

**主线**：
- **V-A8 CMD-THICK-ENTRY-REDUCE-01**（P1-13 PARTIALLY）`cmd/corebundle/main.go` 继续缩减（2h → 实际 main.go 423→95 行，5 个 helper 文件）
- **T6 GOCELL-PER-CELL-ADAPTER-01** 全局 env 拆单 cell adapter 配置（2h，PR-X-PG-REPO-ACCESS 强制前置）

**搭车理由**：同在 `cmd/corebundle/` 下，wiring 逻辑耦合。T6 完成后 main.go 体量自然进一步下降。

**六席位 review 追加修复（~4h）**：
- **F1 BUILDAPP-CLEANUP-ON-FAILURE-01**（P1 correctness）`CellModule.Provide` 扩签名返回 `[]ManagedResource` 作为 provisional；`BuildApp` 任一模块失败时逆序 `Close(ctx)` 已产出资源，防启动失败泄漏 PG pool / vault client
- **F2 PREV-MASTER-KEY-DEMO-GUARD-01**（P1 security）`buildKeyProvider` 对 `GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS` 补相同的 `rejectDemoKey` 检查（历史 key 仍是活跃 decrypt 路径）
- **F3 DOC-PG-CELL-TEMPLATE-REWRITE-01**（P1 DX）原 PR-A28 工作，彻底重写 `docs/patterns/pg-cell-template.md` 为新模型（355 行），删除所有 `AppDepsFromEnv` / `BuildBootstrap` / `AppDeps.PGResource` / `configCellOpts` 旧 API 引用
- **F4 LOADPGCONFIG-FAIL-FAST-01**（P2 ops）`LoadPGConfig` 返回 `(Config, error)`，坏 `MAX_CONNS` / `IDLE_TIMEOUT` / `MAX_LIFETIME` 值带 env 名 + 实际值 fail-fast
- **F5 BUILDAPP-ENV-INTEGRATION-TEST-01**（P2 testing）新建 `cmd/corebundle/buildapp_env_integration_test.go` 走完整 `t.Setenv → LoadSharedDepsFromEnv → BuildApp → ConfigCoreModule.Provide` 路径（含 testcontainers PG）

**文件面**：`cmd/corebundle/` + `adapters/postgres/pool.go` + `runtime/crypto/local_aes_provider.go` + `.env.example` + `docs/ops/env-vars.md` + `docs/patterns/pg-cell-template.md` + `docs/guides/integration-testing.md`

**遗留开放项（不阻塞本 PR）**：
- S4b VAULT-TOKEN-STATIC-REAL-GUARD-01 (real 模式接受静态 `VAULT_TOKEN` 路径) — 已在 backlog P1 安全章节，交 PR-A8 Vault auth 批量处理
- Vault 相关 env 命名未按 per-cell 约定 namespace（本 PR T6 未动）—— 交 PR-A8 / PR-A18 Vault 专项

**风险**：低；wiring 重排。

---

### PR-A4 运行时可观测收口（预计 6h）

**主线**：
- **R2 OBS-HTTP-COLLECTOR-AUTOWIRE-01** `WithMetricsProvider` 自动构造默认 HTTP collector（2h）
- **A21 HEALTH-CHECKER-CTX-BUDGET-01** `Checker` 升级 `func(ctx) error` + 统一 deadline + 并行（3h）

**搭车**：
- **R3 OB-02** safe_observe broken logger 注入测试（1h）

**搭车理由**：同在 `runtime/http/{middleware,health}` 面，middleware/health 是同一 request 生命周期。

**文件面**：`runtime/bootstrap/bootstrap.go` + `runtime/http/middleware/` + `runtime/http/health/` + `kernel/lifecycle/managed_resource.go`

**风险**：中；`Checker` 签名升级涉及所有 `Checkers()` 实现，需同 PR 同步改 adapters + cells 的所有实现。

---

### PR-A5a accesscore cell.go 拆分 + TxRunner helper + initialadmin lifecycle 迁移（✅ **已交付 @ 2026-04-24 via PR #234** / 分支 `refactor/513-pr-a5a-lifecycle-autodiscovery`）

**主线**：
- **V-A15 CELL-GO-SPLIT-01**（P2-7）accesscore/cell.go 582 行拆 `cell_routes.go` + `cell_events.go` + `cell_lifecycle.go`（2h）

**搭车**：
- **V-A16 RUN-IN-TX-HELPER-01**（P2-8）`kernel/persistence.TxRunner` 加 helper（2h）
- **A5 AUTH-INITIALADMIN-LIFECYCLE-MIGRATE-01**（auth-federated F6 应用层 + auth-rebaseline A5）**完整迁移**：删除 `accesscore.WithBootstrapWorkerSink` option + `bootstrapWorkerSink` 字段 + `runInitialAdminBootstrap` 的 worker sink 分支；`initialadmin.Sweep` + `Bootstrapper.EnsureAdmin` 改为注册 `bootstrap.Lifecycle.Hook`（`OnStart` 做 sweep+ensure，`OnStop` 做最终 cleanup）；同步更新 `examples/ssobff/` + `examples/*/` 所有 assembly 入口（2-3h）

**搭车理由**：
- V-A16：accesscore 拆分时会重构多个调用 `RunInTx` 的地方，helper 抽出与 cell 拆分同步替换
- A5：sweep + EnsureAdmin 整段代码从 `cell.go` 迁到 `cell_lifecycle.go`；lifecycle hook 注册属于生命周期面，与 V-A15 拆分目标高度一致；deleting `WithBootstrapWorkerSink` 是打破 worker sink 间接层的唯一时机（不搭车就要再开独立 PR touch 同区）

**文件面**：`cells/accesscore/cell*.go` + `cells/accesscore/internal/initialadmin/*.go` + `kernel/persistence/` + `examples/*/`（assembly 入口适配）

**风险**：中-高；`WithBootstrapWorkerSink` 是 public API，examples 全量适配；PR 应附带 migration note。

---

### PR-A5b configcore cell.go 拆分 + config_repo 错误归类（预计 3h → 5-6h 随 A5a review 尾巴并入）

**主线**：
- **V-A15 CELL-GO-SPLIT-01**（P2-7）configcore/cell.go 431 行拆 `cell_routes.go` + `cell_events.go` + `cell_lifecycle.go`（2h）

**搭车**：
- **S15 ERROR-CTX-CANCELLED-CLASSIFY-01** `cells/configcore/internal/adapters/postgres/config_repo.go` `ctx.Canceled` 归类用 `errcode.IsInfraError`，消除 domain-notfound 误判（1h）
- **A5a-R4 DIRECTPUBLISHMODE-HELPER-DOWNSTREAM-01** (Cx3, 从 PR-A5a review architect P2 #4 移交)：三 cell 共享 "demo=FailOpen / durable=FailClosed" 语义但 configcore 用 `configDirectPublishMode(...)` 翻译、accesscore/auditcore 在本 PR-A5a 硬编码 FailOpen/FailClosed（见 `cells/accesscore/cell_init.go:30-38` + `cells/auditcore/cell.go:131-139`）。**修复**：下沉 `cell.DirectPublishModeForDurability(mode, demoPolicy, durablePolicy)` helper，三 cell 统一调用。PR-A5b 拆 configcore 时自然 touch 同一块翻译逻辑，合并处理比独立 PR 省 ~2h 评审成本（1-2h）
- **A5a-R5 CELL-ROUTES-PROVIDERS-SPLIT-01** (Cx3, 从 PR-A5a review architect P2 #5 移交)：PR-A5a 的 `cells/accesscore/cell_routes.go` 仍混放 providers 构造与路由注册；PR-A5b configcore 拆分时对称处理两 cell 的 providers 独立文件，保证风格一致（2h）

**搭车理由**：同 configcore 包；S15 改的是 repo 层错误分支，会 touch `cell_events.go` 或 `cell_lifecycle.go` 里的事件订阅/日志路径。F5 Errcode Classifier 已完工（`pkg/errcode/classify.go`），应用零阻塞。A5a-R4/R5 属于 accesscore+configcore+auditcore 三 cell 对称收口，在 configcore 拆分的自然范围内做最省评审。

**文件面**：`cells/configcore/cell*.go` + `cells/configcore/internal/adapters/postgres/config_repo.go` + `cells/accesscore/cell_{init,routes}.go` + `cells/auditcore/cell.go` + `kernel/cell/mode_resolver.go`

**依赖**：PR-A5a 落地后再做，复用其 TxRunner helper + `ResolveEmitter`。

---

### PR-A6 EventRouter 身份拆分 + typed event payload + marshal err 显式（预计 7h）

**主线**：
- **PR220-5 EVENTROUTER-SUBSCRIPTION-IDENTITY-SPLIT-01** `ConsumerGroup`（broker）与 `CellID`（observability）拆两字段（3h）

**搭车**：
- **S4 EVENT-PAYLOAD-TYPED-01** 6 event 的 `map[string]any` → typed struct（3h）
- **S41 MARSHAL-ERR-EXPLICIT-01** `sessionlogin/service.go:140` + `sessionlogout/service.go:90` 两处 `_, _ = json.Marshal(...)` 改为显式处理（1h）

**搭车理由**：都触及 subscription 注册面 + event payload contract；S4 改事件 schema 时 EventRouter 接口同时用到；S41 正好是 sessionlogin/logout 的事件发布路径，S4 把 `map[string]any` 改 typed struct 时原地消除 `_, _ =`。

**文件面**：`runtime/eventrouter/` + `kernel/outbox/` + `cells/*/cell.go` + 6 个 event `service.go` + event contract schemas

**风险**：中；broker queue 命名和 observability label 解耦，现有测试需同步跑通。

---

### PR-A7 Principal 契约统一 + rolefetch 收口（预计 6h）

**主线**：
- **P1-A PRINCIPAL-UNIFIED-CONTRACT-01** Principal 契约（definition + accessor + ctx 挂载）统一（4h）

**搭车**：
- **V-A17 FETCH-ROLE-NAMES-DEDUP-01**（P2-9）抽 `cells/accesscore/internal/rolefetch`，`FetchRolesStrict`/`FetchRolesLenient`（2h）

**搭车理由**：Principal 统一时会重写 `sessionlogin:162 fail-closed` / `sessionrefresh:199 fail-open` 的角色查询路径，rolefetch 拆分同步完成。

**文件面**：`runtime/auth/` + 各 cell middleware + `cells/accesscore/internal/rolefetch/`（新）

**风险**：中；auth 关键路径，需 integration test 全覆盖。

---

### PR-A8 Vault auth 批量（预计 5h，含安全项）

**主线**：
- **A14 VAULT-AUTH-PLUGGABLE-01** AppRole / K8s auth 可插拔（3h）

**搭车**：
- **S4b VAULT-TOKEN-STATIC-REAL-GUARD-01**（P1 安全 🟠）real 模式 + static token → `ErrVaultAuthFailed`（1h）
- **VAULT-RENEWAL-DEGRADATION-GAUGE** 静默降级指标（1h，已在 A14 描述内合并）

**搭车理由**：同文件 `adapters/vault/transit_provider.go`，AppRole 接入后 S4b guard 自动生效，degradation gauge 也在同一 init 路径。

**文件面**：`adapters/vault/transit_provider.go`

**风险**：中；生产 auth 链路变化，需 HCP/Enterprise 环境手验。

---

### PR-A14a INTERNAL-LISTENER-MIN（🔴 发布前必做，~7h；**彻底重构版，吸收 PR-A32**）

**实际主线**（物理双 mux + 吸收 PR-A32 F3-CLOSURE）：
- **R4-MIN DUAL-PHYSICAL-MUX** `runtime/http/router/router.go` 从「单 mux + prefix-guard 中间件」重构为 `publicMux + internalMux` 物理双 mux；`Route/Handle/Mount` 按 pattern 前缀自动分流；新增 `Router.PublicHandler()` / `InternalHandler()`；新增 `WithInternalMiddleware(mw ...)`；outerMux 显式 404 `/internal/v1/*`（primary listener 边缘隔离）
- **R4-MIN DUAL-SERVER** `runtime/bootstrap/bootstrap_phases.go::phase7StartHTTPServer` 启动 2 个 `http.Server`（primary + internal），pre-bind 两 listener 同步 fail-fast，parallel shutdown via errgroup
- **R4-MIN CONSISTENCY-ASSERTION** `FinalizeAuth` 启动期断言 `Delegated: true` ⇔ `/internal/v1/*`
- **PR-A32 吸收**：`bootstrap.WithInternalEndpointGuard(prefix, guard)` / `router.WithInternalPathPrefixGuard` / `auth.WithDelegatedMatcher` / `authDelegatedMatcher` 全部删除；F3-CLOSURE 已完成

**破坏性变更**（CLAUDE.md「Review 和重构时不考虑向后兼容」认可）：
- `bootstrap.WithHTTPAddr` → 删除；新 `WithHTTPPrimaryAddr` + `WithHTTPInternalAddr`
- `bootstrap.WithListener` → 删除；新 `WithPrimaryListener` + `WithInternalListener`
- `bootstrap.WithInternalEndpointGuard(prefix, guard)` → `WithInternalMiddleware(mw)`（无 prefix 参数）
- `auth.RouteDecl.Delegated` 职责改：从「驱动 JWT matcher」变为「`/internal/v1/*` 一致性标记」，由 FinalizeAuth 做启动期校验

**文件面**：`runtime/http/router/router.go` + `runtime/bootstrap/{bootstrap,bootstrap_phases}.go` + `runtime/auth/{middleware,options}.go` + `cmd/corebundle/{bundle,shared_deps}.go` + 全部测试迁移 + `docs/ops/env-vars.md` + `.claude/rules/gocell/runtime-api.md` + 示例 README

**依赖**：无

**风险**：中；签名破坏性变更，全部调用方（cell tests / corebundle tests / examples）同步迁移完成，全仓库 `go test ./... -race` + `golangci-lint run` 0 issues 通过。

---

### PR-A25 AUTH-PROD-HARDENING（🟠 real 模式前触发，~4h）

**主线**：
- **S-nonce SERVICE-TOKEN-NONCE-STORE-ENFORCE-01** real 模式强制 `WithNonceStore` + 缺失即启动失败（3h）
- **S32 CONTROLPLANE-TOKEN-PROD-GATE-01** real 模式断言 service-token/mTLS 至少一项（1h）

**搭车理由**：两项都是 real 模式 fail-fast，在 `cmd/corebundle/main.go` + `runtime/auth/authenticator.go` 同一面；S32 的 CI real-mode smoke 也顺便加

**文件面**：`runtime/auth/authenticator.go` + `cmd/corebundle/main.go` + `cmd/corebundle/shared_deps.go`

**风险**：低-中；CI smoke 要新建 real 模式 job

---

### PR-A26 AUTH-SETUP-ENDPOINT（🔴 v1.0 必做 P0，~4h）

**主线**：
- **P1-19 AUTH-SETUP-ENDPOINT-01**：
  - ① 新 slice `cells/accesscore/slices/setup/`
  - ② 新 contract `contracts/http/auth/setup/status/v1/` + `contracts/http/auth/setup/admin/v1/`
  - ③ `GET /api/v1/setup/status`（Public）返回 `{hasAdmin: bool}`
  - ④ `POST /api/v1/setup/admin`（Public）无 admin 时创建，已有则 409
  - ⑤ 两端点 `auth.Declare Public: true`

**搭车**：无（纯新建 slice，独立 blast radius）

**文件面**：新 `cells/accesscore/slices/setup/` + 新 `contracts/http/auth/setup/`

**风险**：低；新功能，现有测试不动

---

### PR-A27 CONFIGWRITE-RETURNING-CONSOLIDATE（🔴 发布前必做，~5h）

**主线**：
- **CONFIGWRITE-RETURNING-01** `cells/configcore/slices/configwrite/service.go` Create/Update/Delete 三方法按 `flagwrite` PR#216 模式重写，改原子 `RETURNING` 消除 TOCTOU：
  - `Update` 改 `repo.Update(ctx, key, value) (*ConfigEntry, error)`（单 SQL RETURNING，消除事务外 `GetByKey` 预读）
  - `Delete` 改 `repo.Delete(ctx, key) (deleted *ConfigEntry, error)`（RETURNING 老值用于 outbox 发布）
  - `Create` 保持 INSERT RETURNING
- **搭车**：`configpublish` 同 TOCTOU 修复（如适用）
- 同步更新 `config_repo.go` + repo interface + 契约测试

**搭车理由**：configwrite 样板债是"其他 cell 若复制 config-core 写法会带坑"，发布前必须收敛到 flagwrite 统一原子模式

**文件面**：`cells/configcore/slices/configwrite/service.go` + `cells/configcore/slices/configpublish/service.go` + `cells/configcore/internal/ports/config_repo.go` + `cells/configcore/internal/adapters/postgres/config_repo.go`

**风险**：中；repo interface 签名变化，需全量 test 跑通

---

### PR-A28 CONFIG-DOCS-REWRITE（🟢 主体已吸收进 PR-A3，~1-2h 残余）

**状态**：主体 `DOC-PG-CELL-TEMPLATE-REWRITE-01` 已在 PR-A3（PR #227，2026-04-24）彻底重写完成（`docs/patterns/pg-cell-template.md` 全量 rewrite 为 SharedDeps + CellModule + BuildApp + LoadPGConfig 新模型，355 行）。触发原因：PR-A3 T6 六席位 review 的 P1-3 findings（模板仍教已删除的 `AppDepsFromEnv` / `BuildBootstrap` 模型）。

**残余（可选，本 PR 未做）**：
- **DOC-CONFIG-ENCRYPTION-APPENDIX-01** 把加密/stale cipher/AAD/migration 010 forward-only 从通用模板剥离到 `docs/patterns/config-core-encryption-appendix.md`（新）。当前 `pg-cell-template.md` 已精简为通用 PG cell 接入指南，不再混入 configcore 加密专项内容，但也没有专门附录可指。**低优先级**，观察到实际读者困惑再动。

**搭车**：无

**文件面**：`docs/patterns/config-core-encryption-appendix.md`（新，可选）

**风险**：低（纯文档）

---

## Wave 2 — 中等架构收口（~12 工作日）

### PR-A9 CONTRACT-META-01 传输层一等公民（Cx3，~3d）

**主线**：
- **LATER-SD-1 CONTRACT-META-01**（P1）`contract.yaml` 补 `Method / Path / PathParams / QueryParams / SuccessStatus / NoContent` 静态界定

**搭车**：
- **L7-FMT15b CONFIG-GET-DUAL-MODE-SPLIT-01** 拆 `contracts/http/config/get/v1` oneOf 合并（2h）
- **S2-follow CONTRACT-ERROR-SCHEMA-EXTEND-01** 其余 HTTP contract 补 401/403（2h）

**搭车理由**：都在 `contract.yaml` 结构面；CONTRACT-META-01 改 metadata parser 时顺便把 config/get 合约 + 错误响应格式一起处理。

**文件面**：`kernel/metadata/` + `kernel/governance/` + 所有 `contracts/http/**/contract.yaml`

**风险**：高；全仓 contract.yaml 都要升级；需 codegen 同步。建议独立 PR，Cx3 人工决策先输出方案再干。

---

### PR-A10 OUTPUT-JSON-SARIF 诊断模型（~6h）

**主线**：
- **P1-4 OUTPUT-JSON-SARIF-01** 统一诊断模型（单一 `Issue` struct → text/JSON/SARIF 三 printer）

**搭车**：无（独立 refactor，不挂 review 其他内容）

**文件面**：`cmd/gocell/` + `kernel/governance/` 序列化

**风险**：低-中；输出格式改动，需保证 legacy text 输出兼容 CI consumer。

---

### PR-A11 KERNEL/WRAPPER（P1，~1d）

**主线**：
- **LATER-K1 KERNEL/WRAPPER** 契约级可观测代理（Traced wrapper）

**搭车**：无（独立新模块）

**文件面**：`kernel/wrapper/`（新）

**依赖**：PR-A9 CONTRACT-META-01 落地后，wrapper 能拿到完整 Method/Path 信息做 trace span 标注。

**风险**：中；Tracing 埋点语义定义需 ADR。

---

### PR-A12 KERNEL/COMMAND（P1，~1d）

**主线**：
- **LATER-K2 KERNEL/COMMAND** 命令队列接口 + L4 操作底座

**搭车**：
- 可考虑 devicecell 的 HandleEnqueue 迁到新底座（**T3 DEVICE-ENQUEUE-RBAC** 触发器项 0/6 的预埋点）

**文件面**：`kernel/command/`（新）+ `cells/devicecell/slices/devicecommand/`

**风险**：中；定义 L4 下发语义，影响未来设备管理类 cell 设计。

---

### PR-A13 PR#220 遗留：文档事实源重写 + DOC-NAMING-GUARD 启用（~6h）

**主线**（问题层，但从 PR220 拆分报告推荐放此顺序）：
- **PR220-1 DOC-CAPABILITY-INVENTORY-REWRITE-01** 按真实 route 重写 `capability-inventory.md` + 其他活动文档
- **PR220-1b DOC-IOTDEVICE-README-ENVELOPE-01** iotdevice 响应补 `data` 包装
- **PR220-e1 NAMING-BASELINE-CONTRADICTION-01** baseline 自身矛盾修正
- **PR220-e3 STATUS-BOARD-J-ORDERCREATE-01** status-board 补条目 + checkRef
- **PR220-2 DOC-NAMING-GUARD-01**（由 PR-A1 下沉此处，~2h）迁入 `worktrees/501-naming-no-dash` 的 `naming-guard.yaml`（58 禁字面量）+ `naming_docs_test.go` 到 `kernel/governance/`；PR-A1 baseline 探测 109 处 hits 分布于本 PR 本就要改的核心文档，合并处理更经济

**搭车理由**：都是文档事实源漂移一次性扫清；本 PR 清完后 naming-guard 可直接启用，无需分两步。

**文件面**：`docs/design/*.md` + `docs/architecture/*.md` + `examples/iotdevice/README.md` + `journeys/*.yaml` + `docs/architecture/naming-guard.yaml`（迁入）+ `kernel/governance/naming_docs_test.go`（新）

**执行顺序**：先清文档（PR220-1/1b/e1/e3）→ 跑 `go test ./kernel/governance/ -run TestActiveDocsAndTemplates_NoLegacyNamingExamples` 0 hit → 迁入 yaml+test → 提交。

---

### PR-A29 AUTH-REFRESH-MAIN（🔴 发布前必做，X11 + X15，~10h）

**主线**（按**强依赖顺序**执行，两阶段一个 PR 内完成或拆两个子 PR 串行）：
- **X11 REFRESH-HMAC-SPLIT-01**（4h）`adapters/postgres/refresh_store.go` 改 token 存储为 `selector|verifier` 两段式：
  - DB 存 `selector TEXT` 明文 + `verifier_hash TEXT`（SHA-256(verifier)）
  - `Issue` 生成 `selector(16B) + "." + verifier(32B)` base64url 字符串
  - `Rotate` CAS 条件改 `selector = $1 AND verifier_hash = sha256($2)`
  - Migration `009_refresh_tokens_hmac_split.sql`（新列 + 回填 NULL）
  - ref: Hydra / Zitadel
- **X15 REFRESH-OPAQUE-INTEGRATION-01**（6h）`sessionlogin/service.go` + `sessionrefresh/service.go` 切换主链：
  - 不再走 JWT refresh 旋转，改调 `refresh.Store.Issue/Rotate/Revoke`
  - 返回给客户端的 refresh token 从 JWT string → opaque `selector.verifier`
  - 移除旧 `GetByPreviousRefreshToken` 应用层 reuse 检测（store CAS 内聚）
  - 对外 contract schema 更新（token length 变化）
  - `cmd/corebundle/access_module.go` wiring 切换

**硬依赖**：**X11 必须在 X15 之前**——X15 明文 token 入库后再改 HMAC-split 格式需要数据迁移

**搭车**：无（两步已是一个主题，不再搭其他）

**文件面**：`adapters/postgres/refresh_store.go` + migration 009 + `runtime/auth/refresh/` + `cells/accesscore/slices/sessionlogin/service.go` + `cells/accesscore/slices/sessionrefresh/service.go` + `cells/accesscore/access_module.go` + `cmd/corebundle/`

**风险**：高；主链重构 + CAS SQL + 对外 token 格式变化；必须 `go test -race -tags=integration` 全通过

---

### PR-A30 AUTH-TEST-COVERAGE（~6h）

**主线**：
- **S19 JWT-AUDIENCE-DRIFT-INTEG-TEST-01** 真实 sessionlogin 路径 audience drift 检测（2h）
- **S21 JWT-AUD-TEST-TABLE-DRIVEN-01** `runtime/auth/jwt_aud_test.go` 9 场景改 table-driven（1h）
- **S22 REFRESH-AUD-REAL-ROUTE-TEST-01** 真实 HTTP refresh wrong/missing aud 集测（2h）
- **S24 AUTH-MIDDLEWARE-AUD-REFRESH-E2E-01** `httptest.NewServer` + 真实 `AuthMiddleware` e2e（1h）

**搭车**：无

**搭车理由**：全是 auth 回归测试，F1 / F7 已完工后本批测试阻力最小；独立 PR 便于 review

**文件面**：`runtime/auth/jwt_aud_test.go` + `runtime/auth/middleware_aud_test.go` + `cells/accesscore/auth_integration_test.go` + `cmd/corebundle/`

**依赖**：PR-A29 完成（refresh 主链切换后测试用例需对齐新 opaque path）

---

### PR-A31 AUTH-FIRSTRUN-DX（~2h）

**主线**：
- **C2-A LOGIN-USERID-RESPONSE** `sessionlogin/service.go` 登录响应补 `userId` 字段
- **C2-B 403-HINT-RESOLVED-PATH** `runtime/auth/middleware.go` 403 错误 hint 包含 resolved path
- **C2-C README-MACOS-BASE64** `examples/ssobff/README.md` macOS `base64` flag 可移植化（去 Linux 特定参数）

**搭车**：无

**文件面**：`cells/accesscore/slices/sessionlogin/` + `runtime/auth/middleware.go` + `examples/ssobff/README.md`

**风险**：低；DX 打磨

---

## Wave 3 — P2 架构延展（~10 工作日）

### PR-A14b INTERNAL-LISTENER-FULL（Cx4，~1d，v1.1）

> **注**：已拆分成 PR-A14a（Wave 1 最小双 listener）+ PR-A14b（Wave 3 完整版）。本条是完整版。

**主线**：
- **R4 INTERNAL-LISTENER-FULL** 在 PR-A14a 最小双 listener 基础上升级完整 RouteGroup：新增 `health` listener（`/healthz|/readyz|/metrics`）+ service-token/mTLS 策略 + `bootstrap.WithRouteGroup` 声明式 API + 编译期 listener 引用校验
- **依赖**：PR-A14a 已合入（primary + internal 双 listener 基座已稳定）

**搭车**：
- 配合 **PR-A32 SELECTOR-CLOSURE** 在本 PR 后删除 `bootstrap.WithInternalEndpointGuard` 过渡层（F3 彻底收尾）

**文件面**：`runtime/bootstrap/bootstrap.go` + `runtime/http/router/group.go`（新）+ 全部 Cell 路由注册 API

**风险**：高；签名破坏性变更，所有 cell 需同步更新。

---

### PR-A15 KERNEL/WEBHOOK（P2，~3d，并入 WM-4）

**主线**：
- **LATER-K3 KERNEL/WEBHOOK** Webhook 出站 Receiver/Dispatcher 抽象（含 HMAC + SSRF 白名单）
- **WM-32 mTLS 中间件**（WinMDM defer，同批，因 mTLS 也是 outbound 安全面）

**搭车理由**：Webhook outbound 和 mTLS 同属出站安全层；WM-4 六席位已通过 P2 defer，本 PR 落地。

**前置**：L3 Outbox Relay 必须稳定（当前 L2 已稳），且 SSRF 策略需评审通过。

**文件面**：`kernel/webhook/`（新） + `runtime/http/outbound/`（可能新）

**风险**：高；SSRF 策略 + HMAC 签名需安全评审。

---

### PR-A16 KERNEL/RECONCILE（P2，~2d）

**主线**：
- **LATER-K4 KERNEL/RECONCILE** L3 收敛控制循环（Reconciler 模式）

**搭车**：
- **LATER-F-1 L3-PROJECTION-REFERENCE-CELL-01** `examples/l3projection/` 官方样板代码（功能 P3）

**搭车理由**：L3 Reconciler 模式发布时官方补 L3 reference cell 示范业务实现。

**文件面**：`kernel/reconcile/`（新） + `examples/l3projection/`（新）

**风险**：中；Reconciler API 设计需 ADR。

---

### PR-A17 RUNTIME/SCHEDULER（P2，~2d）

**主线**：
- **LATER-K5 RUNTIME/SCHEDULER** Cron + 完整定时任务支持（分布式防重 + 并发）

**搭车**：
- **WM-18 延迟消息原语**（WinMDM defer）—— scheduler 稳定后探索 RabbitMQ `x-delayed-message`

**搭车理由**：都属定时调度；WM-18 依赖 scheduler 稳定后方可实现。

**文件面**：`runtime/scheduler/`（新） + 可能 `adapters/rabbitmq/delayed.go`

**风险**：中；分布式协调依赖 Redis/etcd；测试桩需覆盖。

---

### PR-A18 Vault 多租户 + 剩余优化（~5h）

**主线**：
- **A15 VAULT-NAMESPACE-MULTITENANT-01** HCP Vault namespace 支持（1h）
- **A16 VAULT-DATAKEY-ENDPOINT-01** datakey/plaintext endpoint（2h，🟠 S14a 触发）
- **A18 VAULT-ROTATE-OPTIMISTIC-LOCK-01** 无锁 rotate + 写锁仅 version cache（2h）
- **LATER-AL-R RMQ-STATUS-01** RabbitMQ 结构化 ConnectionState（3h）

**搭车理由**：前三项都在 `adapters/vault/transit_provider.go`；RMQ-STATUS-01 是 adapter 层可观测的类似改造，一起合并验证 adapter 层 health/status 一致性。

**文件面**：`adapters/vault/transit_provider.go` + `adapters/rabbitmq/connection.go`

**风险**：低-中。

---

### ~~PR-A32 SELECTOR-CLOSURE~~（已吸收进 PR-A14a）

**状态**：PR-A14a 彻底重构为物理双 mux 后，`WithInternalEndpointGuard` / `WithInternalPathPrefixGuard` / `authDelegatedMatcher` 已全部删除；F3-CLOSURE SELECTOR-GUARD-REMOVE-01 已完成。Wave 3 无需再开独立 PR。

---

## Wave 4 — P3 长期架构演进（~2-3 周）

### PR-A19 AL-01 Outbox Relay → runtime（~1-1.5d）

**主线**：
- **AL-01 OUTBOX-RELAY-RUNTIME-MIGRATE-01** `adapters/postgres/outbox_relay.go` 轮询调度 → `runtime/outbox/relay.go`；Adapter 仅留 Store API

**搭车**：无（纯分层重整，独立）

**文件面**：`adapters/postgres/outbox_relay.go` + `runtime/outbox/`（新）

**风险**：中；依赖注入链会变，需 integration test。

---

### PR-A20 AL-02 DistLock → runtime 抽象（~1d）

**主线**：
- **AL-02 DISTLOCK-RUNTIME-ABSTRACT-01** 续期 goroutine + TTL 刷新 → 通用 DistLock 接口；Redis 仅留 NX/Eval 原语

**搭车**：无

**文件面**：`adapters/redis/distlock.go` + `runtime/distlock/`（新）

---

### PR-A21 AL-04 Auth JWT 依赖评估（~0.5-1d）

**主线**：
- **AL-04 AUTH-JWT-ABSTRACT-01** `runtime/auth` 直接依赖 `golang-jwt/jwt/v5`，评估抽象必要性

**决策点**：JWT 是事实标准；可能结论为 "won't do"（维持现状，补文档说明）。

**搭车**：**T5 AUTH-SIGNER-01**（trigger）若 golang-jwt v6 发布则一并处理。

**文件面**：`runtime/auth/`

---

### PR-A22 Cell ISP 拆分（~1.5d）

**主线**：
- **LATER-ARCH-1 CELL-IFACE-ISP-SPLIT-01** 12 方法基础接口 → `Cell` + `CellLifecycle` + `CellMetadata`

**搭车**：无（影响所有 cell 实现，独立 PR 做分阶段迁移）

**文件面**：`kernel/cell/` + 所有 `cells/*/cell.go`

**风险**：高；接口破坏性变更，所有 cell + examples 需同步更新。

---

### PR-A23 ER-ARCH-01 Subscriber Setup/Run 双阶段（~2d）

**主线**：
- **LATER-ARCH-2 ER-ARCH-01** Router 启动探测 `time.After(500ms)` → Subscriber 接口拆 `Setup()` + `Run()`

**搭车**：无（协议级改造，独立）

**文件面**：`kernel/outbox/subscriber.go` + `runtime/eventrouter/` + `adapters/rabbitmq/subscriber.go` + `adapters/memory/subscriber.go`

**风险**：高；所有 Subscriber 实现需升级；时序竞态修复需跨 AZ 测试验证。

---

### PR-A24 DURABLE-TYPE-01 + G-6 + kernel/replay + rollback（~2d）

**主线**（打包长期债）：
- **DURABLE-TYPE-01** L2/L3 持久化级别类型系统静态保护研究 + 实现
- **G-6 ASSEMBLY-BOUNDARY-DERIVED-01** boundary.yaml 存在性 + 一致性校验（关联 PR220-e2 GENERATED-BOUNDARY-STRATEGY 决策）
- **LATER-K6 KERNEL/REPLAY** 投影重算（v1.1）
- **LATER-K7 KERNEL/ROLLBACK** Rollback 元数据模型（v1.1）

**搭车理由**：都是低频、独立新模块；打包成一个 v1.1 sprint。

**文件面**：`kernel/replay/`（新） + `kernel/rollback/`（新） + `kernel/governance/` + metadata 类型探索

**风险**：低（业务不紧迫），可随时排期。

---

### PR-A33 REFRESH-OPAQUE-POLISH（X12 + X13 + X14，~8h）

**主线**：
- **X12 REFRESH-IDLE-EXPIRE-01**（3h）`refresh_store.go` 加 `idle_expires_at` 滑动窗口；每次 Rotate 刷新 `last_used + idle_ttl`；ref: Zitadel
- **X14 REFRESH-GRACE-COUNTER-01**（2h）`first_used_at` + `used_times` 列，grace 窗口内重用次数上限（默认 3）触发 `ErrTokenReused`；ref: Hydra Fosite
- **X13 REFRESH-PARTITION-01**（3h，🟠 生产流量阈值后）`refresh_tokens` 按 `expires_at` range 分区，`DROP PARTITION` 替代批量 DELETE（migration 012）

**搭车理由**：全部在 `adapters/postgres/refresh_store.go` + migrations；X12/X14 语义补强，X13 性能扩容，一批合测试工作量集中

**依赖**：**PR-A29 AUTH-REFRESH-MAIN 已合入**（主链 opaque 生效）

**文件面**：`adapters/postgres/refresh_store.go` + migration 010/011/012 + `runtime/auth/refresh/policy.go`

**风险**：中；分区涉及数据迁移，建议 X13 单独 staging 演练

---

## PR 依赖关系图

```
Wave 1 (可并行，无相互依赖)：
  PR-A1 治理规则 ─┬─ 对 PR-A13 docs clean 有软依赖
  PR-A2 pkg helper
  PR-A3 入口 + per-cell adapter
  PR-A4 运行时可观测
  PR-A5a accesscore 拆分 + A5 lifecycle 迁移 ──→ PR-A5b configcore 拆分 + S15 ctx 分类
  PR-A6 EventRouter 拆分 + S4 typed + S41 marshal
  PR-A7 Principal 契约（F7）
  PR-A8 Vault auth
  PR-A14a INTERNAL-LISTENER-MIN 🔴 发布前必做（独立 listener 最小版）
  PR-A25 AUTH-PROD-HARDENING 🟠 （S-nonce + S32，real 模式前触发）
  PR-A26 AUTH-SETUP-ENDPOINT 🔴 （P1-19 setup slice + contract）
  PR-A27 CONFIGWRITE-RETURNING 🔴 （configwrite TOCTOU 收敛）
  PR-A28 CONFIG-DOCS-REWRITE 🟡 （pg-cell-template 重写）

Wave 2：
  PR-A9 CONTRACT-META-01 ──→ PR-A11 kernel/wrapper
  PR-A10 JSON-SARIF（独立）
  PR-A12 kernel/command（独立）
  PR-A13 docs clean ─────→ 激活 PR-A1 里的 DOC-NAMING-GUARD
  PR-A29 AUTH-REFRESH-MAIN 🔴 （X11 HMAC-split → X15 opaque 主链切换，强依赖 X11 先）
  PR-A30 AUTH-TEST-COVERAGE（S19+S21+S22+S24，依赖 PR-A29）
  PR-A31 AUTH-FIRSTRUN-DX（login userId + 403 hint + macOS base64）

Wave 3：
  PR-A14b INTERNAL-LISTENER-FULL（完整 RouteGroup + health + mTLS，依赖 PR-A14a）
  PR-A32 SELECTOR-CLOSURE（删 WithInternalEndpointGuard，依赖 PR-A14b）
  PR-A15 kernel/webhook（依赖 L3 Outbox 稳定）
  PR-A16 kernel/reconcile + LATER-F-1 L3 示例
  PR-A17 runtime/scheduler + WM-18
  PR-A18 Vault + RMQ 剩余

Wave 4 (v1.1+)：
  PR-A19 AL-01 Outbox → runtime
  PR-A20 AL-02 DistLock → runtime
  PR-A21 AL-04 Auth JWT 评估
  PR-A22 Cell ISP 拆分
  PR-A23 ER-ARCH-01 Subscriber Setup/Run
  PR-A24 DURABLE-TYPE + G-6 + replay/rollback
  PR-A33 REFRESH-OPAQUE-POLISH（X12 idle + X14 grace + X13 partition，依赖 PR-A29）
```

---

## 关键搭车矩阵

| 主 PR | 搭车项 | 搭车理由 |
|---|---|---|
| PR-A1 治理规则 | L11 + PR220-4 | 同 CI/governance 面（PR220-2 下沉 PR-A13） |
| PR-A2 pkg helper | A7 POOLSTATS-IFACE | 同为 adapter 抽象 |
| PR-A3 入口 | T6 per-cell adapter | 同 cmd/corebundle wiring |
| PR-A4 可观测 | R3 OB-02 | 同 runtime/http/middleware |
| PR-A5a accesscore | V-A16 TxRunner helper + **A5 initialadmin lifecycle 迁移** | accesscore 拆分触发 RunInTx 替换；sweep/EnsureAdmin 迁 lifecycle.Hook 必须同期做（删 WithBootstrapWorkerSink） |
| PR-A5b configcore | **S15 config_repo ctx.Canceled 归类** | 同 cell；复用 F5 `IsInfraError` |
| PR-A6 EventRouter | S4 typed event payload + **S41 marshal err 显式** | 同事件注册+payload 链路；sessionlogin/logout 事件发布路径原地修 |
| PR-A7 Principal | V-A17 rolefetch | Principal 重写触及角色查询路径 |
| PR-A8 Vault auth | S4b + DEGRADATION-GAUGE | 同 transit_provider.go |
| PR-A9 CONTRACT-META | L7-FMT15b + S2-follow | 同 contract.yaml 结构 |
| PR-A13 docs clean | PR220-1/1b/e1/e3 | 文档事实源一次性扫清 |
| PR-A14b listener-full | **PR-A32 SELECTOR-CLOSURE** | listener 隔离生效后删 prefix guard |
| PR-A15 webhook | WM-32 mTLS | 同出站安全 |
| PR-A16 reconcile | LATER-F-1 L3 示例 | 新机制 + 官方样板同发 |
| PR-A17 scheduler | WM-18 延迟消息 | WM-18 依赖 scheduler |
| PR-A18 Vault 剩余 | RMQ-STATUS-01 | 同 adapter health/status 面 |
| PR-A25 auth-prod-harden | S-nonce + S32 | real 模式 fail-fast 同面 |
| PR-A29 auth-refresh-main | X11 → X15（强依赖顺序） | HMAC-split 必须在 opaque integration 之前，否则需数据迁移 |
| PR-A33 refresh-polish | X12 + X13 + X14 | 全在 refresh_store.go + migrations |

---

## 推荐执行顺序

> 下面的 Week 编号按**双人并行 + buffer**给出（每周 ~5 个净工作日）。单人场景把每周拉长到 1.8 倍即可。

**Week 1**（Wave 1 前半 ~5d 净）：PR-A1（10h）+ PR-A2（7h）+ PR-A3（4h）+ PR-A14a（4h）—— 四路并行 worktree，基础设施打底

**Week 2**（Wave 1 中段 ~5d 净）：PR-A4（6h）+ PR-A5a（6-7h A5 lifecycle）+ PR-A5b（3h）+ PR-A6（7h）+ PR-A7（6h）—— cell 内部重组 + EventRouter + Principal 契约

**Week 3**（Wave 1 收尾 + Wave 2 启动 ~5d 净）：PR-A8 Vault（5h）+ PR-A25 AUTH-PROD-HARDENING（4h）+ PR-A26 AUTH-SETUP-ENDPOINT（4h）+ PR-A27 CONFIGWRITE-RETURNING（5h）+ PR-A28 CONFIG-DOCS（3h）+ PR-A13 docs clean（6h）—— Wave 1 发布硬约束全部落地

**Week 4**（Wave 2 重磅 ~5d 净）：PR-A9 CONTRACT-META-01（3d Cx3 瓶颈）+ PR-A29 AUTH-REFRESH-MAIN（X11→X15 串行 10h 🔴）并行

**Week 5**（Wave 2 收尾 ~3.5d 净）：PR-A10 JSON-SARIF（6h）+ PR-A11 wrapper（1d）+ PR-A12 command（1d）+ PR-A30 AUTH-TEST-COVERAGE（6h）+ PR-A31 AUTH-FIRSTRUN-DX（2h）

**🎯 v1.0 发布节点 @ Week 5 末**（双人）/ **Week 8-9 末**（单人）：Wave 1 + Wave 2 全部落地

**Week 6-8**（Wave 3，可与问题/功能层并行，~8-9d 净）：PR-A14b INTERNAL-LISTENER-FULL → PR-A32 SELECTOR-CLOSURE → PR-A15 webhook → PR-A16 reconcile → PR-A17 scheduler → PR-A18

**v1.1+**（Wave 4，~10d 净）：长期债 PR-A19 ~ PR-A24 + PR-A33 refresh polish，按季度排

---

## 验证方式

每个 PR 必须：
1. 本地跑 `golangci-lint run ./修改的包/...` 0 issues
2. 接口变更需跑 `go build -tags=integration ./...`
3. Cx3 复杂度 PR（A9/A14a/A14b/A15/A22/A23/A29）先输出方案 ADR，6 席位审通过后开工
4. 高风险 PR（A14a/A14b INTERNAL-LISTENER、A15 webhook、A22 Cell ISP、A23 ER-ARCH-01、**A29 REFRESH-MAIN**）必须走 `/ultrareview`
5. 🔴 标记 PR（发布前必做）必须跑完整 `go test -race -tags=integration ./...`

完成标志：
- `gocell validate --strict` 0 error
- `gocell check contract-health` 0 warning（CONTRACT-META-01 落地后 + 所有 contract.yaml 升级后）
- v1.0 release 前 Wave 1-2 全部落地（含 PR-A29 refresh 主链）；Wave 3 按需；Wave 4 v1.1+

---

## 已完工基石声明（不占 Wave 计划）

> 2026-04-23 扫描现状时识别——以下 auth/config 基石已落地，无需再排 PR，只在本计划附注供读者对齐。

| 基石 / 条目 | 来源 PR / 位置 | 完工状态 |
|---|---|---|
| **F1 JWT Registry** | `runtime/auth/config/registry.go` + `cmd/corebundle/main.go:225` | ✅ 单一事实源；吸收 S18 + S31 + S20 |
| **F3 Selector 基础设施** | `runtime/auth/exempt.go` + `auth.Declare` + `cmd/corebundle/bundle_hardening_test.go` 守护 | ✅ 吸收 S35 + S39；**剩余过渡层清理**见 PR-A32 SELECTOR-CLOSURE |
| **F5 Errcode Classifier** | `pkg/errcode/classify.go`（Category + IsInfraError + IsDomainNotFound + IsExpected4xx） | ✅ 吸收 P1-18 + S40 + S43 |
| **F6 Lifecycle 基础设施** | `runtime/bootstrap/lifecycle.go` + `runtime/worker/lazy.go` | ✅ 框架到位；**应用层迁移**（initialadmin）见 PR-A5a 搭车的 A5 |
| **F7 Principal API** | `runtime/auth/principal.go` + 5 处 handler 已用 `auth.FromContext` | ✅ claims 直读已清零；**统一契约剩余收口**见 PR-A7 P1-A |
| **L10 RoleInternalAdmin** | PR#218（627a8e6） | ✅ internal rbac-assign 已用 `auth.RoleInternalAdmin` |
| **S42 ROLELIST-CURSOR** | `rbaccheck/handler.go:82` 用 `query.MapPageResult` | ✅ 真实游标返回 |
| **F2 PG RefreshStore** | PR#213 | ✅ 基础设施完工；**主链切换**见 PR-A29 |

---

## 备注

- **非架构项不在本计划**：问题层（安全/兼容/测试/CI/bug/docs）和功能层（发布/新端点）走独立排期，见 `docs/plans/docs-backlog-md-docs-reviews-2026042219-graceful-backus.md` 对应章节
- **已完成项不重复**：L7 FMT15（PR#214）、L10（PR#218）、L1/L2/L4/L6（相关 PR）已核销，不列入；详见上方"已完工基石声明"
- **触发器项**：T1/T2/T4/T5 按条件延后；T3 已触发点埋在 PR-A12
- **auth/config 域源计划已委托本计划**：
  - `docs/plans/202604191515-auth-federated-whistle.md` F1-F7 → F1/F3/F5/F6/F7 基础完工（见"已完工基石声明"）；F2 剩余 → PR-A29；F4 → PR-A14a/A14b
  - `docs/plans/202604211245-024-auth-rebaseline-implementation-plan.md` A/B/C → A1 已 PR#218；A2 → PR-A25；A3 → PR-A29；A4 → PR-A14a/A14b；A5 → PR-A5a 搭车；B1 → PR-A30；B2 → PR-A6 搭车 + 已完工；C1 已 PR#216；C2 → PR-A31；C3 → PR-A14b
  - `docs/plans/202604200313-v1.0-pre-release-plan.md` Batch 5 PR-AUTH-SETUP（P1-19） → PR-A26；Batch 6 S4（typed） → PR-A6 搭车
