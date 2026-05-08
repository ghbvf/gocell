# PR #403 修复路线图（funnel 化优先 / 实施视角）

**对接**：`docs/reviews/202605070153-pr403-third-wave-review.md` §4 修复方案
**关系**：替代原报告 §4 路线，§1-§3 症状/根因诊断保留在 review
**生成日期**：2026-05-07
**最后更新**：2026-05-09（PR-G + PR-E* 一次性 ship 完成 — 实测 24 文件迁移，无需 allowlist + ratchet 机制）

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
| 旧 scanner 渐进迁移（实测 24 文件） | 2 | ✅ | — | **修正 2026-05-09**：原"70+"是把 inventory 总文件数误为待迁移；实测 24 文件 / 39 walk site，单 PR 一次完成（dev 4-agent 并行 ~3-5h wall-clock），不需要按域分多个小 PR |
| `PR408-FU-SCANNER-USAGE-01-ENABLEMENT` | 1（RED-first） | ✅ | — | **修正 2026-05-09**：与"24 文件迁移"合一为单 PR / commit 序列内部红→绿（C1 RED archtest + C2..C5 4 group GREEN 迁移 + C6 文档同步）。**不带 allowlist + ratchet**（实测 24 文件单 PR 一次迁完，allowlist 是 scaffold；既能一次迁完，scaffold 是冗余）。archtest `SCANNER-FRAMEWORK-USAGE-01` 一开始就是无条件硬约束 |
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
| **PR-G + PR-E** `SCANNER-USAGE-01-GATE + 24-file migration` | `PR408-FU-SCANNER-USAGE-01-ENABLEMENT` + 旧 scanner 迁移 | ✅ 已 ship | `tools/archtest/scanner_framework_usage_test.go`（新，无 allowlist）+ 24 个 `tools/archtest/*_test.go` 迁移到 `internal/scanner` |

**PR-G/PR-E 合一依据**（2026-05-09 ship 后回溯）：
- 实测仅 24 文件 / 39 walk site（roadmap 原文"70+"是把 inventory 总文件数误为待迁移）
- 4-agent 并行 ~3-5h wall-clock 即可一次迁完，allowlist + ratchet 机制是为"无法一次迁完"设计的 scaffold；既然能一次迁完，scaffold 是冗余
- 单 PR / commit 序列内部红→绿（C1 RED archtest 在 develop 上有独立可见 commit + C2..C5 4 group GREEN 迁移），符合 CLAUDE.md "TDD 严格红→绿，单独 Wave commit RED" 在 commit 级别的精神
- 不留 amber 中间状态污染 ADR / develop；archtest `SCANNER-FRAMEWORK-USAGE-01` 一开始就是无条件硬约束

PR-A 合并依据：三条共享根因 — 删 grep fallback（ANCHOR）必须补 AST 主路径（OWNER），CI gate 顺路接上立即生效，避免 inventory 重生两次。

不再合并依据：
- PR-B vs PR-A：`rule_inventory_test.go` vs `list-archtests.sh` + 46 archtest，文件域 0 重叠，概念不同（rule 可达性 vs inventory 准确性）
- PR-C vs 其他：`schemas/contract.schema.json` 是数据契约，与 archtest 概念边界完全独立
- PR-D vs PR-A：实测 `panic_invariants_test.go` 已有 INVARIANT 锚点，不在 PR-A 的 46 文件回填范围；PR-D 改 AllowMust 函数体，0 重叠
- PR-G/PR-E vs PR-A：scanner usage 守卫 + 24-file migration 与 inventory 准确性硬化是两个独立概念边界，文件域 0 重叠

#### Batch 2（已收口 — PR-F 候选评估单独成 1 PR）

| PR | 性质 | 工时 dev+review | 启动条件 |
|---|---|---|---|
| **PR-F** `PR-FUNNEL-04` 候选评估 | 扫剩余 archtest 找可 type-system 化 / 冗余 / 重复，发现 ≥3 条候选才启动后续小 PR 系列 | 2h（仅评估）+ 后续小 PR 视情况 | 任意时点（与 PR-G/PR-E 已合一 ship 后无依赖）|

> 原计划 PR-E1..En 渐进迁移已与 PR-G 合一为单 PR ship 完成（实测 24 文件 / 39 walk site，4-agent 并行 ~3-5h），不再切多个小 PR。

#### Batch 3（触发型，无固定时间）

| PR | trigger | 顺序约束 |
|---|---|---|
| `PR411-HANDLER-POLICY-TYPEAWARE-SCANNER-01` | scanner 误报/漏报触发 | 基于 Batch 0 framework 做（直接用 `internal/scanner` API） |
| `PR411-SERVICEOWNED-OWNERSHIP-GUARD-01` | `auth.serviceOwned` endpoint > 1 / auth ownership 模型硬化批次 | 与 framework 解耦，独立 |
| `B-FLOOR-FOLLOWUP` §2.5 Success-Floor | contract.yaml status 声明 ⇔ adapter typed return 漂移事故首现 / cells 数量增长到 Floor 升级 ROI > 16h dev | **必须先做**段 2.5 |
| `B-FLOOR-FOLLOWUP` §4 Full-Floor | §2.5 已 ship 且稳定 | 等 §2.5 |

### 4.2 并行性矩阵

**Batch 1 剩余 4 个 PR**（PR-G/PR-E 已合一 ship）：

| | PR-A | PR-B | PR-C | PR-D |
|---|---|---|---|---|
| PR-A | — | 0 | 0 | 0（实测）|
| PR-B | | — | 0 | 0 |
| PR-C | | | — | 0 |
| PR-D | | | | — |

**Batch 2**：PR-F 候选评估只读分析，与 Batch 1 剩余 PR 完全独立，可任意时点穿插。

**Batch 3**：触发型，独立。

### 4.3 ship 顺序与优先级

**已 ship**：
- ✅ **PR-G/PR-E 合一**（refactor/541-scanner-framework-migration，2026-05-09）— archtest gate + 24 文件迁移单 PR / commit 序列红→绿

**P0 – Batch 1 剩余 4 个 PR**（建议本周内 ship）：
1. **PR-A** — inventory 准确性硬化；影响面大优先排
2. **PR-B** — 替代 PR-FUNNEL-03 临时硬化（zero-diff 是反向证明，新规则漏挂静默通过）
3. **PR-C / PR-D** — 工时小（< 5h），穿插 ship 解锁 reviewer 容量

**P1 – Batch 2**：
- **PR-F 候选评估**：单 worktree 穿插完成（2h）；评估剩余 archtest 是否还有可 type-system 化候选

**P2 – Batch 3**：触发型，无固定排期。

**reviewer 优先级**：**PR-A** > PR-B > PR-C = PR-D > PR-F

### 4.4 调度建议

```
已 ship（refactor/541-scanner-framework-migration，2026-05-09）
─────────────────────────────────────────────────
PR-G/PR-E 合一  archtest gate + 24 file migration  [main 1h + 4 sub-agent 并行 ~5h]
  Commit 1 (RED):  scanner_framework_usage_test.go (39 violations exposed)
  Commit 2 (GREEN Group A): auth+access+security 7 files / 11 walks
  Commit 3 (GREEN Group B): cell-infra+handler 8 files / 12 walks
  Commit 4 (GREEN Group C): outbox+event+pg 4 files / 6 walks
  Commit 5 (GREEN Group D): codegen+errcode+adapter 5 files / 10 walks
  Commit 6 (docs): roadmap 同步

剩余调度
─────────────────────────────────────────────────
Day 0:
  worktree-1: PR-A  INVENTORY-HARDENING  [9-12h dev]
  worktree-2: PR-B  REACHABILITY-TEST    [6h dev]
  worktree-3: PR-C  AUTH-SCHEMA-BOOL     [4h dev]
  worktree-4: PR-D  PANIC-MUST-SCOPE     [3-4h dev]

Day 1-2:
  PR-D / PR-C 穿插 ship
  PR-B ship
  PR-A ship（影响面最大，最后 ship）

任意时点:
  worktree-X: PR-F 候选评估              [2h]

Batch 3：外部信号触发，无固定排期
```

**wall-clock 估算**（PR-G/PR-E 合一 ship 后剩余）：
- Batch 1 剩余（4 PR 并行）：~2-3 天
- Batch 2（PR-F 评估）：~2h
- Batch 3：触发型，无固定时间

---

## 5. 风险

| 风险 | 缓解 |
|---|---|
| PR-A 46 文件批量改 review 复杂度高 | 文件锚点回填是机械性改动（文件头加 `// INVARIANT: <ID>` 一行），reviewer 主要看 list-archtests.sh + 新 archtest + workflow yaml；机械部分可大段折叠 |
| PR-A 删 grep fallback 后 list-archtests.sh AST 主路径有 bug 导致漏报 | 同 PR 加 `inventory_anchor_required_test.go` 守锚点必现 + 重生 inventory.md 与现状 zero-diff 验证；漂移闸进 CI 兜底 |
| PR-B BFS 实现遗漏注册路径（如 const-ident emission / 闭包包装） | 任务表说明列已列出 4 类注册形态；PR 描述要求覆盖矩阵，reviewer 按矩阵逐项核 |
| PR-A 与 Batch 2 PR 同时 ship 导致 inventory 重复重生冲突 | PR-A 先 merge，后续 worktree 基于 merge 后 develop 创建 |

---

## 6. 引用

- 决策原则：`CLAUDE.md` `## 新增 invariant 决策原则`
- ADR：`docs/architecture/202605061500-adr-typed-response-envelope.md` §D6/D7
- Inventory：`docs/audit/archtest-inventory.md`（自动生成）
- 历史版本（含完整根因 / 主流路线对照 / 取舍记录 / 原 7 切片视图）：git history `1472336b` 之前
