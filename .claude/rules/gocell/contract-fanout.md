# 契约变更扇出闭环

## 触发条件

改动 contract schema、contract.yaml、generated contract、event topic、command key、
HTTP path、auth 语义、consistency level、subscription role 时，必须做扇出检查。

## 必查载体

| 载体 | 必查内容 |
|------|----------|
| contract schema | request/response/payload 字段、required、enum、format |
| generated code | handler、client、types、registration glue |
| cell/slice metadata | `contractUsages`、role、field、verify target |
| journey/fixture | 测试输入和验收路径是否仍匹配 |
| governance/archtest | 是否需要新增或更新机器守卫 |

## 规则

- contract 是跨 cell 通信单源，Go 共享类型不是单源。
- 破坏式 wire 变更走新版本目录。
- generated diff 是一等审查材料。
- 新增 contract kind 或 role 必须补 governance 与 codegen 测试。
- 暂不支持的扇出项必须登记 GitHub Issue，不能写在 rules 中当计划占位。

## 契约归属（cell vs framework）

契约的 owner 是 sealed `metadata.ContractOwner`（`Cell(id) | Framework`）：

- 默认 owner 是某个 **Cell**（`ownerCell: <cellid>` 或经 `endpoints.server/publisher` 派生）。
- **中立、provider-agnostic** 契约（正确性要求 provider 可互换，如设备身份/证书签发）由**框架**归属：
  `ownerCell: _framework`（保留 sentinel）。不绑单一 consumer Cell——对齐 cert-manager/SPIFFE/k8s 范式。
- owner→cell 解析**只经 `c.Owner().Cell()`**（框架 owner 返回 ok=false）；禁止直接 `project.Cells[c.OwnerCell]`。
- 框架归属今仅 http/event；`lifecycle: active` 须在某 `assembly.frameworkContracts` 声明且经 bootstrap `validateFrameworkServing` 验证 RouteGroup 已挂载（serving-scan，#2037）；否则须为 draft|deprecated。

完整机制、威胁矩阵、评级见 ADR `docs/architecture/202606130635-1939-adr-framework-owned-contract.md`，
符号/盲区见 `kernel/metadata/owner.go`、`kernel/governance/rules_framework_owned.go`（FRAMEWORK-OWNED-CONTRACT-SCOPED-01）、
`tools/archtest/contract_owner_cell_funnel_01_test.go`（CONTRACT-OWNER-CELL-FUNNEL-01）。

## Implementation matrix

PR body 或实施计划中列：

| 变更 | contract | generated | cell/slice | tests | docs |
|------|----------|-----------|------------|-------|------|
| ... | ... | ... | ... | ... | ... |
