# Archtest 编目 C — Cell / Contract / Assembly / Metadata / Celltest 边界

本批次覆盖 GoCell 声明模型核心约束：Cell 接口 ISP 拆分、元数据单源、codegen funnel、
celltest 边界、Contract 命名与结构、Assembly 代组成与推导、以及跨主题 schema 守卫。

本文件 **43 条** | Hard 14 / Medium 26 / Soft 0 | 通用 0 / 半通用 3 / 专属 40

---

### 批次 1 — Cell 接口 / 初始化 / 层隔离

| INVARIANT ID | 评级 | 机制类型 | 用途 (≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CELL-IFACE-ISP-COMPOSITE-01 | Medium | AST-pattern | Cell 接口必须是4子接口的纯内嵌复合，本身不直接声明方法 | kernel/cell | 专属 |
| CELL-IFACE-ISP-METHODSETS-01 | Medium | AST-pattern | 每个子接口方法集合必须精确匹配契约（hash guard） | kernel/cell | 专属 |
| CELL-IFACE-ISP-BASECELL-CHECK-01 | Medium | AST-pattern | BaseCell 编译期 check 必须四段式分写，对应4子接口 | kernel/cell | 专属 |
| CELL-L2-INIT-CHECKNOTNOOP-CALLED-01 | Medium | AST-pattern | L2+ Cell 的 Init 方法必须调用 kernel/outbox.CheckNotNoop | kernel/cell, kernel/outbox | 专属 |
| CELL-INIT-CONTRACTUSAGE-01 | Medium | import-ban | kernel/cell 不得 import runtime/ 或 adapters/；Registrar 类型必须本地定义 | kernel/cell | 专属 |
| CELL-TEST-NO-ADAPTER-IMPORT-01 | Medium | import-ban | Cell 单元测试禁止 import adapters/ 层；必须用 in-mem fake | adapters/ | 半通用 |
| CELLS-NO-CONTRACTSPEC-IMPORT-01 | Medium | import-ban | cells/ 非生成文件禁止 import kernel/contractspec 并引用 ContractSpec | kernel/contractspec | 专属 |
| CELLTEST-IMPORT-BOUNDARY-01 | Medium | import-ban | 非_test.go / kernel/ / examples/ 生产文件禁止 import kernel/cell/celltest | kernel/cell/celltest | 专属 |
| CELLTEST-IMPORT-SCOPE-01 | Medium | import-ban | 生产文件禁止 import cells/{X}/{X}test 命名的 celltest 包 | cells/ | 专属 |

---

### 批次 2 — Cell 元数据单源 / DTO 漂移 / Raw-infra 封印

| INVARIANT ID | 评级 | 机制类型 | 用途 (≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CELLMETA-SINGLE-SOURCE-01 | Medium (推断) | AST-pattern | kernel/cell 禁止声明已迁至 kernel/metadata 的5个旧类型名 | kernel/cell, kernel/metadata | 专属 |
| CELLMETA-SINGLE-SOURCE-02 | Medium (推断) | AST-pattern | kernel/cell.NewBaseCell 接收单一 *metadata.CellMeta 参数 | kernel/cell, kernel/metadata | 专属 |
| CELLMETA-SINGLE-SOURCE-03 | Medium (推断) | AST-pattern | CellInventory.Metadata() 返回 *metadata.CellMeta，非旧类型 | kernel/cell, kernel/metadata | 专属 |
| CELL-META-DTO-COVERAGE-01 | Medium (推断) | reflect-field-freeze | CellMeta 顶层 yaml 字段必须全部映射到 catalog.CellSpec 或显式排除 | kernel/metadata, runtime/devtools/catalog | 专属 |
| CELL-ID-PATTERN-SINGLE-SOURCE-01 | Medium | AST-pattern | Cell/Assembly ID 正则字面量只能存在于 kernel/metadata/contract_constraints.go | kernel/metadata | 专属 |
| CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 | Medium | callsite-allowlist | cell 子树内 With* Option 函数禁止接收原始 infra 类型参数 | kernel/persistence, kernel/outbox | 专属 |
| CELL-RAW-INFRA-WRAPPER-LOCATION-01 | Medium | callsite-allowlist | WrapForCell 等4个 wrapper 只能在 composition root 或 _test.go 调用 | kernel/persistence, kernel/outbox | 专属 |
| CELL-REPO-READYZ-PROBE-01 | Medium | conformance-test | 每个 kernel/healthz.RepoProber 实现都必须经过 conformance harness 测试 | kernel/healthz, kernel/cell/celltest | 专属 |

---

### 批次 3 — Cellgen / Celltest 边界 / Contract 契约种类

| INVARIANT ID | 评级 | 机制类型 | 用途 (≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CELLGEN-ERRCODE-FUNNEL-01 | Hard | typed-marker-funnel | tools/codegen/cellgen/ 禁止使用 fmt.Errorf / errors.New / errors.Join | pkg/errcode | 专属 |
| CONTRACT-KINDS-CLOSED-SET-01 | Medium (推断) | metadata/yaml-derive | contract.yaml 的 kind 必须是 {http,event,command,projection} 封闭集 | kernel/metadata | 专属 |
| CONTRACT-PATH-QUERY-COVERAGE-01 | Medium | conformance-test | 每个声明 pathParams/queryParams 的 HTTP contract 必须有对应的 MustReject 调用 | kernel/metadata, tests/contracttest | 专属 |
| CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01 | Medium | callsite-allowlist | MustReject{Path,Query}Param 第二参数必须是编译期常量字符串 | tests/contracttest | 专属 |
| CONTRACT-WIRE-FIELD-CAMELCASE-01 | Hard | metadata/yaml-derive | contracts/ 下 wire 字段名必须是 camelCase，不得 snake_case | contracts/ | 半通用 |
| CONTRACT-PAGINATION-PARAM-LIMIT-01 | Hard | metadata/yaml-derive | 分页 queryParam 必须命名为 "limit"，不得用其他名称 | contracts/ | 专属 |
| CONTRACT-PAGINATION-LIMIT-MAXIMUM-01 | Hard | metadata/yaml-derive | 分页 limit 参数必须声明 maximum 约束 | contracts/ | 专属 |
| CONTRACT-EVENT-IDEMPOTENCY-KEY-EVENTID-01 | Hard | metadata/yaml-derive | event contract payload 必须包含名为 "eventId" 的幂等键字段 | contracts/ | 专属 |

---

### 批次 4 — Contract subscribers 单源 / codegen funnel / contracttest 边界

| INVARIANT ID | 评级 | 机制类型 | 用途 (≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| SUBSCRIBERS-DERIVED-FIELD-FROZEN-01 | Hard↓/Medium↑ | reflect-field-freeze | EndpointsMeta.Subscribers 字段必须携带 yaml:"-"，防止手写 subscribers 键 | kernel/metadata | 专属 |
| CONTRACT-YAML-NO-SUBSCRIBERS-KEY-01 | Medium | metadata/yaml-derive | contract.yaml 禁止出现手写 subscribers: 键（该字段为派生字段） | kernel/metadata | 专属 |
| SUBSCRIBE-MARKER-RETIRED-01 | Medium | AST-pattern | cell.go 禁止出现已退役的 `// +slice:subscribe` marker | cells/ | 专属 |
| CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01 | Medium | metadata/yaml-derive | contract.yaml 禁止显式写 `codegen: true`（默认值，冗余） | kernel/metadata | 专属 |
| CONTRACTTEST-BOUNDARY-01 | Medium (推断) | import-ban | tests/contracttest 仅供 _test.go 使用；legacy pkg/contracts 路径必须已删除 | tests/contracttest | 专属 |
| CONTRACTTEST-LOADBYID-LITERAL-01 | Medium (推断) | callsite-allowlist | contracttest.LoadByID 第三参数必须是编译期常量字符串 | tests/contracttest | 专属 |
| NO-MANUAL-CONTRACTSPEC-LITERAL-01 | Hard | callsite-allowlist | cells/examples/runtime 禁止手写 ContractSpec{} 字面量；必须用 codegen 生成或 typed funnel | kernel/contractspec | 专属 |
| CONTRACTSPEC-FRAMEWORK-BUILDERS-EXIST-01 | Medium | callsite-allowlist | NewEventDerivation 调用方限于单文件 allowlist（eventrouter/contract_tracing_subscriber.go） | kernel/contractspec | 专属 |

---

### 批次 5 — Assembly 不变式 / Metadata DTO / Parser 对称性

| INVARIANT ID | 评级 | 机制类型 | 用途 (≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| ASSEMBLY-MODULES-GEN-01 | Medium | codegen-funnel+golden | modules_gen.go 必须带 DO NOT EDIT marker、package main、generatedCellModules() | kernel/metadata | 专属 |
| ASSEMBLY-MODULES-SWITCH-FORBIDDEN-02 | Medium | AST-pattern | assembly 组合入口禁止对 cell ID 字面量做 switch | kernel/metadata | 专属 |
| ASSEMBLY-MAXCONSISTENCY-DERIVED-03 | Hard | reflect-field-freeze | AssemblyMeta.MaxConsistencyLevel 必须携带 yaml:"-"，防止手写 | kernel/metadata | 专属 |
| ASSEMBLY-CELLMODULE-TYPE-04 | Medium | AST-pattern | assembly 组合入口必须声明顶层 CellModule 类型 | kernel/metadata | 专属 |
| ASSEMBLY-SNAPSHOTS-LOCKED-01 | Medium (推断) | AST-pattern | kernel/assembly/ 中对 *.snapshots 的写入必须在 mu.Lock() 临界区内 | kernel/assembly | 专属 |
| ASSEMBLYREF-METHOD-SET-01 | Medium (推断) | AST-pattern | auth.AssemblyRef 接口必须恰好有 3 个方法（ID/CellIDs/Cell） | kernel/auth | 专属 |
| ASSEMBLY-META-DTO-COVERAGE-01 | Medium (推断) | reflect-field-freeze | AssemblyMeta 顶层 yaml 字段必须全部映射到 catalog.AssemblySpec 或显式排除 | kernel/metadata, runtime/devtools/catalog | 专属 |
| PARSER-MATCHER-EXAMPLES-SYMMETRY-01 | Medium | AST-pattern | kernel/metadata/parser.go 中每个 match*YAML 函数必须同时覆盖 root 和 examples/ 形式 | kernel/metadata | 专属 |

---

### 批次 6 — Schema / Query / CAS 守卫

| INVARIANT ID | 评级 | 机制类型 | 用途 (≤30字中文) | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| PATCH-OPTIONAL-BOOL-POINTER-01 | Hard (推断) | metadata/yaml-derive | PATCH Request 中可选 bool 字段必须是 *bool，不得是 bool | generated/contracts | 半通用 |
| META-QUERYPARAM-DRIFT-01 | Medium (推断) | metadata/yaml-derive | HTTP handler 读取的 query param 必须在 contract.yaml 中声明，反之亦然 | kernel/metadata | 专属 |
| CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01 | Hard | metadata/yaml-derive | CAS guard 字段 expectedVersion 必须通过 $ref 引用共享 mixin，禁止 inline 副本 | contracts/shared/cas | 专属 |
