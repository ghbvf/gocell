# GoCell 对标框架 CI 治理调研

> 日期：2026-04-30
> 任务：对 `docs/references/framework-comparison.md` 中明确列出的 6 个对标框架（uber-go/fx、go-zero、Kratos、go-micro、Watermill、Kubernetes）+ 13 个官方组件库进行 CI/lint/治理实践调研
> 调研者：explorer agent
> 关联文件：`../../backlog2.md`、`202604290858-backlog2-ci-governance-analysis.md`、`docs/references/framework-comparison.md`、`202604290945-ci-baseline-raw-extraction.md`（19 项目频次矩阵聚合）、`202604300430-golangci-tier12-priority-and-projection.md`（决策文档）
>
> **K8s 不重复调研**（已在 `202604290900-cncf-ci-rules-research.md` 覆盖）

---

## §1 5 框架 CI 矩阵

| 框架 | .golangci.yml 主要 linter | 自定义 verify | race CI | fuzz CI | 覆盖阈值 | contract 治理 | 最值得借鉴点 |
|---|---|---|---|---|---|---|---|
| **uber-go/fx** | govet(nilness/sortslice/unusedwrite)、errorlint、revive、nolintlint、goheader | `fxlint`（自定义分析器：验证所有 fx event 均被 Logger 处理）、tidy-lint（`go mod tidy -diff`） | `go test -race ./...`（全模块） | 无 | 无显式阈值；cover 跑 `-coverpkg=./...` | 无 | fxlint 自定义 analyzer 模式；goheader 授权头守护 |
| **zeromicro/go-zero** | 未找到独立 .golangci.yml；goctl 工具链使用 govet+staticcheck | `goctl api validate`（解析 + 校验 .api 文件，fail-fast 于无效输入） | `go test -race ./...` | 无 | 无 | goctl 自带 api/rpc 合法性校验 | 代码生成 `validate → parse → generate` 三段管道；`backupAndSweep` 增量保护 |
| **go-kratos/kratos** | bodyclose、durationcheck、errcheck、gocyclo(≤50)、govet(shadow)、lll(160)、misspell、revive、staticcheck、goimports | hack/tools.sh 委托 golangci-lint，5min timeout | `go test -race -coverprofile=profile.out -covermode=atomic` | 无 | 无显式阈值 | 无 | 中间件 `Chain` 倒序合成模式；kratos errors 的 `code+reason+metadata+GRPCStatus` 四维错误模型 |
| **micro/go-micro** | golangci-lint run（无自定义配置发现） | 无 | `go test -v -race ./...` | 无 | 无 | 无 | auth `AccountFromContext`/`ContextWithAccount` 的私有 key 类型隔离模式；Config `Watch(path...) (Watcher, error)` 热更新签名 |
| **ThreeDotsLabs/watermill** | 无独立 .golangci.yml（仅 Makefile fmt）；`test_race`、`test_stress`、`test_reconnect` | `test_stress`（-tags=stress, 30min）；`test_reconnect`（重连测试）；`validate_examples` | `go test ./... -short -race` | 无 | `PubSubConstructor` + `Features` 矩阵测试所有后端 | 多后端 `PubSubConstructor` 参数化测试；channel-based ready 信号（无 sleep）；Router 的 dual-WaitGroup + closedCh 双通道关闭 |

---

## §2 5 框架关键模式提取

### uber-go/fx

源码:
- https://github.com/uber-go/fx/blob/master/lifecycle.go
- https://github.com/uber-go/fx/blob/master/internal/lifecycle/lifecycle.go
- https://github.com/uber-go/fx/blob/master/tools/cmd/fxlint/main.go

**生命周期**: `OnStart` 顺序执行，首个失败立即 short-circuit；`OnStop` 逆序执行且 best-effort continue（`lifecycle.go:L275-281` 用 `multierr.Append` 收集，不中断）。GoCell 当前 `bootstrap/run_state.go` 的 rollback 失败仅 slog.Warn 丢弃（B2-R-03），应改为 `errors.Join(cause, rollbackErr)`。

**fxlint 自定义分析器**: `tools/cmd/fxlint/main.go` 用 `analysis.Analyzer` 验证所有 fx event 都有 Logger 覆盖。GoCell 可仿照此模式为 `kernel/governance/` 写 Go analysis pass，检查 `MustNew*` 在非 cmd/非 test 路径的出现（B2-K-02）。

关键片段:
```go
// internal/lifecycle/lifecycle.go — stop 错误收集
var errs []error
if err != nil {
    errs = append(errs, err) // continues after error
}
return multierr.Combine(errs...)
```

### zeromicro/go-zero (goctl)

源码:
- https://github.com/zeromicro/go-zero/blob/master/tools/goctl/api/gogen/gen.go
- https://github.com/zeromicro/go-zero/blob/master/core/service/servicegroup.go

**代码生成三段管道** (`gen.go:L82-88`): `parser.Parse(apiFile)` → `api.Validate()` → 按序调用 `genEtc`/`genConfig`/`genMain`。失败 fail-fast，不产生半截输出。GoCell 的 `cmd/gocell generate` 应参照此模式：先完整 validate cell.yaml + assembly.yaml，再生成，避免 `generate indexes — not implemented`（B2-X-05）。

**ServiceGroup** (`servicegroup.go:L52-88`): `Add` 倒序插入保证 stop 逆序；`doStart` 并发启动；`doStop` `sync.OnceFunc` 防重复停止。GoCell 的 Worker 管理可参照此 stop-once 模式。

### go-kratos/kratos

源码:
- https://github.com/go-kratos/kratos/blob/main/middleware/middleware.go
- https://github.com/go-kratos/kratos/blob/main/errors/errors.go
- https://github.com/go-kratos/kratos/blob/main/middleware/auth/jwt/jwt.go

**中间件链** (`middleware.go:L13-17`): `Chain` 倒序遍历 `for i := len(m)-1; i >= 0; i--`，调用顺序即参数左→右，语义清晰。GoCell `runtime/http/middleware` 如果执行顺序与声明顺序不一致，参照此修正。

**错误模型** (`errors/errors.go:L17-56`): `Error{Status, cause}` 携带 `code+reason+message+metadata`，`GRPCStatus()` 注入 `ErrorInfo`，`Is()` 比较 code+reason。比 GoCell `pkg/errcode` 多了 `reason` 维度（机器可读的错误分类）和 `metadata map`（上下文字段），可作为 B2-K-04 单源合并的参照模型。

**JWT middleware** (`jwt.go:L50,174`): 在 Options 中固定 `signingMethod: jwt.SigningMethodHS256`，运行时对比 `tokenInfo.Method != o.signingMethod` 拒绝算法替换。GoCell 应在构造期钉 RS256（B2-A-09 timing side-channel 修复之外）。

### micro/go-micro

源码:
- https://github.com/micro/go-micro/blob/master/config/config.go
- https://github.com/micro/go-micro/blob/master/auth/auth.go

**Config Watch** (`config.go:L25-28`): `Watch(path ...string) (Watcher, error)` 返回拉取式 watcher，`Next()` 阻塞等待变更。GoCell `runtime/config` 若做 hot-reload 应参照此签名，避免自定义轮询。

**Auth context key 隔离** (`auth.go:L138-147`): `accountKey` 是未导出的空结构体，`AccountFromContext`/`ContextWithAccount` 均通过私有类型防止外部 ctx value 碰撞。GoCell `runtime/auth/principal.go` 如有相似 ctx key 应对齐此模式。

### ThreeDotsLabs/watermill

源码:
- https://github.com/ThreeDotsLabs/watermill/blob/master/message/router.go
- https://github.com/ThreeDotsLabs/watermill/blob/master/pubsub/tests/test_pubsub.go
- https://github.com/ThreeDotsLabs/watermill/blob/master/components/cqrs/event_bus.go

**Router 双 WaitGroup 关闭** (`router.go:L135-142,401-420`): `handlersWg`（handler 生命周期）+ `runningHandlersWg`（inflight 消息）双 WG，timeout 通过 dual-goroutine 竞争实现，超时返回 `"router close timeout"`。GoCell `adapters/rabbitmq/subscriber.go` B2-A-14 StopIntake 三段关闭应参照此模式。

**多后端参数化测试** (`test_pubsub.go:L42-44`): `PubSubConstructor func(t *testing.T) (Publisher, Subscriber)` + `Features` 能力标志矩阵，每个后端 adapter 用相同测试套件跑。GoCell `adapters/rabbitmq/conformance_test.go` B2-A-17 应仿照此结构，构造 RMQ + in-memory 两版本跑同一测试。

**channel-based 就绪信号** (`test_pubsub.go:L759-776`): `allMessagesSent := make(chan struct{})` + `close(allMessagesSent)` + `<-allMessagesSent` 替代 sleep。GoCell `cmd/corebundle/outbox_e2e_integration_test.go:L169` B2-X-01 的 50ms sleep 应改为此模式。

---

## §3 13 组件库简表

| 库 | backlog2 对应条目 | CI 关键点 | 借鉴优先级 |
|---|---|---|---|
| **jackc/pgx/v5** | B2-A-04/06/07/08/11 | `New()/NewWithConfig()` 全 error-first；`ParseConfig` 强制 createdByParseConfig 标志；无 MustNew，panic 仅用于 invalid config origin（pool.go:L327） | 高 |
| **redis/go-redis/v9** | B2-A-26/27/28/29/30/31 | Options struct 模式；无内置 key namespace；TLS 单独注入；`-race` 测试 | 高 |
| **rabbitmq/amqp091-go** | B2-A-14/15/16/17/18 | build tag `integration` 分离测试；golangci-lint modernize；禁 testpackage/godox/gochecknoinits | 高 |
| **go-chi/chi/v5** | B2-T-04 中间件顺序 | 私有类型 contextKey 防 ctx value 碰撞；middleware 注册顺序即执行顺序（无倒序） | 中 |
| **golang-jwt/jwt/v5** | B2-A-09 JWT timing | `WithValidMethods` 白名单强制；`VerificationKeySet` 多 kid 支持；`ErrTokenSignatureInvalid` 等类型化错误 | 高 |
| **coreos/go-oidc/v3** | B2-A-09 JWKS 刷新 | `remoteKeySet()` mutex 懒初始化；`NewProvider` 两路（配置/Discovery）全 error-first | 中 |
| **aws/aws-sdk-go-v2** | B2-A-18 超时 | `LoadDefaultConfig(ctx)` ctx 贯穿全链；resolver chain fail-fast；超时在调用方 ctx 层管理 | 中 |
| **nhooyr.io/websocket** | B2-W-01/02 | `AcceptOptions.InsecureSkipVerify` 默认 false；cross-origin 默认 reject；`authenticateOrigin` 独立函数易测试 | 高 |
| **open-telemetry/opentelemetry-go** | B2-R-05/06/07 | `NewTracerProvider` 接受 `SpanExporter`（安全由 exporter 层决定，非 Provider）；`Shutdown(ctx)` 有 flush；全局注册须显式 `otel.SetTracerProvider` | 高 |
| **prometheus/client_golang** | B2-A-22/24/25 | `NewRegistry()` 隔离注册；`SafeMultiError` mutex-safe 多错收集；`MustRegister` panic 仅用顶层；goroutine budget bounded gather | 高 |
| **pressly/goose/v3** | B2-A-10 | `GO_TEST_FLAGS=-race -count=1`；`test-postgres-long` 长测单独 target；`golangci-lint run --fix`；多 DB backend matrix | 高 |
| **testcontainers/testcontainers-go** | B2-C-04/13 | `gci` import 分组；`testifylint`；`context-as-argument` 允许 `*testing.T` 先于 ctx（B2-C-04 PG 失败注入测试需参照） | 高 |
| **fsnotify/fsnotify** | B2-R-01（文件监听） | `errorlint`；`revive` var-naming；`prealloc`；`testifylint` | 低 |

---

## §4 backlog2 条目精准映射

### B2-K-02 Must* panic 残留
- **对标**: fx `tools/cmd/fxlint/main.go`（analysis.Analyzer 模式）+ `app.go:L801`（defer error）
- **做法**: fxlint 用 `go/analysis` pass 在编译期扫描非法使用；fx 自身 composition root 用 `app.err` defer 模式而非 panic
- **GoCell 改法**: 在 `kernel/governance/` 新增 `must_panic_guard` analyzer（或 grep CI rule），扫描 `cells/`/`runtime/`/`adapters/` 中 `Must*` 调用，仅允许 `cmd/`+`*_test.go`
- **PR**: PR-B2-B1（KERNEL-WRAPPER-ERRSEMANTIC）

### B2-K-03 AssemblyRef 隐式断言
- **对标**: fx `module.go` + `provide.go:L105-135`（Annotate 类型注册模式）；fx 的 `di.Container` 通过接口而非 type assertion 解耦
- **做法**: fx 从不在运行时 type-assert 容器内对象，所有类型注册走 `Provide`/`Annotate`；watermill Router 通过显式 `AddHandler` 接口注入 Publisher/Subscriber
- **GoCell 改法**: 将 `assemblyWithCell` 上提为 `kernel/cell.AssemblyRef` 显式接口；archtest 禁止 `runtime/bootstrap` 对 kernel 类型做 type assertion
- **PR**: PR-B2-B2（KERNEL-INTERFACE-LIFT）

### B2-A-08/11 PG 构造器风格
- **对标**: pgx `pgxpool/pool.go:L309-398`（全 error-first，`createdByParseConfig` 强制 panic 仅用于非法 config origin，无 MustNew）
- **做法**: pgx 只有 `New`/`NewWithConfig`/`ParseConfig` 三个 error-first 构造器；panic 场景只有 `ParseConfig` 被绕过这一种
- **GoCell 改法**: 审计 `adapters/postgres/` 所有 `MustNew*`/裸 panic，保留唯一例外：cmd 顶层 wrapper；所有 `New*` 返回 error；archtest 锁定
- **PR**: PR-B2-C2（PG-REFRESH-AND-READYZ）

### B2-A-19 OTel Insecure plaintext
- **对标**: opentelemetry-go `sdk/trace/provider.go:L93-125`（Provider 自身无 TLS 配置；exporter 层决定安全策略）
- **做法**: OTel 社区的规范是 exporter 构造时显式选择 `grpc.WithTransportCredentials` 或 `insecure.NewCredentials()`，无隐式默认
- **GoCell 改法**: `adapters/otel/span.go` 构造期校验 `Insecure=false` 或要求显式 `WithUnsafePlaintext()`；attribute 值加 redaction hook
- **PR**: PR-B2-B3（OTEL-PROVIDER-LIFECYCLE）

### B2-A-22/24 Prometheus race 测试
- **对标**: prometheus/client_golang `registry.go:L365-483`（`SafeMultiError` + bounded goroutine pool；goroutine 内 Gather 全 mutex-safe）；`counter.go:L133-140`（`atomic.AddUint64` hot path）
- **做法**: client_golang 内部用 bounded goroutine pool + channel select nil-out 模式；测试通过 `NewPedanticRegistry()` 开启严格检查
- **GoCell 改法**: `adapters/prometheus/metric_provider_test.go` 加 `t.Parallel()` + 多 goroutine 并发 Record，CI 跑 `-race`；用 `NewPedanticRegistry()` 做测试注册
- **PR**: PR-B2-C4（PROMETHEUS-AND-S3-TEST）

### B2-A-27 Redis multi-tenant key
- **对标**: go-redis `options.go`（无内置 namespace 机制，需调用方用 `KeyPrefix` 自行封装）；pgx `pool.go` config 模式
- **做法**: go-redis 无内置 namespace；标准做法是在 adapter 层封装 `KeyNamespace string` 字段，所有 key 拼前缀
- **GoCell 改法**: `adapters/redis/` 构造器加 `KeyNamespace string` 必填字段（`WithUnsafeNoNamespace()` 显式 opt-out）；archtest 守护 cell 级隔离
- **PR**: PR-B2-C5（REDIS-MULTITENANT）

### B2-W-01/02 WebSocket auth/ACL
- **对标**: nhooyr.io/websocket `accept.go:L22-62`（`AcceptOptions.InsecureSkipVerify` 默认 false；`authenticateOrigin` 独立函数；cross-origin 默认 reject）
- **做法**: websocket 在 `Accept()` 阶段（HTTP upgrade 前）就做 origin 校验；auth 校验由调用方注入；`InsecureSkipVerify` 必须显式声明
- **GoCell 改法**: `adapters/websocket/handler.go:L51-65` 的 `UpgradeHandler` 构造期注入 `Authenticator` 接口；空值 fail-fast；hub 的 `Broadcast` 加 `func(conn) bool` filter
- **PR**: PR-B2-A1（WEBSOCKET-AUTH-ARCH-ADR）

### B2-T-04 contract 命名 camelCase
- **对标**: go-kratos `api/` proto 命名规范 + `.golangci.yml` `revive` 标准命名规则；go-zero `goctl api validate`（parse 阶段报错驼峰/蛇形混用）
- **做法**: kratos 通过 revive 的 `var-naming` 规则检测；go-zero 在 goctl parse 阶段校验 .api 文件命名风格
- **GoCell 改法**: `gocell validate` 加 naming-convention rule，扫描 contract.yaml 的 path param / payload 字段名必须 camelCase（对齐 CLAUDE.md）；一次性脚本修正现有 37 处漂移
- **PR**: PR-B2-E4（CONTRACT-DRIFT-FIX）

### B2-T-08 contract.responses ↔ handler
- **对标**: go-zero `goctl api validate`（`gen.go:L82-88`）；go-kratos errors generator（code+reason codegen 模式）
- **做法**: go-zero 在生成前验证 API 定义完整性；kratos errors generator 从 proto errors.proto 生成 Go error 常量，单源消除漂移
- **GoCell 改法**: `gocell validate` 加 ADV-09 规则，校验 handler 返回的 errcode 均在 `contract.yaml responses` 中有对应声明；errors generator 参照 kratos 模式从 errcode 包生成 contract responses stub
- **PR**: PR-B2-E4 或新 PR-B2-B2 扩展

### B2-X-01 test sleep
- **对标**: watermill `test_pubsub.go:L759-776`（channel-based 就绪信号）；fx test 模式（无 sleep，用 `lifecycle.Start(ctx)` 同步等待）
- **做法**: watermill 用 `allMessagesSent := make(chan struct{})` + `close()` + receive 替代 sleep；fx 所有测试通过 `fxtest.New` + `lifecycle.Start` 同步
- **GoCell 改法**: `outbox_e2e_integration_test.go:L169` 改用 `EventRouter.WaitSubscribed(topics, timeout)` 或 channel 就绪信号；CI 加 `-count=1` 防止 flake 掩盖
- **PR**: PR-B2-E3（CMD-COMPOSITION-CLEANUP）

### outbox 模式整体
- **对标**: watermill `message/router.go:L135-420`（dual WaitGroup + closingInProgressCh + closedCh 双通道）
- **做法**: handler goroutine 独立，`handlersWg` 跟踪生命周期，`runningHandlersWg` 跟踪 inflight；关闭时先停止接收，等 inflight 排空，超时 fail
- **GoCell 改法**: `adapters/rabbitmq/subscriber.go:L914` StopIntake 实现三段（停拉新 → 排空 prefetch → 等 inflight ack），timeout 参照 router close timeout 模式
- **PR**: PR-B2-C3（RMQ-LIFECYCLE）

### 中间件顺序
- **对标**: kratos `middleware/middleware.go:L13-17`（倒序遍历确保参数顺序即执行顺序）
- **做法**: `for i := len(m)-1; i >= 0; i--` 构建 next 链，最外层 middleware 是参数列表第一个
- **GoCell 改法**: `runtime/http/middleware/` 链组合逻辑需与此对齐，并在单测中 assert 顺序；CLAUDE.md 中间件文档补充顺序语义
- **PR**: 可并入 PR-B2-B2 或独立 PR

---

## §5 与 CLAUDE.md "对标对比规则" 集成

### 当前状态

CLAUDE.md 的"对标对比规则"要求新建/重构模块时先 WebFetch 对标源码，但只列了源码文件路径，未覆盖"CI/lint 配置"维度。

### 建议落地方式

**1. 在 `docs/references/framework-comparison.md` 增加 "CI 治理" 列**

在每个模块的对标映射块（`primary`/`secondary`/`goal` 三行）后新增第四行：

```
ci-ref: uber-go/fx → .golangci.yml(goheader/errorlint/nilness), tools/cmd/fxlint/main.go
```

这样 ship/fix skill 在查 framework-comparison.md 时自然包含 CI 参考。

**2. 新增 `.claude/rules/gocell/ci-governance-references.md`**

独立文件列出：每条 backlog2 B2-* 条目 → 对标框架文件 → 治理方式（archtest/grep/lint rule）的三列映射，供 ship skill 自动引用。这样新 PR 生成 checklist 时可以自动包含"是否已有对应 governance rule"检查项。

**3. CLAUDE.md "对标对比规则" 第 3 条扩展**

在"3. 提取接口签名、生命周期钩子、错误处理等关键设计决策"后加：

```
3b. 同时提取 .golangci.yml / Makefile 的 CI 守护模式（race/fuzz/自定义 analyzer），
    对应 kernel/governance/ 的 archtest 规则覆盖点
```

**4. 不建议在 ship skill 中自动触发 WebFetch CI 文件**，因为每次都拉取 5 个框架的 CI 文件耗时过高；改为在首次建立 governance rule 时一次性查，结果沉淀到 ci-governance-references.md。

---

## §6 落地清单（最高 ROI 直接照搬项）

### 1. 直接照搬 watermill `test_pubsub.go` 的 PubSubConstructor 模式到 `adapters/rabbitmq/conformance_test.go`

将 `PubSubConstructor func(t *testing.T) (Publisher, Subscriber)` + `Features` 矩阵测试结构照搬，RMQ + in-memory 两版本跑同一套 Ack/Requeue/Reject 三分支测试（B2-A-17）。零架构改动，纯测试文件添加。

源码位置: https://github.com/ThreeDotsLabs/watermill/blob/master/pubsub/tests/test_pubsub.go

### 2. 直接照搬 fx `.golangci.yml` 的 `govet nilness+sortslice+unusedwrite` 配置

fx `.golangci.yml` 的 govet 额外 check 列表（nilness/reflectvaluecompare/sortslice/unusedwrite）直接合并到 GoCell `.golangci.yml`。覆盖 B2-K-02（nilness 可捕获 nil receiver 场景）和 B2-A-11（unusedwrite 检测构造器未使用的 error 变量）。

源码位置: https://github.com/uber-go/fx/blob/master/.golangci.yml

### 3. 直接照搬 pressly/goose 的 Makefile race + count=1 + timeout 组合

`GO_TEST_FLAGS ?= -race -count=1 -v -timeout=5m -json` + `tparse` 格式化输出，照搬到 GoCell 的 CI Makefile。`-count=1` 防止测试缓存掩盖竞争，直接解决 B2-A-24/B2-A-29 的 race 测试缺失。

源码位置: https://github.com/pressly/goose/blob/master/Makefile

### 4. 直接照搬 nhooyr.io/websocket 的 `authenticateOrigin` 独立函数模式

`authenticateOrigin(r *http.Request, patterns []string) error` 独立纯函数（无状态），易于单测。照搬此模式重构 `adapters/websocket/handler.go` 的 upgrade 校验逻辑，直接关闭 B2-W-01。

源码位置: https://github.com/nhooyr/websocket/blob/master/accept.go（L216-242）

### 5. 直接照搬 kratos `errors.go` 的 `reason` 字段 + `Is(code+reason)` 双维比较模式

kratos `errors/errors.go:L30-35` 的 `Is()` 同时比较 code 和 reason，防止不同模块同 code 误命中。照搬此双维 `Is()` 到 `pkg/errcode`，同时在 error struct 加 `reason string`（机器可读错误分类，如 `CONFIG_NOT_FOUND`），单源消除 B2-K-04 的 `errcodeNameToStatus` 手工镜像漂移。

源码位置: https://github.com/go-kratos/kratos/blob/main/errors/errors.go（L17-56）
