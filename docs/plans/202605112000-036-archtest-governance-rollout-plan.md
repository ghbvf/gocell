# archtest / governance 治理 Rollout 计划

**生成日期**：2026-05-11
**关系**：本文件是 `docs/plans/202605101300-ai-first-governance-charter.md`（AI-first 章程）的 **PR 级 rollout 视角**。章程是第一性原理 + 决策原则视角不动；本文件按 PR #445 复盘教训（scope 失控、多维捆绑）把章程 Wave 1-4 翻译成"单 PR = 单维度"的可独立 ship 的 PR 序列，含 Phase 顺序、依赖图、优先级。冲突时以本文件的拆分为准，章程的判断/原则不变。

**核心拆分规则**：新增/修改工程机制的 PR 不得跨维度捆绑。维度清单：
- **framework**（typed function / sealed marker / helper API 建立）
- **bulk migration**（已有站点的批量改造）
- **archtest enforcement**（守卫静态规则建立）
- **special case**（保留豁免 / re-throw 处理）

多维度工作必须拆 N 个 PR，第一个 PR 是 framework，最后一个 PR 是 archtest enforcement（确保 enforcement 上线时所有站点已合规）。理由：PR #445 教训——把 framework + 5 批 bulk migration + path A/A'/B 三路同改一锅塞，触发 type-aware 边界 5 维 fail-open 缝隙同时暴露，两轮 review 后仍遗留 8+ 条 FU。

---

## 0. 状态同步（Wave 1 + 已完成项 backlog 收尾）

### Wave 1 ✅ 全部 done（已 ship）
| 项 | 证据 | backlog 同步动作 |
|---|---|---|
| CLAUDE.md AI 协作章程段 | `.claude/rules/gocell/ai-collab.md` + CLAUDE.md L73 | — |
| PR-Δ1（PR430-FU MIGRATION-DRIFT + SYMLINK + CONSOLIDATE 三条合并）| PR #440 `b30afc52` | backlog 3 条标 ✅ done |

### Wave 3 已 ship 项
| 项 | 证据 |
|---|---|
| PR-A' 锚点（INVENTORY-ANCHOR-REQUIRED-01）| PR #435 |
| PR-Φ 主线 + PR-Φ-HARD-EACHNODE-WALKDEPTH-01 | PR #445 + PR #460 |
| PR430-FU-USAGE-01-TYPE-AWARE | closed by PR #445 |
| PR445-FU-PACKAGEALIASES-TYPE-AWARE-01 | closed by PR #445 |

### 撤回项（backlog 应标 WONTFIX）
- `PR430-FU-MIGRATION-EQUIVALENCE-FIXTURES` — charter §3 已撤回（fixture 框架 AI 可造假，是 Soft 形态）

### Phase 0 实施
**Phase 0.1：backlog 状态收尾**（独立小 PR，1-2h dev）
- `docs/backlog.md` line 408/409/410 三条 PR430-FU 标 ✅ closed by PR #440
- `docs/backlog.md` line 407 `PR430-FU-MIGRATION-EQUIVALENCE-FIXTURES` 标 WONTFIX（reason: charter §3 撤回，fixture 框架 AI 可造假）
- 同步移动到 `docs/backlog/archive/2026-q2-completed.md`（按 backlog.md schema 行 14）

---

## 1. Wave 2 panic 单源 typed marker（按维度拆 3 PR）

### 章程原始描述
> 改成 `panicregister.Approved("reason")` typed function 包装 panic + 同 PR 加 archtest 强制 + 4 处 re-throw + 30+ Must* 改造

→ 4 维捆绑（framework + bulk migration + enforcement + special case）。按拆分规则改成 3 PR：

### Phase 1.1：panicregister framework + special case（PR-PR1）
- **范围**：
  - 新建 `kernel/panicregister/` 包，提供 `Approved(reason string) func(any)` typed wrapper 或 `panicregister.Mustf(format, args...)` 形式（具体 API 在本 PR 内决策）
  - 同时处理 4 处 re-throw（架构师 C 类豁免，charter §3 + error-handling.md "Assertion vs panic"）：lifecycle 启动超时、circuit_breaker、tx_manager、websocket / metrics / kernel/cell bootstrap fatal
- **不含**：30+ Must* 函数包装、archtest 强制
- **工时**：3-4h dev / 1h review
- **特别要求**：本 PR 单独 dogfood 一个最简调用点（比如某个 Must* 函数）作为范本，验证 API 形态
- **依赖**：无

### Phase 1.2：30+ Must* 包装 bulk migration（PR-PR2）
- **范围**：把当前 30+ `MustXxx` 函数内部的 `panic(...)` 全部换 `panicregister.Approved("...")` 或等价 typed wrapper
- **不含**：framework 改动、archtest 强制
- **工时**：3-4h dev / 1h review
- **依赖**：Phase 1.1 merge

### Phase 1.3：panic archtest enforcement + 删除旧 allowlist（PR-PR3）
- **范围**：
  - 新增 archtest 强制所有 `panic(...)` 必须经 `panicregister.Approved` 包装
  - 删 `panic_invariants_test.go` 中 `architecturalPanicWhitelist` + `AllowMust` 字符串约定（charter §1 Soft 形态彻底消除）
  - 同时关闭 `PR419-FU-PANIC-MUST-PATH-SCOPE-01`（path-scope 窄化方案被 typed marker 替代）
- **不含**：任何 panic 站点改动
- **工时**：1-2h dev / 1h review
- **依赖**：Phase 1.2 全部合并（否则 enforcement 上线时仍有未包装站点 → fail-closed 触发）

---

## 2. Wave 3 typeseval helper + 下游 type-aware 规则升级（按 helper 维度拆，dogfood 不捆绑）

### 章程原始描述
> 在 `tools/archtest/internal/typeseval` 加 `ResolveReceiverType(typesInfo, callExpr) (*types.Named, bool)` + `ImplementsInterface(typesInfo, expr, ifaceObj) bool`；20-30h dev

→ 把"helper 建立"和"下游规则迁移"拆开。20-30h 估值的膨胀来自把多个规则迁移捆绑算。

### Phase 2.1：typesutil `ResolveReceiverType` helper + dogfood（PR-TS1）✅ done
- **范围**：
  - `tools/typesutil/receiver_type.go` 加 `ResolveReceiverType(typesInfo *types.Info, call *ast.CallExpr) (*types.Named, bool, bool)`（位置改 `tools/typesutil/` 而非 `tools/archtest/internal/typeseval/`——kernel/governance 测试无法 import `internal/` 包；shared helper 才能真正被 BFS dogfood 消费）
  - dogfood：BFS emitter type-aware 升级（PR431-FU-BFS-EMITTER-RECEIVER-TYPE-IDENT-01）。`kernel/governance/rule_inventory_test.go` handleCall 重写为 signature-based（return ValidationResult + arg0 string + receiver/result 同包），删 `isResultEmitter` 名字 allowlist 和 `assertEmitterMethodsRestrictedToLocator` runtime guard
- **不含**：`ImplementsInterface` helper、ARCHTEST-TYPEAWARE-HARDENING bundle 其他子条迁移
- **工时**：实际 ~6h dev
- **依赖**：无
- **关闭 backlog**：`PR431-FU-BFS-EMITTER-RECEIVER-TYPE-IDENT-01` ✅（charter §3 line 79 覆盖架构师 2026-05-10 "不升级" 决议）
- **AI-rebust 升级**：Soft（名字约定）→ Medium-偏-Hard（三重 type 谓词）。真 Hard 需 sealed interface emitter，超 PR 范围

### Phase 2.2：typeseval `ResolveCallTargetFunc` helper + dogfood（PR-TS2）
- **范围**：
  - `tools/archtest/internal/typeseval/call_target.go` 加 `ResolveCallTargetFunc(typesInfo *types.Info, fun ast.Expr) (*types.Func, bool)`
  - dogfood：给 `scanner_framework_usage_test.go::forbiddenWalkRefs` 加 Ident 分支，覆盖 dot-import 路径（PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01 dogfood scope 闭合）
- **不含**：bundle 其他子条（svctoken / role_admin 剩余 dot-import 迁移留 Phase 3.2）；`ImplementsInterface` helper 不在本 PR 内（详见下方"helper 命名修订"）
- **工时**：6-8h dev / 2h review
- **依赖**：无（与 Phase 2.1 可并行）

### helper 命名修订（plan v0 → v1）
plan 初稿 §2.2 写 `ImplementsInterface` 但 dogfood 写 call-matcher（"裸 Ident / dot-import 匹配"）—— 两者不是同一个 helper。修订后：
- **PR-TS2 实际做 call-matcher**（`ResolveCallTargetFunc`），与 dogfood / Phase 3.2 依赖断言一致
- **`ImplementsInterface` helper 不在 rollout plan 内**，移至 backlog 作为触发型 refactor（ID：`TYPESEVAL-IFACE-IMPL-HELPER-CONSOLIDATE-01`，触发：第 4 处 runtime guard 重复 / 新规则真值需要 interface impl 枚举）。当前 3 处重复（如 `rmq_invariants_test.go::implementsAMQPChannel`）属 refactor 收编，紧迫度低
- charter §5 L4 "interface impl 枚举"语义需求仍承认，但 helper 化驱动由真实场景触发，不预先立项

---

## 3. Wave 3 ARCHTEST-TYPEAWARE-HARDENING bundle 拆 4 PR

### backlog 现状
`ARCHTEST-TYPEAWARE-HARDENING-BUNDLE`（line 418）8 子条吸收了 charter Wave 3/4 多项。bundle 是 **backlog 索引视角**，不是 PR 视角。按维度拆回 4 PR：

### Phase 3.1：build-tag fail-closed（PR-BT1）
- **范围**：3 子条同主题（typeseval build-tag scope）
  - `TYPESEVAL-BUILDTAGS-COMMENTGROUP-COVERAGE-01`：bufio.Scanner → `go/parser.ParseFile + ParseComments`
  - `TYPESEVAL-BUILDTAGS-SCOPE-FAILCLOSED-01`：`FlatNonDefaultTags` 对 `!tag`/OR/互斥 tag-set 改 tag-set evaluator + per-rule helper
  - `TYPESEVAL-BUILDTAGS-LEGACY-DIRECTIVE-01`：扩 coverage 自检覆盖 `// +build` legacy directive
- **工时**：8-12h dev / 3-4h review
- **依赖**：无（typeseval helper 不依赖）

### Phase 3.2：scanner / call-matcher hardening（PR-SH1）
- **范围**：2 子条同主题（scanner 守卫面 + path A' matcher）
  - `PR445-FU-SCANNER-FRAMEWORK-HARDENING-01`：`*inspector.Inspector` 实例方法 `Preorder/Nodes/WithStack` 加 forbiddenMethodSymbols；scanner 内部 subpackage exact allowlist
  - `PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01`：path A' matcher 支持裸 Ident（dot-import）—— Phase 2.2 PR-TS2 已 dogfood `ResolveCallTargetFunc` helper（forbiddenWalkRefs Ident 分支），本 PR 把剩余 2 个规则（svctoken / role_admin）按同一 helper 迁移
- **工时**：8-11h dev / 3-4h review
- **依赖**：Phase 2.2 PR-TS2 merge

### Phase 3.3：generated-skip cross-rule invariant（PR-SH2）
- **范围**：1 子条独立
  - `GENERATED-SKIP-CROSS-RULE-INVARIANT-01`：archtest cross-test invariant 扫 `SharedResolver/LoadPackages + ./... + pkg.Syntax/pkgFileRel` 组合，要求显式 `typeseval.IsGeneratedRelPath` 或 allowlist
- **工时**：3-4h dev / 1-2h review
- **依赖**：无（独立 cross-rule invariant）

### Phase 3.4：INTERNAL-CONTRACT-CLIENTS 上移 governance（PR-IC1）
- **范围**：1 子条独立
  - `INTERNAL-CONTRACT-CLIENTS-SOURCE-GUARD-01`：把 `/internal/v1/*` 必须声明 caller clients 从手写 `wrapper.ContractSpec{}` AST 扫描上移到 contract YAML/governance（charter §5.1 决策树：L5 → L0/L6 载体纠偏）
- **工时**：5-7h dev / 2-3h review
- **依赖**：无（载体上移，与 type-aware 边界无关）

---

## 4. Wave 4 触发型——保留 + 落地时按维度拆模板

### 触发条件不变（charter §4 line 113-122）
| 项 | trigger（与 charter 一致）|
|---|---|
| HANDLER-POLICY-TYPEAWARE-SCANNER-01 | scanner 误报/漏报触发 |
| SERVICEOWNED-OWNERSHIP-GUARD-01 | `auth.serviceOwned` endpoint > 1 |
| B-FLOOR-FOLLOWUP §2.5/§4 | contract.yaml status ↔ adapter typed return 漂移事故首现 |
| AUTH-COMBO-ARCHTEST-DOUBLE-DEFENSE | `hasFMT27AuthModeConflict` 被重新 inline 化 |
| TEST-POLLING-DETERMINISM typed marker | 第二次 race CI flake / 进入下一治理批 / 339 站点新增违反 |
| FINDFIRSTCHILD-TYPED-API-01 | 第 7 处 closure+done sentinel helper 出现 |

### 落地时统一按维度拆模板
有 framework + bulk migration + enforcement 三维以上的项目（**TEST-POLLING-DETERMINISM** 和 **FINDFIRSTCHILD-TYPED-API-01** 都是），触发时按以下模板拆：

**Template-Wave4-3PR**：
- PR1（framework）：新建 `pkg/testutil/testwait/` or `scanner.FindFirstChild` typed API + 单点 dogfood
- PR2（bulk migration）：339 站点 / 6 处 sentinel helper 批量迁移
- PR3（archtest enforcement）：拦裸 `Eventually` / 拦 closure+done sentinel + 删旧 allowlist

### TEST-POLLING-DETERMINISM 工时重估
- 章程原估：24-32h dev / 6-8h review（一锅推）
- 按维度拆：PR1 4-6h + PR2 12-16h + PR3 2-3h = **18-25h dev**（review 总量不变但分摊）
- 收益：每 PR review 面积 ≤ PR #445 一半，fail-open 缝隙按维度暴露而非同时

### FINDFIRSTCHILD-TYPED-API-01 工时重估
- 章程原估：3-4h dev / 1h review（整批小）
- 按维度拆：3-4h 太小，可以保留单 PR，但要求**先 framework + 一个 dogfood**，**再批 5 处迁移 + archtest 同 commit**（PR 内 commit-level 分维度）
- 例外理由：6 处 sentinel helper 总改动 < 200 LOC，review 面积可控

---

## 5. 实施依赖图与并行度

```
Phase 0.1 (backlog 收尾)  独立  ─┐
                                 │
Phase 1.1 (panicregister fw)  ──┐│   独立 (可与 0.1/2.x 并行)
Phase 1.2 (Must* migration)   ←─┤│
Phase 1.3 (archtest enforce)  ←─┘│
                                 │
Phase 2.1 (ResolveReceiverType) ─┤  独立 (helper #1 + BFS emitter dogfood)
Phase 2.2 (ResolveCallTargetFunc) ┤  独立 (call-matcher helper + forbiddenWalkRefs Ident dogfood，与 2.1 并行)
                                 │
Phase 3.1 (build-tag failclosed) │  独立
Phase 3.2 (scanner hardening)  ←─┤  需 Phase 2.2 merge
Phase 3.3 (generated-skip)       │  独立
Phase 3.4 (internal-contract)    │  独立
                                 │
Wave 4 触发型              触发后 按 Template-Wave4-3PR
```

并行窗口：
- **Window 1**：Phase 0.1 + 1.1 + 2.1 + 2.2 + 3.1 + 3.3 + 3.4 七个 PR 完全独立可并行
- **Window 2**：Phase 1.2 (依赖 1.1) + 3.2 (依赖 2.2)
- **Window 3**：Phase 1.3 (依赖 1.2)

---

## 6. 优先级 next-up（按"解锁后续"排序）

| Rank | Phase | 解锁的下游 | 工时 |
|---|---|---|---|
| 1 | Phase 2.1 PR-TS1（ResolveReceiverType helper + BFS emitter dogfood）✅ done | Phase 3.2 scanner hardening 部分 + 关闭 PR431-FU | 6-8h |
| 2 | Phase 2.2 PR-TS2（ResolveCallTargetFunc helper + forbiddenWalkRefs Ident dogfood）| Phase 3.2 scanner hardening 剩余 | 6-8h |
| 3 | Phase 0.1（backlog 收尾，独立小）| — | 1-2h |
| 4 | Phase 1.1 PR-PR1（panicregister framework）| Phase 1.2/1.3 | 3-4h |
| 5 | Phase 3.1 PR-BT1（build-tag fail-closed）| — | 8-12h |
| 6 | Phase 3.3 PR-SH2（generated-skip cross-rule）| — | 3-4h |
| 7 | Phase 3.4 PR-IC1（internal-contract 上移 L0/L6）| — | 5-7h |
| 8 | Phase 1.2 PR-PR2（Must* migration，依赖 1.1）| — | 3-4h |
| 9 | Phase 3.2 PR-SH1（scanner hardening，依赖 2.2）| — | 8-11h |
| 10 | Phase 1.3 PR-PR3（panic archtest enforcement，依赖 1.2）| — | 1-2h |

总工时（Wave 2 + 3）：**50-70h dev / 17-23h review**
对照章程原估：~73-95h dev / 18-25h review（按 charter §4 加总）
减幅约 25%（不是因为干得少，而是 PR #445 类的"两轮 review 仍遗留 8+ 条 FU"在按维度拆后大幅减少）

---

## 7. 与章程的差异点

| 章程 §4 | 本计划 | 差异理由 |
|---|---|---|
| panic 单源一锅推（7-10h）| 拆 3 PR（Phase 1.1/1.2/1.3）| PR #445 教训：framework + migration + enforcement 同 PR 触发多维 fail-open |
| typeseval helper 20-30h | 拆 Phase 2.1/2.2 各 6-8h | helper 本身代码小，膨胀来自捆绑 dogfood；按"helper + 一个 dogfood"切片 |
| ARCHTEST-TYPEAWARE-HARDENING bundle 推 | 拆 Phase 3.1/3.2/3.3/3.4 共 4 PR | bundle 是 backlog 索引视角，不是 PR 视角 |
| TEST-POLLING-DETERMINISM 一锅推（24-32h）| Template-Wave4-3PR 拆 3 PR | 同 panic 单源理由 |
| FINDFIRSTCHILD-TYPED-API-01 单 PR | 保留单 PR，但 commit-level 分维度 | 总改动 < 200 LOC，review 面积可控，是例外 |

---

## 8. 引用

- 章程（原则视角）：`docs/plans/202605101300-ai-first-governance-charter.md`
- PR #445 复盘（scope 失控元教训）：本对话 §"PR #445 为什么遗留这么多问题" 5 层根因分析
- backlog（条目索引）：`docs/backlog.md`（line 47/48/82/317/404-410/418/419/461 为本计划涉及条目）
- AI 协作章程：`.claude/rules/gocell/ai-collab.md`
- 5-PR 主线 roadmap：`docs/plans/202605070431-pr403-funnel-fix-roadmap.md`
