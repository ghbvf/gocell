# PR #404 第二轮 review 实施方案

**对应 review**: `docs/reviews/202605070218-pr404-second-wave-review.md`
**对应 PR**: feat/534-assembly-yaml-minimal (HEAD `84701adb`)
**实施范围**: 段 1（本 PR 闭环）— F1-F6 + ADR 升级
**段 2/3/4**: 不在本 PR；引用 PR #403 review 段 2/3/4 由独立 PR 落地
**预估**: ~380 LOC / 18 文件 / 1 天

---

## 0. 总原则与拒绝事项

### 必须遵守

1. **R-meta 同源 PR #403 R0**：本 PR 仅做封堵 + 最小架构整改 + ADR 升级；**不重复立 invariant Registry / 四件套元清单**
2. **PR 切片纪律**：本 PR 不引入超过段 1 范围的工作；不补 PR #403 段 2 应做的工具链产品化
3. **本 PR 内闭环**：每条新 invariant 落地三件套（静态守卫 + 文档契约 + 回归测试），与 `feedback_constraint_self_close` 一致
4. **激进三原则自审 L1/L2/L3**：每条 fix 落地后人工核对（L1 代码补丁 / L2 PR 整体决策组合 / L3 概念模型 ADR 内部一致性）

### 拒绝事项（明确不做）

- ❌ 新建 FMT-31/FMT-32 governance 规则镜像 schema 约束（那是把 2 源变 3 源，反加根因）
- ❌ 在 parser 引入 jsonschema 运行时依赖（架构上 governance 是单守门员，schema 走"派生 + consistency archtest"）
- ❌ 立 `kernel/governance/invariants.go` Registry / `gocell check invariants` CLI / `.claude/rules/gocell/pr-slicing.md`（PR #403 段 2/3 边界）
- ❌ 切 ent / CUE / proto+buf.validate 路线（长期项，非本 PR）
- ❌ 自动从 lowercase id 推导 GoStructName（types.go:30 注释明确放弃，原因正确）

---

## 1. 落地顺序（依赖图）

```
T1 typed identifier  ──┐
T2 schema 约束单源  ──┼──→  T4.4 ADR 升级
T3.1 verify 收口    ──┤      ↓
T3.2 catalog DTO   ──┤   T5 build/test/lint 校验
T3.3 CI archtest   ──┤
T3.4 DX sweep      ──┘
```

**串行约束**：T1 必须先于 T4.4（ADR 引用 typed boundary）。其余可并行。

---

## 2. T1 — typed `GoIdentifier` boundary（F1, P1, Cx2）

### 2.1 目标

把 `cell.GoStructName` 从 raw string 改为 typed value，编译期阻止未校验值进入 codegen pipeline。

### 2.2 文件清单

| 文件 | 操作 | 内容 |
|------|------|------|
| `kernel/metadata/identifier.go` | 新建 | `GoIdentifier` 类型 + 构造函数 + UnmarshalYAML |
| `kernel/metadata/identifier_test.go` | 新建 | 单元测试 + injection regression |
| `kernel/metadata/types.go` | 修改 | `CellMeta.GoStructName` 类型 `string` → `GoIdentifier` |
| `tools/codegen/cellgen/spec.go` | 修改 | `CellGenSpec.StructName` 类型 → `GoIdentifier` 或保留 string + 在 builder 内 .String() |
| `tools/codegen/cellgen/builder.go` | 修改 | `BuildCellSpec` 取值时调 `.String()`；移除独立 syntax 校验（已在类型构造时完成） |
| `kernel/assembly/generator.go` | 修改 | `GenerateModulesGen` 取 `cm.GoStructName.String()` |
| `tools/archtest/cell_gostructname_typed_test.go` | 新建 | archtest `CELL-GOSTRUCTNAME-TYPED-01` |

### 2.3 实现要点

**`kernel/metadata/identifier.go`** 设计：

```go
package metadata

import (
    "fmt"
    "go/token"

    "gopkg.in/yaml.v3"

    "github.com/ghbvf/gocell/pkg/errcode"
)

// GoIdentifier is a string statically guaranteed to be a valid Go identifier
// per go/token.IsIdentifier, plus the project-specific shape constraint
// `^[A-Z][A-Za-z0-9]*$` (must start with an uppercase letter, ASCII only).
//
// The unexported value field forces all construction through NewGoIdentifier
// or UnmarshalYAML, so any GoIdentifier value reaching codegen templates has
// already been validated. Raw strings cannot be coerced to GoIdentifier.
//
// Invariant: CELL-GOSTRUCTNAME-TYPED-01 (tools/archtest/cell_gostructname_typed_test.go)
//   — CellMeta.GoStructName field type must remain GoIdentifier.
type GoIdentifier struct{ value string }

// String returns the underlying identifier text. Safe to embed into Go code
// generation templates.
func (g GoIdentifier) String() string { return g.value }

// IsZero reports whether the identifier is the zero value (cells that opt out
// of K#04 codegen leave goStructName empty in cell.yaml).
func (g GoIdentifier) IsZero() bool { return g.value == "" }

// NewGoIdentifier validates s and returns a typed GoIdentifier. Empty string
// is allowed and returns the zero value (GoIdentifier{}) — callers that
// require a non-empty value must check IsZero separately.
func NewGoIdentifier(s string) (GoIdentifier, error) {
    if s == "" {
        return GoIdentifier{}, nil
    }
    if !token.IsIdentifier(s) {
        return GoIdentifier{}, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
            "goStructName must be a valid Go identifier",
            errcode.WithInternal(fmt.Sprintf("value=%q", s)))
    }
    if !goStructNameRe.MatchString(s) {
        return GoIdentifier{}, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
            "goStructName must match GoStructNamePattern (start with uppercase ASCII letter)",
            errcode.WithInternal(fmt.Sprintf("value=%q pattern=%s", s, GoStructNamePattern)))
    }
    return GoIdentifier{value: s}, nil
}

// UnmarshalYAML is the YAML decoding hook used by parser.go when a CellMeta
// includes goStructName. Validation happens at parse time so downstream
// derivation, governance, and codegen all operate on validated values.
func (g *GoIdentifier) UnmarshalYAML(node *yaml.Node) error {
    var s string
    if err := node.Decode(&s); err != nil {
        return err
    }
    parsed, err := NewGoIdentifier(s)
    if err != nil {
        return err
    }
    *g = parsed
    return nil
}
```

`goStructNameRe` 在 T2 的 `contract_constraints.go` 定义并 export；T1 文件 import 之。

**`CellMeta` 字段迁移**（types.go:32）：

```go
GoStructName GoIdentifier `yaml:"goStructName,omitempty"`
```

**调用方迁移**：
- `cellgen/builder.go:64` `cell.GoStructName == ""` → `cell.GoStructName.IsZero()`
- `cellgen/builder.go:69` `StructName: cell.GoStructName` → `StructName: cell.GoStructName.String()` 或保留 string，在 spec 边界做转换
- `assembly/generator.go:169` `cm.GoStructName == ""` → `cm.GoStructName.IsZero()`
- `assembly/generator.go:174` `cm.GoStructName+"Module"` → `cm.GoStructName.String()+"Module"`
- `kernel/metadata/types.go::CellMeta.Clone` 检查无值类型 deep copy 风险（GoIdentifier 是 plain struct，浅拷贝即可）
- 检查所有 `*_test.go` 用 `GoStructName: "Foo"` 字面量构造的位置 → 改 `mustGoIdent("Foo")` test helper

### 2.4 archtest

**`tools/archtest/cell_gostructname_typed_test.go`**（CELL-GOSTRUCTNAME-TYPED-01）：

AST 扫描 `kernel/metadata/types.go`，找 `CellMeta` struct 定义，确认 `GoStructName` field 的 `Type` AST node 是 `*ast.Ident{Name:"GoIdentifier"}` 而非 `*ast.Ident{Name:"string"}`。失败信息指向 ADR §typed boundary 段。

### 2.5 测试

`identifier_test.go` 表驱动用例：

| input | expect |
|-------|--------|
| `""` | OK, IsZero() |
| `"AccessCore"` | OK |
| `"OrderCell"` | OK |
| `"lowercase"` | error（pattern 拒，GoStructName 必大写起始）|
| `"With Space"` | error |
| `"Foo;package os;func init(){}//"` | error（非 identifier）|
| `"Foo\n}package main"` | error |
| `"Foo-bar"` | error（dash）|
| `"123Foo"` | error（数字起始）|
| `"_Foo"` | error（pattern 要求 ASCII 字母起始）|
| `"日本"` | error（pattern 要求 ASCII）|
| `"σ"` | error（非 ASCII）|

UnmarshalYAML 用例：直接传 yaml.Node，覆盖 valid/invalid 双路径。

集成测试：`parser_test.go` 加 fixture 用 `goStructName: "Foo;package os"` → ParseFS 必返错。

---

## 3. T2 — schema 约束单源（F2, P1, Cx2）

### 3.1 目标

JSON Schema 中的 pattern/enum 字面量与 Go const 单字节级一致；governance/parser/typed-identifier 全部引用同一份 const，未来漂移由 archtest 抓。

### 3.2 文件清单

| 文件 | 操作 | 内容 |
|------|------|------|
| `kernel/metadata/contract_constraints.go` | 新建 | const + 编译期 regex |
| `kernel/metadata/contract_constraints_test.go` | 新建 | const 内部 round-trip |
| `kernel/metadata/schemas/schema_const_consistency_test.go` | 新建 | parse JSON schema → 与 Go const 比对 |
| `kernel/metadata/parser.go` | 修改 | parseAssembly 用 const 校验 id（缺失校验）|
| `kernel/metadata/identifier.go` | 修改 | import GoStructNamePattern + goStructNameRe |
| `kernel/governance/rules_fmt.go` | 修改 | FMT-29 邻近补 deployTemplate enum 引用（**不新增 FMT-31/32**，复用现有规则路径）|
| `cmd/gocell/app/mode_test.go` | 修改 | fixture `deploy.yaml` → `k8s` + 注释重写 |
| `cmd/gocell/app/mode_test.go:426` | 修改 | 同上第二处 fixture |

### 3.3 实现要点

**`kernel/metadata/contract_constraints.go`**:

```go
// Package metadata — contract syntactic constraints (single source of truth).
//
// JSON Schemas under kernel/metadata/schemas/ retain literal pattern/enum
// expressions for IDE / editor / standalone tooling consumption. The
// constants below are the authoritative source: SchemaConstantConsistencyTest
// asserts they match the schema literals byte-for-byte. Adding a new
// syntactic constraint:
//
//   1. add a const here;
//   2. update the corresponding schema file with the same literal;
//   3. wire the const into parser / governance / GoIdentifier as appropriate;
//   4. extend SchemaConstantConsistencyTest to compare the new pair.
//
// Invariant: SCHEMA-CONSTRAINT-CONST-01 — schema literals == Go const.
package metadata

import "regexp"

const (
    AssemblyIDPattern   = `^[a-z][a-z0-9]+$`
    CellIDPattern       = `^[a-z][a-z0-9]+$`
    GoStructNamePattern = `^[A-Z][A-Za-z0-9]*$`
)

// DeployTemplateEnum lists the canonical deployTemplate values accepted by
// assembly.yaml. Order matches the schema enum order; do not reorder without
// updating schemas/assembly.schema.json in lockstep.
var DeployTemplateEnum = []string{"k8s", "compose", "binary"}

var (
    assemblyIDRe   = regexp.MustCompile(AssemblyIDPattern)
    cellIDRe       = regexp.MustCompile(CellIDPattern)
    goStructNameRe = regexp.MustCompile(GoStructNamePattern)
)
```

**parser 接入**（`kernel/metadata/parser.go::parseAssembly` 现有位置后段加）：

```go
if !assemblyIDRe.MatchString(m.ID) {
    return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
        "assembly id must match AssemblyIDPattern",
        errcode.WithInternal(fmt.Sprintf("id=%q pattern=%s file=%s", m.ID, AssemblyIDPattern, file)))
}
if m.Build.DeployTemplate != "" && !slices.Contains(DeployTemplateEnum, m.Build.DeployTemplate) {
    return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
        "assembly deployTemplate must be one of DeployTemplateEnum",
        errcode.WithInternal(fmt.Sprintf("value=%q allowed=%v file=%s", m.Build.DeployTemplate, DeployTemplateEnum, file)))
}
```

**注意**：parser 校验是在 derivation 之前——derivation 把空值默认为 `"k8s"`，所以 enum 校验只对显式声明的非空值生效，与 schema `additionalProperties` 行为一致。

**governance 接入**：FMT-29（owner 必填）邻近 rules_fmt.go 的 assembly 检查段，**不新增规则号**，而是在 FMT-29 处理同一 asm 时调 `assemblyIDRe.MatchString` 与 `slices.Contains(DeployTemplateEnum, ...)`。这样保持单一访问入口，避免规则号膨胀。

**`schema_const_consistency_test.go`**：

```go
package schemas_test

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"

    "github.com/ghbvf/gocell/kernel/metadata"
)

// TestSchemaConstantConsistency verifies that JSON schema files retain literal
// pattern/enum values byte-equal to the Go constants in
// kernel/metadata/contract_constraints.go. Drift in either direction is a hard
// failure: schema is the user-facing contract, constants are the runtime
// authority, both must agree.
func TestSchemaConstantConsistency(t *testing.T) {
    cases := []struct {
        schemaFile string
        path       []string // JSON path to the constraint
        constName  string
        wantPattern string
        wantEnum   []string
    }{
        {"assembly.schema.json", []string{"properties", "id", "pattern"},
            "AssemblyIDPattern", metadata.AssemblyIDPattern, nil},
        {"assembly.schema.json", []string{"properties", "build", "properties", "deployTemplate", "enum"},
            "DeployTemplateEnum", "", metadata.DeployTemplateEnum},
        {"cell.schema.json", []string{"properties", "id", "pattern"},
            "CellIDPattern", metadata.CellIDPattern, nil},
        {"cell.schema.json", []string{"properties", "goStructName", "pattern"},
            "GoStructNamePattern", metadata.GoStructNamePattern, nil},
    }
    for _, tc := range cases { /* parse schema, walk path, compare */ }
}
```

### 3.4 fixture 修复

`cmd/gocell/app/mode_test.go:58`:

```go
// owner required by FMT-29 (K#10); deployTemplate must be a valid enum value.
// Test intent: trigger REF-16 as the lone Warning. The original
// `deployTemplate: deploy.yaml` is now rejected by parser (enum check); using
// "k8s" (the default) keeps the fixture's lone-warning intent intact.
asmYAML := "id: warnasm\n" +
    "cells: []\n" +
    "owner:\n  team: test\n  role: maintainer\n" +
    "build:\n  entrypoint: cmd/warnasm\n  binary: warnasm\n  deployTemplate: k8s\n"
```

`cmd/gocell/app/mode_test.go:426` 同步替换。check `id: my-asm` 字段：现有 fixture 是 `my-asm`（含 dash），与 AssemblyIDPattern `^[a-z][a-z0-9]+$` 冲突——确认现有 FMT-16 是否已拒（看上去仅检查目录名）；如不拒，本 PR 加的 parser 校验会拒，需同步把 fixture 改为 `myasm` 并审视测试意图（如果 fixture 故意触发 FMT-16 那需另寻 test path）。

### 3.5 测试

**注入回归**（在 T1 用例之外补）：
- 用 `id: My-Asm` fixture → 期 ParseFS 返错
- 用 `deployTemplate: kustomize` fixture → 期 ParseFS 返错
- 用 `deployTemplate: ""` fixture → 期 OK（derivation 填 default `"k8s"`）

---

## 4. T3 — F3-F6 表面修补

### 4.1 T3.1 — verify path 收口（F3, P2, Cx1）

**文件**：

| 文件 | 操作 |
|------|------|
| `cmd/gocell/app/codegen_assembly_cmd.go` | 重写 verify 分支 |
| `cmd/gocell/app/codegen_assembly_cmd_test.go` | 新建（如不存在）或扩展 |

**重写**（line 75-108）：

```go
func processOneAssemblyModulesGen(
    root string, gen *assembly.Generator,
    asmID string, asm *metadata.AssemblyMeta,
    verify bool, result *assemblyDriftResult,
) error {
    content, err := gen.GenerateModulesGen(asmID)
    if err != nil {
        return fmt.Errorf("regenerate modules_gen %s: %w", asmID, err)
    }

    entrypointRel := asm.Build.Entrypoint
    if entrypointRel == "" {
        entrypointRel = filepath.Join("cmd", asmID, "main.go")
    }
    outPath := filepath.Join(root, filepath.Dir(entrypointRel), "modules_gen.go")
    relPath := filepath.Join(filepath.Dir(entrypointRel), "modules_gen.go")

    res, werr := codegen.Write(codegen.WriteOptions{
        Path:     outPath,
        Content:  content,
        RepoRoot: root,
        Verify:   verify,
    })
    if werr != nil {
        if verify {
            return fmt.Errorf("verify modules_gen %s: %w", asmID, werr)
        }
        return fmt.Errorf("write modules_gen %s: %w", asmID, werr)
    }
    switch res.Action {
    case codegen.ActionDrifted:
        result.drifted = append(result.drifted, relPath)
    case codegen.ActionWritten, codegen.ActionUnchanged:
        if !verify {
            result.generated = append(result.generated, relPath)
        }
    }
    return nil
}
```

删除 `bytes` import（如失去其它引用），删 line 93 `//nolint:gosec` 注释。

**测试** `TestVerifyAssembly_RejectsPathEscapesRoot`：构造 ProjectMeta 含 `asm.Build.Entrypoint = "../../../../etc/passwd/main.go"` → `runVerifyCodegenAssembly` 必须返回 path-escapes 错误，不读真实文件。

### 4.2 T3.2 — catalog DTO + 完整性 archtest（F4, P2, Cx2）

**文件**：

| 文件 | 操作 |
|------|------|
| `runtime/devtools/catalog/wire.go` | 加字段 |
| `runtime/devtools/catalog/build.go` | mapper 同步 |
| `runtime/devtools/catalog/build_test.go` | 测试覆盖新字段 |
| `tools/archtest/catalog_assembly_field_coverage_test.go` | 新建 archtest |

**wire.go AssemblySpec**（line 256）：

```go
type AssemblySpec struct {
    Cells               []string          `json:"cells,omitempty"               yaml:"cells,omitempty"`
    Owner               CellSpecOwner     `json:"owner"                         yaml:"owner"`
    MaxConsistencyLevel string            `json:"maxConsistencyLevel,omitempty" yaml:"maxConsistencyLevel,omitempty"`
    Build               AssemblySpecBuild `json:"build"                         yaml:"build"`
}
```

注意：`Owner` 复用 `CellSpecOwner`（已存在 `Team`/`Role` 字段），无需新建类型。

**build.go buildAssemblyEntity**（line 320）：

```go
func buildAssemblyEntity(a *metadata.AssemblyMeta, inc IncludeOptions) Entity {
    spec := AssemblySpec{
        Cells:               a.Cells,
        Owner:               CellSpecOwner{Team: a.Owner.Team, Role: a.Owner.Role},
        MaxConsistencyLevel: a.MaxConsistencyLevel,
        Build: AssemblySpecBuild{
            Entrypoint:     a.Build.Entrypoint,
            Binary:         a.Build.Binary,
            DeployTemplate: a.Build.DeployTemplate,
        },
    }
    // ...
}
```

**archtest** `CATALOG-ASSEMBLY-FIELD-COVERAGE-01`：

```go
// CATALOG-ASSEMBLY-FIELD-COVERAGE-01: every exported field on
// metadata.AssemblyMeta (excluding parser-internal yaml:"-" fields)
// must either be mapped onto runtime/devtools/catalog.AssemblySpec or be
// listed in catalogExcludedAssemblyFields with a documented reason.
//
// Adding a new AssemblyMeta field without wire DTO sync triggers this
// archtest, preventing the "metadata extends but catalog stays stale"
// drift class identified in PR #404 review.
func TestCatalogAssemblyFieldCoverage(t *testing.T) { ... }

// catalogExcludedAssemblyFields lists AssemblyMeta exported fields that are
// intentionally not surfaced via wire DTO. Each entry must have a reason.
var catalogExcludedAssemblyFields = map[string]string{
    // Dir/File are parser-internal location metadata, exposed via
    // Entity.Metadata.File rather than Spec.
    // (yaml:"-" already excludes them from YAML round-trip; archtest also
    //  ignores them by inspecting the yaml tag, this map is for fields that
    //  WOULD round-trip but should still not appear in the DTO.)
}
```

archtest 实现：AST parse `kernel/metadata/types.go`，找 `AssemblyMeta` struct，遍历 fields；过滤 yaml tag `"-"`；对剩余 field name 检查是否在 `AssemblySpec`（parse `runtime/devtools/catalog/wire.go`）的 field set 中，或在 excluded map 中。

**测试**：catalog round-trip 测试（build_test.go 对应位置）覆盖 Owner / MaxConsistencyLevel 字段：

```go
pm.Assemblies["mainbundle"] = &metadata.AssemblyMeta{
    ID: "mainbundle",
    Cells: []string{"realcell"},
    Owner: metadata.OwnerMeta{Team: "platform", Role: "bundle-owner"},
    MaxConsistencyLevel: "L2",
    Build: metadata.BuildMeta{Entrypoint: "cmd/mainbundle/main.go", Binary: "mainbundle", DeployTemplate: "k8s"},
}
// ... build catalog, assert spec.Owner == ..., spec.MaxConsistencyLevel == "L2"
```

### 4.3 T3.3 — CI archtest 同步（F5, P2, Cx1）

**文件**: `tools/archtest/ci_pinning_test.go`

**改 line 587-591**:

```go
var codegenStepNames = []string{
    "Verify generated artifacts are up-to-date",
    "Verify cell codegen (K#04)",
    "Verify contract codegen (K#06)",
    "Verify assembly codegen (K#10)",
}
```

**改 line 614-627** 负面 fixture：

```go
body := []byte(`jobs:
  build-test:
    steps:
      - name: Verify generated artifacts are up-to-date
        if: matrix.static_checks
        run: go run ./cmd/gocell verify generated
      - name: Verify cell codegen (K#04)
        if: matrix.static_checks
        run: ./hack/verify-codegen-cell.sh
      - name: Verify contract codegen (K#06)
        if: matrix.static_checks
        run: ./hack/verify-codegen-contract.sh
      - name: Verify assembly codegen (K#10)
        if: matrix.static_checks
        run: ./hack/verify-codegen-assembly.sh
`)
```

注释段同步从"three steps"改"four steps"。

### 4.4 T3.4 — DX/help/ADR sweep（F6, P2, Cx1）

**help.go:84-92** 重写 generate assembly entry：

```go
{"assembly", []string{
    "Generate the assembly entrypoint cmd/<id>/main.go,",
    "assemblies/<id>/generated/boundary.yaml, and",
    "cmd/<id>/modules_gen.go (the cell→Module factory).",
    "Generated files are owned by gocell. Hand-written",
    "helpers may live in cmd/<id>/run.go etc., but",
    "cmd/<id>/main.go and cmd/<id>/modules_gen.go must",
    "carry the gocell generated header or generation aborts",
    "to protect your edits.",
    "--id=<assemblyID> | --all [--module=<module>]",
}},
```

**generate.go:108-112** 加排序：

```go
func assemblyIDsToGenerate(project *metadata.ProjectMeta, id string, all bool) []string {
    if !all {
        return []string{id}
    }
    ids := make([]string, 0, len(project.Assemblies))
    for asmID := range project.Assemblies {
        ids = append(ids, asmID)
    }
    sort.Strings(ids)
    return ids
}
```

加 import `"sort"`（如已有则不加）。

**测试**新建 `TestAssemblyIDsToGenerate_AllSorted`：构造 ProjectMeta 含 3 个乱序 assembly id（如 `zeta` / `alpha` / `mu`），验证返回切片严格升序。

**ADR 修复** `docs/architecture/202605061800-adr-assembly-yaml-minimal-derivation.md`：

- line 4 `> Date: 2026-05-08` → `> Date: 2026-05-07`
- line 26 `从 id 推导 build.entrypoint（cmd/{id}/main.go）和 build.binary（bin/{id}）` → `从 id 推导 build.entrypoint（cmd/{id}/main.go）和 build.binary（{id}）`
- 同时核查文中其他 `bin/{id}` 引用，统一删除 `bin/` 前缀

### 4.5 T4.4 — ADR 升级（接续 T1+T2）

`docs/architecture/202605061800-adr-assembly-yaml-minimal-derivation.md` 新增段（在 §archtest 守卫之前插入）：

```markdown
## Schema 约束单源 (SCHEMA-CONSTRAINT-CONST-01)

`kernel/metadata/contract_constraints.go` 中的 const 是 schema 类语法约束的
authoritative source。`kernel/metadata/schemas/*.json` 文件保留字面量用于 IDE
/ 编辑器消费，由 `kernel/metadata/schemas/schema_const_consistency_test.go`
验证字面量与 const 字节级一致。Parser、governance、typed identifier
boundary（GoIdentifier）全部引用同一份 const，不再独立写正则或 enum 列表。

加新约束的步骤：
1. `contract_constraints.go` 加 const；
2. 同步更新 `schemas/*.json`；
3. parser/governance/typed-identifier 引用新 const；
4. `schema_const_consistency_test.go` 加比对项。

R-meta 同根 PR #403 R0：本 PR 仅做 schema 字段切面的最小封堵；
通用治理框架（`kernel/governance/invariants.go::Registry` + 四件套清单）
留给 PR #403 段 2 独立 PR 落地，避免与 PR #404 工具链重复。

## 派生物 typed boundary (CELL-GOSTRUCTNAME-TYPED-01)

进入 codegen template 的字段必须是 typed value，raw string 不得直接拼接进
Go 源代码模板。`kernel/metadata/identifier.GoIdentifier` 是该 boundary 的
首批实现：

- 私有 `value` 字段 + 仅 `NewGoIdentifier` / `UnmarshalYAML` 构造路径
- 校验串接 `go/token.IsIdentifier` + `GoStructNamePattern`
- `CellMeta.GoStructName` 已迁移
- archtest `CELL-GOSTRUCTNAME-TYPED-01` 守字段类型不被改回 string

未来 codegen 输入字段（如新增 `cell.yaml` field 进入 template）必须采用
typed boundary，不得绕过 raw string 拼接。
```

文末"## archtest 守卫"段追加：

```markdown
- **CELL-GOSTRUCTNAME-TYPED-01**：CellMeta.GoStructName 字段类型必须是
  GoIdentifier；防止改回 raw string 绕过 codegen-input 校验。
- **SCHEMA-CONSTRAINT-CONST-01**：JSON schema pattern/enum 字面量必须与
  kernel/metadata/contract_constraints.go const 字节级一致。
- **CATALOG-ASSEMBLY-FIELD-COVERAGE-01**：metadata.AssemblyMeta 所有
  exported field 必须映射到 runtime/devtools/catalog.AssemblySpec 或在
  catalogExcludedAssemblyFields 中显式排除（含 reason）。
```

---

## 5. T5 — 校验与提交

### 5.1 校验序列

**每个 batch 收口后**：

```bash
go build ./...
go build -tags=integration ./...
go test ./kernel/... ./tools/archtest/... ./runtime/devtools/catalog/... ./cmd/gocell/...
golangci-lint run --new-from-rev=develop
```

**最终全量校验**（推 PR 前）：

```bash
# 严格对照 .github/workflows/_build-lint.yml 的 integration-test job 命令
# (memory: feedback_ci_exact_integration_scope)
go build -tags=integration ./...
go test -tags=integration -timeout=10m ./...

# verify gates
go run ./cmd/gocell verify generated
./hack/verify-codegen-cell.sh
./hack/verify-codegen-contract.sh
./hack/verify-codegen-assembly.sh

# lint
golangci-lint run --new-from-rev=develop ./...
```

### 5.2 commit 切片

按依赖图 4-5 个 commit：

| commit | 内容 |
|--------|------|
| 1 | T2 contract_constraints.go + schema_const_consistency_test + parser/governance 接入 + fixture 修复 |
| 2 | T1 GoIdentifier + types.go 字段迁移 + 调用方迁移 + injection regression test + archtest CELL-GOSTRUCTNAME-TYPED-01 |
| 3 | T3.1 verify path 收口 + path-traversal regression |
| 4 | T3.2 catalog DTO + archtest CATALOG-ASSEMBLY-FIELD-COVERAGE-01 + round-trip test |
| 5 | T3.3 + T3.4 + T4.4 ADR 升级 |

每个 commit 严格遵循 Conventional Commits + ref 注释（kubebuilder marker / buf.validate / ent / etc.，根据 commit 内容选）。

### 5.3 PR description 必须包含

- review 引用：`docs/reviews/202605070218-pr404-second-wave-review.md`
- plan 引用：`docs/plans/202605070218-031-pr404-second-wave-implementation.md`
- F1-F6 闭环说明
- R-meta 同源 PR #403 R0 的边界声明（"本 PR 不立 invariant Registry / 切片纪律"）

---

## 6. 风险与回滚

### 6.1 风险点

1. **`CellMeta.GoStructName` 类型切换**：所有持有 cell.yaml 的位置（cells/* + examples/*）unmarshal 路径会重新走 GoIdentifier 校验。现有 5 处 `goStructName` 全部符合 pattern（已确认：AccessCore / AuditCore / ConfigCore / DeviceCell / OrderCell），不应触发回归。但要全量跑 `go test` 看 fixture 文件是否有问题。

2. **fixture 依赖 `deploy.yaml` deployTemplate 值**：mode_test.go 两处 fixture（line 58 + 426）依赖该非法值。修复后可能影响 fixture 的测试意图——需重读测试断言看是否原本依赖该 fixture 触发 schema 之外的某个分支。如果是，fixture 改 `k8s` 后该 case 静默通过，会丢失测试覆盖。**缓解**：通读 mode_test 的测试目的，必要时另起 fixture（用 `id: my-asm` 触发 dash 拒绝路径）。

3. **archtest CATALOG-ASSEMBLY-FIELD-COVERAGE-01 误报面**：AssemblyMeta 含 `Dir` / `File` 等 yaml:"-" parser-internal 字段，必须在 archtest 实现里精确过滤（按 yaml tag 而非 field name 排除），避免误报。

4. **PR #403 段 2 land 时可能要重命名 archtest**：如果段 2 Registry 要求统一 ID 命名规范，当前命名 `CELL-GOSTRUCTNAME-TYPED-01` / `SCHEMA-CONSTRAINT-CONST-01` / `CATALOG-ASSEMBLY-FIELD-COVERAGE-01` 可能要调整。**缓解**：命名已尽量对齐 PR #403 review §4 段 2 推荐风格（`{DOMAIN}-{ASPECT}-{NN}`），段 2 land 时如有改动只需 sed 替换。

### 6.2 回滚

T1/T2 类型切换不可回滚到混用状态——必须全部 land 或全部回退。回退路径：

- 删 `kernel/metadata/identifier.go` + `contract_constraints.go`
- `CellMeta.GoStructName` 改回 `string`
- 调用方 `.String()` / `.IsZero()` 还原
- archtest 三个文件删除
- 单 commit revert 操作

T3 各项独立可回滚。

---

## 7. 完工标志（Definition of Done）

- [ ] T1 GoIdentifier + 调用方迁移 + archtest CELL-GOSTRUCTNAME-TYPED-01 + injection regression land
- [ ] T2 contract_constraints.go + schema_const_consistency_test land；fixture deployTemplate 修复
- [ ] T3.1 verify 复用 codegen.Write + path-traversal regression land
- [ ] T3.2 AssemblySpec.Owner/MaxConsistencyLevel + archtest CATALOG-ASSEMBLY-FIELD-COVERAGE-01 + round-trip test land
- [ ] T3.3 codegenStepNames 含 K#10 step land
- [ ] T3.4 help/sort/ADR bin/{id}/date sweep land
- [ ] T4.4 ADR 升级 land（schema 约束单源段 + typed boundary 段 + 三条 archtest 列表）
- [ ] `go test ./...` 全绿
- [ ] `go build -tags=integration ./...` 0 errors
- [ ] `go run ./cmd/gocell verify codegen-assembly` PASS
- [ ] `golangci-lint run --new-from-rev=develop` 0 issues
- [ ] PR description 包含 review + plan 引用 + R-meta 边界声明
