# 032 · Backlog 实施 Roadmap（029 范围外）

> 日期: 2026-05-07 11:00
> 基线: `origin/develop @ a08a77fa`
> 来源: `docs/backlog.md` 14 cap + cap-x-cross + §030 残余
> 形态: 7 wave × 16 PR，与 029 在飞 wave **不重叠**，可独立调度
> 与 029 关系: 并列（029 = errcode + typed envelope + Track A-J 主线；032 = 029 范围外的 OPEN items）

---

## Context

029 master roadmap 已经把 **绝大多数发布前 P0/P1 项** 编排到 Track A-J + 关键路径 + §030 §A/§B/§B''，且大量 ✅ ship。但仍有两类 backlog item 不在 029 范围内：

1. **PR#408（最新 ship 的 funnel-first archtest）4 轮 review 残余**（5 条）— 029 无法预知，必须独立编排
2. **029 §030 Won't-do 段押后但 backlog 仍标 OPEN 的项**（Track C P2/P3 + Track G P2/P3 + Track J + Track F + cap-12 bootstrap 杂项 + M1-M4 架构演进里程碑 + cap-04/13/x-cross 散项）— 029 当时为聚焦稳定性主线，把这些"质量 / 演进 / 文档"维度押后，但 v1.0 GA 前必须收口或显式留 V1.1

本 plan 接管这两类，与 029 **零重叠**：029 已经命名/规划/在飞的 PR（B2 / B5.FU / B11 / C1-C4/C6 / D2-D5 / F1-F5 / G-04 / N5 / R-03 / 06.FU2 / K#07 / K#09 / W4 残）一律不出现在本 plan。030 Won't-do 段中**永久决议**的 K-04/K-05/K-07 也不出现。

预期产出：16 PR，~319h dev + 156h review。

---

## 编排原则

| 维度 | 准则 |
|---|---|
| 与 029 边界 | 029 已编排或已 ship 的 PR 一律不进入；同根但 029 已命名 owner 的（如 J-03/J-04 与 F4）推回 029 owner 顺路收，本 plan 不包 |
| Wave 顺序 | 根因（W1 archtest scanner 框架）→ 工具补全（W2）→ 发布闸门（W3）→ kernel/governance 收口（W4）→ 架构演进 M1-M4（W5）→ 文档/CI/需求（W6）→ 散项收口（W7）|
| PR 大小 | 单 PR ≤ 30h dev，文件域 0 重叠，内部 batch 划分 |
| 显式 co-PR | backlog 写「与 X 同 PR」直接绑（B2-K-08-CARVEOUT-NARROW + ARCHTEST-CARVEOUT-NARROW-FUNCLEVEL；J-03 + J-04 → 推回 029 F4；G-07 + G-08 同 outbox 文件域；F-09 + F-07 + F-06 链）|

---

## Wave 总览

| Wave | 主题 | PR 数 | 工时 (dev+review) | 出场前置 |
|:-:|---|:-:|---|---|
| **W1** | PR#408 review 残余（archtest scanner 根因修复）| 1 | ~45h+20h | — |
| **W2** | cap-14 archtest 规则补全 | 1 | ~22h+11h | W1 ship |
| **W3** | 发布前必出闸门（V-A11 + journey + K-02）| 1 | ~28h+14h | — |
| **W4** | 030 §030 G-07..G-15 押后项 + G-10 大重构 | 4 | ~46h+22h | W2 ship |
| **W5** | M1-M4 架构演进里程碑（串行链）| 4 | ~92h+46h | W4 ship + 029 D3a/D3c ship |
| **W6** | 030 §030 F-01..F-09 + ADR INDEX + 文档 | 5 | ~48h+24h | — |
| **W7** | cap-04 / cap-12 / cap-13 / cap-x-cross 散项 | 3 | ~38h+19h | — |

合计 16 PR，**~319h dev + 156h review = ~475h**。Triggered 段约 25 条不入 wave。

---

## W1 · PR#408 review 残余（1 PR，根因修复）

### PR-X1-A · ARCHTEST-SCANNER-FRAMEWORK-AND-PR408-CLOSURE

| 项 | 值 |
|---|---|
| 来源 | ✅ PR408-FU-PARSE-ERROR-DOUBLE-NIL-SWEEP-01 (PR#412) + PR408-FU-LEGACY-ANCHOR-BACKFILL-01 + PR408-FU-GOVERNANCE-OWNER-AST-EXTRACTION-01 + ✅ PR408-FU-INVENTORY-GIT-LSFILES-01 (PR#412) + PR408-FU-SCANNER-SHARED-FRAMEWORK-01 |
| 问题 | PR#408 4 轮 review 反复出同类反模式（file-level skip / silent parse-error fallback / hardcoded scope / naming heuristic / `// INVARIANT:` 锚点不规范 / inventory 用 find 扫工作树）；per-file scanner 每作者重发明，下一轮新增反模式概率 100% |
| 同 PR 项 | 剩余 3 条 — (b) 39 single-rule 文件加 `// INVARIANT: <ID>` 锚点 + 删 inventory fallback + `INVENTORY-ANCHOR-REQUIRED-01` archtest；(c) inventory AST owner 提取（按 `Rule{ID:...}` struct literal / `const ruleID = "..."` 定位 canonical owner + referenced_by 列；git ls-files 部分已 PR#412 完成）；(d) 新建 `tools/archtest/internal/scanner/` 共享框架（fail-closed by construction、structured scope predicate、内置 vendor/testdata/worktrees skip、统一 receiver-type 解析）+ `SCANNER-FRAMEWORK-USAGE-01` 守 archtest 不许直接 import `filepath.WalkDir/Walk`；(e) 70+ scanner 渐进迁移先迁 ~15 个示范。已闭合：(a) 双 nil sweep 6 处穷举 + (c) inventory `git ls-files` 切换（PR#412） |
| Files | `tools/archtest/internal/scanner/*`(新) + `tools/archtest/inventory_anchor_required_test.go`(新) + 39 个 single-rule `*_test.go` 加锚点 + `scripts/audit/list-archtests.sh`（AST owner 提取部分） |
| ship | L4 |
| 工时 | 32h+16h（scanner 框架）+ 4h+1h（PR408 剩余 3 子项；2 子项已完成于 PR#412）+ 7h+2h（15 scanner 迁移示范） |
| 依赖 | — |

---

## W2 · cap-14 archtest 规则补全（1 PR，依赖 W1 框架）

### PR-X2-B · ARCHTEST-RULES-COVERAGE-AND-NAMING-DRIFT

| 项 | 值 |
|---|---|
| 来源 | ARCHTEST-CONTRACTSPEC-LITERAL-RUNTIME + ARCHTEST-CELL-METADATA-FIELD-DRIFT + CATALOG-DTO-DRIFT-ARCHTEST + ADR-DATE-CONSISTENCY-CHECK + B2-K-08-CARVEOUT-NARROW + ARCHTEST-CARVEOUT-NARROW-FUNCLEVEL（co-PR）+ PR245-F6 OUTBOX-ARCHTEST-SCAN-SCOPE-EXPAND + PR245-F10 CELL-RAW-DEPS-ARCHTEST-EXPAND + K05-ARCHTEST-PACKAGES-LOAD-UPGRADE + IDUTIL-UUID-RAND-FAILURE-TEST + PR250-F3 + TEST-CHDIR-PARALLEL-CLI-01 |
| 问题 | 11 条 archtest scope/owner/drift 规则缺失或退化；K#04/K#05 守卫不够细；errcode carve-out 无 ADR；名义同根的命名漂移（PR245-F* / B2-K-08-* 系列）|
| 同 PR 项 | 11 条；W1 scanner 框架可直接复用 — 新 archtest gates: `CONTRACTSPEC-LITERAL-RUNTIME-01` / `CELL-METADATA-FIELD-DRIFT-01` / `CATALOG-DTO-DRIFT-01` / `ADR-DATE-CONSISTENCY-01` / `ERRCODE-CARVEOUT-FUNCLEVEL-01`（合 B2-K-08 + 同根 ARCHTEST-CARVEOUT-NARROW-FUNCLEVEL，配 1 ADR 登记 carve-out 列表）+ outbox archtest 扩 `cells/<n>/*.go` scope（PR245-F6）+ raw-dep archtest ban 全部 Option（PR245-F10）+ K05 升 packages.Load 类型分析 + UUID rand failure fixture + event wire byte pinning + RootResolver helper 解 t.Parallel |
| Files | `tools/archtest/*`（11 改）+ ADR `docs/architecture/202605070-errcode-carveout-funclevel.md`(新) |
| ship | L4 |
| 工时 | 22h+11h |
| 依赖 | W1 |

---

## W3 · 发布前必出闸门（1 PR）

> 030 §030 Won't-do 把 K-02 押后，但 backlog 仍标 🔴 P0；V-A11 / V-A13 / journey 闸门也没归 029。本 PR 收口这一类。

### PR-X3-C · JOURNEY-AND-GOVERNANCE-PRE-RELEASE-GATES

| 项 | 值 |
|---|---|
| 来源 | K-02 §030 JOURNEY-LIFECYCLE-CI-CLOSE + V-A11 GOVERNANCE-EXAMPLES-COVERAGE-01 + V-A13 GENTPL-LIFECYCLE-PATTERN-01 + JOURNEY-CONTRACT-EXISTENCE-VALIDATE-01 + JOURNEY-STATUS-BOARD-LIFECYCLE-CONSISTENCY-01 + J-02 §030 JOURNEYS-FIXTURES-DECISION |
| 问题 | journey active 集为空 / runner 引用未声明 contract / governance 不扫 examples / status-board 双状态机各表 / fixtures 仅 .gitkeep 与 schema 漂移 / J-confighotreload 引用 `event.config.entry-deleted.v1` 不存在 |
| 同 PR 项 | 6 条全收 — (a) K-02 升 J-ssologin 为 active + `runner.RunActiveJourneys` active 集空 fail + `gocell validate` 增 `journey.contracts ↔ contracts/` 双向存在校验（对偶 ADV-06）+ J-confighotreload contract 漂移修；(b) V-A11 `kernel/governance/rules_examples.go` 扫 examples/；(c) V-A13 gentpl/main.go.tpl 决定 "最小骨架" + 集成测试；(d) JOURNEY-CONTRACT-EXISTENCE-VALIDATE-01 与 K-02 (a) 合并；(e) JOURNEY-STATUS-BOARD-LIFECYCLE-CONSISTENCY-01 board state ↔ yaml lifecycle 强映射；(f) J-02 二选一：删 fixtures/ + 撤 CLAUDE.md 引用，或引入 `fixtures: [fixture-id]` schema 字段（取决于 v1.0 是否需 fixtures） |
| 边界（不在本 PR）| **TEST-JOURNEY-ROOT-HARNESS-01 → 029 C3 owner 顺路收**（C3 PR-V1-TEST-JOURNEY-ROOT 范围已含 root harness）；**J-03 + J-04 → 029 F4 owner 顺路收**（F4 contract v1→v2 演练同根）；**GOVERNANCE-AUTH-PUBLIC-INTERNAL-FORBIDDEN → 029 F4 已吸收 F5**（governance auth-plane 互斥已落 F4 范围）|
| Files | `journeys/J-*.yaml` + `kernel/{governance,verify,assembly/gentpl}/*` + `cmd/gocell/app/printers/verify.go` + 可能 `kernel/metadata/types.go`（fixtures 字段决议）|
| ship | L4 |
| 工时 | 28h+14h |
| 依赖 | — |

---

## W4 · 030 押后项 G-07..G-15 收口（4 PR）

> 030 §030 Won't-do 把 Track G P2/P3 押后，但 G-07/G-08/G-10/G-11/G-12/G-13/G-14/G-15 在 backlog cap-01/02/07/08/10/14 都还是 P1/Cx2 待办。按文件域分 4 PR。

### PR-X4-D · OUTBOX-G-07-AND-G-08

| 项 | 值 |
|---|---|
| 来源 | G-07 §030 OUTBOX-WRITER-MUST-CONTRACT + G-08 §030 OUTBOX-FAILOPEN-COUNTER + INMEM-RECEIPT-FIX |
| 问题 | (G-07) `Writer.Write` 注释 SHOULD 而非 MUST 参与事务 / outbox+command 中 `MaxMetadataKeys` 校验完全重复 / `HandleResult.Receipt` exported 但禁 handler 读写 / 缺 `Ack()/Requeue(err)/Reject(err)` 工厂；(G-08) fail-open RecordDrop 无 metrics / `inMemReceipt.Commit/Release` 共享 sync.Once 静默 false-success / `UnmarshalEnvelope` msg.ID 仅非空可日志注入 |
| 同 PR 项 | 2 条；改 MUST + TxRunner.RunInTx godoc 强制 + 提取 `kernel/metautil`(新) + Receipt 改 unexported + 工厂函数 + `outbox_failopen_drops_total{cell}` + committed atomic.Bool 区分 + 复用 `idutil.IsSafeID` |
| Files | `kernel/{outbox,command,metautil(新)}/*` + `runtime/outbox/*` + `pkg/idutil/*` |
| ship | L3 |
| 工时 | 10h+5h |
| 依赖 | — |

### PR-X4-E · SCAFFOLD-AND-CRYPTO-HARDENING

| 项 | 值 |
|---|---|
| 来源 | G-11 §030 SCAFFOLD-FREETEXT-YAML-INJECTION + G-12 §030 CRYPTO-INTERFACE-HARDENING |
| 问题 | (G-11) `Goal/OwnerTeam` 自由文本写 YAML 无字符过滤，`\n` 注入产生额外键绕过 VERIFY/FMT；(G-12) `MatchKeyID` 普通字符串比较时序侧信道 / `KeyHandle.Encrypt` MUST nonce 唯一无 contract test / `KeyHandle.Encrypt` vs `ValueTransformer.Encrypt` 返回值顺序漂移 |
| 同 PR 项 | 2 条；`validateFreeText()` 拒 `\n\r":#[]{}` + 模板裸 scalar 改单引号 + `TestCreateJourney_YAMLInjection` 对抗测试 + `crypto/subtle.ConstantTimeCompare` + `TestKeyHandle_NonceUniqueness` contract test + `EncryptResult{Ciphertext,Nonce,EDK,KeyID}` 统一签名 |
| Files | `kernel/{scaffold,crypto,governance}/*` |
| ship | L4 |
| 工时 | 8h+4h |
| 依赖 | — |

### PR-X4-F · GOVERNANCE-VERIFY-METADATA-G13-G14-G15

| 项 | 值 |
|---|---|
| 来源 | G-13 §030 GOVERNANCE-RULES-REGISTRATION-GUARD + G-14 §030 VERIFY-PRINTER-ZEROMATCH-WARN + G-15 §030 KERNEL-METADATA-CODEGEN-OVERLAY |
| 问题 | (G-13) `Validator.rules()` 手工 slice / 双列表漂移 / error 规则无修复指导 / rule code 字面量散落；(G-14) verify printer 对 `TestResult.ZeroMatch=true` 无警告，与 PASS 输出相同；(G-15) kernel/metadata 既被动数据又承载 `goStructName` 等 codegen-only 字段 |
| 同 PR 项 | 3 条；archtest 反射枚举 + 统一 `ValidateStrict(strict, failFast bool)` 单入口 + error 规则参照 ADV-06 追加 `; fix:...` + 提取 `rulecodes.go` + verify printer 加 `[WARN] no tests matched` + metadata 二选一 (a) 拆 codegen overlay 或 (b) doc.go 注明承载多消费方字段 |
| Files | `kernel/{governance,metadata}/*` + `tools/archtest/*` + `cmd/gocell/app/printers/verify.go` + 可能 `tools/codegen/*` |
| ship | L4 |
| 工时 | 10h+5h |
| 依赖 | — |

### PR-X4-G · KERNEL-CELL-PACKAGE-DECOMPOSE（G-10 大重构）

| 项 | 值 |
|---|---|
| 来源 | G-10 §030 KERNEL-CELL-PACKAGE-DECOMPOSE |
| 问题 | kernel/cell 是 god-package（AuthPlan / Outbox EmitterFactory / Health alias）；`Cell` 接口 11 方法混合生命周期与元数据自省；3 个 "registry" 命名混乱 |
| 同 PR 项 | 5 子改 — (a) `auth_plan.go → kernel/auth/`；(b) `mode_resolver.go → kernel/outbox/emitter_resolver.go`；(c) `cell.Registry → cell.Registrar`；(d) `Cell` 拆 `CellLifecycle + CellDescriptor`；(e) 删 `health.go` alias |
| 与 029 协同 | backlog Trigger 写「与 029 #13 PR-A22 协同」— 029 #13 是关键路径末项，本 PR-X4-G 与 029 #13 文件域有 ~30% 重叠，建议本 PR ship 后 029 #13 减裁；或 029 #13 owner ship 时把 G-10 顺路收，本 plan 删除本 PR — **决策点：等 029 #13 owner 接纳与否**。本 plan 默认独立 ship |
| Files | `kernel/{cell,auth,outbox,registry}/*` + `cells/{accesscore,auditcore,configcore}/*`（caller 全迁）|
| ship | L4 |
| 工时 | 28h+14h |
| 依赖 | W2（archtest 兜底）|

---

## W5 · M1-M4 架构演进里程碑（4 PR 串行链）

> 来源 ADR-202605041430（`docs/architecture/202605041430-adr-architecture-evolution-milestones.md`）。029 没收，030 §030 Won't-do 没收，但 backlog cap-02/13/14 显式列入。每条独立 PR，依赖锁文件较深，串行执行。

### PR-X5-H · M1-HEALTHZ-INTERFACE-PACKAGE

| 项 | 值 |
|---|---|
| 来源 | M1-OBSERVED HEALTHZ-INTERFACE-PACKAGE-01（cap-13）|
| 问题 | 38 处 Health 实现分散无统一接口 |
| 同 PR 项 | 1 条；新建 `kernel/healthz`（Aggregator/Probe/Snapshot）+ codegen 从 cell.yaml 派生状态 schema + `runtime/observability/healthz/inmemory` + 可选 postgres/otel adapter + `HEALTHZ-WRITE-01` archtest + 38 处分散 Health 收口 |
| Files | `kernel/healthz/*`(新) + `runtime/observability/healthz/*` + `tools/codegen/*` |
| ship | L4 |
| 工时 | 24h+12h |
| 依赖 | 029 D3c CONFIGCORE-READYZ-REPO-PROBE ship + W2（archtest 兜底）|

### PR-X5-I · M2-LIFECYCLE-STATE-MACHINE

| 项 | 值 |
|---|---|
| 来源 | M2-LIFECYCLE + F-08 §030（已 absorb 进 M2）|
| 问题 | (a) cell/slice 缺 lifecycle 字段；(b) `cell.lifecycle` + `outbox.entry.state` 隐含状态机无 enum + transition 表 |
| 同 PR 项 | cell.yaml/slice.yaml 加 `lifecycle: [experimental,candidate,asset,maintenance,retired]` + `kernel/cell/lifecycle.go` + `kernel/outbox/state.go` 显式 state enum + transition map + governance 校验状态转移 + archtest 完备性 + Aggregator 暴露相位（差距由消费方计算）+ 1 ADR |
| Files | `kernel/{metadata/types.go,cell/lifecycle.go,outbox/state.go,governance,healthz}` + ADR(新) |
| ship | L4 |
| 工时 | 24h+12h |
| 依赖 | W5-H |

### PR-X5-J · M3-RULE-ENGINE-AND-INVARIANTS-REGISTRY

| 项 | 值 |
|---|---|
| 来源 | M3-RULE-ENGINE + GOVERNANCE-INVARIANTS-REGISTRY（"与 M3-RULE-ENGINE 同根"）|
| 问题 | governance 64 规则散在 Go 代码 / 派生物 invariants 在 6-8 处独立声明 |
| 同 PR 项 | 2 条同根；`kernel/governance/engine.go` 唯一执行体 + `rules/*.yaml` 数据化（5 槽位 detect/evidence/next/level/harvest）+ next-action 五级（autofix/suggest/advisory/block/escalate）+ 规则带 metric 距离函数 + 修 ADV-05 SeverityError 错分 + invariants Registry 四件套 |
| Files | `kernel/governance/{engine.go(新),invariants.go(新),rules/*.yaml(新)}` |
| ship | L4 |
| 工时 | 28h+14h |
| 依赖 | W5-I（lifecycle 状态机供 governance rules 引用）|

### PR-X5-K · M4-REVERSE-COVERAGE-ARCHTESTS

| 项 | 值 |
|---|---|
| 来源 | M4-COVERAGE |
| 问题 | 缺 5 条反向追溯规则 |
| 同 PR 项 | 5 子 archtest — `IMPL-DECL-COVER-01`（cell 间 Go import 必须经 contract，非 slice 间）+ `HANDLER-DECL-COVER-01`（http handler 必须出现在某 contract.yaml）+ `EMIT-DECL-COVER-01`（outbox emit 必须出现在 contract.triggers）+ `DEAD-CONTRACT-01`（active contract 必须有 handler 入口）+ `DEAD-CODE-01`（deprecated contract 引用代码不能在 main 分支）|
| Files | `tools/archtest/*` + 各 cell `cell.yaml` 漂移修 |
| ship | L4 |
| 工时 | 16h+8h |
| 依赖 | W5-J（rule engine 单源）|

---

## W6 · 030 §030 F-01..F-09 押后项 + 文档（5 PR）

> 030 §030 Won't-do 把 Track F 全部押后，但 backlog cap-x-cross / cap-02 / cap-03 / cap-14 显式列入。029 F1-F2-F4-F5 已 owner 这部分，本 plan 接管 029 没领的 F-01/F-02/F-03/F-04/F-06/F-07/F-09。

### PR-X6-L · CI-CODEOWNERS-MAKEFILE-PKG-CONTRACTS-DOC（F-01+F-02+F-03）

| 项 | 值 |
|---|---|
| 来源 | F-01 §030 CODEOWNERS-PR-TEMPLATE + F-02 §030 MAKEFILE-LINT-RACE-ARCHTEST + F-03 §030 PKG-CONTRACTS-BOUNDARY-DOC + ARCHTEST |
| 同 PR 项 | 3 条；CODEOWNERS（`/kernel/ @owner-kernel` 等）+ pull_request_template.md（4 项 checklist）+ branch protection 配置 + `make lint`/`race`/`archtest` 独立 target + CI yaml 改调 Makefile + `hack/verify-lint-exclusions.sh` 校验时间戳 + `pkg/contracts/doc.go` 边界声明 + `PKG-CONTRACTS-NO-BUSINESS-TYPE` + `PKG-CTXKEYS-NO-CELL-MODEL` archtest |
| Files | `.github/{CODEOWNERS,pull_request_template.md,workflows}` + `Makefile` + `hack/*` + `pkg/{contracts,ctxkeys}/doc.go(新)` + `tools/archtest/*` |
| ship | L3 |
| 工时 | 12h+6h |
| 依赖 | W2 |

### PR-X6-M · CMD-DOC-AND-ADR-INDEX（F-04 + ADR-INDEX-01 + 散 doc）

| 项 | 值 |
|---|---|
| 来源 | F-04 §030 CMD-GOCELL-VS-COREBUNDLE-DOC + ADR-INDEX-01 + PR267-FU-ROLE-PREFIX-ADR + PR284-FU-COMPOSE-HEALTH + P3-TD-05（compose start_period + 删 deprecated version 键）|
| 同 PR 项 | 5 条；`cmd/CLAUDE.md` 加对照段（cmd/gocell = 治理/元数据/生成器 CLI；cmd/corebundle = assemblies 运行时组装）+ `docs/architecture/INDEX.md` 生成 + role prefix ADR + 3 examples docker-compose 补 healthcheck/start_period + 删 `version: "3.9"` |
| Files | `cmd/CLAUDE.md` + `docs/architecture/INDEX.md`(新) + ADR(新) + `examples/*/docker-compose.yml` |
| ship | L2 |
| 工时 | 6h+3h |
| 依赖 | — |

### PR-X6-N · F-06-REQUIREMENTS-TRACEABILITY

| 项 | 值 |
|---|---|
| 来源 | F-06 §030 REQUIREMENTS-TRACEABILITY-CHAIN |
| 问题 | 无 `docs/requirements/`；ADR/Roadmap/journey goal 三处隐含需求；contract.yaml/journey 无 `requirementID` 反向链；V 模型左侧追溯断点 |
| 同 PR 项 | 1 条；`docs/requirements/REQ-*.yaml` schema (id/text/category/priority/satisfiedBy/verify) + contract.yaml + journey schema 加 `requirementID: []` + archtest `REQ-TRACE-01` 双向校验 + 1-2 ADR |
| Files | `docs/requirements/*`(新) + `kernel/metadata/*` + `tools/archtest/*` + ADR(新) |
| ship | L4 |
| 工时 | 16h+8h |
| 依赖 | W5-J（M3 rule engine 提供 invariants 容器）|

### PR-X6-O · F-07-SYSML-VIEW-CODEGEN

| 项 | 值 |
|---|---|
| 来源 | F-07 §030 SYSML-VIEW-CODEGEN |
| 问题 | 5 张 SysML 图（BDD/IBD/用例/活动/状态机）有元数据天然映射但无生成器 |
| 同 PR 项 | 1 条；新建 `tools/sysmlgen/` → `generated/sysml/<view>.{puml,mermaid}` + CI step `make sysml-verify` |
| Files | `tools/sysmlgen/*`(新) + `generated/sysml/*`(新) + `Makefile` + CI |
| ship | L3 |
| 工时 | 8h+4h |
| 依赖 | W6-N（requirementID 字段就绪）|

### PR-X6-P · F-09-CONSTRAINTS-PARAMETRIC-FIELD

| 项 | 值 |
|---|---|
| 来源 | F-09 §030 CONSTRAINTS-PARAMETRIC-FIELD |
| 问题 | cell.yaml 无 `constraints` 字段；SLO/性能/容量约束写在 PR 描述而非模型 |
| 同 PR 项 | 1 条；cell.yaml schema 加 `constraints: { latency: {p99_ms,p999_ms}, throughput: {publish_per_second}, capacity: {queue_depth_max} }` + verify 钩子跑 micro-benchmark 校验 |
| Files | `kernel/metadata/types.go` + `kernel/verify/*` + cell.yaml schema |
| ship | L3 |
| 工时 | 8h+4h |
| 依赖 | W6-N |

---

## W7 · 散项收口（3 PR，跨 cap）

### PR-X7-Q · CAP-12-BOOTSTRAP-RESIDUAL（029 没全收的 bootstrap 杂项）

| 项 | 值 |
|---|---|
| 来源 | STARTUP-ROLLBACK-ERR-JOIN-01 + B2-R-01 + B2-X-03 + COREBUNDLE-MAINTEST-FAIL-FAST-01 + PR333-BOOTSTRAP-OPTION-CROSS-CONCERN + PR405-BOOTSTRAP-SHUTDOWN-BUDGET-DECOUPLE + V-A8-DEFERRED CMD-CORE-INTERNAL-GUARD-PUBLIC-01 |
| 问题 | 7 条 bootstrap 子项 — startup rollback 错误聚合 / HealthListener silent fallback / PG invalid index warn-continue / corebundle main_test 白名单吞错 / option 跨 concern / phase10 shutdown budget 共享 shutCtx / cmd/corebundle/main.go 28 行 archtest 未提升 |
| 边界（不在本 PR）| 029 D2 SHARED-DEPS-SPLIT 不收（owner = 029）；029 N5 OBSERVABILITY-PKGDOC 不收（owner = 029 §B）；029 R-03 BOOTSTRAP-NIL-OPTION 不收（owner = 029 §A'）|
| 同 PR 项 | 7 条；新 ADR `bootstrap-error-aggregation-and-budget-split.md` 收 PR405 + STARTUP-ROLLBACK-ERR-JOIN |
| Files | `runtime/bootstrap/{run_state.go,bootstrap_phases.go,phases_shutdown.go,bootstrap_http_shutdown.go,options.go}` + `cmd/corebundle/{bundle.go,main_test.go}` + ADR(新) |
| ship | L4 |
| 工时 | 18h+9h |
| 依赖 | — |

### PR-X7-R · CAP-04-AND-CAP-13-OBS-RESIDUAL（029 D3 之外的散项）

| 项 | 值 |
|---|---|
| 来源 | A26-R3 SETUP-PATH-NAMESPACE-POLICY + HTTPUTIL-WRITEERRORBODY-DOUBLE-MARSHAL + PR391-HEALTH-VERBOSE-REDACTION-01 + LISTENER-API-SPEC-01 + PR237-DX1 LISTENER-OPTION-NAMING-UNIFY + PR237-A4 listener architecture doc + PR237-PM7 EXAMPLE-INTERNAL-LISTENER-COMMENT + PR237-OB2（029 D3a 已吸收→跳过）+ PR237-F06 DUAL-LISTENER-DEVMODE-WARN-TEST + P4-TD-10 metrics path label cardinality + B2-C-12 audit HMAC key 32-byte minimum + B2-C-09 auditquery raw payload redact |
| 同 PR 项 | 11 条；3 batch — Batch 1 listener doc/naming/test（5 条 PR237-*）+ Batch 2 cap-04 杂项（A26-R3 / HTTPUTIL-double-marshal / PR391 verbose redaction / LISTENER-API-SPEC）+ Batch 3 cap-13 audit safety + metrics（B2-C-12 / B2-C-09 / P4-TD-10）|
| Files | `runtime/{http/{health,middleware,router},auth,bootstrap}` + `cmd/corebundle/internal_guard_env_test.go`(新) + `examples/*/main.go` + `cells/auditcore/*` + `runtime/observability/metrics.go` + `pkg/httputil/*` + `.claude/rules/gocell/api-versioning.md` |
| ship | L4 |
| 工时 | 12h+6h |
| 依赖 | — |

### PR-X7-S · CAP-X-CROSS-RESIDUAL

| 项 | 值 |
|---|---|
| 来源 | B-FLOOR-FOLLOWUP TYPED-ENVELOPE-ADAPTER-FLOOR-UPGRADE + ADAPTER-CONNECT-BUDGET-01 + S3-FAILURE-INJECTION-01 + SWEEPER-OBSERVABLE-01 + ADAPTER-FAKE-EXPORT-01 + PR-BATCH2-RETRO-FU + PR238-FU4 CONFIGREPO-LEGACY-NOTFOUND-TEST-DEDUP + PR-A41-FU1 + DEVOPS-INTEGRATION-CLEANUP-WAIT-TIMEOUT-01 + P3-TD-04 sandbox httptest panic + P4-TD-01 noop outbox 共享包 + P4-TD-06 CI example validation `\|\| true` |
| 边界 | 029 W4 ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01（A-03）不收（owner = 029 W4）；触发条件型项推到 Triggered |
| 同 PR 项 | 12 条；3 batch — Batch 1 adapter（ADAPTER-CONNECT-BUDGET / ADAPTER-FAKE-EXPORT / S3-FAILURE-INJECTION）+ Batch 2 outbox/sweeper（SWEEPER-OBSERVABLE 与 PR252-F2 同 batch 但 PR252-F2 触发条件未达 → Sweeper 单出）+ Batch 3 杂（B-FLOOR / PR238-FU4 / DEVOPS-INTEGRATION-CLEANUP / P3-TD-04 / P4-TD-01 / P4-TD-06 / PR-A41-FU1 / PR-BATCH2-RETRO-FU）|
| Files | `adapters/{rabbitmq,postgres,s3,*/fake(新)}` + `kernel/command/sweeper.go` + `cells/configcore/internal/adapters/postgres/config_repo_test.go` + `tests/e2e/*` + `runtime/testutil/*` + `.github/workflows/*` |
| ship | L4 |
| 工时 | 18h+9h |
| 依赖 | 029 W4 ship（A-03 错误分类先于 SWEEPER-OBSERVABLE 顺路 transient 标）|

---

## 顺序与并行

```
W1 单 PR (root cause)
  └── X1-A scanner 框架 + PR408 4 子项
        │
        ▼
W2 单 PR (依赖 W1)
  └── X2-B archtest 11 规则补全
        │
        ▼
W3 单 PR (与 W4 文件域 0 重叠，可并行)
  └── X3-C journey + governance 闸门

W4 (4 PR，全程可与 W3 并行；X4-D/E/F 文件域 0 重叠 3 worktree；X4-G 大重构串后)
  ┌── X4-D outbox G-07 + G-08
  ├── X4-E scaffold + crypto G-11 + G-12
  ├── X4-F governance/verify/metadata G-13/G-14/G-15
  └── X4-G G-10 KERNEL-CELL-DECOMPOSE  ←── 等 W2 ship + 029 #13 owner 决策
        │
        ▼
W5 单链 (依赖 W4 + 029 D3a/D3c)
  W5-H M1 healthz interface
  └── W5-I M2 lifecycle state machine
       └── W5-J M3 rule engine + invariants
            └── W5-K M4 reverse coverage archtest

W6 (5 PR，X6-L/M 全程可并行；X6-N → X6-O,P 串行)
  ┌── X6-L F-01/02/03 CI/CODEOWNERS/Makefile/pkg-contracts
  ├── X6-M F-04 + ADR INDEX + compose
  ├── X6-N F-06 requirements traceability  ←── 等 W5-J
  │     ├── X6-O F-07 SysML
  │     └── X6-P F-09 constraints
  └── ...

W7 (3 PR，全程可与上面任意 wave 并行)
  ┌── X7-Q cap-12 bootstrap residual
  ├── X7-R cap-04 + cap-13 obs residual
  └── X7-S cap-x-cross residual  ←── 等 029 W4 A-03 ship
```

实际 worktree 调度：W1 → (W2 + W3 + X4-D + X4-E + X4-F + X6-L + X6-M + X7-Q + X7-R 并发 ≤ 6) → X4-G → (W5 串链 + X6-N) → (X6-O + X6-P + X7-S) → 末态。

---

## Triggered（条件式入场，不在 wave）

下列条目留 backlog Trigger 不变；本 plan 不主动起 PR，等条件触发后单独评估：

| ID | Trigger / 备注 |
|---|---|
| OIDC-FAIL-FAST-DISCOVERY-01 | 首个 prod OIDC 部署 |
| OIDC-JWKS-ROTATION-WORKER-01 | A-01 ship 后 — 注：A-01 完整范围 029 没收，本 plan 也没收（adapter Checkers 完整性不在 backlog OPEN，是 PR#373 review 残留），可在 029 D3a R-METRIC-PACK ship 后评估单起 |
| ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 | 029 W4 owner |
| KMS / S14a | 云平台部署需求 |
| X1 PG-DOMAIN-REPO（Device/Command 子集）| 设备/命令需要持久化（accesscore 部分由 029 B2 收）|
| KERNEL-REPLAY-01 / KERNEL-RECONCILE-01 | Consumer 模型稳定 / L3 业务出现，V1.1 |
| KERNEL-WEBHOOK-01 | Outbox Relay 稳定，V1.1 |
| RUNTIME-SCHEDULER-01 | 业务出现 Cron 需求，V1.1 |
| KERNEL-ROLLBACK-01 | V1.1+ |
| WS-DX-01 | observability 接入时 |
| B2-C-10 auditappend mutex 串行化 | 029 D4 owner |
| WM-18 延迟消息 / WM-32 mTLS | V1.1 + 各自 trigger |
| C-AC7 JWT jti / X3 SecureCookie key rotation | 单 token 撤销 / 加密轮换需求 |
| X5 accesscore domain split / X13 REFRESH-PARTITION | X1 落地后 / 生产流量阈值 |
| T3 / T4 / T5 / T6 / T7 / T10 / T11 | 各自 Trigger |
| PR266-AUDITAPPEND-STRICT | strict-audit 客户出现 |
| PR280-FU1 ChangePassword CAS | 客户反馈或安全审查 |
| PR283-OTEL-SLOG-ERROR-ATTR | 首次 OTEL slog bridge 接入 |
| PR252-F1 / PR252-F2 | 下一个 durable command adapter / multi-replica command consumer |
| PR392-FU-RATE-LIMITER-DISTRIBUTED | bootstrap mode + 多 pod |
| PR-CFG-G1-FU6 / PR-CFG-A-DEFER-2 / PR320-FU-CONFIGCORE-CI-NOOP | 各自余项 |
| OBS-SSA-ANALYZER-01 | 跨包 taint 检测需求 |
| L3-EXAMPLE-PROJECTION-01 | V1.1 |
| C-DC9 auditarchive | 业务上线 |
| GOVERNANCE-AUTH-PUBLIC-INTERNAL-FORBIDDEN | 029 F4 owner（已吸收 F5）|
| TEST-JOURNEY-ROOT-HARNESS-01 | 029 C3 owner |
| J-03 CONTRACT-V1V2-DRY-RUN + J-04 CONTRACT-SCHEMA-NAMING-NORMALIZE | 029 F4 owner（同根）|
| ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 | 029 C2 owner |
| B2-C-01 audit hashchain 重启 | 029 C1 owner |
| B2-C-02 SETUP 路由迁 internal | 029 A2 owner（PR#392 已 ship 不同方案：删 bootstrap admin provision mode + setup-driven，已闭口）|
| CELLS-IDENTITYMANAGE-LEVEL-MISLABEL-01 | 一行 yaml fix，单独 commit 即可，不入 wave |

---

## Won't-do（V1.1+ 或决议）

- **030 K-04 PLATFORM-CELLS-BOUNDARY**：已 ADR 决议 won't-do（accesscore/auditcore/configcore 留 framework 仓）
- **030 K-05 CONTRACT-CONCEPT-COLLAPSE**：已 ADR 决议 won't-do
- **030 K-07 CELLS-SLICE-MULTI-VERB-DECOMPOSE**：永久 won't-do（单 PR 触 50+ 文件 review 不可行）
- **CONTRACT-BREAKING-01 / CONTRACT-CODEGEN-01 / CONTRACT-STUB-01**：V1.1 启动
- **CELLS-SLICE-MULTI-VERB-DECOMPOSE-01**：与 030 K-07 同根，永久 won't-do
- **DURABLE-TYPE-01**：V1.1 启动
- **CONTRACTTEST-SCHEMAREF-FAILFAST-01 / CONTRACT-ENDPOINT-TEST-MAPPING-01 / CONTRACT-PATH-QUERY-EXECUTABLE-01**：029 C4 owner（PR-V1-CONTRACT-INTEGRITY 已规划）

---

## 验证

每 PR 必备 done criteria：

1. **范围闭合**：PR description 列出本 PR 收的 backlog ID 清单；commit message 含每 ID 的 `closes` 或 `addresses`
2. **archtest GREEN**：相关新 archtest gate 命名规范化，allowlist 空
3. **journey 不破**：`gocell verify` 全过；W3 后 active 集 J-* 必跑
4. **ADR 落地**：W2（carve-out 收窄）/ W3（J-02 fixtures 决议）/ W4-G（kernel/cell decompose）/ W5-H/I/J（M1/M2/M3 各 1）/ W6-M（ADR INDEX）/ W6-N（requirements）/ W7-Q（bootstrap budget）各 1 ADR
5. **backlog 回填**：合表后 `docs/backlog.md` 对应行 Flag 改 `✅`，季度初批量迁 `archive/2026-q2-completed.md`；本 plan header 同步 wave 状态
6. **与 029 边界**：每 PR PR description 自审一句「本 PR 不与 029 任何 owner item 重叠」，避免 scope 漂移

---

## 后续衔接

- 029 与本 plan 并存，各自跟踪自己的 wave 进度，PR ship 时只在「本 plan owner」与「029 owner」二选一回填，不交叉
- W4-G G-10 大重构与 029 #13 PR-A22 文件域 ~30% 重叠 — ship 前先与 029 #13 owner 协调，决定吸收方向
- W5 M1-M4 是新一波架构演进，ship 后 ADR-202605041430 的 M1-M4 标记可置为 ✅；ADR 本身保留作历史
