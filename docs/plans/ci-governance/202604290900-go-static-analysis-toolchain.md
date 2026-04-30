# Go 生态静态分析工具调研

> 日期：2026-04-30
> 任务：为 GoCell 31 条 CI 治理候选（见 `202604290858-backlog2-ci-governance-analysis.md`）提供工具栈对照与可直接落地的配置
> 调研者：explorer agent（基于 Web 文档 + 工具最新版本）
> 关联文件：`CLAUDE.md`、`../../backlog2.md`、`202604290858-backlog2-ci-governance-analysis.md`、`202604300430-golangci-tier12-priority-and-projection.md`（决策文档）

---

## §1 工具矩阵

### 必启（在 GoCell CI 必须运行）

| 工具 | 版本 | 用途 | GoCell 对标条目 | 性价比 |
|------|------|------|----------------|--------|
| golangci-lint | v2.11.4 | 聚合 linter 运行器 | 全部 | 高 |
| errcheck | built-in | 未检查错误返回值 | B2-K-07、B2-A-21 | 高 |
| errorlint | built-in | fmt.Errorf %w 合规 | B2-A-21 | 高 |
| govet | built-in | printf 格式字面量、shadow | B2-A-21 | 高 |
| gosec | built-in | G401/G404 弱加密、G104 error 未处理 | B2-A-19 Insecure=true | 高 |
| gocognit | built-in | 认知复杂度 ≤15（CLAUDE.md） | CLAUDE.md | 高 |
| depguard | built-in | 跨层 import 守护 | B2-C-03、B2-A-20 | 高 |
| forbidigo | built-in | Must*、time.Sleep、fmt.Println 禁用 | B2-K-02、B2-X-01 | 高 |
| nilerr | built-in | `if err != nil { return nil }` | B2-A-11 构造器 nil 泄漏 | 高 |
| nilnil | built-in | 同时返回 nil, nil | B2-A-11 | 高 |
| wrapcheck | built-in | 外部包错误必须包装 | B2-K-04 errcode 单源 | 高 |
| musttag | built-in | Marshal struct 必须有 tag | B2-T-04 camelCase 漂移 | 高 |
| tagliatelle | built-in | struct tag 命名 camelCase | B2-T-04 | 高 |
| govulncheck | v1.x @latest | CVE 扫描 | 依赖安全 | 高 |
| paralleltest | built-in | 测试必须 t.Parallel() | B2-A-24/29 race | 中 |
| testifylint | built-in | testify 断言正确性 | 测试质量 | 高 |
| staticcheck | built-in | 综合静态分析 | 全部 | 高 |
| unused | built-in | 未使用导出 | 清洁度 | 高 |

### 按需（场景驱动启用）

| 工具 | 版本 | 用途 | GoCell 对标条目 | 性价比 |
|------|------|------|----------------|--------|
| uber NilAway | v0.0.0-20260318 | 跨包 nil 传播推断 | B2-A-19 nil 守护 | 中（false positive 多） |
| revive | built-in | 替代 golint，可自定义规则 | 代码风格一致 | 中 |
| gocritic | built-in | 30+ 风格/性能/安全 checker | 综合 | 中 |
| dupl | built-in | 重复代码块 | 重构发现 | 中 |
| goconst | built-in | 魔法字符串提常量 | B2-K-04 errcode 字符串 | 中 |
| prealloc | built-in | slice 预分配 | 性能 | 中 |
| makezero | built-in | make([]T, n) 后 append 错误 | 性能 | 中 |
| thelper | built-in | 测试 helper 必须调 t.Helper() | 测试质量 | 中 |
| gofumpt | built-in | 格式化（比 gofmt 更严格） | 格式统一 | 中 |
| semgrep | v1.x | 跨语言规则，YAML/Go 混合 | B2-T-01/03/04/05/08 | 中 |
| conftest+opa | latest | contract.yaml 字段治理 | B2-T-01/04/05/08 | 中 |
| CodeQL for Go | GitHub Action | 深度安全分析 | security | 中 |

### 评估中（谨慎引入）

| 工具 | 原因 | 建议 |
|------|------|------|
| wsl（whitespace linter）| 风格强制过严，团队需统一适应 | 先在单包试点 |
| exhaustruct | 强制初始化所有字段，与 functional options 冲突 | 不建议全局 |
| varnamelen | 对短循环变量 i/j 误报 | 在 kernel/ 单独试 |
| nlreturn | 换行风格强制，团队争议大 | 不建议 |
| ireturn | 接口返回类型守护，与 GoCell adapter 模式有冲突 | 按包白名单 |

---

## §2 推荐 .golangci.yml 完整配置

```yaml
# .golangci.yml — GoCell 静态分析配置（golangci-lint v2.11.4）
version: "2"

linters:
  default: none  # v2 新语法，替代 disable-all
  enable:
    # --- 必启：错误处理 ---
    - errcheck        # 未检查错误返回
    - errorlint       # fmt.Errorf 必须用 %w；禁止 errors.Is 外直接比较
    - nilerr          # if err != nil { return nil } 陷阱
    - nilnil          # 同时 return nil, nil
    - wrapcheck       # 第三方包错误必须用 fmt.Errorf("%w") 包装

    # --- 必启：安全 ---
    - gosec           # G101 hardcoded cred, G104 error 未处理, G401/G404 弱加密

    # --- 必启：复杂度（CLAUDE.md ≤15）---
    - gocognit        # 认知复杂度
    - gocyclo         # 圈复杂度

    # --- 必启：跨层 import 守护 ---
    - depguard        # cells/ 不能 import adapters/（见 settings）

    # --- 必启：禁用模式 ---
    - forbidigo       # Must*, time.Sleep in tests, fmt.Println

    # --- 必启：struct tag 一致性（B2-T-04）---
    - musttag         # Marshal 结构体必须有 tag
    - tagliatelle     # JSON/YAML tag 必须 camelCase

    # --- 必启：静态分析基线 ---
    - govet           # printf 格式、shadow、copylocks 等
    - staticcheck     # S1xxx/SA1xxx 综合检查
    - unused          # 未使用导出标识符

    # --- 必启：测试质量 ---
    - paralleltest    # 测试必须声明 t.Parallel()
    - testifylint     # testify 断言语义正确
    - thelper         # helper 函数必须 t.Helper()

    # --- 按需：风格 ---
    - gofumpt         # 格式化（比 gofmt 更严格）
    - goconst         # 魔法字符串提常量（≥3 次）
    - misspell        # 拼写检查

    # --- 按需：性能 ---
    - prealloc        # slice 预分配建议
    - makezero        # make + append 误用

settings:
  # ---- 复杂度阈值（CLAUDE.md 限制）----
  gocognit:
    min-complexity: 15  # 超过 15 报错

  gocyclo:
    min-complexity: 15

  # ---- struct tag camelCase（B2-T-04）----
  tagliatelle:
    rules:
      json: camel     # JSON tag 必须 camelCase
      yaml: camel     # YAML tag 必须 camelCase（contract 字段）

  musttag:
    functions:
      - name: encoding/json.Marshal
        tag: json
      - name: encoding/json.Unmarshal
        tag: json
      - name: gopkg.in/yaml.v3.Marshal
        tag: yaml
      - name: gopkg.in/yaml.v3.Unmarshal
        tag: yaml

  # ---- 错误处理 ----
  wrapcheck:
    ignoreSigs:
      # GoCell 内部包之间不要求重复包装
      - .Errorf(
      - errors.New(
      - errors.Unwrap(
    ignorePackageGlobs:
      - github.com/YOUR_ORG/gocell/pkg/errcode  # 单源，已是规范包

  errcheck:
    check-type-assertions: true  # 类型断言失败也检查
    check-blank: true            # 不允许 _ = f()

  errorlint:
    errorf: true          # 强制 %w
    errorf-multi: true    # 多错误也要 %w
    asserts: true
    comparison: true

  # ---- 安全 ----
  gosec:
    severity: medium
    confidence: medium
    excludes:
      - G304  # 文件路径非字面量（scaffold 工具合理使用）
    includes:
      - G101  # hardcoded credentials
      - G104  # error 未检查（补充 errcheck）
      - G401  # 弱哈希 MD5/SHA1
      - G404  # math/rand 弱随机
      - G501  # 不安全加密导入

  # ---- 禁用模式（forbidigo）----
  forbidigo:
    forbid:
      # B2-K-02：生产路径禁止 Must* 系列（cmd/ 和 _test.go 豁免见 exclude）
      - pattern: "^Must[A-Z]"
        msg: "Must* functions may panic; use New* with error return instead. Allowed only in cmd/ and *_test.go"
      # B2-X-01：测试中禁止 time.Sleep（用 channel/ticker）
      - pattern: "time\\.Sleep"
        msg: "time.Sleep in tests creates flaky tests; use sync primitives or test helpers with timeout"
      # CLAUDE.md：禁止 fmt.Println / fmt.Printf（用 slog）
      - pattern: "fmt\\.Print(ln|f|)\\("
        msg: "Use slog for structured logging instead of fmt.Print*"
    analyze-types: true
    exclude-godoc-examples: true

  # ---- 跨层 import 守护（B2-C-03）----
  depguard:
    rules:
      # kernel/ 只依赖标准库 + pkg/ + gopkg.in/yaml.v3
      kernel_no_runtime:
        files:
          - "**/kernel/**/*.go"
          - "!**/kernel/**/*_test.go"
        deny:
          - pkg: "github.com/YOUR_ORG/gocell/runtime"
            desc: "kernel/ must not import runtime/; define interfaces in kernel instead"
          - pkg: "github.com/YOUR_ORG/gocell/adapters"
            desc: "kernel/ must not import adapters/"
          - pkg: "github.com/YOUR_ORG/gocell/cells"
            desc: "kernel/ must not import cells/"
      # cells/ 不能直接 import adapters/（B2-C-03）
      cells_no_adapters:
        files:
          - "**/cells/**/*.go"
          - "!**/cells/**/*_test.go"
        deny:
          - pkg: "github.com/YOUR_ORG/gocell/adapters"
            desc: "cells/ must use port interfaces; importing adapters/ directly violates DIP. B2-C-03"
      # runtime/ 不能 import cells/ 或 adapters/
      runtime_isolation:
        files:
          - "**/runtime/**/*.go"
          - "!**/runtime/**/*_test.go"
        deny:
          - pkg: "github.com/YOUR_ORG/gocell/cells"
            desc: "runtime/ must not import cells/"
          - pkg: "github.com/YOUR_ORG/gocell/adapters"
            desc: "runtime/ must not import adapters/"
      # B2-A-20：simpleTracer 禁止在生产路径
      no_simple_tracer:
        files:
          - "**/cells/**/*.go"
          - "**/adapters/**/*.go"
          - "!**/*_test.go"
        deny:
          - pkg: "github.com/YOUR_ORG/gocell/runtime/observability"
            desc: "Use injected TracerProvider; do not reference simpleTracer directly. B2-A-20"

  # ---- struct tag 风格 ----
  revive:
    rules:
      - name: exported
        severity: warning
      - name: var-naming
        severity: warning
      - name: error-return
        severity: error
      - name: error-naming
        severity: error

  # ---- 测试质量 ----
  paralleltest:
    ignore-missing: false  # 必须显式 t.Parallel()

  testifylint:
    enable-all: true
    disable:
      - float-compare  # 业务逻辑中有合理浮点比较

  # ---- gocritic（按需启用子集）----
  gocritic:
    enabled-checks:
      - appendAssign
      - appendCombine
      - assignOp
      - badCall
      - badCond
      - captLocal
      - codegenComment
      - commentFormatting
      - deprecatedComment
      - dupArg
      - dupBranchBody
      - dupCase
      - dupSubExpr
      - emptyFallthrough
      - emptyStringTest
      - equalFold
      - evalOrder
      - exitAfterDefer
      - flagName
      - hugeParam       # 大结构体传值警告
      - importShadow
      - initClause
      - methodExprCall
      - nestingReduce
      - newDeref
      - nilValReturn
      - paramTypeCombine
      - ptrToRefParam
      - rangeExprCopy
      - rangeValCopy
      - regexpMust
      - sloppyLen
      - stringXbytes
      - typeAssert
      - typeSwitchVar
      - typeUnparen
      - underef
      - unlabelStmt
      - unslice
      - valSwap
      - weakCond
      - whyNoLint

issues:
  exclude-rules:
    # cmd/ 和 _test.go 允许 Must* 和 time.Sleep
    - path: "cmd/"
      linters: [forbidigo]
    - path: "_test\\.go"
      linters: [forbidigo, wrapcheck, paralleltest]
    # generated/ 代码不扫
    - path: "generated/"
      linters: [all]
    # examples/ 降级扫描
    - path: "examples/"
      linters: [wrapcheck, depguard]
  max-issues-per-linter: 0
  max-same-issues: 0

run:
  timeout: 10m
  go: "1.24"
```

---

## §3 自定义 Analyzer Skeleton

### 3a. `must-not-call` Analyzer — 生产路径禁止调用 `Must*`

```go
// File: cmd/gocell-analyze/analyzers/mustnotcall/mustnotcall.go
package mustnotcall

import (
    "go/ast"
    "strings"

    "golang.org/x/tools/go/analysis"
    "golang.org/x/tools/go/analysis/passes/inspect"
    "golang.org/x/tools/go/ast/inspector"
)

var Analyzer = &analysis.Analyzer{
    Name:     "mustnotcall",
    Doc:      "prohibits Must* function calls in production paths (cells/, runtime/, adapters/); allowed in cmd/ and *_test.go",
    Requires: []*analysis.Analyzer{inspect.Analyzer},
    Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
    // 跳过测试文件（_test.go）
    inTest := false
    for _, f := range pass.Files {
        name := pass.Fset.File(f.Pos()).Name()
        if strings.HasSuffix(name, "_test.go") {
            inTest = true
        }
    }
    if inTest {
        return nil, nil
    }

    // 跳过 cmd/ 包（允许 panic-on-startup 模式）
    pkgPath := pass.Pkg.Path()
    if strings.Contains(pkgPath, "/cmd/") {
        return nil, nil
    }

    insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
    nodeFilter := []ast.Node{(*ast.CallExpr)(nil)}

    insp.Preorder(nodeFilter, func(n ast.Node) {
        call := n.(*ast.CallExpr)
        var fnName string
        switch fn := call.Fun.(type) {
        case *ast.Ident:
            fnName = fn.Name
        case *ast.SelectorExpr:
            fnName = fn.Sel.Name
        default:
            return
        }
        if strings.HasPrefix(fnName, "Must") {
            pass.Reportf(call.Pos(),
                "Must* function %q may panic; use New* with error return in production paths (B2-K-02)",
                fnName)
        }
    })
    return nil, nil
}
```

### 3b. `errcode-mirror` Analyzer — 确保治理表与 errcode 同步

```go
// File: cmd/gocell-analyze/analyzers/errcodemirror/errcodemirror.go
// 策略：扫 pkg/errcode 包导出的所有常量，与 kernel/governance/rules_http_response_alignment.go
// 中的 errcodeNameToStatus map 做集合对比；发现漂移即 Fail。
package errcodemirror

import (
    "go/ast"
    "go/token"
    "strings"

    "golang.org/x/tools/go/analysis"
    "golang.org/x/tools/go/analysis/passes/inspect"
    "golang.org/x/tools/go/ast/inspector"
)

// errcodeConst は Fact：记录 errcode 包导出的常量集合
type errcodeConst struct{ Names map[string]bool }

func (*errcodeConst) AFact() {}

var Analyzer = &analysis.Analyzer{
    Name:      "errcodemirror",
    Doc:       "checks that kernel/governance errcodeNameToStatus map contains exactly the codes in pkg/errcode (B2-K-04)",
    Requires:  []*analysis.Analyzer{inspect.Analyzer},
    FactTypes: []analysis.Fact{(*errcodeConst)(nil)},
    Run:       run,
}

func run(pass *analysis.Pass) (any, error) {
    pkgPath := pass.Pkg.Path()

    // Phase 1：在 pkg/errcode 包中收集所有导出常量（形如 ERR_*）
    if strings.HasSuffix(pkgPath, "pkg/errcode") {
        insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
        names := map[string]bool{}
        insp.Preorder([]ast.Node{(*ast.ValueSpec)(nil)}, func(n ast.Node) {
            vs := n.(*ast.ValueSpec)
            for _, id := range vs.Names {
                if strings.HasPrefix(id.Name, "ERR_") && id.IsExported() {
                    names[id.Name] = true
                }
            }
        })
        pass.ExportPackageFact(&errcodeConst{Names: names})
        return nil, nil
    }

    // Phase 2：在 governance 包中找 errcodeNameToStatus map 字面量，对比 Phase 1 结果
    if !strings.Contains(pkgPath, "kernel/governance") {
        return nil, nil
    }

    var errcodeFact errcodeConst
    // 从依赖包导入 Fact（需在 Requires 中添加 errcode 包的 analyzer）
    // 简化：直接遍历 pass.Pkg.Imports() 找到 errcode 导出的 Fact
    for _, imp := range pass.Pkg.Imports() {
        if strings.HasSuffix(imp.Path(), "pkg/errcode") {
            pass.ImportPackageFact(imp, &errcodeFact)
        }
    }
    if len(errcodeFact.Names) == 0 {
        return nil, nil // errcode 包尚未分析，跳过
    }

    insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
    govCodes := map[string]bool{}
    var mapLitPos token.Pos

    insp.Preorder([]ast.Node{(*ast.CompositeLit)(nil)}, func(n ast.Node) {
        cl := n.(*ast.CompositeLit)
        // 找名为 errcodeNameToStatus 的 map 字面量
        for _, elt := range cl.Elts {
            kv, ok := elt.(*ast.KeyValueExpr)
            if !ok {
                continue
            }
            key, ok := kv.Key.(*ast.Ident)
            if !ok {
                continue
            }
            if strings.HasPrefix(key.Name, "ERR_") {
                govCodes[key.Name] = true
                mapLitPos = cl.Pos()
            }
        }
    })

    // 差集报告
    for code := range errcodeFact.Names {
        if !govCodes[code] {
            pass.Reportf(mapLitPos,
                "errcode %q exists in pkg/errcode but missing from governance map; add it to errcodeNameToStatus (B2-K-04)",
                code)
        }
    }
    for code := range govCodes {
        if !errcodeFact.Names[code] {
            pass.Reportf(mapLitPos,
                "governance map has %q but it does not exist in pkg/errcode; remove stale entry (B2-K-04)",
                code)
        }
    }
    return nil, nil
}
```

### 3c. `cell-init-no-adapter` Analyzer — B2-C-03

```go
// File: cmd/gocell-analyze/analyzers/cellinitadapter/cellinitadapter.go
// 锁定：cells/*/cell_init.go 文件不能引用 adapters/* 包的任何类型或函数。
package cellinitadapter

import (
    "go/ast"
    "path/filepath"
    "strings"

    "golang.org/x/tools/go/analysis"
    "golang.org/x/tools/go/analysis/passes/inspect"
    "golang.org/x/tools/go/ast/inspector"
)

var Analyzer = &analysis.Analyzer{
    Name:     "cellinitadapter",
    Doc:      "prohibits cells/*/cell_init.go from importing adapters/* types (B2-C-03)",
    Requires: []*analysis.Analyzer{inspect.Analyzer},
    Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
    for _, f := range pass.Files {
        filename := filepath.Base(pass.Fset.File(f.Pos()).Name())
        if filename != "cell_init.go" {
            continue
        }
        // 检查 import 声明
        insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
        insp.Preorder([]ast.Node{(*ast.ImportSpec)(nil)}, func(n ast.Node) {
            imp := n.(*ast.ImportSpec)
            path := strings.Trim(imp.Path.Value, `"`)
            if strings.Contains(path, "/adapters/") {
                pass.Reportf(imp.Pos(),
                    "cell_init.go must not import adapters package %q; use port interface defined in kernel/ or runtime/ (B2-C-03)",
                    path)
            }
        })
    }
    return nil, nil
}
```

### 3d. 打包成 multichecker 并集成 CI

```go
// File: cmd/gocell-analyze/main.go
package main

import (
    "golang.org/x/tools/go/analysis/multichecker"

    "github.com/YOUR_ORG/gocell/cmd/gocell-analyze/analyzers/cellinitadapter"
    "github.com/YOUR_ORG/gocell/cmd/gocell-analyze/analyzers/errcodemirror"
    "github.com/YOUR_ORG/gocell/cmd/gocell-analyze/analyzers/mustnotcall"
)

func main() {
    multichecker.Main(
        mustnotcall.Analyzer,
        errcodemirror.Analyzer,
        cellinitadapter.Analyzer,
    )
}
```

**CI 集成方式（三选一）**

方式 A：standalone binary（最简单，推荐）
```yaml
# .github/workflows/ci.yml
- name: Run GoCell custom analyzers
  run: |
    go build -o /tmp/gocell-analyze ./cmd/gocell-analyze/
    /tmp/gocell-analyze ./cells/... ./runtime/... ./adapters/... ./kernel/...
```

方式 B：golangci-lint module plugin（集中管理，需同版本依赖）
```yaml
# .custom-gcl.yml
version: v2.11.4
plugins:
  - module: "github.com/YOUR_ORG/gocell"
    import: "github.com/YOUR_ORG/gocell/cmd/gocell-analyze/plugin"
    path: .  # 本地路径
```
```go
// cmd/gocell-analyze/plugin/plugin.go
package main
import (
    "golang.org/x/tools/go/analysis"
    "github.com/YOUR_ORG/gocell/cmd/gocell-analyze/analyzers/mustnotcall"
    "github.com/YOUR_ORG/gocell/cmd/gocell-analyze/analyzers/cellinitadapter"
)
func New(conf any) ([]*analysis.Analyzer, error) {
    return []*analysis.Analyzer{mustnotcall.Analyzer, cellinitadapter.Analyzer}, nil
}
```

方式 C：`go vet -vettool`（无需 golangci-lint）
```bash
go build -o /tmp/gocell-vet ./cmd/gocell-analyze/
go vet -vettool=/tmp/gocell-vet ./...
```

---

## §4 Contract 治理（YAML/JSON via conftest+rego）

### 覆盖 B2-T-01/03/04/05/08 的 rego 规则

```rego
# File: policy/contract.rego
package gocell.contract

import future.keywords.if
import future.keywords.in

# B2-T-04：所有 path/query/payload 字段名必须 camelCase
# 规则：字段名不能含下划线（userID 合法，user_id 非法）
deny contains msg if {
    some endpoint in input.endpoints
    some param in endpoint.parameters
    contains(param.name, "_")
    msg := sprintf(
        "contract %q endpoint %q param %q violates camelCase naming (B2-T-04)",
        [input.id, endpoint.path, param.name]
    )
}

# B2-T-05：internal contract 禁止出现 actor 或 authentication.kind: bearer
deny contains msg if {
    input.kind == "internal"
    input.authentication.kind == "bearer"
    msg := sprintf(
        "internal contract %q must not use bearer authentication (B2-T-05)",
        [input.id]
    )
}

deny contains msg if {
    input.kind == "internal"
    input.actor != null
    msg := sprintf(
        "internal contract %q must not declare actor field (B2-T-05)",
        [input.id]
    )
}

# B2-T-01：write endpoint 必须声明 concurrencyControl 或 expectedVersion
_write_methods := {"POST", "PUT", "PATCH", "DELETE"}

deny contains msg if {
    some endpoint in input.endpoints
    endpoint.method in _write_methods
    not endpoint.concurrencyControl
    not endpoint.expectedVersion
    msg := sprintf(
        "contract %q write endpoint %q %q must declare concurrencyControl or expectedVersion (B2-T-01)",
        [input.id, endpoint.method, endpoint.path]
    )
}

# B2-T-03：response schema 禁止 additionalProperties: false（妨碍演进）
deny contains msg if {
    some endpoint in input.endpoints
    endpoint.response.schema.additionalProperties == false
    msg := sprintf(
        "contract %q endpoint %q response schema uses additionalProperties:false which blocks evolution; use true or unevaluatedProperties (B2-T-03)",
        [input.id, endpoint.path]
    )
}

# B2-T-08：handler 的 errorCodes 必须是 contract.responses 中声明码的子集
# （需 contract 中有 responses.errorCodes 列表）
deny contains msg if {
    some code in input.handlerErrorCodes
    not code in input.responses.errorCodes
    msg := sprintf(
        "contract %q handler emits error code %q not declared in contract.responses (B2-T-08)",
        [input.id, code]
    )
}

# lifecycle active 的 contract 不能含 bootstrap 类公开端点（B2-C-02）
deny contains msg if {
    input.lifecycle == "active"
    some endpoint in input.endpoints
    endpoint.public == true
    endpoint.tags[_] == "bootstrap"
    msg := sprintf(
        "active contract %q has public bootstrap endpoint %q; use lifecycle:bootstrap instead (B2-C-02)",
        [input.id, endpoint.path]
    )
}
```

### 挂在 PR check

```yaml
# .github/workflows/contract-check.yml
name: contract-governance
on: [pull_request]
jobs:
  conftest:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install conftest
        run: |
          curl -L https://github.com/open-policy-agent/conftest/releases/latest/download/conftest_Linux_x86_64.tar.gz | tar xz
          sudo mv conftest /usr/local/bin/
      - name: Validate contracts
        run: |
          find contracts/ -name "contract.yaml" | xargs -I{} conftest test {} \
            --policy policy/contract.rego \
            --output table \
            --fail-on-warn
```

---

## §5 GoCell G1-G5 PR 工具映射

| PR | 涉及条目 | 推荐工具组合 | 实现成本 |
|----|---------|------------|---------|
| PR-CI-G1 GOVERNANCE-RULES-PHASE1 | B2-K-04 errcode mirror, B2-R-04 单源, B2-T-04 camelCase, B2-T-05 internal actor, B2-X-05 cmd help | `errcodemirror` analyzer（standalone）+ `tagliatelle`（golangci-lint）+ `conftest` rego（B2-T-04/05）+ `forbidigo`（B2-X-05 cmd 输出检测） | 6h |
| PR-CI-G2 ARCHTEST-IMPORT-AND-CONSTRUCTOR | B2-K-02 Must* 禁用, B2-K-03 type assertion, B2-A-11 构造器 error-first, B2-A-20 simpleTracer, B2-A-27 Redis KeyNamespace, B2-C-03 Cell.Init, B2-A-18 connect timeout | `mustnotcall` analyzer + `cellinitadapter` analyzer + `depguard`（B2-C-03/A-20）+ `forbidigo`（B2-K-02 生产路径）+ `govet` 类型断言（B2-K-03）+ `forcetypeassert`（golangci-lint） | 8h |
| PR-CI-G3 CONTRACT-SCHEMA-EVOLUTION | B2-T-03 additionalProperties, B2-T-01 optimistic lock, B2-T-08 errcode 双向校验 | `conftest` rego policy（覆盖全部三条）+ `gocell validate --strict` 新 rule（可复用 rego 输出） | 6h |
| PR-CI-G4 LINT-AND-WAIVER | B2-A-21 fmt 字面量, B2-X-01 test sleep, B2-T-02 waiver 到期, B2-K-07 contracttest key | `govet -printf`（已内置，纳入 required）+ `forbidigo` time.Sleep（_test.go）+ Go time.After waiver check（inline `t.Fatal`）+ `errcheck`（B2-K-07 undeclared key） | 4h |
| PR-CI-G5 RACE-AND-INTEGRATION-CI | B2-A-24/29 race test, B2-A-17/32 testcontainers nightly | `go test -race` CI required job + GitHub Actions matrix（testcontainers nightly workflow）+ `paralleltest` lint 预防 | 4h |

---

## §6 关键版本与偏离说明

**版本汇总**

- golangci-lint: v2.11.4 — https://github.com/golangci/golangci-lint/releases
- NilAway: v0.0.0-20260318（module plugin）— https://pkg.go.dev/go.uber.org/nilaway
- govulncheck: @latest（golang.org/x/vuln/cmd/govulncheck）
- conftest: @latest — https://github.com/open-policy-agent/conftest

**GoCell 特定偏离说明**

1. `wrapcheck` 在 `pkg/errcode` 内部豁免，避免 errcode 包本身被要求二次包装
2. `depguard` 的 `kernel_no_runtime` 规则比通用推荐更严格，因为 CLAUDE.md 明确 kernel/ 只依赖标准库 + pkg/ + yaml.v3
3. `forbidigo` 的 Must* 规则不全局禁用（golangci-lint 默认没有此规则），必须显式配置 pattern
4. NilAway 目前属于"按需"而非"必启"，因其 false positive 率在接口密集代码（adapters/）较高；待 v1.0 稳定后升级为必启
5. `paralleltest` 对 `_test.go` 豁免 forbidigo 但不豁免 paralleltest 本身，确保所有测试声明并行意图

**参考链接**

- [golangci-lint releases](https://github.com/golangci/golangci-lint/releases)
- [golangci-lint v2 blog](https://ldez.github.io/blog/2025/03/23/golangci-lint-v2/)
- [golangci-lint Module Plugin System](https://golangci-lint.run/docs/plugins/module-plugins/)
- [golangci-lint Go Plugin System](https://golangci-lint.run/docs/plugins/go-plugins/)
- [uber-go/nilaway](https://github.com/uber-go/nilaway)
- [NilAway pkg.go.dev](https://pkg.go.dev/go.uber.org/nilaway)
- [NilAway Configuration Wiki](https://github.com/uber-go/nilaway/wiki/Configuration)
- [tagliatelle](https://github.com/ldez/tagliatelle)
- [musttag](https://github.com/go-simpler/musttag)
- [golang.org/x/tools/go/analysis](https://pkg.go.dev/golang.org/x/tools/go/analysis)
- [multichecker](https://pkg.go.dev/golang.org/x/tools/go/analysis/multichecker)
- [conftest/open-policy-agent](https://github.com/open-policy-agent/conftest)
- [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
- [depguard](https://github.com/OpenPeeDeeP/depguard)
- [go-errorlint](https://github.com/polyfloyd/go-errorlint)
- [nilnil](https://github.com/Antonboom/nilnil)
- [example-plugin-linter](https://github.com/golangci/example-plugin-linter)
