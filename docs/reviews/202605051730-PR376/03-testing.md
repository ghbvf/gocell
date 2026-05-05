# PR #376 Review — 测试/回归维度

PR: feat(codegen): PR-V1-CODEGEN-FULL-MIGRATION (方案 D PR-4 末段)
HEAD: 446930ad / Base: origin/develop

## Summary

- 总体结论: 需修复
- Findings: P0=0 P1=1 P2=5
- Cx: Cx1=4 Cx2=2 Cx3=0 Cx4=0

---

## Findings

### F-TEST-001 [P1] [Cx2] tools/archtest/event_subscription_contractgen_coverage_test.go:25
**问题**: `EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01` 缺少负例 fixture 测试。同 PR 其他三个新 gate 均有配套负例（`TestCELLS_NO_WRAPPER_CONTRACTSPEC_IMPORT_01_NegativeFixture` 等），唯独此 gate 无负例。若 `contractIDToExpectedPkgPath` 路径映射出错，gate 会永远静默通过。

**证据**: 文件全文 74 行，只有 `TestEVENT_SUBSCRIPTION_CONTRACTGEN_COVERAGE_01` 一个函数，无 NegativeFixture。

**建议**: 参照 `TestCONTRACT_KINDS_CLOSED_SET_01_NegativeFixture` 模式，构造 tmpdir 写入含 `codegen: true` event 合约但不生成 `subscription_gen.go`，断言报错。

### F-TEST-002 [P2] [Cx1] tools/codegen/contractgen/testdata/golden/
**问题**: contractgen golden 文件中 event 类型仅有"含 payload + 含 headers"一种变体。`BuildContractSpec` 对 `schemaRefs.headers` 有可选分支，但无"仅 payload 无 headers"变体的 golden。

**证据**: golden 仅 4 个 event 文件均含 headers；`render_test.go:186` `TestBuildContractSpec_Event_OrderCreated` 断言 `findDTO(spec.DTOs, "Headers") != nil`。

**建议**: 新增 `testdata/synth/synth_event_no_headers/` fixture + 对应 golden + `TestRender_Golden_Synth_Event_NoHeaders`。

### F-TEST-003 [P2] [Cx1] tools/codegen/contractgen/render_test.go:323
**问题**: `TestRender_Golden`、`TestRender_Golden_Synth_HTTPMinimal`、`TestRender_Golden_Synth_HTTPFull`、`TestRender_Golden_Synth_KeywordConflict`（行 323/370/406/573）均未 `t.Parallel()`，与同文件其他测试不一致。

**建议**: 4 个 golden 测试加 `t.Parallel()`（仅读文件系统）。

### F-TEST-004 [P2] [Cx1] tools/codegen/contractgen/templates/subscription.tmpl:28
**问题**: 生成的 `Subscription.Mount` 无并发测试。结构体只读字段不存在 race，但缺少 `-race` 多 goroutine 验证或模板注释说明 immutable value object。

**建议**: 补并发测试或在 tmpl 注释中显式声明"Subscription is immutable after construction; Mount is safe for concurrent use"。

### F-TEST-005 [P2] [Cx2] cmd/corebundle/subscription_validator_wiring_test.go:45
**问题**: 该测试手写完整 `wrapper.ContractSpec{}` 字面量构造"intentionally broken"订阅，但 `cmd/corebundle/` 不在 `NO-MANUAL-CONTRACTSPEC-LITERAL-01` 扫描范围（scanRoots = cells/+examples/），此手写字面量逃过 archtest 检查。

**建议**: A 替换为通过 `entryupserted.NewSubscription(handler, group, slice)` 内部 spec；B 在 archtest 增加 `cmd/` 扫描并加入 `permanentPathExceptionsLiteral`（注明合理负例）。

### F-TEST-006 [P2] [Cx1] docs/guides/cell-development-guide.md:203
**问题**: 第 203 行使用已删除的 `wrapper.EventSpec("event.my.topic.v1", "amqp")` API。开发者照此示例写代码会编译错误。

**证据**:
```go
if err := reg.Subscribe(wrapper.EventSpec("event.my.topic.v1", "amqp"), c.svc.HandleEvent, c.ID()); err != nil {
```

**建议**: 更新为 `<contractpkg>.NewSubscription(c.svc.HandleEvent, c.ID(), c.sliceID()).Mount(reg)`。

---

## 通过项（已验证）

1. **EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01 正向通过**: 15 个 `codegen: true` event 合约均在 generated/ 下存在 subscription_gen.go。
2. **archtest 三个新 gate 负例覆盖完整**: CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01 / NO-MANUAL-CONTRACTSPEC-LITERAL-01 / CONTRACT-KINDS-CLOSED-SET-01 均含 negative fixture。
3. **contractgen golden 完整**: 4 个 todoorder + 4 个 synth fixture 齐全。
4. **sessionlogin service.go 迁移后测试完整**: 5 个测试文件覆盖 Login/IssueForUser/fail-closed/role-fetch/outbox/cleanup。
5. **wrapper.EventSpec 删除无死引用**: 仅出现在测试断言字符串和文档中。
6. **CH-04/CH-05 回归测试**: `rules_http_response_alignment_test.go` 与 `rules_http_pathparam_uuid_test.go` 完整。
7. **scaffold golden**: `scaffold_golden_test.go` 验证 7 个必需模式。
8. **cellgen / contractgen 单测覆盖**: 30+ table-driven，含 t.Parallel()，golden+drift+干运行+幂等。
9. **subscription.tmpl 与 golden 内容一致**: 完全匹配。

---

## 复杂度汇总
Cx1: 4 / Cx2: 2 / Cx3: 0 / Cx4: 0

## 修复分流建议
全部派发 developer agent（Cx1/Cx2 均为局部改动）。

---

## Out of scope（其他维度）
1. **[架构]** `cell-development-guide.md` ~185 行还有手写 `wrapper.ContractSpec{...}` 字面量示例
2. **[架构]** `.golangci.yml` 新增 `generated/contracts` 到 cells 隔离规则，但 `example-cells-isolation-todoorder` 段落未同步
3. **[架构]** `cmd/corebundle/subscription_validator_wiring_test.go:41` 直接 `reg.Subscribe(wrapper.ContractSpec{...}, ...)`，与 spec.go 文档"Cells MUST NOT construct ContractSpec literals"精神矛盾
4. **[运维]** `files.txt` 列出 `contracts/event/flag/changed/v1/contract.yaml`，但该文件在 worktree 不存在（已删除）—— 已在 OPS 维度证实
5. **[DX]** `kernel/governance/rules_wrapper_test.go` 顶部注释说 FMT-18 测试已删但保留 25 case `TestValidateFMT19WrapperPackageState`，混布注释可能误导首次阅读者
