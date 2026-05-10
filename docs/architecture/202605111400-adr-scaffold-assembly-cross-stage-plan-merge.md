# ADR: SCAFFOLD-ONE-CMD round-6 — Cross-stage plan merge + render/execute split

- **Status**: Accepted (2026-05-11)
- **Roadmap reference**: `docs/plans/202605011500-029-master-roadmap.md` #09 SCAFFOLD-ONE-CMD
- **Related ADRs**:
  - `202605101430-adr-scaffold-one-cmd-double-source-removal.md` — K#09 round-4/5（funnel + Hard 加固）
  - `202605061800-adr-assembly-yaml-minimal-derivation.md` — K#10 minimal `assembly.yaml`

## Context

PR #442 round-4 落地 `pkg/pathsafe.WritePlannedFiles` 单 funnel，scaffold/codegen 写入路径已经全部漏斗化（SCAFFOLD-WRITE-FUNNEL-01，Hard 档）。round-5 又封堵了 leaf-symlink、auto-generate scope、YAML 安全等残留 P1。

但 `gocell scaffold assembly` 命令仍保留**两阶段架构**：

1. **第一阶段**（`Generator.Scaffold`）：写 `assembly.yaml` / `cmd/{id}/run.go` / `cmd/{id}/app.go`，经 `WritePlannedFiles` 单 funnel，含 dry-run + all-or-nothing + rollback。
2. **第二阶段**（CLI 私有 `autoGenerateAssemblyArtifacts`）：再次 `metadata.NewParser(root).Parse()` 读取刚写的 `assembly.yaml`，调用 `Generator.GenerateModulesGen / GenerateEntrypoint / GenerateBoundary`，通过三次独立 `tools/codegen.Write` 顺序写 `cmd/{id}/modules_gen.go` / `cmd/{id}/main.go` / `assemblies/{id}/generated/boundary.yaml`。

这套两阶段架构在第一阶段是漏斗内的，但**跨阶段不一致**：

- `--dry-run` 只打印第一阶段的 3 个文件。`autoGenerateAssemblyArtifacts` 不接受 dry-run 入参，永远写盘——但因为 CLI 的 dry-run 早返回，第二阶段在 dry-run 模式下根本不执行，**计划残缺**。
- live 模式下，第一阶段成功 + 第二阶段任意一处写失败，留下半成品工作树（无跨阶段 rollback）。
- 第二阶段额外做一次 `metadata.NewParser(root).Parse()` re-parse —— 仅仅为了把刚刚写入的 `assembly.yaml` 读回内存。

round-5 的"漏斗化"完成了"写入路径单源"，但"dry-run / live / rollback 计划一致"还差最后一刀。

## Decision

### 1. kernel render / CLI execute 分离

**删除** `Generator.Scaffold(spec) error`。**新增** `Generator.PlanAssemblyScaffold(spec) ([]pathsafe.PlannedFile, error)`：纯 render 函数，返回完整 6-file plan（3 个 skeleton + 3 个 K#10 派生）。

CLI 拿到 plan 后单次调用 `pathsafe.WritePlannedFiles(realRoot, plan, *dryRun)`。dry-run 与 live 共享**同一个 Go 值** `plan`，差异仅在 `WritePlannedFiles` 的 `dryRun bool` 入参。计划一致性由 type system 天然保证——不存在第二条 plan 列表可漂移。

`AssemblyScaffoldSpec.DryRun` 字段从 kernel 完全消失（dry-run 不再是 kernel 域概念）。新字段 `SkipGenerate bool` 控制 plan 是否包含 3 个 K#10 派生文件（`--skip-generate` flag 的 typed 表达）。

### 2. In-memory `AssemblyMeta` 合成 + 消除 re-parse

K#10 三个 `Generate*` 函数原本读 `g.project.Assemblies[id]`。`PlanAssemblyScaffold` 内部用 spec 合成 `metadata.AssemblyMeta`（私有 `synthesizeAssemblyMeta`），临时注入 `g.project.Assemblies[spec.ID]`，调完三个 `Generate*` 后 `defer revert` —— Generator 状态保持 idempotent，文件系统零副作用。

旧 CLI 的第二次 `metadata.NewParser(root).Parse()` re-parse 彻底删除。

### 3. CLI 包袱清理

`cmd/gocell/app/scaffold_assembly.go` 删除 `autoGenerateAssemblyArtifacts` 整个函数（42 LOC）、`tools/codegen` import、第二次 re-parse 调用。文件从 205 行降到 157 行。

### 4. 边界：assembly first-time creation only，cell bundle 保持双路径

assembly scaffold 是 first-time creation 场景（6 个文件都不允许 pre-existing），`pathsafe.WritePlannedFiles` 的 conflict-rejects-existing 完美对应。

但 `scaffold cell` 命令的派生文件路径走 `tools/codegen/contractgen.Generate` / `cellgen.Generate`，它们会同时处理多个 cell 的派生产物（含 regenerate 已存在文件）。如果硬套到 `WritePlannedFiles` 单 plan，regenerate 路径会被 conflict-pass 拒绝。

这是 hard constraint。round-6 限定 assembly 路径（first-time creation funnel），保留 `tools/codegen/writer.go` 的 `WriteFileForce`（overwrite-allowed）作为 regenerate funnel。两条 funnel 都在 `pkg/pathsafe` 单源，archtest `SCAFFOLD-WRITE-FUNNEL-01` 一并守护。

scaffold cell 的"dry-run 不完整 + 跨阶段无 rollback"对称问题登记 backlog `SCAFFOLD-CELL-BUNDLE-CROSS-STAGE-PLAN-MERGE-01`：未来扩 `pathsafe.PlannedFile` 加 `ForceOverwrite bool` 字段把 cell 路径也合并入单 plan funnel（Cx3-4 重构，独立 PR）。

## Consequences

### Positive

- **dry-run 真实反映 live**：6 个文件一次性列出，计划残缺消失。
- **跨阶段 rollback 免费**：单 `WritePlannedFiles` 调用天然 all-or-nothing。
- **CLI 退化为薄壳**：`scaffold_assembly.go` 不再担当 codegen orchestrator 角色，`tools/codegen` import 消失，cmd 层依赖进一步收紧。
- **re-parse 消除**：`metadata.NewParser` 在 scaffold 路径只跑一次（启动时校验 `--cells`）。
- **kernel render-only**：`PlanAssemblyScaffold` 是纯函数（零文件系统副作用），可单元测试无需 tempdir。
- **AI Hard 强保证**：dry-run/live plan 是同一 `[]PlannedFile` Go 值，无需 archtest 防漂移——type system 自然封闭。

### Negative / known carve-outs

1. **`AssemblyMeta` 字段同步风险（Medium）**：`synthesizeAssemblyMeta` 浅复制 spec 字段到 `AssemblyMeta`，未来若 `AssemblyMeta` 加新字段且被 `Generate*` 读取，synthesis 会漏。登记 `ASSEMBLY-META-SYNTHESIS-FIELD-GUARD`（reflect 字段计数 guard 升 Hard 的候选）。当前 grep 已验证三个 `Generate*` 仅读 `Cells / Build.Entrypoint / Build.Binary`，足以支撑 round-6 的合成。

2. **`scaffold cell` 同类问题保留**：cell bundle 的两阶段问题与 assembly 对称但 hard constraint 不同（含 regenerate 语义）。登记 `SCAFFOLD-CELL-BUNDLE-CROSS-STAGE-PLAN-MERGE-01` —— 不 silent carryover。

### Operational

- 没有 deprecation alias / wrapper：`Scaffold(spec) error` 直接删除。项目规则"不向后兼容"。
- `AssemblyScaffoldSpec.DryRun` 字段从 spec 上消失；外部消费者（仅 in-tree 一处 `cmd/gocell/app/scaffold_assembly.go`）已同步改为 CLI 端决策。
- archtest `SCAFFOLD-WRITE-FUNNEL-01` allowlist 无需扩展。删除 `codegen.Write` 在 `scaffold_assembly.go` 的调用后，allowlist 现有 scope 静态零违规。

## Alternatives considered

- **方案 A：保留 `Scaffold(spec)` + 加 `PlanAssemblyScaffold`（双 API）**。被拒。CLI 仍有"是否复用 Plan"的判断，且两 API 实质上做相同事情，违反"优雅简洁"。
- **方案 B：CLI 内部合并 plan，kernel 不动**。被拒。`autoGenerateAssemblyArtifacts` 还在，re-parse 还在，wrapper 没消失；L2 PR 整体不彻底。
- **保留 `AssemblyScaffoldSpec.DryRun`**。被拒。dry-run 是写盘决策，不属于 render 阶段——保留它就是把 kernel 拉回 "execute" 角色，违反 render-only 抽象。

## AI-rebust evaluation

| Defense | Mechanism | 档 |
|---|---|---|
| dry-run/live plan 同源 | 返回值是同一 `[]PlannedFile` Go 值；type system 自然封闭 | **Hard** |
| kernel render-only | `PlanAssemblyScaffold` 纯函数签名 `(spec) ([]PlannedFile, error)`；不接触文件系统 | **Hard** |
| 6-file plan all-or-nothing | `pathsafe.WritePlannedFiles` + SCAFFOLD-WRITE-FUNNEL-01 archtest 联合 | **Hard** |
| `SkipGenerate` typed flag | `AssemblyScaffoldSpec.SkipGenerate bool` 字段；CLI 必须显式传 | **Hard** |
| `synthesizeAssemblyMeta` 字段同步 | godoc 警告 + backlog 升级条目；未来加 reflect 字段计数 guard | **Medium** |

立项硬门槛：≥ Medium。Soft 形态零引入。

ref: `helm/helm pkg/chartutil/create.go` — first-time creation funnel pattern
ref: `kubernetes-sigs/kubebuilder pkg/machinery/scaffold.go` — file-model + single write strategy
ref: `zeromicro/go-zero tools/goctl/api/gogen/gen.go` — render-then-write separation in scaffold orchestrator
