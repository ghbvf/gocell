# Archtest 编目 D — Codegen 漏斗 / Scaffold / Bootstrap 装配 / 依赖注入 funnel

本文件编目 24 个源文件中的所有 INVARIANT，主题覆盖：

- **Codegen 漏斗**：cell/contract 代码生成输出完整性、marker 单源、YAML 字段禁止、spec 字段奇偶性
- **HTTP handler 漏斗**：contractgen 内部 `httpEndpointSpec` sealed 构造与渲染唯一性
- **Scaffold 输出一致性**：bundle 标记嵌入、强制覆盖权限、OS 写禁止、typed ID 构造
- **Pathsafe / PlanSet**：`PlanSet` sealed 构造 + dup-reject
- **Bootstrap 装配**：auth 路由互斥、admin 路径限制、module 顺序、audit observer funnel、capability provider funnel
- **依赖注入 funnel**：required-dep nil guard、accesscore bundle、baseslice 构造、shared schema mirror、sealed marker noop

本文件 **35 条** | Hard 15 / Medium 17 / Soft 0 / 推断 3 | 通用 2 / 半通用 12 / 专属 21

---

### 批次 1 — Codegen Cell/Contract 生成完整性（codegen_invariants_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CODEGEN-CELL-GEN-01 | Medium（推断） | codegen-funnel+golden | 已启用 codegen 的 cell 必须存在 cell_gen.go | tools/codegen/markergen, kernel/metadata | 半通用 |
| CODEGEN-CELL-GEN-02 | Medium（推断） | codegen-funnel+golden | cell_gen.go 必须有标准生成头注释 | tools/codegen/markergen | 半通用 |
| CODEGEN-CELL-GEN-03 | Medium（推断） | codegen-funnel+golden | 不详（merged from cell_gen_test，推断为 initInternal 存在性约束） | kernel/metadata | 半通用 |
| CODEGEN-CELL-GEN-04 | Medium（推断） | codegen-funnel+golden | 不详（merged from cell_gen_test，推断为 goStructName 相关完整性约束） | kernel/metadata | 半通用 |
| CODEGEN-CONTRACT-GEN-01 | Medium（推断） | codegen-funnel+golden | 已启用 codegen 的 contract 必须存在 types_gen.go / iface_gen.go / handler_gen.go | tools/codegen, contracts/ | 半通用 |
| CODEGEN-CONTRACT-GEN-02 | Medium（推断） | codegen-funnel+golden | contract 生成文件必须有标准生成头注释 | tools/codegen | 半通用 |
| CODEGEN-CONTRACT-USER-OVERLAP-01 | Medium（推断） | AST-pattern | 用户文件不得定义与 codegen 重叠的 Init 方法 | tools/codegen/cellgen | 专属 |
| COMMAND-PROJECTION-EXPLICIT-01 | Medium（推断） | codegen-funnel+golden | command/projection 合约不得有 http/event 专属生成文件 | contracts/, kernel/metadata | 专属 |
| SPEC-GEN-VALUE-PARITY-01 | Medium（推断） | codegen-funnel+golden | spec_gen.go 中 ID / Topic 值须与 contract.yaml 一致 | contracts/, tools/codegen | 专属 |
| SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01 | Medium（推断） | codegen-funnel+golden | event contract 的 spec_gen.go topic 字段须等于 contract ID | contracts/, tools/codegen | 专属 |

---

### 批次 2 — K#05 Marker / Wire 单源（codegen_invariants_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| NO-METADATA-LITERAL-IN-CELLGO-01 | Medium（推断） | AST-pattern | cell.go 禁止手写 metadata.CellMeta 复合字面量 | kernel/metadata, tools/codegen | 专属 |
| NO-WIRE-FIELDS-IN-YAML-01 | Medium（推断） | metadata/yaml-derive | cell.yaml 禁止 listeners: 字段；slice.yaml 禁止 routeMounts:/subscribes: 字段 | kernel/metadata, contracts/ | 专属 |
| MARKER-MISSING-FOR-WIRE-CALL-01 | Medium（推断） | AST-pattern | wire call 调用点须有对应 marker 注释，不能孤立调用 | tools/codegen/markergen | 专属 |
| MARKERGEN-DRIFT-VERIFY-01 | Medium（推断） | codegen-funnel+golden | markergen 输出须与源 marker 注释一一对应，不能漂移 | tools/codegen/markergen | 专属 |
| MARKER-WIRE-SINGLE-SOURCE-01 | Medium（推断） | codegen-funnel+golden | wire 标记必须单源；禁止同时在 marker 和字面量双写 | tools/codegen/markergen | 专属 |

---

### 批次 3 — HTTP Handler Funnel（codegen_http_handler_funnel_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01（A1a） | Hard | type-system-seal | contractgen.httpEndpointSpec 为 unexported，包外不可构造 | tools/codegen/contractgen | 半通用 |
| CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01（A1b） | Medium | callsite-allowlist | buildHTTPEndpointSpec 唯一构造+调用点在 contractgen 内 | tools/codegen/contractgen | 半通用 |
| CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01（A2） | Medium | AST-pattern | handler emit 模板在 contractgen 内唯一，禁止同义副本 | tools/codegen/contractgen | 半通用 |
| CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01（A3） | Medium | AST-pattern | handler 模板渲染只在 contractgen 内调用 | tools/codegen/contractgen | 半通用 |
| CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01（A4） | Hard | type-system-seal | 无导出 spec 类型泄漏到 contractgen 包外 | tools/codegen/contractgen | 半通用 |

> 注：上述 5 条均属同一 INVARIANT ID，按 A1a/A1b/A2/A3/A4 腿分别编目，因评级不同。

---

### 批次 4 — Scaffold 输出与写入漏斗

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| SCAFFOLD-BUNDLE-MARKER-01 | Medium | AST-pattern | scaffold 产出的 cell.go 必须嵌入 K#05 listener marker 注释 | tools/codegen/cellgen | 专属 |
| SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01 | Medium | AST-pattern | scaffold 产出的 contract.yaml 禁止含 codegen: 字面量键 | tools/codegen/cellgen | 专属 |
| SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01 | Medium | typed-marker-funnel | scaffold 使用 cellgen.ListenerMarker typed const 而非手写字符串 | tools/codegen/cellgen | 半通用 |
| SCAFFOLD-DERIVED-FORCEOVERWRITE-01 | Hard↓/Hard↑ | codegen-funnel+golden | ForceOverwrite 只能在 PlanFile 构造函数内设置，业务路径不可绕过 | pkg/pathsafe, tools/codegen | 半通用 |
| SCAFFOLD-WRITE-FUNNEL-01 | Medium | callsite-allowlist | scaffold 层禁止直接调用 os.Write 系列，必须走 pathsafe.WritePlannedFiles | pkg/pathsafe | 半通用 |
| SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01 | Hard（推断） | type-system-seal | ScaffoldID 只能通过 scaffoldid.Parse 构造，无 MustParse 后门 | pkg/scaffoldid | 通用 |

---

### 批次 5 — Pathsafe PlanSet Funnel

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| PATHSAFE-PLANSET-FUNNEL-01 | Hard | type-system-seal | PlanSet.items 字段私有，外部只能通过 NewPlanSet 构造，dup-reject 不可绕过 | pkg/pathsafe | 通用 |

---

### 批次 6 — Shared Schema Mirror Funnel

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| SHARED-SCHEMA-MIRROR-FUNNEL-01（A1） | Medium | AST-pattern | 检测新增 schema 副本（反向枚举），防止 shared schema 多处手写 | contracts/shared | 专属 |
| SHARED-SCHEMA-MIRROR-FUNNEL-01（A2） | Medium | callsite-allowlist | Headerless 构造仅在 allowlist callsite 调用，不泄漏到外部包 | contracts/shared, tools/codegen | 专属 |

---

### 批次 7 — Sealed Marker Noop Transparency

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| SEALED-MARKER-NOOP-TRANSPARENCY-01 | Hard | type-system-seal | sealed marker 类型的所有实现必须有 Noop()，通过 go/types 自动发现 | kernel/cell | 专属 |

---

### 批次 8 — BaseSlice 构造漏斗 & Required-Dep Nil Guard

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| BASESLICE-CTOR-FUNNEL-01 | Medium | callsite-allowlist | BaseSlice/SliceMeta 复合字面量禁止在 production 代码手写，必须走构造函数 | kernel/cell, cells/ | 专属 |
| REQUIRED-DEP-NIL-GUARD-01 | Hard | codegen-funnel+golden | `gocell:"required"` tag 派生 validateRequired()，regenerate-and-diff 字节级锁；callsite 唯一性+IsNilInterface ban+tag 白名单四件套 | tools/codegen, kernel/cell, cells/ | 专属 |

---

### 批次 9 — Auth Bootstrap 装配不变式

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CELLS-NO-ROUTEMUX-WRAPPER-01 | Hard | AST-pattern | cells/ 禁止嵌入 cell.RouteMux，防止接口扇出静默截断 | kernel/cell, cells/ | 专属 |
| AUTH-ROUTE-BOOTSTRAP-FLAG-REMOVED-01 | Hard | AST-pattern | runtime/auth.Route 禁止声明 Bootstrap bool 字段，唯一表达方式是 BootstrapAuth 函数值 | runtime/auth | 专属 |
| SETUP-ADMIN-CODEGEN-BOOTSTRAP-AUTH-WIRED-01 | Hard | codegen-funnel+golden | 生成的 setup/admin handler 必须传入非零 BootstrapAuth 参数，锁定 codegen 模板 | tools/codegen/contractgen, runtime/auth | 专属 |
| AUTH-BOOTSTRAP-CLIENTS-MUTEX-01 | Hard | type-system-seal | auth.Route 声明 BootstrapAuth 时 Contract.Clients 必须为空；type-checked Var key 比较，0 已知盲区 | runtime/auth | 专属 |

---

### 批次 10 — Setup Admin Auth 路径约束

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| SETUP-ADMIN-NOT-PUBLIC-01 | Medium（推断） | metadata/yaml-derive | bootstrap 路径上的 contract 禁止声明 auth.public:true | kernel/metadata | 专属 |
| AUTH-BOOTSTRAP-PATH-RESTRICTED-01 | Medium（推断） | metadata/yaml-derive | auth.bootstrap:true 只允许在 IsBootstrapPath 路径的 contract 上 | kernel/metadata | 专属 |
| BOOTSTRAP-PATH-PREDICATE-SOLE-01 | Medium（推断） | AST-pattern | strings.Contains(_, "setup/admin") 只允许在 kernel/metadata/bootstrap_path.go 内 | kernel/metadata | 半通用 |

---

### 批次 11 — 其他 Bootstrap / Module 装配约束

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-DOWNSTREAM-HARD-01 | Hard | typed-marker-funnel | audit.NewBootstrapAuthFailObserver 返回的闭包体必须满足 3 conjunction 条件（下游 Hard） | cells/accesscore, cells/auditcore, runtime/auth | 专属 |
| BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01 | Medium | callsite-allowlist | 每个 bootstrap middleware 对 AppendBootstrapAuthFail 的赋值必须经过 funnel 调用（上游 Medium） | cells/auditcore, runtime/auth | 专属 |
| MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01 | Medium（推断） | metadata/yaml-derive | assembly.yaml 中 auditcore module 必须在 accesscore 之前，保证 SharedDeps.BootstrapLedgerStore 先就绪 | assemblies/ | 专属 |
| CAPABILITY-PROVIDER-FUNNEL-01 | Hard↑/Medium↓ | typed-marker-funnel | postgres.NewPool/NewTxManager/NewOutboxWriter/redis.NewClient 只能在 cap_wiring.go 构造；上游 Hard（sealed interface），下游 Medium（callsite allowlist） | adapters/postgres, adapters/redis, runtime/capability, cmd/corebundle | 专属 |

---

### 批次 12 — 其余杂项约束

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| NO-TEST-SERVICE-CONTEXT-IN-PRODUCTION-01 | Medium（推断） | AST-pattern | auth.TestServiceContext 只能在 _test.go 中调用，禁止出现在生产路径 | runtime/auth | 半通用 |
| PROVISION-STATE-AND-USERSOURCE-BOOTSTRAP-REMOVED-01 | Medium（推断） | AST-pattern | 10 个 bootstrap provision 状态机标识符在 cells/accesscore 中永久禁止出现 | cells/accesscore | 专属 |
| COREBUNDLE-DEPS-01 | Medium（推断） | import-ban | cmd/corebundle 禁止依赖 tools/depgraph 或 golang.org/x/tools | cmd/corebundle, tools/depgraph | 半通用 |
| ACCESSCORE-BUNDLE-FUNNEL-01 | Hard | type-system-seal | accesscore 的散装 With*Repository/WithSetupLock/WithTxManager 选项必须保持 unexported；Go 可见性即 Hard 载体 | cells/accesscore | 专属 |
| ACCESSCORE-FACADE-A61-01 | Medium（推断） | AST-pattern | accesscore 禁止重新暴露 bootstrap credential-path forwarding API | cells/accesscore | 专属 |
| LISTENER-DX-01 | Medium（推断） | AST-pattern | 已删除的 listener 选项 API 和 legacy auth.Route Delegated surface 禁止重新引入 | runtime/http, runtime/auth | 专属 |
| MANAGED-RESOURCE-COMPLETENESS-01 | Medium（推断） | conformance-test | adapters/ 导出类型必须实现 ManagedResource 或在 opt-out 表中明确豁免 | adapters/, kernel/lifecycle | 半通用 |
