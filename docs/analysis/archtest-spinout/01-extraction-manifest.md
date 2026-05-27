# 交付物 #1：archtest 引擎抽离清单（精确）

> 目标：把 archtest 的**引擎层**抽成一个独立仓库 `archtest`（或 `go-archrules`），可被任意 Go 项目作为库依赖、用 `go test` 写自己的架构不变式。**规则层不搬**（理由见交付物 #3）。

---

## 1. 引擎 / 规则边界

archtest 实际是两层叠在一起：

| 层 | 内容 | 去向 |
|----|------|------|
| **引擎** | `tools/archtest/*.go`（非 `_test.go`）+ `internal/scanner` + `internal/typeseval` | ✅ 整体搬走 |
| **引擎自检（meta-archtest）** | `pass_test.go` / `pass_funnel_test.go` / `scanner_framework_usage_test.go` / `golden_test.go` / `eval_predicate_centralization_test.go` / `production_loader_funnel_test.go` / `reflect_string_arg_test.go` / `taggroup_loop_no_runtyped_test.go` / `build_constraint_test.go` / `implements_funnel_test.go` / `testmain_test.go` | ✅ 搬走（验证引擎本身，与业务无关；见交付物 A 编目） |
| **规则 + 规则自检** | 其余 ~170 个 `*_test.go` + `testdata/` | ❌ 留在 gocell |

判定依据已用 `go list` 核实：引擎的**非测试代码零 gocell 业务包依赖**，第三方依赖只有 `golang.org/x/tools` 与 `golang.org/x/sync`（见 §4）。

---

## 2. 必搬文件清单（精确，含行数）

### 2.1 façade 包 `tools/archtest/*.go`（公开 API，1259 行）

| 文件 | 行 | 作用 | 搬迁处理 |
|------|----|------|---------|
| `pass.go` | 515 | `Pass` / `Run` / `RunTyped` / `RunTypedProduction` / `RunTypedDir` 驱动 | 原样 |
| `resolve.go` | 194 | `ResolvePackageRef` / `ResolveMethodCall` / `EvaluateConstString` / build-tag helper re-export | 原样 |
| `walk.go` | 117 | 泛型 AST 遍历 `EachInSubtree` / `EachInChildren` / `FindFirstChild` re-export | 原样 |
| `fixture.go` | 113 | `RunTypedFixture`（加载独立 go.mod 夹具模块） | 原样 |
| `golden.go` | 98 | `AssertGolden`（regenerate-and-diff 字节锁） | 原样 |
| `scope.go` | 89 | `ModuleScope` / `DirsScope` / `ScopeOption` re-export | 原样 |
| `doc.go` | 85 | 包文档 | **改写**：删 LAYER-*/GoCell 规则清单，改为通用引擎说明 |
| `module_root.go` | 64 | `findModuleRoot`（向上找 `go.mod`） | 原样（已通用，见 §3） |
| `content.go` | 31 | `LoadContentFiles` / `EachContentFile`（YAML/JSON 元数据扫描） | 原样 |
| `passfunnel_inpkg_redfixture.go` | 34 | meta-archtest 的包内红夹具 | 搬走（属引擎自检） |
| `warm.go` | 19 | 进程内 warmup（`SharedResolver` 预热） | 原样 |

### 2.2 `internal/scanner`（AST 扫描原语，1408 行）

`content.go`(92) `diagnostic.go`(80) `doc.go`(89) `eachnode.go`(356) `importban.go`(129) `literal.go`(86) `parse.go`(79) `receiver.go`(32) `scope.go`(357) `walk.go`(108) — **全部原样搬走**。

### 2.3 `internal/typeseval`（go/types 求值层，838 行）

`buildtag_predicate.go`(84) `buildtags_extract.go`(194) `buildtags.go`(72) `call_target.go`(89) `eachfile.go`(62) `production_resolver.go`(73) `receiver_type.go`(59) `skip.go`(41) `typeseval.go`(164) — **基本原样**，仅 `buildtags.go` 需参数化（见 §3）。

**引擎核心总计 ≈ 3500 行非测试代码**（façade 1259 + scanner 1408 + typeseval 838）。配套引擎自检约 8000 行测试。

---

## 3. 必改的 repo 耦合点（逐处）

引擎的全部 repo 耦合集中在 **3 处**，都是小修：

### 耦合点 1 — `typeseval/buildtags.go::KnownNonDefaultTags()`（硬编码本仓 build tag）

```
24  return [][]string{
25      nil,
26      {"integration"}, {"e2e"}, {"e2e", "pg"}, {"examples_smoke"},
30      {"integration", "otelcollector"}, {"integration_cluster"},
```

`FlatNonDefaultTags()` / `KnownNonDefaultTags()` 把 gocell 的 build tag 集写死。
**改法**：提成构造参数或 `archtest.Config{ BuildTags [][]string }`，由调用方在 `TestMain` 注入；默认空集（`nil` 一项）。

### 耦合点 2 — `typeseval/skip.go::IsGeneratedRelPath()`（硬编码 `generated/` 前缀）

```
40  return strings.HasPrefix(rel, "generated/")
```

代表"codegen 产物根目录"约定。
**改法**：提成 `Config.GeneratedPrefix string`（默认 `"generated/"`，可空表示无 codegen）。`RunTypedProduction` 据此过滤。

### 耦合点 3 — `scanner/scope.go` 默认跳过目录集

```
28  "vendor": {}, "testdata": {}, "generated": {}, ".git": {}, "node_modules": {}
```

这其实是**合理的通用默认**，唯一沾 repo 约定的是 `"generated"`。
**改法**：与耦合点 2 合并到 `Config`，或保留默认 + 暴露 `ScopeOption` 覆盖。**优先级最低**，默认值即可用。

> `module_root.go::findModuleRoot`（向上找 `go.mod`）**已经是通用实现**，无需改。`doc.go` 的包文档需重写（删 LAYER-01..10 / PGQUERY-01 等 GoCell 规则枚举）。

---

## 4. 新仓库 `go.mod`（外部依赖极少）

`go list -deps` 核实，引擎真实第三方依赖只有两个家族：

```
require (
    golang.org/x/tools  // go/packages, go/ast/inspector, gcexportdata, objectpath, typeutil
    golang.org/x/sync   // singleflight, errgroup
)
```

无需 pgx / prometheus / rabbitmq / yaml 等——之前 grep 看到的那些全部来自规则文件的字符串字面量或规则 import，不在引擎里。**这是引擎可独立的最强信号：依赖面干净到只剩 `x/tools` + `x/sync`。**

---

## 5. 抽离后的目录结构

```
archtest/                       (新 module: github.com/<you>/archtest)
├── go.mod                      x/tools + x/sync
├── archtest.go                 ← 原 pass.go（驱动）
├── resolve.go walk.go scope.go fixture.go golden.go content.go warm.go module_root.go
├── doc.go                      ← 重写为通用引擎文档
├── config.go                   ← 新增：BuildTags / GeneratedPrefix / SkipDirs 注入点（吸收 §3 三处耦合）
├── internal/
│   ├── scanner/                ← 原样
│   └── typeseval/              ← buildtags.go 改为读 Config
└── meta_test.go ...            ← 引擎自检（编目 A 中标"meta-archtest"的那些）
```

调用方（你的项目）用法不变：

```go
// yourproject/arch/layering_test.go
func TestLayering(t *testing.T) {
    diags := archtest.Run(t, archtest.ModuleScope(root), func(p *archtest.Pass) []archtest.Diagnostic {
        // ... 你自己的规则
    })
    archtest.Report(t, "MY-LAYER-01", diags)
}
```

---

## 6. 搬迁步骤（可执行）

1. `git subtree split` 或手动 `cp` §2 清单文件到新 repo，保留 `internal/` 层级。
2. 改 import 路径 `github.com/ghbvf/gocell/tools/archtest/internal/*` → `github.com/<you>/archtest/internal/*`（仅引擎内部互引，~10 处）。
3. 落地 §3 三处耦合到 `config.go`；`buildtags.go` / `skip.go` 改读 Config。
4. 重写 `doc.go`。
5. `go mod tidy` → 应只拉到 `x/tools` + `x/sync`。
6. `go test ./...` 跑引擎自检（meta-archtest）全绿即验证成功。
7. （可选）gocell 侧反向依赖新库：删 `tools/archtest/internal/`，规则文件改 import 新 module——但**不建议**，见下。

> ⚠️ 反向依赖的代价：gocell 的规则文件目前直接用 façade（`archtest.Run` 等），若新库的 `Config` 注入方式变了，183 个规则文件都要适配。若只是"给自己将来用"，**fork 出去单独演进**比"gocell 反依赖新库"更省事，二者不必同时做。

---

## 7. 工作量估算

| 任务 | 量级 |
|------|------|
| 文件搬迁 + import 改写 | ~0.5 天 |
| §3 三处耦合参数化 + `config.go` | ~0.5 天 |
| `doc.go` 重写 + meta-archtest 跑通 | ~0.5 天 |
| **引擎独立可用** | **~1.5 天** |
| 重写规则（按需，每个新项目） | 不在此列，见交付物 #3 |

**结论：引擎抽离是低风险、~1.5 天的活。真正的成本不在搬引擎，而在"引擎搬走后规则要重写"——而规则本就不该搬。**
