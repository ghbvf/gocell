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

### 未启动

| 名 | 简述 | 第一性原理判 |
|---|---|---|
| 节点遍历漏斗（原 PR-Φ）| framework typed callback per node kind + 一锅替换 65 手写 for-loop + 131 裸 ast.Inspect | Hard 档，**保留启动**（typed callback 是 AI-hard）|
| panic 单源（原 PR-D'）| 删 architecturalPanicWhitelist + AllowMust + reconciliation guard，改单源 | **方案重审**：原计划"就地注释 `// PANIC-REGISTERED-01: ADR-approved:`"是 Soft 档（AI 可写任意 reason 通过）→ 应改 typed marker 函数 |
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
| `PR431-FU-BFS-EMITTER-RECEIVER-TYPE-IDENT-01`（BFS emitter 按 receiver type）| P3/触发型 | Soft → Hard 升级路径（receiver type identity）| **升 P1 必做**——convention-by-name 在 AI 项目里就是真问题，`assertEmitterMethodsRestrictedToLocator` runtime guard 仅是缓解；走 Wave 3 §5 typeseval helper（receiver type / interface impl）；同根的 USAGE-01 已由 PR #445 提前闭环，本条独立触发 |
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
| **PR-Φ-HARD-EACHNODE-WALKDEPTH-01**（PR-Φ 补遗） | 12-16h dev / 4-5h review | **本 PR-Φ (PR #445) 合并后立即立项**。`scanner.EachNode[N](root, fn)` 拆为 `EachInSubtree[N]` + `EachInChildren[N]`，删 `EachNode`；让 walk depth 在编译期成为 typed choice（违反不可表达 = Hard）。范围：283 调用站点 / 54 文件 audit + bulk-rename；高风险站点（CompositeLit/SwitchStmt/SelectStmt 内嵌 KeyValueExpr/CaseClause/CommClause 类）逐个语义审计。完成后删除 `eachnode.go` godoc 的 transitional Trap 段（PR #445 Wave 2 加入）。**关闭 PR445-FU 中三处用 paired-index 内联绕过的 Soft 残留**：contract_spec_clients_test.go (F1)、rmq_invariants_test.go SelectStmt/容器 (F3)、4ad2b4a6 commit 已提取的 outbox/security helpers。 |
| **panic 单源 typed marker（原 PR-D' 重审）** | 7-10h dev / 3h review | **方案改造**：放弃"就地注释 ADR-approved"（Soft），改成 `panicregister.Approved("reason")` typed function 包装 panic（Hard）；同 PR 加 archtest 强制所有 `panic(...)` 必须包装 + 4 处 re-throw + 30+ Must* 改造 |

### Wave 3：AI-rebust 升级批（与 Wave 2 并行或之后）

| Wave 3 项 | 工时 | 当前 backlog |
|---|---|---|
| **typeseval helper（receiver type / interface impl）建立**（archtest 分层 §5 新层）| 20-30h dev / 5-7h review | 在 `tools/archtest/internal/typeseval` 加 `ResolveReceiverType(typesInfo, callExpr) (*types.Named, bool)` + `ImplementsInterface(typesInfo, expr, ifaceObj) bool`；复用既有 `SharedResolver`（已存在，18 个 archtest 在用）；不动 Pass 模式（那是 80-150h，与本项是两件事）|
| **BFS emitter receiver-type 升级**（PR431-FU）| 8-12h dev / 2-3h review | BFS 按 receiver 类型识别 emitter，替代 convention-by-name；依赖上一项 typeseval helper（receiver type / interface impl）已建。同根的 USAGE-01 receiver method bypass 已由 PR #445 提前闭环，无需双升。 |
| **PR-A' 锚点升级 typed function call**（PR #435 已 ship）| 4-5h dev / 1-2h review | 字符串 `// INVARIANT: ID` → `archtest.Invariant("ID")` 函数调用 + 同步 archtest 改 |

### Wave 4：触发型保留

| 项 | trigger |
|---|---|
| **HANDLER-POLICY-TYPEAWARE-SCANNER-01** | scanner 误报/漏报触发 |
| **SERVICEOWNED-OWNERSHIP-GUARD-01** | `auth.serviceOwned` endpoint > 1 |
| **B-FLOOR-FOLLOWUP §2.5/§4** | contract.yaml status ↔ adapter typed return 漂移事故首现 |
| **AUTH-COMBO-ARCHTEST-DOUBLE-DEFENSE**（PR432-FU）| `hasFMT27AuthModeConflict` 被重新 inline 化 / schema-oracle 漂移 |
| **GENERATED-SKIP-CROSS-RULE-INVARIANT-01**（PR445-FU F4 后续）| PR #445 合并后立项。archtest cross-test invariant：保证未来新规则若用 `typeseval.SharedResolver(...).Packages()` 后迭代文件路径，必须显式调 `typeseval.IsGeneratedRelPath` 或 explicit allowlist。当前唯一调用点是 `outbox_invariants_test.go` 的 OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01；第 2 处规则需要扫 generated 时立项加守卫。 |
| **TYPESEVAL-BUILDTAGS-LEGACY-DIRECTIVE-01**（PR445-FU F2 后续）| 仓库出现 `// +build` 旧式 directive 时立项。当前 `TestKnownNonDefaultTagsCoverage` 仅 grep `//go:build`；扩 coverage 自检覆盖旧式 directive，避免单一 lint pass 同时漏 helper 集合的双源漂移。 |
| **TEST-POLLING-DETERMINISM typed marker**（PR438-FU）| 第二次 race CI flake 事故 / 进入下一 AI-rebust 升级批 / 339 站点中任一新增违反站点。**形态固定**：新建 `pkg/testutil/testwait/`（`External(reason)` typed marker for 真 wall-clock + `Deterministic` 收纳 channel-as-condvar）+ 一锅改造 339 站点 + 同 PR archtest 拦裸 `(require\|assert).Eventually`，**不 ratchet**（§6.2）。工时按 panic 单源（7-10h / ~34 站点）外推 24-32h dev / 6-8h review |

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
- **按字符串注释豁免**：`// PANIC-REGISTERED-01: ADR-approved: ...`（panic 单源 typed marker 是 Wave 2 必做）
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
