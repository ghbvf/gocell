# CNCF / Kubernetes 类项目 CI 规则调研

> 日期：2026-04-30
> 任务：调研 K8s/Istio/Argo/etcd/containerd/Linkerd/OpenTelemetry-Collector-Contrib 等大型 CNCF Go 项目的 CI/lint/治理实践，对标 GoCell backlog2 31 条 CI 治理候选
> 调研者：explorer agent
> 关联文件：`../../backlog2.md`（70 条新增 backlog 源头）、`202604290858-backlog2-ci-governance-analysis.md`（CI 治理候选筛出）、`202604290945-ci-baseline-raw-extraction.md`（19 项目频次矩阵聚合）、`202604300430-golangci-tier12-priority-and-projection.md`（决策文档）

---

## §1 项目矩阵

| 项目 | 主要 linter 工具集 | 自定义 verify 脚本 | nil 守护策略 | contract/YAML 治理 | 阻塞 CI check 数 |
|---|---|---|---|---|---|
| **kubernetes/kubernetes** | golangci-lint（depguard, forbidigo, gocritic, govet, logcheck plugin, kubeapilinter plugin, staticcheck, revive, modernize, sorted, testifylint, unused, ginkgolinter） | `hack/verify-golangci-lint.sh`、`hack/verify-import-boss.sh`、`hack/verify-prometheus-imports.sh`、`hack/verify-featuregates.sh`、`hack/verify-test-featuregates.sh`、`hack/verify-gofmt.sh`、`hack/verify-typecheck.sh`、`hack/verify-codegen.sh` | kube-api-linter 的 `optionalorrequired` analyzer + nilnil via nilerr 间接守护 | kube-api-linter (`jsontags`, `optionalfields`, `nonullable`, `conditions`)；CRD OpenAPI schema validation | 14 个 linter + 8+ verify 脚本（全部 prow presubmit required） |
| **containerd/containerd** | golangci-lint（depguard, revive, gosec, misspell, modernize, nolintlint, unconvert, usetesting, forbidigo） | 无独立 verify shell 脚本（GitHub Actions 内联） | errcheck 显式 disabled；forbidigo 限制 regexp 包用法 | depguard 阻止 opencontainers/runc 导入 | ~10 linter check（GitHub Actions 阻塞） |
| **containerd/nerdctl** | golangci-lint（depguard, forbidigo, revive, gocritic, govet, staticcheck, unconvert, misspell） | 无 | forbidigo pattern 禁止 hashicorp-lru arc；depguard 分层 pkg 规则阻止 cobra/pflag/viper 进入 pkg 层 | depguard 按目录 `**/pkg/**` 强制架构分层 | ~6 linter check |
| **istio/client-go** | golangci-lint（depguard, errcheck, gocritic, gosec, govet, lll, misspell, revive, staticcheck, unconvert, unparam, unused；gci/gofumpt formatter） | Prow presubmit jobs（makefile-based） | errcheck 启用；depguard 禁 gogo/protobuf | 无自定义 contract 扫描 | ~12 linter + prow presubmit |
| **kubernetes/release** | golangci-lint（87 个 linter 全集：nilnil, nilerr, nilnesserr, errcheck, errorlint, errname, staticcheck, gosec, gocritic, revive 等） | 无独立脚本 | nilnil + nilerr + nilnesserr 三重守护 | 无 | 87 linter 全集（PR 阻塞） |
| **argoproj/argo-workflows** | golangci-lint（42 linter：errcheck, errorlint, gocritic, gosec, govet, nilerr, noctx, staticcheck, revive, testifylint 等） | 无 | nilerr 启用；errcheck 启用 | 无 | ~42 linter check |
| **opentelemetry-collector-contrib** | golangci-lint（depguard, errcheck, errorlint, exhaustive, forbidigo, gocritic, gosec, revive, staticcheck, testifylint, wastedassign 等 24 个） | 无 | errcheck 启用；nilnil 未启用 | forbidigo 阻止 net/http 直接创建 Server；depguard 强制 semconv 版本；depguard 阻止废弃 azure autorest | ~24 linter check |
| **linkerd/linkerd2** | golangci-lint（errcheck, errorlint, gocritic, gosec, govet, ineffassign, misspell, nakedret, revive, staticcheck, stylecheck, unconvert, unparam, unused） | 无 | errcheck 启用（排除 fmt.Fprint 等） | 无 | ~17 linter check |

---

## §2 关键实践提取

### Q1 静态分析工具栈

**kubernetes/kubernetes** 在 `hack/golangci.yaml` 中使用了**自定义插件机制**（golangci-lint v1.57+ Module Plugin System）加载两个私有 analyzer：

- `logcheck`（路径 `_output/local/bin/logcheck.so`）：强制结构化日志，按包白名单区分"必须 contextual logging"与"仍可用 klog"的区域
- `kubeapilinter`（路径 `_output/local/bin/kube-api-linter.so`）：检查 K8s API 类型的 marker、jsontags、optionalorrequired、nonullable、nonpointerstructs、nodurations 等 14 项规则

来源：`https://github.com/kubernetes/kubernetes/blob/master/hack/golangci.yaml`

**forbidigo 配置片段（kubernetes）**：
```yaml
linters-settings:
  forbidigo:
    forbid:
      - pattern: "^md5\\."
        msg: "md5 is outdated, insecure, prefer sha256 or non-cryptographic hash"
      - pattern: "managedfields\\.ExtractInto|managedfields\\.Extract\\b"
        msg: "managedFields was removed"
      - pattern: "\\.Add\\("
        pkg: "k8s.io/component-base/featuregate"
        msg: "use AddVersioned() instead"
```

**depguard 配置片段（kubernetes）**：
```yaml
linters-settings:
  depguard:
    rules:
      utils:
        deny:
          - pkg: "k8s.io/utils/pointer"
            desc: "use k8s.io/utils/ptr instead"
      go-cmp:
        files: ["!**/*_test.go"]
        deny:
          - pkg: "github.com/google/go-cmp/cmp"
            desc: "only allowed in test files"
          - pkg: "html/template"
            desc: "use text/template in non-HTML code"
```

**kubernetes/release** 启用了三重 nil 守护：`nilnil`（同时返回 nil 接口值和 nil error）、`nilerr`（检查到 err!=nil 但返回 nil）、`nilnesserr`（返回了不同的 nil 值 error）。来源：`https://github.com/kubernetes/release/blob/master/.golangci.yml`

**opentelemetry-collector-contrib** 的 forbidigo 用于架构守护，禁止直接使用 `net/http` 创建 Server（必须通过 `confighttp.ServerConfig`）；depguard 强制锁定 semconv 版本并阻止废弃依赖。来源：`https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/.golangci.yml`

### Q2 import / 分层守护

**kubernetes `import-boss` 机制**：每个目录下放 `.import-restrictions` 文件（JSON），通过 `SelectorRegexp`/`AllowedPrefixes`/`ForbiddenPrefixes` 三字段定义规则。`hack/verify-import-boss.sh` 调用 `go run ./cmd/import-boss` 全量扫描。规则示例：
```json
{
  "Rules": [
    {
      "SelectorRegexp": "k8s[.]io/kubernetes",
      "ForbiddenPrefixes": [""]
    }
  ]
}
```
来源：`https://github.com/kubernetes/kubernetes/blob/master/hack/verify-import-boss.sh`、`https://github.com/kubernetes/apiserver/blob/master/.import-restrictions`

**containerd/nerdctl `depguard` 分层守护**：通过 `files` 字段按目录模式 `**/pkg/**/*.go` 限定规则生效范围，阻止 `pkg/` 层引入 cobra/pflag/viper/cmd 层。来源：`https://github.com/containerd/nerdctl/blob/main/.golangci.yml`

**kubernetes `verify-prometheus-imports.sh`**：维护 allowlist，用 `grep -R -l '"github.com/prometheus/.*"'` 扫全库，未授权文件进 `really_failing_files` → exit 2。来源：`https://github.com/kubernetes/kubernetes/blob/master/hack/verify-prometheus-imports.sh`

### Q3 happy path 防御

**错误路径测试覆盖**：没有找到强制"每个错误路径必须有测试"的专用工具。实际上大项目用 **覆盖率 gate**（`go test -coverprofile` + threshold）+ `errcheck` linter（未处理的 error 返回值报错）两者组合。

**nil 守护**：kubernetes/release 启用 nilnil + nilerr + nilnesserr，argo-workflows 和 linkerd2 启用 nilerr。这三个 linter 覆盖了绝大多数 nil 歧义场景：
- `nilerr`：`if err != nil { return nil }` 这种"检查了却忽略"
- `nilnil`：`return nil, nil`（接口值 + error 同时 nil，导致调用方误判成功）
- `nilnesserr`：返回了不是传入 err 的另一个 nil error

**apimachinery nil 惯用法**：Kubernetes API 类型所有指针字段加 `// +optional` 注释，构造时用 `apivalidation.ValidateXxx()` 链式检查，kube-api-linter 的 `optionalorrequired` 规则强制每个字段必须标 `+optional` 或 `+required` 之一。

### Q4 构造期校验 / fail-closed 模式

**kube-api-linter `optionalorrequired` analyzer**：golang.org/x/tools/go/analysis 框架实现的 pass，扫描 K8s API struct 字段，发现未标 `+optional`/`+required` 的字段即报错。集成为 golangci-lint 插件后在 PR 阶段阻塞。
来源：`https://github.com/kubernetes-sigs/kube-api-linter`

**opentelemetry-collector-contrib `forbidigo` 守护构造器**：用 forbidigo 阻止 `http.NewServer` 等直接构造，强制通过工厂函数。来源同 Q1。

**containerd/nerdctl `forbidigo`**：阻止 hashicorp-lru arc 包（专利风险），任何调用直接 lint error，比运行时发现早。

**Istio 注册式验证**：`RegisterValidateFunc()` 自动包裹 `validateMetadata()`，构造阶段即拒绝带 `istio.io/dry-run` 注释的未授权资源，fail-closed。来源：`https://github.com/istio/istio/blob/master/pkg/config/validation/validation.go`

### Q5 YAML / contract 治理

**K8s kube-api-linter**：对 API struct 的 JSON tag、条件字段、marker 冲突、nullable 约束做静态分析，配置在 golangci.yaml 的 `custom-analyzers` 块中。启用 checks：`jsontags`, `conditions`, `conflictingmarkers`, `nonullable`, `optionalfields`, `ssatags`, `duplicatemarkers`, `dependenttags`, `nodurations`, `notimestamp`, `nomaps`, `nonpointerstructs`, `integers`, `commentstart`。这是目前最完整的 API 类型一致性扫描方案。

**opentelemetry depguard semconv 版本锁定**：通过 depguard 阻止 legacy semconv 版本（v1.9.0-v1.39.x）在新代码中使用，保证 schema 稳定。

**verify-featuregates.sh 模式**：用 `go run` 执行 Go 程序做跨文件一致性扫描（reference YAML ↔ 实际 Go 常量），比纯 shell grep 更可靠，适合需要 AST 解析的场景。

### Q6 测试 sleep / flake 检测

没有找到大项目内置的 `verify-flakes.sh`（K8s 社区有 flake 文档但无自动检测脚本）。实际防御手段：

1. **verify-test-featuregates.sh 模式**（K8s）：`git grep MutableFeatureGate -- '*_test.go'` 检测测试代码中禁止的全局状态访问，与检测 `time.Sleep` 同理。
2. **forbidigo 方案**：可配置 `pattern: "time\\.Sleep"` 配合 `pkg: ""` 和 `files: ["!**/testutil/**"]` 实现 sleep 黑名单，允许 testutil helper 中使用。

### Q7 fuzz 测试

没有找到独立的 `verify-fuzz.sh`。K8s 社区通过 **OSS-Fuzz** 持续集成模糊测试（独立于 prow presubmit），非 PR 阻塞。Go 1.18+ `go test -fuzz` 仅在 seed corpus 测试中作为普通单测跑（`-fuzz=.` 为长时间 fuzzing，不适合 CI blocking）。最佳实践：fuzz seed test（`FuzzXxx(f, func(t *testing.T, ...))`）加入单测文件，CI 以 `-run=FuzzXxx/` 跑 seed，weekly job 跑全量 `-fuzz`。

### Q8 CI required check 列表

**kubernetes**（prow presubmit）阻塞 check 包括：`pull-kubernetes-verify`（涵盖 golangci-lint + 所有 verify-*.sh）、`pull-kubernetes-unit`、`pull-kubernetes-typecheck`、`pull-kubernetes-build`。其中 `pull-kubernetes-verify` 调用 `hack/verify-golangci-lint.sh`，该脚本安装 golangci-lint + logcheck.so + kube-api-linter.so 三个工具，`exit "$res"` 传出非零即 PR 阻塞。

**containerd/argo/linkerd**：均用 GitHub Actions `required` check（branch protection rule），lint job 失败则 PR 无法 merge。

---

## §3 与 GoCell backlog2 的对标映射

| GoCell 条目 | 问题描述 | 对标工具 / 规则 | 借鉴方式 |
|---|---|---|---|
| **B2-K-02** Must* 禁用 | 生产路径残留 `MustNew*` panic | K8s `forbidigo` pattern + `files` 白名单 | `forbidigo: forbid: [{pattern: "Must[A-Z]", msg: "use error-first constructor", files: ["!**/cmd/**", "!**/*_test.go"]}]` |
| **B2-K-03** AssemblyRef 类型断言 | `asm.(assemblyWithCell)` 跨层隐式契约 | K8s `import-boss` + nerdctl `depguard` 分层规则 | depguard 按目录 `**/runtime/**` 禁止直接引用 kernel 内部类型 |
| **B2-K-04** errcode mirror drift | 手工镜像映射两处不同步 | K8s `verify-featuregates.sh` 模式（Go 程序跨文件一致性扫描） | 写 `cmd/gocell/internal/verify/errcode_mirror.go` + CI step |
| **B2-R-04** errcode classify 双源 | `expected4xxCodes` ↔ `WriteDomainError` 两处维护 | 同上；单源 + archtest 断言两侧相等 | 同 B2-K-04 |
| **B2-K-05** error 路径泄露 | parse error 含 fs 路径 | `forbidigo` pattern 禁止 `os.PathSeparator` 字面拼接进 error | `forbidigo: [{pattern: "os\\.PathSeparator", msg: "do not expose fs paths in errors"}]` |
| **B2-X-01** test sleep 黑名单 | e2e 用 `time.Sleep(50ms)` | K8s `verify-test-featuregates.sh` grep 模式；forbidigo `files` 白名单 | `forbidigo: [{pattern: "time\\.Sleep", files: ["!**/testutil/**"], msg: "use ready-signal helper instead"}]` |
| **B2-A-27** Redis KeyNamespace 必填 | 多租户 key 共用前缀 | kube-api-linter `optionalorrequired` 思路（自定义 go/analysis pass） | 自定义 `kernel/lint/required_fields.go`（analysis.Analyzer），扫 `redis.Config` struct 的 `KeyNamespace` 字段是否在构造调用点被赋值 |
| **B2-A-11** PG 构造器 error-first | `New*` 和 `MustNew*` 混用 | 同 B2-K-02 forbidigo；K8s 的 error-first 惯用法 | 同 B2-K-02 |
| **B2-A-20** simpleTracer 生产禁用 | 生产路径用 noop tracer | K8s `verify-prometheus-imports.sh` allowlist 模式 | allowlist 脚本：`grep -R simpleTracer -- cells/ adapters/ cmd/corebundle/`，有匹配则 exit 1 |
| **B2-T-04** contract 字段命名 | `userId` vs `userID` 漂移 | K8s kube-api-linter `jsontags` 规则 | 在 `gocell validate` 中加 camelCase 正则检查（`[a-z][a-zA-Z0-9]*`，不含下划线） |
| **B2-A-24/29** race 测试缺失 | redis/prometheus 无 race 测试 | etcd `go test -race` required CI check | GitHub Actions job：`go test -race ./adapters/redis/... ./adapters/prometheus/...`，设为 required check |
| **B2-K-07** contracttest undeclared key 静默 | key 写错测试假通过 | argo-workflows `nilerr` + K8s `verify-test-featuregates.sh` fail-fast 模式 | 代码修改：未声明 key → `t.Fatalf`；配合 `staticcheck SA` 静态分析 |
| **B2-A-17/32** 集成测试缺失 | RMQ/S3 无 testcontainers harness | containerd/etcd 的 testcontainers 集成模式 | nightly GitHub Actions job，`testcontainers-go` 拉 rabbitmq:3-management + minio/minio |
| **B2-C-02** setup 端点常驻 Public | bootstrap 端点永久暴露 | K8s kube-api-linter `nonullable`/`conditions` 模式（元数据字段约束） | `gocell validate` 治理规则：`lifecycle: active` contract 禁止 `Public: true` 无 auth 端点 |

---

## §4 落地建议

### 片段 A：forbidigo 配置（直接用于 G2/G4 PR）

```yaml
# .golangci.yml
linters:
  enable:
    - forbidigo
    - nilnil
    - nilerr
    - errcheck

linters-settings:
  forbidigo:
    forbid:
      # B2-K-02: Must* 生产路径禁用
      - pattern: "Must[A-Z][a-zA-Z]+"
        msg: "use error-first constructor; Must* only allowed in cmd/ and *_test.go"
        files:
          - "!**/cmd/**"
          - "!**/*_test.go"
          - "!**/contracttest/**"
      # B2-X-01: test sleep 黑名单
      - pattern: "time\\.Sleep"
        msg: "use ready-signal helper or Eventually(); direct Sleep causes flaky tests"
        files:
          - "**/*_test.go"
          - "!**/testutil/**"
      # B2-K-05: fs 路径泄露
      - pattern: "os\\.PathSeparator"
        msg: "do not interpolate fs paths into user-visible error messages"
      # B2-A-20: simpleTracer 生产禁用
      - pattern: "simpleTracer"
        pkg: "github.com/yourorg/gocell/runtime/observability"
        msg: "simpleTracer is test-only; inject otel.Tracer in production"
        files:
          - "!**/*_test.go"

  errcheck:
    check-type-assertions: true
    exclude-functions:
      - fmt.Fprintf
      - fmt.Fprintln

  nilnil:
    # 同时返回 nil interface 和 nil error — 调用方无法判断成功还是空结果
    checked-types:
      - ptr
      - func
      - iface
      - map
      - chan

  depguard:
    rules:
      cells-no-adapters:
        files: ["**/cells/**/*.go", "!**/*_test.go"]
        deny:
          - pkg: "github.com/yourorg/gocell/adapters"
            desc: "cells/ must not depend on adapters/; use port interfaces"
      kernel-stdlib-only:
        files: ["**/kernel/**/*.go"]
        deny:
          - pkg: "github.com/yourorg/gocell/runtime"
            desc: "kernel/ must not depend on runtime/"
          - pkg: "github.com/yourorg/gocell/adapters"
            desc: "kernel/ must not depend on adapters/"
```

**适用 PR**：G2（archtest/import 守护）+ G4（lint/waiver）可直接采纳 forbidigo + depguard 块。

### 片段 B：import-boss 风格 allowlist 脚本（用于 G2 PR）

```bash
#!/usr/bin/env bash
# hack/verify-layer-imports.sh
# 守护 kernel/ 不依赖 runtime/ 和 adapters/
set -euo pipefail

VIOLATIONS=$(grep -rn \
  -e '"github.com/yourorg/gocell/runtime' \
  -e '"github.com/yourorg/gocell/adapters' \
  kernel/ --include="*.go" | grep -v "_test.go" || true)

if [[ -n "$VIOLATIONS" ]]; then
  echo "ERROR: kernel/ must not import runtime/ or adapters/:" >&2
  echo "$VIOLATIONS" >&2
  exit 1
fi

# 守护 simpleTracer 不进生产路径
TRACER_VIOLATIONS=$(grep -rn "simpleTracer" \
  cells/ adapters/ cmd/corebundle/ --include="*.go" \
  | grep -v "_test.go" || true)

if [[ -n "$TRACER_VIOLATIONS" ]]; then
  echo "ERROR: simpleTracer is test-only, found in production path:" >&2
  echo "$TRACER_VIOLATIONS" >&2
  exit 1
fi

echo "OK: layer import check passed"
```

**适用 PR**：G2（B2-A-20 simpleTracer，B2-C-03 Cell.Init infra type）可直接采用。

### 片段 C：errcode mirror 一致性扫描（用于 G1 PR）

```bash
#!/usr/bin/env bash
# hack/verify-errcode-mirror.sh
# 检查 kernel/governance 的 errcodeNameToStatus 与 pkg/errcode/status.go 是否同步
set -euo pipefail

go run ./cmd/gocell/internal/verify/errcode_mirror_check.go
echo "OK: errcode mirror consistent"
```

```go
// cmd/gocell/internal/verify/errcode_mirror_check.go
// 用 reflect + AST 对比两侧映射，有 diff 则 os.Exit(1)
// 参考 K8s hack/verify-featuregates.sh 的 go run 模式
```

**适用 PR**：G1（B2-K-04，B2-R-04）直接采纳此模式。

### 片段 D：race + testcontainers CI job（用于 G5 PR）

```yaml
# .github/workflows/ci-required.yml
name: CI Required Checks
on: [pull_request]
jobs:
  race-test:
    name: Race Detector (adapters)
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - run: go test -race -count=1 ./adapters/redis/... ./adapters/prometheus/...
    # 设为 required check via branch protection

  lint:
    name: Lint
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - uses: golangci/golangci-lint-action@v6
        with:
          version: v1.64.x
          args: --timeout=10m
      - run: bash hack/verify-layer-imports.sh
```

**适用 PR**：G5（B2-A-24/29 race required）直接采纳；lint job 适用 G2/G4。

### 片段 E：waiver 到期 build break（用于 G4 PR）

```go
// kernel/governance/rules_waiver_expiry.go
func CheckWaiverExpiry(t *testing.T) {
    waivers := []struct {
        id      string
        expires time.Time
        message string
    }{
        // B2-T-02
        {"B2-T-02", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
         "RBAC assign waiver expired: remove waiver and implement proper RBAC"},
    }
    for _, w := range waivers {
        if time.Now().After(w.expires) {
            t.Errorf("waiver %s expired on %s: %s", w.id, w.expires.Format("2006-01-02"), w.message)
        }
    }
}
// 在 TestMain 或专用 TestWaiverExpiry 中调用，CI 自动 break
```

**适用 PR**：G4（B2-T-02）直接采纳。

---

## §5 关键引用清单

| 来源 | 文件 | 行/片段 | 用途 |
|---|---|---|---|
| kubernetes/kubernetes | `hack/golangci.yaml` | forbidigo+depguard+custom plugins | G2 模板 |
| kubernetes/kubernetes | `hack/verify-import-boss.sh` | `go run ./cmd/import-boss` 模式 | G2 分层守护 |
| kubernetes/kubernetes | `hack/verify-prometheus-imports.sh` | allowlist + grep 双层 | G2 simpleTracer 守护 |
| kubernetes/kubernetes | `hack/verify-test-featuregates.sh` | `git grep <pattern> -- '*_test.go'` | G4 sleep 黑名单 |
| kubernetes/kubernetes | `hack/verify-featuregates.sh` | `go run` 做跨文件一致性扫描 | G1 errcode mirror |
| kubernetes/release | `.golangci.yml` | nilnil+nilerr+nilnesserr 三重守护 | G2 nil guard |
| kubernetes-sigs/kube-api-linter | `doc.go` + pkg/analysis | optionalorrequired analyzer | B2-A-27 必填字段 |
| containerd/nerdctl | `.golangci.yml` | depguard 按目录分层规则 | G2 kernel/cells/adapters 分层 |
| opentelemetry-collector-contrib | `.golangci.yml` | forbidigo 阻止直接构造 + depguard 版本锁定 | G1 semconv/errcode 版本锁定 |
| golangci/golangci-lint | depguard logger 规则示例 | `deny: [{pkg: "logrus", desc: "..."}]` | G2 depguard 模板 |

**Source URLs**:
- [kubernetes/kubernetes hack/golangci.yaml](https://github.com/kubernetes/kubernetes/blob/master/hack/golangci.yaml)
- [kubernetes/kubernetes hack/verify-import-boss.sh](https://github.com/kubernetes/kubernetes/blob/master/hack/verify-import-boss.sh)
- [kubernetes/kubernetes hack/verify-prometheus-imports.sh](https://github.com/kubernetes/kubernetes/blob/master/hack/verify-prometheus-imports.sh)
- [kubernetes/kubernetes hack/verify-test-featuregates.sh](https://sourcegraph.com/github.com/kubernetes/kubernetes@53ba2ce02625c72edf3a997bab7f0f555ce2431e/-/blob/hack/verify-test-featuregates.sh)
- [kubernetes/release .golangci.yml](https://github.com/kubernetes/release/blob/master/.golangci.yml)
- [kubernetes-sigs/kube-api-linter](https://github.com/kubernetes-sigs/kube-api-linter)
- [containerd/nerdctl .golangci.yml](https://github.com/containerd/nerdctl/blob/main/.golangci.yml)
- [containerd/containerd .golangci.yml](https://github.com/containerd/containerd/blob/main/.golangci.yml)
- [open-telemetry/opentelemetry-collector-contrib .golangci.yml](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/.golangci.yml)
- [argoproj/argo-workflows .golangci.yml](https://github.com/argoproj/argo-workflows/blob/main/.golangci.yml)
- [linkerd/linkerd2 .golangci.yml](https://github.com/linkerd/linkerd2/blob/main/.golangci.yml)
- [istio/client-go .golangci.yml](https://github.com/istio/client-go/blob/master/common/config/.golangci.yml)
- [golangci-lint depguard configuration](https://golangci-lint.run/docs/linters/configuration/)
- [hack: fix settings for forbidigo linter kubernetes/kubernetes PR#130647](https://github.com/kubernetes/kubernetes/pull/130647)
- [kubernetes/apiserver .import-restrictions](https://github.com/kubernetes/apiserver/blob/master/.import-restrictions)
- [Golden config for golangci-lint (maratori)](https://gist.github.com/maratori/47a4d00457a92aa426dbd48a18776322)
