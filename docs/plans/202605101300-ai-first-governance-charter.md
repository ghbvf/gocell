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

---

## 3. 已知 follow-up 与 AI-rebust 评级

| 条目 | 当前 backlog 优先级 | AI-rebust 评级 | 真正应做的 |
|---|---|---|---|
| ~~`PR430-FU-USAGE-01-TYPE-AWARE`（receiver method bypass）~~| ~~P2/触发型~~ | ~~当前 Hard 但有边界~~ | **CLOSED — merged into PR-Φ** (refactor/552-archtest-eachnode-funnel)：packages.Load 入口由 PR-Φ 路径 B 强制引入后成本已 sunk，SCANNER-FRAMEWORK-USAGE-01 整规则一次性升 type-aware 顺手清。`forbiddenWalkRefs` 现走 `*types.Info`（package-level + receiver method 同时拦截）。 |
| `PR430-FU-MIGRATION-EQUIVALENCE-FIXTURES`（迁移等价性 fixture 框架）| P3/触发型 | **Soft**（fixture 也 AI 可造假）| **撤回**——fixture 框架不解决 AI 漂移；改靠 review checklist + AI-rebust 升级累积消除 |
| `PR430-FU-SCANNER-INTERNAL-CONSOLIDATE-01`（scope.go Files/contentFiles 双轨 + 注释/行为不一致）| P2/Cx2 | Medium（refactor 提升整洁度）| 保留 |
| `PR430-FU-MIGRATION-DRIFT-CURRENT-FIXES-01`（5 case 漂移）| P1/Cx2 | — bug 修复无 AI-rebust 维度 | 保留 |
| `PR430-FU-SCANNER-SYMLINK-FAIL-CLOSED-01`（symlink fail-closed）| P2/Cx2 | Hard（lstat + skip 是 type-system 拦不住但 fail-closed by construction）| **升 P1**（symlink 是 AI 攻击面：恶意 PR 加 symlink 让 scanner 读模块外文件，发布前必修）|
| `PR431-FU-BFS-EMITTER-RECEIVER-TYPE-IDENT-01`（BFS emitter 按 receiver type）| P3/触发型 | Soft → Hard 升级路径 | **升 P1 必做**——convention-by-name 在 AI 项目里就是真问题，`assertEmitterMethodsRestrictedToLocator` runtime guard 仅是缓解 |
| `PR432-FU-AUTH-COMBO-ARCHTEST-DOUBLE-DEFENSE-01`（auth combo archtest 双重防线）| P3/触发型 | — 5 层 mirror 已 Hard | 保留触发型 |

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
| **panic 单源 typed marker（原 PR-D' 重审）** | 7-10h dev / 3h review | **方案改造**：放弃"就地注释 ADR-approved"（Soft），改成 `panicregister.Approved("reason")` typed function 包装 panic（Hard）；同 PR 加 archtest 强制所有 `panic(...)` 必须包装 + 4 处 re-throw + 30+ Must* 改造 |

### Wave 3：AI-rebust 升级批（与 Wave 2 并行或之后）

| Wave 3 项 | 工时 | 当前 backlog |
|---|---|---|
| **BFS emitter receiver-type 升级**（PR431-FU 升 P1）| 10-15h dev / 3-4h review | 引入 `golang.org/x/tools/go/packages` + `types.Info`；BFS 按 receiver 类型识别 emitter；同时为 Wave 4 USAGE-01-TYPE-AWARE 铺路（共享 packages.Load 入口）|
| **PR-A' 锚点升级 typed function call**（PR #435 已 ship）| 4-5h dev / 1-2h review | 字符串 `// INVARIANT: ID` → `archtest.Invariant("ID")` 函数调用 + 同步 archtest 改 |

### Wave 4：触发型保留

| 项 | trigger |
|---|---|
| **USAGE-01 type-aware 完整版**（PR430-FU-USAGE-01-TYPE-AWARE）| 真实 method-call bypass 事故首现 / 项目 archtest 数到 100+ / Pass 模式重写成本可摊销 |
| **HANDLER-POLICY-TYPEAWARE-SCANNER-01** | scanner 误报/漏报触发 |
| **SERVICEOWNED-OWNERSHIP-GUARD-01** | `auth.serviceOwned` endpoint > 1 |
| **B-FLOOR-FOLLOWUP §2.5/§4** | contract.yaml status ↔ adapter typed return 漂移事故首现 |
| **AUTH-COMBO-ARCHTEST-DOUBLE-DEFENSE**（PR432-FU）| `hasFMT27AuthModeConflict` 被重新 inline 化 / schema-oracle 漂移 |

---

## 6. 设计原则（替代原 5-PR plan §2 决策段）

1. **不建 Registry / 中心化注册表**（保留）
2. **不立 frozen allowlist + ratchet 渐进锁**（保留，Path C 已证实是反模式）
3. **不引入 packages.Load** 仍正确，但**论证修正**：
   - 不是"CI 时间爆炸"——naive 引入会爆，但 Pass 模式重构后反而更快
   - 真正不引入的理由：**Pass 模式重写成本 80-150h dev，是当前 AI-rebust 收益的 5-10×**
   - 触发再评估：单实例 packages.Load 复用入口建立后（Wave 3 BFS receiver-type 升级会建立这个入口）→ 增量 archtest 升级到 type-aware 性价比变正
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
