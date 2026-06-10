# Saga 引擎规则

本文件只保留当前行为约束。完整盲区、符号清单、评级证明写在
`tools/archtest/saga_invariants_test.go`、`kernel/governance/rules_saga.go`、
saga ADR 和 runbook 中。

## 架构语义

使用 saga 编排意味着 L3；L3 不等价于 saga。投影型、CQRS 型最终一致可以是 L3
但不使用 saga 引擎。

## Governance

`kind: saga` contract 必须：

- 有非空 `saga:` block。
- 至少一个 step。
- step name 可生成 Go 标识符且唯一。
- 每个 step 声明 output schema ref。
- compensation order 只能是 reverse。
- consistency level 为 L3。
- retry 和 timeout 是合法非负 duration。

slice 使用 `role: orchestrate` 时，所属 cell 必须声明 L3。

## 构造器

`runtime/saga` 和 `runtime/saga/executor` 的必填 interface 位置参必须经
`validation.IsNilInterface`。`clock.Clock` 必须经 `clock.MustHaveClock`。

## 参考

- ADR：`docs/architecture/202606021000-adr-saga-l3-orchestration-engine.md`
- Runbook：`docs/ops/saga-runbook.md`
- 扇出规则：`.claude/rules/gocell/contract-fanout.md`
