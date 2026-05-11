# PR #442 — 文档工程师维度审查

## 总体结论

文档完整性整体良好，ADR 结构标准、godoc 覆盖充分、backlog 条目登记认真。主要缺陷集中在三处：(1) ADR 文件命名不符合项目编号惯例；(2) `scaffold_bundle_test.go` 含 2 条 INVARIANT 但文件名未使用 `{rule}_test.go` 单规则形式；(3) README.md 未同步 K#09 新增的 `scaffold cell` 完整 bundle 行为与 `scaffold assembly` 子命令；(4) roadmap `202605101839-029-master-roadmap.md`（工作区未跟踪文件）仍显示 K#09 为"待办"，与已合入的 `202605011500-029-master-roadmap.md` 中的 ✅ done 状态不一致。

---

## Finding 列表

### F1 [Cx1] ADR 文件名缺编号字段

**位置**：
- `docs/architecture/202605101430-adr-scaffold-one-cmd-double-source-removal.md`
- `docs/architecture/202605111400-adr-scaffold-assembly-cross-stage-plan-merge.md`

**问题**：CLAUDE.md 规定文档命名格式为 `yyyyMMddHHmm-编号-实际功能或问题.md`。项目中唯一带编号字段的 ADR 是 `202604252235-001-metrics-provider-abstraction-in-kernel.md`，所有其他 ADR（共 20+ 个）均采用 `yyyyMMddHHmm-adr-{topic}.md` 形式，没有数字编号字段。

**证据**：`ls docs/architecture/` 输出显示，除 `202604252235-001-*` 外，全部 ADR 都是 `*-adr-*` 命名，无编号。CLAUDE.md 例示 `202603281443-022-compliance-api-review.md` 含编号，但 ADR 子目录的实践已偏离该格式且全仓一致沿用 `*-adr-*` 形式。

**建议**：项目 ADR 文件命名实践事实上是 `yyyyMMddHHmm-adr-{topic}.md`，与 CLAUDE.md 示例存在长期偏差。两个新 ADR 的命名与已有 ADR 保持一致，无需修改。但应在 CLAUDE.md 或 `docs/architecture/` 的 README 中说明 ADR 文件名例外规则（`adr` 替代编号字段），以消除歧义，避免后续 AI 实例重复不一致判断。

**Backlog 登记**：`DOC-ADR-NAMING-CONVENTION-CLARIFY-01` — 在 `docs/architecture/README` 或 CLAUDE.md 注明 ADR 命名惯例偏离示例格式的原因，建议 P3/Cx1，来源：PR#442 doc-engineer review。

---

### F2 [Cx1] `scaffold_bundle_test.go` 含双 INVARIANT 但未使用 invariants 命名

**位置**：`tools/archtest/scaffold_bundle_test.go`

**问题**：ai-collab.md 规定"单条独立规则 → `{rule}_test.go`"，"同主题规则 ≥ 3 → `{theme}_invariants_test.go`"。该文件含两条 INVARIANT（`SCAFFOLD-BUNDLE-MARKER-01` 和 `SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01`），属于 2 条同主题规则的中间状态：按规则不满足 ≥3 的阈值，但"单条独立规则 → 单文件"的语义也不适用（已含 2 条）。

**证据**：文件头注释：
```
// invariants asserted in this file:
//   - INVARIANT: SCAFFOLD-BUNDLE-MARKER-01
//   - INVARIANT: SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01
```
ai-collab.md 原文："已有单文件升到第 3 条时，重命名为 `{theme}_invariants_test.go`"。目前两条，命名规则无明确覆盖此场景。

**建议**：在 PR 合并时保持现状是合理的（未触发 ≥3 重命名阈值）。但若后续新增第 3 条 scaffold-bundle 相关 INVARIANT（如 `SCAFFOLD-INLINE-TEMPLATE-ARCHTEST` backlog 条目落地），需立即重命名为 `scaffold_bundle_invariants_test.go`。建议在 backlog 条目 `SCAFFOLD-INLINE-TEMPLATE-ARCHTEST` 的描述中加注"落地时触发 `scaffold_bundle_test.go` → `scaffold_bundle_invariants_test.go` 重命名"。

**Backlog 登记**：在既有 `SCAFFOLD-INLINE-TEMPLATE-ARCHTEST` 条目补注：落地时同步重命名 `scaffold_bundle_test.go` → `scaffold_bundle_invariants_test.go`（第 3 条触发）。

---

### F3 [Cx2] README.md 未同步 K#09 新增的 `scaffold` 行为

**位置**：`README.md`（第 347 行仅提及 `scaffold` 作为 CLI 子命令之一）

**问题**：README.md 的"Developer Toolchain"/"CLI"部分只列出了 `gocell validate / scaffold / generate / check / verify`，没有描述 K#09 新增的核心体验：(1) `gocell scaffold cell --id=<id>` 现在产出完整 bundle（cell + slice + contract + JSON schemas + auto-generate），(2) 新增 `gocell scaffold assembly` 子命令，(3) 新的 `--with-http/--with-events/--with-both/--skip-generate` flag 集合。新人阅读 README 后无法发现这些能力。

**证据**：`grep -n "scaffold\|K#09" README.md` 显示第 347 行仅有一处引用，无任何使用示例或 flag 说明。backlog 条目 `SCAFFOLD-KEBAB-MIGRATION-DOC`（F11）已明确要求 README 补充 kebab ID 迁移说明，证明 README 同步本就是待办项。

**建议**：在 README.md 的 CLI 工具链说明中补充 scaffold 使用示例，至少覆盖：
- `gocell scaffold cell --id=mycore --team=platform --role=backend --with-http` 一键产出说明
- `gocell scaffold assembly --id=ssocore --cells=accesscore,auditcore --team=platform --role=backend` 说明
- dry-run 行为说明（--dry-run 仅打印计划，不写盘）

**Backlog 登记**：可并入既有 `SCAFFOLD-KEBAB-MIGRATION-DOC` 条目（同 README 改动），或单独登记 `SCAFFOLD-README-QUICKSTART-01`（P2/Cx1）。

---

### F4 [Cx1] 未跟踪的新 roadmap `202605101839-029-master-roadmap.md` 中 K#09 状态为"待办"

**位置**：`docs/plans/202605101839-029-master-roadmap.md`（工作区 untracked 文件）

**问题**：该文件是工作区未提交的新 roadmap 草稿，其中 K#09 仍显示"待办"和原始工时估算，与 PR#442 已合入的 `docs/plans/202605011500-029-master-roadmap.md` 中的 `✅ done` 状态不一致。该文件为 git untracked，不属于 PR 的产物，但若后续提交此文件，会造成 K#09 状态回退混乱。

**证据**：`git status` 显示 `?? docs/plans/202605101839-029-master-roadmap.md`，内容显示 K#09 为"待办"，而 `git show HEAD:docs/plans/202605011500-029-master-roadmap.md` 确认 K#09 已是 ✅ done。

**建议**：提交该文件前，将 K#09 状态从"待办"更新为 ✅ done，并同步引用 PR#442 合入链接。同时核查文件中所有其他已在 HEAD 中完成的条目状态是否与 develop 对齐。

**Backlog 登记**：无需单独登记，属于本次 roadmap 归档操作的必要校对步骤。

---

### F5 [Cx1] ADR 第一篇中 Roadmap 引用路径在归档后可能失效

**位置**：`docs/architecture/202605101430-adr-scaffold-one-cmd-double-source-removal.md` 第 2 行 `Roadmap reference: docs/plans/202605011500-029-master-roadmap.md`

**问题**：git status 显示 `202605011500-029-master-roadmap.md` 正在被 `R`（rename）移至 `docs/plans/archive/202605011500-029-master-roadmap.md`。ADR 中的引用路径在归档完成后将指向不存在的路径。

**证据**：`git status` 输出：
```
R  docs/plans/202605011500-029-master-roadmap.md -> docs/plans/archive/202605011500-029-master-roadmap.md
```
ADR 内容中 `Roadmap reference: docs/plans/202605011500-029-master-roadmap.md`。

**建议**：提交归档操作时，同步将 ADR 中的 Roadmap reference 更新为 `docs/plans/archive/202605011500-029-master-roadmap.md`，或改为引用新 roadmap `202605101839-029-master-roadmap.md` 中的相应行。第二个 ADR（cross-stage-plan-merge）同样引用了该路径，需一并更新。

**Backlog 登记**：无需单独登记，属于归档操作的配套文档更新。

---

### F6 [Cx1] `help.go` scaffold assembly 入口缺少 `--deploy` 枚举值说明

**位置**：`cmd/gocell/app/help.go` 的 `scaffold assembly` help 条目

**问题**：help 文本中 `--deploy=k8s|compose|binary` 格式正确，但没有说明"默认值 k8s"以及"--deploy=k8s 时 assembly.yaml 不写 deployTemplate 字段"的设计意图。新人使用时无法从 help 推断默认行为。

**证据**：help.go scaffold assembly 条目：
```
"[--deploy=k8s|compose|binary] [--skip-generate] [--dry-run]",
```
ADR `202605101430` 明确说明 `--deploy=k8s` 是默认值且 yaml 中省略该字段，但 help text 中无任何提示。

**建议**：改为 `[--deploy=k8s(default)|compose|binary]` 或在括号内注明"k8s omits deployTemplate in yaml"。这是文档层的 Cx1 改动，不影响代码逻辑。

**Backlog 登记**：可并入既有 `SCAFFOLD-HELP-COMPLETE` 条目（P2/Cx1）。

---

### F7 [Cx1] ADR 交叉引用完整，但第二篇 ADR 未引用 K#06 相关 ADR

**位置**：`docs/architecture/202605111400-adr-scaffold-assembly-cross-stage-plan-merge.md`

**问题**：第一篇 ADR（202605101430）已引用了三个相关 ADR，交叉引用完整。第二篇 ADR 只引用了第一篇和 K#10 ADR，但其 "In-memory AssemblyMeta synthesis" 决策与 K#04（CellMeta 单源化）密切相关，缺少对 `202605051300-adr-kernel-cellmeta-single-source.md` 的引用说明。

**证据**：ADR 第二篇 Related ADRs 段落仅列 2 项，而 K#04 单源化正是 `synthesizeAssemblyMeta` 浅复制模式的架构前提。

**建议**：在 Related ADRs 补充 `202605051300-adr-kernel-cellmeta-single-source.md`，并说明 synthesizeAssemblyMeta 浅复制假设 AssemblyMeta 字段集稳定的基础来自 K#04 数据层单源化。这是文档质量的 Cx1 改动。

**Backlog 登记**：无需单独登记，属于 ADR 完整性的 Cx1 follow-up，可下次触及该文件时顺带修复。
