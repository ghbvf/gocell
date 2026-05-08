# PR #403 修复路线图（funnel 化优先 / 实施视角）

**对接**：`docs/reviews/202605070153-pr403-third-wave-review.md` §4 修复方案
**关系**：替代原报告 §4 路线，§1-§3 症状/根因诊断保留在 review
**生成日期**：2026-05-07
**最后更新**：2026-05-08（激进合并：删 7 个切片视图，合一为单一任务表 + 实施叙事；吸收 PR #418 / #419 ship + 4 follow-up）

---

## 1. 当前状态（对齐时点 2026-05-08）

段 1（typed envelope 闭环 + ADR D6/D7）/ 段 3'（CLAUDE.md funnel-first 原则）/ PR-FUNNEL-01（archtest 文件 104→70）/ PR-FUNNEL-02（handler invariants funnel）/ PR-FUNNEL-03（governance rules 15→8）/ Batch 0 SCANNER-FRAMEWORK 全部 ✅。

实测基线：72 archtest 文件 / 89 INVARIANT 锚点（Go 源）/ 8 governance source 文件。

剩余主线：Batch 1 6 条 follow-up（21-26h dev + 11h review）+ Batch 2 70+ 旧 scanner 渐进迁移 + Batch 2 末尾 USAGE-01 守卫闸 + Batch 3 外部信号触发项。

---

## 2. 决策与不做的事（最终路线立场）

- **不建 Registry / 中心化注册表**：K8s / CockroachDB / Linux / Rust / Go 工具链均无 prior art；多份文档用 `// INVARIANT: <ID>` grep 锚点串联即可。
- **不立 PR template / four-piece kit ADR**：项目大规模重构期填字段会变应付式；funnel-first 原则 < 10 行已写入 CLAUDE.md。
- **接受 funnel 不到的物理残留**：Go 缺 sealed package / newtype / const string 区分，~50% 约束物理上 funnel 不掉（buffer-then-commit 顺序、message const literal、panic 白名单、readyz probe 命名等）；CockroachDB 同款语言天花板，残留 ~30 条平铺管理。
- **archtest 数量基线**：CockroachDB ~30 是参照值，GoCell 规模可压更低；具体数值不立硬指标，按 PR-FUNNEL-NN 自然降。
- **PR 切片纪律**：单 PR 范围控制在 Cx2-Cx3 / 单一概念边界；不为"同根因"叙事打包合并 PR。

详细论证（K8s/CockroachDB/Linux/Rust/Go 工具链对照、Registry 无 prior art、Go 语言天花板）见原 326 行版本（git history `1472336b` 之前）。

---

## 3. 任务表（唯一事实表）

| ID | Batch | Status | 工时 dev+review | 说明 |
|---|---|---|---|---|
| 段 1 typed envelope 闭环 | — | ✅ | — | PR #403（71be4d6e）；ADR D6/D7 |
| 段 3' CLAUDE.md funnel-first | — | ✅ | — | `CLAUDE.md:73` `## 新增 invariant 决策原则` |
| PR-FUNNEL-01 主题聚并 | — | ✅ | — | PR #408（5461d53e）；archtest 104→70 |
| PR-FUNNEL-01 follow-up | — | ✅ | — | PR #412（16a13993）；parse-error fail-loud + git ls-files |
| PR-FUNNEL-02 handler funnel | — | ✅ | — | PR #411（18b60a5c）；4 archtest 删 + HANDLER-POLICY-REQUIRED-01 升 funnel + auth.clientsOnly/serviceOwned spec flag + synth_http_auth_modes fixture |
| Audit step A 清单化 | — | ✅ | — | `list-archtests.sh` + `inventory.md` + `verify-archtest-inventory.sh` 漂移闸 |
| `PR408-FU-SCANNER-SHARED-FRAMEWORK-01` | 0 | ✅ | — | PR #419（996784cf）；`tools/archtest/internal/scanner/` 共享框架 + 4 demo 迁移；fail-closed by construction、structured scope predicate、内置 vendor/testdata/worktrees skip、统一 receiver-type 解析 |
| `PR-FUNNEL-03` governance 聚并 | 1 | ✅ | — | PR #418（e8cdf3c9）；source 15→8 文件（fmt/ref/topo/verify/http/misc_advisory/misc_consistency/misc_strict）+ `rule_inventory_test.go` golden 锁 81 条规则 ID |
| `PR408-FU-LEGACY-ANCHOR-BACKFILL-01` | 1 | ⏳ | 3-4h+2h | 46 个 `tools/archtest/*_test.go` 加 `// INVARIANT:` 锚点（实测 2026-05-08；backlog 写 39 偏少）+ `list-archtests.sh` 删 fallback + 新 archtest `INVENTORY-ANCHOR-REQUIRED-01` 守锚点必现 |
| `PR408-FU-GOVERNANCE-OWNER-AST-EXTRACTION-01` | 1 | ⏳ | 4h+2h | `list-archtests.sh` 改 AST owner 提取（按 `Rule{ID:...}` struct literal / `const ruleID = "..."` 定位）+ inventory 加 `referenced_by` 列；冲突点：与 ANCHOR-BACKFILL 都改 `list-archtests.sh`（不同函数 trivial rebase） |
| `PR411-AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01` | 1 | ⏳ | 4h | schema/governance 对显式 `false` 语义统一 + 回归测试 |
| `GOVERNANCE-RULE-REACHABILITY-TEST-01` | 1 | ⏳ | ~6h+2h | `rule_inventory_test.go` 加静态 BFS：从 `rules()` / `strictRules()` / 公开 `Check*` 4 个注册根扩闭包，覆盖 const-ident emission（`ruleFMT20` 等）/ 双 receiver type（`*Validator` + `*DependencyChecker`）/ 闭包包装注册，断言 reachable rule IDs ⊇ golden 81 条；替代 PR-FUNNEL-03 当前 `gocell validate` zero-diff 反向证明的临时硬化 |
| `PR419-FU-INVENTORY-CI-GATE-01` | 1 | ⏳ | 1-2h+1h | `bash hack/verify-archtest-inventory.sh` 加入 `.github/workflows/_build-lint.yml` integration-test job（或独立 verify job），漂移即 CI 红 |
| `PR419-FU-PANIC-MUST-PATH-SCOPE-01` | 1 | ⏳ | 3-4h+2h | `panic_invariants_test.go` 把 `strings.HasPrefix(node.Name.Name, "Must")` 全局豁免改为受 `architecturalPanicWhitelist` path 前缀约束（仅 websocket/kernel/cell bootstrap 路径下豁免 Must*），或为 6 条 C 类显式补 whitelist 条目 |
| `PR-FUNNEL-04` 候选评估 | 2 | ⏳ | 2h | 扫 70+ archtest 找可 type-system 化（typed `XxxResponseObject` 替代 `(*Response, error)`）/ 冗余 / 重复，发现 ≥3 条候选才启动小 PR 系列；否则保留为长期残留 |
| 70+ 旧 scanner 渐进迁移 | 2 | ⏳ | 持续 | 按文件域（auth / cell / codegen / outbox / ...）分小 PR，每 PR 5-10 个 scanner |
| `PR408-FU-SCANNER-USAGE-01-ENABLEMENT` | **1（RED-first）** | ⏳ | ~3-4h+2h | **修正 2026-05-08**（原计划 Batch 2 末尾启用是误判，违反 CLAUDE.md TDD 红→绿原则）。Day 0 ship `tools/archtest/scanner_framework_usage_test.go`（INVARIANT: SCANNER-FRAMEWORK-USAGE-01）+ `scanner_framework_usage_allowlist.go`（frozen allowlist 含 70+ 文件清单）+ ratchet test 守 size 单调递减；PR-E* 每 PR 缩 allowlist 直至 0；最后一个 PR-En 删 allowlist 机制变无条件硬约束 |
| `PR411-HANDLER-POLICY-TYPEAWARE-SCANNER-01` | 3 | 触发 | — | trigger: scanner 误报/漏报；基于 Batch 0 framework 做 |
| `PR411-SERVICEOWNED-OWNERSHIP-GUARD-01` | 3 | 触发 | — | trigger: `auth.serviceOwned` endpoint > 1 / auth ownership 模型硬化批次 |
| `B-FLOOR-FOLLOWUP` §2.5 Success-Floor | 3 | 触发 | — | trigger: contract.yaml status 声明 ⇔ adapter typed return 漂移事故首现 / cells 数量增长到 Floor 升级 ROI > 16h dev |
| `B-FLOOR-FOLLOWUP` §4 Full-Floor | 3 | 触发 | — | trigger: §2.5 Success-Floor 已 ship 且稳定 |

---

## 4. 实施计划（PR 合并分组 + 并行调度）

### 4.1 PR 全集（Batch 1 + Batch 2 + Batch 3）

按 CLAUDE.md "TDD 严格红→绿，archtest 必须先于实施 batch" + "单一概念边界 + 共享文件 + 一次性产物重生" 原则。Batch 1 5 个 PR 全部 Day 0 并行；Batch 2 渐进迁移按文件域分 N 个小 PR + 1 个 FUNNEL-04 评估；Batch 3 触发型按信号启动。

#### Batch 1（5 个 PR，可全部 Day 0 并行）

| PR | 合并条目 | 工时 dev+review | 文件域 |
|---|---|---|---|
| **PR-A** `INVENTORY-GOVERNANCE-HARDENING` | `ANCHOR-BACKFILL-01` + `OWNER-AST-EXTRACTION-01` + `INVENTORY-CI-GATE-01` 三条 | 9-12h + 4-5h | `scripts/audit/list-archtests.sh` + 46 个 `tools/archtest/*_test.go` + `tools/archtest/inventory_anchor_required_test.go`（新）+ `docs/audit/archtest-inventory.md`（重生 + referenced_by 列）+ `.github/workflows/_build-lint.yml` |
| **PR-B** `GOVERNANCE-RULE-REACHABILITY-TEST-01` | 独立 | ~6h + 2h | `kernel/governance/rule_inventory_test.go`（扩展 ~280 LOC） |
| **PR-C** `AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01` | 独立 | 4h + 1h | `kernel/metadata/schemas/contract.schema.json` + `contract_schema_test.go` + `rules_fmt_test.go` |
| **PR-D** `PANIC-MUST-PATH-SCOPE-01` | 独立 | 3-4h + 2h | `tools/archtest/panic_invariants_test.go` |
| **PR-G** `SCANNER-USAGE-01-GATE`（**RED-first，前置 Batch 2**）| `PR408-FU-SCANNER-USAGE-01-ENABLEMENT` | ~3-4h + 2h | `tools/archtest/scanner_framework_usage_test.go`（新）+ `tools/archtest/scanner_framework_usage_allowlist.go`（frozen allowlist，初始 70+ 文件清单）+ allowlist ratchet test（断言 allowlist size 只缩不长）|

**PR-G 提前依据**（修正 2026-05-08）：
- CLAUDE.md "TDD 严格红→绿"：archtest 必须先于实施 batch ship，单独 Wave commit RED 再让实施转 GREEN
- 不先 ship 守卫，PR-E* 70+ 迁移 PR 无法用 CI 自动证明"确实改用 framework"，每个迁移 PR 都要 reviewer 反证（手工验证 framework 用对、没漏改、没新增 `filepath.WalkDir` 直调），review 成本爆炸
- "建守卫 + 豁免所有违反者"软回退批评不成立：allowlist 是 **frozen ratchet**（只缩不长，由 ratchet test 守 size 单调下降），不是永久豁免；TS strict mode rollout / Go vet checks rollout / Linux kernel sparse rollout 都用这套

PR-A 合并依据：三条共享根因 — 删 grep fallback（ANCHOR）必须补 AST 主路径（OWNER），CI gate 顺路接上立即生效，避免 inventory 重生两次。

不再合并依据：
- PR-B vs PR-A：`rule_inventory_test.go` vs `list-archtests.sh` + 46 archtest，文件域 0 重叠，概念不同（rule 可达性 vs inventory 准确性）
- PR-C vs 其他：`schemas/contract.schema.json` 是数据契约，与 archtest 概念边界完全独立
- PR-D vs PR-A：实测 `panic_invariants_test.go` 已有 INVARIANT 锚点，不在 PR-A 的 46 文件回填范围；PR-D 改 AllowMust 函数体，0 重叠
- PR-G vs PR-A：scanner usage 守卫 + frozen allowlist 与 inventory 准确性硬化是两个独立概念边界，文件域 0 重叠

#### Batch 2（N 个迁移 PR + 1 评估，PR-G + PR-A merge 后启动）

| PR | 性质 | 工时 dev+review | 启动条件 |
|---|---|---|---|
| **PR-E1..En** Scanner 渐进迁移系列 | 按文件域（auth / cell / codegen / outbox / contract / governance / ...）分小 PR，每 PR 5-10 个 scanner 迁移到 `internal/scanner` 框架 + 同 PR 从 PR-G 的 frozen allowlist 中**移除已迁移文件名**（ratchet test 自动守 size 严格递减）| 每 PR 3-5h + 1-2h × N（70+ 文件 ≈ 8-12 PR）| **PR-G + PR-A merge 之后**（PR-G 提供 archtest 红→绿循环，PR-A 提供 inventory 行号变动 CI gate）|
| **PR-F** `PR-FUNNEL-04` 候选评估 | 扫 70+ archtest 找可 type-system 化 / 冗余 / 重复，发现 ≥3 条候选才启动后续小 PR 系列 | 2h（仅评估）+ 后续小 PR 视情况 | Batch 2 期间穿插，单 worktree 完成 |

**收尾**：当 PR-E* 把 frozen allowlist 缩到 0 时，最后一个 PR-En 顺路删 allowlist 文件 + ratchet test，archtest 守卫变为**无条件硬约束**。不需要单独"收尾闸 PR"。

#### Batch 3（触发型，无固定时间）

| PR | trigger | 顺序约束 |
|---|---|---|
| `PR411-HANDLER-POLICY-TYPEAWARE-SCANNER-01` | scanner 误报/漏报触发 | 基于 Batch 0 framework 做（直接用 `internal/scanner` API） |
| `PR411-SERVICEOWNED-OWNERSHIP-GUARD-01` | `auth.serviceOwned` endpoint > 1 / auth ownership 模型硬化批次 | 与 framework 解耦，独立 |
| `B-FLOOR-FOLLOWUP` §2.5 Success-Floor | contract.yaml status 声明 ⇔ adapter typed return 漂移事故首现 / cells 数量增长到 Floor 升级 ROI > 16h dev | **必须先做**段 2.5 |
| `B-FLOOR-FOLLOWUP` §4 Full-Floor | §2.5 已 ship 且稳定 | 等 §2.5 |

### 4.2 并行性矩阵

**Batch 1 内部**（5 个 PR）：

| | PR-A | PR-B | PR-C | PR-D | PR-G |
|---|---|---|---|---|---|
| PR-A | — | 0 | 0 | 0（实测）| 0 |
| PR-B | | — | 0 | 0 | 0 |
| PR-C | | | — | 0 | 0 |
| PR-D | | | | — | 0 |
| PR-G | | | | | — |

PR-G 与 PR-D 都在 `tools/archtest/` 目录，但 PR-G 加新文件（`scanner_framework_usage_test.go` + `_allowlist.go`），PR-D 改已有文件（`panic_invariants_test.go` 函数体），0 重叠。

**Batch 1 与 Batch 2 之间**：
- **PR-G 必须先于 PR-E***（archtest RED-first，让 PR-E* 用 CI 自动证明 GREEN，避免 reviewer 反证）
- **PR-A 必须先于 PR-E***（CI gate 守迁移行号变动，inventory 漂移自动捕获）
- PR-B/C/D 与 PR-E* 完全独立可并行

**Batch 2 内部**：PR-E1..En 文件域按 cell/cap 切分，互不重叠（每 PR 改 5-10 个 scanner + allowlist 同 PR 移除）；PR-F（评估）只读分析与 PR-E* 不冲突。

**Batch 3**：触发型，独立。

### 4.3 ship 顺序与优先级

**P0 – Batch 1**（建议本周内 ship）：
1. **PR-G** — 红→绿前置闸，**必须 Batch 2 启动前 ship**；工时小（~3-4h），优先排单
2. **PR-A** — Batch 2 inventory 行号守，必须 Batch 2 启动前 ship；影响面大优先排
3. **PR-B** — 替代 PR-FUNNEL-03 临时硬化（zero-diff 是反向证明，新规则漏挂静默通过）
4. **PR-C / PR-D** — 工时小（< 5h），穿插 ship 解锁 reviewer 容量

**P1 – Batch 2**（PR-G + PR-A 都 merge 后启动）：
- **PR-E* 渐进迁移**：8-12 个小 PR，每 PR 一个文件域 + 同 PR 缩 allowlist；按团队余量持续推进
- **PR-F 候选评估**：单 worktree 穿插完成（2h）
- **收尾**：最后一个 PR-En 顺路删 allowlist 机制（archtest 变无条件硬约束）

**P2 – Batch 3**：触发型，无固定排期。

**reviewer 优先级**（同时多 PR 在审时）：**PR-G ≥ PR-A** > PR-B > PR-C = PR-D > PR-E* > PR-F

### 4.4 调度建议

```
Week 1
─────────────────────────────────────────────────
Day 0：
  worktree-1：PR-A  INVENTORY-HARDENING       [9-12h dev]
  worktree-2：PR-B  REACHABILITY-TEST         [6h dev]
  worktree-3：PR-C  AUTH-SCHEMA-BOOL          [4h dev]
  worktree-4：PR-D  PANIC-MUST-SCOPE          [3-4h dev]
  worktree-5：PR-G  SCANNER-USAGE-01-GATE     [3-4h dev]  ← RED-first 前置

Day 1-2：
  PR-G ship（最高优先级，前置 Batch 2）
  PR-D / PR-C 穿插 ship
  PR-B ship
  PR-A ship（影响面最大，最后 ship）

Day 2-3（PR-G + PR-A merge 后）：
  worktree-6..N：PR-E1..En 按文件域逐批开
    例：PR-E1 archtest auth/* (5-10 个)     [3-5h dev + allowlist 缩 5-10 项]
        PR-E2 archtest cell/* (5-10 个)     [3-5h dev]
        PR-E3 archtest codegen/* (5-10 个)
        ...
  worktree-X：PR-F 候选评估                  [2h]

Week 2-3
─────────────────────────────────────────────────
  PR-E* 持续渐进 ship（8-12 个小 PR），allowlist 单调缩小
  ratchet test 自动守 allowlist size 严格递减
  PR-F 发现 ≥3 候选则启动后续小 PR 系列

Week 3-4（PR-E* 把 allowlist 缩到 0 时）
─────────────────────────────────────────────────
  最后一个 PR-En 顺路删 allowlist 文件 + ratchet test
  → SCANNER-FRAMEWORK-USAGE-01 变无条件硬约束（无 allowlist）

Batch 3：外部信号触发，无固定排期
```

**wall-clock 估算**：
- Batch 1（5 PR 并行）：~2-3 天
- Batch 2（PR-E* 渐进 + PR-F）：~2-3 周（依团队余量）
- Batch 3：触发型，无固定时间

---

## 5. 风险

| 风险 | 缓解 |
|---|---|
| PR-A 46 文件批量改 review 复杂度高 | 文件锚点回填是机械性改动（文件头加 `// INVARIANT: <ID>` 一行），reviewer 主要看 list-archtests.sh + 新 archtest + workflow yaml；机械部分可大段折叠 |
| PR-A 删 grep fallback 后 list-archtests.sh AST 主路径有 bug 导致漏报 | 同 PR 加 `inventory_anchor_required_test.go` 守锚点必现 + 重生 inventory.md 与现状 zero-diff 验证；漂移闸进 CI 兜底 |
| PR-B BFS 实现遗漏注册路径（如 const-ident emission / 闭包包装） | 任务表说明列已列出 4 类注册形态；PR 描述要求覆盖矩阵，reviewer 按矩阵逐项核 |
| PR-G frozen allowlist 被当成永久豁免（破坏 ratchet 单向性）| 同 PR 加 ratchet test 守 `len(allowlist) <= initialSize` + 每次 PR-E* CI 跑回归确保 size 严格递减；PR review checklist 加"是否缩 allowlist"问句；backlog 登记 ratchet 触发条件（`len(allowlist) == 0` 时删机制）|
| PR-G 初始 allowlist 漏文件（实际违反者 > 70 但只列了 70）| Day 0 用 `grep -lE 'filepath\.(WalkDir\|Walk)' tools/archtest/*_test.go` 自动生成初始 allowlist + 同 PR 加生成脚本 `hack/regen-scanner-usage-allowlist.sh`；ratchet test 双向校验"未在 allowlist 又 import filepath.WalkDir/Walk"立即 fail |
| PR-A 与 Batch 2 第一个迁移 PR 同时 ship 导致 inventory 重复重生冲突 | PR-A + PR-G 都先 merge，Batch 2 worktree 在两者 merge 后再开 |
| Batch 2 PR-E* 迁移漏改导致 framework 用法不正确 | PR-G 守卫强制每个迁移：(a) 删 `filepath.WalkDir/Walk` 直调（不删则 archtest 失败）+ (b) 从 allowlist 移除（不移则 ratchet test fail）；reviewer 不需"反证" |
| Batch 2 70+ 迁移期间漏 regenerate inventory | PR-A 加的 CI gate 自动拦截（漂移即 CI 红） |

---

## 6. 引用

- 决策原则：`CLAUDE.md` `## 新增 invariant 决策原则`
- ADR：`docs/architecture/202605061500-adr-typed-response-envelope.md` §D6/D7
- Inventory：`docs/audit/archtest-inventory.md`（自动生成）
- 历史版本（含完整根因 / 主流路线对照 / 取舍记录 / 原 7 切片视图）：git history `1472336b` 之前
