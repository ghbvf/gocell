# 架构项 PR 实施计划

> 日期: 2026-04-23（2026-04-25 第十三轮回灌 PR-A14b round-3/#262 + PR-A30/#263 + accesscore harden/#264 + distlock flaky/#265 全部落地，Wave 2 主线全部完工，无在飞 PR；第十二轮新增 Wave 2.5 Config 域审查回灌 PR-CFG-1..7；第十一轮回灌 PR-A14b/#258 + PR-A31/#259 + PR-A20/#260 + PR-A10/#261 一批）
> 来源: `docs/plans/docs-backlog-md-docs-reviews-2026042219-graceful-backus.md` 架构层（P1/P2/P3）+ `202604191515-auth-federated-whistle.md`（F1-F7 基石）+ `202604211245-024-auth-rebaseline-implementation-plan.md`（A/B/C）+ `202604200313-v1.0-pre-release-plan.md` 残余 + 2026-04-24 六席位复核新发现
> 基线: `develop @ ce33f6a`（PR#233 合入后 — PR-A2/A3/A4/A8/#231/#233 已落地）
> 目标: 把架构层约 40 条任务 + auth/config 域剩余任务拆成 40 个内聚 PR，明确 wave 顺序、搭车关系、依赖、风险
> **工期**: 净编码 ~40 工作日（~320h）；双人并行 + buffer **~34 工作日（~6.8 周）**；v1.0 路径（Wave 1+2）双人 **~16-17 工作日（~3-4 周）**
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
>
> 2026-04-24 晚间更新（第八轮 · Wave 1 发布硬约束彻底清零 + Wave 2 启动）:
> - 合并回灌：**PR-A25 ✅ PR#244**（AUTH-PROD-HARDENING — S-nonce 默认 InMemoryNonceStore + real-mode fail-fast + S32）/ **PR-A26 ✅ PR#247**（setup slice + `adminprovision` 共享服务 + orphan recovery；round-2 review 登记 A26-R1~R4 共 4 条 backlog）/ **PR-A5c 全量收口 ✅ PR#245**（`WithEmitter` / `WithOutboxDeps` Cell Option 六批迁移 + envelope kernel 迁移，PR#224 主线外的所有 cell publisher/writer 原生字段全清零）/ **PR-A12 ✅ PR#249**（KERNEL/COMMAND L4 queue + devicecell 迁移）/ **PR#248** DACL alias 测试修复 + os-smoke advisory（V-A17/V-A18 衍生 fix）
> - **v1.0 Wave 1 发布硬约束（🔴 区块）全部落地**：PR-A14a / A25 / A26 / A27 / A28 / A34 清零；PR-A5c 全量收口先于 v1.0 路径完成，Wave 2 Emitter 抽象门槛归零
> - **仍 open**：PR #246（PR-A11 kernel/wrapper — Wave 2） / PR #242（PR-A18 test split + PR-id comment 清理 — adapters/vault 后续整理）
> - 下一批优先：PR-A29 AUTH-REFRESH-MAIN（X11 → X15 串行主链，🔴 发布前必做）→ PR-A30/A31 → PR-A13 docs clean 若未走 PR #235 补口 → Wave 3 PR-A14b / A35 / A36 按排期推进
>
> 2026-04-25 更新（第九轮 · PR-A12b kernel/command 生命周期收口）:
> - 新增 **PR-A12b 🔨 PR#252**：在 PR-A12 `kernel/command` L4 底座之上继续收口 Dequeue / Report / Ack / ExtendLease + ActiveScanner，关闭 device command 的领取、回报、确认、续租和 active 扫描生命周期。
> - PR-A12b review 残余 Cx3/Cx4 已登记 backlog：`PR252-F1 COMMAND-QUEUE-REGISTRAR-BOOTSTRAP-FAILFAST-01` / `PR252-F2 COMMAND-SWEEPER-PRODUCTION-GOVERNANCE-01`。
>
> 2026-04-25 更新（第十轮 · Wave 2 几乎清零 + Wave 3/4 启动 + 三 PR 在飞）:
> - 合并回灌：**PR-A12b ✅ PR#252**（command 生命周期收口）/ **PR-A18 ✅ PR#242**（vault test split + PR-id comment 清理）/ **PR-A11 ✅ PR#246**（kernel/wrapper contract-level observable proxy）/ **PR-A6 ✅ PR#250**（typed event payloads + camelCase + `Emit[T]` 泛型；S4 + S41 残余清零）/ **PR-A29 ✅ PR#251**（AUTH-REFRESH-MAIN — opaque refresh tokens + append-only lineage，X10/X11/X15 一批清零）/ **PR-A35 ✅ PR#256**（READYZ-POLISH — wrapCtxSafe + singleflight + strict verbose token；B' 方案彻底替换 Plan 原三件套）/ **PR-257**（PR246-FU1 typed observability + Mount fail-fast + FMT-19 AST，PR-A11 post-merge round-6 review 收口）/ **PR#254**（integration test package 对齐）/ **PR#255**（CI build-test 4 路 matrix shard）
> - **🎯 v1.0 发布硬约束（🔴 区块）全部清零**：PR-A29 ✅ 是最后一块拼图；Wave 1 + Wave 2 主链全绿。**Wave 2 仅剩 PR-A10 OUTPUT-JSON-SARIF（🟡 6h，独立可延）+ PR-A30 AUTH-TEST-COVERAGE（依赖 PR-A29 已合，可启动）+ PR-A31 AUTH-FIRSTRUN-DX（🔨 PR#259 已开）**。
> - **新开 in-flight**：**PR-A14b 🔨 PR#258**（INTERNAL-LISTENER-FULL — three-listener + RouteGroup 声明式 API；R2-01..R2-11 fixes 已合 follow-up commit fc4e54e7）/ **PR-A31 🔨 PR#259**（AUTH-FIRSTRUN-DX — userId + resolved_path + collapse TokenPair）/ **PR-A20 🔨 PR#260**（Wave 4 提前启动：DistLock context-derived lock + shared manager goroutine）。
> - 下一批优先：(1) 走完三个 in-flight；(2) 启动 PR-A10 JSON-SARIF；(3) Wave 3 余项 PR-A15/A16/A17 / PR-A36 / PR-A37 DEVTOOLS-METADATA-EXPORT / PR-A38 TOOLS/DEPGRAPH 按排期推进；(4) Wave 4 PR-A33 REFRESH-OPAQUE-POLISH（PR-A29 已合可解锁）/ PR-A22 Cell ISP / PR-A23 ER-ARCH-01 / PR-A24 长期债打包（PR-A19 已早期完工，Wave 4 不再含此条）。
>
> 2026-04-25 更新（第十一轮 · 第十轮在飞四 PR 全部落地 + PR-A10 提前清零 + 仅剩 PR#262 round-3 follow-up）:
> - 合并回灌：**PR-A14b ✅ PR#258**（three-listener + RouteGroup 声明式 API；R2 fixes 已合 fc4e54e7）/ **PR-A31 ✅ PR#259**（AUTH-FIRSTRUN-DX — userId + resolved_path + collapse TokenPair）/ **PR-A20 ✅ PR#260**（DistLock context-derived lock + shared manager goroutine — Wave 4 长期债提前清零）/ **PR-A10 ✅ PR#261**（OUTPUT-JSON-SARIF — 统一诊断模型 + DX hints；Wave 2 6h 独立项一气呵成）。
> - **新开 in-flight**：**PR#262 🔨**（PR-A14b round-3 reviewer findings #1-#11 — `isAuthFlavoredPolicy` PolicyStack 解析 bug / PolicyVerboseToken SHA-256 比较 / PolicyMTLS API 重写 fail-fast / PolicyJWTFromAssembly typed marker mismatch fail-fast / readyzPayload 401 envelope 兜底 + 4 个 phase0 回归测试），分支 `247-pr-a14b-round3-reviewer`，挂 PR-A14b 后续质量收口
> - **Wave 2 100% 清零（除 PR-A30 测试增强外）**：PR-A14b/A31/A20/A10 在 24h 内一批落地，是发布后清债的密集窗口
> - 下一批优先：(1) 走完 PR#262 PR-A14b round-3；(2) 启动 PR-A30 AUTH-TEST-COVERAGE（最后一条 Wave 2 主线）；(3) Wave 3 PR-A36 metrics realign / PR-A37 DEVTOOLS-METADATA-EXPORT / PR-A38 TOOLS/DEPGRAPH 按排期推进；(4) Wave 4 PR-A33 REFRESH-OPAQUE-POLISH / PR-A22 Cell ISP / PR-A23 ER-ARCH-01 / PR-A24 长期债打包。
>
> 2026-04-25 更新（第十二轮 · Wave 2.5 Config 域审查回灌 7 PR）:
> - 来源：2026-04-25 config 域全链路静态审查（develop @ `788e03e3` 快照），8 条 finding（6×P1 + 2×P2），与 [`docs/reviews/archive/baseline-review-report-2026-04-05.md`](../reviews/archive/baseline-review-report-2026-04-05.md) §7 四根因簇（Fractured Source Of Truth / Non-Canonical Project Graph / Verification Is Not Executable Reality / Runtime Kernel Is A Leaky Stub）残余 instance 对齐——不是新问题，是 Wave 1/2 内未在 config 域内闭合的根因簇残留
> - 新增 7 PR：**PR-CFG-1..7**，按"架构治理先行 → 业务修跟"批次组织（详见新增 Wave 2.5 段）。设计原则：治理 PR 与最后一处违规清零同 PR，避免 allowlist 维护
> - 关联调整：(a) backlog `MULTI-REVIEW-RES-2` 范围收窄（readyz 由 PR-CFG-1 吸收 / counter+archtest 由 PR-CFG-6 吸收）；(b) backlog `PR250-F1` 由 PR-CFG-3 激进方案 supersede（删 wire `sensitive` 字段一并落地）；(c) 触发条件项 `T6 CONTRACT-EVENT-PAYLOAD-CODEGEN-01` cross-ref PR-CFG-3 (B1c) 手写 decoder 后续可由 codegen 替换
> - 工期：~5 工作日（架构治理 11h + 业务修 17h + 测试基建 1d），可与 Wave 3 余项并行（adapter/cells 文件不重叠）
> - 详细 finding + Cx + 影响面：`docs/backlog.md` PR-CFG-1..7
>
> 2026-04-25 更新（第十三轮 · 收尾 + 4 PR 一气清零，Wave 2 主线全部完工）:
> - 合并回灌：**PR-A14b round-3 ✅ PR#262**（reviewer findings #1-#11：`isAuthFlavoredPolicy` PolicyStack envelope 解析 / PolicyVerboseToken SHA-256 hashed compare / readyzPayload 401 envelope fallback / PolicyMTLS API 重写并删 misleading 参数 + phase0 fail-fast / PolicyJWTFromAssembly typed marker mismatch fail-fast + 6 个 phase0 回归测试）/ **PR-A30 ✅ PR#263**（AUTH-TEST-COVERAGE — S19/S21/S22/S24 一批：jwt aud table-driven + login/refresh drift integ + middleware e2e；Wave 2 最后一条主线收口）/ **fix(accesscore) ✅ PR#264**（first-admin harden：UserSource + ProvisionState provenance / printable ASCII password / GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE env + first-run-setup.md 双模式说明 — 顺带收口 backlog `PR247-N7` + `MULTI-REVIEW-RES-1`）/ **test(distlock) ✅ PR#265**（TestManager_HeapOrder flaky race 修：`waitTrackedLocks(m, 2)` barrier 让两次 handleAdd 都落在 fake-clock 0；PR-A20 #260 残留 flake）
> - **Wave 2 主线 100% 完工**：PR-A30 是 Wave 2 最后一条，Wave 2 至此全部清零；🎯 v1.0 发布硬约束在第十一轮已清零，本轮把 Wave 2 测试覆盖 / round-3 polish / first-admin 安全收口三件并行清完
> - **当前在飞 PR**：无（最近 8 个 PR 已合并）
> - 关联 backlog 收口：(a) S19/S21/S22/S24 由 PR#263 标 ✅；(b) PR247-N7 SETUP-ADMIN-EMAIL-CONTROL-CHAR-GUARD-01 由 PR#264 收口（password ASCII pattern + email/username 控制字符拒绝）；(c) MULTI-REVIEW-RES-1 SETUP-410-MIGRATION-DOC-CONSOLIDATE-01 由 PR#264 收口（first-run-setup.md 双模式段落 + env-vars.md 新变量）；(d) 新增 backlog `PR262-AUTH-POLICY-PLAN-01`：PR#262 已点修 PolicyStack 解析 + MTLS API + JWTFromAssembly typed marker，但**typed `ListenerAuth/GroupAuth` plan 完整重构**仍是后续 P1 安全/架构条目（Cx3，1-2d，🟡 当前最小修后可延后）
> - 下一批优先：(1) **Wave 2.5 Config 域 PR-CFG-1..7 batch1 启动**（4 worktree 并行，~1.5d 净，含两条 🔴 安全 + 一条 🔴 生产 gate）；(2) Wave 3 PR-A37 DEVTOOLS-METADATA-EXPORT 短链路高价值（解锁 gocell-web 自包含构建）；(3) Wave 3 PR-A36 metrics realign（🟠 多 cell assembly 部署前触发）

---

## 设计原则

1. **文件亲缘**：同目录或同模块的修改塞进同一 PR，降低 review 成本
2. **语义内聚**：按"治理规则"、"Auth 收口"、"Contract spec"等单一主题切分
3. **抽取先于业务**：先落 helper / 新接口，再把业务切换过去（V-A14 adapterutil 先于 V-A15 cell 拆分用到）
4. **Cx3 独立审**：高复杂度（CONTRACT-META-01、kernel/wrapper、INTERNAL-LISTENER）独立 PR，防互相污染 review
5. **风险由低到高**：pkg helper / CI 治理 → 业务 cell 拆分 → 协议级改造（ER-ARCH-01、Subscriber Setup/Run）

---

## PR 切分总览

40 个 PR 分 4 Wave，**净编码合计 ~40 工作日**（320h），含 P3 长期架构。

| Wave | 目标 | PR 数 | 净编码 | 含 buffer（单人/双人） |
|---|---|---|---|---|
| **Wave 1** — 低风险抽取 + 治理 + auth v1.0 必做 + config 样板收敛 + INTERNAL-LISTENER-MIN | 为后续业务改造铺平基础 + 发布硬约束 | 15 | 9.7d | 14d / 7d（三路 worktree 并行） |
| **Wave 2** — 中等架构收口 + auth refresh 主链 + auth 测试 + DX + outbox 模式收口 | Contract 模型 / kernel 新模块 + refresh opaque 主链（X11+X15）+ auth 收尾 + Emitter 抽象 | 10 | 11d | 15d / 9-10d（A9 + A5c 双 Cx3 瓶颈；A12b 为 command 生命周期收口） |
| **Wave 2.5** — Config 域审查回灌（治理先行 + 业务修） | 把 baseline review §7 四根因簇在 config 域的残余清零 + 平台治理硬化（HealthCheckers 自动聚合 / dead-event validate / 跨 cell event archtest） | 7 | ~5d | 7d / 4d（batch1 4 worktree 并行；batch2/3 串） |
| **Wave 3** — P2 架构延展 + INTERNAL-LISTENER-FULL + F3 Selector 收尾 + DevTools 元数据出口 | v1.1 kernel 子模块 + listener 完整版 + gocell-web 自包含化 | 10 | 12.25d | 17.5d / 10d |
| **Wave 4** — P3 长期架构演进 + refresh opaque 收尾 | 分层重整 / 接口拆分 / 类型保护 / refresh polish | 6 | 8.25d | 11.5d / 7d（PR-A19 已早期完工剔除） |
| **合计** | | 48 | **~46.25d** | **~65d / ~39d**（Wave 2.5 +7 PR / +5d） |

> **v1.0 路径（Wave 1 + Wave 2）**：净编码 ~21d；含 buffer **单人 ~29d（~6 周）/ 双人并行 ~16-17d（~3-4 周）**。
>
> Buffer 1.4x 乘数含 review 往返、integration 调试、Cx3/Cx4 ADR 讨论。实际 PR 编码时间通常只占项目时间 60-70%。

> **⚠️ 发布前必做（🔴 硬约束）**：PR-A25 ✅（#244） / PR-A26 ✅（#247） / PR-A14a ✅（#237） / PR-A27 ✅（#216） / PR-A28 ✅（并入 #227）+ PR-A5a ✅（#234）+ PR-A34 OUTBOX-SAFETY-GATING ✅（已随 #245 收口）+ **PR-A29 AUTH-REFRESH-MAIN ✅ PR#251**（2026-04-25）— **🎯 v1.0 发布硬约束全部清零**。
> **🟡 已完工基石（不占 Wave 计划）**：F1 JWT Registry / F3 Selector 基础设施 / F5 Errcode Classifier / F6 Lifecycle 基础设施 / L10 RoleInternalAdmin / S42 ROLELIST-CURSOR。详见末尾"已完工基石声明"章节。

---

## Wave 1 — 低风险抽取 + 治理（~5 工作日）

> 先落这批：纯 helper 抽取 + governance 规则扩展 + 入口缩减，review 快、冲突小、可并行 worktree。
>
> **状态（2026-04-24 晚间）**：Wave 1 15 条 PR **全部 ✅**（PR-A1/#226 · PR-A2/#225 · PR-A3/#227 · PR-A4/#228 · PR-A5a/#234 · PR-A5b/#238 · PR-A6 🟡主线入 #224 S4/S41 残余 · PR-A7/#241 · PR-A8/#230 · PR-A14a/#237 · PR-A25/#244 · PR-A26/#247 · PR-A27/#216 · PR-A28 并入 #227 · PR-A34 由 #245 F9 收口）。

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

### PR-A2 pkg 共享 helper 三连 ✅ 已落地 PR #225（实际净编码 5-6h）

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

### PR-A3 入口收口 + per-cell adapter ✅ 已落地 PR #227（2026-04-24，实际 ~10h）

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

### PR-A5b configcore cell.go 拆分 + config_repo 错误归类 ✅ 已落地 PR #238（2026-04-24，预计 3h → 实际 5-6h 随 A5a review 尾巴并入）

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

### PR-A6 EventRouter 身份拆分 + typed event payload + marshal err 显式 ✅ 已落地 PR #250（2026-04-24）

> **状态**：✅ 全部清零。PR220-5 EVENTROUTER-SUBSCRIPTION-IDENTITY-SPLIT-01 由 PR #224 先行落地（`kernel/outbox/subscription.go` `ConsumerGroup` + `CellID` 双字段 + `TraceIdentity()` helper）；S4 EVENT-PAYLOAD-TYPED-01 + S41 MARSHAL-ERR-EXPLICIT-01 由 PR #250 收口（typed event payloads + camelCase wire format + `outbox.Emit[T]` 泛型 helper 消除 `payload, _ := json.Marshal(map[string]any{...})` 模式）。
>
> **PR #250 round-1 review 残余 backlog**：`PR250-F1 SENSITIVE-FLAG` / `PR250-F2 USER-FLAG-EVENT-CAMELCASE-FOLLOWUP` / `PR250-F3 WIRE-BYTE-PINNING-TEST`，详见 `docs/backlog.md`。

**主线**：
- **PR220-5 EVENTROUTER-SUBSCRIPTION-IDENTITY-SPLIT-01** `ConsumerGroup`（broker）与 `CellID`（observability）拆两字段（3h）

**搭车**：
- **S4 EVENT-PAYLOAD-TYPED-01** 6 event 的 `map[string]any` → typed struct（3h）
- **S41 MARSHAL-ERR-EXPLICIT-01** `sessionlogin/service.go:140` + `sessionlogout/service.go:90` 两处 `_, _ = json.Marshal(...)` 改为显式处理（1h）

**搭车理由**：都触及 subscription 注册面 + event payload contract；S4 改事件 schema 时 EventRouter 接口同时用到；S41 正好是 sessionlogin/logout 的事件发布路径，S4 把 `map[string]any` 改 typed struct 时原地消除 `_, _ =`。

**文件面**：`runtime/eventrouter/` + `kernel/outbox/` + `cells/*/cell.go` + 6 个 event `service.go` + event contract schemas

**风险**：中；broker queue 命名和 observability label 解耦，现有测试需同步跑通。

---

### PR-A7 sessionmint 统一入口 + P1-A 收尾 ✅ 已落地 PR #241（2026-04-24，实际 ~4h）

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

### PR-A14a INTERNAL-LISTENER-MIN ✅ 已落地 PR #237（2026-04-24，🔴 发布前必做，~7h；**彻底重构版，吸收 PR-A32**）

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

### PR-A25 AUTH-PROD-HARDENING ✅ 已落地 PR #244（2026-04-24，实际 ~5h，2026-04-24 六席位复核确认 P1 阻塞性）

**实际主线**：
- **S-nonce SERVICE-TOKEN-NONCE-STORE-ENFORCE-01** ✅
  - (a) `controlplane.internalGuardFromEnv` 默认构造 `auth.NewInMemoryNonceStore(ttl=ServiceTokenMaxAge+30s buffer)` — 无 Redis 依赖即可落地 anti-replay
  - (b) 新增 `auth.WithServiceTokenNonceStore(store)` option，允许 Redis/持久化实现覆盖 in-memory
  - (c) `SharedDeps.Validate()` real 模式：NonceStore 未注入或为 Noop → fail-fast `ErrControlplaneNonceStoreMissing`（对齐 Vault real-mode guard 风格）
  - (d) 集成测试：replay 用例覆盖（同 token 第二次 401 `ERR_AUTH_UNAUTHORIZED`）；两 pod 场景交 X1 后 Redis store 独立 PR
- **S32 CONTROLPLANE-TOKEN-PROD-GATE-01** ✅ real 模式断言 service-token ring 已配置（非空 secrets），未来 mTLS 接入后改为至少一项

**review 追加修复（round 1 + round 2 共 ~3h，合并 `docs/reviews/202604241700-pr244-reviewer.md` / `202604241915-pr244-reviewer-round2.md` / `202604242030-pr244-fix-diagnosis.md`）**：
- `runtime/auth/nonce.go` + `runtime/auth/nonce_test.go` InMemoryNonceStore 扩展 + 修复；`runtime/auth/servicetoken.go` + `_test.go` ring 构造面补齐；`cmd/corebundle/controlplane_guard_test.go` real-mode negative 路径

**文件面**（交付已实）：`runtime/auth/{authenticator,nonce,servicetoken}.go` + `runtime/auth/*_test.go` + `cmd/corebundle/{controlplane,shared_deps,bundle}.go` + `cmd/corebundle/{controlplane_guard,auth_integration,main}_test.go` + `runtime/bootstrap/topology.go` + `docs/ops/env-vars.md` + `.claude/rules/gocell/runtime-api.md`

---

### PR-A26 AUTH-SETUP-ENDPOINT ✅ 已落地 PR #247（2026-04-24，🔴 v1.0 必做 P0，实际 ~4h 主线 + 扩展到 adminprovision 共享服务）

**实际主线**：
- **P1-19 AUTH-SETUP-ENDPOINT-01** ✅
  - ① 新 slice `cells/accesscore/slices/setup/`（handler / service / contract_test / service_test）
  - ② 新 contract `contracts/http/auth/setup/status/v1/` + `contracts/http/auth/setup/admin/v1/`
  - ③ 端点路径按 Consul per-cell 模式挂在 `/api/v1/access/setup/*`（**非** 顶级 `/api/v1/setup/*`，避免与未来 system-level 初始化端点命名冲突）
  - ④ 两端点 `auth.Declare Public: true`；409/410 快速短路，bcrypt 仅在成功路径触发
- **超出原范围的彻底方案**：抽出 `cells/accesscore/internal/adminprovision/`（`provisioner.go` + `provisioner_test.go` + `doc.go`）共享给 `initialadmin.Bootstrap` 和 `setup.Service`；sync.Mutex 覆盖单进程原子性，orphan recovery 语义（reset password 清除 `require_reset` flag 且不重复发 `user.created` 事件）

**round-2 review 登记 backlog**（4 条 A26-R1~R4）：
- **A26-R1** ADMINPROVISION-DIST-LOCK-01（Cx3, 🟠 PG adapter 接入时触发）— 多实例需 `pg_advisory_xact_lock`
- **A26-R2** SETUP-ADMIN-RATE-LIMIT-01（Cx2, 🟡, 取决于是否面向公网）— per-IP rate-limit / 一次性 bootstrap token
- **A26-R3** SETUP-PATH-NAMESPACE-POLICY-01（Cx1, 🟡）— 在 `api-versioning.md` 明确"顶级 `/api/v1/setup/` 保留给 system-level；per-cell 挂 `/api/v1/{cell}/setup/*`"
- **A26-R4** SETUP-ORPHAN-E2E-01（Cx2, 🟠 PG adapter 接入时触发）— orphan recovery E2E

**文件面**（已实交付）：新 `cells/accesscore/slices/setup/`（6 文件） + 新 `cells/accesscore/internal/adminprovision/` + 新 `contracts/http/auth/setup/{status,admin}/v1/`（6 文件） + `cells/accesscore/initialadmin/bootstrap.go`（重构使用 adminprovision） + `cells/accesscore/cell_{init,routes}.go` + `cells/accesscore/slices/identitymanage/service.go` + `cmd/corebundle/setup_integration_test.go`（新） + `pkg/errcode/errcode.go` + `pkg/httputil/response.go`

---

### PR-A27 CONFIGWRITE-RETURNING-CONSOLIDATE ✅ 已落地 PR #216（2026-04-21，先于本 plan 创建日；plan 原文把 PR#216 描述为"flagwrite 模式"有误——PR#216 本身就是 configwrite 自己的 TOCTOU 修复）

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

### PR-A34 OUTBOX-DIRECT-SAFETY-GATING ✅ 由 PR #245 F9 per-entry FailurePolicy 收口（2026-04-24）

**原设计方案**：`DirectEmitter.SafetyCriticalTopics` 白名单字段 + per-cell 注入式声明。

**实际落地（更优方案）**：PR #245 Round-1 review 追加的 **F9 OUTBOX-EMITTER-PER-ENTRY-FAILURE-POLICY** 以 k8s apiserver audit `Backend.FailurePolicy(Ignore/Fail)` 模型彻底替代了"构造期 topic 白名单"设计：
- `kernel/outbox.Entry.FailurePolicy` 字段（`Default` / `FailOpen` / `FailClosed`）把失败语义从构造期下沉到 entry 层
- `DirectEmitter.Emit` 读 entry 策略；fail-open 路径 log 扩展 `entry_id` + `event_type`
- **accesscore + configcore + auditcore 三 cell 默认改 `DirectPublishFailClosed`**（安全事件发布失败不再被静默吞掉）
- 非关键事件 per-entry opt-in `FailurePolicyFailOpen`
- **新 archtest `OUTBOX-TOPIC-FAILOPEN-01`** 扫 `cells/**/*.go` 禁止 `session.*`/`user.*`/`role.*`/`audit.*` topic 字面量设 `FailurePolicyFailOpen`；5-case regression fixture 验证守卫有效
- 删除原 accesscore / auditcore "session revocation rare / dropping acceptable" 辩护注释

**ref**：`kubernetes apiserver/pkg/audit Backend.FailurePolicy` + `ThreeDotsLabs/watermill retry middleware per-message disposition`

**原 PR-A34 独立 PR 无需再开**；backlog `S-outbox-safety` 可关单。

---

## Wave 2 — 中等架构收口（~13 工作日）

> **状态（2026-04-25 第十三轮）**：✅ **Wave 2 主线 100% 完工**——已落 **PR-A6/#250 · PR-A9/#239 · PR-A10/#261 · PR-A11/#246 (+#257 FU1) · PR-A12/#249 · PR-A12b/#252 · PR-A13/#235 · PR-A29/#251 · PR-A30/#263 · PR-A31/#259 · PR-A5c/#224+#245**。Wave 2 已无待办主线。

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

### PR-A10 OUTPUT-JSON-SARIF 诊断模型 ✅ 已落地 PR #261（2026-04-25，~6h）

**主线**：
- **P1-4 OUTPUT-JSON-SARIF-01** ✅ 统一诊断模型（单一 `Issue` struct → text/JSON/SARIF 三 printer）+ DX hints

**搭车**：无（独立 refactor，不挂 review 其他内容）

**文件面**：`cmd/gocell/` + `kernel/governance/` 序列化

**风险评估**：低-中；输出格式改动已通过 CI 与 SARIF schema 校验。

---

### PR-A11 KERNEL/WRAPPER ✅ 已落地 PR #246（2026-04-24，P1，主线 ~1d + 后续 PR246-FU1 PR#257）

**主线**：
- **LATER-K1 KERNEL/WRAPPER** ✅ 契约级可观测代理（Traced wrapper）落地为 `kernel/wrapper/` 新模块；接收 PR-A9 落地后的完整 Method/Path/PathParams 做 trace span 标注

**post-merge 收口（PR246-FU1 ✅ PR#257）**：三条 P1 correctness/security fix —
- `auth.Mount` prefix fail-fast（无前缀路由注册时拒绝 mount）
- 路径段边界正确性（防止 `/foo` 误匹配 `/foobar`）
- typed observability metadata precedence（wrapper 自带 metadata 覆盖默认推断）
- FMT-19 规避语法 AST 测试覆盖

**搭车**：无（独立新模块）

**文件面**：`kernel/wrapper/`（新）

**依赖**：PR-A9 CONTRACT-META-01 落地后，wrapper 能拿到完整 Method/Path 信息做 trace span 标注。

**风险**：中；Tracing 埋点语义定义需 ADR。

---

### PR-A12 KERNEL/COMMAND ✅ 已落地 PR #249（2026-04-24，P1，~2.5d）

**主线**：
- ✅ **LATER-K2 KERNEL/COMMAND** 新包 `kernel/command/` 提供完整 L4 命令队列底座：`Status`（7 态 iota+1，0 非法）/ `Entry` / `Timeouts` 三阶段（ScheduleToSend/SendToComplete/OverallDeadline）/ `Transition`+`AdvanceCommand`+`ResetForRetry` 纯函数 / `Queue` 门面（Enqueue/Dequeue/Report/Ack/ExtendLease/Cancel）/ `ActiveScanner` 运维扫描口 / `Sweeper` 过期驱动器 / `QueueRegistrar` 可选 Cell 接口 / `commandtest.InMemQueue` 测试后端。代码从 `kernel/outbox/l4.go`（已删）搬家，SRP 收敛——outbox 专注 L2/L3 事件 fanout，command 专注 L4 unicast + ack。

**搭车**：
- ✅ `examples/iotdevice/cells/devicecell/slices/devicecommand/` 彻底迁移到 `kernel/command.Queue`（删除 `domain.Command` + `CommandRepository`；handler DTO 新增 `status`/`attempt`/`completedAt`；request 新增可选 `commandType`，默认 `"default"`）。contract schemas（HTTP + command kind 各 3 × 2）同步重塑。
- ✅ PR-A12b 收口 L4 Queue 语义：外部 GET 改为 `Dequeue` claim+lease，新增 `Report(commandID)` 推进 Delivered，Ack body 增加 `reason` 并单步终态推进；新增 `runtime/command.SweeperLifecycle` 与 `DiscoverQueueRegistrars`，`devicecell` demo 模式接入全局 sweeper；内部运维路由 `/internal/v1/devicecommands` 走 `ActiveScanner.ScanActive`。
- ✅ **T3 DEVICE-ENQUEUE-RBAC** 预埋点 `command.EnqueueOptions.Authz command.AuthzFunc` 就绪；本 PR 不接线（demo 模式 Authz=nil），T3 真正落地只需在 handler 构造 AuthzFunc 传入，无需改 kernel。

**对标**（commits 已记录）：`ThreeDotsLabs/watermill message/router.go` Ack/Nack / `kubernetes/kubernetes staging/src/k8s.io/client-go/util/workqueue` Add/Get/Done + ShutDownWithDrain / `nats-io/nats.go jetstream Msg` InProgress/NakWithDelay/Term / `rabbitmq/amqp091-go channel.go` Ack/Nack(requeue=false)→DLX / `temporal Nexus` 三阶段 timeout。

**覆盖率**：`kernel/command` 98.7%（kernel 层 ≥90% 达标）；`kernel/command/commandtest` 68.6%（test-helper 子包不受 90% 门禁约束）；`examples/iotdevice/cells/devicecell` 86.2%、`devicecommand` 83.3%、`mem` 83.3%（≥80% 达标）。

**残余 backlog**：PR-A12b 已消解 `PR-A12-ACK-ATOMIC` 与 `PR-A12-SWEEPER-WIRE`；postgres command store 仍是独立 adapter 主题，不并入本条。

**风险落地**：中；定义 L4 下发语义首批采纳；`kernel/command` 作为未来设备管理类 cell 的设计范式。

---

### PR-A12b KERNEL/COMMAND 生命周期收口 ✅ 已落地 PR #252（2026-04-25，P1，~1d）

**主线**：
- 在 PR-A12 的 queue 底座上补完整命令生命周期：**Dequeue / Report / Ack / ExtendLease**。
- 引入 **ActiveScanner**，支持 active command 查询与 timeout/sweeper 路径的数据源。
- devicecell command slice 迁移到领取、回报、确认、续租的闭环语义，并补 HTTP + command contract/test。

**已处理 review 收口**：
- Ack 并发幂等：同 reason 并发成功幂等，不同终态 reason 只允许一个赢家。
- ExtendLease 增加最大续租上限，schema 与 service 双层校验。
- `ack.reason` contract 与 handler 接受值对齐；internal active list contract 补齐。

**backlog 残余**（不混入本 PR）：
- **PR252-F1 COMMAND-QUEUE-REGISTRAR-BOOTSTRAP-FAILFAST-01**：QueueRegistrar 发现、注入与 capability 校验纳入唯一 bootstrap phase，缺 queue/scanner fail-fast。
- **PR252-F2 COMMAND-SWEEPER-PRODUCTION-GOVERNANCE-01**：sweeper 增加 leader/分片 ownership、连续失败 readiness 降级、scan/Ack 指标。

**文件面**（已实交付）：`kernel/command/` + `runtime/command/` + `examples/iotdevice/cells/devicecell/slices/devicecommand/` + `examples/iotdevice/contracts/{http,command}/device-command/` + `examples/iotdevice/contracts/http/internal/devicecommands/list/v1/`。

---

### PR-A13 PR#220 遗留：文档事实源重写 + DOC-NAMING-GUARD 启用 ✅ 已落地 PR #235（2026-04-24，~6h）

> 合并范围：PR220-1 文档事实源重写 + PR220-1b iotdevice envelope + PR220-e1 naming baseline + PR220-e3 J-ordercreate checkRef + PR220-2 DOC-NAMING-GUARD-01 全部一批落地。

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

### PR-A29 AUTH-REFRESH-MAIN ✅ 已落地 PR #251（2026-04-25，🔴 发布前必做 → 已清零，X10 + X11 + X15 一批）

**实际交付**：
- **X10 AUTH-REFRESH-OPAQUE-01** ✅ refresh token 已切为 opaque selector/verifier + server-side append-only rotation store；JWT refresh 发行路径删除
- **X11 REFRESH-HMAC-SPLIT-01** ✅ selector 明文查找 + SHA-256(verifier) 存储；migration 012 重建 schema，并增加 destructive hard gate
- **X15 REFRESH-OPAQUE-INTEGRATION-01** ✅ sessionlogin / sessionrefresh 已接入 opaque `refresh.Store`；corebundle 接入 refresh GC lifecycle + metrics
- 实际 migration 编号为 012（不是原计划的 009），因 PR-A29 落地时间晚于 010/011 占位

**硬依赖落地**：X11 与 X15 通过单 PR 内合并落地，避免数据迁移。

**搭车**：无。

**文件面**（已实交付）：`adapters/postgres/refresh_store.go` + migration 012 + `runtime/auth/refresh/` + `cells/accesscore/slices/sessionlogin/service.go` + `cells/accesscore/slices/sessionrefresh/service.go` + `cells/accesscore/access_module.go` + `cmd/corebundle/`

**解锁**：PR-A30 AUTH-TEST-COVERAGE（依赖 PR-A29 的 opaque path 测试用例）+ PR-A33 REFRESH-OPAQUE-POLISH（X12 idle / X13 partition / X14 grace counter，已转 backlog 待 Wave 4）。

---

### PR-A30 AUTH-TEST-COVERAGE ✅ 已落地 PR #263（2026-04-25，~6h，Wave 2 最后一条主线）

**主线**（实际交付）：
- **S19 JWT-AUDIENCE-DRIFT-INTEG-TEST-01** ✅ `cells/accesscore/auth_integration_test.go::loginAndGetPair` 重构为 options + struct return（`withIssuerAuds` / `withVerifierAuds` 注入 drift），新增 `TestAuthIntegration_LoginAccessTokenAudienceDrift` 覆盖 issuer-drift / verifier-drift / multi-aud-one-match / aligned 四象限
- **S21 JWT-AUD-TEST-TABLE-DRIVEN-01** ✅ `runtime/auth/jwt_aud_test.go` 13 个 Test\* 折叠为 3 个 table-driven（`TestJWTVerifier_VerifyIntent_AudienceTable` 9 行 + `TestJWTIssuer_DefaultAudience_Table` 3 行 + `TestNewJWTVerifier_NoAudiences_ReturnsError` 构造探针）
- **S22 REFRESH-AUD-REAL-ROUTE-TEST-01** ✅ 新 `cells/accesscore/auth_refresh_aud_integration_test.go::TestAuthIntegration_RefreshAccessTokenAudienceDrift`（refresh token 已 opaque/PR-A29 后，audience 在 response access JWT 上验证）
- **S24 AUTH-MIDDLEWARE-AUD-REFRESH-E2E-01** ✅ 新 `runtime/auth/middleware_aud_e2e_test.go` 单一 table test 覆盖 `httptest.NewServer` + `AuthMiddleware` 完整链路（right/wrong/missing aud → 200/401/401）

**实际特点**：纯测试 PR，零生产代码改动；Closes Wave 2 PR-A30（v1.0 前最后一条主线）

**搭车**：无

**文件面**：`runtime/auth/jwt_aud_test.go` + `runtime/auth/middleware_aud_e2e_test.go`（新）+ `cells/accesscore/auth_integration_test.go` + `cells/accesscore/auth_refresh_aud_integration_test.go`（新）

**依赖**：PR-A29 ✅ PR#251 已合（refresh 主链 opaque 切换后，本 PR 与新路径对齐）

**post-merge 修复**：PR #265 修复 PR-A20 #260 残留的 distlock TestManager_HeapOrder flaky race（在 PR#263 上首次 surface）

---

### PR-A31 AUTH-FIRSTRUN-DX ✅ 已落地 PR #259（2026-04-25，~2h）

**主线**：
- **C2-A LOGIN-USERID-RESPONSE** ✅ `sessionlogin/service.go` 登录响应补 `userId` 字段
- **C2-B 403-HINT-RESOLVED-PATH** ✅ `runtime/auth/middleware.go` 403 错误 hint 包含 resolved path
- **C2-C README-MACOS-BASE64** ✅ `examples/ssobff/README.md` macOS `base64` flag 可移植化（去 Linux 特定参数）

**搭车实际**：collapse TokenPair 重复 ✅（合并 PR-A29 落地后 sessionlogin/sessionrefresh 共享的 token mint 收口）

**文件面**：`cells/accesscore/slices/sessionlogin/` + `runtime/auth/middleware.go` + `examples/ssobff/README.md`

**风险评估**：低；DX 打磨。

---

### PR-A5c OUTBOX-EMITTER-UNIFY — ✅ **主线 PR #224（2026-04-23）+ 全量收口 PR #245（2026-04-24）**

> 分支 `refactor/520-pr-a5c-outbox-emitter-unify`。净编码 ~18h（round-1 review 后扩容 4h：CI fix + F2 推翻 + F9 per-entry FailurePolicy + 新 archtest）。实际交付范围：
> - ❌ **~~F2 DIRECTPUBLISHMODE-HELPER-01~~** — 初版引入 `DirectPublishModeForDurability(mode, demoPolicy, durablePolicy)` helper 作为 A5a-R4 合规化，但 round-1 review 指出该 helper 只是合规化错误决策：per-Cell 构造期 failure mode 不能表达"安全 topic 必须 fail-closed，观测 topic 可 fail-open"。**删除 helper 及其 4-case 表驱动测试**，改走 F9。A5a-R4 改以 F9 per-topic policy 形态收口。
> - ✅ **F3+F7 CELL-OPTION-API-UNIFY** — 三 cell 删除 `WithPublisher`/`WithOutboxWriter` Option + `publisher`/`outboxWriter` 公开字段；新增 `WithEmitter(outbox.Emitter)` + `WithOutboxDeps(pub, writer)` 互斥 Option；29 处调用点迁移；`kernel/outbox.DurabilityReporter` 接口 + `WriterEmitter`/`DirectEmitter` 实现；`configcore.WithPostgresDefaults(pool, writer)` 拆成 `WithPostgresPool(pool)` + `WithOutboxDeps`。
> - ✅ **F4 CELL-ROUTES-PROVIDERS-SPLIT** — `cells/accesscore/cell_providers.go` 从 `cell_routes.go` 抽出；configcore/auditcore 本就无 provider 方法，不强拆。A5a-R5 收口。
> - ✅ **F5 ENVELOPE-WRAPPER-DELETE** — 删除 `runtime/outbox/envelope.go`+`envelope_test.go`；`runtime/outbox/relay.go` / `runtime/eventbus/eventbus.go` / `adapters/rabbitmq/subscriber.go` 切 `kernel/outbox`。integration-tag 面迁移尾巴由 round-1 CI fail 发现，补齐 `adapters/rabbitmq/integration_test.go` (6 处) + `tests/integration/shutdown_e2e_test.go` (1 处)，完工后本地 `go build -tags=integration ./...` 绿。
> - ✅ **F6 ARCHTEST-CELL-01** — `tools/archtest/outbox_cell_test.go` 新增规则 OUTBOX-CELL-01，扫描 `cells/<name>/cell.go` 禁止 `WithPublisher`/`WithOutboxWriter` 导出 Option；regression-verified。
> - ✅ **F9 OUTBOX-EMITTER-PER-ENTRY-FAILURE-POLICY**（round-1 review 追加）— `kernel/outbox.Entry` 增 `FailurePolicy` 字段（`Default`/`FailOpen`/`FailClosed`，`json:"-"` 不 marshal 到 wire）；`FailurePolicy.Resolve(ctorDefault)` 零值回退到 Emitter 构造期默认；`DirectEmitter.Emit` 读 entry 策略。三 cell 默认 `DirectPublishFailClosed`（k8s apiserver audit 模型），安全/审计事件发布失败默认上抛；非关键事件 per-entry opt-in FailOpen。新 archtest `OUTBOX-TOPIC-FAILOPEN-01`（`tools/archtest/outbox_topic_test.go`）扫 `cells/**/*.go` 禁止 `session.*`/`user.*`/`role.*`/`audit.*` topic 在 `outbox.Entry` 字面量上设 `FailurePolicyFailOpen`；regression fixture 5 case。
>
> **5 条主线由 PR #224 + PR #245 合力完成**：
> - PR #224 主线（2026-04-23）：(1) Emitter 接口 + DirectEmitter/WriterEmitter；(2) wire envelope 从 `runtime/outbox` 下沉到 `kernel/outbox`；(3) accesscore + configcore + auditcore 三 cell L2 slice service 全部迁到 `persistence.TxRunner` + `outbox.Emitter`；(4) Cell 边界 `RunnerOrNoop` + demo/durable 四象限解析；(5) archtest 4 规则（`tools/archtest/` 已建）。
> - PR #245 全量收口（2026-04-24）：F2/F3+F7/F4/F5/F6 六批 Cell Option 统一 `WithEmitter` / `WithOutboxDeps`，去除主线外所有 cell 原生 publisher/writer 字段；envelope kernel 迁移补齐。v1.0 前该抽象门槛归零，不再作为 Wave 2 建议项。
>
> 核心子项之前已由 **PR #224**（outbox emitter refactor）+ **PR-A5a PR#234**（`cell.ResolveEmitter` 抽取 + 10 case 测试）+ **PR-A5b PR#238** retroactively 落地（EMITTER-ABSTRACT / ENVELOPE-KERNEL-DOWN / SERVICE-EMITTER-MIGRATE / CELL-BOUNDARY-RESOLVE / ARCHTEST-NIL-MODE-BLOCK 共 5 子项），本 PR 完成剩余尾巴收口 + F9 per-entry FailurePolicy 对抽象层的补强，让"Cell 层只依赖 `outbox.Emitter` 抽象、失败语义由 entry 自带"成为架构硬保证（双 archtest 守卫）。

<details>
<summary>原计划 Cx3 方案（保留存档，仅作历史对照）</summary>

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
- envelope 下沉 kernel 与 ~~PR-A19~~ ✅ PR#177 AL-01（relay → runtime）正交：契约属 kernel，运行时属 runtime，分层更清晰
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

</details>

---

## Wave 2.5 — Config 域审查回灌（~5 工作日，PR-CFG-1..7）

> 来源：2026-04-25 config 域全链路静态审查（develop @ `788e03e3` 快照），8 条 finding（6×P1 + 2×P2）
>
> **与 baseline review 2026-04-05 的关系**：[`docs/reviews/archive/baseline-review-report-2026-04-05.md`](../reviews/archive/baseline-review-report-2026-04-05.md) §7 识别 4 根因簇——本批次是这些根因簇在 config 域的残余 instance 清零 + 平台治理硬化（防止其他 cell 再种类似问题）：
>
> | baseline §7 根因簇 | 涉及 PR-CFG | 残余原因（2026-04-25 仍存在的 instance） |
> |---|---|---|
> | 7.1 Fractured Source Of Truth | PR-CFG-3, PR-CFG-4 | event 在 producer Go 包 / contract / consumer cache 三处建模（accesscore 直 import `cells/configcore/events`）；config DTO 加了 sensitive 字段但 response.schema.json 未声明 |
> | 7.2 Non-Canonical Project Graph | PR-CFG-2 | `gocell validate` 没有"event contract 必须有非空 publishers + subscribers"规则，allow `flag.changed.v1` 死事件长期存在 |
> | 7.3 Verification Is Not Executable Reality | PR-CFG-1, PR-CFG-5, PR-CFG-7 | `Relay.HealthCheckers()` 实现了无人调（readyz 假绿）；real 模式 + local-aes 仍能启动（启动 gate 假绿）；`tests/e2e/config_pilot_e2e_test.go` 全部 `t.Skip` 但 CI 绿（验证假绿） |
> | 7.4 Runtime Kernel Is A Leaky Stub | PR-CFG-1, PR-CFG-6 | ManagedResource 的 HealthCheckers 没有自动聚合到 readyz 治理约束；fail-open silent drop 无 counter |
>
> **设计原则**：架构治理先行 (PR-CFG-1/2/3) → 业务修跟 (PR-CFG-4/5/6/7)；治理 PR 与最后一处违规清零同 PR，避免 allowlist 维护
>
> **状态（2026-04-25 第十二轮登记后）**：7 PR 全部待开，已登记 `docs/backlog.md`。

### 执行批次

**batch1 — 架构基线先落（4 worktree 并行，~1.5d 净）**：
- PR-CFG-1 READYZ-MANAGEDRESOURCE-AUTO-AGGREGATE-01（4h，治理 P1，🔴）— 关联 baseline §7.4 + §7.3
- PR-CFG-2 EVENT-DEAD-SUBSCRIBER-VALIDATE + FLAG-CHANGED-RETIRE（3h，治理+清理，🟡）— 关联 baseline §7.2
- PR-CFG-3 EVENT-CONTRACT-BOUNDARY-RECOMPLY-01（2d，安全/契约/架构 P1，🔴 Cx4）— 关联 baseline §7.1，supersedes backlog `PR250-F1`
- PR-CFG-5 KEYPROVIDER-LOCALAES-REAL-MODE-REJECT-01（1h，业务 P1，🔴）— 关联 baseline §7.3

**batch2 — 治理基线落地后业务修（3 worktree 并行，~1d 净）**：
- PR-CFG-4 CONFIG-READ-METADATA-ADMIN-GATE-01（1d，安全 P1，🔴）— 与 batch1 任意 PR 文件无冲突，可并行
- PR-CFG-6 OUTBOX-EMIT-FAILOPEN-DROP-COUNTER-01（2h，ops，🟡，依赖 PR-CFG-1）— 吸收 backlog `MULTI-REVIEW-RES-2`

**batch3 — 测试基建（1d）**：
- PR-CFG-7 CONFIG-PILOT-E2E-CONDITIONAL-SKIP-01（1d，测试，🟡，依赖 PR-CFG-3 — 等 entry-upserted metadata-only 落地后写真实 consumer 副作用断言）

### 7 PR 详细范围（详见 backlog 同名条目）

#### PR-CFG-1 READYZ-MANAGEDRESOURCE-AUTO-AGGREGATE-01（治理 P1，🔴 Cx2，~4h）

**问题**：`runtime/outbox/relay.go::HealthCheckers()` 已实现 `outbox-relay-poll/reclaim/cleanup` 三 budget probe 但**全仓零调用方**——relay 连续失败时 HTTP/DB readiness 仍绿（假绿就绪）。
**修复**：`kernel/lifecycle.ManagedResource` 注册时自动收集 `Checkers()` → bootstrap phase5 统一聚合到 readyz handler → archtest 拦"adapter 暴露 HealthCheckers 但未通过 ManagedResource 注册"。
**关联**：吸收 backlog `MULTI-REVIEW-RES-2` 的 readyz 降级部分。
**ref**：Kubernetes `healthz.go` post-start hook 命名检查可单项诊断；Kratos transport server 生命周期与 health 联动；Temporal worker heartbeat 后台 worker 失败独立状态源。
**文件**：`kernel/lifecycle/managed_resource.go` + `runtime/http/health/health.go` + `runtime/bootstrap/bootstrap_phases.go` + 测试。

#### PR-CFG-2 EVENT-DEAD-SUBSCRIBER-VALIDATE-01 + FLAG-CHANGED-RETIRE（治理+清理 P1+P2，🟡 Cx2，~3h）

**问题**：`event.flag.changed.v1` contract subscribers 为空、无 cell 消费，是正式架构里的死事件。
**修复（同 PR）**：(1) 治理：`kernel/governance/contracthealth.go` 加 `CONTRACT-DEAD-EVENT-01`（event kind contract 必须有非空 publishers + subscribers，`lifecycle: deprecated` 给豁免）；(2) 业务：删 `flagwrite/service.go` emit + contract.yaml 标 `lifecycle: deprecated`。
**为什么同 PR**：单合规则会因 flag.changed 立即 CI 红，必须同 PR 清最后一处违规。
**文件**：`kernel/governance/contracthealth.go` + `cells/configcore/slices/flagwrite/service.go` + `contracts/event/flag/changed/v1/contract.yaml`。

#### PR-CFG-3 EVENT-CONTRACT-BOUNDARY-RECOMPLY-01（激进方案）（安全/契约/架构 P1，🔴 Cx4，~2d）

**问题**（三件同 PR）：
- (a) `event.config.entry-upserted.v1` contract 宣称 "subscribers should apply current value"，但 producer 对 sensitive entry 主动降级为 `******` 占位符，subscriber 还把占位符写进本地 cache——同一事件混用 metadata 通知 + 可应用状态语义
- (b) `cells/accesscore/slices/configreceive/service.go:11` 直接 `import configevents "github.com/ghbvf/gocell/cells/configcore/events"`，跨 cell 边界退化为 Go 包依赖，contract 不再是唯一事实源
- (c) gocell 平台没有 archtest 拦"cells/X import cells/Y/{events,internal,**}"

**修复（激进方案，三件同 PR — 用户 2026-04-25 拍板）**：
1. **B1b**：`contracts/event/config/entry-upserted/v1/payload.schema.json` 删 value 字段，事件变 metadata-only 通知（"key/version 变了，请来取"，对标 NATS/Watermill/go-micro 边界模型）；删 configsubscribe 占位符 cache；改 producer 不再发 value
2. **B1c**：删 accesscore configreceive 对 `configcore/events` 的 import；新建 `cells/accesscore/internal/dto/config_event_decoder.go` 本地 decode（schema 仍是 contracts/ 单一真理源）
3. **G1**：`tools/archtest/cell_boundary_test.go` 新增规则禁止 `cells/X` import `cells/Y/{events,internal,**}`

**为什么三件同 PR**：B1b 改 schema → B1c consumer 跟着改 → G1 archtest 装上来时违规已清零；任意一件单合都会让另两件爆雷或要 allowlist 维护。
**Supersedes**：backlog `PR250-F1`（删 wire `sensitive` 字段决策一并落地）。
**Cross-ref**：触发条件项 `T6 CONTRACT-EVENT-PAYLOAD-CODEGEN-01` 后续可用 codegen 替换 B1c 手写 decoder。
**ref**：Watermill `cqrs/marshaler_json.go`（边界是 payload bytes，typed decode 在 consumer 进程内）；go-micro `broker/broker.go`（发布端先编码 body，消费端按本地 handler 类型解码）；NATS JetStream（API 只处理 subject 和 bytes）。
**文件**：`contracts/event/config/entry-upserted/v1/payload.schema.json` + `cells/configcore/slices/{configpublish,configwrite,configrollback,configsubscribe}/service.go` + `cells/configcore/events/config_events.go` + `cells/accesscore/slices/configreceive/service.go` + `cells/accesscore/internal/dto/config_event_decoder.go`（新）+ `tools/archtest/cell_boundary_test.go`。

#### PR-CFG-4 CONFIG-READ-METADATA-ADMIN-GATE-01（安全 P1，🔴 Cx2，~1d）

**问题**：`cells/configcore/cell_routes.go:67-78` 把 `GET /api/v1/config/` 与 `GET /api/v1/config/{key}` 挂在 `auth.Authenticated()` 下，任意已登录用户都能枚举 key 列表 + 读 sensitive 元数据；同时 response.schema.json 未声明 `sensitive` 字段但真实 DTO 已写——contract-first 调用方误判线上响应非法。
**修复（同 PR）**：(1) 两条 GET 改 `auth.AnyRole(auth.RoleAdmin)`；(2) `contracts/http/config/{get,list}/v1/response.schema.json` 显式声明 `sensitive: bool`；(3) handler/service/DTO 校齐；(4) contract_test 加 `MustRejectResponse` 负向断言。
**ref**：Vault KV v2 `data` vs `metadata` 路径分离授权；K8s RBAC ConfigMap/Secret 默认不进 `view`；Consul KV 把 keys 枚举与 metadata/value 读取分离。
**文件**：`cells/configcore/cell_routes.go` + `contracts/http/config/{get,list}/v1/response.schema.json` + `cells/configcore/slices/configread/{handler,service,contract_test}.go`。

#### PR-CFG-5 KEYPROVIDER-LOCALAES-REAL-MODE-REJECT-01（生产 gate P1，🔴 Cx1，~1h）

**问题**：`cmd/corebundle/bundle.go::buildKeyProvider` 的 `case "local-aes":` 只检查 `rejectDemoKey`，未检查 `isRealMode(adapterMode)`。real 模式 + 一个非 demo 的随机 32 字节 hex key 仍能让 local-aes provider 启动，绕过 `vault_transit_ready` checker、token 续租 worker 与续租指标，但服务仍能宣告 ready。
**修复**：`buildKeyProvider` 在 `case "local-aes":` 首行加 `if isRealMode(adapterMode) { return errcode.New(...) }`；同步 `cmd/corebundle/demo_keys_test.go` 加正向用例。
**文件**：`cmd/corebundle/bundle.go` + `cmd/corebundle/demo_keys_test.go`。

#### PR-CFG-6 OUTBOX-EMIT-FAILOPEN-DROP-COUNTER-01（ops 可观测 P1，🟡 Cx2，~2h）

**问题**：原 backlog `MULTI-REVIEW-RES-2` 的 (1) counter + (2) archtest AST 残余——PR-CFG-1 落地后 readyz 部分已解决，本 PR 收口剩余两件。
**修复**：(1) `kernel/outbox/emitter.go` 增加 `gocell_outbox_emit_failopen_dropped_total{cell, topic}` counter；(2) archtest 升级用 AST 解析 topic 常量传播链路（参考 PR#257 PR246-FU1 FMT-19 AST 改造模式）。
**关联**：与 backlog `MULTI-REVIEW-RES-2` 合并实施；MULTI-REVIEW-RES-2 描述已同步收窄。
**文件**：`kernel/outbox/emitter.go` + `runtime/observability/metrics/` + `tools/archtest/outbox_topic_test.go`。

#### PR-CFG-7 CONFIG-PILOT-E2E-CONDITIONAL-SKIP-01（测试基建 P2，🟡 Cx2，~1d，🟠 触发：PR-CFG-3 合后）

**问题**：`tests/e2e/config_pilot_e2e_test.go` 唯一一条覆盖 HTTP → corebundle → PG → encryption → outbox → consumer 的 e2e 用例**全部无条件 `t.Skip("requires full assembly")`**——这正是 P1-1..P1-6 一系列问题能同时留在 develop 上不被发现的原因（baseline §7.3 残余）。
**修复**：(a) skip 改为条件 skip（`requireDocker(t)` / `requirePG(t)` / `requireRMQ(t)`）；(b) 补一条断言：发布 entry → accesscore configreceive 落库副作用可见（PR-CFG-3 落地后写真实 metadata-event 消费）；(c) `.github/workflows/_build-lint.yml` integration-test job 把 `tests/e2e/...` 加进 scope。
**前置**：PR-CFG-3 合后做，否则 consumer 形态会再变一次。
**Cross-ref**：与 backlog F10 (`TEST-JOURNEY-ASSEMBLY-HARNESS-01`) 不同——后者是 28 条 journey skip，本条是 config 域单条 e2e。
**文件**：`tests/e2e/config_pilot_e2e_test.go` + `.github/workflows/_build-lint.yml`。

---

## Wave 3 — P2 架构延展（~10 工作日）

### PR-A14b INTERNAL-LISTENER-FULL ✅ 已落地 PR #258（2026-04-24，Cx4，~1d）+ ✅ PR #262 round-3 follow-up（2026-04-25）

> **注**：已拆分成 PR-A14a（Wave 1 最小双 listener）+ PR-A14b（Wave 3 完整版）。本条是完整版。

**主线（实际交付）**：
- **R4 INTERNAL-LISTENER-FULL** ✅ **three-listener**（primary + internal + health）+ 完整 **RouteGroup 声明式 API**：`bootstrap.WithRouteGroup`、`bootstrap.WithHealthRoutes(WithReadyzPolicy(PolicyVerboseToken(...)))` 装配模式 + 编译期 listener 引用校验
- **依赖**：PR-A14a 已合入（primary + internal 双 listener 基座已稳定）；PR-A35 已合入（health 端点 envelope + verbose token 严格化）

**Round-2 review R2-01..R2-11 已合 follow-up commit `fc4e54e7`**（IN_SCOPE 全清零）：
- R2-01 + R2-04：verbose-token 双机制彻底合一（删 `WithVerboseToken` Option + `health.SetVerboseToken` handler 闸 + 重复 `readyzVerbose` / `policyVerboseActive`；新 `runtime/http/health/probequery` 子包）
- R2-03：`shutdownAllServers` parent ctx 上限闭合（`shutGrace` 不再可越过全局 `shutdownTimeout`）
- R2-05：`routerOpts` 类型化断言（`runtime.FuncForPC` 标识检查 vs 脆弱 `Len==2`）
- R2-06 / R2-07 / R2-11：unit test 缺口补全 + ssobff walkthrough_test 迁移到 RouteGroups（解 PR-A14b 起 integration-tag CI red）
- R2-09：Bootstrap 45 字段加 11 个 group 注释
- R2-10：`listener-topology.md` fallback 章节
- R2-11：`Router.WithSuppressNoAuthVerifierWarn` 选 Health/InternalListener 装配
- R2-08：已存在 stuck-checker timeout test（RESOLVED）

**Round-3 follow-up ✅ PR #262**（已合并 2026-04-25；分支 `247-pr-a14b-round3-reviewer`，独立 reviewer + 用户复审 11 条 finding 全部清零）：
- **#1** P1 — `isAuthFlavoredPolicy` PolicyStack 解析 bug：`strings.Split(p.Name, " + ")` 永远不匹配 `"stack[a, b, c]"` 格式；任何使用 `PolicyStack(PolicyJWT(...), ...)` 作 listener 默认 policy 的调用方会触发 `validateAuthVerifierForDeclaredRoutes` 误 fail-fast
- **#2** P1 — PolicyVerboseToken length oracle：raw-bytes → SHA-256 hashed `subtle.ConstantTimeCompare`（与 `health.verboseDecision` / `cmd/corebundle/metrics.go` 模型一致）
- **#3** P1 — `readyzPayload` 401 envelope gap：缺 `details` 时 fallback 裸 `error` 对象
- **#5/#9/#11** P1 — PolicyMTLS API 重写：删除误导参数 `pool *x509.CertPool`；签名改 `PolicyMTLS()`；phase0 fail-fast：listener 用 PolicyMTLS 但缺 TLS / `ClientAuth` 不严 / `ClientCAs` nil
- **#10** P1 — PolicyJWTFromAssembly mismatch fail-fast：typed `*jwtFromAssemblyMarker` Extension 携带捕获的 asm 引用，phase0 与 `b.assembly` 比较并 fail-fast
- **#6/#7/#8** P2 — DX/文档/test 锁严
- 新增 `TestPhase0_RejectsPolicyMTLSWithoutTLS` 等 6 个 phase0 回归测试

**~~搭车 PR-A32 SELECTOR-CLOSURE~~**：已被 PR-A14a 吸收，无需再开。

**文件面**：`runtime/bootstrap/bootstrap.go` + `runtime/http/router/group.go`（新）+ `runtime/http/health/probequery/`（新子包）+ 全部 Cell 路由注册 API + `docs/architecture/listener-topology.md`

**风险评估**：高；签名破坏性变更，所有 cell 已同步更新；diff +5363/-2072 的 Cx4 大 PR，已通过 round-2 + round-3 双轮 reviewer 收口。

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

### PR-A18 Vault namespace + datakey 默认化 + 锁去除 + RMQ ManagedResource ✅ 已落地

**实际交付**（不向后兼容、修复彻底，基于用户指示）：
- **A15 VAULT-NAMESPACE-MULTITENANT-01** ✅ `applyNamespaceFromEnv(raw)` 读 `VAULT_NAMESPACE` env → `client.SetNamespace(ns)`，复用 HashiCorp 官方 env 名（未引入 `GOCELL_VAULT_NAMESPACE` 别名）。
- **A16 VAULT-DATAKEY-DEFAULT-01** ✅ envelope encrypt 主路径替换为 `transit/datakey/plaintext`：删 `wrapDEKWithVault`、删客户端 `crypto/rand` DEK 生成；DEK 由 Vault 服务端（HSM-backed in HCP）生成，单 RTT。Decrypt 路径不变（`transit/decrypt` 同时支持新老 EDK），**老密文继续可解**。
- **A18 VAULT-ROTATE-LOCK-REMOVE-01** ✅ `TransitKeyProvider` 删 `sync.RWMutex` 字段；`atomic.Int64 cachedLatestVersion` lock-free version cache；`Current()` 命中缓存零 Vault 调用；`Rotate()` 无锁（Vault 服务端原子，本地仅缓存 invalidate+refresh）。`NewTransitKeyProvider` 构造期顺手 warm cache。多 pod 弱一致性对正确性无影响（keyID 从 Vault 响应解，不从缓存读；`/transit/encrypt`/`/datakey` 永远用 latest_version 服务端版本）。
- **RMQ-STATUS-01** ✅ `Connection` 实现 `lifecycle.ManagedResource`（`Checkers() {"rabbitmq_ready": Health}` + `Worker() nil`）。**顺手清理**：`runtime/bootstrap` 删除 `BrokerHealthChecker` 接口 + `WithBrokerHealth` Option + `brokerHealthNil` 字段 + `isNilBrokerHealthChecker` helper（grep 整库 0 生产调用方，是 ManagedResource 之前的过渡 API）；统一通过 `WithManagedResource(conn)` 接入。

**搭车理由**：前三项都在 `adapters/vault/transit_provider.go`；RMQ-STATUS-01 同 adapter 层健康一致性，三处共用 `lifecycle.ManagedResource` 契约。

**文件面**：`adapters/vault/transit_provider.go` + `adapters/rabbitmq/connection.go` + `adapters/rabbitmq/doc.go` + `runtime/bootstrap/{bootstrap,bootstrap_phases,managed_resource}.go` + 对应 `*_test.go`

**风险评估**：
- 老密文兼容性：Decrypt 路径未触；`TestTransitEnvelope_VaultNeverSeesBusinessPlaintext` integration test 升级为校验"datakey body 仅含 `bits:256`，无 `plaintext` 字段"，envelope 边界**比原方案更窄**。
- 多 pod 缓存一致性：经分析无正确性影响（参见 transit_provider.go cachedLatestVersion 注释）。
- 删 `WithBrokerHealth`：grep 整库 0 调用方，CLAUDE.md 明文"无外部消费者"。
- AppRole policy 同步更新：integration test 中 transit 策略由 `transit/encrypt/...` 改为 `transit/datakey/plaintext/...`。

**ref**：`hashicorp/vault@main api/client.go::SetNamespace` + `EnvVaultNamespace` / `api-docs/secret/transit POST /datakey/plaintext/:name + /:name/rotate` / `rabbitmq/amqp091-go@main connection.go::NotifyClose` / `kernel/lifecycle/managed_resource.go`

---

### ~~PR-A32 SELECTOR-CLOSURE~~（已吸收进 PR-A14a）

**状态**：PR-A14a 彻底重构为物理双 mux 后，`WithInternalEndpointGuard` / `WithInternalPathPrefixGuard` / `authDelegatedMatcher` 已全部删除；F3-CLOSURE SELECTOR-GUARD-REMOVE-01 已完成。Wave 3 无需再开独立 PR。

---

### PR-A35 READYZ-POLISH ✅ 2026-04-25（B' 方案 + 统一 envelope，彻底替换 Plan 原三件套）

**实际交付**：
1. `health.wrapCtxSafe` 给 `RegisterChecker` 自动加结构性 race-pattern 包装（outer Checker 在 ctx.Done 结构性返回）+ `golang.org/x/sync/singleflight` 对 `/readyz` 做天然并发 dedup
2. Verbose token 在所有模式强制必配（或 `GOCELL_READYZ_VERBOSE_DISABLED=1` 显式放弃端点；real 模式仍拒绝放弃）；token 不匹配一律 401 `ERR_READYZ_VERBOSE_DENIED`
3. **所有 infra 端点（/healthz /readyz）响应统一到项目标准 envelope**：成功走 `{"data": {...}}`；失败走 `{"error": {"code","message","details"}}`。新增 errcode `ERR_READYZ_UNHEALTHY`（503）/ `ERR_READYZ_SHUTTING_DOWN`（503）。Verbose 字段（cells/dependencies/adapters）在成功时挂 `data.*`、在失败时挂 `error.details.*`，消费者一条路径贯通
4. 新增 `docs/ops/readyz.md` 运维文档；扩展 `docs/ops/env-vars.md` + `pkg/httputil/response.go` errcode→HTTP 映射；examples README 全部更新到 `X-Readyz-Token` 用法

**偏离 Plan 文原三件套（maxConcurrentProbes semaphore / leak counter / 中心化合约测试 + 100ms 启动期门禁）的原因**：
- 原方案均为运行时 band-aid + 硬编码 magic number；B' 在源头结构化消灭问题：坏 probe 的 goroutine runaway 不再影响 aggregator 语义，无需观测泄漏也无需合约门禁；singleflight 替代 semaphore 无需挑选并发上限；verbose 401 严格化暴露配置错误而非静默降级
- 与用户对齐"不考虑向后兼容"后，顺势（a）删除 `token == ""` = unrestricted 分支 + dev 模式 `slog.Warn` 降级路径；（b）删除 infra 端点"成功走裸 + 错误走 envelope"的不一致——所有路径统一 envelope，Kubernetes readinessProbe 只看 HTTP status code 不看 body，迁移无成本

**原 Plan 文描述（保留参考）**：

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

### PR-A37 DEVTOOLS-METADATA-EXPORT（Cx2，~1d，🟡 解锁 gocell-web 自包含构建）

**来源**：`gocell-web/docs/backend-integration.md` — 前端 DevTools 在 `src/cells/devtools/shared/parser.ts` 用 Vite `import.meta.glob('../../../../../gocell/**.yaml')` 直接读 sibling repo YAML，gocell-web 仓库无法独立 clone/构建。后端需要提供单一 JSON 元数据出口。

**主线**：
- **DEVTOOLS-METADATA-EXPORT-01** 新 `gocell export metadata` 子命令：
  - `gocell export metadata [--format=json|yaml] [--out=<path>] [--include-deps] [--root=<dir>]`
  - 复用 `kernel/metadata.NewParser` 解析全部 cell.yaml / slice.yaml / contract.yaml / assembly.yaml / journey.yaml + status-board.yaml
  - 输出顶层结构：`{schemaVersion, generatedAt, cells, slices, contracts, assemblies, journeys, journeyStatuses, cellDependencyGraph}`
  - `cellDependencyGraph` 复用 `kernel/governance.DependencyChecker.buildDependencyGraph()`（已有，`depcheck.go:121`，cell→cell 边由 contractUsages provider/consumer 推导）
  - `--include-deps` 可选：拼入 PR-A38 `tools/depgraph` 的包级 import graph（A38 落地前该 flag 直接 fail-fast `--include-deps requires PR-A38 depgraph`）
  - JSON tags 已被 `kernel/metadata/meta_struct_guard_test.go` camelCase 守住，零额外修整即可序列化
- 部署模式：**静态导出优先**——gocell-web Dockerfile build 阶段执行 `gocell export metadata --include-deps --out=public/metadata.json`，前端 `parser.ts` 改为 `fetch('/metadata.json')`，彻底脱离 sibling repo 依赖；零 CORS、零 live endpoint 部署耦合

**搭车**：无（独立 export 主题；按用户决策不与 PR-A10 OUTPUT-JSON-SARIF 合并，二者共用 JSON 序列化原则但不共用代码以避免 review 互相污染）

**ADR 决策点（开工前）**：
- 静态导出 vs live endpoint：建议静态（理由见上）；如未来需要 live，再开 follow-up `devtoolsexport` slice 挂 internal listener，复用本 PR 的 export 函数
- `schemaVersion: "v1"` 字符串字段固定 wire 形态；任何破坏性变更先升 v2，老 frontend 按 v1 兼容
- 导出范围：默认 platform 元数据；`--include-examples` 可选扩到 examples/* 元数据

**文件面**：
- `cmd/gocell/app/export.go`（新）+ dispatch.go 注册子命令
- `kernel/metadata/export.go`（新薄壳，组装 ExportDocument 结构）
- `kernel/metadata/export_test.go`（新，golden file 校验 wire 形态）
- 复用 `kernel/governance/depcheck.go::buildDependencyGraph`（暴露为 public helper 或新增 `Graph()` 方法）
- 文档：`docs/guides/devtools-metadata-export.md`（新）+ gocell-web 仓库的 `docs/backend-integration.md` 同步

**对标参考**：`kubectl get -o json` / `helm show all` / goda `pkgs -json` 都属"读 + 转 JSON"模式，无新设计

**风险**：低；纯只读导出，不影响运行时；wire 形态由 schemaVersion + golden test 守

**依赖**：无（PR-A38 是可选增强，不是硬依赖）

---

### PR-A38 TOOLS/DEPGRAPH（Cx3，~1.5-2d，🟡 v1.0 后做，goda-like 包级图）

**来源**：本 plan 2026-04-25 PR-A37 讨论扩充 — 前端 DevTools 希望在 cell 级元数据基础上再加一层"Go 包级 import 依赖"视图，覆盖 `kernel/` / `cells/` / `runtime/` / `adapters/` / `pkg/` / `cmd/` / `examples/` 的实际导入边，做架构合规可视化（类似 goda）。当前 `kernel/governance.depcheck.go` 只到 cell 维度，`tools/archtest/` 是规则一次性扫描，无图模型。

**主线**：
- **TOOLS-DEPGRAPH-01** 新模块 `tools/depgraph/`（**严禁放 `kernel/`** — 会引入 `golang.org/x/tools/go/packages`，违反 CLAUDE.md "kernel 只依赖标准库 + pkg/ + yaml.v3" 硬规则；放 `tools/` 与 `tools/archtest/` 同级生态）
  - API：`Load(rootDir string, opts Options) (*Graph, error)` → `Graph{Nodes []PkgNode, Edges []PkgEdge, RootModule string}`
  - `PkgNode = {ImportPath, Layer (kernel|cells|runtime|adapters|pkg|cmd|examples|tools|external), CellID (cells/<id>/... 推导), Files int, LinesApprox int}`
  - `PkgEdge = {From, To, Direct bool}`
  - Layer 字段从 import path 段位推断（与 CLAUDE.md 分层规则强一致），CellID 同理
  - 默认只扫 `github.com/ghbvf/gocell/...`；stdlib + 第三方默认折叠成单节点 `external/<module>`，`--include-stdlib` / `--include-thirdparty` 显式打开
  - 输出 JSON（被 PR-A37 `--include-deps` 消费）+ DOT（开发者本地 `graphviz` 可视化）
  - 依赖：`golang.org/x/tools/go/packages`（已是 archtest 邻居生态，无新供应链风险）
- **CLI**：`tools/depgraph/cmd/depgraph/main.go`（独立小 CLI，便于本地调试），但主入口走 `gocell export metadata --include-deps`

**搭车（可选，不强制）**：
- **LAYER-GO-IMPORT-01 governance rule**：用 depgraph 数据替换/增强 `tools/archtest/` 现有的文件级 string scan（OUTBOX-SERVICE-03 等规则现是 `goimport` 字符串匹配，迁到 depgraph 后可做"传递闭包"级校验，比如 `cells/foo` 不允许间接传递依赖到 `adapters/`）。本搭车不在主线，留作 follow-up，理由见下

**搭车不强制理由**：archtest 的 string scan 是稳定的文件级单点防御，迁到 depgraph 是"基础设施升级"，不属本 PR 主题；混进来会让 review 失去焦点。建议本 PR 只交付图模型 + JSON 出口，archtest 重写另开 PR-A39

**ADR 决策点（开工前）**：
- 模块归属：`tools/depgraph/` ✅（与 archtest 同级，标准库以外的 `x/tools/go/packages` 不污染 kernel）
- 导出粒度：包级 ✅（不下到文件级 — 体量爆炸，前端可视化无收益）
- 缓存策略：默认 in-memory（首次 ~3-10s，后续 export 无加速诉求），不引入 disk cache 复杂度

**文件面**：
- `tools/depgraph/depgraph.go`（新）+ `depgraph_test.go`
- `tools/depgraph/graph.go`（PkgNode/PkgEdge/Graph 类型）
- `tools/depgraph/layer.go`（layer 推断）
- `tools/depgraph/cmd/depgraph/main.go`（独立 CLI，可选）
- `cmd/gocell/app/export.go`（PR-A37 落地后 import depgraph 实现 `--include-deps`）
- `docs/guides/depgraph.md`（新）

**对标参考**：
- `loov/goda` — 表达式语言 `reach()` / `cut` / `nodes` 不复刻，本 PR 只提供"加载 + 输出图"基座
- `golang.org/x/tools/go/packages` — 标准包加载 API
- `kubectl explain` / k8s `pkg/api/types.go` 自描述模式

**风险**：中；
- `x/tools/go/packages` 全量 Load 体量较重，但属编译期工具非运行时，可接受
- meta_struct_guard 已守元数据 JSON tag，但 depgraph 新增 wire 字段需补 golden test 用例
- depgraph 输出量级估算：~200 包 × ~200B/节点 + ~800 边 × ~80B/边 ≈ 100KB JSON，前端无压力

**依赖**：PR-A37 落地（PR-A38 是 PR-A37 `--include-deps` flag 的提供方；A37 自身无需 A38 也可单独工作）

---

## Wave 4 — P3 长期架构演进（~2-3 周）

### PR-A19 AL-01 Outbox Relay → runtime ✅ 已完工（早期落地，PR #177 + PR #188）

> **代码核实（2026-04-25）**：
> - `runtime/outbox/relay.go` 702 行已实现：lifecycle state machine（stopped/starting/running/stopping）+ poll/cleanup/reclaim 三 loop + 实现 `kernel/outbox.Relay` + `runtime/worker.Worker` 接口
> - `adapters/postgres/outbox_relay.go` **不存在**（已删除）；adapter 仅保留 `outbox_store.go` 的 Store API（`ClaimPending` / `MarkPublished` / `MarkRetry` / `MarkDead` / `ReclaimStale` / `CleanupPublished` / `CleanupDead` / `OldestEligibleAt`）
> - **关键 commit**：PR #177 `refactor(outbox): S30 — hoist Store interface to runtime/outbox + S28 envelope share` 已完成搬迁；后续 PR #188 `feat(outbox): P1-15 relay failure budget + readiness probe` 在 runtime/outbox 上继续扩展
>
> 本计划登记本 PR 时未对齐源码现状（早于 plan 创建日 2026-04-23 即落地），实际无需再开 PR-A19。Wave 4 不再保留此条。

---

### PR-A20 AL-02 DistLock → runtime 抽象 ✅ 已落地 PR #260（2026-04-25，~1d，Wave 4 长期债提前清零）

**主线**：
- **AL-02 DISTLOCK-RUNTIME-ABSTRACT-01** ✅ context-derived lock + shared manager goroutine：续期 goroutine + TTL 刷新 → 通用 DistLock 接口；Redis 仅留 NX/Eval 原语

**搭车**：无

**文件面**：
- `runtime/distlock/driver.go`（Driver 接口 + 三原语）
- `runtime/distlock/manager.go`（单共享 manager goroutine + min-heap）
- `runtime/distlock/locker.go`（Locker + Lock-as-Context Acquire 实现）
- `runtime/distlock/options.go`（TTL / drift / retry 配置）
- `runtime/distlock/clock.go`（Clock 接口 + realClock 实现）
- `runtime/distlock/token.go`（随机 token 生成）
- `runtime/distlock/errors.go`（ErrLockNotHeld / ErrLockLost 等）
- `runtime/distlock/doc.go`（包级 godoc）
- `runtime/distlock/locktest/fake_driver.go`（FakeDriver 测试后端）
- `runtime/distlock/locktest/fake_clock.go`（FakeClock 可注入时钟）
- `runtime/distlock/locktest/conformance.go`（Driver 契约一致性测试套件）
- `adapters/redis/distlock.go`（重写为 RedisDriver，实现 Driver 三原语）

**风险**：高（破坏性 API 变更；但当前零生产 caller，迁移成本零）

**对标**：
- `ref: kubernetes/client-go tools/leaderelection/resourcelock/interface.go` — runtime / adapter 拆分（storage primitives only on adapter, lifecycle on runtime）
- `ref: golang stdlib context.WithCancelCause` — Lock-as-Context API 形态
- `ref: go-redsync/redsync redsync.go driftFactor=0.01` — 时钟偏差容忍（drift factor）
- `ref: PR#177 (S30) 镜像拆分` — adapter 只暴露 Store/Driver 原语，runtime 管理生命周期，与 AL-01 同一拆分模式

## Deferred follow-ups

以下两项原计划 P1 问题（PR-A20-FU1/FU2）和两项追加项（Locker.Stats、goroutine pprof labels）已在 PR #260 的尾巴清零轮中全部落地（2026-04-25）。来源：docs/reviews/202604251700-pr258-reviewer-consolidated.md（第七轮）。

### ✅ PR-A20-FU1 DISTLOCK-RENEW-RETRY-BUDGET-01 — 已落地（PR #260）

**落地**：`handleRenew` 新增 retry budget（默认 `maxRenewAttempts=3`）。瞬态 I/O 错误（err != nil）在 renewTimeout 窗口内重试；永久 ownership-lost（held=false）立即 ErrLockLost，不重试。新增 `WithMaxRenewAttempts(n int) Option`（n<1 panic）。`FakeDriver` 新增 `SetRenewErrorPersistent` / `ClearRenewError` 控制。新增 TC-13（transient-then-success）/ TC-14（budget exhausted）/ TC-15（held=false 无重试）三个测试。

### ✅ PR-A20-FU2 DISTLOCK-RELEASE-RETURNS-ERROR-01 — 已落地（PR #260）

**落地**：`Locker.Acquire` 返回签名改为 `(context.Context, func() error, error)`。`manager.remove()` 返回 `error`（Driver.Release 结果）。Manager 内部通过 `resultCh chan error`（buffered=1）将 background goroutine 的 I/O 结果传回 `remove()` 调用方。`FakeDriver` 新增 `SetNextReleaseError`。`releaseWg` 已删除（`remove()` 同步返回结果，Drained() 不再需要等待 releaseWg）。新增 `TestLocker_Release_ReturnsError` 测试。

### ✅ Locker.Stats() — 已落地（PR #260）

**落地**：`Locker` 接口新增 `Stats() Stats` 方法。`Stats` 结构体含 `ActiveLocks int`。实现委托 `Manager.Snapshot().Locks`。新增 `TestLocker_Stats_Empty`、`TestLocker_Stats_AfterAcquire`、`TestLocker_Stats_AfterRelease` 三个测试。

### ✅ goroutine pprof labels — 已落地（PR #260）

**落地**：`Manager.run()` 首行调用 `pprof.SetGoroutineLabels(pprof.WithLabels(ctx, pprof.Labels("distlock", "manager")))`，使 pprof goroutine dump 中 manager goroutine 可识别。

### 仍 defer：worker.Worker 生命周期集成

**DISTLOCK-WORKER-LIFECYCLE-01**（Cx2, 🟡 P3 可延后）：Manager 是 lazy-start（无 `Start(ctx) error`）+最后一锁释放时 drain；`worker.Worker` 是 `Start(ctx) error / Stop(ctx) error` ctx 驱动关闭模型。两种模型不匹配：(a) 改 Manager 为 Worker-controlled 会破坏 lazy/drain 特性并与 bootstrap 耦合；(b) 适配器层仅增加代码但无生产 caller 需要它。distlock 当前在 `cmd/corebundle` 中零 caller；第一个接入 cell 可自选 wiring 策略。**延后至第一个 Cell 接入时（P3）**。

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

## PR 依赖关系图（仅列待开 / 在飞 PR）

```
Wave 1：✅ 全部完工（15 PR 见各章节状态）

Wave 2：🎯 v1.0 发布硬约束已清零；仅剩 PR-A30 一条
  PR-A30 AUTH-TEST-COVERAGE（S19+S21+S22+S24，PR-A29 已合，依赖解锁，可随时启动）

Wave 3：
  PR-A14b INTERNAL-LISTENER-FULL ✅ PR#258（main 已合）+ 🔨 PR#262 round-3 follow-up（11 条 reviewer findings）
  PR-A15 kernel/webhook（依赖 L3 Outbox 稳定 + SSRF 安全评审）
  PR-A16 kernel/reconcile + LATER-F-1 L3 示例
  PR-A17 runtime/scheduler + WM-18
  PR-A36 HTTP-METRICS-LABEL-REALIGN（🟠 多 cell assembly 部署前触发）
  PR-A37 DEVTOOLS-METADATA-EXPORT（gocell export metadata，解锁 gocell-web 自包含构建）
  PR-A38 TOOLS/DEPGRAPH（goda-like 包级 import 图，PR-A37 --include-deps 提供方；依赖 PR-A37）

Wave 4 (v1.1+)：
  PR-A20 AL-02 DistLock → runtime ✅ PR#260（提前清零）
  PR-A21 AL-04 Auth JWT 评估（可能 won't-do）
  PR-A22 Cell ISP 拆分（破坏性，所有 cell + examples 同步）
  PR-A23 ER-ARCH-01 Subscriber Setup/Run（修 router 启动 time.After 时序竞态）
  PR-A24 DURABLE-TYPE + G-6 + replay/rollback（v1.1 长期债打包）
  PR-A33 REFRESH-OPAQUE-POLISH（X12 idle + X14 grace + X13 partition；PR-A29 已合可启动）
```

> 已完工 PR 不在依赖图列出，详见各 PR 章节状态标记 `✅ PR#xxx`。

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
| ~~PR-A18 Vault 剩余~~ ✅ | ~~RMQ-STATUS-01~~ ✅（ManagedResource wiring 已交付）；backlog_later_detail §3 RMQ-STATUS-01（结构化诊断字段）仍开放，属独立未来项 | 同 adapter health/status 面 |
| PR-A25 auth-prod-harden | S-nonce + S32 | real 模式 fail-fast 同面 |
| PR-A29 auth-refresh-main | X11 → X15（强依赖顺序） | HMAC-split 必须在 opaque integration 之前，否则需数据迁移 |
| PR-A33 refresh-polish | X12 + X13 + X14 | 全在 refresh_store.go + migrations |
| PR-A5c outbox-emitter | EMITTER + ENVELOPE + SERVICE-MIGRATE + ARCHTEST | 全属 outbox 模式收口；envelope 下沉 + 三 cell 一起迁可避免重复 ADR |

---

## 推荐执行顺序

> **🎯 v1.0 发布硬约束已达成 @ 2026-04-25**（PR-A29 ✅ PR#251 是最后一块拼图）。Wave 1 全部 ✅；Wave 2 仅剩独立可延项；Wave 3 进入推进期。
>
> 下面按**双人并行 + buffer**给出每周 ~5 个净工作日；单人场景把每周拉长到 1.8 倍。

### 现阶段（v1.0 已达成 + Wave 2 主线 100% 完工，进入 Wave 2.5 / Wave 3 推进期）

**当前在飞**：无（最近 8 个 PR — #258/#259/#260/#261/#262/#263/#264/#265 — 已全部合并）

**Wave 2 收尾**：✅ 全部完工（PR-A30 ✅ PR#263 是最后一条主线）

### Wave 2.5 推进期（~5d 净，**优先于 Wave 3**——含两条 🔴 安全 + 一条 🔴 生产 gate）

**batch1**（4 worktree 并行，~1.5d 净）：
- PR-CFG-1 READYZ-MANAGEDRESOURCE-AUTO-AGGREGATE（4h，🔴 治理）
- PR-CFG-2 EVENT-DEAD-SUBSCRIBER-VALIDATE + FLAG-CHANGED-RETIRE（3h，🟡 治理+清理）
- PR-CFG-3 EVENT-CONTRACT-BOUNDARY-RECOMPLY 激进方案（2d，🔴 安全/契约/架构 Cx4）
- PR-CFG-5 KEYPROVIDER-LOCALAES-REAL-MODE-REJECT（1h，🔴 生产 gate）

**batch2**（CFG-1 合后启动，可与 batch1 残余并行，~1d 净）：
- PR-CFG-4 CONFIG-READ-METADATA-ADMIN-GATE（1d，🔴 安全数据泄漏，文件无冲突可早起）
- PR-CFG-6 OUTBOX-EMIT-FAILOPEN-DROP-COUNTER（2h，依赖 PR-CFG-1）

**batch3**（CFG-3 合后，1d 净）：
- PR-CFG-7 CONFIG-PILOT-E2E-CONDITIONAL-SKIP（1d，依赖 PR-CFG-3）

### Wave 3 推进期（~10-12d 净，与问题/功能层并行）

**优先级 A（短链路高价值）**：
- PR-A37 DEVTOOLS-METADATA-EXPORT（1d；解锁 gocell-web 自包含 docker build，无依赖）
- PR-A38 TOOLS/DEPGRAPH（1.5-2d；A37 落地后启动）
- PR-A36 HTTP-METRICS-LABEL-REALIGN（4h；🟠 多 cell assembly 部署前触发）

**优先级 B（v1.1 kernel 子模块）**：
- PR-A15 KERNEL/WEBHOOK + WM-32 mTLS（3d；Cx3，需 SSRF 安全评审 + ADR 前置）
- PR-A16 KERNEL/RECONCILE + LATER-F-1 L3 示例（2d；ADR Reconciler API）
- PR-A17 RUNTIME/SCHEDULER + WM-18 延迟消息（2d；分布式协调依赖 Redis/etcd）

### Wave 4 v1.1+（~7.5d 净，按季度排）

PR-A20 ✅ PR#260（提前清零）→ PR-A21 / PR-A22 / PR-A23 / PR-A24 / PR-A33 长期债

> 历史 Week 1-5 排期已完成（Wave 1 + Wave 2 全部落地），不在此重复列出。详细 PR 完工概览见各 PR 章节及头部第十轮回灌摘要。

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

> **2026-04-24 晚间更新**：原"当前 6 条主线（立即执行）/ 当前阶段禁止 / 合并顺序 / 滚动补位"四块已历史化——PR-A1/A2/A3/A4/A8 + #224/#237/#238/#239/#241/#244/#245/#247/#249 均已 merge。真实推进状态请看下方"长期 6 条 lane"与各 PR 正文的 ✅/🔨 标记。本节保留做决策过程 provenance。

### ~~当前 6 条主线（立即执行）~~（2026-04-24 已历史化）

~~| Seat | 当前任务 | 分支 / 约束 |~~
~~|---|---|---|~~
~~| 1 | PR-A1 治理规则 + CI | `refactor/508-pr-a1-governance` |~~ ✅ PR #226
~~| 2 | PR-A2 pkg helper | `refactor-508-pkg-helper-trio` |~~ ✅ PR #225
~~| 3 | PR #224 outbox/emitter 基线 |~~ ✅ PR #224 + #245
~~| 4 | PR-A3 入口收口 + per-cell adapter |~~ ✅ PR #227
~~| 5 | PR-A4 运行时可观测收口 |~~ ✅ PR #228
~~| 6 | PR-A8 Vault auth 批量 |~~ ✅ PR #230

### 现阶段（2026-04-25 第十一轮后）在飞 / 待启动主线

| Lane | 在飞 PR | 下一个待启动 |
|---|---|---|
| Governance / Contract | — | PR-A24（Wave 4 长期债打包） |
| Access / Auth | — | PR-A33 REFRESH-OPAQUE-POLISH（Wave 4）（PR-A30 ✅ PR#263 已合，**Wave 2 主线全部完工**） |
| Outbox / Event | — | PR-A23 ER-ARCH-01 Subscriber Setup/Run（Wave 4）（PR-A19 已早期完工） |
| Entry / Bootstrap | — | PR-A36 metrics realign（PR-A14b round-3 ✅ PR#262 已合） |
| Config / Vault / New Modules | — | **Wave 2.5 PR-CFG-1..7（config 域回灌，2026-04-25 登记，🔴 batch1 优先）** / PR-A16 kernel/reconcile / PR-A17 runtime/scheduler / PR-A21 Auth JWT 评估 / PR-A22 Cell ISP |
| Health / Base Runtime | — | （PR-A35 ✅ PR#256 已合，Lane 暂无在飞） |
| DevTools / Tooling | — | PR-A37 DEVTOOLS-METADATA-EXPORT / PR-A38 TOOLS/DEPGRAPH |
| Wave 4 Long-term | — | PR-A22 / PR-A23 / PR-A24 / PR-A33（PR-A19 + PR-A20 均已完工） |

### 长期 7 条 lane（后续持续滚动，含实际 PR 编号）

| Lane | 任务链 |
|---|---|
| Governance / Contract | PR-A1 ✅ PR#226 → PR-A13 ✅ PR#235 → PR-A9 ✅ PR#239 → PR-A11 ✅ PR#246 (+ FU1 PR#257) → PR-A10 ✅ PR#261 → PR-A24（Wave 4） |
| Outbox / Event | PR #224 ✅ → PR-A5c ✅ PR#224+#245（F2/F3+F7/F4/F5/F6 六批 Cell Option 去原生 publisher/writer） → PR-A6 ✅ PR#250（typed event payloads + Emit[T]，S4/S41 清零） → PR-A19 ✅ 早期完工 → PR-A23（Wave 4） |
| Entry / Bootstrap | PR-A3 ✅ PR#227 → PR-A14a ✅ PR#237（吸收 PR-A32） → PR-A25 ✅ PR#244（S-nonce + S32） → PR-A14b ✅ PR#258（three-listener + RouteGroup）+ PR#262 ✅ round-3 follow-up（11 条 reviewer findings 清零） → PR-A36 metrics realign（待开） |
| Health / Base Runtime | PR-A4 ✅ PR#228 → PR-A35 ✅ PR#256（B' 方案：wrapCtxSafe + singleflight + verbose token strict + 统一 envelope；并入 Entry / Bootstrap lane） |
| Access / Auth | PR-A5a ✅ PR#234 → PR-A7 ✅ PR#241 → PR-A26 ✅ PR#247（setup slice + adminprovision；PR#264 ✅ first-admin harden + provenance state） → PR-A29 ✅ PR#251（X10/X11/X15 一批 🎯 v1.0 最后一块拼图） → PR-A31 ✅ PR#259（DX 打磨） → PR-A30 ✅ PR#263（S19/S21/S22/S24，**Wave 2 最后一条主线**） → PR-A33（Wave 4） |
| Config / Vault / New Modules | PR-A5b ✅ PR#238 → PR-A27 ✅ PR#216；并行填充 PR-A8 ✅ PR#230 → PR-A18 ✅ PR#240（主线）+ PR#242（test split） → PR-A12 ✅ PR#249 → PR-A12b ✅ PR#252（command lifecycle closure） → PR-A16 / PR-A17（待开） → PR-A21 / PR-A22（Wave 4） |
| DevTools / Tooling | PR-A37 DEVTOOLS-METADATA-EXPORT（解锁 gocell-web 自包含构建） → PR-A38 TOOLS/DEPGRAPH（goda-like 包级图，PR-A37 `--include-deps` 提供方） |
| Wave 4 Long-term | PR-A20 ✅ PR#260（DistLock 提前清零） → PR-A21 / PR-A22 / PR-A23 / PR-A24 / PR-A33 按季度排（~~PR-A19~~ ✅ 早期 PR#177+#188 完工，已剔除） |

### 高风险 PR 清单（便于快速筛选，仅列待开）

- **PR-CFG-3** EVENT-CONTRACT-BOUNDARY-RECOMPLY（Cx4 激进方案，🔴 安全/契约/架构三件同 PR；2d）
- **PR-A15** KERNEL/WEBHOOK（Cx3，需 SSRF 安全评审 + HMAC 签名 ADR）
- **PR-A22** Cell ISP 拆分（破坏性，所有 cell + examples 同步）
- **PR-A23** ER-ARCH-01 Subscriber Setup/Run（协议级改造，跨 AZ 时序竞态修复）
- **PR-A24** DURABLE-TYPE + G-6 + replay/rollback（v1.1 长期债打包）
- **PR-A33** REFRESH-OPAQUE-POLISH（X12/X13/X14；X13 partition 涉及数据迁移）

> 已合并的 4 个 round-3/test PR（#262/#263/#264/#265）从本清单移除；PR-A14b round-3 风险已通过 reviewer + 用户复审 + 6 个 phase0 回归测试收口。
