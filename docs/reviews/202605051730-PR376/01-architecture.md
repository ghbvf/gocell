# PR #376 Review — 架构合规维度

PR: feat(codegen): PR-V1-CODEGEN-FULL-MIGRATION (方案 D PR-4 末段)
HEAD: 446930ad / Base: origin/develop
规模: +10745 / -4070, 375 files

## Summary

- 总体结论: LGTM（1 个 P1 需要决策，其余为 P2 建议）
- Findings: P0=0 P1=1 P2=3
- Cx: Cx1=3 Cx2=0 Cx3=0 Cx4=1

---

## Findings

### F-ARCH-001 [P1] [Cx4] tools/archtest/no_manual_contractspec_literal_test.go:97-143
**问题**: `NO-MANUAL-CONTRACTSPEC-LITERAL-01` 明确将扫描根限定为 `cells/` 和 `examples/`，并在注释中声明：

> "Scope: cells/ and examples/**/cells/ only. runtime/ and kernel/ own framework-internal ContractSpec usages that are intentional and not subject to this migration gate."

这意味着 `runtime/` 层未来若新增手写 `wrapper.ContractSpec{}` 字面量不会被任何 gate 拦截。目前 `runtime/` 中合理的手写字面量（`runtime/auth/route_test.go`、`runtime/eventrouter/test_contract_test.go`）均为测试文件，但 production 文件（`runtime/eventrouter/router.go`、`runtime/auth/route.go`）import `kernel/wrapper`——它们目前没有字面量，但无静态 gate 守护这个不变量。

**证据**:
```go
scanRoots := []string{
    filepath.Join(root, "cells"),
    filepath.Join(root, "examples"),
}
```

**建议**: 标注"需人工决策"。如果项目认为 runtime/ 层管控属于未来 backlog 触发项（当前 runtime/ 无手写字面量，仅测试文件有），可接受现状并登记 backlog；如果需要完整覆盖，需 architect 评估扩展 gate 范围的边界效应。

### F-ARCH-002 [P2] [Cx1] tools/archtest/event_subscription_contractgen_coverage_test.go:69-73
**问题**: 文件末尾 godoc 风格注释悬空（无后续函数体）：声明 `contractIDToExpectedPkgPath` 来自 `codegen_contract_gen_test.go`，但截断的写法让读者误认为该文件中有完整定义。

**证据**:
```go
// contractIDToExpectedPkgPath (from codegen_contract_gen_test.go, same package)
// converts a contract ID to expected filesystem path, e.g.:
//
//    "event.session.created.v1"        → "generated/contracts/event/session/created/v1"
```

**建议**: 改为 `// See contractIDToExpectedPkgPath in codegen_contract_gen_test.go.` 一行即可。

### F-ARCH-003 [P2] [Cx1] tools/codegen/contractgen/builder.go:556-571
**问题**: `internalapi` 路径重写注释清楚，但 CLAUDE.md 对 `generated/` 层的路径约定没有明确记录"`internal/` 段在 generated/ 中映射为 `internalapi/`"。archtest 的 `contractIDToExpectedPkgPath` 已同步该规则，合规性没有问题。

**建议**: 在 CLAUDE.md 或 `docs/guides/codegen-new-endpoint.md` 中补充一行说明，避免未来开发者命名新 internal contract 时困惑。

### F-ARCH-004 [P2] [Cx1] tools/codegen/contractgen/generator.go:122-129
**问题**: event kind 的 `ContractSpec` 在独立 `spec_gen.go`（变量 `spec`，private），http kind 的内嵌在 `handler_gen.go`（变量 `contractSpec`，private），两种 kind 不对称。当前不是架构违规，但若未来 http 需要在其他 gen 文件中访问 spec，可能需要重构。

**建议**: 当前实现合理（http spec 仅 handler 需要，event spec 需要被 subscription 引用），不需要立即改动。

---

## 详细核查通过项

1. **分层依赖方向完全合规**: kernel/ 生产代码无 runtime/adapters/cells import；cells/ 生产代码无 adapters import（8 个引用全在 `//go:build integration`）；runtime/ 生产代码无 cells/adapters import。
2. **wrapper.EventSpec 删除干净**: `kernel/wrapper/spec.go` 中已无 `EventSpec` 函数，仅 godoc 第 24 行有迁移说明。
3. **kernel/wrapper.ContractSpec godoc 稳定且清晰**: spec.go:8-59 完整说明使用约束、archtest gate 名称、EventSpec 迁移路径、零值无效语义。
4. **internalapi/ 路径重写正确**: `contractIDToPackagePath` 与 `contractIDToImportPath`（cellgen）逻辑一致；archtest `contractIDToExpectedPkgPath` 同步重写。
5. **4 个新 archtest gate 设计合理**: 全部 allowlist 为空；CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01 / NO-MANUAL-CONTRACTSPEC-LITERAL-01 含负向 fixture；EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01 通过元数据驱动；CONTRACT-KINDS-CLOSED-SET-01 含负向 fixture。
6. **cells/ 无手写 ContractSpec 字面量**: 仅 `cells/accesscore/slices/identitymanage/test_contract_test.go` 命中（测试文件，integration build tag）。
7. **cell_gen.go 订阅注册模式正确**: 均使用 `subN.NewSubscription(...).Mount(reg)`，与 eventbus.md 「Registry builder 模式」吻合。
8. **contract v1 schema 扩展合规**: `auth.public` / `passwordResetExempt` 均为 `omitempty` optional 字段，符合 api-versioning v1 只增不删。
9. **eventbus.md 三处声明对齐已验证**: 抽查 `configreceive` slice 的 contractUsages / endpoints.subscribers / verify.contract 三处均闭环。
10. **Cell 聚合边界正确**: 无跨 Cell internalapi/ 直接 import；configreceive 通过 HTTP client adapter 调用，符合接口解耦。

---

## 复杂度汇总
Cx1: 3 / Cx2: 0 / Cx3: 0 / Cx4: 1

## 修复分流建议
- F-ARCH-002/003/004 (Cx1) → 派发 developer agent
- F-ARCH-001 (Cx4) → 标注"需人工决策"

## 总体结论
**需讨论（F-ARCH-001）**，其余为建议改进。核心迁移目标（cells/ 切换到 generated ContractSpec、EventSpec 函数完整删除、internalapi/ 绕过 Go internal package rule）均已达成，架构守护 gate 设计合理且无后门。

---

## Out of scope (其他维度可能关注)
1. **[DX]** `cells/accesscore/slices/identitymanage/test_contract_test.go` 集成测试中手写 `wrapper.ContractSpec{}`，未来如有同类 e2e 需求建议统一成 generated 包引用
2. **[运维]** 待确认 `event.config.entry-deleted.v1` contract.yaml 的 subscribers 字段是否包含 `configcore`
3. **[DX]** `tools/codegen/cellgen/builder.go:298` 硬编 `Transport: "amqp"` 而非从 contract.yaml 读取，未来非 amqp event transport 需要修改 builder
4. **[测试]** `EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01` 仅验证文件存在 + `func NewSubscription` 字符串，未验证内容与 contract.yaml 的幂等性（drift 由 `gocell generate --verify` 承担）
5. **[安全]** `generated/contracts/http/config/internalapi/get/v1/handler_gen.go` 中 `contractSpec.Clients` 来自 contract.yaml，handler_gen 无独立 spec_gen，未来跨文件访问会重复
