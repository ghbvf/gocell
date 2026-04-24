# 架构项 PR 实施计划

> 日期: 2026-04-23（2026-04-24 下午回灌合并状态 + 新增 PR-A34/A35/A36）
> 来源: `docs/plans/docs-backlog-md-docs-reviews-2026042219-graceful-backus.md` 架构层（P1/P2/P3）+ `202604191515-auth-federated-whistle.md`（F1-F7 基石）+ `202604211245-024-auth-rebaseline-implementation-plan.md`（A/B/C）+ `202604200313-v1.0-pre-release-plan.md` 残余 + 2026-04-24 六席位复核新发现
> 基线: `develop @ ce33f6a`（PR#233 合入后 — PR-A2/A3/A4/A8/#231/#233 已落地）
> 目标: 把架构层约 40 条任务 + auth/config 域剩余任务拆成 39 个内聚 PR，明确 wave 顺序、搭车关系、依赖、风险
> **工期**: 净编码 ~39 工作日（~312h）；双人并行 + buffer **~33 工作日（~6.6 周）**；v1.0 路径（Wave 1+2）双人 **~15-17 工作日（~3-3.5 周）**
> 2026-04-23 更新:
> - 第一轮融入：3 条搭车（PR-A5a/A5b/A6）+ 6 条新 PR（A25-A30 auth/config）
> - 第二轮修正（基于现状复核）：F3/F6 非"已完工"而是"基础设施完工+应用层仍有过渡态"；PR-A5a A5 lifecycle 迁移从 0.5h 修正为 2-3h；PR-A14 拆分为 A14a MIN（Wave 1 必做）+ A14b FULL（Wave 3）；新增 PR-A27 CONFIGWRITE-RETURNING / PR-A28 CONFIG-DOCS / PR-A29 AUTH-REFRESH-MAIN（X11+X15 上提 Wave 2 必做）/ PR-A32 SELECTOR-CLOSURE / PR-A33 REFRESH-OPAQUE-POLISH
> - 第三轮修正：工期从虚高"95 工作日 / v1.0 路径 40-45d"校正为**净编码 36d / v1.0 双人 ~3 周**
> - 识别已完工基石：F1 JWT Registry / F5 Errcode Classifier / F7 Principal API / L10 / S42 / F2 PG RefreshStore（详见末尾"已完工基石声明"）
>
> 2026-04-24 更新（第四轮 · 方案 A · nil-mode 边界收口）:
> - PR-A5a 的 V-A16 从 `RunInTxOrDirect(ctx, r, fn)` 升级为 **`persistence.RunnerOrNoop(r) TxRunner`** 边界注入模式，service 层彻底无 `if s.txRunner != nil`；工期不变
> - **新增 PR-A5c OUTBOX-EMITTER-UNIFY**（Wave 2，Cx3，~12-15h，需 ADR）：outbox 维度的 nil-mode 收口——`kernel/outbox.Emitter` 接口 + `DirectEmitter`/`WriterEmitter` + wire envelope 从 `runtime/outbox` 下沉到 `kernel/outbox` + 跨 cell service 层迁移 + archtest 3 规则（禁止 service 层 `txRunner == nil` / 直调 `Publisher.Publish` / 导入 `runtime/outbox`）
> - `ref: github.com/ThreeDotsLabs/watermill` `disabledPublisher`（message/router.go）+ `NopLogger`（log.go）；`ref: github.com/uber-go/fx` `NopLogger`（app.go）；`ref: github.com/zeromicro/go-zero` `getWriter()`（core/logx/logs.go）——三处开源边界注入模式
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
>
> 2026-04-24 下午更新（第七轮 · 六席位复核 + Wave 2 PR-A9 收口）:
> - 合并回灌：**PR-A2 ✅ PR#225**（pkg/validation + adapterutil）/ **PR-A3 ✅ PR#227**（per-cell adapter + main 收口）/ **PR-A4 ✅ PR#228**（autowire + readyz ctx）/ **PR-A8 ✅ PR#230**（Vault pluggable auth + self-healing renewal — A14/S4b/VAULT-RENEWAL 一批吸收，VAULT-RENEWAL-DEGRADATION-GAUGE 被更优自愈方案替代）/ **PR-A14a ✅ PR#237**（dual-listener 物理隔离）/ **PR-A5b ✅ PR#238**（configcore cell.go 拆分 + errcode Category）/ **PR-A9 ✅ PR#239**（CONTRACT-META-01 + FMT-15b + S2-follow 一批落地，32 个 HTTP contract.yaml 迁移 + FMT-13 双向校验）
> - PR-A8 残余：K8s auth e2e 测试 → 转 backlog `PR-A8-RESIDUAL VAULT-K8S-AUTH-E2E-01`（4h, 🟡 可延后）
> - PR-A9 残余（六角色 review 轮 2 发现的 OUT_OF_SCOPE）：共 6 条转 backlog，详见 Wave 2 / 新 PR 段尾部
> - 新 PR：**PR-A34 OUTBOX-DIRECT-SAFETY-GATING**（P1 安全, 🔴 多 pod/任何生产 in-memory 拓扑前必做，3h）/ **PR-A35 READYZ-POLISH**（P2, 3h）/ **PR-A36 HTTP-METRICS-LABEL-REALIGN**（P2, 🟠 多 cell assembly 前触发，4h）
> - PR-A25 主线复核：S-nonce 验证确认（`runtime/auth/authenticator.go:213-218` CheckAndMark 逻辑存在但需 `WithNonceStore` 显式注入；`cmd/corebundle/controlplane.go:51` 未注入），重放窗口 5min；**维持 Wave 1 🟠**；开干前需评估是否同时落 InMemoryNonceStore 默认兜底（无 Redis 依赖的 P1 缓解）

---

## 设计原则

1. **文件亲缘**：同目录或同模块的修改塞进同一 PR，降低 review 成本
2. **语义内聚**：按"治理规则"、"Auth 收口"、"Contract spec"等单一主题切分
3. **抽取先于业务**：先落 helper / 新接口，再把业务切换过去（V-A14 adapterutil 先于 V-A15 cell 拆分用到）
4. **Cx3 独立审**：高复杂度（CONTRACT-META-01、kernel/wrapper、INTERNAL-LISTENER）独立 PR，防互相污染 review
5. **风险由低到高**：pkg helper / CI 治理 → 业务 cell 拆分 → 协议级改造（ER-ARCH-01、Subscriber Setup/Run）

---

## PR 切分总览

39 个 PR 分 4 Wave，**净编码合计 ~39 工作日**（312h），含 P3 长期架构。

| Wave | 目标 | PR 数 | 净编码 | 含 buffer（单人/双人） |
|---|---|---|---|---|
| **Wave 1** — 低风险抽取 + 治理 + auth v1.0 必做 + config 样板收敛 + INTERNAL-LISTENER-MIN | 为后续业务改造铺平基础 + 发布硬约束 | 15 | 9.7d | 14d / 7d（三路 worktree 并行） |
| **Wave 2** — 中等架构收口 + auth refresh 主链 + auth 测试 + DX + outbox 模式收口 | Contract 模型 / kernel 新模块 + refresh opaque 主链（X11+X15）+ auth 收尾 + Emitter 抽象 | 9 | 10d | 14d / 8-9d（A9 + A5c 双 Cx3 瓶颈） |
| **Wave 3** — P2 架构延展 + INTERNAL-LISTENER-FULL + F3 Selector 收尾 | v1.1 kernel 子模块 + listener 完整版 | 8 | 9.75d | 14d / 8d |
| **Wave 4** — P3 长期架构演进 + refresh opaque 收尾 | 分层重整 / 接口拆分 / 类型保护 / refresh polish | 7 | 9.5d | 13d / 8d |
| **合计** | | 39 | **~39d** | **~55d / ~33d** |

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

### PR-A4 运行时可观测收口 ✅ 已落地 PR #228（2026-04-24）

**实际主线**：
- **R2 OBS-HTTP-COLLECTOR-AUTOWIRE-01** ✅ `runtime/bootstrap/bootstrap_phases.go:661-692` autoWireHTTPMetricsCollector；`WithMetricsProvider` 自动构造 `NewProviderCollector` + 防止与 `WithMetricsCollector` 冲突的 duplicate-name 错误包装
- **A21 HEALTH-CHECKER-CTX-BUDGET-01** ✅ `Checker func(ctx) error` 签名升级 + `Readiness.deadline` 统一超时 + 并发执行
- **R3 OB-02** ✅ safe_observe broken logger DI 测试

**六席位复核残余（2026-04-24，转 backlog）**：
- **HTTP-METRICS-LABEL-REALIGN-01**：`provider_collector.go:60,69,89` label 名 `cell` 实际值来自 `assemblyID`，多 cell assembly 下语义错位 → **PR-A36**（Wave 3/按触发条件）
- **READYZ-VERBOSE-TOKEN-DENY-01**：`health.go:397-419` verbose token 不匹配静默降级 → **PR-A35** 搭车
- **READYZ-UNCOOPERATIVE-CHECKER-GUARDRAILS-01**：`health.go:296-322` 自认 uncooperative probe 会 leak goroutine，缺并发上限/指标/合约测试 → **PR-A35** 主线

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

**Round-2 review 遗留**（PR#238 登记 backlog）：PR238-FU1 decrypt-CategoryAuth-eval / PR238-FU2 infra-bucket-counter-audit（触发时 + 配套 governance 静态规则）/ PR238-FU3 ctx-cancel-integ-test / PR238-FU4 legacy-test-dedup / PR238-FU5 cell-split-layout-normalize / PR238-FU6 ctx-cancel op-细化。详见 `docs/backlog.md`。

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

### PR-A7 sessionmint 统一入口 + P1-A 收尾（实际 ~4h，PR #TBD）

**主线落地**：
- **V-A17（升级版）FETCH-ROLE-NAMES-DEDUP-01** ✅ 抽 `cells/accesscore/internal/sessionmint`，单一 `Mint(ctx, deps, req)` 封装 "fetch roles (fail-closed) + issue access + issue refresh"；3 个调用点（`sessionlogin.Login` / `sessionlogin.IssueForUser` / `sessionrefresh.rotateAndIssue`）各收敛到一行；两个 slice 的 `fetchRoleNames` / `issueAccessToken` / `issueRefreshToken` 六个重复私有方法全部删除
- **新增 `errcode.ErrAuthRoleFetchFailed`**（CategoryInfra → HTTP 500），替换 sessionlogin / sessionrefresh 两处 fail-open silent degrade（原先 roleRepo 故障时 Warn + 签空 roles token 导致用户"登录成功但 RBAC 全丢"，现在直接 abort 由客户端重试）
- **sessionvalidate.Service.Verify() dead shim 移除**：AuthMiddleware 已直接走 `VerifyIntent`，壳函数无生产消费；9 处测试调用改用 `svc.VerifyIntent(ctx, tok, auth.TokenIntentAccess)`

**P1-A 状态**：✅ pre-existing — 探索实测 `runtime/auth.Principal` + `WithPrincipal/FromContext/MustFromContext` + `UnionAuthenticator` + JWT/Service `Authenticator` 早已落地；`cmd/corebundle/auth_integration_test.go:321-348` 验证 `/internal/v1` delegated → ServiceToken → `RoleInternalAdmin` 链路全绿；所有 handler 都在消费 `auth.FromContext`；无 `ctx.Value(claimsKey)` 残留。

**替代决策**：原计划 `rolefetch.FetchRolesStrict` / `FetchRolesLenient` 双变体被抛弃——两处源码实测都是 fail-open，不是一个该保留的语义；拔到 `sessionmint.Mint` 这个更高抽象消除整条流水线重复，而不是保留 Strict/Lenient 双函数分叉。

**文件面**：
- 新：`cells/accesscore/internal/sessionmint/{sessionmint.go, sessionmint_test.go}`
- 改：`pkg/errcode/errcode.go` + `pkg/httputil/response.go`（新 errcode + 500 映射）
- 改：`cells/accesscore/slices/{sessionlogin, sessionrefresh, sessionvalidate}/service.go`
- 扩测：`cells/accesscore/slices/{sessionlogin, sessionrefresh}/service_test.go`（fail-closed 回归用例）+ sessionmint 6 用例

**风险**：低；改动集中在 accesscore 内部，fail-closed 语义变化已由新测试覆盖；integration test（cmd/corebundle + identitymanage + ssobff 全部 -tags=integration 绿）确认链路无回归。

---

### PR-A8 Vault auth 批量 ✅ 已落地 PR #230（2026-04-24）

**实际主线**：
- **A14 VAULT-AUTH-PLUGGABLE-01** ✅ `adapters/vault/auth.go` `AuthMethod` 接口 + `MethodToken`/`MethodAppRole`/`MethodKubernetes` 三实现（`auth.go:80-87, 269+`）
- **S4b VAULT-TOKEN-STATIC-REAL-GUARD-01** ✅ `auth.go:528-540` `AssertForRealMode(auth)` + `transit_provider.go:774` 在 Login I/O 之前 fail-fast 拒绝静态 token
- **self-healing renewal** ✅ `transit_provider.go:139-375` `tokenRenewalWorker` + `doReauth()` 无限退避重试（watcher.DoneCh 触发时返回 false 不升级为 worker fatal，`authHealthy` gauge 0→1 追踪）；覆盖测试 `reauth_test.go:38,82,152` 三用例

**替代决策**：`VAULT-RENEWAL-DEGRADATION-GAUGE` 原计划是"降级时加 metric 区分"，实现为更优的"续租失败直接重认证"——不再降级，metric 无需新增。

**残余（转 backlog）**：
- `PR-A8-RESIDUAL VAULT-K8S-AUTH-E2E-01`（4h, 🟡 可延后）— K8s auth 仅单测，缺 e2e 演证 ServiceAccount 挂载 → JWT login → secret fetch 完整链路

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

### PR-A25 AUTH-PROD-HARDENING（🟠 real 模式前触发，~5h，2026-04-24 六席位复核确认 P1 阻塞性）

**本轮验证证据（2026-04-24）**：
- `runtime/auth/authenticator.go:213-218`：`CheckAndMark` 分支已存在但 `cfg.nonceStore != nil` 才生效
- `runtime/auth/authenticator.go:130-131` 注释明写 "NonceStore is nil by default; omitting it disables replay detection. Production deployments MUST supply WithNonceStore"
- `cmd/corebundle/controlplane.go:51` `ServiceTokenMiddleware(ring)` 无 `WithNonceStore` 注入 → 5 min 签名窗内重放任意次

**主线**：
- **S-nonce SERVICE-TOKEN-NONCE-STORE-ENFORCE-01**（4h）：
  - (a) `controlplane.internalGuardFromEnv` 默认构造 `auth.NewInMemoryNonceStore(ttl=ServiceTokenMaxAge+30s buffer)` 作为 P1 缓解（无 Redis 依赖即可落地 anti-replay）
  - (b) 新增 `auth.WithServiceTokenNonceStore(store)` option 允许注入 Redis/持久化实现覆盖 in-memory
  - (c) `SharedDeps.Validate()` real 模式：NonceStore 未注入或为 Noop → fail-fast `ErrControlplaneNonceStoreMissing`（对齐 Vault real-mode guard 风格）
  - (d) 集成测试：replay 用例（同 token 第二次应返回 401 with `ERR_AUTH_UNAUTHORIZED`）；两 pod 场景单测可 skip（Redis store 交 X1 后独立 PR）
- **S32 CONTROLPLANE-TOKEN-PROD-GATE-01**（1h）real 模式断言 service-token ring 已配置（非空 secrets）；如未来接入 mTLS 则改为至少一项

**搭车理由**：两项都是 real 模式 fail-fast，在 `cmd/corebundle/controlplane.go` + `runtime/auth/authenticator.go` 同一面；S32 的 CI real-mode smoke 也顺便加

**文件面**：`runtime/auth/authenticator.go` + `runtime/auth/nonce.go` + `cmd/corebundle/controlplane.go` + `cmd/corebundle/shared_deps.go` + `cmd/corebundle/controlplane_guard_test.go`

**风险**：低-中；CI smoke 要新建 real 模式 job；默认注入 InMemoryNonceStore 在 demo 模式也生效（但 TTL 小，对 demo 无感）

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

### PR-A34 OUTBOX-DIRECT-SAFETY-GATING（🔴 多 pod / 任何生产 in-memory 拓扑前必做，~3h）

**来源**：2026-04-24 六席位复核 P1 安全（backlog `S-outbox-safety`）。

**本轮验证证据**：
- `kernel/outbox/emitter.go:90-95`：`DirectPublishFailOpen` 发布失败 `return nil` 无差别隐藏错误
- `cells/auditcore/cell.go:218-221` + `cells/accesscore/cell.go` 同模式：在 in-memory（`publisher != nil && outboxWriter == nil`）下自动注入 fail-open emitter
- 受影响 topic：`event.session.revoked.v1`、`event.audit.appended.v1`、`event.user.{created,locked}.v1`、`event.session.created.v1`——审计/会话撤销链路会被静默切断

**主线**：
- **S-outbox-safety OUTBOX-DIRECT-SAFETY-GATING-01**（2h）
  - (a) `DirectEmitter` 增加 `SafetyCriticalTopics []string` 字段（或接收 `TopicClassifier func(topic string) Severity` 由 kernel topic registry 提供）
  - (b) 发布 safety-critical topic 失败时：即使 mode 是 FailOpen 也必须返回 error；非关键 topic 保留 FailOpen
  - (c) `cells/auditcore` + `cells/accesscore` 注入时声明各自的 safety-critical topic 白名单
  - (d) 追加 counter `gocell_outbox_direct_publish_failed_total{mode,topic}` — demo 模式下也能被监控
- **测试**：emitter_test 补 FailOpen + safety-critical 组合用例；auditcore/accesscore 集成测试验证 session.revoked 发布失败时上游收到 error（不再被静默吞掉）

**搭车**：无（独立主题；PR-A6 typed event payload 属于不同面，不搭）

**文件面**：`kernel/outbox/emitter.go` + `kernel/outbox/emitter_test.go` + `cells/auditcore/cell.go` + `cells/accesscore/cell.go` + `cells/accesscore/cell_test.go` + `cells/auditcore/cell_test.go`

**风险**：低-中；改的是 fail-open 策略分档；不涉及协议签名

---

## Wave 2 — 中等架构收口（~12 工作日）

### PR-A9 CONTRACT-META-01 传输层一等公民 ✅ 已落地 PR #239（2026-04-24）

**主线**：
- **LATER-SD-1 CONTRACT-META-01** ✅ `pkg/contracts.HTTPTransport` 增加 `PathParams` / `QueryParams` typed map（`ParamSchema{Type, Required, Format}`），类型白名单 `string|integer|number|boolean|uuid`；`kernel/governance` FMT-13 新增路径模板 ↔ pathParams 双向一致性 + 类型白名单校验（path 占位符缺声明、声明多余、未知 type 均为 Error）。

**搭车**（同 PR 落地）：
- **L7-FMT15b CONFIG-GET-DUAL-MODE-SPLIT-01** ✅ 拆 `contracts/http/config/get/v1` 的 oneOf 响应合并；新建 `contracts/http/config/list/v1`，`cells/configcore/slices/configread` serve 双 contract，contract_test 双向 reject 错误形状。
- **S2-follow CONTRACT-ERROR-SCHEMA-EXTEND-01** ✅ 27 个平台 HTTP contract + 5 个 example contract 迁移 pathParams/queryParams；auth-protected 端点补 `responses[401]`，admin-guarded 再补 `responses[403]`；Public 端点（auth/login、auth/refresh）保持无 401/403 声明。

**落地验证**：`gocell validate --strict` → 0 errors（1 个 pre-existing REF-16 boundary.yaml warning，与本 PR 无关）；`gocell check contract-health` → PASS；integration-tag build 0 errors；lint 0 issues；新增 FMT-13 table-driven case 覆盖：缺声明、多声明、未知 type、path-optional、multi-placeholder happy、query-param optional/unknown、duplicate placeholder dedup、combined path+query、empty path 短路。

**文件面**：`pkg/contracts/` + `kernel/metadata/schemas/contract.schema.json` + `kernel/governance/rules_fmt.go` + `kernel/scaffold/templates/contract-http.yaml.tpl`（scaffold 同步更新） + `docs/architecture/metadata-model-v3.md` + 32 个 contract.yaml + 1 个新 contract 目录 `contracts/http/config/list/v1/`。

**解锁**：PR-A11 kernel/wrapper 现可拿到完整 Method/Path/PathParams 做 trace span 标注。

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

### PR-A5c OUTBOX-EMITTER-UNIFY（Cx3，~12-15h，🟡 v1.0 建议）

**主线**：
- **EMITTER-ABSTRACT-01** `kernel/outbox` 新增 `Emitter` 接口 + `DirectEmitter` + `WriterEmitter`：
  ```go
  type Emitter interface {
      Emit(ctx context.Context, entry Entry) error
  }
  type DirectPublishFailureMode int
  const (
      DirectPublishFailClosed DirectPublishFailureMode = iota + 1
      DirectPublishFailOpen
  )
  func NewWriterEmitter(w Writer) (Emitter, error)
  func NewDirectEmitter(p Publisher, mode DirectPublishFailureMode, logger *slog.Logger) (Emitter, error)
  ```
- **ENVELOPE-KERNEL-DOWN-01** wire envelope 契约（`WireMessage` / `EnvelopeSchemaV1` / `MarshalEnvelope` / `MarshalDirectEnvelope` / `UnmarshalEnvelope` / `ErrUnknownEnvelopeVersion`）从 `runtime/outbox` 下沉到 `kernel/outbox`；`runtime/outbox` 保留 relay / store / `ClaimedEntry` 等运行时职责，可短期保留 wrapper 委托减少一次性改动风险
- **SERVICE-EMITTER-MIGRATE-01** accesscore + configcore + auditcore 全部 L2 slice service 层：
  - 删除字段 `publisher outbox.Publisher` / `outboxWriter outbox.Writer`
  - 新增字段 `emitter outbox.Emitter`（永远非 nil，Cell 构造边界解析）
  - 删除 service 内部 `publisher.Publish(...)` + 本地 envelope 包装 + `outboxWriter.Write` 条件分支，统一改为 `return s.emitter.Emit(txCtx, entry)`
  - `configpublish.PublishFailureMode` 映射到 `outbox.DirectPublishFailureMode`，在 Cell 层构造 `DirectEmitter` 时决定
- **CELL-BOUNDARY-RESOLVE-01** 每个 L2 cell 的 `cell_lifecycle.go initSlices()` 统一模式解析：
  - `DurabilityDemo` + 有 publisher 且无 writer → 注入 `DirectEmitter`
  - `DurabilityDemo` + 无 publisher + 需要 L2 outbox 语义 → 注入 `WriterEmitter(outbox.NoopWriter{})`
  - `DurabilityDurable` 必须有真实 writer + tx runner，noop 一律 fail-fast
- **ARCHTEST-NIL-MODE-BLOCK-01** `kernel/governance/archtest/` 新增 3 规则（前置：确认 gocell 是否已有 archtest 框架；若无则本项作为最小 linter 规则或 `gocell validate` 扩展实现）：
  - 禁止 `cells/**/slices/**/service.go` 出现 `txRunner == nil` / `txRunner != nil`
  - 禁止 service 层直接调用 `Publisher.Publish`
  - 禁止 service 层导入 `runtime/outbox`

**搭车理由**：
- `RunnerOrNoop`（PR-A5a 落地）是 tx 维度的 Cell 边界收口；本 PR 是 outbox 维度的对称推广——让 service 层只依赖 `persistence.TxRunner` + `outbox.Emitter` 两个稳定抽象
- envelope 下沉 kernel 与 PR-A19 AL-01（relay → runtime）正交：契约属 kernel，运行时属 runtime，分层更清晰
- 三 cell 同批迁移避免 configcore/auditcore 各自开 PR 时重复讨论同一抽象

**ADR 前置**（开工前必须通过）：
- Emitter 接口签名（`Emit(ctx, entry)` vs 分离 `EmitDirect` / `EmitDurable`；是否暴露 fail mode）
- envelope 层次归属（kernel 契约 vs runtime 运行时，选 kernel 的 trade-off）
- fail-open（demo 场景）vs fail-closed（production 默认）的策略配置入口
- archtest 规则实现层（kernel/governance 扩展 vs golangci-lint 自定义 vs 文档 guard）

**文件面**：
- `kernel/outbox/`（新 Emitter + envelope 契约）
- `runtime/outbox/`（保留 relay/store，可短期 wrapper 委托）
- `cells/{accesscore,configcore,auditcore}/slices/**/service.go`
- 各 cell 的 `cell_lifecycle.go initSlices`
- `kernel/governance/archtest/` 或等效的治理扩展

**依赖**：
- PR-A5a 合入（`RunnerOrNoop` 边界注入模板落地 + accesscore 拆分 + service 层无 nil 检查先例）
- PR-A5b 合入（configcore 拆分落地，否则 configcore service 迁移会与 A5b 冲突）
- ADR 通过

**风险**：高（Cx3）；跨 3 cell service 签名改造 + archtest 规则新增 + envelope 跨层移动；必须 `go test -race -tags=integration ./...` 全通过；Cell 边界模式解析测试必须覆盖 demo/durable 四象限（publisher 有/无 × writer 有/无）

**搭车**：无（独立 Cx3 PR，避免与 A9 CONTRACT-META-01 review 互相污染）

**对标参考**：
- `ref: github.com/ThreeDotsLabs/watermill message/router.go` — `disabledPublisher` 显式类型表达"无 publisher 的 handler"
- `ref: github.com/ThreeDotsLabs/watermill log.go` — `NopLogger` 边界注入
- `ref: github.com/uber-go/fx app.go` — `NopLogger` 作为显式 option
- `ref: github.com/zeromicro/go-zero core/logx/logs.go` — `getWriter()` 边界补齐

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

### PR-A35 READYZ-POLISH（P2，~4h）

**来源**：2026-04-24 六席位复核（backlog `READYZ-VERBOSE` + `READYZ-LEAK-GUARDRAILS`，PR-A4 A21 延伸）。

**本轮验证证据**：
- `runtime/http/health/health.go:296-322` 自注释承认 "An uncooperative probe (one that ignores ctx) will leak its goroutine past this function's return"
- `health.go:397-419` verbose token 不匹配时返回 false → 降级为普通 200 无 verbose（slog.Warn 但客户端无指示）

**主线**：
- **READYZ-UNCOOPERATIVE-CHECKER-GUARDRAILS-01**（3h）
  - (a) `Readiness.maxConcurrentProbes`（默认 8）并发上限，超限请求走 503
  - (b) 追加 metric：`gocell_readyz_ongoing_probes` gauge + `gocell_readyz_probe_leaked_total{name}` counter
  - (c) checker 合约测试：所有内置 probe 必须在 `ctx.Done()` 100ms 内返回；fail-fast 不达标 probe
- **READYZ-VERBOSE-TOKEN-DENY-01**（1h）
  - (a) `VerboseTokenHeader` 已配置但请求 header 不匹配 → 返回 `401` 而非降级 200
  - (b) `VerboseTokenHeader` 未配置 → 保留当前"无 verbose"行为
  - (c) 响应区分两种路径：未配置走普通 200；配置了但不匹配走 401 + `{"error":"verboseDenied"}`

**搭车理由**：同 `health.go` 文件；合并测试改动

**文件面**：`runtime/http/health/health.go` + `runtime/http/health/health_test.go` + `docs/ops/readyz.md`（或 `env-vars.md`）

**风险**：低；行为向严格化但 401 是健康检查通用语义

---

### PR-A36 HTTP-METRICS-LABEL-REALIGN（P2，🟠 多 cell assembly 部署前触发，~4h）

**来源**：2026-04-24 六席位复核（backlog `R2-FOLLOW`，PR-A4 R2 延伸发现的架构层语义错位）。

**本轮验证证据**：
- `runtime/bootstrap/bootstrap_phases.go:675-683`：`cellID := b.assemblyID`（fallback 到 `b.assembly.ID()` 再到 `"default"`）
- `runtime/observability/metrics/provider_collector.go:60,69,89`：label 名为 `"cell"`，值来自 `cfg.CellID`
- 多 cell assembly（如 corebundle 含 access/audit/config 三 cell）下所有 HTTP 指标会贴同一 `cell="corebundle"`，按 cell 维度 dashboard/告警会误归因

**主线**（两步走，建议同 PR 或拆子 PR 串行）：
- **Step 1 最小兼容**（2h）：provider_collector 改为输出两个 label — `assembly`（保留现有值）+ `cell`（暂时 = assembly，保留 dashboard 兼容性）；或直接把 `cell` 重命名为 `assembly` 并发 dashboard migration note
- **Step 2 真解**（2h）：在 `router.Route` 注册时把 owning cell 写入 request context（或 route metadata）；`middleware/metrics.go` 从 ctx 读取 cell；`NewProviderCollector` 配置改为 `AssemblyID string, CellResolver func(*http.Request) string`

**开干前决策点**：需用户确认当前是否已有外部 dashboard/告警消费 `cell` label — 若有，强制双写 + deprecation 周期；若无，Step 2 一步到位。

**搭车**：无（独立主题）

**文件面**：`runtime/bootstrap/bootstrap_phases.go` + `runtime/observability/metrics/provider_collector.go` + `runtime/http/router/router.go` + `runtime/http/middleware/metrics.go` + `runtime/http/middleware/metrics_wiring_test.go`

**参考**：Kratos request labels（operation/kind/code/reason 分层）、go-zero HTTP metrics（path/method/code 不混服务名）、OpenTelemetry Resource vs Semantic-attr 分层

**风险**：中；涉及 dashboard/告警消费方；需要 migration 节奏

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
  PR-A2 pkg helper ✅ PR#225
  PR-A3 入口 + per-cell adapter ✅ PR#227
  PR-A4 运行时可观测 ✅ PR#228（残余 PR-A35/A36 延 Wave 3）
  PR-A5a accesscore 拆分 + A5 lifecycle 迁移 ──→ PR-A5b configcore 拆分 + S15 ctx 分类
  PR-A6 EventRouter 拆分 + S4 typed + S41 marshal
  PR-A7 Principal 契约（F7）
  PR-A8 Vault auth ✅ PR#230（残余 K8s auth e2e → PR-A8-RESIDUAL backlog）
  PR-A14a INTERNAL-LISTENER-MIN 🔴 发布前必做（独立 listener 最小版）
  PR-A25 AUTH-PROD-HARDENING 🟠 （S-nonce 默认 InMemoryNonceStore + real-mode fail-fast + S32）
  PR-A26 AUTH-SETUP-ENDPOINT 🔴 （P1-19 setup slice + contract）
  PR-A27 CONFIGWRITE-RETURNING 🔴 （configwrite TOCTOU 收敛）
  PR-A28 CONFIG-DOCS-REWRITE 🟡 （pg-cell-template 重写）
  PR-A34 OUTBOX-SAFETY-GATING 🔴 （DirectPublishFailOpen 对安全事件 fail-closed + counter）

Wave 2：
  PR-A9 CONTRACT-META-01 ──→ PR-A11 kernel/wrapper
  PR-A10 JSON-SARIF（独立）
  PR-A12 kernel/command（独立）
  PR-A13 docs clean ─────→ 激活 PR-A1 里的 DOC-NAMING-GUARD
  PR-A29 AUTH-REFRESH-MAIN 🔴 （X11 HMAC-split → X15 opaque 主链切换，强依赖 X11 先）
  PR-A30 AUTH-TEST-COVERAGE（S19+S21+S22+S24，依赖 PR-A29）
  PR-A31 AUTH-FIRSTRUN-DX（login userId + 403 hint + macOS base64）
  PR-A5c OUTBOX-EMITTER-UNIFY 🟡 （Emitter 抽象 + envelope 下沉 + 跨 cell service 迁移，依赖 PR-A5a + PR-A5b + ADR）

Wave 3：
  PR-A14b INTERNAL-LISTENER-FULL（完整 RouteGroup + health + mTLS，依赖 PR-A14a）
  PR-A32 SELECTOR-CLOSURE（删 WithInternalEndpointGuard，依赖 PR-A14b）
  PR-A15 kernel/webhook（依赖 L3 Outbox 稳定）
  PR-A16 kernel/reconcile + LATER-F-1 L3 示例
  PR-A17 runtime/scheduler + WM-18
  PR-A18 Vault + RMQ 剩余
  PR-A35 READYZ-POLISH（verbose token deny + uncooperative checker guardrails）
  PR-A36 HTTP-METRICS-LABEL-REALIGN（🟠 多 cell assembly 前触发）

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
| PR-A5c outbox-emitter | EMITTER + ENVELOPE + SERVICE-MIGRATE + ARCHTEST | 全属 outbox 模式收口；envelope 下沉 + 三 cell 一起迁可避免重复 ADR |

---

## 推荐执行顺序

> 下面的 Week 编号按**双人并行 + buffer**给出（每周 ~5 个净工作日）。单人场景把每周拉长到 1.8 倍即可。

**Week 1**（Wave 1 前半 ~5d 净）：PR-A1（10h）+ PR-A2（7h）+ PR-A3（4h）+ PR-A14a（4h）—— 四路并行 worktree，基础设施打底

**Week 2**（Wave 1 中段 ~5d 净）：PR-A4（6h）+ PR-A5a（6-7h A5 lifecycle）+ PR-A5b（3h）+ PR-A6（7h）+ PR-A7（6h）—— cell 内部重组 + EventRouter + Principal 契约

**Week 3**（Wave 1 收尾 + Wave 2 启动 ~5d 净）：PR-A8 Vault（5h）+ PR-A25 AUTH-PROD-HARDENING（4h）+ PR-A26 AUTH-SETUP-ENDPOINT（4h）+ PR-A27 CONFIGWRITE-RETURNING（5h）+ PR-A28 CONFIG-DOCS（3h）+ PR-A13 docs clean（6h）—— Wave 1 发布硬约束全部落地

**Week 4**（Wave 2 重磅 ~5d 净）：PR-A9 CONTRACT-META-01（3d Cx3 瓶颈）+ PR-A29 AUTH-REFRESH-MAIN（X11→X15 串行 10h 🔴）并行

**Week 5**（Wave 2 收尾 ~3.5d 净）：PR-A10 JSON-SARIF（6h）+ PR-A11 wrapper（1d）+ PR-A12 command（1d）+ PR-A30 AUTH-TEST-COVERAGE（6h）+ PR-A31 AUTH-FIRSTRUN-DX（2h）；**ADR A5c Emitter 抽象评审启动**

**Week 5.5**（PR-A5c 专攻 ~1.5-2d）：PR-A5c OUTBOX-EMITTER-UNIFY（12-15h，Cx3，ADR 通过后落地，三 cell service 同批迁移 + archtest 规则）

**🎯 v1.0 发布节点 @ Week 5 末**（双人）/ **Week 8-9 末**（单人）：Wave 1 + Wave 2 全部落地（PR-A5c 标 🟡 建议但非硬约束，若 ADR 或实现延期可滑入 v1.1）

**Week 6-8**（Wave 3，可与问题/功能层并行，~8-9d 净）：PR-A14b INTERNAL-LISTENER-FULL → PR-A32 SELECTOR-CLOSURE → PR-A15 webhook → PR-A16 reconcile → PR-A17 scheduler → PR-A18

**v1.1+**（Wave 4，~10d 净）：长期债 PR-A19 ~ PR-A24 + PR-A33 refresh polish，按季度排

---

## 验证方式

每个 PR 必须：
1. 本地跑 `golangci-lint run ./修改的包/...` 0 issues
2. 接口变更需跑 `go build -tags=integration ./...`
3. Cx3 复杂度 PR（A9/A14a/A14b/A15/A22/A23/A29/**A5c**）先输出方案 ADR，6 席位审通过后开工
4. 高风险 PR（A14a/A14b INTERNAL-LISTENER、A15 webhook、A22 Cell ISP、A23 ER-ARCH-01、**A29 REFRESH-MAIN**）必须走 `/ultrareview`
5. 🔴 标记 PR（发布前必做）必须跑完整 `go test -race -tags=integration ./...`

完成标志：
- `gocell validate --strict` 0 error
- `gocell check contract-health` 0 warning（CONTRACT-META-01 落地后 + 所有 contract.yaml 升级后）
- v1.0 release 前 Wave 1-2 全部落地（含 PR-A29 refresh 主链）；Wave 3 按需；Wave 4 v1.1+

---

## 已完工基石声明（不占 Wave 计划）


## 备注

- **非架构项不在本计划**：问题层（安全/兼容/测试/CI/bug/docs）和功能层（发布/新端点）走独立排期，见 `docs/plans/docs-backlog-md-docs-reviews-2026042219-graceful-backus.md` 对应章节
- **触发器项**：T1/T2/T4/T5 按条件延后；T3 已触发点埋在 PR-A12
- **auth/config 域源计划已委托本计划**：
  - `docs/plans/202604191515-auth-federated-whistle.md` F1-F7 → F1/F3/F5/F6/F7 基础完工（见"已完工基石声明"）；F2 剩余 → PR-A29；F4 → PR-A14a/A14b
  - `docs/plans/202604211245-024-auth-rebaseline-implementation-plan.md` A/B/C → A1 已 PR#218；A2 → PR-A25；A3 → PR-A29；A4 → PR-A14a/A14b；A5 → PR-A5a 搭车；B1 → PR-A30；B2 → PR-A6 搭车 + 已完工；C1 已 PR#216；C2 → PR-A31；C3 → PR-A14b
  - `docs/plans/202604200313-v1.0-pre-release-plan.md` Batch 5 PR-AUTH-SETUP（P1-19） → PR-A26；Batch 6 S4（typed） → PR-A6 搭车
  - **outbox direct publish + writer nil 收口** → PR-A5c（原散落在 configpublish / sessionlogin / sessionlogout / audit 事件发布路径的 nil 检查统一到 Cell 边界）

---

## 2026-04-24 补充：当前分支态势下的最大并行排期（不死守 Wave）

> 以当前真实在实施的分支为基线：PR-A1 / PR-A2 / PR-A3 / PR-A4 / PR-A8 已开工，另有 GitHub PR #224（`codex/outbox-emitter-nil-mode`）正在进行。以下排期按“热区 lane”而非 PR 编号排序，目标是最大化吞吐并最小化冲突。

### 当前 6 条主线（立即执行）

| Seat | 当前任务 | 分支 / 约束 |
|---|---|---|
| 1 | PR-A1 治理规则 + CI | `refactor/508-pr-a1-governance` |
| 2 | PR-A2 pkg helper | `refactor-508-pkg-helper-trio`；虽有少量 service 替换，但仍视为 helper lane |
| 3 | PR #224 outbox/emitter 基线 | `pr-224` / `codex/outbox-emitter-nil-mode` |
| 4 | PR-A3 入口收口 + per-cell adapter | `refactor/509-pr-a3-percell-adapter`；**必须 stack / rebase 到 PR #224 上** |
| 5 | PR-A4 运行时可观测收口 | `508-pr-a4-obs-runtime`；当前视为 health/bootstrap 接口基线 |
| 6 | PR-A8 Vault auth 批量 | `refactor/510-pr-a8-vault-auth-batch`；若未形成实际 diff，可临时替换为 PR-A13 |

### 当前阶段禁止新开的热点 PR

- **先不要开 PR-A5a / A5b / A6 / A7 / A14a / A18 / A25 / A27 / A29**
- 原因：
  - PR-A5a / A5b / A6 / A7 / A27 / A29 会撞 PR-A2 或 PR #224 所在的 access/config/outbox 热区
  - PR-A14a / A25 会撞 PR-A3 + PR-A4 的 `cmd/corebundle` / `runtime/bootstrap` 热区
  - PR-A18 会撞 PR-A8 的 `adapters/vault/transit_provider.go`

### 当前阶段合并顺序

1. PR-A13（若提前插入）→ PR-A1
2. PR-A2
3. PR-A4 与 PR #224 谁先 ready 谁先合
4. PR-A3 固定最后合（在 PR #224 之后 rebase 收口）
5. PR-A8 → PR-A28

### 滚动补位规则（谁先合谁腾 Seat）

| 完成的 lane | 下一条补位 |
|---|---|
| PR-A1 | PR-A13 → PR-A9 → PR-A10 → PR-A24 |
| PR-A2（但 PR #224 / A4 未完成） | PR-A12（不要急着开 PR-A5a / A5b） |
| PR-A8 | PR-A18 |
| PR #224（但 PR-A3 还未完成） | Seat 让给 PR-A12 / PR-A16，PR-A3 继续做 rebase 收口 |
| PR-A3 + PR-A4 + PR #224 全部完成 | 同时解锁 PR-A5a 与 PR-A14a/PR-A25（二选一先做） |
| PR-A5a | PR-A5b（stack 在 PR-A5a 上）→ PR-A26 |
| PR-A5b | PR-A27（重起新分支，不复用旧分支） |
| PR-A29 | PR-A31 + PR-A30 → PR-A33 |

### 长期 6 条 lane（后续持续滚动）

| Lane | 任务链 |
|---|---|
| Governance / Contract | PR-A1 → PR-A13 → PR-A9 → PR-A11 → PR-A10 → PR-A24 |
| Outbox / Event | PR #224 → **重算 PR-A5c 范围** → PR-A6 → PR-A19 → PR-A23 |
| Entry / Bootstrap | PR-A3 → PR-A14a → PR-A25 → PR-A14b → PR-A32 |
| Health / Base Runtime | PR-A4（完成后并入 Entry / Bootstrap lane） |
| Access / Auth | PR-A5a → PR-A7 → PR-A26 → PR-A29 → PR-A31 → PR-A30 → PR-A33 |
| Config / Vault / New Modules | PR-A5b → PR-A27；并行填充 PR-A8 → PR-A18 → PR-A12 → PR-A16 → PR-A17 → PR-A20 → PR-A21 → PR-A22 |

### 当前排期的硬规则

1. **PR-A3 永远放在 PR #224 后面处理**，作为 stacked / rebased 分支合入。
2. **PR-A5a / PR-A5b 只能做 stacked，不开 sibling PR。**
3. **PR-A27 不复用旧分支**；等 PR-A5b 后重起。
4. **PR-A6 / PR-A5c / PR-A19 / PR-A23 共用 outbox/event 热区，一次只开一个主 PR。**
5. **PR-A14a / PR-A25 / PR-A14b / PR-A32 共用 bootstrap/cmd 热区，一次只开一个主 PR。**
6. **PR-A29 开始后，auth lane 的其它改动必须围着 PR-A29 排。**

---

## 2026-04-24 补充：六角色 review 的 pre-merge / post-merge 分级策略

> 目标不是取消六角色 review，而是把它从“所有 PR 的同步阻塞门禁”改成“按风险分级的门禁 + post-merge 滚动修复”。仓库已有先例：`PR #7 ~ #12` 合并后形成 `#18 ~ #27` 的 post-merge review follow-up backlog；也有计划明确写过“单一集成 PR 完成后再跑 six-seat review”。

### 结论

- **可以先合并，再把六角色 review 的问题聚合成滚动修复 backlog。**
- **但不是所有 PR 都适合这么做。**
- 建议改成：**低/中风险 PR 允许先合；高风险 PR 仍保留 pre-merge 六角色 / ultrareview 门禁。**

### 必须 pre-merge 六角色 / ultrareview 的 PR 类型

- auth/security 主链：PR-A25 / PR-A29 / PR-A33
- internal/public listener 边界：PR-A14a / PR-A14b / PR-A32
- migration / schema / contract 变更：PR-A9 / PR-A27 / PR-A29 / PR-A33
- kernel / cell 接口破坏性重构：PR-A22 / PR-A23 / PR-A24 / PR-A5c
- outbox/event 语义主链重构：PR-A6 / PR-A5c / PR #224（若其语义继续扩大）
- 任何 Cx3 / Cx4 且带生产行为变化的 PR

### 可以先合并、后做六角色滚动修复的 PR 类型

- docs / workflow / backlog / naming / governance 非破坏性规则：PR-A1 / PR-A13 / PR-A28
- helper 抽取、adapterutil、纯测试、DX、SARIF 输出：PR-A2 / PR-A10 / PR-A12 / PR-A21
- `cmd/corebundle` 内部收口但不改变外部协议语义的重构：PR-A3
- vault / scheduler / reconcile / distlock 等相对隔离的新模块或低 blast radius 改动：PR-A8 / PR-A16 / PR-A17 / PR-A20

### 低/中风险 PR 的最小 pre-merge 门禁

1. `golangci-lint run ./修改的包/...` = 0 issues
2. `go build ./...`
3. 目标包测试 + 至少一轮 integration / e2e 冒烟
4. 至少 1 个直接 owner / 实施人之外的 reviewer 看过
5. 明确 rollback 方式（revert PR 或 follow-up PR）

### post-merge 六角色的执行方式

1. **不要每个 PR 合并后立即同步等待 6 份报告。**
2. 改成按 lane 或按 24h 批次聚合审查：
   - `cmd/bootstrap lane`
   - `outbox/event lane`
   - `access/auth lane`
   - `config lane`
   - `governance/docs lane`
3. 每批输出 1 份聚合报告，findings 按 root cause cluster 去重，不按 PR 零碎评论。
4. findings 处置：
   - P0：当天 hotfix / revert
   - P1：进入最近滚动修复 PR
   - P2：进入周批次 backlog

### 对当前计划的建议执行口径

- **PR-A1 / A2 / A3 / A8**：可以先合并，再进入按 lane 聚合的六角色 review。
- **PR-A4**：如果只停留在 health/checker / metrics collector 语义，可采用“轻量 pre-merge + 合后聚合 review”；若继续扩大到 bootstrap 生命周期基线，则提升为高风险，要求 pre-merge 六角色。
- **PR #224**：当前已接近 outbox/emitter 语义基线，建议至少做一次 pre-merge focused review（架构 + 测试 + 安全），不建议完全跳过 pre-merge 审查。

### 最终推荐

- **不要把六角色 review 作为所有 PR 的同步阻塞门禁。**
- **把它改成：高风险 PR pre-merge，低/中风险 PR post-merge 聚合审查。**
- **按 lane 聚合，而不是按单 PR 聚合。**
- **始终保留 P0 当天 hotfix / revert 的纪律。**

---

## 2026-04-24 补充：PR 风险分级矩阵（用于决定 pre-merge 门禁）

> 口径说明：
> - **高**：默认要求 pre-merge 六角色 review 或 `/ultrareview`
> - **中**：focused review 后可合，full six-seat 可放到合后 lane 聚合
> - **低**：轻门禁（lint/build/目标包测试/1 位非作者 reviewer）后可先合

| PR | 级别 | 建议门禁 |
|---|---|---|
| PR #224 | 高 | pre-merge focused review（架构 + 测试 + 安全） |
| PR-A1 | 中 | focused review 后可合 |
| PR-A2 | 低 | 轻门禁后可合 |
| PR-A3 | 中 | focused review 后可合 |
| PR-A4 | 中→高 | 若继续扩大到 bootstrap 生命周期基线，则升为高风险 |
| PR-A5a | 中 | focused review 后可合 |
| PR-A5b | 中 | focused review 后可合 |
| PR-A6 | 高 | pre-merge 六角色 |
| PR-A7 | 中 | focused review 后可合 |
| PR-A8 | 中 | focused review 后可合 |
| PR-A14a | 高 | pre-merge 六角色 |
| PR-A25 | 高 | pre-merge focused review |
| PR-A26 | 中 | focused review 后可合 |
| PR-A27 | 高 | pre-merge focused review |
| PR-A28 | 低 | 轻门禁后可合 |
| PR-A9 | 高 | pre-merge 六角色 |
| PR-A10 | 中 | focused review 后可合 |
| PR-A11 | 中 | focused review 后可合 |
| PR-A12 | 中 | focused review 后可合 |
| PR-A13 | 低 | 轻门禁后可合 |
| PR-A29 | 高 | pre-merge 六角色 |
| PR-A30 | 低 | 轻门禁后可合 |
| PR-A31 | 低 | 轻门禁后可合 |
| PR-A5c | 高 | pre-merge 六角色 |
| PR-A14b | 高 | pre-merge 六角色 |
| PR-A15 | 高 | pre-merge 六角色 |
| PR-A16 | 中 | focused review 后可合 |
| PR-A17 | 中 | focused review 后可合 |
| PR-A18 | 中 | focused review 后可合 |
| PR-A32 | 中 | focused review 后可合 |
| PR-A19 | 高 | pre-merge focused review |
| PR-A20 | 中 | focused review 后可合 |
| PR-A21 | 低 | 轻门禁后可合 |
| PR-A22 | 高 | pre-merge 六角色 |
| PR-A23 | 高 | pre-merge 六角色 |
| PR-A24 | 高 | pre-merge focused review |
| PR-A33 | 高 | pre-merge focused review |

### 高风险 PR 清单（便于快速筛选）

- PR #224
- PR-A6
- PR-A9
- PR-A14a
- PR-A14b
- PR-A15
- PR-A19
- PR-A22
- PR-A23
- PR-A24
- PR-A25
- PR-A27
- PR-A29
- PR-A33
- PR-A5c
