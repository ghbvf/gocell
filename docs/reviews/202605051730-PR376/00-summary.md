# PR #376 6角色 Review 汇总

PR: feat(codegen): PR-V1-CODEGEN-FULL-MIGRATION (方案 D PR-4 末段)
URL: https://github.com/ghbvf/gocell/pull/376
HEAD: 446930ad / Base: origin/develop
规模: +10745 / -4070, 375 files

## 维度结论

| # | 维度 | 结论 | P0 | P1 | P2 | Cx1 | Cx2 | Cx3 | Cx4 |
|---|------|------|----|----|----|-----|-----|-----|-----|
| 1 | 架构合规 | LGTM/需讨论 | 0 | 1 | 3 | 3 | 0 | 0 | 1 |
| 2 | 安全/权限 | 需修复 | 0 | 2 | 2 | 2 | 0 | 1 | 0 |
| 3 | 测试/回归 | 需修复 | 0 | 1 | 5 | 4 | 2 | 0 | 0 |
| 4 | 运维/部署 | 需修复 | 0 | 1 | 4 | 4 | 1 | 0 | 0 |
| 5 | 可维护性/DX | 需修复 | 0 | 3 | 5 | 5 | 2 | 1 | 0 |
| 6 | 正确性 | 需修复 | 0 | 1 | 3 | 3 | 1 | 0 | 0 |
| **合计** | | | **0** | **9** | **22** | **21** | **6** | **2** | **1** |

## CI 状态（review 时刻）

| Job | 结论 |
|-----|------|
| build-test (kernel) | **FAILURE** |
| integration-test | **FAILURE** |
| e2e | **FAILURE** |
| build-test (runtime/cells/tools/others/os-smoke) | SUCCESS |
| go test -race | SUCCESS |
| make verify (governance strict) | SUCCESS |
| CodeQL / Semgrep / govulncheck | SUCCESS |

3 个 FAIL job 是阻塞合并因素，需结合 F-OPS-005 推测原因（kernel coverage gate / e2e 路径 double-prefix）人工排查。

## P1 阻塞清单（需在合并前处理）

| # | Finding | 维度 | 文件 | Cx | 建议处理 |
|---|---------|------|------|----|----|
| 1 | F-ARCH-001 | 架构 | tools/archtest/no_manual_contractspec_literal_test.go:97 | Cx4 | 人工决策：是否扩展 gate 覆盖 runtime/，或登记 backlog |
| 2 | F-SEC-001 | 安全 | generated/contracts/http/auth/login/v1/handler_gen.go:68 + handler.tmpl | Cx1 | developer：login password 长度 oracle，模板识别 password 字段不暴露 minLength/maxLength |
| 3 | F-SEC-002 | 安全 | kernel/governance + contractgen/builder.go | Cx3 | developer + architect：补 `auth.public + /internal/v1/` 互斥 governance 规则 |
| 4 | F-TEST-001 | 测试 | tools/archtest/event_subscription_contractgen_coverage_test.go:25 | Cx2 | developer：补 `EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01` 负例 fixture |
| 5 | F-OPS-001 | 运维 | .github/workflows/_build-lint.yml:164-182 | Cx2 | developer：3 个 verify gate 改为 `if: always() && matrix.static_checks` |
| 6 | F-DX-001 | DX | tools/codegen/contractgen/doc.go:12 | Cx1 | developer：iface_gen.go 注释与 event 实际产物不符 |
| 7 | F-DX-002 | DX | tools/codegen/contractgen/doc.go:11-13 | Cx1 | developer：补 spec_gen.go / subscription_gen.go 文件列表 |
| 8 | F-DX-003 | DX | tools/archtest/cells_no_wrapper_contractspec_import_test.go:130 | Cx1 | developer：fset_new 注释指向不存在对象 |
| 9 | F-COR-001 | 正确性 | tools/codegen/contractgen/builder.go:69-82 + handler.tmpl | Cx2 | developer：可选 int query param + minimum 在未传值时绕过校验，需先 grep 确认服务层 fallback |

## /fix 派发建议

**派发 developer agent 批处理**（Cx1/Cx2 共 27 条）：
- F-ARCH-002, F-ARCH-003, F-ARCH-004
- F-SEC-001, F-SEC-003, F-SEC-004
- F-TEST-001, F-TEST-002, F-TEST-003, F-TEST-004, F-TEST-005, F-TEST-006
- F-OPS-001, F-OPS-003, F-OPS-004
- F-DX-001 ~ F-DX-007
- F-COR-001 ~ F-COR-004

**需人工决策**（Cx3/Cx4 共 3 条）：
- F-ARCH-001 (Cx4): runtime/ 是否纳入 NO-MANUAL-CONTRACTSPEC-LITERAL-01 扫描
- F-SEC-002 (Cx3): 补 governance 互斥规则
- F-DX-008 (Cx3): README 教程 cell.yaml schema.primary 缺失

**需 CI artifact 排查**:
- F-OPS-005: kernel / integration-test / e2e 三个 FAIL job 根因

## 总体结论

**需修复后再合并。** 无 P0 阻塞，但有 9 条 P1，且 CI 有 3 个 job FAILURE。核心代码质量整体良好（spec.go godoc、archtest 覆盖、dead code 清理彻底、service.go 命名一致），主要修复项集中在：
1. CI 工作流配置（verify gate 被 kernel test fail 遮蔽）
2. 生成的 login handler password 字段长度 oracle
3. 缺少静态守卫规则（public + internal path、event coverage 负例）
4. doc.go 与 event 实际产物的描述不一致
5. 可选 query param 校验缺失

## 各维度详细报告
- [01-architecture.md](./01-architecture.md)
- [02-security.md](./02-security.md)
- [03-testing.md](./03-testing.md)
- [04-ops.md](./04-ops.md)
- [05-dx.md](./05-dx.md)
- [06-correctness.md](./06-correctness.md)
