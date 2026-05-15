# archtest / governance 治理 Rollout 计划

**生成日期**：2026-05-11
**最近同步**：2026-05-12（origin/develop @ `8d213883`，Wave 2 panic 单源 + Phase 2.1/2.2/3.1/3.2/3.3/3.4/3.5/3.7/3.8/3.9 已 ship；Phase 0.1 ✅ done，Phase 3.6 ❌ cancelled by PR #435；剩余 Wave 2/3 = PR-MD1 follow-up `CELLGEN-LITERAL-FUNNEL-02`）
**关系**：本文件是 `docs/plans/202605101300-ai-first-governance-charter.md`（AI-first 章程）的 **PR 级 rollout 视角**。章程是第一性原理 + 决策原则视角不动；本文件按 PR #445 复盘教训（scope 失控、多维捆绑）把章程 Wave 1-4 翻译成"单 PR = 单维度"的可独立 ship 的 PR 序列，含 Phase 顺序、依赖图、优先级。冲突时以本文件的拆分为准，章程的判断/原则不变。

**核心拆分规则**：新增/修改工程机制的 PR 不得跨维度捆绑。维度清单：
- **framework**（typed function / sealed marker / helper API 建立）
- **bulk migration**（已有站点的批量改造）
- **archtest enforcement**（守卫静态规则建立）
- **special case**（保留豁免 / re-throw 处理）

多维度工作必须拆 N 个 PR，第一个 PR 是 framework，最后一个 PR 是 archtest enforcement（确保 enforcement 上线时所有站点已合规）。理由：PR #445 教训——把 framework + 5 批 bulk migration + path A/A'/B 三路同改一锅塞，触发 type-aware 边界 5 维 fail-open 缝隙同时暴露，两轮 review 后仍遗留 8+ 条 FU。

---

## 0. 状态同步（Wave 1 + 已完成项 backlog 收尾）

### 撤回项（backlog 应标 WONTFIX）
- `PR430-FU-MIGRATION-EQUIVALENCE-FIXTURES` — charter §3 已撤回（fixture 框架 AI 可造假，是 Soft 形态）

### Phase 0 实施
**Phase 0.1：backlog 状态收尾** ✅ done (2026-05-12, in-place backlog 直接标记)
- ✅ `docs/backlog.md` 三条 PR430-FU 标 ✅ closed by PR #440：`PR430-FU-SCANNER-INTERNAL-CONSOLIDATE-01` / `PR430-FU-MIGRATION-DRIFT-CURRENT-FIXES-01` / `PR430-FU-SCANNER-SYMLINK-FAIL-CLOSED-01`
- ✅ `PR430-FU-MIGRATION-EQUIVALENCE-FIXTURES` 标 ❌ WONTFIX（reason: charter §3 撤回，fixture 框架 AI 可造假，违反 AI-rebust 立项硬门槛 ≥ Medium）
- ✅ Wave 3 已 ship 条目同步：
  - `PR431-FU-BFS-EMITTER-RECEIVER-TYPE-IDENT-01`（`docs/backlog/cap-02-metadata-governance.md`）已 ✅ closed by PR-TS1（无需改动）
  - `ARCHTEST-TYPEAWARE-HARDENING-BUNDLE` 8 子条：`TYPESEVAL-BUILDTAGS-*` 3 条 ✅ closed by PR #472；`GENERATED-SKIP-CROSS-RULE-INVARIANT-01` ✅ closed by PR #567；`INTERNAL-CONTRACT-CLIENTS-SOURCE-GUARD-01` ✅ closed by PR-IC1；`PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01` helper ✅ closed by PR-TS2（caller 迁移仍 OPEN 转 Phase 3.2 PR-SH1）；bundle 主条目状态从 🟠 改 🟢 partial（trigger 列写明剩余去向）
- ⏳ 归档：✅/WONTFIX 条目移 `docs/backlog/archive/2026-q2-completed.md` —— 按 backlog schema 行 28 "归档：人工"，留待手动归档

---

## 1. Wave 2 panic 单源 typed marker ✅ 已 ship（PR #467 一锅推）

### 章程原始描述
> 改成 `panicregister.Approved("reason")` typed function 包装 panic + 同 PR 加 archtest 强制 + 4 处 re-throw + 30+ Must* 改造

### 实际落地：PR #467（charter Wave 2 panic-single-source 行）
本计划原拟按"framework / bulk migration / enforcement"拆 3 PR。实际 PR #467 把 4 维一次性合并：

- `pkg/panicregister/Approved(reason string, value any) any` typed marker 新建（zero-dep，允许 `pkg/errcode` 反向 import 不成环）
- 全量 30+ `Must*` / 状态机不可达 panic 站点迁移至 `panic(panicregister.Approved("<kebab-reason>", errcode.Assertion(...)))`
- 4 处 C 类 re-throw 收编：`kernel/wrapper/lifecycle.go::recoverAndFinish` / `runtime/http/middleware/circuit_breaker.go::repanicAfterBreakerFailure` / `adapters/postgres/tx_manager.go::repanicAfterTopLevelTxRollback` / `repanicAfterSavepointRollback`（**精简至 4 处**，原计划描述的"6 处豁免"在落地时实际整合至 4 处具名函数）
- `tools/archtest/panic_invariants_test.go` 重写为 `PANIC-REGISTERED-01`：panic arg 必须 = `*ast.CallExpr` 且 Fun 经 `*types.Info` 解析到 `pkg/panicregister.Approved`，reason 必须 = `*ast.BasicLit` STRING；删除 `architecturalPanicWhitelist` map + `AllowMust` 字符串约定（Soft 形态彻底消除）
- 12 类 RED/GREEN fixture 覆盖（`tools/archtest/testdata/panic_registered_fixtures/`）
- `tools/codegen/contractgen` handler.tmpl 同步 emit `panic(panicregister.Approved("<contract-id>-<reason>", errcode.Assertion(...)))`，reason 由 `BuildContractSpec` 在 Go 侧预算 const literal，避免 archtest 误判
- 关闭 backlog：`PR419-FU-PANIC-MUST-PATH-SCOPE-01`（path-scope 窄化被 typed marker 替代）

**对应 ADR / 章程更新**：
- `docs/architecture/202604270030-architectural-panic-whitelist.md` 改写 §4 Approved reason catalog（取代函数名 whitelist 表）+ §5 No prefix-based exemption
- `.claude/rules/gocell/error-handling.md` "Panic taxonomy and Approved funnel" 章节
- `.claude/rules/gocell/ai-collab.md` "Hard 范本" 新增 "typed function call as Hard funnel for unbounded operations" 条目
- charter §4 Wave 2 panic-single-source 行已标 shipped

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
- **关闭 backlog**：`PR431-FU-BFS-EMITTER-RECEIVER-TYPE-IDENT-01` ✅（charter §3 "AI-rebust 评级" table 中 PR431-FU 行的 mandate 覆盖架构师 2026-05-10 "不升级" 决议）
- **AI-rebust 升级**：Soft（名字约定）→ Medium-偏-Hard（三重 type 谓词）。真 Hard 需 sealed interface emitter，超 PR 范围

### Phase 2.2：typeseval `ResolvePackageRef` + `ResolveMethodCall` helpers + dogfood（PR-TS2）✅ done
- **范围**：
  - `tools/archtest/internal/typeseval/call_target.go` 加 `ResolvePackageRef(typesInfo *types.Info, expr ast.Expr) (pkgPath, name string, ok bool)`（path A.2 qualified `pkg.Func` + path A.3 dot-imported bare `Func`，单源 info.Uses 配 partial type info 容错）
  - `tools/archtest/internal/typeseval/receiver_type.go` 加 `ResolveMethodCall(typesInfo *types.Info, sel *ast.SelectorExpr) (*types.Func, bool)`（path A' 方法调用，走 info.Selections.Obj() 单源，原生处理 promoted/named-type-def/generic-typeparam/alias 多形态）
  - dogfood：给 `scanner_framework_usage_test.go::forbiddenWalkRefs` 加 (2c) bare-Ident 分支（path A.3），把 (2b) SelectorExpr 分支的 path A.2 + path A' 全部迁到上述两个 helper；新增 promoted method + named-type def fixture 关 RED bypass（PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01 + path A' embedding gap 一并闭合）
- **不含**：bundle 其他子条（svctoken / role_admin 剩余 dot-import 迁移留 Phase 3.2 PR-SH1）；`ImplementsInterface` helper 不在本 PR 内（详见下方"helper 命名修订"）
- **工时**：实际 ~6h dev
- **证据**：PR #469

### helper 命名修订（plan v0 → v1 → v2 → v3）
plan 初稿 §2.2 写 `ImplementsInterface` 但 dogfood 写 call-matcher（"裸 Ident / dot-import 匹配"）—— 两者不是同一个 helper。修订后：
- **PR-TS2 实际做 call-matcher**：实施期再次修订为 `ResolvePackageRef(typesInfo, expr) (pkgPath, name string, ok bool)`（v2），不返回 `*types.Func`。原因：fixture 经常用 `importer.Default()`，对非 stdlib 包（如 `golang.org/x/tools/go/ast/inspector`）`info.Uses[sel.X]` 仍是 `*types.PkgName`，但 `info.Uses[sel.Sel]` 是 nil；strict `*types.Func` 形式会误判漏报。`(pkgPath, name)` tuple 形式对 partial type info 容错，并且更贴合 archtest matcher 的实际消费形态（svctoken / role_admin 也是 `pkgPath + symbolName` 配对）
- **v3 再修订**：path A' 不再走 sel.X 静态类型 walker（旧 `NamedTypeImportPath`，PR469 review-round-2 P1 证明 promoted/named-type-def case 漏报），改为 `ResolveMethodCall(info, sel) (*types.Func, bool)` 走 `info.Selections.Obj()` 路径，对齐 upstream `golang/tools` `go/types/typeutil.Callee` / `dominikh/go-tools` `analysis/code.IsCallTo`。`ResolveMethodCall` 与 Phase 2.1 计划的 `ResolveReceiverType(callExpr) (*types.Named, bool)` 各管一面：前者面向方法调用 → method `*types.Func`，后者面向 receiver value → named type；不冲突可共存
- **`ImplementsInterface` helper 不在 rollout plan 内**，移至 backlog 作为触发型 refactor（ID：`TYPESEVAL-IFACE-IMPL-HELPER-CONSOLIDATE-01`，触发：第 4 处 runtime guard 重复 / 新规则真值需要 interface impl 枚举）。当前 3 处重复（如 `rmq_invariants_test.go::implementsAMQPChannel`）属 refactor 收编，紧迫度低
- charter §5 L4 "interface impl 枚举"语义需求仍承认，但 helper 化驱动由真实场景触发，不预先立项

---

## 3. Wave 3 ARCHTEST-TYPEAWARE-HARDENING bundle 拆 PR

### backlog absorption（2026-05-12）

原 `ARCHTEST-TYPEAWARE-HARDENING-BUNDLE` 9 子条已 **100% 拆解吸收到本 plan**，bundle 主条目从 `docs/backlog.md` 删除（避免 mega-row 索引视角与 plan PR 视角双源不一致）。子条映射：

| 子条 | 状态 | 落点 |
|---|---|---|
| `TYPESEVAL-BUILDTAGS-COMMENTGROUP-COVERAGE-01` | ✅ PR #472 | §3 Phase 3.1 PR-BT1 |
| `TYPESEVAL-BUILDTAGS-SCOPE-FAILCLOSED-01` | ✅ PR #472 | §3 Phase 3.1 PR-BT1 |
| `TYPESEVAL-BUILDTAGS-LEGACY-DIRECTIVE-01` | ✅ PR #472 | §3 Phase 3.1 PR-BT1 |
| `PR445-FU-SCANNER-FRAMEWORK-HARDENING-01` | ✅ PR #474 | §3 Phase 3.2 PR-SH1 |
| `PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01` | ✅ PR #474（caller）+ PR-TS2（helper） | §3 Phase 3.2 PR-SH1 |
| `GENERATED-SKIP-CROSS-RULE-INVARIANT-01` | ✅ PR #471 | §3 Phase 3.3 PR-SH2 |
| `INTERNAL-CONTRACT-CLIENTS-SOURCE-GUARD-01` | ✅ PR #470 | §3 Phase 3.4 PR-IC1 |
| `PRODUCTION-LOADER-API-PRIVATE-HARD-UPGRADE-01` | 🟢 触发型 | §4 trigger A3 |
| `PR460-FU-FINDFIRSTCHILD-TYPED-API-01` | 🟡 触发型 | §4 trigger（FINDFIRSTCHILD-TYPED-API-01 行）|

按维度拆 PR：

### Phase 3.1：build-tag fail-closed（PR-BT1）✅ done
- **范围**：3 子条同主题（typeseval build-tag scope）
  - `TYPESEVAL-BUILDTAGS-COMMENTGROUP-COVERAGE-01`：bufio.Scanner → `go/parser.ParseFile + ParseComments`
  - `TYPESEVAL-BUILDTAGS-SCOPE-FAILCLOSED-01`：`FlatNonDefaultTags` 对 `!tag`/OR/互斥 tag-set 改 tag-set evaluator + per-rule helper
  - `TYPESEVAL-BUILDTAGS-LEGACY-DIRECTIVE-01`：扩 coverage 自检覆盖 `// +build` legacy directive
- **证据**：PR #472

### Phase 3.2：scanner / call-matcher hardening（PR-SH1）✅ done
- **证据**：PR #474 `refactor(archtest): inspector method ban + svctoken/role_admin path A.3 closure (PR-SH1)`
- **范围**：2 子条（inspector method ban + svctoken/role_admin path A.3 closure）
  - `PR445-FU-SCANNER-FRAMEWORK-HARDENING-01` partial — inspector 部分：
    - `forbiddenWalkSymbols[inspector]` 从 6 项缩为 `{New, All}`（top-level 真集，反映 inspector 包实际导出形态）
    - `forbiddenMethodSymbols[inspector]` 新增 `{Preorder, Nodes, WithStack, PreorderSeq}`（path A' via `typeseval.ResolveMethodCall` → `info.Selections.Obj()`）
    - 新增 `tools/archtest/internal/inspectorredfixture/` 真实 package + `TestScannerFrameworkUsage01_InspectorMethodBanLive` 经 `typeseval.SharedResolver` end-to-end 锁定 4 method coverage（Hard：删除 inspector 条目 → 测试红）
    - scanner 内部 subpackage allowlist 已由 file-Dir 精确匹配（scanner_framework_usage_test.go:118 `Dir(rel) != "tools/archtest"`）实现，本 PR 不涉及
  - `PR445-FU-TYPEAWARE-CALL-MATCHER-IDENT-01` caller 迁移残余（`docs/backlog/cap-14-tooling.md`）：
    - `svctoken_caller_cell_test.go`：删除 `isAuthFuncCall` helper，inline `typeseval.ResolvePackageRef(call.Fun)` 统一覆盖 A.2 + A.3；同时为 `runtime/auth/` 包内调用加 exempt（迁移暴露 `servicetoken_test.go` 内部负向 test，rule 语义是 cross-package caller 校验，所以 owning package 自调用豁免）
    - `role_admin_literal_test.go`：替换 `call.Fun.(*ast.SelectorExpr) + info.Uses → *types.PkgName` 内联逻辑为 `typeseval.ResolvePackageRef`
    - 测试覆盖：helper 层 `TestResolvePackageRef_DotImportBareIdent` (call_target_test.go:85) 已锁 path A.3 契约；caller 迁移为机械委托
- **从原 PR-SH1 scope 移除（已 ship 项）**：
  - `PR430-FU-MIGRATION-DRIFT-CURRENT-FIXES-01` ✅ closed by PR #440 (PR-Δ1)
  - `PR430-FU-SCANNER-SYMLINK-FAIL-CLOSED-01` ✅ closed by PR #440
  - `PR430-FU-SCANNER-INTERNAL-CONSOLIDATE-01` ✅ closed by PR #440
  （三条 backlog 状态已对齐，rollout plan §3.2 滞后；本次同步修正）
- **工时**：3-5h dev / 1-2h review（原 12-16h 估值含已 ship 项）
- **依赖**：Phase 2.2 PR-TS2 merge ✅

### Phase 3.3：generated-skip cross-rule invariant（PR-SH2）✅ done
- **范围**：1 子条独立
  - `GENERATED-SKIP-CROSS-RULE-INVARIANT-01`：archtest cross-test invariant 扫 `SharedResolver/LoadPackages + ./... + pkg.Syntax/pkgFileRel` 组合，要求显式 `typeseval.IsGeneratedRelPath` 或 allowlist
- **证据**：PR #471

### Phase 3.4：INTERNAL-CONTRACT-CLIENTS 上移 governance（PR-IC1）✅ done
- **范围**：1 子条独立
  - `INTERNAL-CONTRACT-CLIENTS-SOURCE-GUARD-01`：把 `/internal/v1/*` 必须声明 caller clients 从手写 `wrapper.ContractSpec{}` AST 扫描上移到 contract YAML/governance（charter §5.1 决策树：L5 → L0/L6 载体纠偏）
- **证据**：PR #470（FMT-31）

### Phase 3.5：typeseval eval predicate centralization（PR-EP1）✅ done
- **证据**：PR #475 `feat(archtest): TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01 Hard funnel (Phase 3.5 PR-EP1)`
- **范围**：1 子条独立（PR #472 follow-up，trigger 已满足）
  - `TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01`（`docs/backlog/cap-02-metadata-governance.md`）：新增 `tools/archtest/eval_predicate_centralization_test.go`，AST walk `tools/archtest/` 包下所有 `constraint.Expr.Eval(...)` callsite，断言 predicate argument ∈ {`typeseval.BuildContextPredicate(...)`, 全 false sentinel `func(_ string) bool { return false }`}；其他形式 fail-closed
  - **AI-rebust 升级**：near-Hard → Hard（违反不可表达，未来手写含过期 tag map 的 predicate 在 archtest 时报错）
- **工时**：3-5h dev / 1h review
- **依赖**：Phase 3.1 PR-BT1 merge ✅

### Phase 3.6：archtest-inventory drift CI gate（PR-IG1）❌ cancelled by PR #435 (PR-A')
- **取消理由**：原任务前提已不成立。PR #435 (PR-A', 2026-05-10) 已将 `hack/verify-archtest-inventory.sh` + `docs/audit/archtest-inventory.md` 派生产物整体删除，改由 `tools/archtest/inventory_anchor_required_test.go`（`INVENTORY-ANCHOR-REQUIRED-01` + `INVENTORY-ANCHOR-VALID-ID-01`）单源接替，并已通过 `.github/workflows/_build-lint.yml::verify-archtest` 16-shard 矩阵在 CI 强制
- **enforcement gap 分析**：新机制严格强于旧机制——(1) 锚点本身即 ground truth，无派生 inventory 文件 → drift surface 从根本上消除；(2) VALID-ID-01 额外校验锚点 ID 规范 grammar（旧 gate 无此能力）；(3) `ARCHTEST-VERIFY-COVERAGE-01` 守卫 16-shard discovery 与 `tools/archtest/*_test.go` AST 集合一致，新文件自动入 shard
- **backlog 同步**：`PR419-FU-INVENTORY-CI-GATE-01`（`docs/backlog/cap-14-tooling.md`）已标 ✅ closed by PR #435（2026-05-12 直接 in-place 标记）

### Phase 3.7：archtest 扫描 scope 扩展（PR-SC1）✅ done
- **证据**：PR #473 `refactor(contractspec): typed framework funnel + archtest Hard upgrade to runtime/ (Phase 3.7 PR-SC1)`
- **范围**：1 子条（C9 已 moot，同 PR 关闭）
  - `ARCHTEST-CONTRACTSPEC-LITERAL-RUNTIME`（`docs/backlog/cap-14-tooling.md`，P1/Cx1）：`NO-MANUAL-CONTRACTSPEC-LITERAL-01` 扫描根从 `cells/` + `examples/` 扩到 `runtime/`，**并通过 typed funnel 升级为 Hard** —— 新增 `kernel/contractspec.NewFrameworkHTTP` + `NewEventDerivation` 两个 typed builder，5 处 runtime/ 字面量全量迁移；composite literal 在 cells/ + examples/ + runtime/ 0 escape hatch，违反不可表达
  - ~~`PR245-F6 OUTBOX-ARCHTEST-SCAN-SCOPE-EXPAND-01`~~ — **MOOT**: target `tools/archtest/outbox_cell_test.go::isCellFile` 已在 PR-560 删除（ADR 202605101900 §D7）；替代规则 `CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` 已实现请求的 scope。backlog line 336 同 PR 标 ✅
- **AI-rebust**：Soft（runtime/ "framework infra" 部落知识）→ **Hard**（typed funnel 是唯一合法构造路径，archtest 形态唯一性，对齐章程 `typed function call as Hard funnel` 范本）
- **工时**：5-8h dev / 1-2h review（Hard 升级 + dead code 清理）
- **依赖**：无

### Phase 3.8：metadata → 派生消费方字段集覆盖守卫束（PR-MD1）✅ done
- **范围**：2 子条同主题合并（同主题"元数据 ↔ 派生 DTO 字段级漂移"）
  - `ARCHTEST-CELL-METADATA-FIELD-DRIFT`（`docs/backlog/cap-14-tooling.md`，P1/Cx2）：原 backlog 担心的"字段级漂移"实际已被 K#04 verify-codegen-cell.sh 守 Hard；真实 gap 在上一层 cellgen pipeline 字段集覆盖。PR-MD1 走 reflect-driven Go literal printer（Absolute Hard）：删 `cellgen.CellMetadataLiteral` 手写 struct + `buildMetadataLiteral` 手写 reduce；新增 `cellgen.RenderCellMetaLiteral` 用 reflect 遍历 `metadata.CellMeta` 自动渲染 Go 字面量。3 个 cell_gen.go 重生 0 diff，K#04 verify GREEN。
  - `CATALOG-DTO-DRIFT-ARCHTEST`（`docs/backlog/cap-14-tooling.md`，P2/Cx2）：原 `runtime/devtools/catalog/assembly_field_coverage_test.go` 等价实现已存在但缺治理目录注册。迁入 `tools/archtest/assembly_meta_dto_drift_test.go` 注册 INVARIANT `ASSEMBLY-META-DTO-COVERAGE-01`，删除 catalog 包内 4 个迁出符号（单源治理）。Medium-偏-Hard（reflect + excludelist）。
- **工时**：实际 ~5h dev（reflect renderer ~3h + 任务二迁移 ~1h + 文档/inventory ~1h）/ 1-2h review
- **依赖**：无
- **证据**：PR-MD1 (feat/004-cellgen-reflect-literal-printer)
- **Follow-up 立即开 next-up**：`CELLGEN-LITERAL-FUNNEL-02`（P1/Cx1，🟠，type-system 级 Hard funnel guard，关闭"AI 改回手写 cell.tmpl 字段枚举"漏洞窗口，工时 1.5-2h）
- **Follow-up 触发型**：`CATALOG-DTO-CODEGEN-DERIVE-01`（P3/Cx3，🟢，catalog 5 DTO codegen 派生 Hard 升级，工时 15-25h，触发条件：第 2 次 metadata 字段未同步 / DTO ≥ 6 / wire 漂移事故）

### Phase 3.9：PR450 治理升级束（PR-S7）✅ done
- **证据**：PR-S7 (refactor/572-pr-s7-archtest-bundle)
- **范围**：5 子条 bundle（cap-14 line 403，已是 bundle）
  - ✅ `K-01` + `A-10`：`audit_ledger_composition_root_test.go` 升级为 type-aware `typeseval.LoadProductionPackages` + `ResolvePackageRef`，canonical import path 识别（alias-immune）。新增 `tools/archtest/internal/auditledgerfixture/aliased.go` build-tag fixture + `TestAuditLedgerProtocol_ScannerCatchesAliasBypass` 锁定检测能力。**AI-rebust 升级**：Soft（`pkg.Name` 字符串）→ Medium-true（canonical import path identity）；Hard 不可达——cells 必须 import ledger 消费 typed `*Protocol`，文件 godoc 诚实标注
  - ✅ `K-04`：prefix allowlist 改为枚举 `{runtime/audit/ledger, runtime/audit/ledger/storetest}`（同 PR 落入）
  - ✅ `K-05`：新增 `tools/archtest/migration_pair_deploy_test.go`（`MIGRATION-PAIR-DEPLOY-01`），扫 `-- pair-deploy: <stem>` 单向 directive + 校验被引用 migration 存在；canary hard-assert 021↔020 pair 防止 anchor 静默删除导致规则退化。`migrations/021_audit_entries_event_id_unique.sql` 已落入 anchor。**用户决策**：单向 anchor（拒绝双向 reciprocity——pair-deploy 是文档型 + filename-exists 静态校验，与 release manifest 解耦）
  - ✅ `F-12/K-02`：**范围扩大到 cell 子树全部 11 处 `WithTxManager`**（按"不留小尾巴"反馈，超出原 backlog 仅 auditcore appender 的窄 scope；最后一处 `examples/todoorder/ordercreate` 由 archtest scope 扩展时自动扫出，进一步验证 scope 扩展的防御能力）。11 处形参与 `txRunner` 字段类型 `persistence.TxRunner` → `persistence.CellTxManager`：auditcore (appender) + configcore (configpublish/configwrite/flagwrite) + accesscore (identitymanage/rbacassign/sessionlogin/sessionlogout/sessionrefresh/setup) + examples/todoorder (ordercreate)；测试调用点 ~85 处 `WithTxManager(x)` → `WithTxManager(persistence.WrapForCell(x))`。**同时 `CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` archtest scope 从 cell-package root 扩到 cell 整棵子树**（`isCellPackageRootFile` → `isCellSubtreeFile`，封死未来 AI 在 slice/internal 退化 sealing 的窗口）；ADR `docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md` Amendment 2026-05-12 记录边界扩展决策。outbox 维度 grep 验证 cell 子树 0 处 raw `outbox.Publisher`/`Writer`，scope 扩展自动覆盖该维度。**注**：原 backlog 中提到的 `auditverify.WithTxManager` 已在 Wave 2 Batch D 删除，scope 项不存在
- **工时**：实际 ~10h dev / 待 review（含 PR review 后追加的 ADR + archtest scope 扩展闭环修复）
- **依赖**：无

### Phase 3.10：cellgen literal funnel type-system Hard guard（PR-MD1 follow-up）
> **执行顺序**：§6 next-up Rank 1（**先于 Phase 3.9**），数字编号承 3.9 之后仅为索引连续。

- **目标**：把 PR-MD1 的 L1（reflect renderer）+ L2（K#04 重生 diff）两层防线升级为 **type-system 级 Hard funnel guard**，关闭"AI 同 PR 改回手写 cell.tmpl 字段枚举 + 不加 CellMeta 字段"造成的 silent drift 漏洞窗口。

- **当前漏洞模型**（PR-MD1 留下的窗口）：
  - **L1** = `cellgen.RenderCellMetaLiteral`（reflect 遍历 `metadata.CellMeta` 自动渲染 Go 字面量）
  - **L2** = `hack/verify-codegen-cell.sh`（worktree 沙箱重生 + diff，K#04 Hard 门）
  - **gap**：L1 + L2 都依赖 "`cell.tmpl` 调用 `renderCellMetaLiteral` template func"。AI 同 PR 把 `cell.tmpl` 改回手写 `{{ .MetadataLiteral.ID }}...` + **不**给 `CellMeta` 加新字段 → K#04 重生 0 diff（稳定 PASS）→ 下次 `CellMeta` 真正加字段时 silent drift（cell_gen.go 静默漏字段）

- **修复方案**（funnel API 层去除"手写枚举"的数据源）：
  1. `tools/codegen/cellgen/spec.go::CellGenSpec`：
     - **删除** `MetadataLiteral *metadata.CellMeta` 字段
     - **新增** `RenderedMetaLiteral string` 字段（pre-rendered Go literal 字符串）
  2. `tools/codegen/cellgen/builder.go::BuildCellSpec`：
     - 调用 `RenderCellMetaLiteral(cell)` 一次，将结果填入 `spec.RenderedMetaLiteral`
  3. `tools/codegen/cellgen/templates/cell.tmpl`：
     - 改为 `var cellMeta = {{ .RenderedMetaLiteral }}`
     - **删除** `renderCellMetaLiteral` template func（template 上下文不再有数据源可调）

- **Hard 来源**（type system 最高档，对齐 charter §1 "violation not expressible"）：
  - `cell.tmpl` 拿不到 `*metadata.CellMeta` 实例 —— `CellGenSpec` 字段集已不暴露此对象
  - 手写字段枚举（如 `{{ .MetadataLiteral.ID }}`）在 template 执行期**没有数据源可访问** → 编译/渲染期 fail
  - AI 想绕过必须**显式修改 `CellGenSpec` Go API**（重新暴露 `MetadataLiteral` 字段或加 helper）—— 高显式度变更，必触发 review，无法静默漂移

- **AI-rebust 升级**：PR-MD1 的 Medium-偏-Hard（reflect renderer + K#04 重生 diff）→ **Absolute Hard**（type-system funnel API，违反不可表达 + archtest 形态唯一性）

- **工时**：1.5-2h dev / ~0.5h review
- **依赖**：Phase 3.8 PR-MD1 ship ✅
- **触发**：**next-up**（不等触发条件；silent-drift 漏洞窗口越早封越好，且工时极小不阻塞 Phase 3.9）
- **范围限制**：本 PR 仅做 CellGenSpec API 收窄 + cell.tmpl 改造 + 重生 0-diff 验证；**不**做 catalog DTO 派生（那是触发型 `CATALOG-DTO-CODEGEN-DERIVE-01`，工时 15-25h，不在本 PR scope）
- **验证**：(1) `cellgen.CellGenSpec` 删字段后 grep `.MetadataLiteral` 在 cell.tmpl 0 命中；(2) 3 个 cell_gen.go 重生 0 diff；(3) K#04 verify GREEN；(4) 单测覆盖 `RenderedMetaLiteral` 字段被 BuildCellSpec 正确填充

---

## 4. Wave 4 触发型——保留 + 落地时按维度拆模板

### 触发条件（charter §4 line 113-122 + 2026-05-12 backlog 同步）
| 项 | trigger | backlog 锚点 |
|---|---|---|
| HANDLER-POLICY-TYPEAWARE-SCANNER-01 | scanner 误报/漏报触发 | charter §4 |
| SERVICEOWNED-OWNERSHIP-GUARD-01 | `auth.serviceOwned` endpoint > 1 | charter §4 |
| B-FLOOR-FOLLOWUP §2.5/§4 | contract.yaml status ↔ adapter typed return 漂移事故首现 | charter §4 |
| AUTH-COMBO-ARCHTEST-DOUBLE-DEFENSE | `hasFMT27AuthModeConflict` 被重新 inline 化 | charter §4 |
| TEST-POLLING-DETERMINISM typed marker | 第二次 race CI flake / 进入下一治理批 / 339 站点新增违反 | charter §4 |
| FINDFIRSTCHILD-TYPED-API-01 | 第 7 处 closure+done sentinel helper 出现 | charter §4 + `docs/backlog/cap-14-tooling.md` PR460-FU |

### 保留触发型条目（trigger 是真事故/方案待定/量级未到，A + C 类筛选后 6 条）
| 项 | trigger | backlog 锚点 |
|---|---|---|
| **PR-TS1-FU-VALIDATIONRESULT-EMITTER-SEALED-MARKER-01**（A2）| (a) 同包内新增非-`*locator` emitter receiver 出现真 false-positive / (b) 任何 archtest 规则需要 sealed marker 范本时顺带建立 | `docs/backlog/cap-02-metadata-governance.md` |
| **PRODUCTION-LOADER-API-PRIVATE-HARD-UPGRADE-01**（A3）| 首次出现 cross-function file-scope `var pat = "./..."` 间接调用 escape，或新 archtest 规则需要绕过 funnel；unexport `typeseval.SharedResolver/LoadPackages` 为包内私有 + `LoadPackagesForFixtures` 显式入口 | `docs/backlog/cap-14-tooling.md` |
| **CELLGEN-ERRCODE-FUNNEL-HARDEN**（C2）| depguard method-level rule 方案确定 OR typed wrapper 抽出；cellgen 包 errcode Hard 升级路径 | `docs/backlog/cap-14-tooling.md` |
| **ARCHTEST-CARVEOUT-NARROW-FUNCLEVEL** + **B2-K-08-CARVEOUT-NARROW** 合并条目（C3）| ✅ 提前推进 → 037 PR-A2 (581-archtest-carveout-narrow)；function-level carve-out + ADR registry + ERRCODE-CARVEOUT-ADR-CONSISTENCY-01 Hard 守卫 | `docs/backlog/cap-14-tooling.md` + line 361 |
| **PR408-FU-GOVERNANCE-OWNER-AST-EXTRACTION-01**（C6）| 第二次主题归属错误；`list-archtests.sh` grep → go/ast 解析按 `Rule{ID:...}` struct literal 或 `const ruleID = "..."` 定位 canonical owner + inventory 加 `referenced_by` 列 | `docs/backlog/cap-02-metadata-governance.md` |
| **POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01**（C8）| 第 2 次同类漂移；archtest 静态扫 `*_test.go`，`_NotFound` 后缀测试必须断言 typed `errcode.Error.Code` 等于 `Err*NotFound`，禁裸 `assert.AnError`（违反不可表达 → Hard）| `docs/backlog/cap-14-tooling.md` |

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

## 5. 实施依赖图与并行度（更新后剩余）

```
Wave 4 触发型                        触发后 按 Template-Wave4-3PR
```

---

## 6. 优先级 next-up

| Rank | 项 | 工时 | 并行能力 |
|---|---|---|---|
| 1 | **`CELLGEN-LITERAL-FUNNEL-02`**（PR-MD1 follow-up，type-system Hard funnel guard，关闭"AI 改回手写 cell.tmpl 字段枚举"漏洞窗口）| 1.5-2h dev / ~0.5h review | ✅ 完全并行（仅改 cellgen spec.go/builder.go/cell.tmpl）|

**总剩余工时（Wave 2/3 范围）**：**1.5-2h dev / 0.5h review**

### 排期建议

- **Now**：`CELLGEN-LITERAL-FUNNEL-02` 立刻 next-up（PR-MD1 留下的 silent-drift 漏洞窗口越早封越好，1.5-2h 工时不阻塞其他工作）
- **Wave 4 触发型**：保留 6 条触发型 + 7 条 charter Wave 4 触发清单，触发时按 Template-Wave4-3PR 拆分

---

## 7. 引用

- 章程（原则视角）：`docs/plans/202605101300-ai-first-governance-charter.md`
- PR #445 复盘（scope 失控元教训）：本对话 §"PR #445 为什么遗留这么多问题" 5 层根因分析
- backlog（条目索引）：`docs/backlog.md`（line 47/48/82/317/404-410/418/419/461 为本计划涉及条目）
- AI 协作章程：`.claude/rules/gocell/ai-collab.md`
- 5-PR 主线 roadmap：`docs/plans/202605070431-pr403-funnel-fix-roadmap.md`
