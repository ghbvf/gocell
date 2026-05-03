# ADR: DevTools Catalog Wire Format Out of kernel/metadata

- **Status**: Accepted
- **Date**: 2026-05-04
- **Driver PR**: #357 (`feat/164-devtools-catalog`)
- **Supersedes**: 029 master roadmap §Track J 原始落点（`kernel/metadata/export.go`）

## Context

`gocell` 的产品定位是**通用 cell-native 框架**：下游产品基于它构建，会 import `kernel/metadata` 来消费 ProjectMeta 解析模型（`gocell validate`、自定义 governance 工具、自动化脚本等场景）。

PR #357 (J1 PR-A37 + J2 absorb) 初版把 Backstage-Catalog-style wire format（964 行：`Document`/`Entity`/`BuildDocument`/`MarshalDocument`/`IncludeMask`/`ExportOptions`/`PackageDepsView` + 各 kind 的 Spec types）落在 `kernel/metadata/export.go`，理由是 CLI (`gocell export catalog`) 与 HTTP handler (`/api/v1/devtools/catalog`) 双路径共享。

9-agent review 后明确：wire format 留在 kernel 会产生**下游绑架**：
- 任意 import `kernel/metadata` 的下游产品都隐式背上 Backstage schema 演化负担
- Backstage v2 schema 演化 → 强制下游同步迁移，即便它不消费 catalog 端点
- 下游想扩展 catalog 实体类型，被 kernel 内 `Entity.Spec=any` 的设计死锁
- kernel 层的"底座灵魂"定位不应承载对外协议表示层

## Decision

把 wire format **整体迁出** `kernel/metadata`，落在 `runtime/devtools/catalog/`：

- `runtime/devtools/catalog/wire.go` — 所有 wire types
- `runtime/devtools/catalog/build.go` — `BuildDocument(pm *metadata.ProjectMeta, opts ExportOptions) (Document, error)`
- `runtime/devtools/catalog/marshal.go` — `MarshalDocument(doc, format)`
- `runtime/devtools/catalog/redact.go` — `redactStatusBoard`

`kernel/metadata` 只保留 ProjectMeta 解析模型 + parser（核心契约）。

## Why `runtime/devtools/catalog/` 而非其他位置

| 选项 | 否决理由 |
|------|---------|
| `kernel/metadata/export.go`（原方案） | 下游绑架（见 Context） |
| `pkg/catalog/` | `pkg/` 分层规则禁止 import `kernel/`；BuildDocument 接收 ProjectMeta 必须经过抽象接口 → 引入额外间接层，维护成本 > 收益 |
| `contracts/devtools/catalog/v1/` | `contracts/` 是 cell 之间的边界契约，不是 framework 对外协议；定位错配 |
| `runtime/http/devtools/wire/` | wire 与 HTTP handler 同包过紧；CLI (`cmd/`) import HTTP 包语义不洁 |
| `runtime/devtools/catalog/` ✅ | runtime 可 import kernel；CLI (cmd/) 与 HTTP handler 都自然依赖 runtime；下游产品复用 runtime 包正常；与 `runtime/http/devtools/` 同名空间但物理隔离 |

## Concomitant 改动（同 PR 闭环）

1. **`IncludeMask uint8`（bitmask）→ `IncludeOptions struct{Relations,StatusBoard,CellDeps,PackageDeps bool}`**：bitmask 在加 flag 时编译器无提示，struct 字段安全且可扩展
2. **`ExportOptions.Now time.Time` → `Clock clock.Clock` 注入**：与项目 `kernel/clock` 注入模式对齐，可 fake
3. **`PackageDepsView.Status` 字段删除**：build-time 生成永远 ready，`Graph != nil` ↔ ready / `Error != ""` ↔ error，Status 是冗余字段；J2 真正落 lazy load 时再加（roadmap §J2 已 absorb 到 J1，lazy load 不在本次范围）

## Constraints / Guardrails

- **archtest `KERNEL-METADATA-NO-WIRE-01`**（`tools/archtest/kernel_metadata_no_wire_test.go`）静态守卫 `kernel/metadata` 不再含 `Document`/`Entity`/`BuildDocument`/`MarshalDocument`/`IncludeOptions`/`ExportOptions`/`PackageDepsView`/`CellDepGraph`/`CellEdge`/`*Spec` 等 wire 符号或对 backstage 第三方 dep 的 import。新增 wire 类型必须落在 `runtime/devtools/catalog/` 或其同级新包。
- **不留 alias / shim**（CLAUDE.md「Review 和重构时不考虑向后兼容」）：`kernel/metadata.BuildDocument` 等符号直接删除，import 调用点全量改为新包。
- **wrapper.ContractSpec.ID 不变**：`http.framework.devtools.catalog.v1`（ID 是 wire 标识，与代码物理位置解耦）。

## Consequences

### Positive
- 下游 import `kernel/metadata` 不再被 wire format 锁定
- wire schema 演化（v1 → v2）只需改 `runtime/devtools/catalog/`，kernel/metadata 零影响
- runtime/devtools/catalog 可独立升 v2（v1/ + v2/ 子包并存），不破坏 kernel 契约稳定性
- archtest 静态防回退，新人不会无意中把 wire 符号塞回 kernel

### Negative
- `cmd/gocell/app/export.go` 与 `runtime/http/devtools/catalog.go` 的 import 路径变更 — 一次性成本，已在本 PR 完成
- runtime/devtools/catalog 与 runtime/http/devtools 包名相邻易混淆 — 通过命名空间分层（catalog 是 wire / http/devtools 是 handler）和 archtest 不冲突缓解

### Pending Evaluation（推迟）
- `kernel/depgraph.Graph`/`Node` 是否随 wire 一并出 kernel？服务于 cellGraph + packageGraph + 未来其他依赖图消费者，不只 catalog；触发条件 = 出现第三个 depgraph 消费者时启动评估（backlog ARCH-06）

## References

- Driver PR: #357
- Backstage wire model: `backstage/backstage packages/catalog-model/src/entity/Entity.ts@master`
- K8s 分层先例: `k8s.io/api`（wire types）vs `k8s.io/apiserver`（业务）
- Roadmap: `docs/plans/202605011500-029-master-roadmap.md` §Track J
