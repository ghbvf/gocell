# PR #403 修复路线图（funnel 化优先 / 实施视角）

**对接**：`docs/reviews/202605070153-pr403-third-wave-review.md` §4 修复方案
**关系**：替代原报告 §4 路线，§1-§3 症状/根因诊断保留在 review
**生成日期**：2026-05-07
**最后更新**：2026-05-10（彻底版重写：5-PR plan PR-Φ/A'/B/C/D'，吸收 Path C 关系；删 PR-G frozen allowlist + ratchet 渐进锁；删 OWNER-AST-EXTRACTION 子项；panic 守卫改就地注释单源；扩 framework 节点 API 收编 65 处手写 for-loop + 131 处裸 ast.Inspect）

---

## 1. 当前状态（对齐时点 2026-05-10）

### 已 ship

段 1（typed envelope 闭环 + ADR D6/D7）/ 段 3'（CLAUDE.md funnel-first 原则）/ PR-FUNNEL-01（archtest 文件 104→70）/ PR-FUNNEL-02（handler invariants funnel）/ PR-FUNNEL-03（governance rules 15→8）/ Batch 0 SCANNER-FRAMEWORK（PR #419 framework 文件遍历层）全部 ✅。

### 实测基线（重对齐 vs roadmap 历史数据，2026-05-10 c21ed4de 更新）

| 维度 | 旧 roadmap 估值 | 实测 | PR-A'/Path C 后 |
|---|---|---|---|
| 顶层 archtest `_test.go` | 72 | 73 + 17（internal/scanner/）| 同 |
| INVARIANT 锚点（Go 源） | 89 | 89 | **全 55 文件回填，0 缺锚点**（PR #435）|
| 缺锚点 `_test.go` | 39 / 46 | 46 | **0**（PR #435 INVENTORY-ANCHOR-REQUIRED-01 强制）|
| 旧 scanner 待迁移（filepath.WalkDir/Walk）| "70+" | 25 | **0**（Path C #430 一锅迁完）|
| 手写 AST `for ... Decls/List/Specs` | 未提 | 65 处跨 29 文件 | 同（PR-Φ 待启动）|
| `ast.Inspect` 已用 | 未提 | 131 处跨 43 文件 | 同（PR-Φ 待启动）|
| 两种混用文件 | 未提 | 22 个 | 同（PR-Φ 待启动）|
| `archtest-inventory.md` 持久产物 | 存在 | 存在 | **删除**（PR #435 单源接替）|
| `verify-archtest-inventory.sh` drift gate | 存在 | 存在 | **删除**（PR #435 archtest 单源接替）|

### 与 Path C（PR #424 跟进）的边界

PR #424 已 ship `SCANNER-FRAMEWORK-USAGE-01`（拦 `path/filepath.WalkDir/Walk`）+ 一锅迁移 24 文件 / 39 walk site；后续追加的 `SCANNER-FRAMEWORK-USAGE-02`（substring + 自由 category 锚点）经 review 暴露 6 条反模式漏洞，Path C 单独处理：USAGE-01 升级 traversal symbol table（path/filepath/os/io/fs 全 traversal）+ scanner 加 EachContentFile + MatchRels + IncludeTestdata + 19 bypass 站点全部迁 framework + USAGE-02 整规则删除。**Path C 由他人独立推进，本路线图不重叠**。

### 剩余主线（更新于 2026-05-10 c21ed4de）

5 主线进展：✅ Path C (#430) / ✅ PR-A' (#435) / ✅ PR-B (#431) / ✅ PR-C (#432)；剩余 **2 条未启动**：
- **PR-Φ**（节点遍历漏斗）：framework typed callback per node kind + 一锅替换 65 手写 for-loop + 131 裸 ast.Inspect；可立即启动，14-18h dev / 5-7h review
- **PR-D'**（panic 守卫就地注释单源）：可立即启动，5-7h dev / 2h review；与 PR-Φ 在 panic_invariants_test.go 函数体冲突 → 必须等 PR-Φ merge 后启动

Batch 3 触发型项不变（HANDLER-POLICY-TYPEAWARE-SCANNER / SERVICEOWNED-OWNERSHIP-GUARD / B-FLOOR-FOLLOWUP §2.5+§4 + 5 条 PR430/431/432-FU 触发型）。

---

## 2. 决策与不做的事（最终路线立场）

- **不建 Registry / 中心化注册表**：K8s / CockroachDB / Linux / Rust / Go 工具链均无 prior art；多份文档用 `// INVARIANT: <ID>` grep 锚点串联即可。
- **不立 PR template / four-piece kit ADR**：项目大规模重构期填字段会变应付式；funnel-first 原则 < 10 行已写入 CLAUDE.md。
- **不立 frozen allowlist + ratchet 渐进锁**（**新增 2026-05-10**）：73 archtest 文件 / 单仓库 / 单团队规模能一锅迁完；frozen allowlist + ratchet 是为"无法一锅迁完"设计的 scaffold（TS strict mode rollout / Linux sparse rollout 都是百万行规模），GoCell 不需要。原 roadmap PR-G 思路守在"是否用 framework"这层不彻底（framework 内调用方仍可手写 for-loop），下一轮必触发 follow-up——这就是用户原话"做一点出新问题"的根源。
- **不要双源对账**（**新增 2026-05-10**）：panic_invariants_test.go 当前 architecturalPanicWhitelist Go map + ADR markdown 表格 + assertPanicWhitelistMatchesADR reconciliation 是双源症状。改就地注释单源（对齐 CockroachDB `forbiddenmethod` nolint 模式）。
- **接受 funnel 不到的物理残留**：Go 缺 sealed package / newtype / const string 区分，~50% 约束物理上 funnel 不掉（buffer-then-commit 顺序、message const literal、panic 白名单、readyz probe 命名等）；CockroachDB 同款语言天花板，残留 ~30 条平铺管理。但**应优先把能 funnel 的全 funnel 进去**，残留只接受真正物理不可表达的。
- **archtest 数量基线**：CockroachDB ~30 是参照值，GoCell 规模可压更低；具体数值不立硬指标，按 PR-FUNNEL-NN 自然降。
- **PR 切片纪律**：单 PR 范围控制在 Cx2-Cx3 / 单一概念边界；不为"同根因"叙事打包合并 PR；但**单一概念边界内允许超大 PR**（PR-Φ 一锅 65 + 131 处迁移），换"无 follow-up"。
- **OWNER-AST-EXTRACTION 不做**（**新增 2026-05-10**）：原 PR-A 计划 list-archtests.sh AST owner 提取 + inventory `referenced_by` 列。bash + AST 是反模式（要做就用 Go 写新 CLI，引入新工具新维护边界 ROI 不值）；reverse index 由 reviewer 用 `grep ruleFMT20 kernel/governance/` 现场解决，不立持久产物。

详细论证（K8s/CockroachDB/Linux/Rust/Go 工具链对照、Registry 无 prior art、Go 语言天花板、彻底版反 ratchet 论证）见配套 plan `~/.claude-ming/plans/ast-ast-inspect-inherited-wadler.md`（含开源对标 K8s/CockroachDB/staticcheck/golang.org/x/tools 报告）。

---

## 3. 任务表（唯一事实表）

| ID | Batch | Status | 工时 dev+review | 说明 |
|---|---|---|---|---|
| 段 1 typed envelope 闭环 | — | ✅ | — | PR #403（71be4d6e）；ADR D6/D7 |
| 段 3' CLAUDE.md funnel-first | — | ✅ | — | `CLAUDE.md` `## 新增 invariant 决策原则` |
| PR-FUNNEL-01 主题聚并 | — | ✅ | — | PR #408（5461d53e）；archtest 104→70 |
| PR-FUNNEL-01 follow-up | — | ✅ | — | PR #412（16a13993）；parse-error fail-loud + git ls-files |
| PR-FUNNEL-02 handler funnel | — | ✅ | — | PR #411（18b60a5c）；HANDLER-POLICY-REQUIRED-01 升 funnel |
| Audit step A 清单化 | — | ✅ shipped (彻底版 by PR-A') | — | `list-archtests.sh` 改 stdout-only；`inventory.md` + `verify-archtest-inventory.sh` 漂移闸已被 PR-A' 删除（INVENTORY-ANCHOR-REQUIRED-01 archtest 单源接替） |
| `PR408-FU-SCANNER-SHARED-FRAMEWORK-01` | 0 | ✅ | — | PR #419（996784cf）；framework **文件遍历层** + 4 demo 迁移 |
| `PR-FUNNEL-03` governance 聚并 | 1 | ✅ | — | PR #418（e8cdf3c9）；source 15→8 文件 + `rule_inventory_test.go` golden 锁 81 条 |
| **Path C** scanner 文件扫描漏斗收编 | 0' | ✅ PR #430 (2026-05-10) | — | PR #424 衍生：USAGE-01 traversal symbol table 升级 + scanner 加 EachContentFile / MatchRels / IncludeTestdata + 19 bypass 站点全 framework + USAGE-02 删除 |
| **PR-Φ** `SCANNER-FRAMEWORK-NODE-API-COMPLETE` | 1 | ⏳ Path C ship 后 | 14-18h+5-7h | framework **节点遍历层** API（EachFuncDecl / EachCallExpr / EachImportSpec / EachGenDecl）+ USAGE-02 archtest（**复用 Path C 删空的 ID**，无条件，与 USAGE-01 同样原子）+ 一锅替换 65 手写 for-loop + 131 裸 ast.Inspect → 全 typed callback；保留出口走严格 category 就地注释 |
| **PR-A'** `INVENTORY-ANCHOR-SINGLE-SOURCE`（彻底版）| 1 | ✅ shipped | — | 55 个 `tools/archtest/*_test.go` 加 `// INVARIANT:` 锚点 + INVENTORY-ANCHOR-REQUIRED-01 archtest 单源 + 删 `docs/audit/archtest-inventory.md` 持久产物 + 删 `hack/verify-archtest-inventory.sh` drift gate（make verify glob discovery 自动出 CI）+ 删 `kernel/governance/rule_inventory_test.go::TestArchtestInventoryNoIDTruncation`（底层 .md 没了，truncation 场景消失）+ `list-archtests.sh` 改 stdout-only（删 fallback / hardcode 黑名单 / governance section）+ doc.go / backlog.md / 033-pg-plan 同步。**OWNER-AST-EXTRACTION 砍掉** |
| **PR-B** `GOVERNANCE-RULE-REACHABILITY-TEST-01` | 1 | ✅ PR #431 (2026-05-10) | — | BFS 从 4 注册根扩闭包；emitter 用 convention（newResult/newScopedResult）+ 防漂移 `assertEmitterMethodsRestrictedToLocator`；reviewer P2 finding（按名字非按 receiver type）→ architect 决策不升级，trigger 登记 backlog `PR431-FU-BFS-EMITTER-RECEIVER-TYPE-IDENT-01` |
| **PR-C** `AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01` | 1 | ✅ PR #432 (2026-05-10) | — | single oracle (`metadata.AuthComboLegal`) + 5 层 mirror（type system / reflect 字段数 / governance 委托 / schema if-then mirror / 双侧 32-combo matrix）；reviewer finding #1（archtest 双重防线）→ architect 决策不立，trigger 登记 backlog `PR432-FU-AUTH-COMBO-ARCHTEST-DOUBLE-DEFENSE-01` |
| **PR-D'** `PANIC-WHITELIST-INLINE-COMMENT-SINGLE-SOURCE-01` | 1 | ⏳ | 5-7h+2h | 删 architecturalPanicWhitelist Go map + 删 AllowMust 全局豁免 + 删 assertPanicWhitelistMatchesADR reconciliation → 改就地注释 `// PANIC-REGISTERED-01: ADR-approved: <reason>`；4 处 re-throw + 30+ Must* 站点逐个加注释；ADR markdown 改写为元规则文档（删函数名清单表格）。ref: cockroachdb/cockroach pkg/testutils/lint/passes/forbiddenmethod |
| **PR-F** `PR-FUNNEL-04` 候选评估 | 2 | ⏳ | 2h | 单 worktree 扫 73 archtest 找可 type-system 化（typed `XxxResponseObject` 替代 `(*Response, error)`）/ 冗余 / 重复，发现 ≥3 条候选才启动小 PR 系列；否则保留为长期残留 |
| ~~`PR-G` SCANNER-USAGE-01-GATE~~ | — | ❌ 取消 | — | 被 Path C（USAGE-01 升级）+ PR-Φ（USAGE-02 节点层）共同吸收。frozen allowlist + ratchet 渐进锁反模式不立 |
| ~~PR-E1..En 渐进迁移~~ | — | ❌ 取消 | — | filepath.Walk 文件迁移由 Path C 一锅做完；ast 节点迁移由 PR-Φ 一锅做完；不立"按文件域分小 PR + ratchet 缩 allowlist"链路 |
| ~~`PR408-FU-LEGACY-ANCHOR-BACKFILL-01`~~ | — | 合入 PR-A' | — | — |
| ~~`PR408-FU-GOVERNANCE-OWNER-AST-EXTRACTION-01`~~ | — | ❌ 取消（决策见 §2）| — | — |
| ~~`PR411-AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01`~~ | — | 改名 PR-C | — | — |
| ~~`PR419-FU-INVENTORY-CI-GATE-01`~~ | — | ❌ 取消（gate 整体删除 by PR-A'） | — | — |
| ~~`PR419-FU-PANIC-MUST-PATH-SCOPE-01`~~ | — | ❌ 升级为 PR-D'（path scope 是双源反模式，改就地注释单源）| — | — |
| ~~`PR408-FU-SCANNER-USAGE-01-ENABLEMENT`~~ | — | 由 Path C 实现 | — | — |
| `PR411-HANDLER-POLICY-TYPEAWARE-SCANNER-01` | 3 | 触发 | — | trigger: scanner 误报/漏报；基于 framework 做（直接用 internal/scanner API） |
| `PR411-SERVICEOWNED-OWNERSHIP-GUARD-01` | 3 | 触发 | — | trigger: `auth.serviceOwned` endpoint > 1 / auth ownership 模型硬化批次 |
| `B-FLOOR-FOLLOWUP` §2.5 Success-Floor | 3 | 触发 | — | trigger: contract.yaml status 声明 ⇔ adapter typed return 漂移事故首现 / cells 数量增长到 Floor 升级 ROI > 16h dev |
| `B-FLOOR-FOLLOWUP` §4 Full-Floor | 3 | 触发 | — | trigger: §2.5 Success-Floor 已 ship 且稳定 |

---

## 4. 实施计划（Path C 后 5-PR 并行 + 触发型）

### 4.1 PR-Φ 详细设计（节点遍历漏斗）

**Path C 边界**：Path C ship 后 `tools/archtest/scanner_framework_usage_test.go` 含 USAGE-01 traversal symbol table（path/filepath + os + io/fs 全 traversal symbol，无条件硬约束）+ scanner 已扩 EachContentFile / MatchRels / IncludeTestdata。**Path C 已删除原 bf37fa8 的 USAGE-02 substring 反模式**，编号空出。PR-Φ 在此基础上**复用 USAGE-02 命名**（不跳号到 USAGE-03 留墓碑）守节点遍历层，语义上承接为"第二条 SCANNER-FRAMEWORK-USAGE 系列规则"。

**节点 API（开源对标修正后收敛）**：

```go
// tools/archtest/internal/scanner/walk_node.go (新文件 ~250 LOC + unit test)
func EachFuncDecl(t *testing.T, fc FileContext, fn func(*testing.T, FileContext, *ast.FuncDecl))
func EachCallExpr(t *testing.T, fc FileContext, fn func(*testing.T, FileContext, *ast.CallExpr))
func EachImportSpec(t *testing.T, fc FileContext, fn func(*testing.T, FileContext, *ast.ImportSpec))
func EachGenDecl(t *testing.T, fc FileContext, fn func(*testing.T, FileContext, *ast.GenDecl))
```

**砍掉的 API**（开源对标确认无收益）：
- `EachNode([]ast.Node, fn)`：通用 ast.Inspect 包装，调用方还要 type-switch
- `Walk(fc, fn(node, push) bool)`：在没有 inspector events 摊销时只是重新发明 inspector.Nodes

**保留出口**（跨节点状态机场景，如 panic_invariants_test.go scope stack，< 5 文件）：

```
// SCANNER-FRAMEWORK-USAGE-02: bypass: <one of: scope-stack | cross-node-state | other>
```

USAGE-02 archtest 校验 (a) 注释存在 (b) category 严格属于 {scope-stack, cross-node-state, other}（避免 USAGE-02 substring + 自由 category 反模式重演）(c) 注释紧贴 ast.Inspect 调用站点上方 ≤ 3 行。

**Commit 序列**（红→绿严格分波，单 PR 内原子）：

```
C1 RED-1   feat(scanner): add EachFuncDecl/EachCallExpr/EachImportSpec/EachGenDecl
           ref: golang/tools go/analysis/passes/lostcancel
           +250 LOC framework + unit tests
           archtest CI 仍绿（API 还没人用）

C2 RED-2   test(archtest): add SCANNER-FRAMEWORK-USAGE-02 archtest gate
           +80 LOC scanner_framework_usage_test.go 内追加
           archtest CI 全红：FAIL USAGE-02 ×~196（131 裸 ast.Inspect + 65 手写 for-loop）

C3 GREEN-A refactor(archtest): migrate auth/security/setup to typed callbacks
C4 GREEN-B refactor(archtest): migrate cell/codegen/contract to typed callbacks
C5 GREEN-C refactor(archtest): migrate outbox/observability/redis to typed callbacks
C6 GREEN-D refactor(archtest): migrate clock/test_sleep/remaining; panic_invariants
           的 scope stack 加 USAGE-02 bypass 就地注释
C7 GREEN-E refactor(archtest): handle remaining tail; 收尾
C8 GREEN-F chore: remove unused ast/parser/token imports; go vet pass

最终 archtest CI 全绿；net LOC -100 ~ -300
```

reviewer commit-by-commit 审，单次注意力 ~1-2h × 8 commit。

### 4.2 PR-A' 详细设计（已 ship 实际形态）

```
C1 RED      test(archtest): add INVENTORY-ANCHOR-REQUIRED-01 + INVENTORY-ANCHOR-VALID-ID-01
            tools/archtest/inventory_anchor_required_test.go：
              - REQUIRED-01：每文件头第一个 CommentGroup 至少含一条合 grammar 的
                // INVARIANT: <ID>
              - VALID-ID-01：所有 INVARIANT 锚点（任意位置）必须合 canonical
                grammar `^[A-Z][A-Z0-9]+(-[A-Z0-9]+)*-[0-9]+([A-Za-z]|-[A-Z0-9]+)?$`
                覆盖 LAYER-05T / KERNEL-POOLSTATS-LOCATION-01a /
                RMQ-CHANNEL-MAX-PER-CONN-01-A 等子序 / 字母后缀形态

C2 GREEN-A  docs(archtest): backfill INVARIANT anchors for 55 files
            机械改动；参照已存在的 27 个有 anchor 的文件作为模板。
            archtest_test.go 用真实 LAYER-05/05T/06/06T/07/08/09/09T/10 +
            PGQUERY-01 多锚点列表，不立合成 alias。

C3 GREEN-B  chore(audit): drop archtest-inventory.md + verify gate, stdout-only listing
            - 删 docs/audit/archtest-inventory.md
            - 删 hack/verify-archtest-inventory.sh（make verify glob discovery 自动出 CI）
            - 删 kernel/governance/rule_inventory_test.go::TestArchtestInventoryNoIDTruncation
            - scripts/audit/list-archtests.sh 退化为 raw grep 输出
              （删 awk grammar / theme_for_id / fallback / hardcode 黑名单）。
              grammar 单源在 archtest 一处。
            - tools/archtest/doc.go 改写指引为 on-demand 命令

C4 GREEN-C  docs: sync backlog/plans/roadmap to PR-A' completion
```

**hardcode 黑名单删除**（**修订自原 PR-A**）：旧规则 `archtest_test.go|helpers_test.go|*_fixtures_test.go` hardcode 排除是漂移源（lintgate_smoke_test.go 这种 case）。新规则：`tools/archtest/*_test.go` 全部必须有 `// INVARIANT:` 锚点；helper / fixture 含锚点也合规（锚点本质是"归属哪条规则"的反向索引）。

### 4.3 PR-D' 详细设计（panic 单源）

**删除清单**：
- `architecturalPanicWhitelist map[string]string`（panic_invariants_test.go L102-111）
- `AllowMust: strings.HasPrefix(node.Name.Name, "Must")` 全局豁免（L320 + L354）
- `assertPanicWhitelistMatchesADR` 双源 reconciliation guard
- ADR markdown 中"白名单清单表格"

**新增清单**：
- panic_invariants_test.go 改用 `parser.ParseComments` 模式 + 扫描 panic 站点上方 ≤ 3 行 + godoc 注释 + 寻找 `// PANIC-REGISTERED-01: ADR-approved: <reason>` 形如注释；无注释 panic 即违规
- 4 处 re-throw（lifecycle/circuit_breaker/tx_manager × 2）+ 30+ Must* 函数逐个加就地注释（机械批量改）
- ADR markdown 改写为元规则文档（保留 A/B/C 三类原则 + reviewer checklist；删函数名清单）
- ref: cockroachdb/cockroach pkg/testutils/lint/passes/forbiddenmethod `HasNolintComment`

**Commit 序列**：

```
C1 RED      test(archtest): rewrite PanicRegistered to scan inline comments
            旧 architecturalPanicWhitelist + AllowMust 暂未删（双通道兼容，CI 绿）

C2 GREEN-A  docs(panic): annotate 4 re-throw sites + 30+ Must* sites
            机械批量加 PANIC-REGISTERED-01 注释

C3 GREEN-B  feat(archtest): drop architecturalPanicWhitelist + AllowMust + reconciliation
            ADR markdown 改写为元规则文档
            CI 单源化完成
```

### 4.4 PR-B / PR-C 详细设计（保留原 roadmap 设计）

**PR-B**：`kernel/governance/rule_inventory_test.go` 加静态 BFS，4 注册根扩闭包覆盖 const-ident emission（`ruleFMT20` 等）+ 双 receiver type（`*Validator` + `*DependencyChecker`）+ 闭包包装注册，断言 reachable rule IDs ⊇ golden 81 条。替代 zero-diff 反向证明的临时硬化。

**PR-C**：`kernel/metadata/schemas/contract.schema.json` + `contract_schema_test.go` + `rules_fmt_test.go` 覆盖 6 个 auth 布尔字段所有合法/非法组合。FMT-27/28 互斥校验已存在不动。

### 4.5 并行度矩阵（5 PR）

| | PR-Φ | PR-A' | PR-B | PR-C | PR-D' |
|---|---|---|---|---|---|
| PR-Φ | — | **冲突1** | 0 | 0 | **冲突2** |
| PR-A' | | — | 0 | 0 | 0 |
| PR-B | | | — | 0 | 0 |
| PR-C | | | | — | 0 |
| PR-D' | | | | | — |

- **冲突1**：PR-Φ 改 22 混用文件 + 部分纯裸 ast.Inspect 文件，PR-A' 在 46 文件加 anchor，文件域大重叠
- **冲突2**：PR-Φ GREEN-D 处理 panic_invariants_test.go scope stack 加 USAGE-02 bypass 注释，PR-D' 删 architecturalPanicWhitelist + AllowMust + 重写检测逻辑，同文件大冲突

**PR-A' / PR-B / PR-C / PR-D' 与 Path C 文件域 0 重叠**，可与 Path C 并行；**PR-Φ 必须 Path C ship 后启动**（共享 scanner_framework_usage_test.go）。

### 4.6 调度

```
Day 0（Path C 已 ship 假设）：
  worktree-1：PR-Φ        启动（最大 PR；先 RED commits）
  worktree-2：PR-B        启动（独立）
  worktree-3：PR-C        启动（独立）

Day 1-3：
  PR-Φ commit 推进
  PR-B / PR-C ship

Day 3-4：
  PR-Φ ship（review 5-7h，最重）

Day 4-5（PR-Φ merge 后）：
  worktree-4：PR-A'       启动
  worktree-5：PR-D'       启动

Day 5-7：
  PR-A' ship
  PR-D' ship
```

reviewer 优先级：**PR-Φ ≫ PR-B = PR-C > PR-A' > PR-D' > PR-F**。

**wall-clock 估算**（Path C 完成后启动）：
- 5-PR Batch 1：~1 周
- PR-F：穿插 2h
- Batch 3：触发型，无固定时间

**总账**（vs 原 roadmap）：

| 维度 | 原 roadmap | 彻底版 |
|---|---|---|
| 总 dev 工时 | 45-60h | **34-42h** |
| 总 review 工时 | 25-35h | **12-15h** |
| Batch 2 PR 数 | 8-12 PR-E* | **0**（filepath.Walk 由 Path C 处理；ast 节点由 PR-Φ 一锅）|
| 总 wall-clock | 3-4 周 | **~1 周** |
| 永久 ratchet 维护 | 一直在 | **0** |
| "下一轮 follow-up" 风险 | 高 | **极低** |

### 4.7 Batch 3（触发型，与原 roadmap 一致）

| PR | trigger | 顺序约束 |
|---|---|---|
| `PR411-HANDLER-POLICY-TYPEAWARE-SCANNER-01` | scanner 误报/漏报 | 基于 framework 做（直接用 `internal/scanner` API） |
| `PR411-SERVICEOWNED-OWNERSHIP-GUARD-01` | `auth.serviceOwned` endpoint > 1 / auth ownership 模型硬化 | 与 framework 解耦 |
| `B-FLOOR-FOLLOWUP` §2.5 Success-Floor | contract.yaml status 声明 ⇔ adapter typed return 漂移事故首现 / cells 数量增长到 Floor 升级 ROI > 16h dev | 必须先做段 2.5 |
| `B-FLOOR-FOLLOWUP` §4 Full-Floor | §2.5 已 ship 且稳定 | 等 §2.5 |

---

## 5. 风险与缓解

| 风险 | 等级 | 缓解 |
|---|---|---|
| **R1**：PR-Φ 单 PR 太大 reviewer 不审 | 高 | (a) commit-by-commit 严格分波（C1=API / C2=archtest / C3-C7=按文件域 5-10 文件 / C8=收尾），reviewer commit-by-commit 审；(b) PR description 给 framework API 表 + 全员迁移 diff 模板（多文件改的 pattern 一致，识别一次审 N 次）；(c) `walk_node.go` 单测 ≥ 95%；(d) 接受 5-7h review 作为换"无 follow-up"的代价 |
| **R2**：一锅迁移触发 CI 雪崩 | 中 | (a) 本地先跑全套 archtest 确认全绿再 push；(b) 不并行任何 worktree 改 tools/archtest（PR-A'/PR-D' 等到 PR-Φ merge）；(c) 万一雪崩可原子 revert 整 PR |
| **R3**：framework API 漏 case | 中 | (a) 从已有 22 混用 + 65 处手写 + 131 裸 ast.Inspect 作为真实样本逆向设计；(b) 接受"保留出口" + 严格 category 就地注释，能力等价裸 ast.Inspect |
| **R4**：PR-A' 46 文件批量改 review 复杂度高 | 中 | (a) 文件锚点回填是机械性改动（文件头加 `// INVARIANT: <ID>` 一行）；(b) reviewer 主要看 list-archtests.sh + 新 archtest + workflow yaml；机械部分可大段折叠 |
| **R5**：PR-A' 删持久 inventory.md + drift gate 后规则锚点漂移无警告 | 低 | INVENTORY-ANCHOR-REQUIRED-01 archtest（每文件至少一锚点）+ INVENTORY-ANCHOR-VALID-ID-01 archtest（canonical grammar）共同单源守卫；漏写 / 格式漂移即 archtest 红，无需 drift gate 兜底；list-archtests.sh raw grep 仅作 audit 视图，不再有第二份 grammar |
| **R6**：PR-B BFS 实现遗漏注册路径（const-ident emission / 闭包包装） | 低 | 任务表已列出 4 类注册形态；PR description 要求覆盖矩阵，reviewer 按矩阵逐项核 |
| **R7**：PR-D' 30+ Must* 站点漏标 | 中 | (a) RED-first commit C1 让 archtest 红遍所有未标 panic 站点，CI 自动保证全标完才转绿；(b) 注释模板统一减少漏改 |
| **R8**：新 USAGE-02 bypass 就地注释重蹈原 bf37fa8 USAGE-02 substring 反模式 | 中 | category 字符串严格属于已知集合（{scope-stack, cross-node-state, other}），不允许自由文本；live 规则用真实源码 fixture（不 hand-craft）；category 集合 grow 必走 PR review |
| **R9**：Path C 未 ship 时 PR-Φ 启动导致 USAGE-01/03 文件冲突 | 低 | 显式约束 PR-Φ 必须 Path C ship 后启动；PR-A'/B/C/D' 与 Path C 文件域 0 重叠不受此约束 |

---

## 6. 引用

- 决策原则：`CLAUDE.md` `## 新增 invariant 决策原则`
- ADR：`docs/architecture/202605061500-adr-typed-response-envelope.md` §D6/D7（typed envelope）/ `docs/architecture/202604270030-architectural-panic-whitelist.md`（PR-D' 改写目标）
- Inventory（**已被 PR-A' 删除**；现场清单：`bash scripts/audit/list-archtests.sh`，唯一守卫 `INVENTORY-ANCHOR-REQUIRED-01` archtest）
- 配套 plan（含开源对标 K8s/CockroachDB/staticcheck/golang.org/x/tools 报告）：`~/.claude-ming/plans/ast-ast-inspect-inherited-wadler.md`
- ref（PR-Φ）：golang/tools `go/analysis/passes/lostcancel/lostcancel.go@master`（typed-callback 单遍 idiom）
- ref（PR-D'）：cockroachdb/cockroach `pkg/testutils/lint/passes/forbiddenmethod/forbiddenmethod.go@master`（nolint 就地注释单源）
- 历史版本（含完整根因 / 主流路线对照 / 取舍记录 / 原 7 切片视图 / PR-G frozen allowlist + ratchet 论证）：git history `1472336b`（原 326 行版本）、`6c7dfba6`（funnel roadmap rewrite 之前的 215 行版本）之前
