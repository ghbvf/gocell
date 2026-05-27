# archtest 不变式编目 A — 引擎自检 / meta-archtest / 分层 import 规则

本文件汇总 `tools/archtest/` 下与「引擎自检、meta-archtest、分层 import」主题相关的源文件中
所有去重 INVARIANT ID，分析其评级、机制类型、gocell 耦合度与可移植性。

覆盖源文件（共 19 个）：
`archtest_test.go`, `pgquery_boundary_test.go`, `archtest_ci_shard_count_test.go`,
`archtest_invariants_coverage_test.go`, `archtest_verify_coverage_test.go`,
`helpers_test.go`, `testmain_test.go`, `pass_test.go`, `pass_funnel_test.go`,
`eval_predicate_centralization_test.go`, `implements_funnel_test.go`,
`scanner_framework_usage_test.go`, `reflect_string_arg_test.go`, `golden_test.go`,
`production_loader_funnel_test.go`, `taggroup_loop_no_runtyped_test.go`,
`build_constraint_test.go`, `inventory_anchor_required_test.go`,
`kernel_internal_dag_test.go`

**本文件 30 条 | Hard 12 / Medium 17 / Soft 0 (推断 1) | 通用 11 / 半通用 12 / 专属 7**

---

### 批次 1 — 分层 import 规则（LAYER-*）

| INVARIANT ID | 评级 | 机制类型 | 用途(≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| LAYER-05 | Medium (推断) | import-ban | 禁止 cells 跨 Cell 直接 import 对方的 internal/ 包 | kernel/depgraph, cells/ | 半通用 |
| LAYER-05T | Medium (推断) | import-ban | LAYER-05 的传递闭包变体，拦截 utility 中介绕行 | kernel/depgraph, cells/ | 半通用 |
| LAYER-06 | Medium (推断) | import-ban | cell 自有公开子包仅限本 cell / cmd / examples 引用 | kernel/depgraph, cells/ | 半通用 |
| LAYER-06T | Medium (推断) | import-ban | LAYER-06 传递闭包变体，捕获多跳绕行 | kernel/depgraph, cells/ | 半通用 |
| LAYER-07 | Medium (推断) | import-ban | 禁止 cells/ 直接 import runtime/http/router | cells/, runtime/http/router | 半通用 |
| LAYER-08 | Medium | type-system-seal | 禁止任何包重新声明 HTTPRegistrar 类型（已删除旧接口） | — | 半通用 |
| LAYER-09 | Medium (推断) | import-ban | 禁止 cells/X 直接 import cells/Y/events 跨 Cell 事件包 | kernel/depgraph, cells/ | 半通用 |
| LAYER-09T | Medium (推断) | import-ban | LAYER-09 传递闭包变体 | kernel/depgraph, cells/ | 半通用 |
| LAYER-10 | Medium | callsite-allowlist | cells root 包导出 API 不得暴露 adapter/driver 具体类型 | kernel/depgraph, cells/, adapters/ | 半通用 |

> 注：LAYER-01..04 由 `.golangci.yml` depguard 执行，不在本批次。LAYER-* 规则均无显式 AI-robust 标注，依机制（depgraph 图 + typed 包扫描）推断为 Medium。

---

### 批次 2 — 引擎自检 / meta-archtest

| INVARIANT ID | 评级 | 机制类型 | 用途(≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01 | Medium | metadata/yaml-derive | 守卫 CI yaml 中 verify-archtest 步骤必须显式设 SHARD_COUNT=16 | — | 通用 |
| ARCHTEST-INVARIANTS-COVERAGE-01 | Medium | AST-pattern | verify-archtest-invariants.sh 中 -run 正则引用的 Test 函数必须在 AST 中存在 | — | 通用 |
| ARCHTEST-VERIFY-COVERAGE-01 | Medium | AST-pattern | verify-archtest.sh 发现集与 AST 扫描集一致；shard 分区不重叠不遗漏 | — | 通用 |
| ARCHTEST-HELPERS-01 | Medium (推断) | conformance-test | 共享测试 helper（类型工具、文件枚举）的单元覆盖锚点 | kernel/metadata | 半通用 |
| ARCHTEST-TESTMAIN-01 | Medium (推断) | conformance-test | TestMain 预热 SharedResolver 缓存，防止 OOM 及 shard 超时 | — | 通用 |
| ARCHTEST-PASS-DRIVER-UNIT-01 | Medium (推断) | conformance-test | archtest.Pass 驱动 API（Run/RunTyped/RunTypedDir 等）单元覆盖 | — | 通用 |

---

### 批次 3 — Pass Funnel 规则（PASS-FUNNEL-*）

| INVARIANT ID | 评级 | 机制类型 | 用途(≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| PASS-FUNNEL-EACHFILE-01 | Medium | callsite-allowlist | 禁止 archtest *_test.go 直接调用 scanner.EachFile，必须走 archtest.Run | — | 通用 |
| PASS-FUNNEL-LOADPACKAGES-01 | Medium | callsite-allowlist | 禁止 archtest *_test.go 直接调用 typeseval 加载符号，必须走 Pass 驱动 | — | 通用 |
| PASS-FUNNEL-PACKAGES-IMPORT-01 | Medium | callsite-allowlist | 禁止 archtest *_test.go 直接 import golang.org/x/tools/go/packages | — | 通用 |
| PASS-FUNNEL-RESOLVE-01 | Medium | callsite-allowlist | 禁止直接调用 typeseval 解析辅助函数，必须用 Pass facade 封装 | — | 通用 |
| PASS-FUNNEL-FIXTURE-TAG-01 | Hard | typed-marker-funnel | 禁止向 loader 传 archtest_fixture build tag 绕过 RunTypedFixture funnel | — | 通用 |

---

### 批次 4 — 内部工具约束

| INVARIANT ID | 评级 | 机制类型 | 用途(≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01 | Hard | typed-marker-funnel | constraint.Expr.Eval 调用必须经 BuildContextPredicate 或 all-false sentinel | — | 通用 |
| TYPESUTIL-IMPLEMENTS-FUNNEL-01 | Hard | typed-marker-funnel | go/types.Implements 仅允许在 typesutil 单一 funnel 文件中调用 | — | 通用 |
| SCANNER-FRAMEWORK-USAGE-01 | Hard↓/Medium↑ | callsite-allowlist | archtest *_test.go 禁止绕过 scanner 框架直接使用 ast/fs/inspector 遍历 | — | 通用 |
| SCANNER-FRAMEWORK-USAGE-02 | Hard↓/Medium↑ | typed-marker-funnel | 禁止在 EachInChildren 上手写 closure+done 哨兵，必须用 FindFirstChild | — | 通用 |
| REFLECT-STRING-ARG-SCANNER-01 | Medium | AST-pattern | reflect.Value.FieldByName/MethodByName 字符串参数扫描共享辅助自检 | — | 通用 |
| GOLDEN-HELPER-UNIT-01 | Medium (推断) | conformance-test | AssertGolden 与 scanner.Canonical 排序去重逻辑的单元覆盖 | — | 通用 |
| PRODUCTION-LOADER-FUNNEL-01 | Hard | typed-marker-funnel | 禁止 archtest *_test.go 用 ./... 直调原始 loader，必须经 ProductionResolver | — | 通用 |
| TAGGROUP-LOOP-FORBIDS-RUNTYPED-01 | Hard | typed-marker-funnel | 禁止在 KnownNonDefaultTags() range 循环体内调用 RunTyped（防 OOM shard） | — | 通用 |

---

### 批次 5 — 构建约束 / 库边界 / 锚点元规则

| INVARIANT ID | 评级 | 机制类型 | 用途(≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| BUILD-CONSTRAINT-INTEGRATION-TAG-01 | Medium | AST-pattern | *_integration_test.go 必须携带正确 //go:build integration 约束 | — | 通用 |
| INVENTORY-ANCHOR-REQUIRED-01 | Medium | AST-pattern | tools/archtest/*_test.go 每个文件头必须有 INVARIANT: 锚点 | — | 通用 |
| INVENTORY-ANCHOR-VALID-ID-01 | Medium | AST-pattern | 所有 INVARIANT: <ID> 必须符合规范语法正则，防止拼写漂移 | — | 通用 |
| PGQUERY-01 | Medium (推断) | import-ban | SQL Builder/keyset 辅助函数必须在 pkg/pgquery，pkg/query 只留通用分页 | pkg/query, pkg/pgquery | 专属 |
| KERNEL-INTERNAL-DAG-01 | Medium | callsite-allowlist | kernel 内部子模块跨 owner 依赖必须与 allowedKernelEdges 精确匹配 | kernel/depgraph, kernel/* | 专属 |

---

## 可移植性说明

- **通用**：规则概念（Pass funnel、分片脚本覆盖、build tag 约束、锚点格式）可直接迁移到任意 Go 项目，无需了解 GoCell 架构。
- **半通用**：规则概念可迁移（分层 import 禁令、adapter 泄漏），但涉及 GoCell 专属层名（cells/kernel/adapters）或包路径，需适配替换。
- **专属**：仅在 GoCell 架构语义（Cell/Slice 模型、kernel 内部 owner 拓扑、pgquery 迁移约定）下成立，搬到无关项目无意义。
