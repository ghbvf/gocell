# PR #442 — Architect 维度审查

## 总体结论

**有条件通过**。PR 在分层结构（删除 kernel/scaffold + 把 codegen 单源化到 tools/codegen）和接口稳定性（sealed `contractgen.Scope`、Plan-then-Execute）两个维度上是干净的架构提升。两条具体异议：

1. `kernel/assembly.PlanAssemblyScaffold` 返回 `[]pathsafe.PlannedFile` 让 kernel 持有 "scaffold 写入计划" 这一领域语义——尽管 pkg/ 在分层规则上属下层，但 kernel 直接持有 PlannedFile 让它承担了它之前刚被移走的职责；
2. `SCAFFOLD-BUNDLE-MARKER-01` / `NO-CODEGEN-LITERAL-01` 被 PR body 自承 "Cannot be Hard"，把升 Hard 任务延迟到 `SCAFFOLD-BUNDLE-ARCHTEST-HARDEN` backlog——这是 `ai-collab.md` 明确禁止的 Soft 新引入 carry-over，应当场升 Medium 或撤回。

## Finding 列表

### F1 [Cx3] kernel/assembly 持有 scaffold 写入计划语义，职责回流
**位置**：`kernel/assembly/generator.go`（PR442 新增 `PlanAssemblyScaffold`）
**问题**：`Generator.PlanAssemblyScaffold(spec) ([]pathsafe.PlannedFile, error)` 是 "render-only pure function" 返回 `pathsafe.PlannedFile`。从分层规则看 pkg/ 在 kernel/ 之下，import 方向合法；但语义层面 PlannedFile 是 "scaffold/codegen 写入计划"——`kernel/scaffold` 被删除的初衷就是把这种"生成"语义剥离出 kernel。现在 kernel 重新承担 "render scaffold plan" 的领域概念，只是把执行 (`os.WriteFile`) 推给 caller。职责 50% 回流。
**证据**：PR body Round-6 节 "extends `kernel/assembly.Generator` with `PlanAssemblyScaffold/Scaffold`，演化为 render-only pure function"；同时 PR body Summary 节声称 kernel/scaffold 全删 (~400 LOC)。
**建议**：要么把 `PlanAssemblyScaffold` 整体下移到 `tools/codegen/assemblygen`（与 cellgen 同层），让 kernel/assembly 只保留运行期/构建期 derivation（`GenerateEntrypoint/Boundary/ModulesGen`）；要么在 ADR 中显式裁决 "render plan 属于 kernel/assembly 的合法扩展，因为它复用 ProjectMeta + 已有 Generator"——但此时 "kernel/scaffold 删除把 scaffold 移出 kernel" 的 framing 不再准确，应改为 "把 hand-written 模板移出 kernel，render plan 留在 kernel"。当前 PR body 两种 framing 共存，概念模型不一致。
**AI-rebust 评级**：不涉及 enforcement 机制。
**Backlog 登记**：`ARCH-ASSEMBLY-SCAFFOLD-OWNERSHIP-01` —— 裁决 scaffold render 阶段是否属于 kernel/assembly。

### F2 [Cx4] SCAFFOLD-BUNDLE-MARKER-01 / NO-CODEGEN-LITERAL-01 Soft carry-over 违反立项门槛
**位置**：`tools/archtest/scaffold_bundle_*_test.go`
**问题**：PR body Reflection 节自承 "Medium × 2 (real-source AST capture; marker 是 hand-written string in text/template，no type-system enforcement available)"，并把升级写入 backlog `SCAFFOLD-BUNDLE-ARCHTEST-HARDEN`。根据 `.claude/rules/gocell/ai-collab.md`「Soft 形态严禁立项；新引入 Soft → 直接 reject，要求改 ≥ Medium」。这是**新引入**的 enforcement，不是既有 Soft 的补丁。
**证据**：ai-collab.md "Review checklist"；PR body 自承认 archtest 评级和 backlog 安排。
**建议**：合入后必须在 follow-up PR 升 Medium —— 例如把 marker 检查转为 typed marker function call（`scaffoldbundle.RegisterBundleArtifact(...)` typed funnel + scanner 强制 caller 是 cellgen 内部包），违反者编译期失败；或撤回这两个 archtest，依赖已 Hard 的 `SCAFFOLD-WRITE-FUNNEL-01` + `SCAFFOLD-CONTRACT-CODEGEN-DEFAULT-TRUE` 兜底。**不接受 silent carry-over**。
**AI-rebust 评级**：Soft（违反规则）。
**Backlog 登记**：`SCAFFOLD-BUNDLE-ARCHTEST-HARDEN` 应标记为 PR442 follow-up blocker，不是普通演进项。

### F3 [Cx2] SCAFFOLD-WRITE-FUNNEL-01 Hard 评级证据不足，更接近 Medium
**位置**：`tools/archtest/scaffold_write_funnel_test.go`
**问题**：PR body 说该 archtest 是 "scanner AST + concrete-package allowlist"，"Bypass requires (a) 改 allowlist + (b) 再加 os.* call"。ai-collab.md 对 Hard 的定义是「违反不可表达」（codegen funnel / type system / sealed interface）。"两步可绕过且两步都 visible in diff" 是 Medium 的典型形态：违反可表达，只是有 archtest 阻挡。kernel-guardian agent 已在 F5 中独立确认此判断（`HasPrefix("scaffold")` 字符串匹配）。
**证据**：ai-collab.md AI-rebust 三档表："archtest by string anchor" = Soft；"archtest type-aware" = Medium。当前形态介于二者之间。
**建议**：把 `os` 包对 `tools/codegen/...` + `kernel/assembly/...` 的 import 走 `.golangci.yml` depguard 路径级 ban（除已注册 allowlist 文件），让违反者直接 build/lint 失败而非 archtest run-time 失败。组合后总评级 Hard 才成立。
**AI-rebust 评级**：当前 Medium，目标 Hard（path-level depguard）。
**Backlog 登记**：`SCAFFOLD-WRITE-FUNNEL-DEPGUARD-01`。

### F4 [Cx2] sealed `contractgen.Scope` 设计 Hard 但 `OnlyContract` 删除验证缺口
**位置**：`tools/codegen/contractgen/scope.go` / `tools/codegen/contractgen/generator.go`
**问题**：sealed scope marker（unexported `contractScope()` 方法）是教科书 Hard 模式，评级成立。但同时 PR body 第 2 节提到 `OnlyContract` 字段被删除——`OnlyContract` 是布尔字段，删除后所有调用方必须迁到 `Scope: ScopeContracts`。PR body 未交代 `OnlyContract` 是否还有 in-tree 调用方，是否做了全替换。
**证据**：PR body Round-5 节 "`Options.OnlyContract` field deleted"；项目规则「Review 和重构时不考虑向后兼容」支持硬删除。
**建议**：reviewer 在 develop 上 `grep -rn "OnlyContract" cmd/ cells/ examples/` 核验已无残留。建议同时增 archtest `CONTRACTGEN-OPTIONS-FIELDS-FROZEN-01` 用 `typeseval` 校验 `Options` struct 字段集，防止 ad-hoc 重新添加。
**AI-rebust 评级**：sealed scope = Hard；field-frozen archtest = Medium（typeseval-based）。
**Backlog 登记**：`CONTRACTGEN-OPTIONS-FIELDS-FROZEN-01`。

### F5 [Cx3] Round-6 cross-stage plan merge 只解决一半，cell bundle backlog 化的概念裂缝
**位置**：`kernel/assembly/generator.go` + `tools/codegen/cellgen/scaffold.go`
**问题**：PR body Round-6 节称把 assembly 从「写 skeleton + autoGenerateAssemblyArtifacts 二次写」合并为「单一 render → execute」，但同类 cell bundle 仍是两阶段，靠 `SCAFFOLD-CELL-BUNDLE-CROSS-STAGE-PLAN-MERGE-01` backlog 跟踪。backlog 登记动作合规，但**架构裁决一致性**有问题：assembly 与 cell bundle 是结构同构问题（render→execute 两阶段合并），一个 PR 内只解决一半会让"为什么 assembly 必须现在改、cell 可以缓"成为概念模型缝隙。
**证据**：PR body Round-6 节 "assembly first-time creation funnel ≠ cell bundle regenerate funnel"，但未给出二者不同步裁决的概念原因。
**建议**：ADR `202605111400` 明确写清"cell bundle 两阶段在当前 contractgen 依赖关系下是必要的"+给出 cell bundle 单阶段化的前置条件（需 `pathsafe.PlannedFile` 加 `ForceOverwrite bool`）；或把 cell bundle 单阶段化纳入后续 PR 的强约束 trigger。否则用户视角 ："为什么 scaffold cell 是两次写盘，scaffold assembly 是一次写盘？" 仍是开放概念缝隙。
**AI-rebust 评级**：不涉及。
**Backlog 登记**：`SCAFFOLD-CELL-BUNDLE-CROSS-STAGE-PLAN-MERGE-01` 已存在；需补 ADR 段落解释二者裁决差异的概念原因。

### F6 [Cx1] ADR 覆盖完整性
**位置**：`docs/architecture/202605101430-adr-scaffold-one-cmd-double-source-removal.md` + `docs/architecture/202605111400-adr-scaffold-assembly-cross-stage-plan-merge.md`
**问题**：两份 ADR 概念上互补（前者解决"代码生成的双源"，后者解决"两阶段 vs 单阶段"）。但 PR body 提到的两大架构裁决——(a) `kernel/scaffold` 整层删除、(b) `ContractMeta.Codegen` 默认 false→true 翻转——前者应在 ADR 202605101430 明确记录"为什么 scaffold 不属于 kernel"，后者应有独立段落记录"默认翻转的回滚策略"。doc-engineer agent 独立确认 ADR 内容质量高且 K#04 ADR 交叉引用缺失（F7）。
**证据**：doc-engineer 报告 F7 同向独立确认。
**建议**：补 ADR 202605111400 对 K#04 数据层单源化 ADR `202605051300-adr-kernel-cellmeta-single-source.md` 的交叉引用。
**AI-rebust 评级**：不涉及。
**Backlog 登记**：`ADR-202605111400-AMEND-K04-CROSSREF`。

---

## 优先级裁决

| Finding | 优先级 | 阻塞类型 |
|---------|--------|---------|
| F2 | **P0** | Soft archtest 新引入违反 ai-collab.md；merge 后须 follow-up PR 升 Medium |
| F1 | P1 | 概念模型裂缝；ADR framing 需修正 |
| F3 | P1 | Hard 评级偏高一档；低成本可通过 depguard 升真 Hard |
| F4 | P2 | 核验性建议；可通过 grep + typeseval archtest 闭环 |
| F5 | P2 | ADR 补充段落即可；cell bundle 单阶段化已 backlog |
| F6 | P2 | ADR 交叉引用补全 |
