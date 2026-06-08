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

## Implementation matrix

PR body 或实施计划中列：

| 变更 | contract | generated | cell/slice | tests | docs |
|------|----------|-----------|------------|-------|------|
| ... | ... | ... | ... | ... | ... |
