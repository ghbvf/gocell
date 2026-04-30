# GoCell 非 lint 工程基础设施现状 + SoT 项目交叉对标

> 日期：2026-04-30
> 任务：在 lint/CI 治理研究（见 `../ci-governance/`）之外，扫 GoCell 12 个工程维度的现状，并对 K8s/kubebuilder、Temporal/fx、CRDB/Vault 三组 SoT 项目做横向对标，作为决策文档的数据底稿
> 调研方式：4 个并行 explorer agent（1 GoCell 现状盘点 + 3 SoT 项目调研，每个 SoT agent 2 项目 × 3 维度，所有结论均有 raw URL 证据）
> 关联文件：
> - `202604300500-engineering-priority-decision.md`（基于本文的 6 落点决策 + 路线图）
> - `../ci-governance/`（lint/CI 治理研究链路，已完成）

---

## §1 GoCell 12 维度现状盘点

### 维度 1 — 代码生成（codegen）

**有：**
- `cmd/gocell/app/dispatch.go` 注册 5 个子命令：`validate / scaffold / generate / check / verify`
- `generate` 子命令支持 `assembly --boundary-only` 和 `metrics-schema --id=<id>` 两条生成路径
- `assemblies/corebundle/generated/boundary.yaml`（contracts 导出/导入指纹）和 `assemblies/corebundle/generated/metrics-schema.yaml`（metric 名称/label/桶注册清单）均为工具生成产物
- CI 在 `_build-lint.yml` kernel shard 有两个 verify-and-diff gate：重生成后 `git diff --exit-code`，漂移即失败（PR-CFG-M / OBS-01）
- `scaffold` 子命令负责 new-cell/slice/contract/journey 骨架生成（`--dry-run` 可用）

**没有：**
- 无 contract → handler stub 生成（路由声明手写，通过 `auth.Mount` 注册）
- 无 mock 生成工具（mockgen / counterfeiter 均未引入）；mock 均手写在 `*_test.go` 同包内
- 无 protobuf / openapi → Go 生成链（合同是 YAML contract.yaml，不是 proto）
- 无 `go generate` 注释驱动的生成

**判断：半成品**。生成链专注于 assembly boundary 和 metrics schema 两条核心路径，codegen diff-gate 已接入 CI；contract→handler 和 mock 生成尚不存在。

---

### 维度 2 — 测试基础设施

**有：**
- `go.mod` 引入 `testcontainers-go v0.42.0` + postgres/redis/rabbitmq/vault 四个 module，integration 测试用真实容器
- build tag `//go:build integration` 隔离；CI `test-integration` 目标传 `-tags=integration,e2e`
- E2E harness：`tests/e2e/docker-compose.e2e.yaml`（4-service：postgres + redis + migrate + corebundle），CI 跑完整 docker-compose up → bootstrap-admin.sh → go test -tags=e2e 链路，自定义 `tools/e2egate` 工具做执行门控（零测试即失败）
- `examples/ssobff` 下有 subprocess smoke test（`//go:build examples_smoke`），CI job `examples-smoke` 独立运行
- `go.uber.org/goleak v1.3.0` 引入做 goroutine 泄漏检测
- Makefile `test-integration` / `test-examples-smoke` 本地可复现 CI 命令

**没有：**
- 无公共 table-driven test helper 库
- fixtures/ 目录存在但未找到 `run-journey` 直接消费 fixture-*.yaml 的 Go 入口
- 无 HTTP contract 测试框架（pact / dredd）

**判断：成熟**。testcontainers + 真实容器 + docker-compose E2E + goleak + e2egate 主动门控构成完整测试层次。

---

### 维度 3 — 错误处理 / 可观测性栈

**有：**
- `pkg/errcode/errcode.go`：`Error{Code, Message, InternalMessage, Details, Cause, Category}` 结构体；`New/Safe/Wrap/WithDetails` 构造器；`Unwrap()` 支持 errors.Is/As 链
- `InternalMessage` 字段区分日志诊断信息和 API 响应（5xx 永远不暴露 InternalMessage）
- `pkg/httputil/response.go`：`MapCodeToStatus(code)` / `WriteDomainError` / `WritePublicError` / `WriteError` 统一出口；5xx 自动 mask
- 80+ Code 常量覆盖所有模块边界
- `kernel/observability/metrics/metrics.go`：`Provider` 接口抽象，具体实现在 adapters/（Prometheus、OTel）
- `go.opentelemetry.io/otel v1.43.0` + OTLP gRPC exporter + B3 propagator 已引入；stdlib simpleTracer + OTel 适配器
- `metrics-schema.yaml` 记录所有 metric 的名称 / label / 桶，archtest OBS-01 门控防 label 漂移
- `docs/observability/metrics-migration-acks.yaml` 人工确认文件
- `ctxkeys` 包注入 `request_id / trace_id / span_id / correlation_id`，4xx/5xx 路径自动附加

**没有：**
- 无 archtest 锁定 log key 名称漂移（metric label 有 OBS-01，log key 无对应门控）
- 无 pprof 集成端点

**判断：成熟**。三层可观测性（metrics 抽象 + OTel trace + slog 结构化日志）+ metrics-schema 漂移门控均到位。

---

### 维度 4 — 配置 / 启动生命周期

**有：**
- `runtime/bootstrap/bootstrap.go`：10 个 phase（phase0 validate → phase1 config → ... → phase10 LIFO teardown）
- LIFO teardown 参照 fx 和 controller-runtime；startup rollback on phase failure
- `kernel/metadata` 用 `yaml.KnownFields(true)` strict parse
- cell.yaml / slice.yaml strict 字段集由 `gocell validate --strict` 拦截
- 配置热更新：`runtime/config/watcher.go` 用 `fsnotify` + debounce，支持 K8s ConfigMap symlink pivot
- readyz：`WithHealthChecker(name, fn)` 注册具名 checker；`_ready` 后缀命名规范在 observability.md
- `WithReadyzVerboseToken` 显式 verbose 保护（fail-closed）

**没有：**
- 无 Consul / etcd 等远程配置后端
- 无 readyz probe 命名漂移的 archtest 门控

**判断：成熟**。10-phase 启动 + LIFO teardown + fail-closed + 配置热更新均到位。

---

### 维度 5 — 依赖注入 / 模块组装

**有：**
- `runtime/bootstrap` 用 Option pattern（非 DI container），显式类型化、编译期检查
- `assemblies/corebundle/assembly.yaml` 声明 cells + entrypoint + binary + deployTemplate
- `cmd/corebundle/main.go` 和 `cmd/gocell/main.go` 两个 CLI 入口
- 依赖方向由 archtest LAYER-01~10 静态守卫
- `bootstrap.Lifecycle.WithLifecycle(fn)` 注册 Start/Stop 回调
- `cell.yaml allowedFiles` + `internal/` 目录约束 + archtest 三重守卫包级可见性（比 `fx.Private` 更结构化）

**没有：**
- 无 fx / wire 等 DI 框架（**这是有意选择，不是缺失**：CLAUDE.md「领域逻辑保留自建」原则）
- main.go composition root 手写
- `boundary.yaml` 没有 DOT/SVG 可视化导出工具

**判断：成熟**（评级修正）。GoCell 走的是 "显式 Option pattern + 10-phase orchestration + archtest LAYER + boundary.yaml" 这条路径，与 fx 的「反射 DI + 隐式装配」是两条不同但都成立的设计。GoCell 的选择有以下优势：(a) 类型安全编译期检查；(b) 启动错误不依赖 DOT 图解读；(c) 与 archtest LAYER + cell.yaml allowedFiles 形成多重守卫，比 fx.Private 单层更严。

**真实缺口**（限定为 2 项轻量改进，而非引入框架）：
1. `bootstrap.Lifecycle` 在 phase 内多个 hook 失败时是否 LIFO 触发已注册的 OnStop —— 待代码审查确认
2. `boundary.yaml` 缺 DOT 可视化导出工具（吸收 `fx.Visualize` 的开发体验语义，但实现自建）

详见决策文档 L3 节。

---

### 维度 6 — 安全 / 供应链

**有：**
- `.github/dependabot.yml`：github-actions + gomod 两生态，weekly，分 4 组（testcontainers / OTel / golang.org/x / other）
- `pkg/secutil.ValidateTLSEndpoint`：非 loopback 的 non-TLS 端点构造时 fail-fast
- `goleak` goroutine 泄漏检测
- CI actions 均 pin commit hash

**没有：**
- 无 govulncheck（CI 无 `.github/workflows/security.yml`）
- 无 SBOM / cyclonedx
- 无镜像签名 / cosign
- 无 trufflehog / gitleaks
- 无 SAST（除 lint 外）

**判断：缺位明显**。Actions pin + dependabot 已做；govulncheck / SBOM / secret scan / 签名均缺。

---

### 维度 7 — 发布工程

**有：**
- 单 go.mod（`github.com/ghbvf/gocell`）
- Makefile `build` 输出到 `bin/`；Conventional Commits 强制
- CI `sonarcloud` job

**没有：**
- 无 goreleaser
- 无 semver / calver tag 自动化
- 无 changelog 自动化
- 无 GitHub Release job

**判断：缺位**。

---

### 维度 8 — 数据迁移 / Schema

**有：**
- `adapters/postgres/migrator.go`：基于 `pressly/goose/v3`，`Up/Down/Status/Close`；`Up()` 前检测 INVALID 索引拒绝迁移
- `NewMigrator` 接受 embed.FS 支持多 tableName
- goose 提供 advisory locking
- CI e2e job 跑 docker-compose harness，`migrate` 服务 one-shot 跑 `Up`

**没有：**
- 无 CI up/down 对称验证
- 无 atlas lint
- 无 migration 文件独立测试 job

**判断：半成品**。goose 集成完整 + invalid-index 前检查；down 对称未自动化。

---

### 维度 9 — API 版本治理

**有：**
- `contracts/` 按 `{kind}/{domain-path}/{version}/` 组织；`boundary.yaml` 记录 `exportedContracts / importedContracts`，含 31 个导出契约（v1 前缀）
- `gocell validate --strict`：拦截 `FMT-14/16/C1/A1` 等；`ADV-06` 双向校验 `endpoints.subscribers ↔ contractUsages[role=subscribe]`；`VERIFY-01` 检查 verify.contract ↔ contractUsages 闭环
- API 版本规范：`/api/v1/`、`/internal/v1/`
- sourceFingerprint in boundary.yaml：contract.yaml 结构变化指纹漂移即 CI fail

**没有：**
- 无 buf / protolint / openapi linter（契约是 YAML 非 proto/OpenAPI）
- 无 api-linter / kube-api-linter 等价物
- 无 storageVersion 标记 + 弃用窗口硬约束

**判断：半成品**。YAML contract + fingerprint diff gate 是自定义治理；缺主流 API spec lint 的等价物。

---

### 维度 10 — 文档自动化

**有：**
- `cmd/gocell/app/dispatch.go` 的 `PrintUsage()` 给 inline 帮助
- `docs/references/framework-comparison.md` 手写但完整
- `docs/architecture/metadata-model-v3.md` 维护元数据真相

**没有：**
- 无 godoc 完整度检查
- 无 API reference 自动生成
- 无 changelog 自动化（release-drafter / git-cliff / conventional-changelog）
- 无 ADR 体系目录

**判断：缺位**。

---

### 维度 11 — 性能基线

**有：**
- CI kernel/runtime shard 有 `-race`
- `goleak` goroutine 检测
- `adapters/postgres/pool.go` `PoolStats()` 暴露连接池统计

**没有：**
- 无 benchmark 文件
- 无 benchstat CI 比对
- 无 pprof endpoint
- 无 load-test harness（k6 / vegeta）

**判断：缺位**。

---

### 维度 12 — CLI / DX

**有：**
- `gocell` 子命令：`validate / scaffold / generate / check / verify`
- POSIX 退出码分级：`ExitOK=0 / ExitRuntime=1 / ExitUsage=2`
- `cmd/gocell/app` 包可直接 import 用于 smoke test
- `hack/verify-*.sh` glob 发现机制

**没有：**
- 无 shell completion
- 无交互 TUI
- 无 `--json` 顶层统一格式（部分子命令有 `--format json|text|sarif`）

**判断：半成品**。命令清晰，但 completion + JSON 输出不全；底层手写 dispatch map 而非 cobra。

---

## §2 SoT 对标矩阵 — K8s + kubebuilder（A 代码生成 / G API 治理 / H 文档自动化）

### A. 代码生成体系

**K8s `hack/update-codegen.sh`**
依次调用十余个独立 generator 二进制（`k8s.io/code-generator` 系列）：deepcopy、defaults、conversion、register、openapi、applyconfigs、clients、listers、informers。每个 generator 接受 `--output-file=zz_generated.*.go`。产物分散写入各包，加前缀 `zz_generated.` 区分。

**Lock 机制**（重点）：`hack/verify-codegen.sh` → `hack/lib/verify-generated.sh` 通过 `git worktree add -f -q "${_tmpdir}" HEAD` 建**隔离沙箱**，沙箱内重跑生成命令，再用 `git status --porcelain | wc -l` 计数修改文件，非零即判定 stale。这比 `git diff` 更稳——本地工作区 dirty 不会污染。

证据：
- https://raw.githubusercontent.com/kubernetes/kubernetes/master/hack/update-codegen.sh
- https://raw.githubusercontent.com/kubernetes/kubernetes/master/hack/lib/verify-generated.sh

**kubebuilder / controller-tools**
入口 `make generate`，串联 `generate-testdata` 和 `generate-docs`。Marker 体系（以 `// +` 开头单行注释）：

- 字段级：`// +kubebuilder:validation:Maximum=100`、`// +required`、`// +optional`、`// +default=xxx`
- 类型级：`// +kubebuilder:resource:scope=Namespaced,shortName=ct`
- 包级（doc.go）：`// +groupName=batch.tutorial.kubebuilder.io`

`controller-gen` 读 marker 通过 `filterTypesForCRDs` AST 过滤 + `FindKubeKinds` 找内嵌 TypeMeta+ObjectMeta 的类型，将 Go 类型展开为 CRD YAML。产物 `{group}_{plural}.yaml` 集中写到 `config/crd/bases/`。

证据：https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/main/pkg/crd/markers/validation.go

**对 GoCell 落地点：**
1. `gocell generate` 的 verify gate 改为 `git worktree add` 隔离沙箱模式（替换当前直接 `git diff`）
2. 引入 marker 体系：`// +gocell:contract:role=subscribe`、`// +gocell:slice:authoritativeData=true`，让 `gocell validate` AST-visitor 模式遍历替代当前 switch-case
3. 生成产物集中到 `generated/schemas/` 而非散落

---

### G. API 版本治理

**多版本存储**：CRD `spec.versions[]` 数组中只有一个标 `storage: true`（Hub），其他靠 conversion webhook 互转。`conversion-gen` 为每个版本生成 `zz_generated.conversion.go`，Hub 版本充当中间格式（star topology）。

**kube-api-linter**（golangci-lint plugin，30+ 规则）：
- `OptionalOrRequired`（默认启）：所有字段必须有 `// +optional` 或 `// +required`
- `JSONTags`（默认启）：JSON tag 必须 camelCase
- `NoNullable`（默认启）：禁 `+nullable`
- `SSATags`（默认启）：数组必须有 SSA 合并 key
- `DefaultOrRequired`（默认启）：required 字段不能有 default
- `CommentStart`（默认启）：注释必须以类型序列化名称开头（godoc 完整度守卫）

证据：https://raw.githubusercontent.com/kubernetes-sigs/kube-api-linter/main/docs/linters.md

**弃用政策**：Beta API 弃用后必须再支持 9 个月或 3 个 release（取长）；GA feature gate 在 GA 后 n+2 release 可移除；API 字段只增不删。

证据：https://kubernetes.io/docs/reference/using-api/deprecation-policy/

**对 GoCell 落地点：**
1. `contract.yaml` 加 `storageVersion: true` 标记，`gocell validate` ADV-06 扩展为多版本中有且仅有一个 storageVersion
2. 引入 kube-api-linter 作为 golangci-lint plugin，启 `OptionalOrRequired / JSONTags / CommentStart` 三条
3. 弃用窗口硬约束：`contract.yaml` 加 `deprecatedAt` (sprint 编号)，超过 3 sprint 仍 deprecated 且无 active 替代报 warning

---

### H. 文档自动化

**`gen-crd-api-reference-docs`**（`github.com/ahmetb/gen-crd-api-reference-docs`）：
- 用 `k8s.io/gengo` 解析 Go AST 抽 godoc 注释
- `html/template` 渲染，自定义函数 `typeDisplayName / linkForType / anchorIDForType`
- 产物 HTML（不是 Markdown）

证据：https://raw.githubusercontent.com/ahmetb/gen-crd-api-reference-docs/master/main.go

**kubebuilder literatec 模式**：mdbook 写 book，`hack/docs/generate.sh` 驱动 `go run hack/docs/generate_samples.go`。从 testdata 真实可构建项目按标注位置嵌入 book Markdown，CI `make verify-docs` `git diff` 守护。

证据：https://raw.githubusercontent.com/kubernetes-sigs/kubebuilder/master/hack/docs/generate.sh

**KEP 体系**：`keps/{sig-name}/{NNNN-feature-name}/README.md`，必填章节 Summary / Motivation / Proposal / Design Details / Test Plan / Graduation Criteria / Production Readiness Review。生命周期：implementable → alpha → beta → stable。需要 SIG lead approve + Production Readiness Review sign-off。

**krel release-notes**（`kubernetes/release` 项目）：通过 git graph traversal 遍历 PR，按 PR label 分类生成 Release Notes Draft，自动建 PR 到 `kubernetes/release` 和 `relnotes.k8s.io`。

**对 GoCell 落地点：**
1. `gen-crd-api-reference-docs` 直接套用 `kernel/metadata`：从 `CellMeta / SliceMeta / ContractMeta` Go struct godoc 自动生成 `docs/references/metadata-api.html`，替代当前手写的 `metadata-model-v3.md`
2. `examples/` 下加 `// +doc:snippet:begin/end` 标注 + `hack/docs/sync.sh`，CI `make verify-docs` git diff 守护
3. plan 文档加结构化 frontmatter（`status / stage / approvers / milestone`），轻量级 KEP

---

## §3 SoT 对标矩阵 — Temporal + fx（D DI/启动 / C log+metrics / J CLI/DX）

### D. DI / 启动生命周期

**fx 核心 API**（来自 `uber-go/fx/master/app.go` + `lifecycle.go` + `module.go`）：

```go
app := fx.New(
    fx.Module("infra", fx.Provide(NewDB, NewCache)),
    fx.Provide(NewService),
    fx.Invoke(func(lc fx.Lifecycle, svc *Service) {
        lc.Append(fx.Hook{
            OnStart: func(ctx context.Context) error { return svc.Start(ctx) },
            OnStop:  func(ctx context.Context) error { return svc.Stop(ctx) },
        })
    }),
)
```

关键能力：
- `Hook.OnStart` 失败时后续钩子不执行，且**只有已完成 OnStart 的钩子才触发 OnStop**（对称清理）
- `fx.Module` 产生 `dig.Scope`：子可见父 provide，父不可见子；`fx.Private` 进一步限制 provide 在模块内
- `fx.Annotated{Name / Group}` 命名实例和多实现注入

**Temporal 实际深度**：
- `common/resource/fx.go`：`Module`（持久化、动态配置、namespace registry、gRPC listener、health check、metrics reporter）+ `DefaultOptions`（SDK client factory、RPC factory、Archival provider）；用 `fx.In` struct 批量注入
- `service/history/fx.go`：30+ providers，顶部引 `resource.Module` 表示继承基础设施层；`ServiceLifetimeHooks(lc, svc)` 把 `Service.Start/Stop` 挂上去
- `cmd/server/main.go`：urfave/cli/v2 + 配置加载 → `log.NewZapLogger()` → 实例化 authorizer → claim mapper → `s.Start()`

证据：
- https://raw.githubusercontent.com/uber-go/fx/master/app.go
- https://raw.githubusercontent.com/temporalio/temporal/main/common/resource/fx.go
- https://raw.githubusercontent.com/temporalio/temporal/main/service/history/fx.go

**对 GoCell 落地点（吸收设计语义，不引入 fx 实现）：**

> **关键澄清**：CLAUDE.md「Cell 运行时 → Uber fx」是参考关系，目的是吸收设计模式。CLAUDE.md「领域逻辑保留自建」原则适用 —— DI/Lifecycle 编排是 Cell 模型的一部分，不应引入 `go.uber.org/fx` 包。GoCell 已有路径（Option pattern + 10-phase + archtest LAYER + boundary.yaml）self-consistent，直接引 fx 会形成双轨。

1. **吸收 Lifecycle 对称清理语义**：审查 `runtime/bootstrap/bootstrap.go` 的 `Lifecycle.Append` 实现，确认 phase 内多个 hook，第 N 个失败时前 N-1 个 OnStop 是否按 LIFO 触发；如缺，扩展自有实现（参考 fx `lifecycle.go` 算法但不依赖其包）+ 写 table-driven test 锁定矩阵
2. **不引入 `fx.Module("resource", ...)`**：GoCell 当前 adapter 通过显式 Option 注入 cell（编译期类型安全），优于 fx 的反射 + dig；adapters 共享通过 assembly.yaml + boundary.yaml 已有结构化表达
3. **不引入 `fx.Private`**：GoCell `cell.yaml allowedFiles` + `internal/` + archtest LAYER 三重守卫已比 fx.Private 单层更严

---

### C. 错误 + log + label

**Temporal log 接口**（`common/log/interface.go`）：

```go
type Logger interface {
    Debug(msg string, tags ...tag.Tag)
    Info(msg string, tags ...tag.Tag)
    ...
}
```

关键设计：**msg 必须是静态字符串，所有动态信息通过 `tag.Tag` 传入**（注释原文："msg should be static, do not use fmt.Sprintf() for msg. Anything dynamic should be tagged"）。msg 成为可聚合的日志指纹，不是自由文本。

tag 命名规范（`common/log/tag/tags.go`）：以 kebab-case 为主（`"wf-id" / "wf-run-id" / "cluster-name" / "blob-size"`），但有个别例外（`"hostId"`），说明规范靠 review 执行而非代码强制。**没有 cardinality 代码级限制**。

**Temporal metrics**（`common/metrics/defs.go`）：自研 wrapper（`NewTimerDef / NewCounterDef / NewBytesHistogramDef`）+ `globalRegistry`，`With(handler)` 绑定具体实现。**无 cardinality guard**。

**fx 错误 surface**：`app.Err()` 返回依赖图解析错误；`VisualizeError(err)` 把失败节点输出 DOT 图。

**对 GoCell 落地点（吸收设计，不引依赖）：**
1. 建 `pkg/logtag` 包仿 temporal `tag.Tag` 设计：`func CellID(id string) slog.Attr { return slog.String("cell-id", id) }` + CLAUDE.md 加规约「msg 必须字面量」+ `go/analysis` pass CI 检测（标准库实现，不引 temporal 包）
2. metrics label cardinality guard：GoCell 已有 metrics-schema.yaml + OBS-01 archtest，比 temporal 更严，**保留并扩展**为 dev 模式 panic-on-unknown-label（`AllowedValues []string`）
3. 不依赖 `fx.VisualizeError`：直接基于 `boundary.yaml` 实现 `gocell visualize` 子命令输出 DOT/SVG，验证失败附图作为 CI artifact

---

### J. CLI / DX

**Temporal CLI**（`cmd/server/main.go`）：urfave/cli/v2，三子命令 `validate-dynamic-config / render-config / start`。无 shell completion。

**fx DX**：`fx.Visualize(app, w)` 输出 DOT 依赖图；启动失败时 error 含断链 provider 名。

**对 GoCell 落地点（吸收设计，不引依赖）：**
1. 新增 `gocell visualize`：消费 `assemblies/<name>/generated/boundary.yaml`，用 stdlib `text/template` 渲染 DOT/SVG/mermaid（**不调用 `fx.Visualize`，不引 fx 包**）。开发者可 `gocell visualize --format=svg > graph.svg` 在 PR 描述中附图
2. cobra 替换手写 dispatch map（独立判断，与 fx 无关）：cobra `RegisterCompletion` 成本极低，`gocell validate --cell=<TAB>` 自动补全 cell id
3. `gocell validate --strict` 失败追加 `--dot` flag 输出 DOT 图作 CI artifact（基于 boundary.yaml + 验证错误信息合成，不依赖 fx）

---

## §4 SoT 对标矩阵 — CRDB + Vault（C 错误库 / B 测试 harness / E 安全供应链）

### C. 错误库设计

**cockroachdb/errors 核心 API**：

```go
func Wrap(err error, msg string) error
func Wrapf(err error, format string, args ...interface{}) error
func Mark(err error, reference error) error      // 给 err 打与 ref 同 marker
func WithSafeDetails(err error, format string, args ...interface{}) error
func Safe(v interface{}) SafeMessager             // 标记参数"可明文出现在报告中"
func GetSafeDetails(err error) SafeDetailPayload
func EncodeError(ctx, err) EncodedError           // protobuf 编码（跨网络）
func DecodeError(ctx, enc) error
func AssertionFailedf(format string, args ...) error  // 编程不变量违反
func HasAssertionFailure(err) bool
```

关键能力：
- **Mark 机制**：`Mark(err, ErrNotFound)` 跨类型断言保持语义标签，`errors.Is(err, ErrNotFound)` 即可匹配；底层 `errorMark` struct 存 family + extension，纯字符串比较，跨 protobuf 反序列化边界仍可用
- **redact/Safe PII 分层**：`WithSafeDetails(err, "user=%s acct=%s", redact.Safe(userID), password)` 中 password 被自动 redact，userID 因 `Safe()` 包裹明文出现在 Sentry/telemetry。`Error()` 始终不暴露 safe details，只 `%+v` / `GetSafeDetails()` / Sentry SDK 路径看到
- **AssertionFailedf** 区分编程 bug（assertion failure）与运行时操作错误，前者可被 `HasAssertionFailure(err)` 单独路由到 crash reporter

证据：
- https://github.com/cockroachdb/errors/blob/master/safedetails_api.go

**vault errutil**（`sdk/helper/errutil/error.go`）：

```go
type UserError struct{ Err string }      // 用户输入错误
type InternalError struct{ Err string }  // 内部错误
```

无错误码、无 Category、无 PII 脱敏。两类型 `errors.As` 区分，HTTP handler 自行判断 400 vs 500。Vault 复杂错误处理依赖 `hashicorp/go-multierror` 聚合。

**分类对比：**
| 项目 | 分类维度 | PII 标注 | 跨网络 |
|---|---|---|---|
| CRDB | Mark（语义标签） + safe/unsafe | 有（`Safe()` 包裹） | 有（protobuf 编解码） |
| Vault | 类型层级（User/Internal 二分） | 无 | 无 |
| GoCell（现状） | Code 枚举 + Category（Domain/Infra/Auth） | 部分（Message/InternalMessage 二分，但 Details map 无标注） | 不适用（无 RPC 层） |

**对 GoCell 落地点：**
1. `WithSafeDetails(err *Error, safe map[string]any)` variant 区分 audit-loggable 与 debug-only 字段（解决 Details map PII 泄漏风险）
2. Mark 机制**不引入**：场景不匹配（GoCell Cell 间通过 EventBus，error 不跨进程）
3. `NewAssertion(code Code, format string, args ...any) *Error` + `IsAssertion(err error) bool`：kernel/ 不变量检查走这个路径，HTTP handler 统一映射 500 + slog.Error("assertion failure")

---

### B. 测试 / E2E harness

**CRDB testcluster + serverutils**（`pkg/testutils/serverutils/test_server_shim.go` + `pkg/testutils/testcluster/testcluster.go`）：

```go
tc := testcluster.StartTestCluster(t, 3, base.TestClusterArgs{})
defer tc.Stopper().Stop(ctx)
db := tc.ServerConn(0)  // 连第 0 个节点
```

`TestServerInterface` 定义单节点完整接口（`Start / Stopper / SQLConn / DB / AdminURL / GetAuthenticatedHTTPClient`）。`TestCluster` 在进程内启多节点。`ParallelStart` 并行初始化；`Partitioner` 注入网络分区故障。全程在进程内，远快于容器。

**Vault TestCore + TestCluster**（`vault/testing.go`）：纯 in-memory，无容器：

```go
func TestCore(t testing.TB) *Core
func TestCoreUnsealed(t) (*Core, [][]byte, string)
func AddTestLogicalBackend(name string, factory logical.Factory)  // 全局 mock 注册
```

`testCoreConfig` 用 `physInmem.NewInmem` + noop audit + noop credential。Race detector 通过 `VAULT_CI_GO_TEST_RACE=1` + `gotestsum` 启用。**Vault 不用 goleak**。

**Fixtures**：CRDB 用 `datadriven` 驱动器 + golden file；Vault 用 `testify/assert` + 内联 fixture struct。

**对 GoCell 落地点：**
1. `cell.TestHarness` 接口规范化：CRDB `TestServerInterface` 模式封装 container lifecycle，避免每个 cell test 各自管理（GoCell 已有 testcontainers，接口对齐即可，不引 in-process）
2. Mock backend 全局注册：Vault `AddTestLogicalBackend` 模式让 cell 单元测试无须 docker —— L0/L1 用 in-memory adapter（`DiscardPublisher` 已有），扩展为完整 in-memory adapter 矩阵
3. Race detector 独立 job：跑 `go test -race ./kernel/... ./runtime/...`（Vault `VAULT_CI_GO_TEST_RACE` 模式 + gotestsum 扫 "WARNING: DATA RACE"）

---

### E. 安全 / 供应链

**CRDB CI**：
- govulncheck：未在 essential CI 中（CRDB 通过 EngFlow + Bazel 内嵌依赖扫描，不暴露为 Actions step）
- SBOM：未见 syft / cyclonedx-gomod
- Race：`github-actions-essential-ci.yml` 含 `race_canary` job
- Fuzzing：有 `code-cover-gen.yml` / `code-cover-publish.yaml`，native fuzz 不在公开 CI
- dependabot：404（Bazel 管理）

**Vault CI**：
- `security-scan.yml`：CodeQL + **Semgrep**（`semgrep==1.45.0`）静态分析，SARIF 上传 GitHub security dashboard
- `mend-pr-scan.yml`：**Mend (WhiteSource)** 依赖扫描，30 分钟 timeout，结果 90 天保留
- `test-go.yml`：Race + **FIPS 140-3**（`GOEXPERIMENT=boringcrypto`）+ `gotestsum`
- govulncheck / cosign：未在公开 CI 工作流
- dependabot：仅 `github-actions` ecosystem

**对 GoCell 落地点（极高优先）：**
1. **govulncheck 单独 step**（30s cost）：`go run golang.org/x/vuln/cmd/govulncheck@latest ./...`，标记 required check —— 两 SoT 公开 CI 都没显式做，但这是 Go 官方推荐工具，GoCell 规模小、依赖可控，落地零阻力
2. **Semgrep + CodeQL**（复制 Vault `security-scan.yml` skeleton）：Semgrep 用 `p/golang` 官方规则包，CodeQL 上传 SARIF。lint 管风格，Semgrep/CodeQL 管安全模式（SQL 注入、硬编码密钥、不安全随机数）
3. **Race detector 独立 job**：跑核心 `kernel/... runtime/...`（不全量），Vault `VAULT_CI_GO_TEST_RACE` 模式

---

## §5 横向交叉对照（GoCell × 3 SoT × 12 维度）

| 维度 | GoCell 现状 | K8s/kubebuilder | Temporal/fx | CRDB/Vault | 最值得借鉴的 |
|---|---|---|---|---|---|
| 1 codegen | 半成品（assembly+metrics-schema 两条线，diff gate 用 git diff） | 隔离沙箱 worktree + git status，marker 体系 | — | — | **K8s 隔离沙箱** |
| 2 测试 | 成熟（testcontainers + e2egate + goleak） | — | — | testcluster in-process / TestCore in-memory / 全局 mock 注册 | **Vault 全局 mock 注册（补 L0/L1）** |
| 3 错误观测 | 成熟（errcode + Category + InternalMessage + OBS-01 schema gate） | — | tag.Tag 静态 msg | cockroachdb/errors `Safe`/`AssertionFailedf` | **CRDB Safe + AssertionFailedf** + **temporal tag.Tag** |
| 4 启动 | 成熟（10-phase + LIFO） | — | fx 30+ providers + `lc.Append` 对称清理 | — | **吸收 Lifecycle 对称清理语义**（自建实现，不引 fx 包） |
| 5 DI | **成熟**（评级修正）：Option pattern + archtest LAYER + cell.yaml allowedFiles + boundary.yaml 是不同于 fx 的 self-consistent 路径 | — | fx.Module / fx.Private / Annotated | — | 不引 fx；按「领域逻辑保留自建」原则保留现路径 |
| 6 安全供应链 | 缺位（仅 dependabot + actions pin） | — | — | Vault：Semgrep + Mend + CodeQL + FIPS race | **Vault security-scan.yml + govulncheck**（直接引依赖：govulncheck/Semgrep/CodeQL 都是外部协议工具） |
| 7 发布 | 缺位 | krel release-notes（PR label → changelog） | — | — | krel 模式（push 到 GitHub Release） |
| 8 migration | 半成品（goose + invalid-index 检测） | — | — | — | 暂无（已有方案合理） |
| 9 API 治理 | 半成品（YAML contract + fingerprint diff gate） | storageVersion + kube-api-linter plugin + 9 个月弃用窗口 | — | — | **storageVersion 标记 + 弃用窗口**（吸收语义自建）+ kube-api-linter plugin 评估（如不适用 YAML，纯吸收设计） |
| 10 文档 | 缺位（手写 metadata-model-v3.md） | gen-crd-api-reference-docs + literatec + KEP | — | — | **gen-crd-api-reference-docs**（直接引：实现 godoc → HTML 是工具协议，自建无收益） |
| 11 性能 | 缺位 | — | — | — | （暂无上对标，可推迟） |
| 12 CLI/DX | 半成品（手写 dispatch） | — | fx.Visualize DOT 图 | — | **吸收依赖图可视化语义** → `gocell visualize` 自建（基于 boundary.yaml + text/template，不引 fx） |

### 「吸收设计」vs「引入实现」判断准则

CLAUDE.md 已有原则：「实现外部协议/标准必须优先使用官方或成熟开源库；实现 GoCell 领域逻辑保留自建」。本表中「最值得借鉴的」一列按此准则区分：

| 维度类型 | 处理方式 | 本表示例 |
|---|---|---|
| 外部协议 / 工具规范（govulncheck / Semgrep / CodeQL / gen-crd-api-reference-docs / cobra） | **直接引依赖** | 维度 6、10、12（cobra 部分） |
| Go 工具链共识（goimports / misspell / unconvert 等） | **直接引** | ci-governance 范围 |
| GoCell 领域语义（DI/Lifecycle 编排、Cell 模型、治理规则、错误码体系、metadata schema） | **吸收设计语义，自建实现** | 维度 4、5、9、11（性能基线设计自建）；维度 3 错误库、12 visualize |
| 与领域无关的纯工具（DOT 图渲染、changelog 生成） | 视成本决定，倾向引入 | 维度 7（git-cliff）、维度 12（DOT 用 stdlib text/template） |

**反模式示例（修正前 L3）**：把「Cell 运行时 → fx」对标关系误读为「采纳 fx.App 重构」。**正确做法**：吸收 Lifecycle 对称清理 + Module 隔离 + 依赖图可视化的设计语义，但实现保留 GoCell 自建（Option pattern + archtest + boundary.yaml）。

---

## §6 数据底稿引用清单

| Agent | 输出文件路径 | 调用次数 |
|---|---|---|
| GoCell 现状盘点 | `/private/tmp/claude-501/.../tasks/a4764fa4faa87be97.output` | 76 次 tool_use |
| K8s + kubebuilder | `/private/tmp/claude-501/.../tasks/abe41867cc75a6994.output` | 28 次 tool_use |
| Temporal + fx | `/private/tmp/claude-501/.../tasks/aa157c1b6f19f95c8.output` | 20 次 tool_use |
| CRDB + Vault | `/private/tmp/claude-501/.../tasks/a251ce3ff6389904d.output` | 42 次 tool_use |

每个维度结论的 raw URL 证据已嵌入正文。决策落点的优先级排序见 `202604300500-engineering-priority-decision.md`。
