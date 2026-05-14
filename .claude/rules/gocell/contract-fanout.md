# 契约变更扇出闭环

> 契约变更的真值同时活在 5 处载体；任一处漏同步，bug 就在最深的载体暴露。本规则约束扇出必须在同 PR 内被强制同步、被机器验证。

## 触发条件

以下任一变更触发本规则，PR 必须出 implementation matrix：

- Go interface 方法签名 / error sentinel / 返回值 metadata 变化
- contract.yaml endpoint / payload / event schema 变化（含 outbox / event payload v1 → v2 演化）
- DB schema / migration 列约束或语义变化
- errcode 新 Kind / Category / Sentinel

> 不触发：纯内部 helper 签名、未导出类型、CLI flag、observability label 调整。

## 5 个必查载体 + 强制手段

| 载体 | 必须同步 | Enforcement |
|------|---------|-------------|
| 1. interface / schema 定义 | godoc 或 schema doc 写明新约束 | 人审 |
| 2. 全部实现 | 每个实现满足新约束 | conformance test 挂 interface 包，跑遍所有实现；archtest 守"新增实现自动接入 conformance" |
| 3. 各层 test | unit + integration + conformance 三层覆盖 | PR 描述 verbatim 列出失败复现命令；merge gate 跑过才能 close |
| 4. 测试夹具 | fixture seed 满足新契约前提 | fixture diff 是 review 一等公民 |
| 5. 公开 contract / docs | contract.yaml + API schema + ADR 同步 | governance scan：新增 status / error / payload 字段必须能枚举所有受影响 contract（archtest） |

## Implementation matrix 模板

PR 描述强制包含：

```
Contract: <interface or schema>
Change: <one line>
Implementations: [ ] memstore  [ ] PG  [ ] fake
Conformance test: <package.TestName>
Repro: <go test -tags=... -run='...'>
Dependent contracts (governance scan): <list or "none">
```
