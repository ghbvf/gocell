# PR #404 第二轮 Review 报告

**PR**: feat(assembly): ASSEMBLY-YAML-MINIMAL — Owner/MaxConsistency 派生 + modules_gen 派生 (K#10)
**分支**: feat/534-assembly-yaml-minimal (HEAD `84701adb`)
**审查范围**: K#10 落盘 + 六席位 review + 跨 PR L3 概念模型一致性
**审查日期**: 2026-05-07

---

## 0. 方向评估

**Direction: 正确，不可逆。**

K#10 实质交付：
1. `assemblies/{id}/assembly.yaml` 从 9 行（含整个 build 块）简化为 3-7 行（id + cells + owner，build 全 optional）
2. `AssemblyMeta.Owner` / `MaxConsistencyLevel` 字段化，前者必填、后者派生只读
3. `cmd/{id}/modules_gen.go` 由 `gocell generate assembly` 派生，删 `run.go` 手工 cell→Module switch
4. governance TOPO-09 增 max consistency level 安全网；FMT-29 守 owner 必填
5. drift gate 三件套（archtest + verify-codegen-assembly + manifest）锁定 modules_gen.go 漂移
6. `kernel/cell/levelrank` 子包出生：`kernel/cell` 与 `kernel/metadata` 共用 L0-L4 ordering，消除 inline 复制（commit `84701adb`）

向上对标 Kubernetes CRD 派生 + go-zero goctl + Kratos 生成器范式。**架构方向独立成立，与下文挑出的问题无关。**

---

## 1. 验证：6 个症状

六席位 review（Security / Architecture / Tests / Ops / Product/DX / Maintainability）共识 finding，事实层面全部核对成立。O1-O4 上一轮 OUT_OF_SCOPE 项已分别处理（O1 by-design closed / O2 by-design closed / O3+O4 在 commit `84701adb` 落地），不再纳入本轮。

| # | 严重级别 | 席位 | 文件:行 | 行为 | 证据 |
|---|---|---|---|---|---|
| F1 | P1 | Security | `kernel/assembly/generator.go:174` + `kernel/metadata/types.go:32` + `tools/codegen/cellgen/builder.go:69` | `cell.GoStructName` 进入 Go 模板/wire，schema 有 pattern `^[A-Z][A-Za-z0-9]*$` 但 runtime 不 enforce；恶意 `cell.yaml` 可注入 Go 源代码，drift gate 还会接受它 | schema-only 约束未进入 parser/governance/codegen；`grep -rn GoStructName kernel/governance/` 全无；codegen 直接拼字符串入 template |
| F2 | P1 | Architecture / Tests | `assembly.schema.json:13`+`:55` + `kernel/governance/rules_fmt.go` + `cmd/gocell/app/mode_test.go:58` | schema 约束 id pattern `^[a-z][a-z0-9]+$` 与 deployTemplate enum `{k8s,compose,binary}`，governance 仅 FMT-16 拒 `-`；schema 用户与 CLI 用户看到不同 contract；fixture 用 `deployTemplate: deploy.yaml` 在 governance 阶段静默通过 | schema 约束未进入 governance 链；CLI 不跑 jsonschema |
| F3 | P2 | Security / Ops | `cmd/gocell/app/codegen_assembly_cmd.go:89-97` + `tools/codegen/writer.go:81` | verify --local 用 `os.ReadFile(outPath)` 直接读，绕过写入路径已有的 `governance.IsWithinRoot` guard；nolint:gosec 注释错误声称"path 来自 metadata，不是用户输入"——`asm.Build.Entrypoint` 来自 user-controlled assembly.yaml | `codegen.Write{Verify:true}` 已支持，verify 没复用 |
| F4 | P2 | Architecture | `kernel/metadata/types.go:209` + `runtime/devtools/catalog/wire.go:256` + `runtime/devtools/catalog/build.go:321` | metadata 加 `Owner` / `MaxConsistencyLevel`，devtools catalog `AssemblySpec` 仅 Cells+Build；catalog 消费方看不到新 contract | `grep AssemblySpec wire.go` 验证缺字段 |
| F5 | P2 | Ops | `.github/workflows/_build-lint.yml:564` + `tools/archtest/ci_pinning_test.go:587` | CI 已加 `Verify assembly codegen (K#10)` step，archtest `codegenStepNames` 仍仅锁旧 3 个；后续误删 K#10 gate 不会被结构测试抓到 | archtest 数组 grep |
| F6 | P2 | Product/DX / Maintainability | `cmd/gocell/app/help.go:84-92` + `cmd/gocell/app/generate.go:108-112` + `docs/architecture/202605061800-adr-...md:4`+`:26` | help/ADR 与实现漂移：(a) help 不提 modules_gen.go / `--all`；(b) `assemblyIDsToGenerate` map iteration 未排序；(c) ADR 写 `bin/{id}` 但代码派生为 `asm.ID`；(d) ADR 日期 2026-05-08 晚于 worktree 当天 2026-05-07 | 4 子项独立成立 |

---

## 2. 根因（与 PR #403 R0 同源）

### R-meta — GoCell 派生物治理无产品化通道

PR #403 第三轮 review 已经命名了同一根因：**R0（meta，跨 PR）— GoCell 派生物治理无产品化通道**（`docs/reviews/202605070153-pr403-third-wave-review.md` §2.R0）。本 PR 的 F1-F6 是同一上游缺陷在 schema/字段切面的下游表现。

#### 病灶

GoCell 的 cell/assembly/contract 资源契约目前在 6-8 处独立持有约束语义，互相手工同步：

```
JSON Schema（IDE/test，runtime 不跑）─┐
Go struct + yaml tag                ─┤
parser.KnownFields(true)            ─┤  同一个"AssemblyMeta 是什么"
parser derivation                   ─┼─→ 被独立写 6-8 次
governance Validator (FMT-*/REF-*)  ─┤   谁都不是 source-of-truth
codegen template (cellgen/asmgen)   ─┤
catalog wire DTO                    ─┤
help text / ADR / CI step + archtest ┘
```

加一个新字段或新约束需要在 6-8 处编辑，漏一处即漂移。**review 反复出现同类 finding（K#04 / K#06 / K#10 都犯过），根因不在代码层，在概念模型层**。

#### F1-F6 的同根映射

| Finding | 漂移轴 | 多源数 |
|---------|-------|-------|
| F1 goStructName injection | schema pattern ↔ codegen consumption | 2 源（schema 拒 / codegen 信任）|
| F2 id+deployTemplate divergence | schema constraint ↔ governance rule | 2 源（同字段两份约束）|
| F4 catalog DTO drift | metadata struct ↔ wire DTO | 2 源（手工 mapper）|
| F5 CI step un-locked | workflow step ↔ archtest pin list | 2 源（同 step 两处声明）|
| F6 help/ADR drift | impl ↔ user-facing doc | 多源（文档跟随过程问题）|
| F3 verify path traversal | write path guard ↔ verify path guard | 2 源（同安全约束两份实现）|

每条 finding 都是 *双源声明 + 手工同步* 的同一类故障。

#### 业界标杆数据（PR #403 R0 同源调研）

| 项目 | authoritative source | 新加约束改几处 |
|------|---------------------|---------------|
| kubebuilder（K8s CRD） | Go struct marker (`+kubebuilder:validation:Pattern=...`) | **1** |
| buf.validate / protovalidate-go | `.proto` field option | **1** |
| ent (Meta) | Go schema func | **1** |
| kin-openapi + oapi-codegen | OpenAPI YAML | **1** |
| CUE (Istio/Tekton) | `.cue` 文件 | **1** |
| **GoCell 现状** | 无（6-8 处 first-class） | **6-8** |

通用原则：**single source 派生其余 artifact，runtime 不二次校验**。GoCell 反过来——多源声明 + codegen 完全信任，注入面无防护。

ref: `docs/reviews/202605070153-pr403-third-wave-review.md` §2.R0；kubebuilder controller-tools markers/validation.go；bufbuild/protovalidate-go validator.go；ent/ent entc/gen/template/builder/create.tmpl；getkin/kin-openapi openapi3/schema.go；cue-lang/cue encoding/openapi/build.go。

### R-local — F1-F6 子根因

在 R-meta 之外，本 PR 还有两条 PR-local 子根因可独立编址：

- **R1（L2 / Cx2）** — codegen 输入字段无 typed boundary：CellMeta.GoStructName 是 raw string，进入 Go 模板/wire 的路径无类型保护，纯靠纪律
- **R2（L1 / Cx1）** — 新字段半穿透：metadata 加字段后，下游 catalog DTO / help / ADR 需手工同步，无 archtest 守

---

## 3. 与 PR #403 R0/段 2 的对齐策略

PR #403 review 段 2 已经为 R0 立了通用治理框架（独立 PR，不在 PR #403 主体）：

| 件 | 必须 | 位置 |
|---|---|---|
| 静态守卫 | ✅ | `tools/archtest/{ID}_test.go` 或 `kernel/governance/rules_*.go` |
| 文档契约 | ✅ | godoc 顶部 `// {ID}: ...` + ADR 段 |
| 回归测试 | ✅ | unit/contract/integration ≥1 处 |
| 注册表登记 | ✅ | `kernel/governance/invariants.go::Registry` |

工具：
- `kernel/governance/invariants.go` 中心注册表
- archtest `INVARIANT-REGISTRY-COMPLETENESS-01` 守解析完整
- `gocell check invariants` 命令

**目标**：加新 invariant 成本从"扫 8-12 处"降到"Registry +1 行 + 写四件套"。

### PR #404 与 PR #403 段 2 的边界

**PR #404 不重复立框架**。两条 review 撞同一根因 = 必须并道治理。

| 阶段 | 内容 | 归属 PR |
|------|------|--------|
| **本 PR (#404)** | 表面封堵：typed `GoIdentifier` boundary + 单源 const + 表面 fix（F3-F6） | PR #404，等同 PR #403 段 1 风格 |
| **下一个独立 PR** | 建 `kernel/governance/invariants.go` Registry + 四件套清单 + `gocell check invariants` | PR #403 段 2 通用 framework |
| **段 2 land 后** | 把 PR #404 引入的 schema 类 invariant（GoIdentifier-syntax / AssemblyIDPattern / DeployTemplateEnum / catalog DTO 完整性）入 Registry | PR #404 派生 followup |
| **PR 切片纪律** | `.claude/rules/gocell/pr-slicing.md` + CLAUDE.md 增链 | PR #403 段 3 |

**两线在中期自然汇聚到同一份 `kernel/governance/invariants.go` Registry**，不分裂治理工具链。

---

## 4. 复杂度分级

| Finding | L 级 | Cx | 说明 |
|---------|------|----|------|
| F1 goStructName typed boundary | L2 | Cx2 | typed identifier + 调用方迁移 + injection regression test |
| F2 schema/governance 约束单源 | L2 | Cx2 | const 文件 + consistency archtest + fixture 修复 |
| F3 verify path 复用 codegen.Write | L1 | Cx1 | 直接调用替换，加 path-traversal 测试 |
| F4 catalog DTO 漂移 archtest | L2 | Cx2 | 补字段 + AST archtest 守完整性 |
| F5 CI archtest 加 step | L1 | Cx1 | 数组 +1 |
| F6 DX/help/ADR/sort sweep | L1 | Cx1 | 文档 + sort.Strings |
| **R-meta** | L3 + 流程 | Cx4 | 独立 roadmap，不在本 PR |

---

## 5. 修复方案（4 段）

### 段 1（本 PR 内，必做）— 闭环 F1-F6

**1.1 R1 typed identifier boundary（F1 P1）**

新建 `kernel/metadata/identifier.go`：

```go
// GoIdentifier is a string statically guaranteed to be a valid Go identifier
// (per go/token.IsIdentifier). The unexported underlying field forces all
// construction through NewGoIdentifier, so any value reaching codegen
// templates has been validated.
type GoIdentifier struct{ value string }

func NewGoIdentifier(s string) (GoIdentifier, error) { ... }
func (g GoIdentifier) String() string { return g.value }
func (g GoIdentifier) UnmarshalYAML(...) error { ... } // dispatch to NewGoIdentifier
```

迁移调用方：
- `CellMeta.GoStructName` 类型从 `string` 改为 `GoIdentifier`（yaml unmarshal 即校验）
- `tools/codegen/cellgen/builder.go::BuildCellSpec` 与 `kernel/assembly/generator.go::GenerateModulesGen` 接收 `GoIdentifier`
- archtest `CELL-GOSTRUCTNAME-TYPED-01`：AST 扫描，`CellMeta.GoStructName` 字段类型必须是 `GoIdentifier`，禁止改回 string

注入回归测试 fixture：
```yaml
goStructName: "Foo;package os;func init(){}//"   # 期望 unmarshal 即拒
goStructName: "Foo\n}package main"               # 同上
goStructName: "lowercase"                        # 同上（schema pattern 要求大写起始）
```

**1.2 R2 schema 约束单源（F2 P1）**

新建 `kernel/metadata/contract_constraints.go`：

```go
// authoritative single source for syntactic constraints declared in
// JSON Schemas under kernel/metadata/schemas/. Schema files retain literal
// patterns/enums for IDE consumption; the SchemaConstantConsistencyTest
// asserts they match these constants byte-equal.
const (
    AssemblyIDPattern = `^[a-z][a-z0-9]+$`
    CellIDPattern     = `^[a-z][a-z0-9]+$`
    GoStructNamePattern = `^[A-Z][A-Za-z0-9]*$`
)

var DeployTemplateEnum = []string{"k8s", "compose", "binary"}
```

接入：
- `kernel/metadata/parser.go::parseAssembly` 使用 `regexp.MustCompile(AssemblyIDPattern)` 校验 id（此前 governance 不查）
- governance FMT 规则**不新增** FMT-31/32 重复声明，而是在现有 FMT 路径里引用 const（`MatchString(AssemblyIDPattern, asm.ID)` / `slices.Contains(DeployTemplateEnum, ...)`）
- `kernel/metadata/schemas/schema_const_consistency_test.go`：parse JSON schema → 取 pattern/enum 字面量 → assert 与 Go const byte-equal
- `cmd/gocell/app/mode_test.go:58` fixture `deployTemplate: deploy.yaml` 改 `deployTemplate: k8s`，重写 fixture 注释

**1.3 R-local F3-F6 表面修补**

F3 verify 收口：
- `cmd/gocell/app/codegen_assembly_cmd.go:75-108` 重写：删 `bytes.Equal(got, content)` 分支，统一调用
  ```go
  res, werr := codegen.Write(codegen.WriteOptions{
      Path: outPath, Content: content, RepoRoot: root, Verify: verify,
  })
  ```
- 根据 `res.Action == codegen.ActionDrifted` 决定是否加入 `result.drifted`
- 删 line 93 `//nolint:gosec` 误导注释
- 加测试 `TestVerifyAssembly_RejectsPathEscapesRoot`：fixture `asm.Build.Entrypoint = "../../../../etc/passwd/main.go"` → verify 必须返回 path-escapes 错误

F4 catalog DTO + archtest：
- `runtime/devtools/catalog/wire.go::AssemblySpec` 加字段：
  ```go
  Owner               CellSpecOwner `json:"owner"                         yaml:"owner"`
  MaxConsistencyLevel string        `json:"maxConsistencyLevel,omitempty" yaml:"maxConsistencyLevel,omitempty"`
  ```
- `runtime/devtools/catalog/build.go::buildAssemblyEntity` mapper 同步
- 新建 archtest `CATALOG-ASSEMBLY-FIELD-COVERAGE-01`：AST 扫描 `metadata.AssemblyMeta` 所有 exported 字段（`yaml:"-"` 内部字段除外），要么映射到 `catalog.AssemblySpec`，要么显式 listed in `catalogExcludedAssemblyFields`（with reason comment）
- 单测：catalog round-trip 覆盖两个新字段

F5 CI archtest 同步：
- `tools/archtest/ci_pinning_test.go:587` `codegenStepNames` 加 `"Verify assembly codegen (K#10)"`
- `:614` 起的负面 fixture 同步加该 step 字符串

F6 DX sweep：
- `cmd/gocell/app/help.go:84-92` 重写 generate assembly 描述：明确产出 `cmd/<id>/main.go` + `assemblies/<id>/generated/boundary.yaml` + **`cmd/<id>/modules_gen.go`**；新增 `--all` flag 说明
- `cmd/gocell/app/generate.go::assemblyIDsToGenerate` 末尾 `sort.Strings(ids)`；加单测验证多 assembly fixture 输出顺序稳定
- ADR `:26` `bin/{id}` → `{id}` 与 `assembly_derive.go:55` 实际派生（`asm.Build.Binary = asm.ID`）一致
- ADR `:4` 日期 `2026-05-08` → `2026-05-07`

**1.4 ADR 升级**

`docs/architecture/202605061800-adr-assembly-yaml-minimal-derivation.md` 新增段：

- §"schema 约束单源" — `kernel/metadata/contract_constraints.go` const 是 authoritative，schema 文件 + governance + parser 引用同一 const，archtest 守一致性
- §"派生物 typed boundary" — codegen 输入字段必须是 typed value（`GoIdentifier` etc.），raw string 不得直接进入 template pipeline
- 关联 ref: `docs/reviews/202605070153-pr403-third-wave-review.md` §2.R0（同根表征）

**段 1 完成 = 本 PR ship gate 全绿**

### 段 2（独立 PR，必做，跨 PR）— 派生物治理产品化（PR #403 段 2）

不在 PR #404 实现。引用 PR #403 review §4 段 2 全文。

PR #404 在段 2 framework land 后，把以下 invariant 入 `kernel/governance/invariants.go::Registry`：

| ID | 内容 | 静态守卫 | 文档契约 | 回归测试 |
|----|------|---------|---------|---------|
| `CELL-GOSTRUCTNAME-TYPED-01` | CellMeta.GoStructName 必须是 typed `GoIdentifier` | archtest 同名 | godoc + ADR §typed boundary | injection regression fixture |
| `SCHEMA-CONSTRAINT-CONST-01` | JSON schema pattern/enum 必须与 Go const byte-equal | `schema_const_consistency_test` | godoc + ADR §schema 约束单源 | const round-trip test |
| `CATALOG-ASSEMBLY-FIELD-COVERAGE-01` | metadata.AssemblyMeta 所有 exported field 必须映射到 wire DTO | archtest 同名 | godoc + ADR §catalog 派生 | catalog round-trip |

### 段 3（独立 PR，必做，跨 PR）— PR 切片纪律（PR #403 段 3）

不在 PR #404 实现。引用 PR #403 review §4 段 3。

### 段 4（roadmap，不立即）— 历史 invariant 审计

不在 PR #404 实现。引用 PR #403 review §4 段 4。

---

## 6. 时间盘

| 段 | 估时 | 顺序 | 依赖 |
|---|------|------|------|
| 段 1 | 1 天（~380 LOC） | 立刻 | 无 |
| 段 2 | 1 天 | PR #403 主体 ship 后 | PR #403 段 1 |
| 段 3 | 0.5 天 | 段 2 同周期 | 无 |
| 段 4 | 3-5 天 | 段 2/3 land 后 | 段 2 注册表 |

---

## 7. 结论

**REQUEST_CHANGES — 段 1 必须本 PR 闭环**

- 方向正确不可逆，不应回退
- 段 1 是 PR #404 自身闭环（F1-F6 + ADR 升级）
- 段 2/3/4 是 GoCell 工具链账单，与 PR #403 共享同一根因 R0；**PR #404 不重复立框架**，等待 PR #403 段 2 独立 PR 落地后回头入注 Registry

**本 PR ship 标准**：

- 6 个 finding 全闭，typed boundary archtest + schema-const-consistency archtest + catalog-field-coverage archtest 全绿
- `go run ./cmd/gocell verify codegen-assembly` PASS
- `go test ./...` 全绿（含 `kernel/cell/levelrank` 100% coverage 已就位）
- `go build -tags=integration ./...` 0 errors
- `golangci-lint run --new-from-rev=develop` 0 issues
- ADR `202605061800-adr-assembly-yaml-minimal-derivation.md` 升级 land

---

## 附录 A — O1-O4 上一轮处理回顾

| # | 上一轮裁决 | 处理方式 | 状态 |
|---|-----------|---------|------|
| O1 | by-design closed | 三层守已覆盖（ASSEMBLY-MODULES-GEN-01 + ASSEMBLY-CELLMODULE-TYPE-04 + drift gate），不增第四层 | ✅ |
| O2 | by-design closed | AssemblyMeta.Owner 不进 fingerprint 与 ContractMeta `fingerprint:"-"` 排除 metadata 一致；6 项目对标全部支持（K8s issue #67428 / Cargo PackageId / Bazel Action proto / Terraform identity layer / Cargo authors 排除 / OCI Author 反例） | ✅ |
| O3 | 本 PR 改 | `strings.Contains(err.Error(), ...)` → `require.Error` 单点，对齐 sigs.k8s.io/yaml 风格 | ✅ commit `84701adb` |
| O4 | 本 PR 改 | 新建 `kernel/cell/levelrank` 子包（etcd raftpb / CockroachDB storage/enginepb 模式），消除 inline `consistencyOrder` 与 `levelStrings` 双源 | ✅ commit `84701adb` |

附录 B — 引用文献：见 `docs/reviews/202605070153-pr403-third-wave-review.md` §2.R0（同根表征）；本报告未重复列出业界对标证据。
