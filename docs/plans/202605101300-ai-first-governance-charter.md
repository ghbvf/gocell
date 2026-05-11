# GoCell archtest / governance 治理章程（AI-first 重写）

**生成日期**：2026-05-10
**重建说明**：本文件是 2026-05-10 我误用 `git restore` 丢失的 AI-first 章程版本的重建。原本以覆盖式写入 `docs/plans/202605070431-pr403-funnel-fix-roadmap.md`，按用户"重新写一份是新文件"的本意，本次写入独立文件，不覆盖 5-PR 主线 roadmap。
**关系**：与 `docs/plans/202605070431-pr403-funnel-fix-roadmap.md`（5-PR 主线 + 实施进度）并存——后者是路线图视角，本文件是**第一性原理章程视角**。冲突时以本文件为准。

---

## 0. 第一性原理：GoCell 是 AI 项目

承认事实：**主要实施者是 AI（claude code），不是人**。

| 维度 | 传统项目假设 | AI 项目实际 |
|---|---|---|
| 实施者持续性 | 人在项目内学习累积 | 每 session fresh instance，无记忆 |
| 反馈学习 | 人看 review 反馈学聪明 | AI 不学，下次同样错 |
| 约定/惯例 | 人理解约定不绕 | AI 按字面翻译，约定即漏 |
| 错误模式 | 人犯随机错 | AI 倾向"机械翻译形式，丢失语义"（PR #430 P1.2 finding 已确认 meta-pattern）|
| 工程机制角色 | 给人减负的工具 | AI 的**唯一持久记忆** |

由此推出 **GoCell 工程治理目标必须从"对人友好"转为"AI-rebust"**：违反不可表达 / 机制不可绕过 / 字面约定全部消除。

---

## 1. AI-rebust 工程治理三档分级

| 档 | 定义 | AI 可绕过性 | 例子 |
|---|---|---|---|
| **Hard** | 违反不可表达（type system / sealed interface / typed function call / reflect 字段数）| 0 | PR-C `AuthComboLegal` single oracle + 5 层 mirror；PR-Φ 节点 typed callback per node kind（待启动）|
| **Medium** | 违反需 runtime guard / 跨多约束 cross-validate 才能识别 | 低 | PR-B BFS + `assertEmitterMethodsRestrictedToLocator` runtime invariant |
| **Soft** | 字符串约定 / 注释豁免 / 名字 convention / fixture 框架 | **高** | `// INVARIANT: ID` 锚点字符串；`// PANIC-REGISTERED-01: ADR-approved: ...` 就地注释；`newResult` 方法名识别；hand-crafted fixture |

**治理原则**：
1. 新引入工程机制必须 ≥ Medium 档；Soft 档严禁立项
2. 既有 Soft 档项目按"实际事故密度 × AI 暴露面"排队升级
3. 改造方向规范化：
   - 字符串锚点 → typed function call（`archtest.Invariant("ID")`）
   - 注释豁免 → typed marker（`panicregister.Approved("reason")`）
   - 名字 convention → sealed interface / receiver type
   - hand-crafted fixture → real source AST capture（AI 难造假）

---

## 2. 当前状态（基线 `c21ed4de`，2026-05-10）

### 已 ship

| PR | 内容 | AI-rebust 等级 |
|---|---|---|
| #408 | 11-theme 聚并（archtest 104→70）| — 一次性聚并 |
| #411 | PR-FUNNEL-02 handler funnel | Medium |
| #412 | parse-error fail-loud + git ls-files | Hard |
| #418 | PR-FUNNEL-03 governance 8 themed files | — 一次性聚并 |
| #419 | scanner 共享框架（文件遍历层）| Hard（fail-closed by construction）|
| #430 | Path C scanner sole funnel + 19 bypass 站点迁移 + USAGE-02 substring 反模式删除 | Hard（USAGE-01 traversal symbol table type-aware 升级）|
| #431 | PR-B BFS reachability + 防漂移 guard | **Medium**（按方法名 convention，AI-soft 边缘）|
| #432 | PR-C single oracle + 5 层 mirror | **Hard**（AI-rebust 范本）|
| #435 | PR-A' 彻底版（删 inventory.md + drift gate + list-archtests.sh stdout-only + INVENTORY-ANCHOR-REQUIRED-01 archtest）| **Soft**（锚点是 `// INVARIANT: ID` 字符串约定，AI 可写假锚点）|
| #TBD | panic 单源 typed marker（Wave 2）: `pkg/panicregister.Approved` funnel + archtest PANIC-REGISTERED-01 重写（删 architecturalPanicWhitelist + AllowMust + ADR reconciliation）+ 50 call-site 迁移 + codegen template + ADR/error-handling.md/ai-collab.md 同步 | **Hard**（typed function call 唯一 funnel；form uniqueness + archtest fail-on-deviation）|

### 未启动

| 名 | 简述 | 第一性原理判 |
|---|---|---|
| 节点遍历漏斗（原 PR-Φ）| framework typed callback per node kind + 一锅替换 65 手写 for-loop + 131 裸 ast.Inspect | Hard 档，**保留启动**（typed callback 是 AI-hard）|
| 测试 polling 单源（PR #438 衍生）| `(require\|assert).Eventually` 339 站点 / 70 文件；当前形态是字符串 budget + 注释 allowlist 倾向（Soft）；PR #438 race CI flake 已暴露 D500ms 踩穿事故 → 应改 `pkg/testutil/testwait.External("reason")` typed marker + `Deterministic` 框架收纳 channel-as-condvar，archtest 拦裸 Eventually | **事故密度驱动**：与 panic 单源同范式（注释豁免 → typed marker，§1 改造方向 #2）；按 §6.2 一锅替换不 ratchet；当前事故密度 1，挂 Wave 4 触发型 |

---

## 3. 已知 follow-up 与 AI-rebust 评级

| 条目 | 当前 backlog 优先级 | AI-rebust 评级 | 真正应做的 |
|---|---|---|---|
| ~~`PR430-FU-USAGE-01-TYPE-AWARE`（receiver method bypass）~~| ~~P2/触发型~~ | ~~当前 Hard 但有边界~~ | **CLOSED — merged into PR-Φ** (refactor/552-archtest-eachnode-funnel)：packages.Load 入口由 PR-Φ 路径 B 强制引入后成本已 sunk，SCANNER-FRAMEWORK-USAGE-01 整规则一次性升 type-aware 顺手清。`forbiddenWalkRefs` 现走 `*types.Info`（package-level + receiver method 同时拦截）。 |
| `PR430-FU-MIGRATION-EQUIVALENCE-FIXTURES`（迁移等价性 fixture 框架）| P3/触发型 | **Soft**（fixture 也 AI 可造假）| **撤回**——fixture 框架不解决 AI 漂移；改靠 review checklist + AI-rebust 升级累积消除 |
| `PR430-FU-SCANNER-INTERNAL-CONSOLIDATE-01`（scope.go Files/contentFiles 双轨 + 注释/行为不一致）| P2/Cx2 | Medium（refactor 提升整洁度）| 保留 |
| `PR430-FU-MIGRATION-DRIFT-CURRENT-FIXES-01`（5 case 漂移）| P1/Cx2 | — bug 修复无 AI-rebust 维度 | 保留 |
| `PR430-FU-SCANNER-SYMLINK-FAIL-CLOSED-01`（symlink fail-closed）| P2/Cx2 | Hard（lstat + skip 是 type-system 拦不住但 fail-closed by construction）| **升 P1**（symlink 是 AI 攻击面：恶意 PR 加 symlink 让 scanner 读模块外文件，发布前必修）|
| `PR431-FU-BFS-EMITTER-RECEIVER-TYPE-IDENT-01`（BFS emitter 按 receiver type）| ✅ done by PR-TS1（2026-05-11）| Soft → Medium-偏-Hard（signature-based 三谓词识别）| **已完成**——helper 落地 `tools/typesutil/`（非 `tools/archtest/internal/typeseval/`，避开 internal/ visibility 限制），BFS 改 signature-based（return ValidationResult + arg0 string + receiver/result 同包），删名字 allowlist 和 runtime guard；真 Hard 需 sealed interface emitter 留作未来工作 |
| `PR432-FU-AUTH-COMBO-ARCHTEST-DOUBLE-DEFENSE-01`（auth combo archtest 双重防线）| P3/触发型 | — 5 层 mirror 已 Hard | 保留触发型 |
| `PR438-FU-TEST-POLLING-DETERMINISM-01`（D500ms+Eventually flake-class，339 站点）| P2/触发型 | Soft → Hard 升级路径（对标 panic 单源 typed marker）| **挂 Wave 4 触发型**——事故密度驱动；PR #438 已是首例（race CI D500ms 踩穿），第二次事故 / 进入下一治理批时启动一锅替换 |

---

## 4. 升级路线（Wave 制，不再按 PR 编号叙事）

### Wave 1：章程落地 + 当前 bug 修复（本周）

| Wave 1 项 | 工时 | 与现 PR 关系 |
|---|---|---|
| **CLAUDE.md 加 "AI 协作章程" 段** | 1-2h | 独立 PR；明文承认 AI 实施者；工程目标 AI-rebust；review checklist 加 "AI-soft 检测" |
| **PR-Δ1 当前 bug + 安全收口**（PR430-FU-MIGRATION-DRIFT-CURRENT-FIXES + PR430-FU-SCANNER-SYMLINK-FAIL-CLOSED + PR430-FU-SCANNER-INTERNAL-CONSOLIDATE）| 8-11h dev / 3-4h review | 独立 PR；3 条合并（同 scanner 文件域）|

### Wave 2：未启动主线

| Wave 2 项 | 工时 | 与原计划关系 |
|---|---|---|
| **节点遍历漏斗（原 PR-Φ）** | 14-18h dev / 5-7h review | 保留（typed callback per node kind 是 AI-hard 范本）|
| **PR-Φ-HARD-EACHNODE-WALKDEPTH-01**（PR-Φ 补遗）✅ 已 ship (PR #460) | 12-16h dev / 4-5h review | **已 ship (PR #460)**。`scanner.EachNode[N](root, fn)` 拆为 `EachInSubtree[N]` + `EachInChildren[N]`，删 `EachNode`；让 walk depth 在编译期成为 typed choice（违反不可表达 = Hard）。实际范围：297 调用站点 / 64 文件 audit + bulk-rename；高风险站点（CompositeLit/SwitchStmt/SelectStmt 内嵌 KeyValueExpr/CaseClause/CommClause 类）逐个语义审计。完成后删除 `eachnode.go`（符号彻底消除）。7 commits（详见 PR #460 commit history；squash merge 后通过 PR 编号追溯）。**paired-index 收编**：原 plan 估 8 处，实际扩展到 14 处（path B 守卫翻牌命中 PR #445 之外的隐性 Soft 残留：adapter_returns_declared_types / goose_session_locker / redis_key_namespace / setup_admin_bootstrap_closure 等）——符合 §6.2 一锅替换不 ratchet。**PR445-FU F1**（contract_spec_clients_test.go paired-index）+ **F3**（rmq_invariants_test.go SelectStmt/容器）+ **4ad2b4a6 commit 已提取的 outbox/security helpers** 一并收编关闭。**吸收 PR #445 round-2 已确认的 direct-child 漂移**：codegen ContractSpec nested KeyValueExpr、ROLE-ADMIN top-level const、outbox top-level const。单源 backlog：`PR-Φ-HARD-EACHNODE-WALKDEPTH-01`。 |
| **panic 单源 typed marker（原 PR-D' 重审）✅ 已 ship (PR #TBD，2026-05-11)** | 7-10h dev / 3h review | **已 ship (PR #TBD)**。放弃"就地注释 ADR-approved"（Soft），改为 `pkg/panicregister.Approved("reason", value)` typed function 包装所有生产 panic（Hard）。archtest `PANIC-REGISTERED-01` 重写：删除 `architecturalPanicWhitelist` Go map、`AllowMust` prefix 豁免、ADR reconciliation guard；改为 AST + `*types.Info` 验证 panic arg = `panicregister.Approved(literal, _)` CallExpr。50 call-site 迁移（4 C-class re-throw + 4.2 目录 A/B-class programmer-error sites）。codegen template 更新。ADR `docs/architecture/202604270030-architectural-panic-whitelist.md` 同步重写（§4 reason catalog + §5 No prefix-based exemption + Mechanics 段）。`.claude/rules/gocell/error-handling.md` Panic taxonomy 段重写。`.claude/rules/gocell/ai-collab.md` Hard 范本新增 typed function call as Hard funnel 条目。 |

### Wave 3：AI-rebust 升级批（与 Wave 2 并行或之后）

| Wave 3 项 | 工时 | 当前 backlog |
|---|---|---|
| **typesutil helper（receiver type / interface impl）建立**（archtest 分层 §5 新层）| ResolveReceiverType ✅ done by PR-TS1（2026-05-11）；ImplementsInterface 待 PR-TS2 | helper 落地 `tools/typesutil/`（非 internal——kernel/governance 测试需要 import，internal/ 不通），保留 SharedResolver 在 `tools/archtest/internal/typeseval/`（archtest-specific 工具）。ResolveReceiverType 单测 10 cases 覆盖指针/值/包级/builtin/interface/方法值/嵌入/泛型/nil 边界；后续 ImplementsInterface 走同模式 |
| **TYPESEVAL-BUILDTAGS-FAILCLOSED-01**（PR #445 round-2） | 8-12h dev / 3-4h review | build-tag scope 统一收口：修 comment-group 漏扫、`FlatNonDefaultTags` 对 `!tag` / OR / 互斥 tag-set 的非等价问题、`tags=nil` 规则作用域收窄，以及 `KnownNonDefaultTags` godoc 鼓励 per-tag `SharedResolver` OOM 模式。单源 backlog：`TYPESEVAL-BUILDTAGS-COMMENTGROUP-COVERAGE-01`、`TYPESEVAL-BUILDTAGS-SCOPE-FAILCLOSED-01`、`TYPESEVAL-BUILDTAGS-LEGACY-DIRECTIVE-01`。 |
| **SCANNER-FRAMEWORK-USAGE-01-HARDENING**（PR #445 round-2） | 8-11h dev / 3-4h review | 补 scanner guard 与 type-aware matcher 的 fail-open 缺口：inspector instance methods、internal subpackage exact allowlist、auth dot-import/unqualified call matcher、generated/ skip cross-rule invariant。单源 backlog：`PR445-FU-SCANNER-FRAMEWORK-HARDENING-01`、`PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01`、`GENERATED-SKIP-CROSS-RULE-INVARIANT-01`。 |
| **INTERNAL-CONTRACT-CLIENTS-SOURCE-GUARD-01**（PR #445 round-2） | 5-7h dev / 2-3h review | 把 `/internal/v1/*` 必须声明 caller clients 的 enforcement 从手写 `wrapper.ContractSpec{}` AST 扫描上移到 contract YAML/governance 源头，与 FMT-28 互补。单源 backlog：`INTERNAL-CONTRACT-CLIENTS-SOURCE-GUARD-01`。 |
| **BFS emitter receiver-type 升级**（PR431-FU）| ✅ done by PR-TS1（2026-05-11）| BFS 改 signature-based 三谓词（return ValidationResult + arg0 string + receiver/result 同包），删 `isResultEmitter` 名字 allowlist 和 `assertEmitterMethodsRestrictedToLocator` runtime guard。同根的 USAGE-01 receiver method bypass 已由 PR #445 提前闭环。 |
| **PR-A' 锚点升级 typed function call**（PR #435 已 ship）| 4-5h dev / 1-2h review | 字符串 `// INVARIANT: ID` → `archtest.Invariant("ID")` 函数调用 + 同步 archtest 改 |

### Wave 4：触发型保留

| 项 | trigger |
|---|---|
| **HANDLER-POLICY-TYPEAWARE-SCANNER-01** | scanner 误报/漏报触发 |
| **SERVICEOWNED-OWNERSHIP-GUARD-01** | `auth.serviceOwned` endpoint > 1 |
| **B-FLOOR-FOLLOWUP §2.5/§4** | contract.yaml status ↔ adapter typed return 漂移事故首现 |
| **AUTH-COMBO-ARCHTEST-DOUBLE-DEFENSE**（PR432-FU）| `hasFMT27AuthModeConflict` 被重新 inline 化 / schema-oracle 漂移 |
| **TEST-POLLING-DETERMINISM typed marker**（PR438-FU）| 第二次 race CI flake 事故 / 进入下一 AI-rebust 升级批 / 339 站点中任一新增违反站点。**形态固定**：新建 `pkg/testutil/testwait/`（`External(reason)` typed marker for 真 wall-clock + `Deterministic` 收纳 channel-as-condvar）+ 一锅改造 339 站点 + 同 PR archtest 拦裸 `(require\|assert).Eventually`，**不 ratchet**（§6.2）。工时按 panic 单源（7-10h / ~34 站点）外推 24-32h dev / 6-8h review |
| **FINDFIRSTCHILD-TYPED-API-01**（PR460-FU）| 第 7 处 closure+done sentinel helper 出现 / 进入下一 AI-rebust 升级批。**形态固定**：`tools/archtest/internal/scanner` 加 `FindFirstChild[S any, N interface{*S; ast.Node}](root ast.Node, pred func(N) bool) (N, bool)` typed find-first API（语义：depth=1 + early-stop = typed function choice），把 6 处 closure+done helper（outbox_invariants_test.go × 4 / security_defaults_test.go::hasKey / contract_spec_clients_test.go × 2）一锅迁移，删除 sentinel 内联注释 + 删除 ai-collab.md "closure+done family" 提及。**触发条件**：PR #460 review 多席位回归提出此 Soft → Hard 升级路径；用户在 Plan Phase 3 Q2 已选定 closure+done（与 4ad2b4a6 commit found-bool 闭包形态一致），不在 PR #460 本身推翻。当 sentinel 7 处出现或下一 AI-rebust 升级批启动时立项。工时估 3-4h dev / 1h review |

---

## 5. archtest 8 层分层模型

GoCell invariant 不只 archtest 一种载体——按"违反可不可达 → AI-rebust 等级"分 8 层。新增 invariant 必须按下表选层。当前 117 个 INVARIANT ID，78 个 `tools/archtest/*_test.go` 文件。

| 层 | 名称 | 实现入口 | AI-rebust | 例 | 占比 |
|---|---|---|---|---|---|
| L0 | codegen funnel + golden 字节锁 | `tools/codegen/contractgen/*.tmpl` + `*.golden` | **Hard** | 4 个 HANDLER-* / typed response envelope / subscription_gen | 持续扩 |
| L1 | 路径级 import ban | `.golangci.yml` `linters.settings.depguard.rules`（list-mode strict）| **Hard** | LAYER-01..04 | 4 |
| L2 | 包级 import graph + 传递闭包 | `kernel/depgraph.Graph` + `archtest_test.go::checkLayering`，复用 `typeseval.SharedResolver` | **Medium** | LAYER-05/05T/06/06T/07/09/09T | 7 |
| L3 | go/types 全量 typed walk | 复用同一份 SharedResolver 的 `pkg.Types.Scope()` / `pkg.TypesInfo.TypeOf` / `findDisallowedTypePath` | **Hard** | LAYER-08（types.Scope 符号封禁）/ LAYER-10（exported API 类型 leak）| 2 |
| L4 | 选择性 typed（复用 SharedResolver）| `tools/archtest/internal/typeseval` 提供 helper：const folding / receiver type / interface impl 同属此层 | **Medium / Hard**（按 helper 强度）| MESSAGE-CONST-LITERAL / ERRCODE-KIND-LITERAL / DETAILS-SLOG-ATTR / EXPORTED-ERROR-NEW（const folding 已实现）；SCANNER-FRAMEWORK-USAGE-01 receiver method bypass（PR #445 已实现）；BFS emitter receiver-type（receiver type helper 暂缺，Wave 3 补）| ~10+ |
| L5 | 纯 AST 模式 + 文件域 import ban | `tools/archtest/internal/scanner.{ImportBan, EachFile, Scope, Report}` | **Medium / 可降为 Soft** | OUTBOX-* / RMQ-* / PANIC-* / SCANNER-FRAMEWORK-USAGE / HTTPUTIL-* | **70+（大头）** |
| L6 | 元数据 / 文件存在性 / YAML 派生 | `scanner.EachContentFile` + 自定义 yaml 解析 + AST decl 检查 | **Medium** | EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE / CODEGEN-CELL-GEN / ASSEMBLY-* | ~10 |
| L7 | 反身索引（archtest 自治）| `parser.ParseComments` + file-header CommentGroup 扫 `// INVARIANT: <ID>` | **Soft（自治容忍）** | INVENTORY-ANCHOR-REQUIRED-01 / INVENTORY-ANCHOR-VALID-ID-01 | 2 |

### 5.1 选层决策路径

```
违反可由 schema/marker 单源 → codegen 消除？
  ├─ 是 → L0
  └─ 否 → 是纯路径级 import ban？
        ├─ 是 → L1
        └─ 否 → 需要按 cell 归属 / 传递闭包 / import graph？
              ├─ 是 → L2
              └─ 否 → 需要 types.Type / types.Object 层信息？
                    ├─ exported API 类型 leak / 符号封禁（全量 typed walk）→ L3
                    └─ 受体类型 / interface 实现 / 常量折叠（选择性 typed）→ L4
                  否 → 规则真值在 Go AST 还是元数据？
                    ├─ AST → L5
                    └─ YAML / 派生文件 → L6
反身索引（archtest 自治）→ L7
```

### 5.2 L5 何时 Medium、何时 Soft

L5（scanner AST）默认 Medium，但落入下面任一形态即降为 Soft，必须升 L4 或 L0：

- **按方法名识别 receiver**：`f.ReadDir(...)` AST 看不到 `f` 是 `*os.File`（PR431-FU-BFS-EMITTER 仍待治理；同类 USAGE-01 已由 PR #445 升 type-aware 闭环）
- **按字符串注释豁免**：`// PANIC-REGISTERED-01: ADR-approved: ...`（panic 单源 typed marker 已 ship PR #TBD，2026-05-11）
- **按方法名 convention**：`newResult` / `Emit*` 等命名约定识别 emitter
- **hand-crafted fixture**：fixture 内容由人/AI 编写，不是 real source AST capture

L5 已知限制写在 `scanner_framework_usage_test.go` 文件头 known-limitation。新规则若真值需要 receiver type / interface impl，**直接选 L4，要求 typeseval 补 helper，不要在 L5 用方法名兜底**。

### 5.3 关键认知校正

- `typeseval.SharedResolver` 已存在并被 18 个 archtest 使用，process-wide singleflight 缓存——packages.Load 入口早就引入了（见 §6.3）
- "在 typeseval 加 receiver type / interface impl helper"（20-30h）和"全量 Pass 模式重写"（80-150h）是两件事，charter 早期版本混淆这两个数字以拒绝前者
- USAGE-01 receiver method bypass 已由 PR #445 实现 type-aware 闭环（`forbiddenWalkRefs` 走 `*types.Info`），证明"建入口"路径成本可接受；剩余 BFS emitter 等同根类规则按 Wave 3 推进

---

## 6. 设计原则（替代原 5-PR plan §2 决策段）

1. **不建 Registry / 中心化注册表**（保留）
2. **不立 frozen allowlist + ratchet 渐进锁**（保留，Path C 已证实是反模式）
3. **packages.Load 入口已存在；区分"建入口" vs "全量 Pass 模式迁移"两个数字**：
   - 现状：`tools/archtest/internal/typeseval.SharedResolver` 已经引入并被 18 个 archtest 使用（const folding + LAYER-08/10 type walk + depgraph）。说"未引入 packages.Load"是表述误导。
   - 已用层（§5 L2/L3/L4）：包级 import graph、go/types 全量 typed walk、常量折叠选择性 typed
   - 缺位（Wave 3 必做）：receiver type identity + interface impl 枚举的 typed helpers——成本 20-30h dev，复用既有 SharedResolver
   - 仍不做的：**全量 Pass 模式重写**——`go/analysis.Analyzer` DAG 重构所有 70+ archtest 是 80-150h dev，5-10× 当前 AI-rebust 收益，触发型保留
   - 两数字区分：建入口（20-30h）解锁 BFS emitter 等 receiver-type 类规则升 Hard（USAGE-01 同根已由 PR #445 提前闭环）；全量迁移（80-150h）是把 L5 全量上 Pass DAG，与本项无关
4. **不要双源对账**（保留）
5. **接受 funnel 不到的物理残留**（保留）
6. **不要 Soft 档新机制**（**新增**）：fixture 框架 / 字符串约定 / 注释豁免 / 名字 convention 一律拒绝
7. **AI 实施者承认**（**新增**）：所有 review / decision 默认按 AI-rebust 评级；Soft 档 finding 优先升级而非补丁

---

## 7. 引用

- 决策原则：`CLAUDE.md` `## 新增 invariant 决策原则` + 待加 `## AI 协作章程` 段（Wave 1 输出）
- ADR 范本：`docs/architecture/202605061500-adr-typed-response-envelope.md` §D6/D7（typed response envelope, AI-hard 范例）
- 5-PR 主线 roadmap（实施进度视角）：`docs/plans/202605070431-pr403-funnel-fix-roadmap.md`
- 配套 plan（含开源对标 + Path C / PR-B / PR-C 实施细节）：`~/.claude-ming/plans/ast-ast-inspect-inherited-wadler.md`
- ref（节点 API）：golang/tools `go/analysis/passes/lostcancel`@master
- ref（typed marker for panic）：golang/go `runtime.SetFinalizer` typed registration 模式 / cockroachdb/cockroach `pkg/util/log.Fatal` typed wrapper
