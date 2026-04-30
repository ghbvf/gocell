# CI 基线 Raw 全集提取（19 项目矩阵）

> 日期：2026-04-30
> 任务：直接从 19 个工业 Go 项目 CI 配置 raw 内容提取规则全集，建立**规则 × 项目频次矩阵**与 **Tier 1/2/3 工业基线分级**
> 调研者：explorer agent（早期包揽 20 项目版本）
> 关联文件：
> - `202604290945-ci-raw-dump-A1.md`（K8s/CRDB/TiDB/Temporal/Vault/Kratos/fx/Watermill 8 项目 raw 完整提取，含 CRDB lint_test.go 67 条禁令、K8s 14 linter + 3 verify 脚本、Vault semgrep 8 文件清单）
> - `202604290945-ci-raw-dump-B1.md`（caddy/pulumi/minio/grafana/nats/opa）
> - `202604290945-ci-raw-dump-B2.md`（cilium/crossplane/spire/teleport/prometheus/traefik）
>
> **本文为聚合产物**：包含 19 项目逐个 dump 的关键摘要 + 频次矩阵 + 三档分级 + GoCell 当前差距清单

---

## §1 项目逐个 dump（关键摘要）

### kubernetes/kubernetes

**raw URL:**
- `https://raw.githubusercontent.com/kubernetes/kubernetes/master/hack/golangci.yaml`
- `https://raw.githubusercontent.com/kubernetes/kubernetes/master/hack/verify-golangci-lint.sh`
- `https://raw.githubusercontent.com/kubernetes/kubernetes/master/hack/verify-import-boss.sh`
- `https://raw.githubusercontent.com/kubernetes/kubernetes/master/hack/verify-featuregates.sh`

#### `hack/golangci.yaml` 启用 linter（14 个）

```
depguard, forbidigo, ginkgolinter, gocritic, govet, ineffassign,
kubeapilinter (自定义插件), logcheck (自定义插件), modernize,
revive, sorted (自定义插件), staticcheck, testifylint, unused
```

**depguard:**
- rule `utils`: deny `k8s.io/utils/pointer` → use `k8s.io/utils/ptr` instead
- rule `go-cmp`: deny `github.com/google/go-cmp/cmp` 在非测试文件中使用 → 限于测试

**forbidigo（每条全列）:**
- `pattern: "^md5\\."` → msg: "md5 is outdated, insecure, prefer sha256 or non-cryptographic hash"
- `pattern: "managedfields\\.ExtractInto|managedfields\\.Extract\\b"` → msg: "managedFields was removed"
- `pattern: "\\.Add\\("` + `pkg: "k8s.io/component-base/featuregate"` → msg: "use AddVersioned() instead"

**kubeapilinter（自定义插件，检查 K8s API 类型）:**
启用：`comments`, `conditions`, `jsontags`, `maps`, `nonpointerstructs`, `nodurations`, `optionalorrequired`, `nonullable`

**logcheck（自定义插件）:** 强制结构化日志；按包白名单区分"必须 contextual logging"与"仍可用 klog"区域。

**hack/verify-import-boss.sh:** 运行 `import-boss` 工具验证包导入限制（每目录 `.import-restrictions` JSON）。
**hack/verify-featuregates.sh:** 验证 feature gates 文档与代码一致性，`go run test/compatibility_lifecycle/main.go` + `genfeaturegates` + diff。

---

### cockroachdb/cockroach

**raw URL:** `https://raw.githubusercontent.com/cockroachdb/cockroach/master/pkg/testutils/lint/lint_test.go`

#### lint_test.go 禁令完整列表（40 条）

| # | 禁止对象 | 强制替代 | 理由 |
|---|---------|---------|------|
| 1 | `golang.org/x/net/context` | `context` (stdlib) | 已并入标准库 |
| 2 | `log` (stdlib) | `util/log` | 统一日志 |
| 3 | `github.com/golang/protobuf/proto` | `github.com/gogo/protobuf/proto` | CRDB 使用 gogo |
| 4 | `github.com/satori/go.uuid` | `util/uuid` | 内部封装 |
| 5 | `golang.org/x/sync/singleflight` | `syncutil/singleflight` | 内部封装 |
| 6 | `syscall` | `sysutil` | 平台抽象 |
| 7 | `errors` (stdlib) | `github.com/cockroachdb/errors` | 更丰富错误链 |
| 8 | `go.uber.org/atomic` | `sync/atomic` (stdlib) | Go 1.19+ 原子已足够 |
| 9 | `github.com/pkg/errors` | `github.com/cockroachdb/errors` | 统一错误库 |
| 10 | `errors` 子包直接 import | 导入父包 | 保持一致 |
| 11 | `sync.Mutex` / `sync.RWMutex` / `sync.Map` 直接用 | `syncutil` 等价物 | 更好 tracing/deadlock 检测 |
| 12 | `http.Get` / `http.Put` / `http.Head` | `httputil` | 统一 HTTP 客户端 |
| 13 | `os.Getenv`/`os.LookupEnv` 读取 `COCKROACH_*` 前缀 | `envutil` | 集中环境变量管理 |
| 14 | `context.WithDeadline` / `context.WithTimeout` | `timeutil.RunWithTimeout` | 统一超时管理 |
| 15 | `grpc.NewServer` | `rpc.NewServer` | 内部封装（拦截器等） |
| 16 | `proto.Clone` | `protoutil.Clone` | 标准化 |
| 17 | `proto.Equal` | `.Equal()` method 或 `reflect.DeepEqual` | 标准化 |
| 18 | `proto.Message` 接口 | `protoutil.Message` | 标准化 |
| 19 | `proto.Marshal` | `protoutil.Marshal` | 一致性 |
| 20 | `proto.Unmarshal` | `protoutil.Unmarshal` | 一致性 |
| 21 | `time.Now()` / `time.Since()` / `time.Unix()` / `time.LoadLocation()` | `timeutil` 等价物 | 可注入时钟，测试可控 |
| 22 | `timeutil.Now().Sub(t)` | `timeutil.Since(t)` | 效率 + 一致性 |
| 23 | `context.TODO()` 在测试中 | `context.Background()` | 语义明确 |
| 24 | `hlc.NewClock(..., 0)` 在测试中 | 非零时钟偏移 | 避免测试漏测时钟逻辑 |
| 25 | 大写 SQL 内置函数（`FAMILY(`等） | 小写（`family(`） | 代码风格统一 |
| 26 | `telemetry.Count()` | `sqltelemetry.xxxCounter()` 或 `telemetry.Inc` | 统一遥测 |
| 27 | `telemetry.GetCounter` | `sqltelemetry.xxxCounter()` | 统一遥测 |
| 28 | `collate.Supported()` | `collatedstring.Supported()` | 统一调用 |
| 29 | `database/sql` 无别名导入 | 必须 `import gosql "database/sql"` | 避免与内部 sql 包混淆 |
| 30 | `panic()` 直接调用在 sql/opt | `panic(errors.AssertionFailedf(...))` | 更好错误信息 |
| 31 | `pgerror.NewError/Wrap` + `CodeInternalError` | `errors.AssertionFailedf()` | 统一断言错误 |
| 32 | `coldata.NewMemBatch` / `coldata.NewVec` 直接调用 | `colmem.Allocator` | 向量化引擎内存管理 |
| 33 | `panic()` 在 sql/col* 包 | `colexecerror.InternalError()` 或 `.ExpectedError()` | 向量化错误路由 |
| 34 | `os.Exit()` | `exit.WithCode()` | 统一退出点 |
| 35 | `runtime.NumCPU()` | `system.NumCPU` | 容器感知 CPU 计数 |
| 36 | `t.Parallel()` 无 `SAFE FOR TESTING` 注释 | `sync.WaitGroup` 或加注释 | 防止并发测试干扰 |
| 37 | `t.Skip()` | `skip.WithIssue()` / `skip.IgnoreLint()` | 跳过时必须关联 issue |
| 38 | `yaml.Unmarshal()` | `yamlutil.UnmarshalStrict()` | 防止 YAML 字段漂移 |
| 39 | `oserror.Is*` 系列 | `oserror` 包封装 | 统一 OS 错误处理 |
| 40 | `pprof.Do` / `pprof.SetGoroutineLabels` | `pprofutil` 等价物 | 统一 pprof 标签 |

---

### pingcap/tidb

**启用 linter（15 个）：** bodyclose, copyloopvar, durationcheck, errcheck, gosec, ineffassign, intrange, lll, makezero, prealloc, predeclared, revive, rowserrcheck, staticcheck, unused

**revive:** 约 60 条规则（context-as-argument / error-naming / atomic 等）
**gosec:** 排除 G101 / G112
**Makefile:** `check-static`, `lint`, `check-parallel`（禁 t.Parallel()），`tools/check/check-errdoc.sh`（errdoc-gen 生成 errors.toml + diff 验证）

---

### temporalio/temporal

**Makefile target:** `lint-actions` (actionlint), `lint-api` (api-linter), `lint-protos` (buf lint), `lint-yaml` (yamlfmt), `unit-test`, `integration-test`, `functional-test`, `functional-with-fault-injection-test`, `verify-test-log`, `ensure-no-changes`（git status 干净检查），**TEST_RACE_FLAG=on 默认**（所有测试编译加 -race）

---

### hashicorp/vault

**depguard:** 仅允许 `github.com/hashicorp/go-metrics/compat`，禁 `go-metrics`/`armon/go-metrics`
**Makefile:** `testrace`(60min, CGO_ENABLED=1), `protolint` (buf), `check-proto-fmt`, `check-proto-delta`, `semgrep`, `check-semgrep`(CI 严格)
**tools/semgrep/ci/ 19 规则文件:** atomic / bad-multierror-append / bad-nil-guard / error-shadowing / fmt-printf / hashsum / hmac-bytes / hmac-hash / lock-not-unlocked-on-return / logger-format-string / loop-time-after / loopclosure / no-nil-check / oddifsequence / return-nil-error / return-nil / time-parse-duration / wrongerrcall / wronglock
**根目录 8 规则:** hostport / joinpath / logger-sprintf / paths-with-callbacks* / physical-storage / replication-has-state / self-equals

---

### go-kratos/kratos

**启用 linter（17 个）:** bodyclose, dogsled, durationcheck, errcheck, goconst, gocyclo, govet(shadow), ineffassign, lll(160), misspell, mnd, prealloc, revive, staticcheck, unconvert, unused, wastedassign, whitespace
**gocyclo:** min-complexity 50（大型框架宽松）
**格式化:** gofmt + gofumpt + goimports（local-prefixes `github.com/go-kratos`）

---

### uber-go/fx

**启用 linter:** govet, ineffassign, staticcheck, unused, errorlint, nolintlint, revive, goheader
**govet 扩展:** nilness, reflectvaluecompare, sortslice, unusedwrite
**goheader:** Copyright (c) {年份} Uber Technologies, Inc. + MIT license 完整文本验证
**revive:** 禁用 unused-parameter（含理由），禁用空代码块告警
**格式化:** gofumpt

---

### ThreeDotsLabs/watermill

**Makefile:** test, test_v, test_short, test_race, test_stress(30min), test_codecov(atomic), test_reconnect, validate_examples
（`.golangci.yml` 未独立，纯 Makefile 驱动）

---

### caddyserver/caddy

**29 个 linter:** asasalint, asciicheck, bidichk, bodyclose, decorder, dogsled, dupl, dupword, durationcheck, errcheck, errname, exhaustive, gosec, govet, importas, ineffassign, misspell, modernize, prealloc, promlinter, sloglint, sqlclosecheck, staticcheck, testableexamples, testifylint, tparallel, unconvert, unused, wastedassign, whitespace, zerologlint

**gosec 豁免:** G115/G107/G203/G204/G404
**格式化:** gci/gofmt/gofumpt/goimports（自定义 import 顺序）

---

### pulumi/pulumi

**21 个 linter** 含 depguard/forbidigo/goheader/perfsprint/usetesting/copyloopvar/gocritic
**forbidigo:** 强制 require.NoError 替代 assert.NoError；禁止 os.Rename
**goheader:** Apache 2.0 + Copyright [年份], Pulumi Corporation
**ruleguard:** 自定义规则 `${config-path}/.golangci/rules.go`

---

### minio/minio

**13 个 linter（极简）:** durationcheck, forcetypeassert, gocritic, gomodguard, govet, ineffassign, misspell, revive, staticcheck, unconvert, unused, usetesting, whitespace
**Makefile:** test-race, install-race(GORACE=history_size=7), check-gen(go generate + git diff)

---

### grafana/grafana

**20 个 linter** + depguard 8 个 rule 块（apimachinery/apiserver/apps/coreplugins/storage 模块隔离）
**main 全局禁用:** io/ioutil / yaml.v2/v3 / pkg/errors / xorcare/pointer / gofrs/uuid / bmizerany/assert
**timeout 15m, concurrency 10**

---

### nats-io/nats-server

**6 个 linter（最简）:** forbidigo, govet, ineffassign, misspell, staticcheck, unused
**forbidigo:** 禁 fmt.Print/Printf/Println（输出走专用日志，主二进制例外豁免）
**govet printf 扩展:** logutils.Log Infof/Warnf/Errorf/Fatalf 识别

---

### open-policy-agent/opa

**15 个 linter:** copyloopvar, errcheck, gocritic, govet(deepequalerrors+nilness), ineffassign, intrange, mirror, misspell, perfsprint, prealloc, revive, staticcheck, unconvert, unused, usetesting
**gosec 显式注释关闭**（false positive 太多）
**lll 200 字符**
**Docker 化执行:** `golangci/golangci-lint:${VERSION}` 容器

---

### cilium/cilium

**17 个 linter:** copyloopvar, depguard, err113, errorlint, forbidigo, goheader, gomodguard, gosec(G402), govet(nilness), ineffassign, misspell, modernize, sloglint, staticcheck, testifylint, unused
**gomodguard 13 模块阻断:** miekg/dns→cilium/dns, goleak→testutils, logrus→slog, x/exp/*→stdlib, yaml.v2/v3→go.yaml.in, k8s.io/utils/pointer→ptr 等
**forbidigo 50+ netlink 函数禁令**（强制 safenetlink）
**sloglint:** camelCase key, no-raw-keys, forbidden-keys=[time/level/msg/source]

---

### crossplane/crossplane

**default: all** 减法策略，禁约 30 个
**depguard 测试文件 deny:** stretchr/testify, ginkgo, gomega（要求 stdlib testing）
**tagliatelle:** json goCamel
**interfacebloat:** max 5 方法
**nolintlint:** require-explanation + require-specific
**unparam check-exported false, unused exported-fields-are-used true**

---

### spiffe/spire

**21 个 linter:** bodyclose, copyloopvar, durationcheck, errorlint, exptostd, gocritic, gosec, intrange, mirror, misspell, nakedret, nilerr, nilnesserr, nolintlint, predeclared, reassign, revive, unconvert, unparam, wastedassign, whitespace
**revive 24 条规则**（atomic / bool-literal / constant-logical / context-as-argument / datarace 等）
**Makefile:** lint-code(go run @version), race-test(必跑), govulncheck(必跑)
**timeout 12m**

---

### gravitational/teleport

**14 个 linter** + 三层 depguard（main / api 域 / client-tools 二进制尺寸）
**forbidigo 13 条:** crypto/rsa.GenerateKey→cryptosuites, AWS SDK iam/sts/stscreds wrapper, protojson 替代 jsonpb 等
**sloglint:** context: all, snake_case key, static-msg, forbidden-keys=[level/msg/source/time]
**timeout 15m, go 1.23**

---

### prometheus/prometheus

**21 个 linter** 含 depguard/errorlint/exptostd/fatcontext/gocritic(enable-all)/godot/loggercheck/modernize/nilnesserr/perfsprint/predeclared/revive/sloglint/testifylint/usestdlibvars
**depguard 9 条 deny:** sync/atomic→uber/atomic, testify/assert→require, regexp→grafana/regexp, gzip/zlib→klauspost/compress, yaml.v2/v3→go.yaml.in, pkg/errors→stdlib 等
**warn-unused: true**（无效 nolint 警告）
**timeout 15m**

---

### traefik/traefik

**default: all** 减法（禁 ~37 个）
**importas 25 条 K8s 别名**（corev1, netv1, metav1, gateinformers 等）
**tagalign:** 强制 description→json→toml→yaml→yml 顺序
**funlen:** statements 120
**gocyclo min-complexity 14**
**forbidigo:** print/println/spew.Print/spew.Dump 禁

---

## §2 跨项目规则频次矩阵

列：K8s, CRDB, TiDB, Temp, Vault, fx, Krat, Caddy, Pulu, Mini, Graf, NATS, OPA, Cili, Cross, SPIR, Tele, Prom, Trae（共 19 项目）

✅=启用, —=未启用, ?=未拉到对应文件

| # | 规则/工具 | K8s | CRDB | TiDB | Temp | Vault | fx | Krat | Caddy | Pulu | Mini | Graf | NATS | OPA | Cili | Cross | SPIR | Tele | Prom | Trae | 启用数 |
|---|-----------|-----|------|------|------|-------|----|------|-------|------|------|------|------|-----|------|-------|------|------|------|------|--------|
| 1 | `staticcheck` | ✅ | ? | ✅ | ? | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 18/19 |
| 2 | `ineffassign` | ✅ | ? | ✅ | ? | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 18/19 |
| 3 | `govet` | ✅ | ? | ✅ | ? | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 18/19 |
| 4 | `unused` | ✅ | ? | ✅ | ? | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 18/19 |
| 5 | `goimports` | ✅ | ? | ✅ | ? | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 18/19 |
| 6 | `gofmt` | ✅ | ? | ✅ | ? | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 18/19 |
| 7 | `gosimple`（含于 staticcheck） | ✅ | ? | ✅ | ? | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 18/19 |
| 8 | `misspell` | — | ? | ✅ | ? | ✅ | — | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | — | 15/19 |
| 9 | `revive` | ✅ | ? | ✅ | ? | ✅ | ✅ | ✅ | — | ✅ | ✅ | ✅ | — | ✅ | — | ✅ | ✅ | ✅ | ✅ | — | 14/19 |
| 10 | `errcheck` | — | ? | ✅ | ? | ✅ | — | ✅ | ✅ | ✅ | — | ✅ | — | ✅ | — | ✅ | — | — | ✅ | — | 10/19 |
| 11 | `unconvert` | — | ? | ✅ | ? | — | — | ✅ | ✅ | ✅ | ✅ | ✅ | — | ✅ | — | ✅ | ✅ | — | ✅ | — | 10/19 |
| 12 | `gosec` | — | ? | ✅ | ? | ✅ | — | — | ✅ | ✅ | — | ✅ | — | — | ✅ | ✅ | ✅ | ✅ | ✅ | — | 10/19 |
| 13 | `gocritic` | ✅ | ? | ✅ | ? | — | — | — | — | ✅ | ✅ | — | — | ✅ | — | ✅ | ✅ | — | ✅ | — | 9/19 |
| 14 | `depguard` | ✅ | ? | — | ? | ✅ | — | — | — | ✅ | ✅ | ✅ | — | — | ✅ | ✅ | — | ✅ | ✅ | — | 9/19 |
| 15 | `-race` flag in CI | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ? | ? | ✅ | ? | ? | ✅ | ✅ | ? | ✅ | ✅ | ? | ? | 11/19 |
| 16 | `bodyclose` | — | ? | ✅ | ? | — | — | ✅ | ✅ | — | — | ✅ | — | — | — | ✅ | ✅ | ✅ | ✅ | — | 8/19 |
| 17 | `durationcheck` | — | ? | ✅ | ? | — | — | ✅ | ✅ | ✅ | ✅ | — | — | ✅ | — | ✅ | ✅ | — | — | — | 8/19 |
| 18 | `gofumpt` | — | ? | — | ? | — | ✅ | ✅ | ✅ | ✅ | ✅ | — | — | — | — | ✅ | — | — | ✅ | ✅ | 8/19 |
| 19 | `prealloc` | — | ? | ✅ | ? | — | — | ✅ | ✅ | ✅ | — | ✅ | — | ✅ | — | — | — | — | — | — | 7/19 |
| 20 | `whitespace` | — | ? | — | ? | — | — | ✅ | ✅ | ✅ | ✅ | ✅ | — | — | — | ✅ | ✅ | — | — | — | 7/19 |
| 21 | `wastedassign` | — | ? | — | ? | — | — | ✅ | ✅ | ✅ | — | — | — | ✅ | — | ✅ | ✅ | — | — | — | 6/19 |
| 22 | `errorlint` | — | ? | — | ? | — | ✅ | — | — | — | — | ✅ | — | — | ✅ | — | ✅ | ✅ | ✅ | — | 6/19 |
| 23 | `copyloopvar` | — | ? | ✅ | ? | — | — | — | — | ✅ | — | — | — | ✅ | ✅ | ✅ | ✅ | — | — | — | 6/19 |
| 24 | `forbidigo` | ✅ | ? | — | ? | — | — | — | — | ✅ | — | — | ✅ | — | ✅ | — | — | ✅ | — | ✅ | 6/19 |
| 25 | `nolintlint` | — | ? | — | ? | — | ✅ | — | — | ✅ | — | — | — | — | — | ✅ | ✅ | ✅ | — | — | 5/19 |
| 26 | `gci` | — | ? | — | ? | — | — | ✅ | ✅ | — | — | — | — | — | — | ✅ | — | ✅ | ✅ | ✅ | 6/19 |
| 27 | `exhaustive` | — | ? | — | ? | — | — | — | ✅ | ✅ | — | ✅ | — | — | — | ✅ | — | — | — | — | 4/19 |
| 28 | `testifylint` | ✅ | ? | — | ? | — | — | — | ✅ | — | — | — | — | — | ✅ | — | — | ✅ | ✅ | — | 5/19 |
| 29 | `goheader` | — | ? | — | ? | — | ✅ | — | — | ✅ | — | — | — | — | ✅ | — | — | ✅ | — | — | 4/19 |
| 30 | `nakedret` | — | ? | — | ? | — | — | — | — | ✅ | — | ✅ | — | — | — | — | ✅ | — | — | — | 3/19 |
| 31 | `sloglint` | — | ? | — | ? | — | — | — | ✅ | — | — | — | — | — | ✅ | — | — | ✅ | ✅ | — | 4/19 |
| 32 | `usetesting` | — | ? | — | ? | — | — | — | — | ✅ | ✅ | — | — | ✅ | — | — | — | — | — | — | 3/19 |
| 33 | `modernize` | ✅ | ? | — | ? | — | — | — | ✅ | — | — | — | — | — | ✅ | — | — | — | — | — | 3/19 |
| 34 | `intrange` | — | ? | ✅ | ? | — | — | — | — | — | — | — | — | ✅ | — | — | ✅ | — | — | — | 3/19 |
| 35 | `perfsprint` | — | ? | — | ? | — | — | — | — | ✅ | — | — | — | ✅ | — | — | — | — | ✅ | — | 3/19 |
| 36 | `govet: nilness` | ✅ | ? | — | — | — | ✅ | — | — | — | — | — | — | ✅ | ✅ | — | — | — | — | — | 4/19 |
| 37 | `govet: sortslice` | — | ? | — | — | — | ✅ | — | — | — | — | — | — | — | — | — | ✅ | — | — | — | 2/19 |
| 38 | `govet: unusedwrite` | — | ? | — | — | — | ✅ | — | — | — | — | — | — | — | — | — | ✅ | — | — | — | 2/19 |
| 39 | `gomodguard` | — | ? | — | — | — | — | — | — | — | ✅ | — | — | — | ✅ | — | — | — | — | — | 2/19 |
| 40 | `importas` | — | ? | — | — | — | — | — | ✅ | ✅ | — | — | — | — | — | — | — | — | — | ✅ | 3/19 |
| 41 | `mirror` | — | ? | — | — | — | — | — | — | — | — | — | — | ✅ | — | — | ✅ | — | — | — | 2/19 |
| 42 | `nilerr` | — | ? | — | ? | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | — | — | 1/19（GoCell 已有！） |
| 43 | `nilnesserr` | — | ? | — | ? | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | ✅ | — | 2/19（GoCell 已有！） |
| 44 | `nilnil` | — | ? | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 0/19（GoCell 独有）|
| 45 | `gocognit` | — | ? | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 0/19（GoCell 独有）|
| 46 | `forbidigo: 禁 fmt.Print*` | — | ? | — | ? | — | — | — | — | — | — | — | ✅ | — | — | — | — | — | — | ✅ | 2/19 |
| 47 | `forbidigo: 禁 md5/sha1` | ✅ | ? | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 48 | `depguard: 禁 io/ioutil` | — | ? | — | — | — | — | — | — | — | — | ✅ | — | — | — | — | — | ✅ | ✅ | — | 3/19 |
| 49 | `depguard: 禁 pkg/errors` | — | ? | — | — | — | — | — | — | — | — | ✅ | — | — | — | — | — | — | ✅ | ✅ | 3/19 |
| 50 | `depguard: 禁 logrus → slog` | — | ? | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | — | ✅ | — | — | 2/19 |
| 51 | `depguard: 禁 yaml.v2 → go.yaml.in` | — | ? | — | — | — | — | — | — | — | — | ✅ | — | — | ✅ | — | — | — | ✅ | — | 3/19 |
| 52 | `depguard: 禁 x/exp/*` | — | ? | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | — | ✅ | ✅ | — | 3/19 |
| 53 | `depguard: 禁旧 protobuf` | — | ✅ | — | — | — | — | — | — | ✅ | — | — | — | — | — | — | — | ✅ | — | — | 3/19 |
| 54 | `depguard: 禁 testify/assert（用 require）` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | 1/19 |
| 55 | `depguard: 架构层级隔离` | ✅ | ? | — | — | — | — | — | — | ✅ | — | ✅ | — | — | — | ✅ | — | ✅ | — | — | 5/19 |
| 56 | `逻辑：禁 panic() 直接调用` | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 57 | `逻辑：禁 sync.Mutex 直用` | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 58 | `逻辑：禁 time.Now() 直用` | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 59 | `逻辑：t.Skip() 必须关 issue` | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 60 | `kubeapilinter（K8s API 检查）` | ✅ | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 61 | `import-boss（包导入层次）` | ✅ | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 62 | `featuregate 生命周期验证` | ✅ | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 63 | `errdoc-gen + diff` | — | ? | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 64 | `semgrep` | — | ? | — | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 65 | `actionlint` | — | ? | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 66 | `buf lint` | — | ? | — | ✅ | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 2/19 |
| 67 | `api-linter` | — | ? | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 68 | `yamlfmt / yamllint` | — | ? | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | 2/19 |
| 69 | `paralleltest` | — | ? | — | — | — | — | — | — | ✅ | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 70 | `tparallel` | — | ? | — | — | — | — | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 71 | `dupl` | — | ? | — | — | — | — | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 72 | `errname` | — | ? | — | — | — | — | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | 1/19 |
| 73 | `forcetypeassert` | — | ? | — | — | — | — | — | — | — | ✅ | — | — | — | — | — | — | — | — | — | 1/19 |
| 74 | `goheader (license)` | — | ? | — | — | — | ✅ | — | — | ✅ | — | — | — | — | ✅ | — | — | ✅ | — | — | 4/19 |
| 75 | `tagliatelle (JSON tag case)` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | — | — | — | 1/19 |
| 76 | `interfacebloat` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | — | — | — | 1/19 |
| 77 | `predeclared` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | ✅ | — | 2/19 |
| 78 | `reassign` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | — | — | 1/19 |
| 79 | `unparam` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | — | — | 1/19 |
| 80 | `exptostd (exp pkg → stdlib)` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | ✅ | — | 2/19 |
| 81 | `loggercheck` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | 1/19 |
| 82 | `usestdlibvars` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | 1/19 |
| 83 | `fatcontext` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | — | 1/19 |
| 84 | `tagalign` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | 1/19 |
| 85 | `funlen` | — | ? | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | ✅ | 1/19 |
| 86 | `goconst` | — | ? | — | — | — | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | ✅ | 2/19 |
| 87 | `lll` | — | ? | ✅ | — | — | — | ✅ | — | ✅ | — | — | — | ✅ | — | — | — | — | — | — | 4/19 |
| 88 | `dogsled` | — | ? | — | — | — | — | ✅ | ✅ | — | — | ✅ | — | — | — | — | — | — | — | — | 3/19 |
| 89 | `gocyclo` | — | ? | — | — | — | — | ✅ | — | — | — | ✅ | — | — | — | — | — | — | — | ✅ | 3/19 |
| 90 | `mnd (魔数)` | — | ? | — | — | — | — | ✅ | — | — | — | — | — | — | — | — | — | — | — | — | 1/19 |

---

## §3 工业基线分级

### Tier 1（必启，≥ 10/19 项目启用）

| 规则/工具 | 启用数 | 说明 |
|-----------|--------|------|
| `staticcheck`（含 gosimple） | 18/19 | 工业绝对共识 |
| `ineffassign` | 18/19 | 工业绝对共识 |
| `govet`（基础检查） | 18/19 | 工业绝对共识 |
| `unused` | 18/19 | 工业绝对共识 |
| `gofmt` / `goimports` | 18/19 | 格式化工业共识 |
| `misspell`（拼写检查，US locale） | 15/19 | 强烈推荐 |
| `revive`（替代 golint） | 14/19 | 综合规则集 |
| `errcheck` | 10/19 | 工业共识 |
| `gosec` | 10/19 | 安全扫描共识 |
| `unconvert`（多余类型转换） | 10/19 | 工业共识 |
| `go test -race` 作为 CI 阻塞项 | 11/19（已确认）| 工业共识 |

### Tier 2（推荐，6-9/19 项目启用）

| 规则/工具 | 启用数 | 说明 |
|-----------|--------|------|
| `depguard`（依赖白/黑名单） | 9/19 | 架构分层利器 |
| `gocritic` | 9/19 | 综合代码批评 |
| `gofumpt`（严格 gofmt） | 8/19 | 更严格格式化 |
| `bodyclose`（HTTP body 关闭） | 8/19 | 防资源泄漏 |
| `durationcheck` | 8/19 | 时间运算错误 |
| `prealloc` | 7/19 | slice 预分配 |
| `whitespace` | 7/19 | 空行规范 |
| `wastedassign` | 6/19 | 浪费的赋值 |
| `errorlint`（错误链规范） | 6/19 | errors.As/Is 用法 |
| `copyloopvar` | 6/19 | Go 1.22 前必须 |
| `forbidigo` | 6/19 | 自定义禁用模式 |
| `gci`（import 分组排序） | 6/19 | 强制导入顺序 |

### Tier 3（按需，< 6/19 项目启用）

| 规则/工具 | 启用数 | 说明 |
|-----------|--------|------|
| `nolintlint`（nolint 必须有说明） | 5/19 | 治理 nolint 滥用 |
| `testifylint` | 5/19 | testify 正确用法 |
| `depguard 架构分层` | 5/19 | 适合大项目 |
| `exhaustive`（枚举穷举） | 4/19 | switch 完整性 |
| `goheader`（license 头） | 4/19 | 开源项目必备 |
| `sloglint` | 4/19 | slog 使用规范（新兴） |
| `govet: nilness` | 4/19 | nil 指针分析 |
| `lll` | 4/19 | 行长限制 |
| `nakedret` | 3/19 | 裸 return 控制 |
| `usetesting` | 3/19 | 测试函数正确使用 |
| `modernize` | 3/19 | Go 新语法提示 |
| `intrange` | 3/19 | for range int 语法 |
| `perfsprint` | 3/19 | sprintf 性能 |
| `dogsled` | 3/19 | 多 _ 变量警告 |
| `gocyclo` | 3/19 | 圈复杂度 |
| `importas` | 3/19 | 包别名强制 |
| `depguard: 禁 io/ioutil` | 3/19 | 已废弃包 |
| `depguard: 禁 pkg/errors` | 3/19 | 旧错误库 |
| `depguard: 禁 yaml.v2` | 3/19 | 旧 yaml |
| `depguard: 禁 x/exp/*` | 3/19 | Go 1.21+ stdlib |
| `depguard: 禁旧 protobuf` | 3/19 | proto 版本统一 |
| `semgrep`（自定义语义规则） | 1/19 | Vault 特色 |
| `actionlint` | 1/19 | CI workflow 验证 |
| `buf lint` | 2/19 | proto 规范 |
| `import-boss`（包导入层次） | 1/19 | 超大项目专用 |
| `errdoc-gen` | 1/19 | TiDB 特色 |
| `kubeapilinter` | 1/19 | K8s API 检查 |
| 其他长尾规则 | 1-2/19 | 各项目特色 |

---

## §4 GoCell 当前缺失项（Tier 1 + Tier 2 对照）

### Tier 1 缺失项（必须补的）

1. `misspell` — 15/19 项目启用，GoCell 未启用；英文注释/变量名拼写错误完全漏检
2. `revive` — 14/19 项目启用；GoCell 目前无综合规则集，仅靠 staticcheck 不够
3. `gosec` — 10/19 项目启用；GoCell 有 JWT/加密/OIDC 代码，安全扫描是基础门槛
4. `unconvert` — 10/19 项目启用；多余类型转换是常见低质量代码
5. `errcheck` — 10/19；GoCell 已有默认启用，但需确认测试文件豁免规则对齐

### Tier 2 缺失项（推荐补的）

6. `depguard` — 9/19；GoCell 有严格的层次依赖规则（kernel 不依赖 runtime/adapters 等），当前只靠人工 review，没有工具守护
7. `bodyclose` — 8/19；GoCell 有 HTTP 客户端调用（OIDC / S3 / webhook），未关闭 body 是常见泄漏点
8. `durationcheck` — 8/19；时间运算错误，adapters/redis 和 runtime/worker 里有超时设置
9. `gofumpt` — 8/19；比 gofmt 更严格，GoCell 仅有 gofmt
10. `errorlint` — 6/19；GoCell 使用 `%w` wrap，errorlint 保证 `errors.Is/As` 链正确

### GoCell 已启用但不在主流的（说明价值）

- `nilnil` — 0/19 主流项目启用，但 GoCell 启用是合理的（防接口 nil 歧义）
- `gocognit` — 0/19，GoCell 启用因 CLAUDE.md 限制 ≤15
- `nilerr` / `nilnesserr` — 1-2/19，GoCell 已启用是前瞻性

---

## §5 引用清单

- [kubernetes/kubernetes hack/golangci.yaml](https://raw.githubusercontent.com/kubernetes/kubernetes/master/hack/golangci.yaml)
- [cockroachdb/cockroach pkg/testutils/lint/lint_test.go](https://raw.githubusercontent.com/cockroachdb/cockroach/master/pkg/testutils/lint/lint_test.go)
- [pingcap/tidb .golangci.yml](https://raw.githubusercontent.com/pingcap/tidb/master/.golangci.yml)
- [hashicorp/vault .golangci.yml](https://raw.githubusercontent.com/hashicorp/vault/main/.golangci.yml)
- [uber-go/fx .golangci.yml](https://raw.githubusercontent.com/uber-go/fx/master/.golangci.yml)
- [go-kratos/kratos .golangci.yml](https://raw.githubusercontent.com/go-kratos/kratos/main/.golangci.yml)
- [caddyserver/caddy .golangci.yml](https://raw.githubusercontent.com/caddyserver/caddy/master/.golangci.yml)
- [pulumi/pulumi .golangci.yml](https://raw.githubusercontent.com/pulumi/pulumi/master/.golangci.yml)
- [minio/minio .golangci.yml](https://raw.githubusercontent.com/minio/minio/master/.golangci.yml)
- [grafana/grafana .golangci.yml](https://raw.githubusercontent.com/grafana/grafana/main/.golangci.yml)
- [nats-io/nats-server .golangci.yml](https://raw.githubusercontent.com/nats-io/nats-server/main/.golangci.yml)
- [open-policy-agent/opa .golangci.yaml](https://raw.githubusercontent.com/open-policy-agent/opa/main/.golangci.yaml)
- [cilium/cilium .golangci.yaml](https://raw.githubusercontent.com/cilium/cilium/main/.golangci.yaml)
- [crossplane/crossplane .golangci.yml](https://raw.githubusercontent.com/crossplane/crossplane/main/.golangci.yml)
- [spiffe/spire .golangci.yml](https://raw.githubusercontent.com/spiffe/spire/main/.golangci.yml)
- [gravitational/teleport .golangci.yml](https://raw.githubusercontent.com/gravitational/teleport/master/.golangci.yml)
- [prometheus/prometheus .golangci.yml](https://raw.githubusercontent.com/prometheus/prometheus/main/.golangci.yml)
- [traefik/traefik .golangci.yml](https://raw.githubusercontent.com/traefik/traefik/master/.golangci.yml)
- [temporalio/temporal Makefile](https://raw.githubusercontent.com/temporalio/temporal/main/Makefile)
- [hashicorp/vault Makefile](https://raw.githubusercontent.com/hashicorp/vault/main/Makefile)
