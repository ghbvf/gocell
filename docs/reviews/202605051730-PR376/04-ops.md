# PR #376 Review — 运维/部署维度

PR: feat(codegen): PR-V1-CODEGEN-FULL-MIGRATION (方案 D PR-4 末段)
HEAD: 446930ad / Base: origin/develop

## Summary

- 总体结论: 需修复
- Findings: P0=0 P1=1 P2=4
- Cx: Cx1=4 Cx2=1 Cx3=0 Cx4=0

---

## Findings

### F-OPS-001 [P1] [Cx2] .github/workflows/_build-lint.yml:164-182
**问题**: `verify generated`、`verify-codegen-cell.sh`（K#04）、`verify-codegen-contract.sh`（K#06）三个 verify gate 全部绑在 `if: matrix.static_checks`（即 kernel shard）。当 kernel shard `Test` step FAILURE（PR CI 报告 `build-test (kernel)` FAIL）时，GitHub Actions 默认跳过后续无 `if: always()` 的 step，**三个 verify gate 全部被跳过**，generated/contracts 下 215 个 artifact 的 drift 检测未执行。kernel shard 长期红灯会导致 drift 检测空窗。

**证据**:
```yaml
- name: Verify cell codegen (K#04)
  if: matrix.static_checks
  run: ./hack/verify-codegen-cell.sh
- name: Verify contract codegen (K#06)
  if: matrix.static_checks
  run: ./hack/verify-codegen-contract.sh
```

**建议**: 三步骤改为 `if: always() && matrix.static_checks`，或抽到独立 job（`needs: []`），与 build-test 解耦。

### F-OPS-002 [P2] [Cx1] contracts/event/flag/changed/v1/（已删除）+ assemblies/corebundle/generated/boundary.yaml:2
**问题（确认项，已合规）**: `event.flag.changed.v1` 在本 PR 完全删除（contract + 2 schema），lifecycle 原为 `deprecated`，subscribers: []，无 Go 订阅方。boundary fingerprint 已重新生成（`16566d... → 090be1...`）。删除路径符合"项目无外部消费方"约束。

**建议**: 无需操作。

### F-OPS-003 [P2] [Cx1] .github/workflows/_build-lint.yml:83,237,316,396,433 + governance.yml:29
**问题**: CI 在 6 处硬编 `go-version: "1.25"`，go.mod 声明 `go 1.25.9`。`test-race.yml` / `security-static.yml` 已用 `go-version-file: go.mod`，存在内部不一致。Go 1.26 发布后语义可能漂移。

**建议**: 全部改为 `go-version-file: go.mod`，可与 F-OPS-001 合并一个 commit。

### F-OPS-004 [P2] [Cx1] tools/codegen/contractgen/templates/subscription.tmpl:1-31
**问题**: 生成的 `subscription_gen.go` 不含 slog/OTel 注入，observability 由 `kernel/wrapper.WrapConsumer` 在 `reg.Subscribe` 内统一注入（符合 ADR `202604242030-adr-kernel-wrapper-contract-observability.md`）。设计正确，但模板顶部/`NewSubscription` godoc 未声明此约定，未来维护者可能误向模板添加 slog/span 调用。

**建议**: 在 `NewSubscription` godoc 补充：`// Observability (tracing, metrics, structured logging) is injected by kernel/wrapper.WrapConsumer at reg.Subscribe time; do not add span or slog calls here.`

### F-OPS-005 [P2] [Cx1] CI FAILURE 推测（需人工审查）
**问题**: PR 报告 `build-test (kernel)`、`integration-test`、`e2e` 三个 job FAILURE。推测：
1. **kernel shard**: `kernel/governance/rules_wrapper.go` 删除 ~520 行 + `_test.go` 删除 ~518 行后，kernel coverage 可能跌破 90% gate（`_build-lint.yml:190-212` 的 awk 校验）。
2. **integration/e2e**: `changepassword_e2e_test.go` 改为 `h.RegisterRoutes()`，handler 声明完整路径 `/api/v1/access/users/{id}/password`，与 fixture 的 `mux.Route("/api/v1/access/users", ...)` 前缀组合可能 double-prefix 造成 404。

**证据**:
```go
mux.Route("/api/v1/access/users", func(s cell.RouteMux) {
    if err := h.RegisterRoutes(s); err != nil { panic(...) }
})
```

**建议**: 合并前必须修复 CI 重新 GREEN。需读 CI artifact log 确认根因后定向修复。

---

## go.mod / go.sum 干净度（已确认）
全量 diff 中无 go.mod/go.sum 变化，未引入新外部依赖。

## 复杂度汇总
Cx1: 4 / Cx2: 1 / Cx3: 0 / Cx4: 0

## 修复分流建议
- F-OPS-001 (P1 Cx2) + F-OPS-003 (P2 Cx1) → 派发 developer agent，可合并一个 commit
- F-OPS-004 (P2 Cx1) → 派发 developer agent
- F-OPS-005 → 标注"需人工决策"，需读 CI artifact log

## 总体结论
**需修复**。F-OPS-001 是 P1 必修项，3 个 CI job FAILURE 需人工审 log 后定向修复再重跑。

---

## Out of scope（其他维度）
1. **[测试]** kernel test 删除 ~518 行后需确认 coverage 不跌破 90%
2. **[架构]** `cmd/corebundle/subscription_validator_wiring_test.go:45-57` 保留手动 `wrapper.ContractSpec{}`（测试豁免），应补豁免注释
3. **[安全]** `.golangci.yml` 新增允许 `generated/contracts` import + `generated-contracts-isolation` 规则守卫反向依赖，两规则互补
4. **[可维护性]** `.golangci.yml` `cells-isolation` 注释只引 PR 名而无 ADR 编号，后续追溯困难
5. **[产品]** `cells/accesscore/slices/identitymanage/contract_test.go:448-449` lock 请求需带 `Content-Type: application/json` + `{}` body，但 lock 语义无 body，造成 API UX 问题
